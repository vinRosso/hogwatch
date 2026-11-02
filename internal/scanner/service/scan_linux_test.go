//go:build linux

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/gen"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func put(t *testing.T, root, rel string, size int) string {
	t.Helper()
	p := filepath.Join(root, rel)
	must(t, os.MkdirAll(filepath.Dir(p), 0o750))
	must(t, os.WriteFile(p, make([]byte, size), 0o600))
	return p
}

// alloc is the allocated bytes of a file, as the scanner counts them.
func alloc(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Lstat(p)
	must(t, err)
	return fi.Sys().(*syscall.Stat_t).Blocks * 512
}

// pipelineOptions is testOptions with the scan pipeline wired in. Backoff is
// shortened, writable mounts are allowed (a temp dir is not read-only).
func pipelineOptions(t *testing.T, clk *fakeClock) Options {
	t.Helper()
	o := testOptions(t, clk)
	o.AllowWritable = true
	o.Workers = 2
	o.PublishBackoff = time.Millisecond
	o.hooks.startScan = startScan
	o.hooks.cleanup = func(ctx context.Context, s *service, now time.Time) { s.cleanup(ctx, now) }
	return o.withDefaults()
}

// setSetting stores a settings row.
func setSetting(t *testing.T, d *sql.DB, key, value string) {
	t.Helper()
	exec(t, d, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
}

// topEverywhere makes every directory qualify for top lists.
func topEverywhere(t *testing.T, d *sql.DB) {
	t.Helper()
	setSetting(t, d, "top_n", "3")
	setSetting(t, d, "top_max_depth", "5")
	setSetting(t, d, "top_min_bytes", "0")
}

// addRootAt inserts a root that scans the real directory dir.
func addRootAt(t *testing.T, d *sql.DB, name, dir string) int64 {
	t.Helper()
	res, err := d.ExecContext(context.Background(),
		`INSERT INTO scan_roots (name, container_path, host_path, schedule, enabled) VALUES (?, ?, ?, '', 1)`,
		name, dir, "/"+name)
	must(t, err)
	id, _ := res.LastInsertId()
	return id
}

func addUser(t *testing.T, d *sql.DB, id int64) {
	t.Helper()
	exec(t, d, `INSERT INTO users (id, username, created_at) VALUES (?, ?, 1)`, id, fmt.Sprintf("user%d", id))
}

func addPath(t *testing.T, d *sql.DB, id, user, root int64, rel string) {
	t.Helper()
	exec(t, d, `INSERT INTO user_paths (id, user_id, root_id, rel_path, created_at) VALUES (?, ?, ?, ?, 1)`, id, user, root, []byte(rel))
}

// runScanJob queues a manual scan job for root, claims it and runs the
// pipeline directly. It returns the job id and the pipeline's error.
func runScanJob(t *testing.T, s *service, root int64) (int64, error) {
	t.Helper()
	id := addJob(t, s.o.DB, "scan", root, "queued", `{"trigger":"manual"}`)
	j, err := s.q.ClaimJob(context.Background(), dbq.ClaimJobParams{Kind: "scan", StartedAt: sql.NullInt64{Int64: 1, Valid: true}})
	must(t, err)
	if j.ID != id {
		t.Fatalf("claimed job %d, want %d", j.ID, id)
	}
	return id, s.runScan(context.Background(), j)
}

func genFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dataDir, "scans"))
	if os.IsNotExist(err) {
		return nil
	}
	must(t, err)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func scanStatus(t *testing.T, d *sql.DB, id int64) (status, errMsg string) {
	t.Helper()
	var e sql.NullString
	must(t, d.QueryRow(`SELECT status, error FROM scans WHERE id = ?`, id).Scan(&status, &e))
	return status, e.String
}

func currentScan(t *testing.T, d *sql.DB, root int64) (id int64, ok bool) {
	t.Helper()
	var n sql.NullInt64
	must(t, d.QueryRow(`SELECT current_scan_id FROM scan_roots WHERE id = ?`, root).Scan(&n))
	return n.Int64, n.Valid
}

// capLog records log records.
type capLog struct {
	mu   sync.Mutex
	recs []slog.Record
	on   func(slog.Record)
}

func (c *capLog) Enabled(context.Context, slog.Level) bool { return true }
func (c *capLog) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.recs = append(c.recs, r)
	on := c.on
	c.mu.Unlock()
	if on != nil {
		on(r)
	}
	return nil
}
func (c *capLog) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capLog) WithGroup(string) slog.Handler      { return c }

func (c *capLog) count(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.recs {
		if r.Message == msg {
			n++
		}
	}
	return n
}

// fixture is the tree of totals.TestUsage_Cases with its expected values.
type fixture struct {
	root                   string
	a1, a2, a3, b4, b5, e7 string
	al                     func(...string) int64
}

func buildFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{root: t.TempDir()}
	f.a1 = put(t, f.root, "a/1", 1000)
	f.a2 = put(t, f.root, "a/x/2", 2000)
	f.a3 = put(t, f.root, "a/3", 300)
	must(t, os.MkdirAll(filepath.Join(f.root, "b"), 0o750))
	must(t, os.Link(f.a1, filepath.Join(f.root, "b/1")))
	f.b4 = put(t, f.root, "b/4", 4000)
	f.b5 = put(t, f.root, "b/s1/5", 500)
	must(t, os.MkdirAll(filepath.Join(f.root, "b/s2"), 0o750))
	must(t, os.Link(f.b5, filepath.Join(f.root, "b/s2/6")))
	f.e7 = put(t, f.root, "\xff\n/7", 70)
	f.al = func(ps ...string) (n int64) {
		for _, p := range ps {
			n += alloc(t, p)
		}
		return n
	}
	return f
}

// assign creates users 10, 20, 40 and 50 with the paths of Task 5's case.
func (f *fixture) assign(t *testing.T, d *sql.DB, root int64) {
	t.Helper()
	for _, u := range []int64{10, 20, 40, 50} {
		addUser(t, d, u)
	}
	addPath(t, d, 1, 10, root, "a")
	addPath(t, d, 2, 10, root, "a/x")
	addPath(t, d, 3, 20, root, "b")
	addPath(t, d, 4, 40, root, "missing")
	addPath(t, d, 5, 50, root, "\xff\n")
}

func TestRunScan_PublishesEverything(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	topEverywhere(t, o.DB)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	f.assign(t, o.DB, root)
	s := newService(o)

	job, err := runScanJob(t, s, root)
	if err != nil {
		t.Fatalf("runScan = %v", err)
	}
	if st, msg := jobStatus(t, o.DB, job); st != "done" {
		t.Fatalf("job = %q (%s), want done", st, msg)
	}
	scan := int64(1)
	if st, msg := scanStatus(t, o.DB, scan); st != "done" {
		t.Fatalf("scan = %q (%s), want done", st, msg)
	}
	if cur, ok := currentScan(t, o.DB, root); !ok || cur != scan {
		t.Fatalf("current_scan_id = %d, %v; want %d", cur, ok, scan)
	}

	type urow struct{ user, logical, alloc, files, shared int64 }
	rs, err := o.DB.Query(`SELECT user_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes FROM usage WHERE scan_id = 1 ORDER BY user_id`)
	must(t, err)
	var got []urow
	for rs.Next() {
		var u urow
		must(t, rs.Scan(&u.user, &u.logical, &u.alloc, &u.files, &u.shared))
		got = append(got, u)
	}
	must(t, rs.Err())
	_ = rs.Close()
	want := []urow{
		{10, 3300, f.al(f.a1, f.a2, f.a3), 3, 0},
		{20, 5500, f.al(f.b4, f.b5) + f.al(f.a1), 3, f.al(f.a1)},
		{40, 0, 0, 0, 0},
		{50, 70, f.al(f.e7), 1, 0},
	}
	if len(got) != len(want) {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("usage[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	if n := count(t, o.DB, `SELECT count(*) FROM mount_stats WHERE scan_id = 1 AND fs_total > 0 AND fs_free >= 0`); n != 1 {
		t.Errorf("mount_stats rows with fs_total > 0 = %d, want 1", n)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM path_stats WHERE scan_id = 1`); n != 5 {
		t.Errorf("path_stats rows = %d, want 5", n)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM path_stats WHERE scan_id = 1 AND redundant = 1 AND user_path_id = 2`); n != 1 {
		t.Errorf("redundant a/x rows = %d, want 1", n)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM path_stats WHERE redundant = 1`); n != 1 {
		t.Errorf("redundant rows = %d, want 1", n)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM path_stats WHERE user_path_id = 4 AND found = 0`); n != 1 {
		t.Errorf("missing path rows = %d, want 1", n)
	}
	for scope, wantN := range map[string]int{"mount": 1, "user_mount": 4, "user": 4} {
		if n := count(t, o.DB, `SELECT count(*) FROM history_totals WHERE scope = ?`, scope); n != wantN {
			t.Errorf("history_totals scope %s = %d, want %d", scope, n, wantN)
		}
	}
	if n := count(t, o.DB, `SELECT count(*) FROM history_totals WHERE scope = 'mount' AND root_id = ? AND ts = ?`, root, t0.Unix()); n != 1 {
		t.Errorf("mount row for root %d at publish time = %d, want 1", root, n)
	}

	g, _, err := gen.OpenReadOnly(context.Background(), gen.Path(o.DataDir, root, scan))
	must(t, err)
	defer func() { _ = g.Close() }()
	anchors := count(t, g, `SELECT count(DISTINCT anchor_id) FROM top_files`)
	if anchors == 0 {
		t.Fatal("fixture has no top_files anchors")
	}
	if n := count(t, o.DB, `SELECT count(*) FROM history_dirs WHERE root_id = ?`, root); n != anchors {
		t.Errorf("history_dirs rows = %d, want %d (one per top_files anchor)", n, anchors)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM config_issues`); n != 0 {
		t.Errorf("config_issues = %d, want 0", n)
	}
	if got := genFiles(t, o.DataDir); len(got) != 1 || got[0] != "1-1.db" {
		t.Errorf("generation files = %v, want [1-1.db]", got)
	}
}

func TestRunScan_RootDisabled(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	root := addRootAt(t, o.DB, "r", t.TempDir())
	exec(t, o.DB, `UPDATE scan_roots SET enabled = 0 WHERE id = ?`, root)
	s := newService(o)
	job, err := runScanJob(t, s, root)
	if err == nil {
		t.Fatal("runScan = nil, want an error")
	}
	if st, msg := jobStatus(t, o.DB, job); st != "failed" || msg != "root disabled" {
		t.Errorf("job = %q %q, want failed \"root disabled\"", st, msg)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM scans`); n != 0 {
		t.Errorf("scans = %d, want 0", n)
	}
}

func TestRunScan_InvalidExclude(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	dir := t.TempDir()
	put(t, dir, "a/f", 1)
	root := addRootAt(t, o.DB, "r", dir)
	exec(t, o.DB, `UPDATE scan_roots SET excludes = '["a/../b"]' WHERE id = ?`, root)
	s := newService(o)
	job, _ := runScanJob(t, s, root)
	st, msg := jobStatus(t, o.DB, job)
	if st != "failed" || !strings.Contains(msg, "a/../b") {
		t.Errorf("job = %q %q, want failed naming the entry", st, msg)
	}
	if st, _ := scanStatus(t, o.DB, 1); st != "failed" {
		t.Errorf("scan = %q, want failed", st)
	}
	if got := genFiles(t, o.DataDir); len(got) != 0 {
		t.Errorf("generation files = %v, want none", got)
	}
}

func TestRunScan_Excludes(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	dir := t.TempDir()
	put(t, dir, "keep/f", 100)
	put(t, dir, "skip/f", 100)
	root := addRootAt(t, o.DB, "r", dir)
	exec(t, o.DB, `UPDATE scan_roots SET excludes = '["skip"]' WHERE id = ?`, root)
	addUser(t, o.DB, 1)
	addPath(t, o.DB, 1, 1, root, "")
	s := newService(o)
	if _, err := runScanJob(t, s, root); err != nil {
		t.Fatal(err)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM usage WHERE files = 1`); n != 1 {
		t.Errorf("usage with 1 file = %d, want 1 (skip/ excluded)", n)
	}
}

func TestRunScan_SecondScanSupersedes(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	o.Grace = time.Minute
	f1, f2 := buildFixture(t), buildFixture(t)
	r1 := addRootAt(t, o.DB, "r1", f1.root)
	r2 := addRootAt(t, o.DB, "r2", f2.root)
	addUser(t, o.DB, 10)
	addPath(t, o.DB, 1, 10, r1, "a")
	addPath(t, o.DB, 2, 10, r2, "b")
	s := newService(o)

	for _, r := range []int64{r1, r2, r1} {
		if _, err := runScanJob(t, s, r); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Second)
	}
	first := gen.Path(o.DataDir, r1, 1)
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("first generation: %v, want it kept during the grace period", err)
	}
	s.cleanup(context.Background(), clk.Now())
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("first generation removed inside the grace period: %v", err)
	}
	clk.Advance(2 * time.Minute)
	s.cleanup(context.Background(), clk.Now())
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("first generation after grace: %v, want removed", err)
	}
	if st, _ := scanStatus(t, o.DB, 1); st != "deleted" {
		t.Errorf("scan 1 = %q, want deleted", st)
	}
	if st, _ := scanStatus(t, o.DB, 3); st != "done" {
		t.Errorf("scan 3 = %q, want done", st)
	}
	if _, err := os.Stat(gen.Path(o.DataDir, r2, 2)); err != nil {
		t.Errorf("other root's current generation: %v", err)
	}

	var alloc3, alloc2, user int64
	must(t, o.DB.QueryRow(`SELECT bytes_alloc FROM usage WHERE scan_id = 3`).Scan(&alloc3))
	must(t, o.DB.QueryRow(`SELECT bytes_alloc FROM usage WHERE scan_id = 2`).Scan(&alloc2))
	must(t, o.DB.QueryRow(`SELECT bytes_alloc FROM history_totals WHERE scope = 'user' AND user_id = 10 ORDER BY rowid DESC LIMIT 1`).Scan(&user))
	if user != alloc3+alloc2 {
		t.Errorf("latest user history = %d, want %d + %d (both roots' current usage)", user, alloc3, alloc2)
	}
}

func TestRunScan_FreeSpace(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	s := newService(o)
	if _, err := runScanJob(t, s, root); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(gen.Path(o.DataDir, root, 1))
	must(t, err)
	need := (fi.Size()*6 + 4) / 5 // ceil(1.2 * size)

	s.o.hooks.freeBytes = func(string) (int64, error) { return need - 1, nil }
	job, err := runScanJob(t, s, root)
	if err == nil {
		t.Fatal("runScan with too little space = nil, want an error")
	}
	st, msg := jobStatus(t, o.DB, job)
	want := fmt.Sprintf("insufficient free space in data dir: need %d bytes, have %d", need, need-1)
	if st != "failed" || msg != want {
		t.Errorf("job = %q %q, want failed %q", st, msg, want)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM scans WHERE status = 'running'`); n != 0 {
		t.Errorf("%d scans left running", n)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM scans`); n != 1 {
		t.Errorf("scans = %d, want 1 (the check runs before the scan row)", n)
	}
	if got := genFiles(t, o.DataDir); len(got) != 1 {
		t.Errorf("generation files = %v, want only the current one", got)
	}

	s.o.hooks.freeBytes = func(string) (int64, error) { return need, nil }
	if _, err := runScanJob(t, s, root); err != nil {
		t.Fatalf("runScan with exactly 1.2x free = %v, want success", err)
	}
}

func TestRunScan_Cancel(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	f.assign(t, o.DB, root)
	job := addJob(t, o.DB, "scan", root, "queued", `{"trigger":"manual"}`)

	var once sync.Once
	o.hooks.onProgress = func(ctx context.Context, _ db.Progress) {
		once.Do(func() {
			_, err := o.DB.Exec(`INSERT INTO jobs (kind, root_id, params, status, created_at) VALUES ('cancel', ?, ?, 'queued', 1)`,
				root, fmt.Sprintf(`{"job_id":%d}`, job))
			if err != nil {
				t.Error(err)
			}
			<-ctx.Done() // the walk goes on only once the cancel job has been applied
		})
	}
	h := startRun(t, o)
	waitFor(t, "scan job cancelled", func() bool { st, _ := jobStatus(t, o.DB, job); return st == "cancelled" })
	h.stop(t)

	if st, _ := scanStatus(t, o.DB, 1); st != "cancelled" {
		t.Errorf("scan = %q, want cancelled", st)
	}
	if got := genFiles(t, o.DataDir); len(got) != 0 {
		t.Errorf("generation files = %v, want none", got)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM usage`); n != 0 {
		t.Errorf("usage rows = %d, want 0", n)
	}
	if _, ok := currentScan(t, o.DB, root); ok {
		t.Error("current_scan_id set after a cancel")
	}
}

// TestRunScan_Shutdown pins the shutdown rule: the service stopping is not a
// user cancel. The partial file goes, the scan and job are failed with
// "scanner stopped" so no row is left running.
func TestRunScan_Shutdown(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	job := addJob(t, o.DB, "scan", root, "queued", `{"trigger":"manual"}`)

	inWalk := make(chan struct{})
	var once sync.Once
	o.hooks.onProgress = func(ctx context.Context, _ db.Progress) {
		once.Do(func() { close(inWalk); <-ctx.Done() })
	}
	h := startRun(t, o)
	<-inWalk
	h.stop(t)

	if st, msg := jobStatus(t, o.DB, job); st != "failed" || msg != "scanner stopped" {
		t.Errorf("job = %q %q, want failed \"scanner stopped\"", st, msg)
	}
	if st, msg := scanStatus(t, o.DB, 1); st != "failed" || msg != "scanner stopped" {
		t.Errorf("scan = %q %q, want failed \"scanner stopped\"", st, msg)
	}
	if got := genFiles(t, o.DataDir); len(got) != 0 {
		t.Errorf("generation files = %v, want none", got)
	}
}

// openLock opens a second connection on the main DB file and returns a
// function that takes the write lock on it and one that releases it.
func openLock(t *testing.T, dbFile string) (lock, unlock func()) {
	t.Helper()
	d2, err := db.Open(context.Background(), dbFile)
	must(t, err)
	t.Cleanup(func() { _ = d2.Close() })
	c, err := d2.Conn(context.Background())
	must(t, err)
	t.Cleanup(func() { _ = c.Close() })
	lock = func() {
		if _, err := c.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
			t.Error(err)
		}
	}
	unlock = func() {
		if _, err := c.ExecContext(context.Background(), `ROLLBACK`); err != nil {
			t.Error(err)
		}
	}
	return lock, unlock
}

// busyDB is a migrated main DB whose connections wait only busyMS ms for a
// lock, and its data dir. It stands in for the 5 s busy_timeout of
// production, which the plan's 6 s test would need to outlast.
func busyDB(t *testing.T, busyMS int) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "hogwatch.db")
	boot, err := db.Open(context.Background(), file) // creates the file with the right mode
	must(t, err)
	must(t, db.EnsureMigrated(context.Background(), boot))
	must(t, boot.Close())
	uri, err := db.FileURI(file, fmt.Sprintf("_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(%d)&_txlock=immediate", busyMS))
	must(t, err)
	d, err := sql.Open("sqlite", uri)
	must(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d, dir
}

// TestRunScan_PublishBusyRetry holds the write lock past the connection's
// busy_timeout, so the publish transaction fails with SQLITE_BUSY and is
// retried. The plan's 6 s hold past a 5 s timeout is scaled down: the DB here
// has a 100 ms busy_timeout, and the lock is released only once the retry has
// been logged, so the outcome does not depend on timing.
func TestRunScan_PublishBusyRetry(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	o.DB, o.DataDir = busyDB(t, 100)
	cl := &capLog{}
	o.Log = slog.New(cl)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	f.assign(t, o.DB, root)

	lock, unlock := openLock(t, filepath.Join(o.DataDir, "hogwatch.db"))
	var released sync.Once
	cl.on = func(r slog.Record) {
		if r.Message == "publish busy, retrying" {
			released.Do(unlock)
		}
	}
	o.hooks.beforePublish = lock
	s := newService(o)
	job, err := runScanJob(t, s, root)
	if err != nil {
		t.Fatalf("runScan = %v, want the retry to succeed", err)
	}
	if st, msg := jobStatus(t, o.DB, job); st != "done" {
		t.Fatalf("job = %q %q, want done", st, msg)
	}
	if n := cl.count("publish busy, retrying"); n < 1 {
		t.Errorf("retries logged = %d, want >= 1", n)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM usage`); n != 4 {
		t.Errorf("usage rows = %d, want 4", n)
	}
}

// busyError returns a real SQLITE_BUSY error: a transaction that cannot take
// the write lock another connection holds.
func busyError(t *testing.T) error {
	t.Helper()
	d, dir := busyDB(t, 1)
	lock, unlock := openLock(t, filepath.Join(dir, "hogwatch.db"))
	lock()
	defer unlock()
	tx, err := d.BeginTx(context.Background(), nil)
	if err == nil {
		_ = tx.Rollback()
		t.Fatal("BeginTx succeeded under a held write lock")
	}
	return err
}

func TestRunScan_PublishFailsCleanly(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	cl := &capLog{}
	o.Log = slog.New(cl)
	busy := busyError(t)
	var attempts int
	o.hooks.publish = func(context.Context, *service, *publication) error { attempts++; return busy }
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	f.assign(t, o.DB, root)
	s := newService(o)

	job, err := runScanJob(t, s, root)
	if err == nil {
		t.Fatal("runScan = nil, want the busy error")
	}
	if attempts != 6 {
		t.Errorf("publish attempts = %d, want 6 (the first try and 5 retries)", attempts)
	}
	if st, msg := jobStatus(t, o.DB, job); st != "failed" || msg == "" {
		t.Errorf("job = %q %q, want failed with the error text", st, msg)
	}
	if st, msg := scanStatus(t, o.DB, 1); st != "failed" || msg == "" {
		t.Errorf("scan = %q %q, want failed with the error text", st, msg)
	}
	if got := genFiles(t, o.DataDir); len(got) != 0 {
		t.Errorf("generation files = %v, want none", got)
	}
	if _, ok := currentScan(t, o.DB, root); ok {
		t.Error("current_scan_id set after a failed publish")
	}
	if n := count(t, o.DB, `SELECT count(*) FROM usage`); n != 0 {
		t.Errorf("usage rows = %d, want 0", n)
	}
}

// TestRunScan_PublishRollsBack pins that a publish that fails halfway leaves
// no row of the scan behind: the scan is cancelled after the usage rows are
// prepared, so FinishScan matches nothing after usage was inserted.
func TestRunScan_PublishRollsBack(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	f.assign(t, o.DB, root)
	o.hooks.beforePublish = func() { exec(t, o.DB, `UPDATE scans SET status = 'cancelled' WHERE id = 1`) }
	s := newService(o)
	job, err := runScanJob(t, s, root)
	if err == nil {
		t.Fatal("runScan = nil, want an error")
	}
	if st, _ := jobStatus(t, o.DB, job); st != "failed" {
		t.Errorf("job = %q, want failed", st)
	}
	for _, tbl := range []string{"usage", "mount_stats", "path_stats", "history_totals", "history_dirs"} {
		if n := count(t, o.DB, `SELECT count(*) FROM `+tbl); n != 0 {
			t.Errorf("%s rows = %d, want 0 after a rolled-back publish", tbl, n)
		}
	}
}

// TestRunScan_PathDeletedBeforePublish pins the M4 entry criterion: a user
// path deleted after usage was computed makes the publish recompute, so the
// scan ends done without a row for the vanished path or its owner.
func TestRunScan_PathDeletedBeforePublish(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	f.assign(t, o.DB, root)
	calls := 0
	o.hooks.beforePublish = func() {
		if calls++; calls == 1 {
			exec(t, o.DB, `DELETE FROM user_paths WHERE id = 3`)
		}
	}
	s := newService(o)
	job, err := runScanJob(t, s, root)
	if err != nil {
		t.Fatalf("runScan = %v", err)
	}
	if st, _ := jobStatus(t, o.DB, job); st != "done" {
		t.Errorf("job = %q, want done", st)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM path_stats WHERE user_path_id = 3`); n != 0 {
		t.Errorf("path_stats rows for the deleted path = %d", n)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM path_stats`); n != 4 {
		t.Errorf("path_stats rows = %d, want 4", n)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM usage WHERE user_id = 20`); n != 0 {
		t.Errorf("usage rows of the owner of the deleted path = %d, want 0", n)
	}
	if calls != 2 {
		t.Errorf("beforePublish ran %d times, want 2 (first attempt, then the recompute)", calls)
	}
}

func TestRunScan_PathAddedBeforePublish(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	f.assign(t, o.DB, root)
	calls := 0
	o.hooks.beforePublish = func() {
		if calls++; calls == 1 {
			addPath(t, o.DB, 6, 20, root, "b/new")
		}
	}
	s := newService(o)
	if _, err := runScanJob(t, s, root); err != nil {
		t.Fatalf("runScan = %v", err)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM path_stats WHERE user_path_id = 6`); n != 1 {
		t.Errorf("path_stats rows for the added path = %d, want 1", n)
	}
}

func TestRunScan_PathsKeepChanging(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	f.assign(t, o.DB, root)
	calls := 0
	o.hooks.beforePublish = func() {
		calls++
		exec(t, o.DB, `DELETE FROM user_paths WHERE id = (SELECT min(id) FROM user_paths WHERE id >= 3)`)
	}
	s := newService(o)
	job, err := runScanJob(t, s, root)
	if err != nil {
		t.Fatalf("runScan = %v", err)
	}
	if st, _ := jobStatus(t, o.DB, job); st != "done" {
		t.Errorf("job = %q, want done", st)
	}
	if calls != 2 {
		t.Errorf("beforePublish ran %d times, want 2", calls)
	}
	// Paths 3 and 4 were deleted, one per attempt; the second attempt skipped path 4.
	if n := count(t, o.DB, `SELECT count(*) FROM path_stats WHERE user_path_id IN (3, 4)`); n != 0 {
		t.Errorf("path_stats rows for deleted paths = %d", n)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM path_stats`); n != 3 {
		t.Errorf("path_stats rows = %d, want 3", n)
	}
	// User 40 owned only path 4, deleted after the second sum: its usage is not published.
	if n := count(t, o.DB, `SELECT count(*) FROM usage WHERE user_id = 40`); n != 0 {
		t.Errorf("usage rows of the user whose path vanished = %d, want 0", n)
	}
}

func TestRunScan_ETA(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)

	var mu sync.Mutex
	var seen []db.Progress
	o.hooks.onProgress = func(_ context.Context, p db.Progress) {
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
	}
	s := newService(o)
	collect := func() []db.Progress {
		mu.Lock()
		defer mu.Unlock()
		out := seen
		seen = nil
		return out
	}

	if _, err := runScanJob(t, s, root); err != nil {
		t.Fatal(err)
	}
	first := collect()
	if len(first) == 0 {
		t.Fatal("no progress written during the first scan")
	}
	for _, p := range first {
		if p.ETASeconds != nil || p.Phase != "walk" {
			t.Errorf("first scan progress = %+v, want phase walk and no eta", p)
		}
	}

	// The previous scan saw far more files than this one will.
	exec(t, o.DB, `UPDATE scans SET files = 1000000 WHERE id = 1`)
	if _, err := runScanJob(t, s, root); err != nil {
		t.Fatal(err)
	}
	var withETA int
	for _, p := range collect() {
		if p.ETASeconds != nil {
			withETA++
			if *p.ETASeconds <= 0 || p.RatePerSec <= 0 {
				t.Errorf("progress = %+v, want a positive eta and rate", p)
			}
		}
	}
	if withETA == 0 {
		t.Error("second scan wrote no progress row with an eta")
	}

	// The last progress row of a finished scan is the publish phase.
	var raw string
	must(t, o.DB.QueryRow(`SELECT progress FROM jobs WHERE id = 2`).Scan(&raw))
	var last db.Progress
	must(t, json.Unmarshal([]byte(raw), &last))
	if last.Phase != "publish" || last.Files == 0 {
		t.Errorf("final progress = %+v, want phase publish with the walk's counts", last)
	}
}

func TestRunScan_PublishPhase(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	var phases []string
	var mu sync.Mutex
	o.hooks.beforePublish = func() {
		// The publish phase is written before the transaction starts.
		var raw string
		must(t, o.DB.QueryRow(`SELECT progress FROM jobs WHERE id = 1`).Scan(&raw))
		var p db.Progress
		must(t, json.Unmarshal([]byte(raw), &p))
		mu.Lock()
		phases = append(phases, p.Phase)
		mu.Unlock()
	}
	s := newService(o)
	if _, err := runScanJob(t, s, root); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 1 || phases[0] != "publish" {
		t.Errorf("phase before the publish transaction = %v, want [publish]", phases)
	}
}

func TestRunScan_ConfigIssues(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	parent := t.TempDir()
	put(t, parent, "d/f", 10)
	put(t, parent, "other/g", 10)
	child := filepath.Join(parent, "d")
	rb := addRootAt(t, o.DB, "b", parent)
	ra := addRootAt(t, o.DB, "a", child)
	addUser(t, o.DB, 1)
	// The user's path d on root b is the very directory root a scans.
	addPath(t, o.DB, 1, 1, rb, "d")
	s := newService(o)

	for _, r := range []int64{ra, rb} {
		if _, err := runScanJob(t, s, r); err != nil {
			t.Fatal(err)
		}
	}
	rs, err := o.DB.Query(`SELECT root_id, kind, details FROM config_issues`)
	must(t, err)
	type issue struct {
		root          int64
		kind, details string
	}
	var got []issue
	for rs.Next() {
		var i issue
		must(t, rs.Scan(&i.root, &i.kind, &i.details))
		got = append(got, i)
	}
	must(t, rs.Err())
	_ = rs.Close()
	if len(got) != 1 || got[0].kind != "cross_root_duplicate" || got[0].root != rb {
		t.Fatalf("config_issues = %+v, want one cross_root_duplicate on root %d", got, rb)
	}
	var d db.CrossRootDuplicateDetails
	must(t, json.Unmarshal([]byte(got[0].details), &d))
	if d.UserPathID != 1 || d.OtherRootID != ra || !strings.HasPrefix(d.Node, "1.") {
		t.Errorf("details = %+v, want path 1, other root %d, a node of scan 1", d, ra)
	}

	// Removing the overlap and rescanning clears the issue.
	exec(t, o.DB, `DELETE FROM user_paths WHERE id = 1`)
	if _, err := runScanJob(t, s, rb); err != nil {
		t.Fatal(err)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM config_issues`); n != 0 {
		t.Errorf("config_issues after removing the overlap = %d, want 0", n)
	}
}

func TestRunScan_WritableRootRefused(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	o.AllowWritable = false // the temp dir is writable: the root itself is refused
	dir := t.TempDir()
	put(t, dir, "f", 10)
	root := addRootAt(t, o.DB, "r", dir)
	s := newService(o)
	job, err := runScanJob(t, s, root)
	if err == nil {
		t.Fatal("runScan on a writable root = nil, want the refusal")
	}
	if st, msg := jobStatus(t, o.DB, job); st != "failed" || !strings.Contains(msg, "writable") {
		t.Errorf("job = %q %q, want failed with a writable-root message", st, msg)
	}
}

func TestStartScan_SpawnsClaimedJob(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	job := addJob(t, o.DB, "scan", root, "queued", `{"trigger":"schedule"}`)
	h := startRun(t, o)
	waitFor(t, "job done", func() bool { st, _ := jobStatus(t, o.DB, job); return st == "done" })
	h.stop(t)
	var trig string
	must(t, o.DB.QueryRow(`SELECT trigger FROM scans WHERE job_id = ?`, job).Scan(&trig))
	if trig != "schedule" {
		t.Errorf("scan trigger = %q, want schedule", trig)
	}
}

// A lenient publish drops the usage of a user whose paths changed. That user's
// history must not record the resulting total, which would miss this root and
// read as a drop that never happened.
func TestRunScan_PathsKeepChangingWritesNoUserHistoryForDroppedUsers(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	f := buildFixture(t)
	root := addRootAt(t, o.DB, "r", f.root)
	f.assign(t, o.DB, root)
	// User 40 also has usage under another root, so SumCurrentUsageByUser
	// would return a row for it.
	other := addRoot(t, o.DB, "other", "", false)
	exec(t, o.DB, `INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (100, ?, 'x', 'done', 'manual', 1)`, other)
	exec(t, o.DB, `INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files) VALUES (100, 40, ?, 10, 10, 1)`, other)
	exec(t, o.DB, `UPDATE scan_roots SET current_scan_id = 100 WHERE id = ?`, other)
	o.hooks.beforePublish = func() {
		exec(t, o.DB, `DELETE FROM user_paths WHERE id = (SELECT min(id) FROM user_paths WHERE id >= 3)`)
	}
	s := newService(o)
	if _, err := runScanJob(t, s, root); err != nil {
		t.Fatalf("runScan = %v", err)
	}
	for _, scope := range []string{"user", "user_mount"} {
		if n := count(t, o.DB, `SELECT count(*) FROM history_totals WHERE scope = ? AND user_id = 40`, scope); n != 0 {
			t.Errorf("%s history rows for the user whose usage was dropped = %d, want 0", scope, n)
		}
	}
	if n := count(t, o.DB, `SELECT count(*) FROM history_totals WHERE scope = 'user' AND user_id = 10`); n != 1 {
		t.Errorf("user history rows for an unaffected user = %d, want 1", n)
	}
}
