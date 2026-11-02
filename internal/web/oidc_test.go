package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/auth/oidc"
	"github.com/vinRosso/hogwatch/internal/auth/oidc/oidctest"
	"github.com/vinRosso/hogwatch/internal/auth/session"
	"github.com/vinRosso/hogwatch/internal/config"
)

const flowCookieName = "__Host-hogwatch-oidc"

func claims(sub, email string, verified any, extra map[string]any) map[string]any {
	m := map[string]any{"sub": sub}
	if email != "" {
		m["email"] = email
	}
	if verified != nil {
		m["email_verified"] = verified
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func cookieHeader(cs ...*http.Cookie) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		if c != nil {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// start opens /oidc/start like a browser and returns the IdP URL and the flow cookie.
func (e *env) start(t *testing.T, cs ...*http.Cookie) (string, *http.Cookie, *httptest.ResponseRecorder) {
	t.Helper()
	w := e.do(req{method: "GET", path: "/api/v1/auth/oidc/start", header: map[string]string{"Cookie": cookieHeader(cs...)}})
	if w.Code != http.StatusFound {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	return w.Header().Get("Location"), cookieNamed(w, flowCookieName), w
}

func (e *env) callbackURL(t *testing.T, iss *oidctest.Issuer, authURL string) string {
	t.Helper()
	u, err := url.Parse(iss.Authorize(t, authURL))
	if err != nil {
		t.Fatal(err)
	}
	return u.RequestURI()
}

func (e *env) callback(path string, cs ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(req{method: "GET", path: path, header: map[string]string{"Cookie": cookieHeader(cs...)}})
}

// oidcLogin walks start -> identity provider -> callback with the given claims.
func (e *env) oidcLogin(t *testing.T, iss *oidctest.Issuer, c map[string]any, cs ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	iss.Next(c)
	authURL, flow, _ := e.start(t, cs...)
	cb := e.callbackURL(t, iss, authURL)
	return e.callback(cb, append([]*http.Cookie{flow}, cs...)...)
}

func (e *env) userCount() int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) auditCount(action string) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = ?`, action).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func wantRedirect(t *testing.T, w *httptest.ResponseRecorder, loc string) {
	t.Helper()
	if w.Code != http.StatusFound || w.Header().Get("Location") != loc {
		t.Fatalf("got %d Location %q, want 302 %q (body %s)", w.Code, w.Header().Get("Location"), loc, w.Body)
	}
}

func wantRefused(t *testing.T, w *httptest.ResponseRecorder, code string) {
	t.Helper()
	wantRedirect(t, w, "/login?oidc_error="+code)
	if c := cookieOf(w); c != nil && c.MaxAge >= 0 {
		t.Fatal("a refused login set a session cookie")
	}
}

// userRow reads the columns the tests care about.
type userRow struct {
	id                     int64
	username, display      string
	email, issuer, subject *string
	adminOIDC, adminLocal  bool
	notice                 *string
	hasPassword            bool
}

func (e *env) userBy(where string, arg any) (userRow, bool) {
	e.t.Helper()
	var r userRow
	var ao, al int
	err := e.db.QueryRow(`SELECT id, username, display_name, email, oidc_issuer, oidc_subject, admin_oidc, admin_local, notice,
		password_hash IS NOT NULL FROM users WHERE `+where+` = ?`, arg).
		Scan(&r.id, &r.username, &r.display, &r.email, &r.issuer, &r.subject, &ao, &al, &r.notice, &r.hasPassword)
	if err != nil {
		return userRow{}, false
	}
	r.adminOIDC, r.adminLocal = ao == 1, al == 1
	return r, true
}

func (e *env) meWith(t *testing.T, c *http.Cookie) (int, map[string]any) {
	t.Helper()
	w := e.callback("/api/v1/me", c)
	var me map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	return w.Code, me
}

func TestAuthConfig(t *testing.T) {
	get := func(e *env) map[string]any {
		w := e.do(req{method: "GET", path: "/api/v1/auth/config"})
		if w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	if m := get(newEnv(t)); m["local"] != true || m["oidc"] != false {
		t.Fatalf("no OIDC: %v", m)
	}
	iss := oidctest.New(t)
	if m := get(newEnv(t, withOIDC(iss))); m["local"] != true || m["oidc"] != true {
		t.Fatalf("OIDC: %v", m)
	}
	off := func(c *config.Config) { c.Auth.Local.Enabled = false }
	if m := get(newEnv(t, withOIDC(iss, off))); m["local"] != false || m["oidc"] != true {
		t.Fatalf("OIDC only: %v", m)
	}
}

func TestOIDC_Start(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	authURL, ck, _ := e.start(t)
	if ck == nil || ck.Value == "" || !ck.Secure || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode ||
		ck.Path != "/" || ck.MaxAge != 600 || ck.Domain != "" {
		t.Fatalf("flow cookie = %+v", ck)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if !strings.HasPrefix(authURL, iss.URL+"/authorize") || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") != ck.Value || q.Get("nonce") == "" ||
		q.Get("nonce") == q.Get("state") || q.Get("redirect_uri") != "https://hog.example/api/v1/auth/oidc/callback" {
		t.Fatalf("auth URL = %s", authURL)
	}
	// Each start is a new flow.
	_, ck2, _ := e.start(t)
	if ck2.Value == ck.Value {
		t.Fatal("two starts shared a state")
	}
}

func TestOIDC_StartDisabled(t *testing.T) {
	e := newEnv(t)
	if got := e.do(req{method: "GET", path: "/api/v1/auth/oidc/start"}).Code; got != http.StatusNotFound {
		t.Fatalf("start without OIDC: %d, want 404", got)
	}
	w := e.callback("/api/v1/auth/oidc/callback?code=x&state=y")
	wantRedirect(t, w, "/login?oidc_error=disabled")
}

func TestOIDC_LoginCreates(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	w := e.oidcLogin(t, iss, claims("sub-a", "alice@example.com", true,
		map[string]any{"preferred_username": "Alice", "name": "Alice Liddell"}))
	wantRedirect(t, w, "/")
	sess := cookieOf(w)
	if sess == nil || !sess.HttpOnly || !sess.Secure {
		t.Fatalf("session cookie = %+v", sess)
	}
	if fc := cookieNamed(w, flowCookieName); fc == nil || fc.MaxAge >= 0 {
		t.Fatalf("flow cookie not cleared: %+v", fc)
	}
	u, ok := e.userBy("username", "alice")
	if !ok || u.display != "Alice Liddell" || u.email == nil || *u.email != "alice@example.com" ||
		u.issuer == nil || *u.issuer != iss.URL || u.subject == nil || *u.subject != "sub-a" ||
		u.adminOIDC || u.adminLocal || u.hasPassword || u.notice != nil {
		t.Fatalf("user = %+v (found %v)", u, ok)
	}
	var paths int
	_ = e.db.QueryRow(`SELECT count(*) FROM user_paths WHERE user_id = ?`, u.id).Scan(&paths)
	if paths != 0 {
		t.Fatalf("new user has %d paths", paths)
	}
	code, me := e.meWith(t, sess)
	if code != 200 || me["username"] != "alice" || me["oidc_linked"] != true || me["is_admin"] != false {
		t.Fatalf("/me = %d %v", code, me)
	}
	if e.auditCount("user.created_oidc") != 1 || e.auditCount("auth.login") != 1 {
		t.Fatal("audit rows missing")
	}
	var details string
	_ = e.db.QueryRow(`SELECT details FROM audit_log WHERE action = 'auth.login'`).Scan(&details)
	if details != `{"method":"oidc"}` {
		t.Fatalf("login audit details = %s", details)
	}
}

func TestOIDC_UsernameSuffix(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	e.addUser("alice", "", false, false)
	for i, want := range []string{"alice-2", "alice-3"} {
		sub := "sub-" + want
		wantRedirect(t, e.oidcLogin(t, iss, claims(sub, "", nil, map[string]any{"preferred_username": "alice"})), "/")
		if _, ok := e.userBy("oidc_subject", sub); !ok {
			t.Fatalf("round %d: not created", i)
		}
		if u, ok := e.userBy("username", want); !ok || *u.subject != sub {
			t.Fatalf("round %d: username %s missing", i, want)
		}
	}
}

func TestOIDC_UsernameFromHostileClaims(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	for i, name := range []string{"", "../x", strings.Repeat("A", 200), "Jürgen", "Admin"} {
		sub := "h" + string(rune('a'+i))
		w := e.oidcLogin(t, iss, claims(sub, "", nil, map[string]any{"preferred_username": name}))
		wantRedirect(t, w, "/")
		u, ok := e.userBy("oidc_subject", sub)
		if !ok || u.username == "" || len(u.username) > 64 {
			t.Fatalf("%q: user = %+v", name, u)
		}
	}
}

func TestOIDC_LoginLinked(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	first := e.oidcLogin(t, iss, claims("sub-a", "a@example.com", true, map[string]any{"preferred_username": "a"}))
	wantRedirect(t, first, "/")
	n := e.userCount()
	second := e.oidcLogin(t, iss, claims("sub-a", "a@example.com", true, map[string]any{"preferred_username": "a"}))
	wantRedirect(t, second, "/")
	if e.userCount() != n || e.auditCount("user.created_oidc") != 1 {
		t.Fatal("a returning user created a second account")
	}
	if cookieOf(second) == nil || cookieOf(second).Value == cookieOf(first).Value {
		t.Fatal("no fresh session")
	}
}

func TestOIDC_LinksVerifiedEmail(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	id := e.addUser("carol", "pw-carol-1234", false, false)
	e.exec(`UPDATE users SET email = 'carol@example.com' WHERE id = ?`, id)
	n := e.userCount()
	w := e.oidcLogin(t, iss, claims("sub-c", "Carol@Example.COM", true, nil))
	wantRedirect(t, w, "/")
	u, _ := e.userBy("id", id)
	if u.subject == nil || *u.subject != "sub-c" || *u.issuer != iss.URL || u.notice == nil || *u.notice != "oidc_linked" {
		t.Fatalf("not linked: %+v", u)
	}
	if e.userCount() != n {
		t.Fatal("linking created a user")
	}
	code, me := e.meWith(t, cookieOf(w))
	if code != 200 || me["username"] != "carol" || me["notice"] != "oidc_linked" {
		t.Fatalf("/me = %d %v", code, me)
	}
	var details string
	_ = e.db.QueryRow(`SELECT details FROM audit_log WHERE action = 'oidc.linked'`).Scan(&details)
	if details != `{"method":"email"}` || e.auditCount("oidc.linked") != 1 {
		t.Fatalf("link audit: %s", details)
	}
	// The password still works: linking added a way in, it removed none.
	if login(e, "carol", "pw-carol-1234").Code != 200 {
		t.Fatal("password login broke after linking")
	}
}

// Review focus 2: an IdP account that merely claims a victim's address.
func TestOIDC_RefusesUnverifiedMatch(t *testing.T) {
	for name, verified := range map[string]any{"false": false, "missing": nil, "string true": "true", "number 1": 1} {
		t.Run(name, func(t *testing.T) {
			iss := oidctest.New(t)
			e := newEnv(t, withOIDC(iss))
			id := e.addUser("victim", "pw-victim-123", false, false)
			e.exec(`UPDATE users SET email = 'victim@example.com' WHERE id = ?`, id)
			n := e.userCount()
			wantRefused(t, e.oidcLogin(t, iss, claims("sub-evil", "victim@example.com", verified, nil)), "ask_admin")
			u, _ := e.userBy("id", id)
			if u.subject != nil || u.notice != nil || e.userCount() != n {
				t.Fatalf("victim touched or a user was created: %+v", u)
			}
			if e.auditCount("auth.oidc_refused") != 1 {
				t.Fatal("refusal not audited")
			}
		})
	}
}

func TestOIDC_UnverifiedEmailNotStored(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	wantRedirect(t, e.oidcLogin(t, iss, claims("sub-squat", "owner@example.com", false, map[string]any{"preferred_username": "squatter"})), "/")
	u, ok := e.userBy("username", "squatter")
	if !ok || u.email != nil {
		t.Fatalf("unverified email stored: %+v", u)
	}
	// The real owner (an admin-created account) is still linkable by their verified email.
	id := e.addUser("realowner", "pw-owner-1234", false, false)
	e.exec(`UPDATE users SET email = 'owner@example.com' WHERE id = ?`, id)
	wantRedirect(t, e.oidcLogin(t, iss, claims("sub-real", "owner@example.com", true, nil)), "/")
	if o, _ := e.userBy("id", id); o.subject == nil || *o.subject != "sub-real" {
		t.Fatalf("owner not linked: %+v", o)
	}
	if s, _ := e.userBy("username", "squatter"); *s.subject != "sub-squat" {
		t.Fatal("squatter changed")
	}
}

func TestOIDC_AlreadyLinkedTargetRefused(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	id := e.addUser("dave", "pw-dave-12345", false, false)
	e.exec(`UPDATE users SET email = 'dave@example.com', oidc_issuer = ?, oidc_subject = 'sub-dave', oidc_linked_at = 1 WHERE id = ?`, iss.URL, id)
	n := e.userCount()
	wantRefused(t, e.oidcLogin(t, iss, claims("sub-other", "dave@example.com", true, nil)), "ask_admin")
	if d, _ := e.userBy("id", id); *d.subject != "sub-dave" || e.userCount() != n {
		t.Fatalf("dave was relinked or a user created: %+v", d)
	}
}

func TestOIDC_EmailChangedAfterLink(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	a := e.addUser("anna", "", false, false)
	e.exec(`UPDATE users SET email = 'anna@example.com', oidc_issuer = ?, oidc_subject = 'sub-anna', oidc_linked_at = 1 WHERE id = ?`, iss.URL, a)
	b := e.addUser("ben", "", false, false)
	e.exec(`UPDATE users SET email = 'ben@example.com' WHERE id = ?`, b)
	w := e.oidcLogin(t, iss, claims("sub-anna", "ben@example.com", true, nil))
	wantRedirect(t, w, "/")
	_, me := e.meWith(t, cookieOf(w))
	if me["username"] != "anna" {
		t.Fatalf("logged in as %v", me["username"])
	}
	if u, _ := e.userBy("id", b); u.subject != nil {
		t.Fatal("ben was linked by anna's changed email")
	}
}

func TestOIDC_DisabledStaysDisabled(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	l := e.addUser("linked", "", false, true)
	e.exec(`UPDATE users SET oidc_issuer = ?, oidc_subject = 'sub-l', oidc_linked_at = 1 WHERE id = ?`, iss.URL, l)
	wantRefused(t, e.oidcLogin(t, iss, claims("sub-l", "", nil, nil)), "disabled")
	m := e.addUser("mailed", "", false, true)
	e.exec(`UPDATE users SET email = 'm@example.com' WHERE id = ?`, m)
	wantRefused(t, e.oidcLogin(t, iss, claims("sub-m", "m@example.com", true, nil)), "disabled")
	if u, _ := e.userBy("id", m); u.subject != nil {
		t.Fatal("a disabled account was linked")
	}
}

func TestOIDC_LocalAdminNotAutolinked(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	id := e.addUser("root", "pw-root-12345", true, false)
	e.exec(`UPDATE users SET email = 'root@example.com' WHERE id = ?`, id)
	wantRefused(t, e.oidcLogin(t, iss, claims("sub-r", "root@example.com", true, nil)), "link_from_profile")
	if u, _ := e.userBy("id", id); u.subject != nil {
		t.Fatal("a local admin was auto-linked")
	}

	iss2 := oidctest.New(t)
	e2 := newEnv(t, withOIDC(iss2, func(c *config.Config) { c.Auth.OIDC.AutolinkLocalAdmins = true }))
	id2 := e2.addUser("root", "pw-root-12345", true, false)
	e2.exec(`UPDATE users SET email = 'root@example.com' WHERE id = ?`, id2)
	wantRedirect(t, e2.oidcLogin(t, iss2, claims("sub-r", "root@example.com", true, nil)), "/")
	if u, _ := e2.userBy("id", id2); u.subject == nil {
		t.Fatal("autolink_local_admins did not link")
	}
}

func (e *env) sessionOf(userID int64) (life time.Duration, oidcAdmin bool, n int) {
	e.t.Helper()
	var l, o int64
	rows, err := e.db.Query(`SELECT expires_at - created_at, oidc_admin FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := rows.Scan(&l, &o); err != nil {
			e.t.Fatal(err)
		}
		n++
	}
	return time.Duration(l) * time.Second, o == 1, n
}

// Review focus 3: group membership gives admin for one session of at most 12 h,
// and every change of the group revokes the old sessions.
func TestOIDC_AdminFromGroups(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	adm := map[string]any{"groups": []string{"admins"}, "preferred_username": "gina"}
	plain := map[string]any{"groups": []string{"staff"}, "preferred_username": "gina"}

	w1 := e.oidcLogin(t, iss, claims("sub-g", "", nil, adm))
	wantRedirect(t, w1, "/")
	a := cookieOf(w1)
	if _, me := e.meWith(t, a); me["is_admin"] != true || me["admin_oidc"] != true {
		t.Fatalf("admin login: %v", me)
	}
	u, _ := e.userBy("oidc_subject", "sub-g")
	if life, flag, _ := e.sessionOf(u.id); life != 12*time.Hour || !flag {
		t.Fatalf("session life %v oidc_admin %v", life, flag)
	}
	// Active use does not extend the cap.
	e.now = e.now.Add(11 * time.Hour)
	if code, _ := e.meWith(t, a); code != 200 {
		t.Fatalf("at 11h: %d", code)
	}
	e.now = e.now.Add(time.Hour)
	if code, _ := e.meWith(t, a); code != http.StatusUnauthorized {
		t.Fatalf("at 12h: %d, want 401", code)
	}

	// Removed from the group: the next login is a plain session and the old one is gone.
	w2 := e.oidcLogin(t, iss, claims("sub-g", "", nil, adm))
	b := cookieOf(w2)
	w3 := e.oidcLogin(t, iss, claims("sub-g", "", nil, plain))
	wantRedirect(t, w3, "/")
	if code, _ := e.meWith(t, b); code != http.StatusUnauthorized {
		t.Fatalf("pre-demotion session still valid: %d", code)
	}
	c := cookieOf(w3)
	if _, me := e.meWith(t, c); me["is_admin"] != false || me["admin_oidc"] != false {
		t.Fatalf("demoted login: %v", me)
	}
	if life, flag, _ := e.sessionOf(u.id); life != 168*time.Hour || flag {
		t.Fatalf("plain session life %v oidc_admin %v", life, flag)
	}

	// Clear then regrant: the session from before the demotion must not come back.
	w4 := e.oidcLogin(t, iss, claims("sub-g", "", nil, adm))
	if code, _ := e.meWith(t, c); code != http.StatusUnauthorized {
		t.Fatalf("pre-regrant session survived: %d", code)
	}
	if code, me := e.meWith(t, cookieOf(w4)); code != 200 || me["is_admin"] != true {
		t.Fatalf("regranted login: %d %v", code, me)
	}
	if _, _, n := e.sessionOf(u.id); n != 1 {
		t.Fatalf("%d live sessions after role changes, want 1", n)
	}
}

func TestOIDC_AdminLocalInGroupKeepsFullSession(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss, func(c *config.Config) { c.Auth.OIDC.AutolinkLocalAdmins = true }))
	id := e.addUser("root", "pw-root-12345", true, false)
	e.exec(`UPDATE users SET email = 'root@example.com' WHERE id = ?`, id)
	w := e.oidcLogin(t, iss, claims("sub-r", "root@example.com", true, map[string]any{"groups": []string{"admins"}}))
	wantRedirect(t, w, "/")
	if life, flag, _ := e.sessionOf(id); life != 168*time.Hour || flag {
		t.Fatalf("local admin session: life %v oidc_admin %v", life, flag)
	}
	if _, me := e.meWith(t, cookieOf(w)); me["is_admin"] != true {
		t.Fatalf("local admin lost admin: %v", me)
	}
}

// Review focus 1: the callback is bound to the browser that started the flow.
func TestOIDC_StateBinding(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))

	newFlow := func(sub string) (string, *http.Cookie) {
		iss.Next(claims(sub, "", nil, map[string]any{"preferred_username": sub}))
		authURL, ck, _ := e.start(t)
		return e.callbackURL(t, iss, authURL), ck
	}
	noSession := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		wantRefused(t, w, "expired")
		if fc := cookieNamed(w, flowCookieName); fc == nil || fc.MaxAge >= 0 {
			t.Fatalf("%s: flow cookie not cleared", name)
		}
	}

	cb, _ := newFlow("a")
	noSession("no cookie", e.callback(cb))
	cb, _ = newFlow("b")
	noSession("foreign cookie", e.callback(cb, &http.Cookie{Name: flowCookieName, Value: "attacker-chosen-value"})) //nolint:gosec // a request cookie
	// A cookie that belongs to another flow is as foreign as a made-up one.
	cb1, _ := newFlow("c1")
	_, ck2 := newFlow("c2")
	noSession("other flow's cookie", e.callback(cb1, ck2))
	noSession("no state", e.callback("/api/v1/auth/oidc/callback?code=x", ck2))
	if e.userCount() != 5 { // the five role users only
		t.Fatalf("a rejected callback created a user: %d users", e.userCount())
	}

	// Replay of a completed callback.
	cb, ck := newFlow("d")
	wantRedirect(t, e.callback(cb, ck), "/")
	n := e.userCount()
	noSession("replay", e.callback(cb, ck))
	if e.userCount() != n {
		t.Fatal("a replay created a user")
	}

	// A flow older than 10 minutes.
	cb, ck = newFlow("e")
	e.now = e.now.Add(oidc.FlowTTL)
	noSession("expired flow", e.callback(cb, ck))
	if _, ok := e.userBy("oidc_subject", "e"); ok {
		t.Fatal("an expired flow created a user")
	}
}

func TestOIDC_CallbackErrorParam(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	iss.DenyNext("access_denied")
	authURL, ck, _ := e.start(t)
	cb := e.callbackURL(t, iss, authURL)
	if !strings.Contains(cb, "error=access_denied") {
		t.Fatalf("callback = %s", cb)
	}
	wantRefused(t, e.callback(cb, ck), "cancelled")
	// The flow was consumed by the cancelled attempt.
	wantRefused(t, e.callback(cb, ck), "expired")
}

func TestOIDC_IssuerDown(t *testing.T) {
	iss := oidctest.New(t)
	iss.Down(true)
	e := newEnv(t, withOIDC(iss)) // the server starts while the IdP is down
	if got := e.do(req{method: "GET", path: "/api/v1/auth/oidc/start"}).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("start with the IdP down: %d, want 503", got)
	}
	if e.flowsPending() != 0 {
		t.Fatal("a failed start left a pending flow")
	}
	iss.Down(false)
	e.start(t) // no cached failure

	// The IdP dies between authorize and the token exchange.
	iss.Next(claims("sub-x", "", nil, nil))
	authURL, ck, _ := e.start(t)
	cb := e.callbackURL(t, iss, authURL)
	iss.Down(true)
	wantRefused(t, e.callback(cb, ck), "unavailable")
	iss.Down(false)

	// A token that fails verification is "failed", never a session.
	iss.SetTokenMutator(func(m map[string]any) { m["aud"] = "someone-else" })
	wantRefused(t, e.oidcLogin(t, iss, claims("sub-y", "", nil, nil)), "failed")
	iss.SetTokenMutator(nil)
	iss.SignWithForeignKey(true)
	wantRefused(t, e.oidcLogin(t, iss, claims("sub-z", "", nil, nil)), "failed")
	if e.userCount() != 5 {
		t.Fatalf("a failed login created a user: %d", e.userCount())
	}
}

func TestOIDC_ConcurrentFirstLogin(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	iss.Next(claims("sub-race", "race@example.com", true, map[string]any{"preferred_username": "racer"}))
	type hop struct {
		cb string
		ck *http.Cookie
	}
	var hops []hop
	for i := 0; i < 2; i++ {
		authURL, ck, _ := e.start(t)
		hops = append(hops, hop{e.callbackURL(t, iss, authURL), ck})
	}
	var wg sync.WaitGroup
	res := make([]*httptest.ResponseRecorder, len(hops))
	for i, h := range hops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i] = e.callback(h.cb, h.ck)
		}()
	}
	wg.Wait()
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM users WHERE oidc_subject = 'sub-race'`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d users for one subject", n)
	}
	for i, w := range res {
		wantRedirect(t, w, "/")
		if code, me := e.meWith(t, cookieOf(w)); code != 200 || me["username"] != "racer" {
			t.Fatalf("browser %d: %d %v", i, code, me)
		}
	}
}

func TestOIDC_RevokesCarriedSession(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	old := &http.Cookie{Name: session.CookieName, Value: e.cookies["plain"]} //nolint:gosec // a request cookie
	if code, _ := e.meWith(t, old); code != 200 {
		t.Fatal("fixture session invalid")
	}
	w := e.oidcLogin(t, iss, claims("sub-n", "", nil, map[string]any{"preferred_username": "newcomer"}), old)
	wantRedirect(t, w, "/")
	if code, _ := e.meWith(t, old); code != http.StatusUnauthorized {
		t.Fatalf("the carried session survived an OIDC login: %d", code)
	}
}

func (e *env) flowsPending() int { return e.flows.Len() }

// One address cannot push other people's pending logins out of the store.
func TestOIDC_StartFloodDoesNotEvictOthers(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	iss.Next(claims("sub-victim", "", nil, map[string]any{"preferred_username": "victim"}))
	w := e.do(req{method: "GET", path: "/api/v1/auth/oidc/start", remote: "198.51.100.7:4000"})
	if w.Code != http.StatusFound {
		t.Fatalf("victim start: %d", w.Code)
	}
	victim, authURL := cookieNamed(w, flowCookieName), w.Header().Get("Location")
	for i := 0; i < 3*oidc.MaxFlowsPerClient; i++ {
		e.do(req{method: "GET", path: "/api/v1/auth/oidc/start", remote: "203.0.113.9:4000"})
	}
	if n := e.flowsPending(); n > oidc.MaxFlowsPerClient+1 {
		t.Fatalf("one address holds %d pending flows, cap is %d", n-1, oidc.MaxFlowsPerClient)
	}
	wantRedirect(t, e.callback(e.callbackURL(t, iss, authURL), victim), "/")
}
