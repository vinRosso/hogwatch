package repo

import (
	"testing"
)

// rootFixture has scan 1 as the current scan of root 1.
func rootFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.scan(1, tree...)
	f.exec(`UPDATE scans SET finished_at = 500 WHERE id = 1`)
	f.exec(`UPDATE scan_roots SET current_scan_id = 1 WHERE id = 1`)
	f.exec(`INSERT INTO mount_stats (scan_id, root_id, fs_total, fs_free) VALUES (1, 1, 1000, 400)`)
	return f
}

func (f *fixture) stats(pathID int64, found, redundant bool) {
	f.t.Helper()
	f.exec(`INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files, shared_hardlink_bytes)
	        VALUES (1, ?, ?, ?, 0, 0, 0, 0)`, pathID, found, redundant)
}

func TestRoots(t *testing.T) {
	f := rootFixture(t)
	owner, nested, missing, plain := f.user("owner"), f.user("nested"), f.user("missing"), f.user("plain")
	f.stats(f.path(owner, "a"), true, false)
	f.stats(f.path(nested, "bad\xff"), true, false)
	f.stats(f.path(nested, "bad\xff/sub"), true, true)
	f.stats(f.path(missing, "nope"), false, false)

	admin, err := f.repo.Roots(t.Context(), Caller{UserID: 999, Admin: true})
	if err != nil || len(admin) != 1 {
		t.Fatalf("admin roots = %+v, %v", admin, err)
	}
	r := admin[0]
	if r.ID != 1 || r.Name != "r" || r.Scanning || r.LastScannedAt == nil || *r.LastScannedAt != 500 ||
		r.FSTotal == nil || *r.FSTotal != 1000 || *r.FSFree != 400 {
		t.Errorf("admin root = %+v", r)
	}
	if len(r.Anchors) != 1 || r.Anchors[0].Ref == nil || *r.Anchors[0].Ref != f.ref(1, "") ||
		string(r.Anchors[0].HostPath) != "/srv/r" || !r.Anchors[0].Found {
		t.Errorf("admin anchors = %+v", r.Anchors)
	}

	got, err := f.repo.Roots(t.Context(), Caller{UserID: owner})
	if err != nil || len(got) != 1 || len(got[0].Anchors) != 1 {
		t.Fatalf("owner roots = %+v, %v", got, err)
	}
	if a := got[0].Anchors[0]; a.Ref == nil || *a.Ref != f.ref(1, "a") || string(a.HostPath) != "/srv/r/a" || !a.Found {
		t.Errorf("owner anchor = %+v", a)
	}

	got, err = f.repo.Roots(t.Context(), Caller{UserID: nested})
	if err != nil || len(got) != 1 || len(got[0].Anchors) != 1 || string(got[0].Anchors[0].HostPath) != "/srv/r/bad\xff" {
		t.Errorf("nested roots = %+v, %v; want one anchor (the redundant nested path is hidden)", got, err)
	}

	got, err = f.repo.Roots(t.Context(), Caller{UserID: missing})
	if err != nil || len(got) != 1 || len(got[0].Anchors) != 1 || got[0].Anchors[0].Found || got[0].Anchors[0].Ref != nil ||
		string(got[0].Anchors[0].HostPath) != "/srv/r/nope" {
		t.Errorf("missing-path roots = %+v, %v", got, err)
	}

	if got, err = f.repo.Roots(t.Context(), Caller{UserID: plain}); err != nil || len(got) != 0 {
		t.Errorf("plain roots = %+v, %v; want none", got, err)
	}
}

func TestRoots_CapacitySettingAndScanning(t *testing.T) {
	f := rootFixture(t)
	owner := f.user("owner")
	f.stats(f.path(owner, "a"), true, false)
	f.exec(`INSERT INTO settings (key, value) VALUES ('ui.users_see_mount_capacity', 'false')`)
	f.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (2, 1, 'x.db', 'running', 'manual', 600)`)

	got, err := f.repo.Roots(t.Context(), Caller{UserID: owner})
	if err != nil || len(got) != 1 {
		t.Fatalf("owner roots = %+v, %v", got, err)
	}
	if got[0].FSTotal != nil || got[0].FSFree != nil {
		t.Errorf("capacity shown to a user although hidden: %+v", got[0])
	}
	if !got[0].Scanning {
		t.Error("a running scan is not reported")
	}
	admin, _ := f.repo.Roots(t.Context(), Caller{UserID: 999, Admin: true})
	if len(admin) != 1 || admin[0].FSTotal == nil {
		t.Errorf("admin must still see capacity: %+v", admin)
	}
}

func TestRoots_RootWithoutCurrentScan(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'empty', '/mnt/e', '/srv/e')`)
	u := f.user("u")
	f.exec(`INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, 2, CAST('x' AS BLOB), 1)`, u)
	got, err := f.repo.Roots(t.Context(), Caller{UserID: u})
	if err != nil || len(got) != 1 {
		t.Fatalf("roots = %+v, %v", got, err)
	}
	r := got[0]
	if r.LastScannedAt != nil || r.FSTotal != nil || r.Scanning || len(r.Anchors) != 1 || r.Anchors[0].Found || r.Anchors[0].Ref != nil {
		t.Errorf("root without a scan = %+v", r)
	}
	admin, err := f.repo.Roots(t.Context(), Caller{Admin: true})
	if err != nil || len(admin) != 2 || admin[1].Anchors[0].Found {
		t.Errorf("admin roots = %+v, %v", admin, err)
	}
}
