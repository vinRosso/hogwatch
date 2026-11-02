package repo

import (
	"testing"
)

func TestMyUsage(t *testing.T) {
	f := rootFixture(t)
	owner, plain := f.user("owner"), f.user("plain")
	pid := f.path(owner, "a")
	f.exec(`INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files, shared_hardlink_bytes, zfs_used_snapshots)
	        VALUES (1, ?, 1, 0, 40, 50, 3, 0, 7)`, pid)
	redundant := f.path(owner, "a/b")
	f.exec(`INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files, shared_hardlink_bytes, zfs_used_snapshots)
	        VALUES (1, ?, 1, 1, 10, 10, 1, 0, 100)`, redundant)
	f.exec(`INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes) VALUES (1, ?, 1, 40, 50, 3, 5)`, owner)
	f.exec(`UPDATE users SET quota_bytes = 1000, quota_basis = 'logical' WHERE id = ?`, owner)

	u, err := f.repo.MyUsage(t.Context(), Caller{UserID: owner}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if u.QuotaBytes == nil || *u.QuotaBytes != 1000 || u.QuotaBasis != "logical" {
		t.Errorf("quota = %v %q", u.QuotaBytes, u.QuotaBasis)
	}
	if u.Total != (Totals{Files: 3, BytesLogical: 40, BytesAlloc: 50}) {
		t.Errorf("total = %+v", u.Total)
	}
	if len(u.Mounts) != 1 || u.Mounts[0].RootName != "r" || u.Mounts[0].SharedHardlinkBytes != 5 ||
		u.Mounts[0].FSTotal == nil || *u.Mounts[0].FSTotal != 1000 || u.Mounts[0].Usage != u.Total {
		t.Errorf("mounts = %+v", u.Mounts)
	}
	if len(u.Paths) != 2 || !u.Paths[0].Found || u.Paths[0].Redundant || string(u.Paths[0].HostPath) != "/srv/r/a" ||
		u.Paths[0].Usage.BytesAlloc != 50 || !u.Paths[1].Redundant {
		t.Errorf("paths = %+v", u.Paths)
	}

	// Snapshots count only on request, and never for a redundant path.
	f.exec(`INSERT INTO settings (key, value) VALUES ('quota.include_snapshots', 'true')`)
	u, err = f.repo.MyUsage(t.Context(), Caller{UserID: owner}, 0)
	if err != nil || u.Total.BytesAlloc != 57 {
		t.Errorf("total with snapshots = %+v, %v; want 57 allocated", u.Total, err)
	}

	// No paths: well-formed zeros.
	u, err = f.repo.MyUsage(t.Context(), Caller{UserID: plain}, 0)
	if err != nil || u.Total != (Totals{}) || len(u.Mounts) != 0 || len(u.Paths) != 0 || u.QuotaBytes != nil {
		t.Errorf("plain usage = %+v, %v", u, err)
	}
}

func TestMyUsage_CapacityHiddenFromUsers(t *testing.T) {
	f := rootFixture(t)
	owner := f.user("owner")
	f.exec(`INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes) VALUES (1, ?, 1, 1, 1, 1, 0)`, owner)
	f.exec(`INSERT INTO settings (key, value) VALUES ('ui.users_see_mount_capacity', 'false')`)
	u, err := f.repo.MyUsage(t.Context(), Caller{UserID: owner}, 0)
	if err != nil || len(u.Mounts) != 1 || u.Mounts[0].FSTotal != nil || u.Mounts[0].FSFree != nil {
		t.Errorf("capacity leaked: %+v, %v", u.Mounts, err)
	}
}

func TestMyUsage_Growth(t *testing.T) {
	f := rootFixture(t)
	owner := f.user("owner")
	f.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'bare', '/mnt/bare', '/srv/bare')`)
	f.exec(`INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes) VALUES (1, ?, 1, 40, 200, 3, 0)`, owner)
	pid := f.path(owner, "a")
	f.exec(`INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files, shared_hardlink_bytes, zfs_used_snapshots)
	        VALUES (1, ?, 1, 0, 40, 200, 3, 0, 30)`, pid)
	now := int64(10_000_000)
	day := int64(86400)
	hist := func(scope string, root any, ts, alloc int64) {
		f.exec(`INSERT INTO history_totals (ts, scope, user_id, root_id, bytes_logical, bytes_alloc, files, tier) VALUES (?, ?, ?, ?, 0, ?, 0, 'raw')`,
			ts, scope, owner, root, alloc)
	}
	hist("user", nil, now-40*day, 100)
	hist("user", nil, now-20*day, 150)
	hist("user_mount", 1, now-40*day, 100)
	hist("user_mount", 1, now-20*day, 150)

	u, err := f.repo.MyUsage(t.Context(), Caller{UserID: owner}, now-30*day)
	if err != nil {
		t.Fatal(err)
	}
	if u.GrowthBytes == nil || *u.GrowthBytes != 100 { // 200 now - 100 at -40d
		t.Errorf("total growth = %v, want 100", u.GrowthBytes)
	}
	if len(u.Mounts) != 1 || u.Mounts[0].GrowthBytes == nil || *u.Mounts[0].GrowthBytes != 100 {
		t.Errorf("mount growth = %+v", u.Mounts)
	}
	u, _ = f.repo.MyUsage(t.Context(), Caller{UserID: owner}, now-10*day)
	if u.GrowthBytes == nil || *u.GrowthBytes != 50 { // base is the point at -20d
		t.Errorf("10d growth = %v, want 50", u.GrowthBytes)
	}
	u, _ = f.repo.MyUsage(t.Context(), Caller{UserID: owner}, now-90*day)
	if u.GrowthBytes != nil || u.Mounts[0].GrowthBytes != nil {
		t.Errorf("no point before the cutoff must give nil: %v %v", u.GrowthBytes, u.Mounts[0].GrowthBytes)
	}

	// History carries no snapshot space, so snapshots must not show as growth.
	f.exec(`INSERT INTO settings (key, value) VALUES ('quota.include_snapshots', 'true')`)
	u, _ = f.repo.MyUsage(t.Context(), Caller{UserID: owner}, now-30*day)
	if u.Total.BytesAlloc != 230 || u.GrowthBytes == nil || *u.GrowthBytes != 100 {
		t.Errorf("total %d growth %v: growth must stay 100 with snapshots on", u.Total.BytesAlloc, u.GrowthBytes)
	}
}

// The per-user base query must give the same bases as the all-users one, including a
// tie in ts between tiers and a user who has no point before the cutoff.
func TestGrowthBasesForUserAgreesWithAll(t *testing.T) {
	f := rootFixture(t)
	owner := f.user("owner")
	other := f.user("other")
	day := int64(86400)
	now := int64(10_000_000)
	hist := func(uid int64, scope string, root any, ts, alloc int64, tier string) {
		f.exec(`INSERT INTO history_totals (ts, scope, user_id, root_id, bytes_logical, bytes_alloc, files, tier) VALUES (?, ?, ?, ?, 0, ?, 0, ?)`,
			ts, scope, uid, root, alloc, tier)
	}
	for _, uid := range []int64{owner, other} {
		hist(uid, "user", nil, now-40*day, 100+uid, "raw")
		hist(uid, "user", nil, now-40*day, 120+uid, "weekly") // same ts, other tier: the larger wins
		hist(uid, "user", nil, now-10*day, 900, "raw")
		hist(uid, "user_mount", 1, now-40*day, 50+uid, "raw")
	}
	hist(other, "user", nil, now-50*day, 1, "raw") // other has an older point; owner has none past the cutoff below
	since := now - 30*day
	for _, scope := range []string{"user", "user_mount"} {
		all, err := f.repo.growthBases(t.Context(), scope, since)
		if err != nil {
			t.Fatal(err)
		}
		for _, uid := range []int64{owner, other} {
			one, err := f.repo.growthBasesForUser(t.Context(), scope, uid, since)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range one {
				if k[0] != uid || all[k] != v {
					t.Errorf("%s user %d: %v = %d, all-users query has %d", scope, uid, k, v, all[k])
				}
			}
			n := 0
			for k := range all {
				if k[0] == uid {
					n++
				}
			}
			if n != len(one) {
				t.Errorf("%s user %d: %d bases, all-users query has %d", scope, uid, len(one), n)
			}
		}
	}
	one, _ := f.repo.growthBasesForUser(t.Context(), "user", owner, since)
	if one[[2]int64{owner, 0}] != 120+owner {
		t.Errorf("tie base = %d, want %d", one[[2]int64{owner, 0}], 120+owner)
	}
}
