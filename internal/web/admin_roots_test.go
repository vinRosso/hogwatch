package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const rootsURL = "/api/v1/admin/roots"

// mounts publishes the given container paths as available mounts. A leading
// "!" marks a writable mount.
func (e *env) mounts(paths ...string) {
	e.t.Helper()
	type m struct {
		ContainerPath string `json:"container_path"`
		HostPath      string `json:"host_path"`
		FSType        string `json:"fstype"`
		ReadOnly      bool   `json:"read_only"`
	}
	var ms []m
	for _, p := range paths {
		ro := !strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		ms = append(ms, m{p, "/host" + strings.TrimPrefix(p, "/mnt"), "zfs", ro})
	}
	b, _ := json.Marshal(ms)
	e.exec(`UPDATE scanner_status SET available_mounts = ?`, string(b))
}

func (e *env) createRoot(name, container string, extra string) (int, map[string]any) {
	e.t.Helper()
	body := fmt.Sprintf(`{"name":%q,"container_path":%q%s}`, name, container, extra)
	return e.admin("POST", rootsURL, body)
}

func TestCreateRoot(t *testing.T) {
	e := newEnv(t)
	e.mounts("/mnt/storage")
	code, r := e.createRoot("storage", "/mnt/storage", `,"schedule":"0 3 * * *","excludes":["tmp/","cache"],"cross_fs":false`)
	if code != http.StatusCreated {
		t.Fatalf("%d %v", code, r)
	}
	if r["host_path"] != "/host/storage" || r["schedule"] != "0 3 * * *" || r["enabled"] != true ||
		fmt.Sprint(r["excludes"]) != "[tmp cache]" || r["current_scan_id"] != nil {
		t.Fatalf("body %v", r)
	}
	if e.auditCount("root.created") != 1 {
		t.Fatal("not audited once")
	}
	var found bool
	_, list := e.admin("GET", rootsURL, "")
	for _, it := range list["items"].([]any) {
		if it.(map[string]any)["name"] == "storage" {
			found = true
		}
	}
	if !found {
		t.Fatal("not listed")
	}
}

func TestRootInput_Validation(t *testing.T) {
	many := make([]string, 1001)
	for i := range many {
		many[i] = fmt.Sprintf("d%d", i)
	}
	manyJSON, _ := json.Marshal(many)
	cases := []struct {
		name, container, extra string
		want                   int
	}{
		{"not a mount", "/mnt/nope", ``, 422},
		{"writable mount", "/mnt/w", ``, 422},
		{"bad schedule", "/mnt/ok", `,"schedule":"61 * * * *"`, 422},
		{"garbage schedule", "/mnt/ok", `,"schedule":"bogus"`, 422},
		{"long schedule", "/mnt/ok", `,"schedule":"` + strings.Repeat("*", 129) + `"`, 422},
		{"dot-dot exclude", "/mnt/ok", `,"excludes":["a/../b"]`, 422},
		{"duplicate exclude", "/mnt/ok", `,"excludes":["a","a/"]`, 422},
		{"nul exclude", "/mnt/ok", `,"excludes":["x\u0000"]`, 422},
		{"empty exclude", "/mnt/ok", `,"excludes":[""]`, 422},
		{"too many excludes", "/mnt/ok", `,"excludes":` + string(manyJSON), 422},
		{"empty name", "/mnt/ok", ``, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.mounts("/mnt/ok", "!/mnt/w")
			name := "n"
			if c.want == 400 {
				name = " "
			}
			a, n := e.auditTotal(), e.rootCount()
			if code, b := e.createRoot(name, c.container, c.extra); code != c.want {
				t.Fatalf("%d %v, want %d", code, b, c.want)
			}
			if e.auditTotal() != a || e.rootCount() != n {
				t.Fatal("a refused create changed state")
			}
		})
	}
	t.Run("writable allowed by config", func(t *testing.T) {
		e := newEnv(t, func(d *Deps) { d.Cfg.Scan.AllowWritableMounts = true })
		e.mounts("!/mnt/w")
		if code, b := e.createRoot("w", "/mnt/w", ``); code != http.StatusCreated {
			t.Fatalf("%d %v", code, b)
		}
	})
	t.Run("mount in use and duplicate name", func(t *testing.T) {
		e := newEnv(t)
		e.mounts("/mnt/ok", "/mnt/other")
		if code, _ := e.createRoot("a", "/mnt/ok", ``); code != 201 {
			t.Fatal("first create failed")
		}
		if code, _ := e.createRoot("b", "/mnt/ok", ``); code != 422 {
			t.Fatalf("mount reuse: %d, want 422", code)
		}
		if code, _ := e.createRoot("a", "/mnt/other", ``); code != 409 {
			t.Fatalf("duplicate name: %d, want 409", code)
		}
	})
}

func (e *env) rootCount() int {
	e.t.Helper()
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM scan_roots`).Scan(&n)
	return n
}

func TestRoots_Nesting(t *testing.T) {
	t.Run("inner under a non-crossing outer", func(t *testing.T) {
		e := newEnv(t)
		e.mounts("/mnt/host", "/mnt/host/storage")
		if code, _ := e.createRoot("outer", "/mnt/host", `,"cross_fs":false`); code != 201 {
			t.Fatal("outer")
		}
		if code, b := e.createRoot("inner", "/mnt/host/storage", ``); code != 201 {
			t.Fatalf("inner: %d %v", code, b)
		}
	})
	t.Run("inner under a crossing outer", func(t *testing.T) {
		e := newEnv(t)
		e.mounts("/mnt/host", "/mnt/host/storage")
		e.createRoot("outer", "/mnt/host", `,"cross_fs":true`)
		if code, _ := e.createRoot("inner", "/mnt/host/storage", ``); code != 422 {
			t.Fatalf("%d, want 422", code)
		}
	})
	t.Run("crossing outer after inner", func(t *testing.T) {
		e := newEnv(t)
		e.mounts("/mnt/host", "/mnt/host/storage")
		e.createRoot("inner", "/mnt/host/storage", ``)
		if code, _ := e.createRoot("outer", "/mnt/host", `,"cross_fs":true`); code != 422 {
			t.Fatalf("%d, want 422", code)
		}
	})
	t.Run("patch cross_fs while nested", func(t *testing.T) {
		e := newEnv(t)
		e.mounts("/mnt/host", "/mnt/host/storage")
		_, outer := e.createRoot("outer", "/mnt/host", `,"cross_fs":false`)
		e.createRoot("inner", "/mnt/host/storage", ``)
		id := int64(outer["id"].(float64))
		if code, _ := e.admin("PATCH", fmt.Sprintf("%s/%d", rootsURL, id), `{"cross_fs":true}`); code != 422 {
			t.Fatalf("%d, want 422", code)
		}
	})
	t.Run("sibling prefix is not nesting", func(t *testing.T) {
		e := newEnv(t)
		e.mounts("/mnt/host", "/mnt/hostile")
		e.createRoot("a", "/mnt/host", `,"cross_fs":true`)
		if code, b := e.createRoot("b", "/mnt/hostile", ``); code != 201 {
			t.Fatalf("%d %v", code, b)
		}
	})
}

func TestUpdateRoot(t *testing.T) {
	e := newEnv(t)
	e.mounts("/mnt/ok")
	_, r := e.createRoot("r1", "/mnt/ok", `,"schedule":"@daily","excludes":["a"]`)
	url := fmt.Sprintf("%s/%d", rootsURL, int64(r["id"].(float64)))
	code, u := e.admin("PATCH", url, `{"enabled":false}`)
	if code != 200 || u["enabled"] != false || u["schedule"] != "@daily" || fmt.Sprint(u["excludes"]) != "[a]" || u["name"] != "r1" {
		t.Fatalf("partial patch: %d %v", code, u)
	}
	if code, u := e.admin("PATCH", url, `{"excludes":[],"name":"renamed"}`); code != 200 || fmt.Sprint(u["excludes"]) != "[]" || u["name"] != "renamed" {
		t.Fatalf("clear excludes: %d %v", code, u)
	}
	if !strings.Contains(e.auditDetails("root.updated"), `"fields":["excludes","name"]`) {
		t.Fatalf("details %s", e.auditDetails("root.updated"))
	}
	for body, want := range map[string]int{
		`{}`: 400, `{"schedule":"nope"}`: 422, `{"excludes":["../x"]}`: 422, `{"name":""}`: 400, `{"container_path":"/x"}`: 400,
	} {
		if got, _ := e.admin("PATCH", url, body); got != want {
			t.Errorf("%s: %d, want %d", body, got, want)
		}
	}
	if code, _ := e.admin("PATCH", rootsURL+"/999", `{"enabled":true}`); code != 404 {
		t.Fatalf("unknown root: %d", code)
	}
	e.mounts("/mnt/ok", "/mnt/o2")
	e.createRoot("second", "/mnt/o2", ``)
	if code, _ := e.admin("PATCH", url, `{"name":"second"}`); code != 409 {
		t.Fatalf("rename onto an existing name: %d, want 409", code)
	}
}

func TestDeleteRoot_ActiveScan(t *testing.T) {
	for _, status := range []string{"queued", "running"} {
		e := newEnv(t)
		e.exec(`INSERT INTO jobs (kind, root_id, status, created_at) VALUES ('scan', 1, ?, 1)`, status)
		a := e.auditTotal()
		if code, _ := e.admin("DELETE", rootsURL+"/1", ""); code != http.StatusConflict {
			t.Fatalf("%s: %d, want 409", status, code)
		}
		if e.rootCount() != 1 || e.auditTotal() != a {
			t.Fatalf("%s: a refused delete changed state", status)
		}
	}
}

func TestDeleteRoot_Cascade(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'o', '/mnt/o', '/srv/o')`)
	e.exec(`INSERT INTO config_issues (root_id, kind, details, created_at) VALUES (2, 'cross_root_duplicate', '{"user_path_id":1,"other_root_id":1,"node":"x"}', 1)`)
	e.exec(`INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files) VALUES (1, ?, 1, 1, 1, 1)`, e.users["owner"])
	if code, _ := e.admin("DELETE", rootsURL+"/1", ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	count := func(q string) int {
		var n int
		_ = e.db.QueryRow(q).Scan(&n)
		return n
	}
	for _, q := range []string{
		`SELECT count(*) FROM user_paths WHERE root_id = 1`, `SELECT count(*) FROM scans WHERE root_id = 1`,
		`SELECT count(*) FROM usage WHERE scan_id = 1`, `SELECT count(*) FROM config_issues`,
	} {
		if n := count(q); n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}
	if w := e.do(req{method: "GET", path: "/api/v1/nodes/1.1", role: "admin"}); w.Code != http.StatusNotFound {
		t.Fatalf("node of a deleted root: %d, want 404", w.Code)
	}
	if d := e.auditDetails("root.deleted"); !strings.Contains(d, `"paths_removed":2`) {
		t.Fatalf("details %s", d)
	}
	if code, _ := e.admin("DELETE", rootsURL+"/1", ""); code != 404 {
		t.Fatalf("second delete: %d", code)
	}
}

func TestListAvailableMounts(t *testing.T) {
	e := newEnv(t)
	e.exec(`DELETE FROM scanner_status`) // the scanner never ran
	if code, b := e.admin("GET", "/api/v1/admin/available-mounts", ""); code != 200 || len(b["items"].([]any)) != 0 {
		t.Fatalf("never ran: %d %v", code, b)
	}
	e.exec(`INSERT INTO scanner_status (id, heartbeat_at, version) VALUES (1, 1, 't')`)
	e.mounts("/mnt/used", "/mnt/free")
	e.createRoot("used", "/mnt/used", ``)
	_, b := e.admin("GET", "/api/v1/admin/available-mounts", "")
	items := b["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("%v", b)
	}
	used := items[0].(map[string]any)
	free := items[1].(map[string]any)
	if used["root_id"] == nil || free["root_id"] != nil || used["fstype"] != "zfs" || used["readonly"] != true {
		t.Fatalf("%v %v", used, free)
	}
}
