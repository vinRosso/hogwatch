package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/auth/oidc"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

// validSubject accepts what an identity provider could have sent: a non-empty
// string of at most oidc.MaxSubject bytes with no surrounding space or control
// characters, so a pasted subject with a stray newline cannot link silently to
// an identity that never matches.
func validSubject(s string) bool {
	if s == "" || len(s) > oidc.MaxSubject || s != strings.TrimSpace(s) {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

// LinkUserOidc links a user to an identity-provider subject of the configured
// issuer (the admin's way past rule 3). It never relinks: a user who already
// has a link, or a subject another user holds, is a 409.
func (a *server) LinkUserOidc(ctx context.Context, req oapi.LinkUserOidcRequestObject) (oapi.LinkUserOidcResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if !a.oidcEnabled() {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if req.Body == nil || !validSubject(req.Body.Subject) {
		return nil, &apiError{status: http.StatusBadRequest, detail: "subject must be 1 to 256 bytes, without surrounding space or control characters"}
	}
	issuer := sql.NullString{String: a.cfg.Auth.OIDC.Issuer, Valid: true}
	subject := sql.NullString{String: req.Body.Subject, Valid: true}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)

	target, err := q.GetUserByID(ctx, int64(req.Id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return nil, err
	}
	switch _, err := q.GetUserByOIDC(ctx, dbq.GetUserByOIDCParams{Issuer: issuer, Subject: subject}); {
	case err == nil:
		return nil, &apiError{status: http.StatusConflict, detail: "that subject is linked to a user"}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	n, err := q.LinkOIDC(ctx, dbq.LinkOIDCParams{
		Issuer: issuer, Subject: subject, At: sql.NullInt64{Int64: a.now().Unix(), Valid: true}, ID: target.ID,
	})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, &apiError{status: http.StatusConflict, detail: "the user is already linked"}
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "oidc.linked", Target: target.Username,
		Details: map[string]string{"method": "admin", "subject": req.Body.Subject}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.LinkUserOidc204Response{}, nil
}

// UnlinkUserOidc removes a user's identity-provider link. It is a role change:
// admin_oidc is cleared and every session of the user is revoked. It works
// with OIDC switched off, so a leftover link can always be removed.
func (a *server) UnlinkUserOidc(ctx context.Context, req oapi.UnlinkUserOidcRequestObject) (oapi.UnlinkUserOidcResponseObject, error) {
	admin, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)

	target, err := q.GetUserByID(ctx, int64(req.Id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if err != nil {
		return nil, err
	}
	old, err := q.GetOIDCLink(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	n, err := q.UnlinkOIDC(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, &apiError{status: http.StatusConflict, detail: "the user is not linked"}
	}
	if _, err := q.DeleteUserSessions(ctx, target.ID); err != nil {
		return nil, err
	}
	// Unlinking clears admin_oidc, so it must not remove the last administrator.
	if err := a.requireAdminLeft(ctx, q); err != nil {
		return nil, err
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &admin.ID, Action: "oidc.unlinked", Target: target.Username,
		Details: map[string]string{"issuer": old.OidcIssuer.String, "subject": old.OidcSubject.String}, At: a.now(),
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return oapi.UnlinkUserOidc204Response{}, nil
}
