package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// audited runs call and reports an error unless it added exactly one audit
// row, of the given action, by the admin, with a target, and no other.
func audited(e *env, action string, call func()) error {
	var total, n int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&total)
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = ?`, action).Scan(&n)
	var maxID int64
	_ = e.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM audit_log`).Scan(&maxID)
	call()
	var total2, n2 int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&total2)
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = ?`, action).Scan(&n2)
	if total2 != total+1 || n2 != n+1 {
		return fmt.Errorf("audit rows %d→%d, %s rows %d→%d, want one new %s row", total, total2, action, n, n2, action)
	}
	var actor *int64
	var target string
	if err := e.db.QueryRow(`SELECT actor_id, target FROM audit_log WHERE id > ? AND action = ?`, maxID, action).Scan(&actor, &target); err != nil {
		return err
	}
	if actor == nil || *actor != e.users["admin"] || target == "" {
		return fmt.Errorf("actor %v, target %q", actor, target)
	}
	return nil
}

// quiet runs call and reports an error if it wrote any audit row.
func quiet(e *env, call func()) error {
	var total, total2 int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&total)
	call()
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&total2)
	if total2 != total {
		return fmt.Errorf("a refused call wrote %d audit rows", total2-total)
	}
	return nil
}

func idOf(t testing.TB, m map[string]any) int64 {
	t.Helper()
	f, ok := m["id"].(float64)
	if !ok {
		t.Fatalf("no id in %v", m)
	}
	return int64(f)
}

func TestEveryMutationAudited(t *testing.T) {
	const cat = "/api/v1/admin/categories/"
	type op struct {
		action string
		setup  func(e *env) func() // returns the call under test
	}
	want := func(e *env, method, url, body string, code int) func() {
		return func() {
			e.t.Helper()
			if got, b := e.admin(method, url, body); got != code {
				e.t.Errorf("%s %s: %d %v, want %d", method, url, got, b, code)
			}
		}
	}
	ops := []op{
		{"user.created", func(e *env) func() {
			return want(e, "POST", "/api/v1/admin/users", `{"username":"zed","password":"correct horse"}`, 201)
		}},
		{"user.updated", func(e *env) func() {
			return want(e, "PATCH", userURL(e.users["plain"]), `{"can_list_files":true}`, 200)
		}},
		{"user.deleted", func(e *env) func() { return want(e, "DELETE", userURL(e.users["plain"]), ``, 204) }},
		{"user.sessions_revoked", func(e *env) func() {
			return want(e, "POST", userURL(e.users["plain"])+"/revoke-sessions", ``, 204)
		}},
		{"path.assigned", func(e *env) func() {
			return func() {
				if code, b := e.createPath(e.users["plain"], 1, "ab"); code != 201 {
					e.t.Errorf("%d %v", code, b)
				}
			}
		}},
		{"path.removed", func(e *env) func() {
			_, p := e.createPath(e.users["plain"], 1, "ab")
			return want(e, "DELETE", fmt.Sprintf("%s/%d", pathsURL(e.users["plain"]), idOf(e.t, p)), ``, 204)
		}},
		{"root.created", func(e *env) func() {
			e.mounts("/mnt/storage")
			return func() {
				if code, b := e.createRoot("storage", "/mnt/storage", ""); code != 201 {
					e.t.Errorf("%d %v", code, b)
				}
			}
		}},
		{"root.updated", func(e *env) func() {
			e.mounts("/mnt/storage")
			_, r := e.createRoot("storage", "/mnt/storage", "")
			return want(e, "PATCH", fmt.Sprintf("%s/%d", rootsURL, idOf(e.t, r)), `{"name":"renamed"}`, 200)
		}},
		{"root.deleted", func(e *env) func() {
			e.mounts("/mnt/storage")
			_, r := e.createRoot("storage", "/mnt/storage", "")
			return want(e, "DELETE", fmt.Sprintf("%s/%d", rootsURL, idOf(e.t, r)), ``, 204)
		}},
		{"scan.requested", func(e *env) func() { return want(e, "POST", "/api/v1/scans", `{"root_id":1}`, 202) }},
		{"scan.cancel_requested", func(e *env) func() {
			id := e.insertScanJob(1, "queued", nil)
			return want(e, "POST", fmt.Sprintf("/api/v1/scans/%d/cancel", id), ``, 204)
		}},
		{"category.put", func(e *env) func() { return want(e, "PUT", cat+"mp4", `{"category":"audio"}`, 200) }},
		{"category.deleted", func(e *env) func() { return want(e, "DELETE", cat+"mp4", ``, 204) }},
		{"settings.updated", func(e *env) func() {
			return want(e, "PUT", "/api/v1/admin/settings", `{"top_n":"7"}`, 200)
		}},
	}
	for _, o := range ops {
		t.Run(o.action, func(t *testing.T) {
			e := newEnv(t)
			if err := audited(e, o.action, o.setup(e)); err != nil {
				t.Fatal(err)
			}
		})
	}

	// Negative control: a call that writes no row must fail the check.
	t.Run("negative control", func(t *testing.T) {
		e := newEnv(t)
		if err := audited(e, "user.created", want(e, "GET", "/api/v1/admin/users", ``, 200)); err == nil {
			t.Fatal("the audit check passed for a call that audited nothing")
		}
	})

	refused := []struct {
		name  string
		setup func(e *env) func()
	}{
		{"duplicate user", func(e *env) func() {
			return want(e, "POST", "/api/v1/admin/users", `{"username":"owner","password":"correct horse"}`, 409)
		}},
		{"unknown user update", func(e *env) func() { return want(e, "PATCH", userURL(999), `{"disabled":true}`, 404) }},
		{"delete self", func(e *env) func() { return want(e, "DELETE", userURL(e.users["admin"]), ``, 409) }},
		{"unknown user revoke", func(e *env) func() { return want(e, "POST", userURL(999)+"/revoke-sessions", ``, 404) }},
		{"overlapping path", func(e *env) func() {
			return func() {
				if code, _ := e.createPath(e.users["plain"], 1, "a/b"); code != 422 {
					e.t.Errorf("overlap: %d", code)
				}
			}
		}},
		{"unknown path delete", func(e *env) func() {
			return want(e, "DELETE", pathsURL(e.users["plain"])+"/999", ``, 404)
		}},
		{"root not a mount", func(e *env) func() {
			return func() {
				if code, _ := e.createRoot("x", "/mnt/nowhere", ""); code != 422 {
					e.t.Errorf("not a mount: %d", code)
				}
			}
		}},
		{"unknown root update", func(e *env) func() { return want(e, "PATCH", rootsURL+"/999", `{"name":"q"}`, 404) }},
		{"unknown root delete", func(e *env) func() { return want(e, "DELETE", rootsURL+"/999", ``, 404) }},
		{"scan unknown root", func(e *env) func() { return want(e, "POST", "/api/v1/scans", `{"root_id":999}`, 404) }},
		{"cancel unknown", func(e *env) func() { return want(e, "POST", "/api/v1/scans/999/cancel", ``, 404) }},
		{"bad category", func(e *env) func() { return want(e, "PUT", cat+"mp4", `{"category":"nope"}`, 400) }},
		{"missing category delete", func(e *env) func() { return want(e, "DELETE", cat+"nosuchext", ``, 404) }},
		{"bad setting", func(e *env) func() { return want(e, "PUT", "/api/v1/admin/settings", `{"top_n":"0"}`, 422) }},
	}
	for _, r := range refused {
		t.Run("refused/"+r.name, func(t *testing.T) {
			e := newEnv(t)
			if err := quiet(e, r.setup(e)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdminCursors_Tampered(t *testing.T) {
	e := newEnv(t)
	enc := func(v any) string { return encodeCursor(v) }
	good := enc(idCursor{V: cursorVersion, ID: 5})
	bad := []string{
		"!!!notbase64", good[:len(good)-3], enc(map[string]any{"v": 2, "id": 5}),
		enc(scanCursor{V: cursorVersion, Root: 1, StartedAt: 1, ID: 1}), enc(map[string]any{"v": 1, "id": "x"}),
	}
	for _, c := range []string{enc(map[string]any{"v": 1}), enc(idCursor{V: cursorVersion, ID: -5})} {
		if code, _ := e.admin("GET", "/api/v1/admin/audit?cursor="+c, ``); code != http.StatusBadRequest {
			t.Errorf("audit cursor %q: %d", c, code)
		}
	}
	for _, url := range []string{"/api/v1/admin/users", "/api/v1/admin/audit"} {
		for _, c := range bad {
			if code, _ := e.admin("GET", url+"?cursor="+c, ``); code != http.StatusBadRequest {
				t.Errorf("%s cursor %q: %d", url, c, code)
			}
		}
	}
	scanBad := []string{"!!!notbase64", enc(map[string]any{"v": 2, "root": 0, "started_at": 1, "id": 1}), enc(map[string]any{"v": 1, "bogus": 1}), enc(map[string]any{"v": 1, "root": 0, "started_at": 1, "id": 0})}
	for _, c := range scanBad {
		if code, _ := e.admin("GET", "/api/v1/admin/scans?cursor="+c, ``); code != http.StatusBadRequest {
			t.Errorf("scans cursor %q: %d", c, code)
		}
	}
}

func TestSetup_StillWorksAfterM8(t *testing.T) {
	e := newEnv(t)
	e.clearAdmins()
	w := e.do(req{method: "POST", path: "/api/v1/setup", body: setupBody("boss", "long enough pw")})
	if w.Code != 200 {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	// The new admin logs in with the password and can create a user.
	lw := login(e, "boss", "long enough pw")
	if lw.Code != 200 {
		t.Fatalf("login: %d %s", lw.Code, lw.Body)
	}
	c := cookieOf(lw)
	if c == nil {
		t.Fatal("no session cookie")
	}
	cw := e.do(req{method: "POST", path: "/api/v1/admin/users", header: map[string]string{"Cookie": c.Name + "=" + c.Value}, body: `{"username":"second","password":"correct horse"}`})
	if cw.Code != 201 {
		t.Fatalf("create user: %d %s", cw.Code, cw.Body)
	}
	var u map[string]any
	_ = json.Unmarshal(cw.Body.Bytes(), &u)
	if w := e.do(req{method: "POST", path: "/api/v1/setup", body: setupBody("again", "long enough pw")}); w.Code != 404 {
		t.Fatalf("setup after an admin exists: %d", w.Code)
	}
}
