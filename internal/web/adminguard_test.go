package web

import (
	"context"
	"net/http"
	"testing"

	"github.com/vinRosso/hogwatch/internal/auth/oidc/oidctest"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

func TestEnabledAdmins(t *testing.T) {
	e := newEnv(t) // one enabled local admin
	e.exec(`INSERT INTO users (username, created_at, admin_oidc) VALUES ('oidc-only', 1, 1)`)
	e.exec(`INSERT INTO users (username, created_at, admin_oidc, disabled) VALUES ('oidc-off', 1, 1, 1)`)
	q := dbq.New(e.db)
	ctx := context.Background()
	if n, err := enabledAdmins(ctx, q, true); err != nil || n != 2 {
		t.Fatalf("OIDC on: %d, %v; want the local admin and the enabled admin_oidc user", n, err)
	}
	if n, err := enabledAdmins(ctx, q, false); err != nil || n != 1 {
		t.Fatalf("OIDC off: %d, %v; want only the local admin", n, err)
	}
}

// Without OIDC the unlink guard counts local admins only; unlinking a
// stale admin_oidc user leaves the local admin and is allowed.
func TestUnlink_GuardIgnoresOIDCWhenOff(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE users SET admin_oidc = 1, oidc_issuer = 'https://old', oidc_subject = 'x', oidc_linked_at = 1 WHERE id = ?`, e.users["plain"])
	if got := e.do(req{method: "POST", path: adminPath(e.users["plain"], "unlink"), role: "admin"}).Code; got != http.StatusNoContent {
		t.Fatalf("unlink of a stale admin_oidc user: %d, want 204", got)
	}
}

func TestUnlink_LastAdminRefusedWithOIDC(t *testing.T) {
	iss := oidctest.New(t)
	e := newEnv(t, withOIDC(iss))
	// The only admin is an OIDC admin; unlinking clears admin_oidc, so it is refused.
	e.exec(`UPDATE users SET admin_local = 0 WHERE id = ?`, e.users["admin"])
	e.exec(`UPDATE users SET admin_oidc = 1, oidc_issuer = ?, oidc_subject = 'a', oidc_linked_at = 1 WHERE id = ?`, iss.URL, e.users["admin"])
	e.exec(`UPDATE sessions SET oidc_admin = 1 WHERE user_id = ?`, e.users["admin"])
	if got := e.do(req{method: "POST", path: adminPath(e.users["admin"], "unlink"), role: "admin"}).Code; got != http.StatusConflict {
		t.Fatalf("unlink of the last admin: %d, want 409", got)
	}
	if u, _ := e.userBy("id", e.users["admin"]); u.subject == nil || !u.adminOIDC {
		t.Fatalf("a refused unlink changed the user: %+v", u)
	}
}
