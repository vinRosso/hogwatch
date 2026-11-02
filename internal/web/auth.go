package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"unicode/utf8"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/auth/local"
	"github.com/vinRosso/hogwatch/internal/auth/session"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

type httpKey struct{}

type httpPair struct {
	w http.ResponseWriter
	r *http.Request
}

// withHTTP is the strict middleware that hands handlers the raw response
// writer and request, which only the cookie-setting operations use.
func withHTTP(f oapi.StrictHandlerFunc, _ string) oapi.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
		return f(context.WithValue(ctx, httpKey{}, httpPair{w, r}), w, r, req)
	}
}

func httpFrom(ctx context.Context) (http.ResponseWriter, *http.Request) {
	p := ctx.Value(httpKey{}).(httpPair)
	return p.w, p.r
}

// errBadLogin is the one failure every kind of bad login shares, so a
// response never tells an unknown user from a wrong password.
func errBadLogin() error {
	return &apiError{status: http.StatusUnauthorized, detail: "invalid credentials"}
}

func normUsername(s string) string { return local.NormalizeUsername(s) }

// localAllowed reports whether the account may use a password at all: local
// auth is on, or the account is an admin listed as break-glass.
func (a *server) localAllowed(username string, adminLocal bool) bool {
	if a.cfg.Auth.Local.Enabled {
		return true
	}
	return adminLocal && slices.ContainsFunc(a.cfg.Auth.Local.BreakGlassAdmins, func(s string) bool {
		return normUsername(s) == username
	})
}

func (a *server) checkNewPassword(pw string) error {
	if len(pw) > local.MaxPasswordBytes {
		return &apiError{status: http.StatusBadRequest, detail: "password too long"}
	}
	if utf8.RuneCountInString(pw) < a.cfg.Auth.Local.MinPasswordLen {
		return &apiError{status: http.StatusBadRequest, detail: "password too short"}
	}
	return nil
}

func meOf(u session.User) oapi.Me {
	return oapi.Me{
		Id: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email,
		IsAdmin: u.IsAdmin(), AdminLocal: u.AdminLocal, AdminOidc: u.AdminOIDC,
		CanListFiles: u.CanListFiles, HasPassword: u.HasPassword, OidcLinked: u.OIDCLinked,
		Notice: u.Notice, Prefs: prefsOf(u.Prefs),
	}
}

// startSession creates a session for the user, sets its cookie and returns the
// user as the session sees them.
//
// oidcAdmin marks a session whose admin right comes from an OIDC login; it
// gets the short absolute lifetime.
func (a *server) startSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int64, oidcAdmin bool) (session.User, error) {
	token, expires, err := a.sessions.Create(ctx, userID, oidcAdmin, clientIP(r, a.trusted).String(), r.UserAgent())
	if err != nil {
		return session.User{}, err
	}
	session.SetCookie(w, token, expires)
	return a.sessions.Lookup(ctx, token)
}

func (a *server) Login(ctx context.Context, req oapi.LoginRequestObject) (oapi.LoginResponseObject, error) {
	w, r := httpFrom(ctx)
	if req.Body == nil {
		return nil, &apiError{status: http.StatusBadRequest, detail: "invalid request"}
	}
	name := normUsername(req.Body.Username)
	// The key is capped so one request cannot allocate a body-sized limiter key.
	userKey := "user:" + name[:min(len(name), 128)]
	keys := []string{"ip:" + clientIP(r, a.trusted).String(), userKey}
	if wait, ok := a.limiter.Allow(keys...); !ok {
		return nil, &apiError{status: http.StatusTooManyRequests, retryAfter: wait}
	}
	row, err := a.q.GetUserByUsername(ctx, name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	valid := false
	if err == nil && row.Disabled == 0 && row.PasswordHash.Valid && len(req.Body.Password) <= local.MaxPasswordBytes &&
		a.localAllowed(name, row.AdminLocal == 1) {
		valid, _ = local.Verify(row.PasswordHash.String, req.Body.Password)
	} else {
		local.DummyVerify(req.Body.Password)
	}
	if !valid {
		return nil, errBadLogin()
	}
	// Only the account's key is forgotten: resetting the address key would let
	// one valid login restart a password-spraying run.
	a.limiter.Succeed(userKey)
	if old, ok := session.TokenFrom(r); ok {
		_ = a.sessions.Revoke(ctx, old)
	}
	u, err := a.startSession(ctx, w, r, row.ID, false)
	if err != nil {
		return nil, err
	}
	if err := a.q.SetLastLogin(ctx, dbqLastLogin(row.ID, a.now().Unix())); err != nil {
		a.log.Error("set last login", "err", err)
	}
	if err := audit.Write(ctx, a.q, audit.Entry{ActorID: &row.ID, Action: "auth.login", Details: map[string]string{"method": "password"}, At: a.now()}); err != nil {
		a.log.Error("audit", "err", err)
	}
	return oapi.Login200JSONResponse(meOf(u)), nil
}

func (a *server) Logout(ctx context.Context, _ oapi.LogoutRequestObject) (oapi.LogoutResponseObject, error) {
	w, r := httpFrom(ctx)
	if token, ok := session.TokenFrom(r); ok {
		if err := a.sessions.Revoke(ctx, token); err != nil {
			return nil, err
		}
	}
	session.ClearCookie(w)
	if u, ok := UserFrom(ctx); ok {
		if err := audit.Write(ctx, a.q, audit.Entry{ActorID: &u.ID, Action: "auth.logout", At: a.now()}); err != nil {
			a.log.Error("audit", "err", err)
		}
	}
	return oapi.Logout204Response{}, nil
}
