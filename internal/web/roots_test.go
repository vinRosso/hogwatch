package web

import "testing"

// seedStats gives the fixture's owner (path a) and lister (path d) path stats
// and usage on scan 1.
func seedStats(e *env) {
	var ownerPath, listerPath int64
	_ = e.db.QueryRow(`SELECT id FROM user_paths WHERE user_id = ?`, e.users["owner"]).Scan(&ownerPath)
	_ = e.db.QueryRow(`SELECT id FROM user_paths WHERE user_id = ?`, e.users["lister"]).Scan(&listerPath)
	e.exec(`INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files, shared_hardlink_bytes)
	        VALUES (1, ?, 1, 0, 25, 30, 3, 0)`, ownerPath)
	e.exec(`INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files, shared_hardlink_bytes)
	        VALUES (1, ?, 1, 0, 1, 1, 1, 0)`, listerPath)
	e.exec(`INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes) VALUES (1, ?, 1, 25, 30, 3, 0)`, e.users["owner"])
	e.exec(`INSERT INTO mount_stats (scan_id, root_id, fs_total, fs_free) VALUES (1, 1, 1000, 400)`)
}

func TestListRoots_Handler(t *testing.T) {
	e := newEnv(t)
	seedStats(e)
	m := getJSON(t, e, "owner", "/api/v1/roots", 200)
	items := m["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("owner roots = %v", m)
	}
	root := items[0].(map[string]any)
	anchors := root["anchors"].([]any)
	if root["name"] != "r" || root["last_scanned_at"] != float64(2) || root["fs_total"] != float64(1000) || len(anchors) != 1 {
		t.Errorf("owner root = %v", root)
	}
	if an := anchors[0].(map[string]any); an["ref"] != "1.2" || an["host_path"] != "/srv/r/a" || an["found"] != true {
		t.Errorf("owner anchor = %v", an)
	}
	if m = getJSON(t, e, "plain", "/api/v1/roots", 200); m["items"] == nil || len(m["items"].([]any)) != 0 {
		t.Errorf("plain roots must be [], got %v", m)
	}
	root = getJSON(t, e, "admin", "/api/v1/roots", 200)["items"].([]any)[0].(map[string]any)
	if an := root["anchors"].([]any)[0].(map[string]any); an["ref"] != "1.1" || an["host_path"] != "/srv/r" {
		t.Errorf("admin anchor = %v", an)
	}
	e.exec(`INSERT INTO settings (key, value) VALUES ('ui.users_see_mount_capacity', 'false')`)
	if root = getJSON(t, e, "owner", "/api/v1/roots", 200)["items"].([]any)[0].(map[string]any); root["fs_total"] != nil {
		t.Errorf("capacity shown to a user although hidden: %v", root)
	}
	if root = getJSON(t, e, "admin", "/api/v1/roots", 200)["items"].([]any)[0].(map[string]any); root["fs_total"] == nil {
		t.Errorf("admin lost the capacity: %v", root)
	}
}

func TestGetMyUsage_Handler(t *testing.T) {
	e := newEnv(t)
	seedStats(e)
	e.exec(`UPDATE users SET quota_bytes = 100 WHERE id = ?`, e.users["owner"])
	m := getJSON(t, e, "owner", "/api/v1/me/usage", 200)
	total := m["total"].(map[string]any)
	if m["quota_bytes"] != float64(100) || m["quota_basis"] != "allocated" || total["bytes_alloc"] != float64(30) ||
		len(m["mounts"].([]any)) != 1 || len(m["paths"].([]any)) != 1 {
		t.Errorf("owner usage = %v", m)
	}
	m = getJSON(t, e, "plain", "/api/v1/me/usage", 200)
	if m["quota_bytes"] != nil || m["mounts"] == nil || len(m["mounts"].([]any)) != 0 || m["paths"] == nil || len(m["paths"].([]any)) != 0 ||
		m["total"].(map[string]any)["files"] != float64(0) {
		t.Errorf("plain usage = %v", m)
	}
}

func TestListScans_Handler(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'o', '/mnt/o', '/srv/o')`)
	e.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (5, 2, 'z.db', 'done', 'schedule', 9, 10)`)
	e.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (6, 1, 'y.db', 'deleted', 'schedule', 0, 1)`)
	count := func(role, q string) int {
		return len(getJSON(t, e, role, "/api/v1/scans"+q, 200)["items"].([]any))
	}
	if n := count("owner", ""); n != 1 {
		t.Errorf("owner sees %d scans, want 1 (the current scan of root 1)", n)
	}
	if n := count("owner", "?root=2"); n != 0 {
		t.Errorf("owner sees %d scans of a root without a path, want 0", n)
	}
	if n := count("admin", ""); n != 3 {
		t.Errorf("admin sees %d scans, want 3 (deleted included)", n)
	}
	if n := count("admin", "?root=2"); n != 1 {
		t.Errorf("admin root 2 = %d scans, want 1", n)
	}
	if n := count("plain", ""); n != 0 {
		t.Errorf("plain sees %d scans", n)
	}
	for _, it := range getJSON(t, e, "owner", "/api/v1/scans", 200)["items"].([]any) {
		if _, has := it.(map[string]any)["error"]; has {
			t.Errorf("a user scan has an error field: %v", it)
		}
	}
}
