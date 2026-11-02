package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func (a *server) GetSetupStatus(ctx context.Context, _ oapi.GetSetupStatusRequestObject) (oapi.GetSetupStatusResponseObject, error) {
	n, err := a.q.CountEnabledAdmins(ctx)
	if err != nil {
		return nil, err
	}
	return oapi.GetSetupStatus200JSONResponse(oapi.SetupStatus{Needed: n == 0}), nil
}

func (a *server) Setup(ctx context.Context, req oapi.SetupRequestObject) (oapi.SetupResponseObject, error) {
	w, r := httpFrom(ctx)
	if req.Body == nil {
		return nil, &apiError{status: http.StatusBadRequest, detail: "invalid request"}
	}
	key := "setup:" + clientIP(r, a.trusted).String()
	if wait, ok := a.limiter.Allow(key); !ok {
		return nil, &apiError{status: http.StatusTooManyRequests, retryAfter: wait}
	}
	if n, err := a.q.CountEnabledAdmins(ctx); err != nil {
		return nil, err
	} else if n > 0 {
		return nil, &apiError{status: http.StatusNotFound}
	}
	if !a.setup.Check(req.Body.Token) {
		return nil, &apiError{status: http.StatusUnauthorized, detail: "invalid setup token"}
	}
	a.limiter.Succeed(key)
	name := normUsername(req.Body.Username)
	if !local.ValidUsername(name) {
		return nil, &apiError{status: http.StatusBadRequest, detail: "invalid username"}
	}
	if err := a.checkNewPassword(req.Body.Password); err != nil {
		return nil, err
	}
	hash, err := local.Hash(req.Body.Password)
	if err != nil {
		return nil, err
	}
	display := name
	if req.Body.DisplayName != nil && *req.Body.DisplayName != "" {
		display = *req.Body.DisplayName
	}
	var email sql.NullString
	if req.Body.Email != nil && *req.Body.Email != "" {
		email = sql.NullString{String: *req.Body.Email, Valid: true}
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	tq := a.q.WithTx(tx)
	id, err := tq.InsertAdminIfNone(ctx, dbq.InsertAdminIfNoneParams{
		Username: name, DisplayName: display, Email: email, PasswordHash: hash, CreatedAt: a.now().Unix(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apiError{status: http.StatusNotFound}
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_CONSTRAINT {
		return nil, &apiError{status: http.StatusConflict, detail: "username already exists"}
	}
	if err != nil {
		return nil, err
	}
	if err := audit.Write(ctx, tq, audit.Entry{ActorID: &id, Action: "setup.admin_created", Target: name, At: a.now()}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.setup.Invalidate()
	if !a.localAllowed(name, true) {
		a.log.Warn("first admin created while local login is off and not listed as break-glass; it cannot log in again", "username", name)
	}
	u, err := a.startSession(ctx, w, r, id, false)
	if err != nil {
		return nil, err
	}
	return oapi.Setup200JSONResponse(meOf(u)), nil
}
