package repo

import (
	"fmt"
	"testing"
)

func TestScans(t *testing.T) {
	f := rootFixture(t)
	f.exec(`UPDATE scans SET started_at = 100, status = 'done' WHERE id = 1`)
	f.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (2, 1, 'x2.db', 'running', 'manual', 300)`)
	f.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (3, 1, 'x3.db', 'deleted', 'schedule', 50, 60)`)
	f.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'o', '/mnt/o', '/srv/o')`)
	f.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (4, 2, 'x4.db', 'done', 'schedule', 200, 250)`)
	f.exec(`UPDATE scan_roots SET current_scan_id = 4 WHERE id = 2`)
	owner, plain := f.user("owner"), f.user("plain")
	f.path(owner, "a")

	ids := func(rows []ScanRow) []int64 {
		var out []int64
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	admin, err := f.repo.Scans(t.Context(), Caller{Admin: true}, nil)
	if got := fmt.Sprint(ids(admin)); err != nil || got != "[2 4 1 3]" {
		t.Errorf("admin scans = %s, %v; want newest first incl. deleted [2 4 1 3]", got, err)
	}
	root2 := int64(2)
	admin, _ = f.repo.Scans(t.Context(), Caller{Admin: true}, &root2)
	if got := fmt.Sprint(ids(admin)); got != "[4]" {
		t.Errorf("admin root 2 = %s", got)
	}

	got, err := f.repo.Scans(t.Context(), Caller{UserID: owner}, nil)
	if s := fmt.Sprint(ids(got)); err != nil || s != "[2 1]" {
		t.Errorf("owner scans = %s, %v; want the running and current scan of root 1 only", s, err)
	}
	if got[1].Status != "done" || got[1].FinishedAt == nil || *got[1].FinishedAt != 500 || got[1].Trigger != "manual" {
		t.Errorf("scan row = %+v", got[1])
	}
	if got, _ = f.repo.Scans(t.Context(), Caller{UserID: owner}, &root2); len(got) != 0 {
		t.Errorf("owner on a root without a path = %+v, want none", got)
	}
	if got, err = f.repo.Scans(t.Context(), Caller{UserID: plain}, nil); err != nil || len(got) != 0 {
		t.Errorf("plain scans = %+v, %v", got, err)
	}
}

func TestScans_AdminCap(t *testing.T) {
	f := rootFixture(t)
	for i := 10; i < 10+maxScanRows+5; i++ {
		f.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (?, 1, 'x.db', 'done', 'schedule', ?)`, i, 1000+i)
	}
	rows, err := f.repo.Scans(t.Context(), Caller{Admin: true}, nil)
	if err != nil || len(rows) != maxScanRows {
		t.Errorf("admin scans = %d rows, %v; want the cap %d", len(rows), err, maxScanRows)
	}
}
