package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/auth/oidc"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

// LinkOidc starts linking the caller's own account: the browser goes to the
// identity provider and the callback links the identity it comes back with.
func (a *server) LinkOidc(ctx context.Context, _ oapi.LinkOidcRequestObject) (oapi.LinkOidcResponseObject, error) {
	w, r := httpFrom(ctx)
	u, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if !a.oidcEnabled() {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if u.OIDCLinked {
		return nil, &apiError{status: http.StatusConflict, detail: "already linked"}
	}
	loc, err := a.beginFlow(ctx, w, r, oidc.ModeLink, u.ID)
	if err != nil {
		return nil, err
	}
	return oapi.LinkOidc200JSONResponse(oapi.Redirect{Url: loc}), nil
}

// oidcLink finishes a profile link. It needs the session of the user who
// started the flow, so a link flow cannot be completed in another browser or
// by another user. The email is not consulted: the user proved both
// identities, and an unverified address links nothing it was not asked to.
func (a *server) oidcLink(ctx context.Context, flow oidc.Flow, id oidc.Identity) oapi.OidcCallbackResponseObject {
	u, ok := UserFrom(ctx)
	if !ok || u.ID != flow.LinkUserID {
		return oidcFailure(oidc.ModeLink, errExpired)
	}
	code, err := a.linkIdentity(ctx, u.ID, u.Username, id)
	if err != nil {
		a.log.Error("oidc link", "err", err)
		return oidcFailure(oidc.ModeLink, errFailed)
	}
	if code != "" {
		return oidcFailure(oidc.ModeLink, code)
	}
	return oidcRedirect("/profile?oidc=linked")
}

// linkIdentity links id to the user in one transaction. A non-empty code is
// the refusal; the identity must not belong to anyone yet.
func (a *server) linkIdentity(ctx context.Context, userID int64, username string, id oidc.Identity) (string, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	issuer := sql.NullString{String: id.Issuer, Valid: true}
	subject := sql.NullString{String: id.Subject, Valid: true}
	switch _, err := q.GetUserByOIDC(ctx, dbq.GetUserByOIDCParams{Issuer: issuer, Subject: subject}); {
	case err == nil:
		return errAlreadyLinked, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	n, err := q.LinkOIDC(ctx, dbq.LinkOIDCParams{
		Issuer: issuer, Subject: subject, At: sql.NullInt64{Int64: a.now().Unix(), Valid: true}, ID: userID,
	})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return errAlreadyLinked, nil
	}
	err = audit.Write(ctx, q, audit.Entry{
		ActorID: &userID, Action: "oidc.linked", Target: username, Details: map[string]string{"method": "profile", "issuer": id.Issuer, "subject": id.Subject}, At: a.now(),
	})
	if err != nil {
		return "", err
	}
	return "", tx.Commit()
}
