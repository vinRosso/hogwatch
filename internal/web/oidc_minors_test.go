package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/vinRosso/hogwatch/internal/auth/oidc"
	"github.com/vinRosso/hogwatch/internal/auth/oidc/oidctest"
)

// auditDetails returns the details JSON of the newest audit row of an action.
func (e *env) auditDetails(action string) string {
	e.t.Helper()
	var d string
	if err := e.db.QueryRow(`SELECT details FROM audit_log WHERE action = ? ORDER BY id DESC LIMIT 1`, action).Scan(&d); err != nil {
		e.t.Fatalf("audit %s: %v", action, err)
	}
	return d
}

func TestOIDC_RefusalAuditNamesTheIdentity(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	e.exec(`UPDATE users SET email = 'plain@example.com' WHERE id = ?`, e.users["plain"])
	wantRefused(t, e.oidcLogin(t, iss, claims("sub-who", "plain@example.com", false, nil)), "ask_admin")
	d := e.auditDetails("auth.oidc_refused")
	if !strings.Contains(d, `"subject":"sub-who"`) || !strings.Contains(d, `"issuer":"`+iss.URL+`"`) || !strings.Contains(d, "ask_admin") {
		t.Fatalf("refusal audit does not name the identity: %s", d)
	}
}

func TestOIDC_DisplayNameLosesControlCharacters(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	wantRedirect(t, e.oidcLogin(t, iss, claims("sub-n", "", nil, map[string]any{
		"preferred_username": "nina", "name": "Ni\x00na\n Rossi\t\x1b",
	})), "/")
	u, _ := e.userBy("oidc_subject", "sub-n")
	if u.display != "Nina Rossi" {
		t.Fatalf("display name = %q", u.display)
	}
}

// Fifty accounts already hold alice, alice-2 to alice-50: the next alice
// fails cleanly, creating nothing and setting no session.
func TestOIDC_UsernameExhaustion(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	names := []string{"alice"}
	for n := 2; n <= 50; n++ {
		names = append(names, oidc.WithSuffix("alice", n))
	}
	for _, name := range names {
		e.exec(`INSERT INTO users (username, display_name, created_at) VALUES (?, ?, 1)`, name, name)
	}
	before := e.userCount()
	wantRefused(t, e.oidcLogin(t, iss, claims("sub-late", "", nil, map[string]any{"preferred_username": "alice"})), "failed")
	if e.userCount() != before {
		t.Fatal("an exhausted username search still created a user")
	}
}

// With two flow cookies in one request the first one counts, so a wrong
// first cookie fails closed even when a second one matches.
func TestOIDC_DuplicateFlowCookies(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	iss.Next(claims("sub-dup", "", nil, map[string]any{"preferred_username": "dup"}))
	authURL, good, _ := e.start(t)
	cb := e.callbackURL(t, iss, authURL)
	bad := &http.Cookie{Name: flowCookieName, Value: "not-the-state"} //nolint:gosec // a request cookie
	wantRefused(t, e.callback(cb, bad, good), "expired")
	if _, ok := e.userBy("oidc_subject", "sub-dup"); ok {
		t.Fatal("a callback with a forged first cookie created a user")
	}
}

// The mismatched browser must not cost an identity-provider round trip.
func TestLinkOIDC_OtherSessionNeverReachesTheProvider(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	iss.Next(claims("sub-x", "", nil, nil))
	authURL, fck := e.linkStart(t, "plain")
	before := iss.TokenRequests()
	wantRedirect(t, e.callback(e.callbackURL(t, iss, authURL), fck, e.sessionCookie("owner")), "/profile?oidc_error=expired")
	if got := iss.TokenRequests(); got != before {
		t.Fatalf("the token endpoint was called %d time(s) for a mismatched session", got-before)
	}
}

func TestLinkOIDC_AuditNamesTheIdentity(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	iss.Next(claims("sub-prof", "", nil, nil))
	authURL, fck := e.linkStart(t, "plain")
	wantRedirect(t, e.callback(e.callbackURL(t, iss, authURL), fck, e.sessionCookie("plain")), "/profile?oidc=linked")
	if d := e.auditDetails("oidc.linked"); !strings.Contains(d, `"subject":"sub-prof"`) || !strings.Contains(d, "profile") {
		t.Fatalf("profile link audit: %s", d)
	}
}

func TestAdminUnlink_AuditNamesTheRemovedIdentity(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	e.exec(`UPDATE users SET oidc_issuer = ?, oidc_subject = 'sub-gone', oidc_linked_at = 1 WHERE id = ?`, iss.URL, e.users["plain"])
	if got := e.do(req{method: "POST", path: adminPath(e.users["plain"], "unlink"), role: "admin"}).Code; got != http.StatusNoContent {
		t.Fatalf("unlink: %d", got)
	}
	if d := e.auditDetails("oidc.unlinked"); !strings.Contains(d, `"subject":"sub-gone"`) {
		t.Fatalf("unlink audit: %s", d)
	}
}

// An admin whose only admin right is OIDC cannot unlink themself (or the last
// other admin) and lock everyone out of the admin API.
func TestAdminUnlink_KeepsAnAdministrator(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	e.exec(`UPDATE users SET admin_local = 0, admin_oidc = 0`)
	e.exec(`UPDATE users SET admin_oidc = 1, oidc_issuer = ?, oidc_subject = 'sub-last', oidc_linked_at = 1 WHERE id = ?`, iss.URL, e.users["plain"])
	tok, _, err := e.sessions.Create(t.Context(), e.users["plain"], true, "192.0.2.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	e.cookies["lastadmin"] = tok
	if got := e.do(req{method: "POST", path: adminPath(e.users["plain"], "unlink"), role: "lastadmin"}).Code; got != http.StatusConflict {
		t.Fatalf("unlinking the last administrator: %d, want 409", got)
	}
	u, _ := e.userBy("id", e.users["plain"])
	if u.subject == nil || !u.adminOIDC || u.notice != nil {
		t.Fatalf("a refused unlink changed the account: %+v", u)
	}
	if e.auditCount("oidc.unlinked") != 0 {
		t.Fatal("a refused unlink was audited")
	}
	// With another enabled administrator the same unlink goes through.
	e.exec(`UPDATE users SET admin_local = 1 WHERE id = ?`, e.users["admin"])
	if got := e.do(req{method: "POST", path: adminPath(e.users["plain"], "unlink"), role: "admin"}).Code; got != http.StatusNoContent {
		t.Fatalf("unlink with another administrator left: %d, want 204", got)
	}
}

func TestAdminLink_SubjectShape(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	for name, subj := range map[string]string{
		"leading space": " s", "trailing newline": "s\n", "control": "s\x00t", "empty": "",
		"too long": strings.Repeat("s", 257),
	} {
		b, _ := json.Marshal(map[string]string{"subject": subj})
		w := e.do(req{method: "POST", path: adminPath(e.users["plain"], "link"), role: "admin", body: string(b)})
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	if u, _ := e.userBy("id", e.users["plain"]); u.subject != nil {
		t.Fatal("a malformed subject was linked")
	}
}

// Two links of one subject to two users at once: one wins, the other gets a
// 409, and nobody gets a 500.
func TestAdminLink_Concurrent(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	ids := []int64{e.users["plain"], e.users["owner"]}
	codes := make([]int, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.do(req{method: "POST", path: adminPath(id, "link"), role: "admin", body: `{"subject":"sub-race"}`}).Code
		}()
	}
	wg.Wait()
	n204, n409 := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusNoContent:
			n204++
		case http.StatusConflict:
			n409++
		}
	}
	if n204 != 1 || n409 != 1 {
		t.Fatalf("codes = %v, want one 204 and one 409", codes)
	}
}
