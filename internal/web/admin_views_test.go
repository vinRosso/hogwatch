package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func (e *env) usageRow(user string, logical, alloc, shared int64) {
	e.exec(`INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes) VALUES (1, ?, 1, ?, ?, 1, ?)`,
		e.users[user], logical, alloc, shared)
}

func (e *env) history(user string, ts, alloc int64) {
	e.exec(`INSERT INTO history_totals (ts, scope, user_id, bytes_logical, bytes_alloc, files, tier) VALUES (?, 'user', ?, 0, ?, 0, 'raw')`,
		ts, e.users[user], alloc)
}

func (e *env) ranking(q string) []map[string]any {
	e.t.Helper()
	code, b := e.admin("GET", "/api/v1/admin/ranking"+q, ``)
	if code != 200 {
		e.t.Fatalf("ranking%s: %d %v", q, code, b)
	}
	var out []map[string]any
	for _, it := range b["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func names(rows []map[string]any) (out []string) {
	for _, r := range rows {
		out = append(out, r["username"].(string))
	}
	return out
}

func TestRanking(t *testing.T) {
	e := newEnv(t)
	now := e.now.Unix()
	e.usageRow("owner", 500, 400, 7)
	e.usageRow("lister", 100, 900, 0)
	e.exec(`UPDATE users SET quota_bytes = 1000, quota_basis = 'logical' WHERE id = ?`, e.users["owner"])
	e.exec(`UPDATE users SET quota_bytes = 1000 WHERE id = ?`, e.users["lister"])
	e.history("owner", now-40*86400, 100)
	e.history("owner", now-3*86400, 300) // inside the window: not the base
	e.history("lister", now-3*86400, 100)

	rows := e.ranking("")
	if got := names(rows)[:2]; got[0] != "lister" || got[1] != "owner" {
		t.Fatalf("usage order: %v", names(rows))
	}
	if len(rows) != len(e.users) {
		t.Fatalf("%d rows for %d users", len(rows), len(e.users))
	}
	byName := map[string]map[string]any{}
	for _, r := range rows {
		byName[r["username"].(string)] = r
	}
	if byName["owner"]["quota_pct"].(float64) != 40 { // allocated bytes whatever the basis: 400/1000
		t.Fatalf("owner pct %v", byName["owner"]["quota_pct"])
	}
	if byName["lister"]["quota_pct"].(float64) != 90 { // allocated basis: 900/1000
		t.Fatalf("lister pct %v", byName["lister"]["quota_pct"])
	}
	if byName["owner"]["growth_bytes"].(float64) != 300 { // 400 now - 100 ten days ago
		t.Fatalf("owner growth %v", byName["owner"]["growth_bytes"])
	}
	if byName["lister"]["growth_bytes"] != nil { // only a point inside the window
		t.Fatalf("lister growth %v", byName["lister"]["growth_bytes"])
	}
	if byName["owner"]["shared_hardlink_bytes"].(float64) != 7 {
		t.Fatal("shared bytes")
	}
	if p := byName["plain"]; p["bytes_alloc"].(float64) != 0 || p["quota_pct"] != nil || p["quota_bytes"] != nil {
		t.Fatalf("user without usage: %v", p)
	}
	if got := names(e.ranking("?sort=quota_pct")); got[0] != "lister" || got[1] != "owner" {
		t.Fatalf("pct order: %v", got)
	}
	if got := names(e.ranking("?sort=growth")); got[0] != "owner" {
		t.Fatalf("growth order: %v", got)
	}
	// A wider window finds the older point for the other user too.
	e.history("lister", now-40*86400, 50)
	for _, r := range e.ranking("?window=30d") {
		if r["username"] == "lister" && r["growth_bytes"].(float64) != 850 {
			t.Fatalf("30d growth %v", r["growth_bytes"])
		}
	}
	for _, q := range []string{"?window=2d", "?sort=name"} {
		if code, _ := e.admin("GET", "/api/v1/admin/ranking"+q, ``); code != 400 {
			t.Fatalf("%s: %d", q, code)
		}
	}
}

func TestRanking_Snapshots(t *testing.T) {
	e := newEnv(t)
	e.usageRow("owner", 500, 400, 0)
	var pid int64
	_ = e.db.QueryRow(`SELECT id FROM user_paths WHERE user_id = ?`, e.users["owner"]).Scan(&pid)
	e.exec(`INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files, shared_hardlink_bytes, zfs_used_snapshots)
	        VALUES (1, ?, 1, 0, 500, 400, 1, 0, 1000)`, pid)
	e.exec(`UPDATE users SET quota_bytes = 10000 WHERE id = ?`, e.users["owner"])
	pick := func(field string) float64 {
		for _, r := range e.ranking("") {
			if r["username"] == "owner" {
				return r[field].(float64)
			}
		}
		return -1
	}
	if pick("bytes_alloc") != 400 {
		t.Fatal("snapshots counted while off")
	}
	e.exec(`INSERT INTO settings (key, value) VALUES ('quota.include_snapshots', 'true')`)
	if pick("bytes_alloc") != 1400 || pick("bytes_logical") != 500 || pick("quota_pct") != 14 {
		t.Fatalf("on: alloc %v logical %v pct %v", pick("bytes_alloc"), pick("bytes_logical"), pick("quota_pct"))
	}
	// The same rule as /me/usage.
	w := e.do(req{method: "GET", path: "/api/v1/me/usage", role: "owner"})
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestMounts(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'bare', '/mnt/bare', '/srv/bare')`)
	e.exec(`UPDATE scans SET bytes_alloc = 77 WHERE id = 1`)
	e.exec(`INSERT INTO mount_stats (scan_id, root_id, fs_total, fs_free, zfs_used, zfs_used_snapshots) VALUES (1, 1, 1000, 400, 600, 50)`)
	_, b := e.admin("GET", "/api/v1/admin/mounts", ``)
	items := b["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("%v", b)
	}
	m1, m2 := items[0].(map[string]any), items[1].(map[string]any)
	if m1["fs_total"].(float64) != 1000 || m1["bytes_alloc"].(float64) != 77 || m1["zfs_used"].(float64) != 600 ||
		m1["zfs_used_snapshots"].(float64) != 50 || m1["zfs_avail"] != nil {
		t.Fatalf("root 1: %v", m1)
	}
	for _, k := range []string{"bytes_alloc", "fs_total", "fs_free", "zfs_used", "zfs_avail", "zfs_quota", "zfs_used_snapshots"} {
		if m2[k] != nil {
			t.Fatalf("root without a scan has %s = %v", k, m2[k])
		}
	}
}

func TestConfigIssues(t *testing.T) {
	e := newEnv(t)
	var ownerPath int64
	_ = e.db.QueryRow(`SELECT id FROM user_paths WHERE user_id = ?`, e.users["owner"]).Scan(&ownerPath)
	e.exec(`INSERT INTO config_issues (root_id, kind, details, created_at) VALUES (1, 'writable_mount', '{"count":1}', 5)`)
	e.exec(`INSERT INTO config_issues (root_id, kind, details, created_at) VALUES (1, 'cross_root_duplicate', ?, 5)`,
		fmt.Sprintf(`{"user_path_id":%d,"other_root_id":2,"node":"1.1"}`, ownerPath))
	e.exec(`INSERT INTO config_issues (root_id, kind, details, created_at) VALUES (1, 'cross_root_duplicate', '{"user_path_id":9999,"other_root_id":2,"node":"1.1"}', 5)`)
	// Same user, nested: redundant. Different users, nested: overlap.
	e.exec(`INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, 1, CAST('a/x' AS BLOB), 1)`, e.users["owner"])
	e.exec(`INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, 1, CAST('d/y' AS BLOB), 1)`, e.users["plain"])
	e.exec(`INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files, shared_hardlink_bytes)
	        VALUES (1, ?, 1, 0, 1, 1, 1, 33)`, ownerPath)

	_, b := e.admin("GET", "/api/v1/admin/config-issues", ``)
	kinds := map[string][]map[string]any{}
	for _, it := range b["items"].([]any) {
		m := it.(map[string]any)
		kinds[m["kind"].(string)] = append(kinds[m["kind"].(string)], m)
	}
	if len(kinds["writable_mount"]) != 1 || kinds["writable_mount"][0]["id"] == nil {
		t.Fatalf("stored: %v", b)
	}
	if len(kinds["cross_root_duplicate"]) != 1 {
		t.Fatalf("a duplicate of a missing path was returned: %v", kinds["cross_root_duplicate"])
	}
	red := kinds["redundant_path"]
	if len(red) != 1 || red[0]["id"] != nil || red[0]["details"].(map[string]any)["user_id"].(float64) != float64(e.users["owner"]) {
		t.Fatalf("redundant: %v", red)
	}
	if ov := kinds["overlap"]; len(ov) != 1 || ov[0]["id"] != nil {
		t.Fatalf("overlap: %v", ov)
	}
	sh := kinds["shared_hardlinks"]
	if len(sh) != 1 || sh[0]["details"].(map[string]any)["bytes"].(float64) != 33 {
		t.Fatalf("shared: %v", sh)
	}
}

func TestGrowthPerRoot(t *testing.T) {
	e := newEnv(t)
	now := e.now.Unix()
	e.usageRow("owner", 500, 400, 0)
	e.exec(`INSERT INTO history_totals (ts, scope, user_id, root_id, bytes_logical, bytes_alloc, files, tier) VALUES
		(?, 'user', ?, NULL, 0, 100, 0, 'raw'), (?, 'user_mount', ?, 1, 0, 150, 0, 'raw')`,
		now-40*86400, e.users["owner"], now-40*86400, e.users["owner"])
	e.exec(`INSERT INTO history_totals (ts, scope, user_id, root_id, bytes_logical, bytes_alloc, files, tier) VALUES (?, 'mount', NULL, 1, 0, 50, 0, 'raw')`, now-40*86400)
	e.exec(`UPDATE scans SET bytes_alloc = 80 WHERE id = 1`)

	w := e.do(req{method: "GET", path: "/api/v1/me/usage?window=30d", role: "owner"})
	var u struct {
		GrowthBytes *int64 `json:"growth_bytes"`
		Mounts      []struct {
			RootID      int64  `json:"root_id"`
			GrowthBytes *int64 `json:"growth_bytes"`
		} `json:"mounts"`
	}
	if w.Code != 200 {
		t.Fatalf("usage: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.GrowthBytes == nil || *u.GrowthBytes != 300 || len(u.Mounts) != 1 || u.Mounts[0].GrowthBytes == nil || *u.Mounts[0].GrowthBytes != 250 {
		t.Fatalf("usage growth: %s", w.Body)
	}
	if w := e.do(req{method: "GET", path: "/api/v1/me/usage?window=90d", role: "owner"}); !strings.Contains(w.Body.String(), `"growth_bytes":null`) {
		t.Fatalf("90d has no base point: %s", w.Body)
	}
	if w := e.do(req{method: "GET", path: "/api/v1/me/usage", role: "owner"}); w.Code != 200 {
		t.Fatalf("default window: %d", w.Code)
	}
	if w := e.do(req{method: "GET", path: "/api/v1/me/usage?window=2d", role: "owner"}); w.Code != 400 {
		t.Fatalf("bad window: %d", w.Code)
	}

	rows := e.ranking("?window=30d&root=1")
	seen := false
	for _, r := range rows {
		if r["username"] == "owner" {
			seen = true
			if r["growth_bytes"] == nil || r["growth_bytes"].(float64) != 250 {
				t.Fatalf("ranking root growth: %v", r)
			}
		}
	}
	if !seen {
		t.Fatal("owner missing from the root ranking")
	}
	if code, _ := e.admin("GET", "/api/v1/admin/ranking?root=0", ``); code != 200 {
		t.Fatalf("root=0 means all roots: %d", code)
	}
	if code, _ := e.admin("GET", "/api/v1/admin/ranking?root=999", ``); code != 404 {
		t.Fatalf("unknown root: %d", code)
	}
	_, b := e.admin("GET", "/api/v1/admin/mounts?window=30d", ``)
	if m := b["items"].([]any)[0].(map[string]any); m["growth_bytes"] == nil || m["growth_bytes"].(float64) != 30 {
		t.Fatalf("mount growth: %v", b)
	}
	if code, _ := e.admin("GET", "/api/v1/admin/mounts?window=2d", ``); code != 400 {
		t.Fatalf("mounts bad window: %d", code)
	}
}
