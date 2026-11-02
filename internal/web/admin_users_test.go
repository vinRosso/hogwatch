package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func (e *env) auditTotal() int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) sessionCount(user int64) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM sessions WHERE user_id = ?`, user).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) admin(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	w := e.do(req{method: method, path: path, role: "admin", body: body})
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func userURL(id int64) string { return fmt.Sprintf("/api/v1/admin/users/%d", id) }

func TestCreateUser(t *testing.T) {
	e := newEnv(t)
	code, u := e.admin("POST", "/api/v1/admin/users",
		`{"username":" Dave ","password":"correct horse","email":"Dave@Example.com","can_list_files":true,"quota_bytes":1000,"quota_basis":"logical"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, u)
	}
	if u["username"] != "dave" || u["has_password"] != true || u["email"] != "Dave@Example.com" ||
		u["can_list_files"] != true || u["quota_bytes"] != float64(1000) || u["quota_basis"] != "logical" ||
		u["admin_local"] != false || u["oidc_linked"] != false {
		t.Fatalf("body %v", u)
	}
	id := int64(u["id"].(float64))
	if code, got := e.admin("GET", userURL(id), ""); code != 200 || fmt.Sprint(got) != fmt.Sprint(u) {
		t.Fatalf("GET differs from the create body: %d %v", code, got)
	}
	if w := login(e, "dave", "correct horse"); w.Code != 200 {
		t.Fatalf("the new user cannot log in: %d", w.Code)
	}
	if e.auditCount("user.created") != 1 {
		t.Fatal("create not audited once")
	}
	// A body without a password is refused and inserts nothing.
	n, a := e.userCount(), e.auditTotal()
	if code, _ := e.admin("POST", "/api/v1/admin/users", `{"username":"erin"}`); code != http.StatusBadRequest {
		t.Fatalf("no password: %d, want 400", code)
	}
	for _, body := range []string{
		`{"username":"dave","password":"another pass"}`,
		`{"username":"erin","password":"another pass","email":"DAVE@example.com"}`,
	} {
		if code, _ := e.admin("POST", "/api/v1/admin/users", body); code != http.StatusConflict {
			t.Fatalf("%s: %d, want 409", body, code)
		}
	}
	if e.userCount() != n || e.auditTotal() != a {
		t.Fatal("a refused create changed state")
	}
}

func TestUserInput_Validation(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("a", 65)
	for _, body := range []string{
		`{"username":"Bad Name","password":"correct horse"}`,
		`{"username":"` + long + `","password":"correct horse"}`,
		`{"username":"ok","password":"short"}`,
		`{"username":"ok","password":"correct horse","email":"a\nb@x"}`,
		`{"username":"ok","password":"correct horse","email":"nope"}`,
		`{"username":"ok","password":"correct horse","quota_bytes":-1}`,
		`{"username":"ok","password":"correct horse","display_name":"a\u0000b"}`,
		`{"username":"ok","password":"correct horse","quota_basis":"nope"}`,
		`not json`,
	} {
		n, a := e.userCount(), e.auditTotal()
		if code, _ := e.admin("POST", "/api/v1/admin/users", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, code)
		}
		if e.userCount() != n || e.auditTotal() != a {
			t.Errorf("%s changed state", body)
		}
	}
}

func TestUpdateUser(t *testing.T) {
	e := newEnv(t)
	id := e.users["plain"]
	set := func(body string) (int, map[string]any) { return e.admin("PATCH", userURL(id), body) }

	if code, u := set(`{"display_name":"Plain P","can_list_files":true,"quota_bytes":5,"email":"p@x.org"}`); code != 200 ||
		u["display_name"] != "Plain P" || u["can_list_files"] != true || u["quota_bytes"] != float64(5) || u["email"] != "p@x.org" {
		t.Fatalf("update: %d %v", code, u)
	}
	if e.sessionCount(id) != 1 {
		t.Fatal("a can_list_files change must not revoke sessions")
	}
	if code, u := set(`{"quota_bytes":null,"email":null}`); code != 200 || u["quota_bytes"] != nil || u["email"] != nil {
		t.Fatalf("null clears: %d %v", code, u)
	}
	for field, body := range map[string]string{
		"admin_local": `{"admin_local":true}`, "disabled": `{"disabled":true}`, "password": `{"password":"new password"}`,
	} {
		e.exec(`INSERT INTO sessions (id_hash, user_id, created_at, expires_at, last_seen_at) VALUES (randomblob(32), ?, 1, 9999999999, 1)`, id)
		if code, _ := set(body); code != 200 {
			t.Fatalf("%s: %d", field, code)
		}
		if n := e.sessionCount(id); n != 0 {
			t.Fatalf("%s change left %d sessions", field, n)
		}
	}
	if w := login(e, "plain", "new password"); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
		// disabled by the previous step: the login must not succeed.
		t.Fatalf("a disabled user logged in: %d", w.Code)
	}
	d := e.auditDetails("user.updated")
	if strings.Contains(d, "new password") || !strings.Contains(d, `"fields"`) {
		t.Fatalf("audit details %s", d)
	}
	if code, _ := e.admin("PATCH", userURL(9999), `{"disabled":true}`); code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", code)
	}
	a := e.auditTotal()
	if code, _ := set(`{}`); code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d, want 400", code)
	}
	if code, _ := set(`{"email":"no-at-sign"}`); code != http.StatusBadRequest {
		t.Fatalf("bad email: %d, want 400", code)
	}
	if code, _ := set(`{"quota_basis":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("bad basis: %d, want 400", code)
	}
	e.exec(`UPDATE users SET email = 'taken@x.org' WHERE id = ?`, e.users["owner"])
	if code, _ := set(`{"email":"TAKEN@x.org"}`); code != http.StatusConflict {
		t.Fatalf("duplicate email: %d, want 409", code)
	}
	if e.auditTotal() != a {
		t.Fatal("a refused update was audited")
	}
}

func TestLastAdminGuard(t *testing.T) {
	cases := []struct {
		name string
		do   func(e *env) int
	}{
		{"demote self", func(e *env) int {
			c, _ := e.admin("PATCH", userURL(e.users["admin"]), `{"admin_local":false}`)
			return c
		}},
		{"disable self", func(e *env) int {
			c, _ := e.admin("PATCH", userURL(e.users["admin"]), `{"disabled":true}`)
			return c
		}},
		{"delete self", func(e *env) int {
			c, _ := e.admin("DELETE", userURL(e.users["admin"]), "")
			return c
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			a := e.auditTotal()
			if got := c.do(e); got != http.StatusConflict {
				t.Fatalf("%d, want 409", got)
			}
			u, _ := e.userBy("id", e.users["admin"])
			if e.auditTotal() != a || e.sessionCount(e.users["admin"]) != 1 || u.id == 0 {
				t.Fatal("a refused change altered state")
			}
			var local, disabled int
			_ = e.db.QueryRow(`SELECT admin_local, disabled FROM users WHERE id = ?`, e.users["admin"]).Scan(&local, &disabled)
			if local != 1 || disabled != 0 {
				t.Fatal("the admin was changed")
			}
		})
	}

	// Two admins demote each other: the second request is refused.
	t.Run("mutual demotion", func(t *testing.T) {
		e := newEnv(t)
		b := e.addUser("boss2", "correct horse", true, false)
		tok, _, err := e.sessions.Create(t.Context(), b, false, "192.0.2.1", "test")
		if err != nil {
			t.Fatal(err)
		}
		e.cookies["boss2"] = tok
		do := func(role string, target int64) int {
			return e.do(req{method: "PATCH", path: userURL(target), role: role, body: `{"admin_local":false}`}).Code
		}
		if got := do("admin", b); got != 200 {
			t.Fatalf("first demotion: %d, want 200", got)
		}
		// boss2 lost their sessions; a fresh one still must not demote the last admin.
		tok, _, _ = e.sessions.Create(t.Context(), b, false, "192.0.2.1", "test")
		e.cookies["boss2"] = tok
		if got := do("boss2", e.users["admin"]); got != http.StatusForbidden && got != http.StatusConflict {
			t.Fatalf("second demotion: %d, want refusal", got)
		}
		var n int
		_ = e.db.QueryRow(`SELECT count(*) FROM users WHERE admin_local = 1 AND disabled = 0`).Scan(&n)
		if n != 1 {
			t.Fatalf("%d admins left, want 1", n)
		}
	})

	// OIDC off: an admin_oidc user does not count, so the last local admin stays.
	t.Run("admin_oidc ignored when OIDC off", func(t *testing.T) {
		e := newEnv(t)
		e.exec(`INSERT INTO users (username, created_at, admin_oidc) VALUES ('ghost', 1, 1)`)
		if code, _ := e.admin("PATCH", userURL(e.users["admin"]), `{"admin_local":false}`); code != http.StatusConflict {
			t.Fatalf("demote with only an admin_oidc user left: %d, want 409", code)
		}
	})
}

func TestDeleteUser_Cascade(t *testing.T) {
	e := newEnv(t)
	id := e.users["owner"]
	e.exec(`INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files) VALUES (1, ?, 1, 1, 1, 1)`, id)
	e.exec(`INSERT INTO history_totals (ts, scope, user_id, bytes_logical, bytes_alloc, files, tier) VALUES (1, 'user', ?, 1, 1, 1, 'raw')`, id)
	e.exec(`INSERT INTO jobs (kind, root_id, requested_by, status, created_at) VALUES ('listing', 1, ?, 'running', 1)`, id)
	if code, _ := e.admin("DELETE", userURL(id), ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	count := func(q string) int {
		var n int
		_ = e.db.QueryRow(q, id).Scan(&n)
		return n
	}
	if count(`SELECT count(*) FROM sessions WHERE user_id = ?`)+count(`SELECT count(*) FROM user_paths WHERE user_id = ?`)+
		count(`SELECT count(*) FROM usage WHERE user_id = ?`) != 0 {
		t.Fatal("rows of the user survived")
	}
	if count(`SELECT count(*) FROM history_totals WHERE user_id = ?`) != 1 {
		t.Fatal("history must outlive the user")
	}
	var by any
	_ = e.db.QueryRow(`SELECT requested_by FROM jobs WHERE kind = 'listing'`).Scan(&by)
	if by != nil {
		t.Fatalf("requested_by = %v, want NULL", by)
	}
	if e.auditCount("user.deleted") != 1 {
		t.Fatal("delete not audited once")
	}
	if code, _ := e.admin("DELETE", userURL(id), ""); code != http.StatusNotFound {
		t.Fatalf("second delete: %d", code)
	}
}

func TestListUsers_Paging(t *testing.T) {
	e := newEnv(t)
	for i := range 250 {
		e.exec(`INSERT INTO users (username, created_at) VALUES (?, 1)`, fmt.Sprintf("bulk%03d", i))
	}
	want := e.userCount()
	seen := map[float64]bool{}
	path := "/api/v1/admin/users"
	for pages := 0; ; pages++ {
		code, body := e.admin("GET", path, "")
		if code != 200 || pages > 10 {
			t.Fatalf("page %d: %d", pages, code)
		}
		items := body["items"].([]any)
		if len(items) > usersPageSize {
			t.Fatalf("page of %d", len(items))
		}
		for _, it := range items {
			id := it.(map[string]any)["id"].(float64)
			if seen[id] {
				t.Fatalf("user %v twice", id)
			}
			seen[id] = true
		}
		next, _ := body["next_cursor"].(string)
		if next == "" {
			break
		}
		path = "/api/v1/admin/users?cursor=" + next
	}
	if len(seen) != want {
		t.Fatalf("saw %d users, want %d", len(seen), want)
	}
}

func TestRevokeUserSessions(t *testing.T) {
	e := newEnv(t)
	id := e.users["plain"]
	if code, _ := e.admin("POST", userURL(id)+"/revoke-sessions", ""); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	if e.sessionCount(id) != 0 || e.auditCount("user.sessions_revoked") != 1 {
		t.Fatal("sessions or audit wrong")
	}
	if code, _ := e.admin("POST", userURL(9999)+"/revoke-sessions", ""); code != http.StatusNotFound {
		t.Fatalf("unknown user: %d", code)
	}
}

// An admin without a password cannot log in locally, so it does not stand
// between the instance and a lockout.
func TestLastAdminGuard_PasswordlessAdminDoesNotCount(t *testing.T) {
	e := newEnv(t)
	boss := e.addUser("boss2", "correct horse", true, false)
	e.exec(`UPDATE users SET password_hash = NULL WHERE username = ?`, "admin")
	// The fixture admin and this one have no password.
	e.exec(`INSERT INTO users (username, created_at, admin_local) VALUES ('ghost', 1, 1)`)
	if c, _ := e.admin("PATCH", userURL(boss), `{"admin_local":false}`); c != http.StatusConflict {
		t.Fatalf("demoting the only admin who can log in: %d, want 409", c)
	}
	if c, _ := e.admin("DELETE", userURL(boss), ""); c != http.StatusConflict {
		t.Fatalf("deleting the only admin who can log in: %d, want 409", c)
	}
}

// With another admin around the last-admin guard allows it, but disabling your own
// account is still refused.
func TestDisableSelfRefusedWithOtherAdmin(t *testing.T) {
	e := newEnv(t)
	e.addUser("boss2", "correct horse", true, false)
	a := e.auditTotal()
	if code, _ := e.admin("PATCH", userURL(e.users["admin"]), `{"disabled":true}`); code != http.StatusConflict {
		t.Fatalf("%d, want 409", code)
	}
	var disabled int
	_ = e.db.QueryRow(`SELECT disabled FROM users WHERE id = ?`, e.users["admin"]).Scan(&disabled)
	if disabled != 0 || e.auditTotal() != a {
		t.Fatal("a refused change altered state")
	}
}

// Your own role is not yours to change, even with another admin around: the UI locks it,
// and the server now agrees.
func TestChangeOwnRoleRefusedWithOtherAdmin(t *testing.T) {
	e := newEnv(t)
	e.addUser("boss2", "correct horse", true, false)
	a := e.auditTotal()
	if code, _ := e.admin("PATCH", userURL(e.users["admin"]), `{"admin_local":false}`); code != http.StatusConflict {
		t.Fatalf("%d, want 409", code)
	}
	var local int
	_ = e.db.QueryRow(`SELECT admin_local FROM users WHERE id = ?`, e.users["admin"]).Scan(&local)
	if local != 1 || e.auditTotal() != a || e.sessionCount(e.users["admin"]) != 1 {
		t.Fatal("a refused change altered state")
	}
	// Saying what is already true is not a change.
	if code, _ := e.admin("PATCH", userURL(e.users["admin"]), `{"admin_local":true}`); code != 200 {
		t.Fatalf("no-op role: %d, want 200", code)
	}
}
