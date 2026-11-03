package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

func dbqLastLogin(id, ts int64) dbq.SetLastLoginParams {
	return dbq.SetLastLoginParams{Ts: sql.NullInt64{Int64: ts, Valid: true}, ID: id}
}

func (a *server) GetMe(ctx context.Context, _ oapi.GetMeRequestObject) (oapi.GetMeResponseObject, error) {
	u, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	return oapi.GetMe200JSONResponse(meOf(u)), nil
}

func (a *server) ChangePassword(ctx context.Context, req oapi.ChangePasswordRequestObject) (oapi.ChangePasswordResponseObject, error) {
	w, r := httpFrom(ctx)
	u, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if req.Body == nil {
		return nil, &apiError{status: http.StatusBadRequest, detail: "invalid request"}
	}
	key := "pw:" + itoa(u.ID)
	if wait, allowed := a.limiter.Allow(key); !allowed {
		return nil, &apiError{status: http.StatusTooManyRequests, retryAfter: wait}
	}
	row, err := a.q.GetUserByID(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if !row.PasswordHash.Valid {
		local.DummyVerify(req.Body.Current)
		return nil, &apiError{status: http.StatusConflict, detail: "account has no local password"}
	}
	// Not 401: the caller is logged in, and a client may treat 401 as an expired session.
	wrong := &apiError{status: http.StatusUnprocessableEntity, detail: "current password is wrong"}
	if len(req.Body.Current) > local.MaxPasswordBytes {
		return nil, wrong
	}
	if good, _ := local.Verify(row.PasswordHash.String, req.Body.Current); !good {
		return nil, wrong
	}
	a.limiter.Succeed(key)
	if err := a.checkNewPassword(req.Body.New); err != nil {
		return nil, err
	}
	hash, err := local.Hash(req.Body.New)
	if err != nil {
		return nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	tq := a.q.WithTx(tx)
	if _, err := tq.SetPassword(ctx, dbq.SetPasswordParams{PasswordHash: sql.NullString{String: hash, Valid: true}, ID: u.ID}); err != nil {
		return nil, err
	}
	if _, err := tq.DeleteUserSessions(ctx, u.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// The replacement is a plain session even for an OIDC admin: restarting the
	// 12 h cap here would let a password change extend it. The next OIDC login
	// restores the admin session.
	if _, err := a.startSession(ctx, w, r, u.ID, false); err != nil {
		return nil, err
	}
	if err := audit.Write(ctx, a.q, audit.Entry{ActorID: &u.ID, Action: "auth.password_change", At: a.now()}); err != nil {
		a.log.Error("audit", "err", err)
	}
	return oapi.ChangePassword204Response{}, nil
}

// prefsOf reads the stored document. Keys the API no longer knows are ignored, and a
// document that does not parse reads as the defaults rather than failing the profile.
func prefsOf(doc string) oapi.Prefs {
	var p oapi.Prefs
	if err := json.Unmarshal([]byte(doc), &p); err != nil {
		return oapi.Prefs{}
	}
	return p
}

func (a *server) SetMyPrefs(ctx context.Context, req oapi.SetMyPrefsRequestObject) (oapi.SetMyPrefsResponseObject, error) {
	u, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if req.Body == nil {
		return nil, &apiError{status: http.StatusBadRequest, detail: "invalid request"}
	}
	p := *req.Body
	bad := func(field string) error {
		return &apiError{status: http.StatusBadRequest, detail: "invalid " + field}
	}
	switch {
	case p.Depth != nil && (*p.Depth < 1 || *p.Depth > 5):
		return nil, bad("depth")
	case p.PanelWidth != nil && (*p.PanelWidth < 320 || *p.PanelWidth > 900):
		return nil, bad("panel_width")
	case p.RootId != nil && *p.RootId < 1:
		return nil, bad("root_id")
	case p.HistoryWindow != nil && !p.HistoryWindow.Valid():
		return nil, bad("history_window")
	case p.UsageWindow != nil && !p.UsageWindow.Valid():
		return nil, bad("usage_window")
	case p.AdminWindow != nil && !p.AdminWindow.Valid():
		return nil, bad("admin_window")
	}
	// Only the validated, known keys reach the store.
	patch, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if _, err := a.q.MergeUserPrefs(ctx, dbq.MergeUserPrefsParams{Patch: string(patch), ID: u.ID}); err != nil {
		return nil, err
	}
	doc, err := a.q.GetUserPrefs(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return oapi.SetMyPrefs200JSONResponse(prefsOf(doc)), nil
}

func (a *server) DismissNotice(ctx context.Context, _ oapi.DismissNoticeRequestObject) (oapi.DismissNoticeResponseObject, error) {
	u, ok := UserFrom(ctx)
	if !ok {
		return nil, &apiError{status: http.StatusUnauthorized}
	}
	if err := a.q.ClearNotice(ctx, u.ID); err != nil {
		return nil, err
	}
	return oapi.DismissNotice204Response{}, nil
}
