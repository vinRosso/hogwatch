package repo

import (
	"testing"
	"time"
)

func TestRanking_Root(t *testing.T) {
	f := rootFixture(t)
	a, b := f.user("owner"), f.user("plain")
	f.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'bare', '/mnt/bare', '/srv/bare')`)
	f.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (2, 2, 'x2', 'done', 'manual', 1, 500)`)
	f.exec(`UPDATE scan_roots SET current_scan_id = 2 WHERE id = 2`)
	f.exec(`INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes) VALUES (1, ?, 1, 10, 100, 1, 0), (2, ?, 2, 20, 300, 2, 5)`, a, a)
	f.exec(`UPDATE users SET quota_bytes = 1000 WHERE id = ?`, a)
	now := time.Unix(10_000_000, 0)
	day := int64(86400)
	h := func(scope string, root any, ts, alloc int64) {
		f.exec(`INSERT INTO history_totals (ts, scope, user_id, root_id, bytes_logical, bytes_alloc, files, tier) VALUES (?, ?, ?, ?, 0, ?, 0, 'raw')`,
			ts, scope, a, root, alloc)
	}
	h("user_mount", 2, now.Unix()-40*day, 100)
	h("user", nil, now.Unix()-40*day, 150)

	rows, err := f.repo.Ranking(t.Context(), "usage", 30*24*time.Hour, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]RankRow{}
	for _, r := range rows {
		by[r.Username] = r
	}
	o := by["owner"]
	if o.Total.BytesAlloc != 300 || o.SharedHardlinkBytes != 5 {
		t.Errorf("root 2 usage = %+v", o)
	}
	if o.GrowthBytes == nil || *o.GrowthBytes != 200 { // 300 - 100 from user_mount rows
		t.Errorf("root growth = %v, want 200", o.GrowthBytes)
	}
	if o.QuotaPct == nil || *o.QuotaPct != 40 { // all-roots 400 of 1000
		t.Errorf("quota_pct = %v, want the all-roots 40", o.QuotaPct)
	}
	p, ok := by["plain"]
	if !ok || p.Total != (Totals{}) || p.GrowthBytes != nil {
		t.Errorf("user without a path on the root must show zeros: %+v (%v)", p, ok)
	}
	_ = b
	// A root without a current scan has no growth to report.
	f.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (3, 'idle', '/mnt/idle', '/srv/idle')`)
	idle, err := f.repo.Ranking(t.Context(), "usage", 30*24*time.Hour, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range idle {
		if r.GrowthBytes != nil {
			t.Errorf("unscanned root growth = %v, want nil", *r.GrowthBytes)
		}
	}
	if _, err := f.repo.Ranking(t.Context(), "usage", time.Hour, 999, now); err != ErrNotFound {
		t.Errorf("unknown root: %v", err)
	}
	all, _ := f.repo.Ranking(t.Context(), "usage", 30*24*time.Hour, 0, now)
	seen := false
	for _, r := range all {
		if r.Username == "owner" {
			seen = true
			if r.Total.BytesAlloc != 400 || r.GrowthBytes == nil || *r.GrowthBytes != 250 {
				t.Errorf("all roots = %+v", r)
			}
		}
	}
	if !seen {
		t.Error("owner missing from the all-roots ranking")
	}
}

func TestMounts_Growth(t *testing.T) {
	f := rootFixture(t)
	f.exec(`UPDATE scans SET bytes_alloc = 500 WHERE id = 1`)
	f.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'bare', '/mnt/bare', '/srv/bare')`)
	now := time.Unix(10_000_000, 0)
	f.exec(`INSERT INTO history_totals (ts, scope, root_id, bytes_logical, bytes_alloc, files, tier) VALUES (?, 'mount', 1, 0, 350, 0, 'raw')`, now.Unix()-40*86400)
	rows, err := f.repo.Mounts(t.Context(), 30*24*time.Hour, now)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%+v %v", rows, err)
	}
	if rows[0].GrowthBytes == nil || *rows[0].GrowthBytes != 150 {
		t.Errorf("root 1 growth = %v", rows[0].GrowthBytes)
	}
	if rows[1].GrowthBytes != nil {
		t.Errorf("root without a scan must give nil growth: %v", rows[1].GrowthBytes)
	}
}
