package db

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

func TestQueries_Admin(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	q := dbq.New(d)
	seedRoot(t, d) // root 1 "data", user 1 alice
	mustExec(t, d, `INSERT INTO users (id, username, email, password_hash, admin_local, created_at) VALUES (2, 'bob', 'b@x', 'h', 1, 200)`)
	mustExec(t, d, `INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'other', '/mnt/o', '/o')`)
	mustExec(t, d, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at, bytes_alloc) VALUES
		(1, 1, 'a.db', 'done', 'manual', 1000, 1100, 500), (2, 1, 'b.db', 'done', 'schedule', 2000, 2100, 700), (3, 2, 'c.db', 'done', 'schedule', 2000, 2100, 9)`)
	mustExec(t, d, `UPDATE scan_roots SET current_scan_id = 2 WHERE id = 1`)

	t.Run("users", func(t *testing.T) {
		rows, err := q.ListUsersPage(ctx, dbq.ListUsersPageParams{AfterID: 0, Lim: 1})
		if err != nil || len(rows) != 1 || rows[0].Username != "alice" || rows[0].HasPassword != 0 {
			t.Fatalf("page 1 = %+v (%v)", rows, err)
		}
		rows, _ = q.ListUsersPage(ctx, dbq.ListUsersPageParams{AfterID: 1, Lim: 10})
		if len(rows) != 1 || rows[0].Username != "bob" || rows[0].HasPassword != 1 {
			t.Fatalf("page 2 = %+v", rows)
		}
		if n, _ := q.CountEnabledLocalAdmins(ctx); n != 1 {
			t.Fatalf("local admins = %d, want 1", n)
		}
		n, err := q.DeleteUser(ctx, 2)
		if err != nil || n != 1 {
			t.Fatalf("DeleteUser = %d, %v", n, err)
		}
		if n, _ := q.DeleteUser(ctx, 2); n != 0 {
			t.Fatalf("second DeleteUser = %d", n)
		}
	})

	t.Run("paths and issues", func(t *testing.T) {
		p, err := q.InsertUserPath(ctx, dbq.InsertUserPathParams{UserID: 1, RootID: 1, RelPath: []byte("a"), CreatedAt: 5})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.InsertUserPath(ctx, dbq.InsertUserPathParams{UserID: 1, RootID: 1, RelPath: []byte("a"), CreatedAt: 5}); err == nil {
			t.Fatal("duplicate path accepted")
		}
		if n, _ := q.DeleteUserPath(ctx, dbq.DeleteUserPathParams{ID: p.ID, UserID: 2}); n != 0 {
			t.Fatal("deleted another user's path")
		}
		mustExec(t, d, `INSERT INTO config_issues (root_id, kind, details, created_at) VALUES
			(1, 'cross_root_duplicate', '{"user_path_id":`+itoa(p.ID)+`,"other_root_id":2,"node":"x"}', 1),
			(1, 'cross_root_duplicate', '{"user_path_id":999,"other_root_id":2,"node":"x"}', 1),
			(2, 'cross_root_duplicate', '{"user_path_id":999,"other_root_id":1,"node":"x"}', 1)`)
		if err := q.DeleteDupIssuesForPath(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		if got := countRows(t, d, `config_issues`); got != 2 {
			t.Fatalf("issues after path delete = %d, want 2", got)
		}
		if err := q.DeleteDupIssuesForRoot(ctx, 2); err != nil {
			t.Fatal(err)
		}
		left, _ := q.ListStoredIssues(ctx)
		if len(left) != 1 || left[0].RootID != 2 {
			t.Fatalf("issues after root delete = %+v", left)
		}
	})

	t.Run("root name index", func(t *testing.T) {
		_, err := q.InsertRoot(ctx, dbq.InsertRootParams{Name: "data", ContainerPath: "/x", HostPath: "/x", Excludes: "[]"})
		if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
			t.Fatalf("duplicate root name err = %v", err)
		}
	})

	t.Run("scans page", func(t *testing.T) {
		rows, err := q.ListScansAdminPage(ctx, dbq.ListScansAdminPageParams{Lim: 2})
		if err != nil || len(rows) != 2 || rows[0].ID != 3 || rows[1].ID != 2 {
			t.Fatalf("page 1 = %+v (%v)", rows, err)
		}
		rows, _ = q.ListScansAdminPage(ctx, dbq.ListScansAdminPageParams{
			BeforeStarted: int64(2000), BeforeID: sql.NullInt64{Int64: 2, Valid: true}, Lim: 10})
		if len(rows) != 1 || rows[0].ID != 1 {
			t.Fatalf("page 2 = %+v", rows)
		}
		rows, _ = q.ListScansAdminPage(ctx, dbq.ListScansAdminPageParams{RootID: int64(2), Lim: 10})
		if len(rows) != 1 || rows[0].ID != 3 {
			t.Fatalf("root filter = %+v", rows)
		}
	})

	t.Run("ranking and growth", func(t *testing.T) {
		mustExec(t, d, `INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes) VALUES
			(1, 1, 1, 1, 1, 1, 0), (2, 1, 1, 10, 20, 3, 4)`)
		rows, err := q.RankingRows(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatalf("ranking = %+v (%v)", rows, err)
		}
		if rows[0].BytesAlloc != 20 || rows[0].SharedHardlinkBytes != 4 {
			t.Fatalf("ranking counts a non-current scan: %+v", rows[0])
		}
		mustExec(t, d, `INSERT INTO users (id, username, created_at) VALUES (3, 'carol', 1)`)
		rows, _ = q.RankingRows(ctx)
		if len(rows) != 2 || rows[1].Username != "carol" || rows[1].BytesAlloc != 0 {
			t.Fatalf("a user without usage must show zeros: %+v", rows)
		}
		mustExec(t, d, `INSERT INTO history_totals (ts, scope, user_id, bytes_logical, bytes_alloc, files, tier) VALUES
			(100, 'user', 1, 0, 5, 0, 'raw'), (200, 'user', 1, 0, 8, 0, 'raw'), (900, 'user', 1, 0, 99, 0, 'raw')`)
		g, err := q.GrowthBase(ctx, dbq.GrowthBaseParams{Scope: "user", Since: int64(250)})
		if err != nil || len(g) != 1 || g[0].BytesAlloc != 8 {
			t.Fatalf("growth base = %+v (%v)", g, err)
		}
		g, _ = q.GrowthBase(ctx, dbq.GrowthBaseParams{Scope: "user", Since: int64(50)})
		if len(g) != 0 {
			t.Fatalf("growth base before any point = %+v", g)
		}
	})

	t.Run("mounts", func(t *testing.T) {
		mustExec(t, d, `INSERT INTO mount_stats (scan_id, root_id, fs_total, fs_free) VALUES (2, 1, 100, 40)`)
		rows, err := q.ListMountRows(ctx)
		if err != nil || len(rows) != 2 {
			t.Fatalf("mounts = %+v (%v)", rows, err)
		}
		if !rows[0].FsTotal.Valid || rows[0].FsTotal.Int64 != 100 || !rows[0].ScanBytesAlloc.Valid {
			t.Fatalf("root 1 = %+v", rows[0])
		}
		if rows[1].FsTotal.Valid || rows[1].ScanBytesAlloc.Valid {
			t.Fatalf("root 2 has no current scan, want nulls: %+v", rows[1])
		}
	})

	t.Run("categories and audit", func(t *testing.T) {
		if err := q.UpsertCategory(ctx, dbq.UpsertCategoryParams{Ext: "mp4", Category: "audio"}); err != nil {
			t.Fatal(err)
		}
		if n, _ := q.DeleteCategory(ctx, "mp4"); n != 1 {
			t.Fatalf("DeleteCategory = %d", n)
		}
		if n, _ := q.DeleteCategory(ctx, "mp4"); n != 0 {
			t.Fatalf("second DeleteCategory = %d", n)
		}
		for i := 0; i < 3; i++ {
			mustExec(t, d, `INSERT INTO audit_log (ts, action) VALUES (?, 'x')`, i)
		}
		rows, err := q.ListAuditPage(ctx, dbq.ListAuditPageParams{Lim: 2})
		if err != nil || len(rows) != 2 || rows[0].ID != 3 {
			t.Fatalf("audit page 1 = %+v (%v)", rows, err)
		}
		rows, _ = q.ListAuditPage(ctx, dbq.ListAuditPageParams{BeforeID: int64(2), Lim: 2})
		if len(rows) != 1 || rows[0].ID != 1 {
			t.Fatalf("audit page 2 = %+v", rows)
		}
	})
}

func countRows(t *testing.T, d *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
