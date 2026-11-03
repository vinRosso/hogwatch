package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func meOf2(t *testing.T, e *env, role string) map[string]any {
	t.Helper()
	w := e.do(req{method: "GET", path: "/api/v1/me", role: role})
	if w.Code != 200 {
		t.Fatalf("GET /me as %s: %d %s", role, w.Code, w.Body)
	}
	var me map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	return me
}

// admin_oidc alone grants nothing: only a session created by an OIDC login
// carries the right, and then for 12 h.
func TestSession_PasswordLoginOfOIDCAdmin(t *testing.T) {
	e := newEnv(t)
	id := e.addUser("olga", "correct horse", false, false)
	e.exec(`UPDATE users SET admin_oidc = 1 WHERE id = ?`, id)

	w := login(e, "olga", "correct horse")
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	e.cookies["olga"] = cookieOf(w).Value
	me := meOf2(t, e, "olga")
	if me["is_admin"] != false || me["admin_oidc"] != true {
		t.Fatalf("password session: %v", me)
	}
	if got := e.do(req{method: "GET", path: "/api/v1/admin/users", role: "olga"}).Code; got != http.StatusForbidden {
		t.Fatalf("admin route with a password session: %d, want 403", got)
	}

	// Control: the same user in an OIDC session is admin (200).
	tok, _, err := e.sessions.Create(t.Context(), id, true, "192.0.2.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	e.cookies["olga-oidc"] = tok
	if meOf2(t, e, "olga-oidc")["is_admin"] != true {
		t.Fatal("OIDC session is not admin")
	}
	if got := e.do(req{method: "GET", path: "/api/v1/admin/users", role: "olga-oidc"}).Code; got != http.StatusOK {
		t.Fatalf("admin route with an OIDC session: %d, want 200", got)
	}
}

// A password change starts a plain session: it must not carry the
// OIDC admin right, or an OIDC admin could renew 12 h admin sessions forever
// without another identity-provider login.
func TestSession_PasswordChangeOfOIDCAdminIsPlain(t *testing.T) {
	e := newEnv(t)
	id := e.addUser("olga", "correct horse", false, false)
	e.exec(`UPDATE users SET admin_oidc = 1 WHERE id = ?`, id)
	tok, _, err := e.sessions.Create(t.Context(), id, true, "192.0.2.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	e.cookies["olga"] = tok
	if meOf2(t, e, "olga")["is_admin"] != true {
		t.Fatal("fixture: OIDC session is not admin")
	}
	w := e.do(req{method: "POST", path: "/api/v1/me/password", role: "olga",
		body: `{"current":"correct horse","new":"another long passphrase"}`})
	if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Fatalf("password change: %d %s", w.Code, w.Body)
	}
	c := cookieOf(w)
	if c == nil {
		t.Fatal("no new session cookie after the password change")
	}
	e.cookies["olga"] = c.Value
	if me := meOf2(t, e, "olga"); me["is_admin"] != false {
		t.Fatalf("session after a password change is admin: %v", me)
	}
}

func TestMe_Notice(t *testing.T) {
	e := newEnv(t)
	if meOf2(t, e, "plain")["notice"] != nil {
		t.Fatal("fresh user has a notice")
	}
	e.exec(`UPDATE users SET notice = 'oidc_linked' WHERE id = ?`, e.users["plain"])
	for i := 0; i < 2; i++ { // reading does not consume it
		if got := meOf2(t, e, "plain")["notice"]; got != "oidc_linked" {
			t.Fatalf("read %d: notice = %v", i, got)
		}
	}
	if got := e.do(req{method: "DELETE", path: "/api/v1/me/notice", role: "plain", bare: true}).Code; got != http.StatusForbidden {
		t.Fatalf("DELETE without the CSRF header: %d, want 403", got)
	}
	if meOf2(t, e, "plain")["notice"] != "oidc_linked" {
		t.Fatal("a CSRF-less DELETE cleared the notice")
	}
	if got := e.do(req{method: "DELETE", path: "/api/v1/me/notice", role: "anon"}).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous DELETE: %d, want 401", got)
	}
	if got := e.do(req{method: "DELETE", path: "/api/v1/me/notice", role: "plain"}).Code; got != http.StatusNoContent {
		t.Fatalf("DELETE: %d, want 204", got)
	}
	if meOf2(t, e, "plain")["notice"] != nil {
		t.Fatal("notice survived the DELETE")
	}
	// Another user's notice is untouched.
	e.exec(`UPDATE users SET notice = 'oidc_unlinked' WHERE id = ?`, e.users["owner"])
	if got := e.do(req{method: "DELETE", path: "/api/v1/me/notice", role: "plain"}).Code; got != http.StatusNoContent {
		t.Fatalf("second DELETE: %d", got)
	}
	if meOf2(t, e, "owner")["notice"] != "oidc_unlinked" {
		t.Fatal("DELETE /me/notice cleared another user's notice")
	}
}

func prefsOfMe(t *testing.T, e *env, role string) map[string]any {
	t.Helper()
	p, ok := meOf2(t, e, role)["prefs"].(map[string]any)
	if !ok {
		t.Fatal("GET /me has no prefs object")
	}
	return p
}

// Preferences belong to the account: a patch changes only the keys sent, survives a new
// session, and never reaches another user.
func TestPrefs_PatchMergesPerAccount(t *testing.T) {
	e := newEnv(t)
	if got := prefsOfMe(t, e, "plain"); len(got) != 0 {
		t.Fatalf("fresh account prefs = %v, want none", got)
	}
	patch := func(role, body string) *httptest.ResponseRecorder {
		return e.do(req{method: "PATCH", path: "/api/v1/me/prefs", role: role, body: body})
	}
	if w := patch("plain", `{"depth":4,"usage_window":"7d","panel_width":520,"root_id":2}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body)
	}
	if w := patch("plain", `{"depth":3}`); w.Code != http.StatusOK {
		t.Fatalf("second PATCH: %d %s", w.Code, w.Body)
	}
	got := prefsOfMe(t, e, "plain")
	if got["depth"] != float64(3) || got["usage_window"] != "7d" || got["panel_width"] != float64(520) || got["root_id"] != float64(2) {
		t.Fatalf("prefs after two patches = %v, want depth 3 and usage_window 7d kept", got)
	}
	if other := prefsOfMe(t, e, "owner"); len(other) != 0 {
		t.Fatalf("another account's prefs = %v, want none", other)
	}
	// A new session of the same account sees them.
	tok, _, err := e.sessions.Create(t.Context(), e.users["plain"], false, "192.0.2.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	e.cookies["plain2"] = tok
	if got := prefsOfMe(t, e, "plain2"); got["depth"] != float64(3) {
		t.Fatalf("prefs in a new session = %v", got)
	}
}

func TestPrefs_RejectsBadValues(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{`{"depth":0}`, `{"depth":6}`, `{"history_window":"2y"}`, `{"usage_window":"1y"}`, `{"admin_window":"all"}`, `{"panel_width":319}`, `{"panel_width":901}`, `{"root_id":0}`} {
		if w := e.do(req{method: "PATCH", path: "/api/v1/me/prefs", role: "plain", body: body}); w.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s: %d, want 400", body, w.Code)
		}
	}
	if got := prefsOfMe(t, e, "plain"); len(got) != 0 {
		t.Fatalf("a refused patch stored %v", got)
	}
	if got := e.do(req{method: "PATCH", path: "/api/v1/me/prefs", role: "anon", body: `{"depth":2}`}).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous PATCH: %d, want 401", got)
	}
}
