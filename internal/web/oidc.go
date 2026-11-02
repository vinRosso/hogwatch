package web

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/vinRosso/hogwatch/internal/audit"
	"github.com/vinRosso/hogwatch/internal/auth/oidc"
	"github.com/vinRosso/hogwatch/internal/auth/session"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/web/oapi"
)

// flowCookie binds a callback to the browser that started the flow (login
// CSRF): its value is the flow's state, and the callback must present both.
const flowCookie = "__Host-hogwatch-oidc"

// Error codes of the redirects the callback sends the browser to. The SPA
// (M9) renders them; the callback never redirects anywhere else.
const (
	errCancelled       = "cancelled"
	errExpired         = "expired"
	errFailed          = "failed"
	errUnavailable     = "unavailable"
	errDisabled        = "disabled"
	errAskAdmin        = "ask_admin"
	errLinkFromProfile = "link_from_profile"
	errAlreadyLinked   = "already_linked"
)

func (a *server) oidcEnabled() bool { return a.oidc != nil && a.flows != nil }

func setFlowCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie, Value: state, Path: "/", MaxAge: int(oidc.FlowTTL.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func clearFlowCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// oidcRedirect is the callback's only way to answer: a 302 to a fixed in-app path.
func oidcRedirect(path string) oapi.OidcCallbackResponseObject {
	return oapi.OidcCallback302Response{Headers: oapi.OidcCallback302ResponseHeaders{Location: &path}}
}

// oidcFailure is the redirect for an error code: the profile page for a flow
// that was started from there, the login page otherwise.
func oidcFailure(mode oidc.Mode, code string) oapi.OidcCallbackResponseObject {
	if mode == oidc.ModeLink {
		return oidcRedirect("/profile?oidc_error=" + code)
	}
	return oidcRedirect("/login?oidc_error=" + code)
}

func (a *server) GetAuthConfig(context.Context, oapi.GetAuthConfigRequestObject) (oapi.GetAuthConfigResponseObject, error) {
	return oapi.GetAuthConfig200JSONResponse(oapi.AuthConfig{Local: a.cfg.Auth.Local.Enabled, Oidc: a.oidcEnabled()}), nil
}

func (a *server) OidcStart(ctx context.Context, _ oapi.OidcStartRequestObject) (oapi.OidcStartResponseObject, error) {
	w, r := httpFrom(ctx)
	if !a.oidcEnabled() {
		return nil, &apiError{status: http.StatusNotFound}
	}
	loc, err := a.beginFlow(ctx, w, r, oidc.ModeLogin, 0)
	if err != nil {
		return nil, err
	}
	return oapi.OidcStart302Response{Headers: oapi.OidcStart302ResponseHeaders{Location: &loc}}, nil
}

// beginFlow registers a flow, sets its cookie and returns the identity
// provider URL. A provider that cannot be reached is a 503 and leaves no
// pending flow behind.
func (a *server) beginFlow(ctx context.Context, w http.ResponseWriter, r *http.Request, mode oidc.Mode, linkUserID int64) (string, error) {
	fl, err := a.flows.BeginFrom(clientIP(r, a.trusted).String(), mode, linkUserID)
	if err != nil {
		return "", err
	}
	authURL, err := a.oidc.AuthURL(ctx, fl.State, fl.Nonce, fl.Verifier)
	if err != nil {
		a.flows.Take(fl.State)
		if errors.Is(err, oidc.ErrUnavailable) {
			return "", &apiError{status: http.StatusServiceUnavailable, detail: "identity provider unavailable"}
		}
		return "", err
	}
	setFlowCookie(w, fl.State)
	return authURL, nil
}

func (a *server) OidcCallback(ctx context.Context, req oapi.OidcCallbackRequestObject) (oapi.OidcCallbackResponseObject, error) {
	w, r := httpFrom(ctx)
	clearFlowCookie(w)
	if !a.oidcEnabled() {
		return oidcFailure(oidc.ModeLogin, errDisabled), nil
	}

	// The flow must be the one this browser started: a valid state arriving
	// without the matching cookie is a login-CSRF attempt or a stale tab.
	state := deref(req.Params.State)
	ck, err := r.Cookie(flowCookie)
	if state == "" || err != nil || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(state)) != 1 {
		return oidcFailure(oidc.ModeLogin, errExpired), nil
	}
	flow, ok := a.flows.Take(state)
	if !ok {
		return oidcFailure(oidc.ModeLogin, errExpired), nil
	}
	if deref(req.Params.Error) != "" {
		return oidcFailure(flow.Mode, errCancelled), nil
	}
	code := deref(req.Params.Code)
	if code == "" {
		return oidcFailure(flow.Mode, errFailed), nil
	}
	// A link flow belongs to the session that started it; check before the
	// provider is asked anything.
	if flow.Mode == oidc.ModeLink {
		if u, ok := UserFrom(ctx); !ok || u.ID != flow.LinkUserID {
			return oidcFailure(oidc.ModeLink, errExpired), nil
		}
	}
	id, err := a.oidc.Exchange(ctx, code, flow.Verifier, flow.Nonce)
	switch {
	case errors.Is(err, oidc.ErrUnavailable):
		return oidcFailure(flow.Mode, errUnavailable), nil
	case err != nil:
		return oidcFailure(flow.Mode, errFailed), nil
	}

	if flow.Mode == oidc.ModeLink {
		return a.oidcLink(ctx, flow, id), nil
	}
	res, err := a.oidcLogin(ctx, id)
	if err != nil {
		a.log.Error("oidc callback", "err", err)
		return oidcFailure(flow.Mode, errFailed), nil
	}
	if res.refuse != "" {
		return oidcFailure(flow.Mode, res.refuse), nil
	}
	// A login replaces whatever session the browser carried.
	if old, ok := session.TokenFrom(r); ok {
		_ = a.sessions.Revoke(ctx, old)
	}
	if _, err := a.startSession(ctx, w, r, res.userID, id.GroupsAdmin && !res.adminLocal); err != nil {
		a.log.Error("oidc session", "err", err)
		return oidcFailure(flow.Mode, errFailed), nil
	}
	return oidcRedirect("/"), nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// oidcResult is the outcome of the database half of a login: a user to start a
// session for, or the error code to refuse with.
type oidcResult struct {
	userID     int64
	adminLocal bool
	refuse     string
}

func candidate(id int64, adminLocal, disabled, linked int64) *oidc.Candidate {
	return &oidc.Candidate{ID: id, AdminLocal: adminLocal != 0, Disabled: disabled != 0, Linked: linked != 0}
}

func refusalCode(r oidc.Reason) string {
	switch r {
	case oidc.ReasonDisabled:
		return errDisabled
	case oidc.ReasonLinkFromProfile:
		return errLinkFromProfile
	default:
		return errAskAdmin
	}
}

func uniqueOn(err error, column string) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_CONSTRAINT && strings.Contains(se.Error(), column)
}

// oidcLogin finds, links or creates the account of a verified identity and
// applies its groups, in one write transaction so racing first logins of one
// subject serialize and the second finds the first's account (rule 1).
func (a *server) oidcLogin(ctx context.Context, id oidc.Identity) (oidcResult, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return oidcResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := a.q.WithTx(tx)
	now := a.now()

	var bySubject, byEmail *oidc.Candidate
	row, err := q.GetUserByOIDC(ctx, dbq.GetUserByOIDCParams{
		Issuer: sql.NullString{String: id.Issuer, Valid: true}, Subject: sql.NullString{String: id.Subject, Valid: true},
	})
	switch {
	case err == nil:
		bySubject = candidate(row.ID, row.AdminLocal, row.Disabled, 1)
	case !errors.Is(err, sql.ErrNoRows):
		return oidcResult{}, err
	}
	if bySubject == nil && id.Email != "" {
		er, err := q.GetUserByEmail(ctx, sql.NullString{String: id.Email, Valid: true})
		switch {
		case err == nil:
			byEmail = candidate(er.ID, er.AdminLocal, er.Disabled, er.Linked)
		case !errors.Is(err, sql.ErrNoRows):
			return oidcResult{}, err
		}
	}

	refuse := func(code string) (oidcResult, error) {
		if err := a.auditRefusal(ctx, q, id, code, now); err != nil {
			return oidcResult{}, err
		}
		return oidcResult{refuse: code}, tx.Commit()
	}

	d := oidc.Decide(id, bySubject, byEmail, a.cfg.Auth.OIDC.AutolinkLocalAdmins)
	var userID int64
	var action, target string // the extra audit entry of this login
	var details any
	switch d.Kind {
	case oidc.KindRefuse:
		return refuse(refusalCode(d.Reason))
	case oidc.KindLogin:
		userID = d.UserID
	case oidc.KindLinkAndLogin:
		n, err := q.LinkOIDC(ctx, dbq.LinkOIDCParams{
			Issuer: sql.NullString{String: id.Issuer, Valid: true}, Subject: sql.NullString{String: id.Subject, Valid: true},
			At: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: d.UserID,
		})
		if err != nil {
			return oidcResult{}, err
		}
		if n == 0 { // linked by someone else since we looked
			return refuse(errAskAdmin)
		}
		userID, action, details = d.UserID, "oidc.linked", map[string]string{"method": "email"}
	case oidc.KindCreate:
		userID, target, err = a.insertOIDCUser(ctx, q, id, d.StoreEmail, now.Unix())
		if err != nil {
			return oidcResult{}, err
		}
		action = "user.created_oidc"
	}

	u, err := q.GetUserByID(ctx, userID)
	if err != nil {
		return oidcResult{}, err
	}
	if u.Disabled != 0 {
		// Drop the link or account this login just wrote; only the refusal stays.
		_ = tx.Rollback()
		if err := a.auditRefusal(ctx, a.q, id, errDisabled, now); err != nil {
			return oidcResult{}, err
		}
		return oidcResult{refuse: errDisabled}, nil
	}
	// admin_oidc follows the groups at every login; a change is a role change,
	// which revokes every session of the user before the new one.
	if (u.AdminOidc != 0) != id.GroupsAdmin {
		v := int64(0)
		if id.GroupsAdmin {
			v = 1
		}
		if _, err := q.SetAdminOIDC(ctx, dbq.SetAdminOIDCParams{V: v, ID: userID}); err != nil {
			return oidcResult{}, err
		}
		if _, err := q.DeleteUserSessions(ctx, userID); err != nil {
			return oidcResult{}, err
		}
	}
	if err := q.SetLastLogin(ctx, dbqLastLogin(userID, now.Unix())); err != nil {
		return oidcResult{}, err
	}
	if target == "" {
		target = u.Username
	}
	entries := []audit.Entry{{ActorID: &userID, Action: "auth.login", Details: map[string]string{"method": "oidc"}, At: now}}
	if action != "" {
		entries = append(entries, audit.Entry{ActorID: &userID, Action: action, Target: target, Details: details, At: now})
	}
	for _, e := range entries {
		if err := audit.Write(ctx, q, e); err != nil {
			return oidcResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return oidcResult{}, err
	}
	return oidcResult{userID: userID, adminLocal: u.AdminLocal != 0}, nil
}

// auditRefusal records a refused login with the identity it was about, so an
// admin can tell which identity-provider account to look at.
func (a *server) auditRefusal(ctx context.Context, q *dbq.Queries, id oidc.Identity, code string, now time.Time) error {
	return audit.Write(ctx, q, audit.Entry{
		Action: "auth.oidc_refused", At: now,
		Details: map[string]string{"reason": code, "issuer": id.Issuer, "subject": id.Subject},
	})
}

// cleanName drops control characters from a name the identity provider sent.
func cleanName(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

var errUsernamesTaken = errors.New("web: no free username")

// insertOIDCUser creates the account of a first-time identity. A taken
// username gets -2, -3… A verified email is stored; an unverified one never is.
func (a *server) insertOIDCUser(ctx context.Context, q *dbq.Queries, id oidc.Identity, storeEmail bool, now int64) (int64, string, error) {
	base := oidc.DeriveUsername(id.PreferredUsername, id.Email)
	display := []rune(cleanName(id.Name))
	if len(display) > 100 {
		display = display[:100]
	}
	var email sql.NullString
	if storeEmail && id.Email != "" {
		email = sql.NullString{String: id.Email, Valid: true}
	}
	for n := 1; n <= 50; n++ {
		name := base
		if n > 1 {
			name = oidc.WithSuffix(base, n)
		}
		shown := string(display)
		if shown == "" {
			shown = name
		}
		newID, err := q.InsertOIDCUser(ctx, dbq.InsertOIDCUserParams{
			Username: name, DisplayName: shown, Email: email,
			OidcIssuer:   sql.NullString{String: id.Issuer, Valid: true},
			OidcSubject:  sql.NullString{String: id.Subject, Valid: true},
			OidcLinkedAt: sql.NullInt64{Int64: now, Valid: true},
			AdminOidc:    boolInt(id.GroupsAdmin), CreatedAt: now,
		})
		switch {
		case err == nil:
			return newID, name, nil
		case uniqueOn(err, "users.username"):
			continue
		default:
			return 0, "", err
		}
	}
	return 0, "", errUsernamesTaken
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
