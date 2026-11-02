package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vinRosso/hogwatch/internal/auth/oidc/oidctest"
	"github.com/vinRosso/hogwatch/internal/auth/session"
)

func (e *env) sessionCookie(role string) *http.Cookie {
	return &http.Cookie{Name: session.CookieName, Value: e.cookies[role]} //nolint:gosec // a request cookie
}

// linkStart is POST /me/link-oidc as role; it returns the IdP URL and flow cookie.
func (e *env) linkStart(t *testing.T, role string) (string, *http.Cookie) {
	t.Helper()
	w := e.do(req{method: "POST", path: "/api/v1/me/link-oidc", role: role})
	if w.Code != 200 {
		t.Fatalf("link-oidc as %s: %d %s", role, w.Code, w.Body)
	}
	var out struct{ URL string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.URL == "" {
		t.Fatalf("body %s: %v", w.Body, err)
	}
	ck := cookieNamed(w, flowCookieName)
	if ck == nil {
		t.Fatal("no flow cookie")
	}
	return out.URL, ck
}

func TestLinkOIDC_Flow(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	n := e.userCount()
	iss.Next(claims("sub-plain", "", nil, nil))
	authURL, fck := e.linkStart(t, "plain")
	cb := e.callbackURL(t, iss, authURL)
	w := e.callback(cb, fck, e.sessionCookie("plain"))
	wantRedirect(t, w, "/profile?oidc=linked")
	u, _ := e.userBy("id", e.users["plain"])
	if u.subject == nil || *u.subject != "sub-plain" || *u.issuer != iss.URL || u.notice == nil || *u.notice != "oidc_linked" {
		t.Fatalf("not linked: %+v", u)
	}
	if e.userCount() != n {
		t.Fatal("linking created a user")
	}
	// The caller keeps their session, and is told once.
	if _, me := e.meWith(t, e.sessionCookie("plain")); me["oidc_linked"] != true || me["notice"] != "oidc_linked" {
		t.Fatalf("/me = %v", me)
	}
	var details string
	_ = e.db.QueryRow(`SELECT details FROM audit_log WHERE action = 'oidc.linked'`).Scan(&details)
	if details != `{"issuer":"`+iss.URL+`","method":"profile","subject":"sub-plain"}` || e.auditCount("oidc.linked") != 1 {
		t.Fatalf("audit: %s", details)
	}
	// Later logins match on (issuer, sub) alone.
	lw := e.oidcLogin(t, iss, claims("sub-plain", "other@example.com", true, nil))
	wantRedirect(t, lw, "/")
	if _, me := e.meWith(t, cookieOf(lw)); me["username"] != "plain" {
		t.Fatalf("logged in as %v", me["username"])
	}
}

func TestLinkOIDC_Refusals(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	e.exec(`UPDATE users SET oidc_issuer = ?, oidc_subject = 'sub-owner', oidc_linked_at = 1 WHERE id = ?`, iss.URL, e.users["owner"])
	if got := e.do(req{method: "POST", path: "/api/v1/me/link-oidc", role: "owner"}).Code; got != http.StatusConflict {
		t.Fatalf("already linked: %d, want 409", got)
	}
	if got := e.do(req{method: "POST", path: "/api/v1/me/link-oidc", role: "anon"}).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d, want 401", got)
	}
	if got := newEnv(t).do(req{method: "POST", path: "/api/v1/me/link-oidc", role: "plain"}).Code; got != http.StatusNotFound {
		t.Fatalf("OIDC off: %d, want 404", got)
	}
	iss.Down(true)
	if got := e.do(req{method: "POST", path: "/api/v1/me/link-oidc", role: "plain"}).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("IdP down: %d, want 503", got)
	}
}

func TestLinkOIDC_SubjectTaken(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	e.exec(`UPDATE users SET oidc_issuer = ?, oidc_subject = 'sub-shared', oidc_linked_at = 1 WHERE id = ?`, iss.URL, e.users["owner"])
	iss.Next(claims("sub-shared", "", nil, nil))
	authURL, fck := e.linkStart(t, "plain")
	w := e.callback(e.callbackURL(t, iss, authURL), fck, e.sessionCookie("plain"))
	wantRedirect(t, w, "/profile?oidc_error=already_linked")
	if u, _ := e.userBy("id", e.users["plain"]); u.subject != nil || u.notice != nil {
		t.Fatalf("plain was linked: %+v", u)
	}
	if o, _ := e.userBy("id", e.users["owner"]); *o.subject != "sub-shared" {
		t.Fatal("the owner's link changed")
	}
}

// Review focus 1: a link flow started by one user must not complete in
// another user's browser, nor in a browser with no session.
func TestLinkOIDC_OtherSession(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	for name, cs := range map[string][]*http.Cookie{
		"another user's session": {e.sessionCookie("owner")},
		"no session":             {},
	} {
		t.Run(name, func(t *testing.T) {
			iss.Next(claims("sub-victim-"+strings.ReplaceAll(name, " ", "-"), "", nil, nil))
			authURL, fck := e.linkStart(t, "plain")
			w := e.callback(e.callbackURL(t, iss, authURL), append([]*http.Cookie{fck}, cs...)...)
			wantRedirect(t, w, "/profile?oidc_error=expired")
			for _, role := range []string{"plain", "owner"} {
				if u, _ := e.userBy("id", e.users[role]); u.subject != nil {
					t.Fatalf("%s was linked: %+v", role, u)
				}
			}
		})
	}
}

// The user is linked by an admin between starting the flow and the callback:
// the callback must refuse, not report a link that did not happen.
func TestLinkOIDC_LinkedMeanwhile(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	iss.Next(claims("sub-late", "", nil, nil))
	authURL, fck := e.linkStart(t, "plain")
	e.exec(`UPDATE users SET oidc_issuer = ?, oidc_subject = 'sub-admin-set', oidc_linked_at = 1 WHERE id = ?`, iss.URL, e.users["plain"])
	w := e.callback(e.callbackURL(t, iss, authURL), fck, e.sessionCookie("plain"))
	wantRedirect(t, w, "/profile?oidc_error=already_linked")
	if u, _ := e.userBy("id", e.users["plain"]); u.subject == nil || *u.subject != "sub-admin-set" {
		t.Fatalf("the admin's link was replaced: %+v", u)
	}
	if e.auditCount("oidc.linked") != 0 {
		t.Fatal("a refused link was audited")
	}
}

func TestLinkOIDC_UnverifiedEmailOK(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	iss.Next(claims("sub-u", "whatever@example.com", false, nil))
	authURL, fck := e.linkStart(t, "lister")
	wantRedirect(t, e.callback(e.callbackURL(t, iss, authURL), fck, e.sessionCookie("lister")), "/profile?oidc=linked")
	if u, _ := e.userBy("id", e.users["lister"]); u.subject == nil || *u.subject != "sub-u" || u.email != nil {
		t.Fatalf("lister: %+v", u)
	}
}

func adminPath(id int64, op string) string { return fmt.Sprintf("/api/v1/admin/users/%d/%s", id, op) }

func TestAdminLink(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	post := func(role string, id int64, body string) int {
		return e.do(req{method: "POST", path: adminPath(id, "link"), role: role, body: body}).Code
	}
	if got := post("plain", e.users["owner"], `{"subject":"s-1"}`); got != http.StatusForbidden {
		t.Fatalf("non-admin: %d, want 403", got)
	}
	if got := post("admin", e.users["owner"], `{"subject":"s-1"}`); got != http.StatusNoContent {
		t.Fatalf("link: %d, want 204", got)
	}
	u, _ := e.userBy("id", e.users["owner"])
	if u.subject == nil || *u.subject != "s-1" || *u.issuer != iss.URL || u.notice == nil || *u.notice != "oidc_linked" {
		t.Fatalf("owner: %+v", u)
	}
	var actor int64
	var details string
	_ = e.db.QueryRow(`SELECT actor_id, details FROM audit_log WHERE action = 'oidc.linked'`).Scan(&actor, &details)
	if actor != e.users["admin"] || details != `{"method":"admin","subject":"s-1"}` || e.auditCount("oidc.linked") != 1 {
		t.Fatalf("audit: actor %d %s", actor, details)
	}
	for name, c := range map[string]struct {
		id   int64
		body string
		want int
	}{
		"subject linked to another user": {e.users["lister"], `{"subject":"s-1"}`, http.StatusConflict},
		"user already linked":            {e.users["owner"], `{"subject":"s-2"}`, http.StatusConflict},
		"unknown user":                   {9999, `{"subject":"s-3"}`, http.StatusNotFound},
		"empty subject":                  {e.users["lister"], `{"subject":""}`, http.StatusBadRequest},
		"long subject":                   {e.users["lister"], `{"subject":"` + strings.Repeat("x", 257) + `"}`, http.StatusBadRequest},
	} {
		if got := post("admin", c.id, c.body); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if e.auditCount("oidc.linked") != 1 {
		t.Fatal("a refused link was audited as a link")
	}
	if got := e.do(req{method: "POST", path: adminPath(e.users["lister"], "link"), role: "admin", body: `{"subject":"s-9"}`, bare: true}).Code; got != http.StatusForbidden {
		t.Fatalf("no CSRF header: %d, want 403", got)
	}
	if got := post("admin", e.users["disabled"], `{"subject":"s-5"}`); got != http.StatusNoContent {
		t.Fatalf("admins may link a disabled account too: %d", got)
	}
	if got := post("admin", e.users["lister"], `{"subject":"s-4"}`); got != http.StatusNoContent {
		t.Fatalf("second link: %d", got)
	}
	// Linking is refused when OIDC is not configured.
	if got := newEnv(t).do(req{method: "POST", path: adminPath(2, "link"), role: "admin", body: `{"subject":"s"}`}).Code; got != http.StatusNotFound {
		t.Fatalf("OIDC off: %d, want 404", got)
	}
}

// Unlink must stay unlinked: the user's address is cleared, so the next login
// with a verified email equal to it cannot take rule 2 back to the account.
func TestAdminUnlink_NotRelinkedByEmail(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	e.exec(`UPDATE users SET email = 'plain@example.com', oidc_issuer = ?, oidc_subject = 'sub-old', oidc_linked_at = 1 WHERE id = ?`,
		iss.URL, e.users["plain"])
	if got := e.do(req{method: "POST", path: adminPath(e.users["plain"], "unlink"), role: "admin"}).Code; got != http.StatusNoContent {
		t.Fatalf("unlink: %d, want 204", got)
	}
	w := e.oidcLogin(t, iss, claims("sub-new", "plain@example.com", true, map[string]any{"preferred_username": "plain-idp"}))
	wantRedirect(t, w, "/")
	if u, _ := e.userBy("id", e.users["plain"]); u.subject != nil {
		t.Fatalf("the unlinked account was linked again: %+v", u)
	}
}

func TestAdminUnlink(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	e.exec(`UPDATE users SET oidc_issuer = ?, oidc_subject = 'sub-p', oidc_linked_at = 1, admin_oidc = 1 WHERE id = ?`, iss.URL, e.users["plain"])
	post := func(role string, id int64) int {
		return e.do(req{method: "POST", path: adminPath(id, "unlink"), role: role}).Code
	}
	if got := post("owner", e.users["plain"]); got != http.StatusForbidden {
		t.Fatalf("non-admin: %d, want 403", got)
	}
	if code, _ := e.meWith(t, e.sessionCookie("plain")); code != 200 {
		t.Fatal("fixture session invalid")
	}
	if got := post("admin", e.users["plain"]); got != http.StatusNoContent {
		t.Fatalf("unlink: %d, want 204", got)
	}
	u, _ := e.userBy("id", e.users["plain"])
	if u.subject != nil || u.issuer != nil || u.adminOIDC || u.notice == nil || *u.notice != "oidc_unlinked" {
		t.Fatalf("after unlink: %+v", u)
	}
	if code, _ := e.meWith(t, e.sessionCookie("plain")); code != http.StatusUnauthorized {
		t.Fatalf("the unlinked user's session survived: %d", code)
	}
	if e.auditCount("oidc.unlinked") != 1 {
		t.Fatal("unlink not audited once")
	}
	if got := post("admin", e.users["plain"]); got != http.StatusConflict {
		t.Fatalf("second unlink: %d, want 409", got)
	}
	if got := post("admin", 9999); got != http.StatusNotFound {
		t.Fatalf("unknown user: %d, want 404", got)
	}
	if e.auditCount("oidc.unlinked") != 1 {
		t.Fatal("a refused unlink was audited")
	}
	// After the unlink, the old subject is just an unknown identity again.
	wantRedirect(t, e.oidcLogin(t, iss, claims("sub-p", "", nil, map[string]any{"preferred_username": "plain"})), "/")
	if again, _ := e.userBy("oidc_subject", "sub-p"); again.id == e.users["plain"] {
		t.Fatal("the old subject logged in as the unlinked account")
	}
	// An unlink works with OIDC switched off: a leftover link must stay removable.
	off := newEnv(t)
	off.exec(`UPDATE users SET oidc_issuer = 'https://old', oidc_subject = 'x', oidc_linked_at = 1 WHERE id = ?`, off.users["plain"])
	if got := off.do(req{method: "POST", path: adminPath(off.users["plain"], "unlink"), role: "admin"}).Code; got != http.StatusNoContent {
		t.Fatalf("unlink with OIDC off: %d, want 204", got)
	}
}
