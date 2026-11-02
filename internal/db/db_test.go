package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vinRosso/hogwatch/internal/cube"
)

// mainTables is every table of the main database.
var mainTables = []string{
	"users", "sessions", "scan_roots", "user_paths", "file_categories",
	"scanner_status", "audit_log", "settings",
	"jobs", "scans", "listing_results", "config_issues",
	"usage", "mount_stats", "history_totals", "history_dirs", "history_paths", "path_stats",
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "hogwatch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return d
}

func mustExec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func tableNames(t *testing.T, d *sql.DB) []string {
	t.Helper()
	rows, err := d.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

func TestFileURI(t *testing.T) {
	got, err := FileURI("/x/a b#c?d%20e/hogwatch.db", "mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	want := "file:///x/a%20b%23c%3Fd%2520e/hogwatch.db?mode=ro"
	if got != want {
		t.Fatalf("FileURI = %q, want %q", got, want)
	}
}

func TestMigrate_EmptyDB(t *testing.T) {
	d := openMigrated(t)
	want := append(slices.Clone(mainTables), "goose_db_version")
	slices.Sort(want)
	if got := tableNames(t, d); !slices.Equal(got, want) {
		t.Fatalf("tables:\n got %v\nwant %v", got, want)
	}
	// Every table is STRICT (M2 Global Constraints).
	for _, name := range mainTables {
		var strict int
		err := d.QueryRowContext(context.Background(),
			`SELECT strict FROM pragma_table_list WHERE name = ?`, name).Scan(&strict)
		if err != nil || strict != 1 {
			t.Errorf("table %s: strict=%d err=%v, want STRICT", name, strict, err)
		}
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	pending, err := Pending(ctx, d)
	if err != nil || pending {
		t.Fatalf("Pending after Migrate = %v, %v; want false, nil", pending, err)
	}
	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestMigrate_PendingOnFreshDB(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	pending, err := Pending(ctx, d)
	if err != nil || !pending {
		t.Fatalf("Pending on fresh DB = %v, %v; want true, nil", pending, err)
	}
}

func TestMigrate_DownUp(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	p, err := newProvider(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("DownTo(0): %v", err)
	}
	if got := tableNames(t, d); !slices.Equal(got, []string{"goose_db_version"}) {
		t.Fatalf("tables after down: %v", got)
	}
	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("Migrate after down: %v", err)
	}
	if got := len(tableNames(t, d)); got != len(mainTables)+1 {
		t.Fatalf("tables after up again: %d", got)
	}
}

func TestMigrate_SeedCategories(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	for ext, want := range map[string]string{"mp4": "video", "ts": "video", "go": "code"} {
		var got string
		if err := d.QueryRowContext(ctx,
			`SELECT category FROM file_categories WHERE ext = ?`, ext).Scan(&got); err != nil {
			t.Fatalf("ext %s: %v", ext, err)
		}
		if got != want {
			t.Errorf("ext %s = %s, want %s", ext, got, want)
		}
	}
	rows, err := d.QueryContext(ctx, `SELECT ext, category FROM file_categories`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var ext, cat string
		if err := rows.Scan(&ext, &cat); err != nil {
			t.Fatal(err)
		}
		n++
		if _, err := cube.ParseCategory(cat); err != nil {
			t.Errorf("ext %s: %v", ext, err)
		}
		if cat == "other" {
			t.Errorf("ext %s mapped to other; other has no rows", ext)
		}
		if ext != strings.ToLower(ext) || strings.Contains(ext, ".") {
			t.Errorf("ext %q not lowercase without dot", ext)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("file_categories is empty")
	}
}

func TestOpen_Pragmas(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	conns := make([]*sql.Conn, 3)
	for i := range conns {
		c, err := d.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = c
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i, c := range conns {
		var jm string
		var fk, bt int
		if err := c.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&jm); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&bt); err != nil {
			t.Fatal(err)
		}
		if jm != "wal" || fk != 1 || bt != 5000 {
			t.Errorf("conn %d: journal_mode=%s foreign_keys=%d busy_timeout=%d", i, jm, fk, bt)
		}
	}
}

func TestOpen_PathSpecialChars(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	dir := filepath.Join(parent, "a b#c?d%20e")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hogwatch.db")
	d, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		t.Fatalf("DB not at %q: %v", path, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "a b#c?d%20e" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("parent holds %q, want only the data dir", names)
	}
	inner, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range inner {
		switch e.Name() {
		case "hogwatch.db", "hogwatch.db-wal", "hogwatch.db-shm":
		default:
			t.Errorf("stray entry %q in data dir", e.Name())
		}
	}
}

func TestOpen_ConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hogwatch.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	if err := Migrate(ctx, a); err != nil {
		t.Fatal(err)
	}
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()

	const n = 200
	errs := make(chan error, 2*n)
	var wg sync.WaitGroup
	for w, h := range []*sql.DB{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range n {
				errs <- writeSetting(ctx, h, w, i)
			}
		}()
	}
	wg.Wait()
	close(errs)
	var failed []error
	for err := range errs {
		if err != nil {
			failed = append(failed, err)
		}
	}
	if len(failed) > 0 {
		t.Fatalf("%d of %d writes failed; first: %v", len(failed), 2*n, failed[0])
	}
	var cnt int
	if err := a.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'counter'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 2*n {
		t.Fatalf("counter = %d, want %d", cnt, 2*n)
	}
}

// writeSetting reads before it writes inside one transaction: the pattern
// whose deferred-lock upgrade deadlocks without _txlock=immediate.
func writeSetting(ctx context.Context, h *sql.DB, w, i int) error {
	tx, err := h.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var cur sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'counter'`).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES ('counter', '1')
		 ON CONFLICT (key) DO UPDATE SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT)`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		fmt.Sprintf("w%d", w), fmt.Sprint(i)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func seedUserAndRoot(t *testing.T, d *sql.DB) {
	t.Helper()
	mustExec(t, d, `INSERT INTO users (id, username, created_at) VALUES (1, 'alice', 0)`)
	mustExec(t, d, `INSERT INTO scan_roots (id, name, container_path, host_path)
		VALUES (1, 'storage', '/host/storage', '/mnt/storage')`)
}

func TestSchema_Constraints(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	seedUserAndRoot(t, d)

	if _, err := d.ExecContext(ctx, `INSERT INTO user_paths (user_id, root_id, rel_path, created_at)
		VALUES (999, 1, x'61', 0)`); err == nil {
		t.Error("user_paths with an unknown user: no FK error")
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO jobs (kind, root_id, created_at)
		VALUES ('x', 1, 0)`); err == nil {
		t.Error("jobs.kind = 'x': no CHECK error")
	}
	mustExec(t, d, `INSERT INTO scanner_status (id, heartbeat_at, version) VALUES (1, 0, 'v')`)
	if _, err := d.ExecContext(ctx, `INSERT INTO scanner_status (id, heartbeat_at, version)
		VALUES (2, 0, 'v')`); err == nil {
		t.Error("scanner_status id = 2: no CHECK error")
	}

	mustExec(t, d, `INSERT INTO sessions (id_hash, user_id, created_at, expires_at, last_seen_at)
		VALUES (x'01', 1, 0, 1, 0)`)
	mustExec(t, d, `DELETE FROM users WHERE id = 1`)
	var n int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("sessions after user delete = %d, want 0 (cascade)", n)
	}
}

func TestSchema_BlobRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	seedUserAndRoot(t, d)
	raw := []byte("\xff\n\x00a")

	mustExec(t, d, `INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (1, 1, ?, 0)`, raw)
	mustExec(t, d, `INSERT INTO jobs (id, kind, root_id, created_at) VALUES (1, 'listing', 1, 0)`)
	mustExec(t, d, `INSERT INTO listing_results (job_id, seq, rel_name, type, size, alloc, mtime, uid)
		VALUES (1, 0, ?, 'f', 0, 0, 0, 0)`, raw)

	for _, q := range []string{
		`SELECT rel_path FROM user_paths`,
		`SELECT rel_name FROM listing_results`,
	} {
		var got []byte
		if err := d.QueryRowContext(ctx, q).Scan(&got); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if !bytes.Equal(got, raw) {
			t.Errorf("%s = %q, want %q", q, got, raw)
		}
	}
}

func TestMigrate_0002UpDown(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	p, err := newProvider(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("DownTo(0): %v", err)
	}
	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Down of 0002 only: its tables and columns go, 0001 stays.
	if _, err := p.DownTo(ctx, 1); err != nil {
		t.Fatalf("DownTo(1): %v", err)
	}
	for _, name := range []string{"config_issues", "path_stats"} {
		if slices.Contains(tableNames(t, d), name) {
			t.Errorf("table %s survives Down", name)
		}
	}
	if hasColumn(t, d, "scans", "job_id") || hasColumn(t, d, "scans", "error") {
		t.Error("scans columns survive Down")
	}
	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	for _, c := range [][2]string{
		{"scans", "job_id"}, {"scans", "error"},
		{"config_issues", "details"}, {"config_issues", "scan_id"},
		{"path_stats", "shared_hardlink_bytes"}, {"path_stats", "zfs_used_snapshots"},
	} {
		if !hasColumn(t, d, c[0], c[1]) {
			t.Errorf("missing column %s.%s", c[0], c[1])
		}
	}
	var idx int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_index_list('config_issues') WHERE name = 'config_issues_root_kind'`).Scan(&idx); err != nil || idx != 1 {
		t.Errorf("config_issues index: %d, %v", idx, err)
	}
}

func hasColumn(t *testing.T, d *sql.DB, table, col string) bool {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestEnsureMigrated_Concurrent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hogwatch.db")
	var conns [2]*sql.DB
	for i := range conns {
		d, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		conns[i] = d
	}
	errs := make(chan error, len(conns))
	var wg sync.WaitGroup
	for _, d := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- EnsureMigrated(ctx, d)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("EnsureMigrated: %v", err)
		}
	}
	p, err := newProvider(conns[0])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != 5 {
		t.Fatalf("goose version = %d, %v; want 5", v, err)
	}
}

func TestMigrate_0005Prefs(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	if _, err := d.ExecContext(ctx, `INSERT INTO users (username, created_at) VALUES ('u', 1)`); err != nil {
		t.Fatal(err)
	}
	var prefs string
	if err := d.QueryRowContext(ctx, `SELECT prefs FROM users WHERE username = 'u'`).Scan(&prefs); err != nil || prefs != "{}" {
		t.Fatalf("default prefs = %q, %v; want {}", prefs, err)
	}
	p, err := newProvider(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 4); err != nil {
		t.Fatalf("DownTo(4): %v", err)
	}
	if hasColumn(t, d, "users", "prefs") {
		t.Error("users.prefs survives Down")
	}
	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if !hasColumn(t, d, "users", "prefs") {
		t.Error("users.prefs missing after re-Up")
	}
}

func TestMigrate_0003Notice(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	insert := func(name string, notice any) error {
		_, err := d.ExecContext(ctx,
			`INSERT INTO users (username, created_at, notice) VALUES (?, 1, ?)`, name, notice)
		return err
	}
	for i, n := range []any{nil, "oidc_linked", "oidc_unlinked"} {
		if err := insert(fmt.Sprintf("u%d", i), n); err != nil {
			t.Errorf("notice %v: %v", n, err)
		}
	}
	if err := insert("bad", "bogus"); err == nil {
		t.Error("notice 'bogus' passed the CHECK")
	}
	// Up is idempotent through the goose version table; Down drops the column and Up restores it.
	p, err := newProvider(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 2); err != nil {
		t.Fatalf("DownTo(2): %v", err)
	}
	if hasColumn(t, d, "users", "notice") {
		t.Error("users.notice survives Down")
	}
	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("repeat Migrate: %v", err)
	}
	if !hasColumn(t, d, "users", "notice") {
		t.Error("users.notice missing after re-Up")
	}
}
