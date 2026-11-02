package web

import (
	"fmt"
	"testing"
)

func TestUpdateUser_DisplayNameAndEmailShape(t *testing.T) {
	e := newEnv(t)
	id := e.users["plain"]
	if code, u := e.admin("PATCH", userURL(id), `{"display_name":"  "}`); code != 200 || u["display_name"] != "plain" {
		t.Fatalf("a blank display name must fall back to the username: %d %v", code, u)
	}
	for _, email := range []string{"@", "a@", "@b.org", "a b@c.org", "a@b@"} {
		if code, _ := e.admin("PATCH", userURL(id), fmt.Sprintf(`{"email":%q}`, email)); code != 400 {
			t.Errorf("email %q: %d, want 400", email, code)
		}
		if code, _ := e.admin("POST", "/api/v1/admin/users", fmt.Sprintf(`{"username":"mail%d","password":"correct horse","email":%q}`, len(email), email)); code != 400 {
			t.Errorf("create with email %q: %d, want 400", email, code)
		}
	}
	if code, u := e.admin("PATCH", userURL(id), `{"email":"p@x.org"}`); code != 200 || u["email"] != "p@x.org" {
		t.Fatalf("a valid email: %d %v", code, u)
	}
}

func TestPatchKeys_CaseInsensitive(t *testing.T) {
	e := newEnv(t)
	id := e.users["plain"]
	e.exec(`UPDATE users SET email = 'p@x.org', quota_bytes = 9 WHERE id = ?`, id)
	// encoding/json fills the field from "EMAIL"; the null-vs-absent check must agree.
	if code, u := e.admin("PATCH", userURL(id), `{"EMAIL":null,"Quota_Bytes":null}`); code != 200 || u["email"] != nil || u["quota_bytes"] != nil {
		t.Fatalf("mixed-case nulls: %d %v", code, u)
	}
	// Two spellings of one key are ambiguous.
	if code, _ := e.admin("PATCH", userURL(id), `{"email":null,"Email":"q@x.org"}`); code != 400 {
		t.Fatalf("duplicate key: %d, want 400", code)
	}
}

func TestUpdateRoot_RefusesContainerPath(t *testing.T) {
	e := newEnv(t)
	var before string
	_ = e.db.QueryRow(`SELECT container_path FROM scan_roots WHERE id = 1`).Scan(&before)
	if code, _ := e.admin("PATCH", rootsURL+"/1", `{"name":"x","container_path":"/mnt/elsewhere"}`); code != 400 {
		t.Fatalf("container_path in a patch: %d, want 400", code)
	}
	var after, name string
	_ = e.db.QueryRow(`SELECT container_path, name FROM scan_roots WHERE id = 1`).Scan(&after, &name)
	if after != before || name == "x" || e.auditCount("root.updated") != 0 {
		t.Fatal("a refused patch changed the root")
	}
}

func TestDeleteRoot_ActiveListing(t *testing.T) {
	for _, status := range []string{"queued", "running"} {
		e := newEnv(t)
		e.exec(`INSERT INTO jobs (kind, root_id, params, status, created_at) VALUES ('listing', 1, '{"mode":"children"}', ?, 1)`, status)
		if code, _ := e.admin("DELETE", rootsURL+"/1", ``); code != 409 {
			t.Fatalf("%s listing: %d, want 409", status, code)
		}
		var n int
		_ = e.db.QueryRow(`SELECT count(*) FROM scan_roots WHERE id = 1`).Scan(&n)
		if n != 1 || e.auditCount("root.deleted") != 0 {
			t.Fatalf("%s listing: the root was touched", status)
		}
	}
	e := newEnv(t)
	e.exec(`INSERT INTO jobs (kind, root_id, params, status, created_at, finished_at) VALUES ('listing', 1, '{"mode":"children"}', 'done', 1, 2)`)
	if code, _ := e.admin("DELETE", rootsURL+"/1", ``); code != 204 {
		t.Fatalf("a finished listing must not block: %d", code)
	}
}

func TestDeleteUserPath_UnservableGeneration(t *testing.T) {
	e := newEnv(t)
	owner := e.users["owner"]
	// The current generation file is gone: the recompute cannot run.
	e.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (2, 1, 'gone-2.db', 'done', 'manual', 3, 4)`)
	e.exec(`UPDATE scan_roots SET current_scan_id = 2`)
	e.exec(`INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes) VALUES (2, ?, 1, 10, 10, 1, 0)`, owner)
	e.exec(`INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, 1, CAST('zz' AS BLOB), 1)`, owner)
	id := e.pathIDs(owner)[0]
	if code, _ := e.admin("DELETE", fmt.Sprintf("%s/%d", pathsURL(owner), id), ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM usage WHERE scan_id = 2 AND user_id = ?`, owner).Scan(&n)
	if n != 0 {
		t.Fatal("a usage row that still counts the deleted path survived; unknown must not be shown as a number")
	}
}
