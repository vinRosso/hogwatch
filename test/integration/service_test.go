//go:build integration && linux

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"github.com/vinRosso/hogwatch/internal/scanner/listing"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
)

// serviceBin is the hogwatch binary built once with -tags integration, so the
// HOGWATCH_TEST_PAUSE_PHASE hook is compiled in. Nothing but the kill test sets it.
var (
	serviceBinOnce sync.Once
	serviceBinPath string
	serviceBinErr  error
)

func serviceBinary(t *testing.T) string {
	t.Helper()
	serviceBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "hogwatch-bin-")
		if err != nil {
			serviceBinErr = err
			return
		}
		serviceBinPath = filepath.Join(dir, "hogwatch")
		cmd := exec.Command("go", "build", "-tags", "integration", "-o", serviceBinPath, "./cmd/hogwatch") //nolint:gosec // fixed arguments
		cmd.Dir = filepath.Join("..", "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			serviceBinErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if serviceBinErr != nil {
		t.Fatal(serviceBinErr)
	}
	return serviceBinPath
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("run via make test-integration")
	}
}

// mountTmpfs mounts a tmpfs on a fresh directory and unmounts it at cleanup.
func mountTmpfs(t *testing.T, opts string) string {
	t.Helper()
	dir := t.TempDir()
	if err := syscall.Mount("tmpfs", dir, "tmpfs", 0, opts); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Unmount(dir, syscall.MNT_DETACH) })
	return dir
}

func remount(t *testing.T, dir string, flags uintptr) {
	t.Helper()
	if err := syscall.Mount("", dir, "", syscall.MS_REMOUNT|flags, ""); err != nil {
		t.Fatalf("remount %s: %v", dir, err)
	}
}

func write(t *testing.T, root, rel string, size int) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func allocOf(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Blocks * 512
}

// lockedBuffer is an io.Writer a test can read while a process still writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// service is a running `hogwatch scan`.
type service struct {
	t    *testing.T
	cmd  *exec.Cmd
	out  *lockedBuffer
	done chan error
}

func startService(t *testing.T, data string, env ...string) *service {
	t.Helper()
	s := &service{t: t, out: &lockedBuffer{}, done: make(chan error, 1)}
	s.cmd = exec.Command(serviceBinary(t), "scan") //nolint:gosec // the binary this test built
	s.cmd.Env = append(os.Environ(), append([]string{"HOGWATCH_DATA_DIR=" + data, "HOGWATCH_CONFIG="}, env...)...)
	s.cmd.Stdout, s.cmd.Stderr = s.out, s.out
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { s.done <- s.cmd.Wait() }()
	t.Cleanup(func() { s.kill() })
	return s
}

// kill sends SIGKILL and waits. It is idempotent.
func (s *service) kill() {
	_ = s.cmd.Process.Signal(syscall.SIGKILL)
	_ = s.wait()
}

// stop sends SIGTERM and requires exit code 0.
func (s *service) stop() {
	s.t.Helper()
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	err := s.wait()
	if err != nil {
		s.t.Fatalf("service exit after SIGTERM: %v\n%s", err, s.out)
	}
}

func (s *service) wait() error {
	select {
	case err := <-s.done:
		s.done <- err
		return err
	case <-time.After(30 * time.Second):
		s.t.Error("service did not exit")
		return errors.New("timeout")
	}
}

// site is a data dir with its main database.
type site struct {
	t    *testing.T
	data string
	db   *sql.DB
	q    *dbq.Queries
}

func newSite(t *testing.T) *site {
	t.Helper()
	data := t.TempDir()
	d, err := db.Open(context.Background(), filepath.Join(data, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.EnsureMigrated(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return &site{t: t, data: data, db: d, q: dbq.New(d)}
}

func (s *site) exec(q string, args ...any) int64 {
	s.t.Helper()
	res, err := s.db.Exec(q, args...)
	if err != nil {
		s.t.Fatalf("%s: %v", q, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (s *site) addRoot(dir string) int64 {
	return s.exec(`INSERT INTO scan_roots (name, container_path, host_path, schedule, enabled) VALUES ('r', ?, '/r', '', 1)`, dir)
}

func (s *site) addUser(name string, admin bool, paths map[int64][]string) int64 {
	s.t.Helper()
	a := 0
	if admin {
		a = 1
	}
	id := s.exec(`INSERT INTO users (username, admin_local, can_list_files, created_at) VALUES (?, ?, 1, 1)`, name, a)
	for root, ps := range paths {
		for _, p := range ps {
			s.exec(`INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, ?, ?, 1)`, id, root, []byte(p))
		}
	}
	return id
}

func (s *site) queueScan(root int64) int64 {
	s.t.Helper()
	id, err := s.q.InsertJob(context.Background(), dbq.InsertJobParams{
		Kind: "scan", RootID: root, Params: `{"trigger":"manual"}`, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *site) queueListing(root, user int64, node, params string) int64 {
	s.t.Helper()
	id, err := s.q.InsertJob(context.Background(), dbq.InsertJobParams{
		Kind: "listing", RootID: root, TargetNode: sql.NullString{String: node, Valid: true},
		Params: params, RequestedBy: sql.NullInt64{Int64: user, Valid: true}, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *site) job(id int64) (status, errMsg, progress string) {
	s.t.Helper()
	var e, p sql.NullString
	if err := s.db.QueryRow(`SELECT status, error, progress FROM jobs WHERE id = ?`, id).Scan(&status, &e, &p); err != nil {
		s.t.Fatal(err)
	}
	return status, e.String, p.String
}

// waitJob waits until the job has one of the statuses (or its log context
// ends the test) and returns its status and error.
func (s *site) waitJob(svc *service, id int64, within time.Duration, statuses ...string) (status, errMsg string) {
	s.t.Helper()
	deadline := time.Now().Add(within)
	for {
		st, msg, _ := s.job(id)
		if slices.Contains(statuses, st) {
			return st, msg
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("job %d is %q (%s), want one of %v after %v\nservice output:\n%s", id, st, msg, statuses, within, svc.out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *site) waitProgress(svc *service, id int64, within time.Duration, ok func(db.Progress) bool) {
	s.t.Helper()
	deadline := time.Now().Add(within)
	for {
		_, _, raw := s.job(id)
		var p db.Progress
		if raw != "" && json.Unmarshal([]byte(raw), &p) == nil && ok(p) {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("job %d progress %q never satisfied the condition\nservice output:\n%s", id, raw, svc.out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *site) scansDir() []string {
	s.t.Helper()
	ents, err := os.ReadDir(filepath.Join(s.data, "scans"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// waitScansDir waits until the scans directory holds exactly want.
func (s *site) waitScansDir(want ...string) {
	s.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := s.scansDir()
		slices.Sort(got)
		if slices.Equal(got, want) || (len(got) == 0 && len(want) == 0) {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("scans directory = %v, want %v", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *site) currentScan(root int64) (int64, bool) {
	s.t.Helper()
	var n sql.NullInt64
	if err := s.db.QueryRow(`SELECT current_scan_id FROM scan_roots WHERE id = ?`, root).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n.Int64, n.Valid
}

// node returns the reference of the directory at segs below the root of scan.
func (s *site) node(root, scan int64, segs ...string) string {
	s.t.Helper()
	g, _, err := gen.OpenReadOnly(context.Background(), gen.Path(s.data, root, scan))
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	raw := make([][]byte, len(segs))
	for i, sg := range segs {
		raw[i] = []byte(sg)
	}
	d, err := gen.Lookup(context.Background(), g, raw)
	if err != nil {
		s.t.Fatal(err)
	}
	return fmt.Sprintf("%d.%d", scan, d.ID)
}

// listingRows returns the rel_name and type of the listing's rows by seq.
func (s *site) listingRows(job int64) (names, types []string) {
	s.t.Helper()
	rs, err := s.db.Query(`SELECT CAST(rel_name AS TEXT), type FROM listing_results WHERE job_id = ? ORDER BY seq`, job)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		var n, ty string
		if err := rs.Scan(&n, &ty); err != nil {
			s.t.Fatal(err)
		}
		names, types = append(names, n), append(types, ty)
	}
	if err := rs.Err(); err != nil {
		s.t.Fatal(err)
	}
	return names, types
}

// fixture is the e2e tree on a tmpfs: users 1 (a) and 2 (b) share a hardlink.
type fixture struct {
	site         *site
	tree         string
	root         int64
	u1, u2, adm  int64
	a1, a2, a3   string
	b4, sentinel string
}

// newFixture builds the tree and the rows. The tmpfs is left read-write; the
// caller remounts it read-only before a scan the service must accept.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	requireRoot(t)
	f := &fixture{site: newSite(t), tree: mountTmpfs(t, "size=16m")}
	f.a1 = write(t, f.tree, "a/1", 1000)
	f.a2 = write(t, f.tree, "a/x/2", 2000)
	f.a3 = write(t, f.tree, "a/3", 300)
	write(t, f.tree, "a/sub/inner", 7)
	if err := os.MkdirAll(filepath.Join(f.tree, "a", "n"), 0o750); err != nil {
		t.Fatal(err)
	}
	f.b4 = write(t, f.tree, "b/4", 4000)
	if err := os.Link(f.a1, filepath.Join(f.tree, "b", "1")); err != nil {
		t.Fatal(err)
	}
	f.sentinel = write(t, f.tree, "z/SENTINEL_FILE", 9)

	f.root = f.site.addRoot(f.tree)
	f.u1 = f.site.addUser("u1", false, map[int64][]string{f.root: {"a"}})
	f.u2 = f.site.addUser("u2", false, map[int64][]string{f.root: {"b"}})
	f.adm = f.site.addUser("admin", true, nil)
	return f
}

func TestService_EndToEnd(t *testing.T) {
	f := newFixture(t)
	s := f.site
	remount(t, f.tree, syscall.MS_RDONLY)
	job := s.queueScan(f.root)
	svc := startService(t, s.data)

	if st, msg := s.waitJob(svc, job, 60*time.Second, "done", "failed"); st != "done" {
		t.Fatalf("scan job = %q (%s)\nservice output:\n%s", st, msg, svc.out)
	}
	path := gen.Path(s.data, f.root, 1)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("generation missing: %v", err)
	}
	if err := build.Verify(context.Background(), path); err != nil {
		t.Fatalf("build.Verify: %v", err)
	}
	if cur, ok := s.currentScan(f.root); !ok || cur != 1 {
		t.Errorf("current_scan_id = %d, %v, want 1", cur, ok)
	}

	type urow struct{ user, logical, alloc, files, shared int64 }
	rs, err := s.db.Query(`SELECT user_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes FROM usage WHERE scan_id = 1 ORDER BY user_id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []urow
	for rs.Next() {
		var u urow
		if err := rs.Scan(&u.user, &u.logical, &u.alloc, &u.files, &u.shared); err != nil {
			t.Fatal(err)
		}
		got = append(got, u)
	}
	_ = rs.Close()
	a1 := allocOf(t, f.a1)
	want := []urow{
		{f.u1, 3300 + 7, a1 + allocOf(t, f.a2) + allocOf(t, f.a3) + allocOf(t, filepath.Join(f.tree, "a/sub/inner")), 4, 0},
		{f.u2, 5000, allocOf(t, f.b4) + a1, 2, a1}, // b/1 is a link of a/1, which user 1's path holds
	}
	if len(got) != len(want) {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("usage[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM history_totals WHERE scope = 'mount' AND root_id = ?`, f.root).Scan(&n); err != nil || n != 1 {
		t.Errorf("history_totals mount rows = %d (%v), want 1", n, err)
	}
	var hb int64
	if err := s.db.QueryRow(`SELECT heartbeat_at FROM scanner_status`).Scan(&hb); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(time.Unix(hb, 0)); d > 15*time.Second || d < -15*time.Second {
		t.Errorf("heartbeat_at is %v old, want within 15 s", d)
	}
	svc.stop()
}

func TestService_WritableRootRefused(t *testing.T) {
	f := newFixture(t) // left read-write
	s := f.site
	job := s.queueScan(f.root)
	svc := startService(t, s.data)

	st, msg := s.waitJob(svc, job, 60*time.Second, "done", "failed")
	if st != "failed" || !strings.Contains(msg, "root filesystem is writable") {
		t.Fatalf("scan job = %q (%s), want failed with 'root filesystem is writable'", st, msg)
	}
	svc.stop()
	if left := s.scansDir(); len(left) != 0 {
		t.Errorf("a refused scan left %v", left)
	}
	if _, ok := s.currentScan(f.root); ok {
		t.Error("a refused scan was published")
	}
}

func TestService_ListingNoFollowNoXDev(t *testing.T) {
	f := newFixture(t)
	s := f.site
	remount(t, f.tree, syscall.MS_RDONLY)
	scanJob := s.queueScan(f.root)
	svc := startService(t, s.data)
	if st, msg := s.waitJob(svc, scanJob, 60*time.Second, "done", "failed"); st != "done" {
		t.Fatalf("scan job = %q (%s)\n%s", st, msg, svc.out)
	}
	list := func(node, mode string) (status, msg string, id int64) {
		id = s.queueListing(f.root, f.adm, node, `{"mode":"`+mode+`"}`)
		status, msg = s.waitJob(svc, id, 30*time.Second, "done", "failed", "cancelled")
		return status, msg, id
	}
	nodeA := s.node(f.root, 1, "a")

	// A plain children listing.
	st, msg, id := list(nodeA, "children")
	names, types := s.listingRows(id)
	if st != "done" || !slices.Equal(names, []string{"1", "3", "n", "sub", "x"}) || !slices.Equal(types, []string{"file", "file", "dir", "dir", "dir"}) {
		t.Fatalf("children listing = %q (%s), rows %v %v", st, msg, names, types)
	}

	// A tmpfs mounted over a/n after the scan: a subtree listing skips it.
	remount(t, f.tree, 0) // read-write, so the tests below can change the tree
	nested := filepath.Join(f.tree, "a", "n")
	if err := syscall.Mount("tmpfs", nested, "tmpfs", 0, "size=1m"); err != nil {
		t.Fatalf("mount nested tmpfs: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Unmount(nested, syscall.MNT_DETACH) })
	write(t, nested, "in_nested_tmpfs", 5)
	st, msg, id = list(nodeA, "subtree")
	names, _ = s.listingRows(id)
	_, _, raw := s.job(id)
	var prog db.ListingProgress
	if err := json.Unmarshal([]byte(raw), &prog); err != nil {
		t.Fatal(err)
	}
	if st != "done" || prog.MountsSkipped != 1 || !slices.Equal(names, []string{"1", "3", "sub/inner", "x/2"}) {
		t.Errorf("subtree over a nested tmpfs = %q (%s), rows %v, progress %+v; want done, 4 rows without n/*, mounts_skipped 1", st, msg, names, prog)
	}

	// A listing whose target is the mountpoint itself is on another
	// filesystem: it fails and writes no rows.
	st, msg, id = list(s.node(f.root, 1, "a", "n"), "children")
	names, _ = s.listingRows(id)
	if st != "failed" || msg != "target is on another filesystem" || len(names) != 0 {
		t.Errorf("listing of a mountpoint = %q (%s), rows %v; want failed 'target is on another filesystem' with no rows", st, msg, names)
	}

	// a/sub is replaced by a symlink to a directory holding the sentinel.
	nodeSub := s.node(f.root, 1, "a", "sub")
	if err := os.Rename(filepath.Join(f.tree, "a", "sub"), filepath.Join(f.tree, "a", "sub.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.tree, "z"), filepath.Join(f.tree, "a", "sub")); err != nil {
		t.Fatal(err)
	}
	st, msg, id = list(nodeSub, "children")
	names, _ = s.listingRows(id)
	if st != "failed" || msg != "changed since last scan" || len(names) != 0 {
		t.Errorf("listing of a replaced dir = %q (%s), rows %v; want failed 'changed since last scan' with no rows", st, msg, names)
	}
	// A subtree listing over the swapped symlink reports it as a symlink and
	// does not follow it.
	st, msg, id = list(nodeA, "subtree")
	names, types = s.listingRows(id)
	if st != "done" {
		t.Fatalf("subtree listing after the swap = %q (%s)", st, msg)
	}
	for i, n := range names {
		if strings.Contains(n, "SENTINEL") || strings.HasPrefix(n, "sub/") {
			t.Errorf("row %q (%s) comes from behind the symlink", n, types[i])
		}
	}
	if i := slices.Index(names, "sub"); i < 0 || types[i] != "symlink" {
		t.Errorf("rows %v %v: want 'sub' listed as a symlink", names, types)
	}
	svc.stop()
}

// TestListing_SkipsMountAndZFSCtl runs listing.Run in-process: a tmpfs under
// the target is counted in MountsSkipped and its files are not listed. A
// directory named .zfs on a filesystem other than ZFS is an ordinary directory
// and is listed (only a ZFS dataset root hides its snapshot control directory;
// no ZFS is available to this suite).
func TestListing_SkipsMountAndZFSCtl(t *testing.T) {
	requireRoot(t)
	ctx := context.Background()
	s := newSite(t)
	tree := mountTmpfs(t, "size=4m")
	write(t, tree, "t/file", 3)
	if err := os.MkdirAll(filepath.Join(tree, "t", "m"), 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, tree, "t/.zfs/snap", 1)
	remount(t, tree, syscall.MS_RDONLY)
	root := s.addRoot(tree)
	if _, err := build.Run(ctx, build.Options{
		DataDir: s.data, RootID: root, ScanID: 1, Root: tree, ScannerVersion: "test",
		Walk: walk.Options{Workers: 1, Limits: agg.DefaultLimits()},
	}); err != nil {
		t.Fatal(err)
	}
	remount(t, tree, 0)
	nested := filepath.Join(tree, "t", "m")
	if err := syscall.Mount("tmpfs", nested, "tmpfs", 0, "size=1m"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Unmount(nested, syscall.MNT_DETACH) })
	write(t, nested, "hidden", 1)

	adm := s.addUser("admin", true, nil)
	id := s.queueListing(root, adm, s.node(root, 1, "t"), `{"mode":"subtree"}`)
	job, err := s.q.ClaimJob(ctx, dbq.ClaimJobParams{Kind: "listing", StartedAt: sql.NullInt64{Int64: 1, Valid: true}})
	if err != nil || job.ID != id {
		t.Fatalf("claim: %v (job %d, want %d)", err, job.ID, id)
	}
	row, err := s.q.GetRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := listing.Run(ctx, listing.Deps{DB: s.db, DataDir: s.data}, job, row)
	if err != nil {
		t.Fatal(err)
	}
	names, _ := s.listingRows(id)
	if prog.MountsSkipped != 1 || !slices.Equal(names, []string{"file", ".zfs/snap"}) {
		t.Errorf("progress %+v, rows %v; want mounts_skipped 1 and [file .zfs/snap]", prog, names)
	}
}

// bigTree fills a tmpfs with 200,000 empty files in 200 directories and
// returns it read-only.
func bigTree(t *testing.T) string {
	t.Helper()
	tree := mountTmpfs(t, "size=64m,nr_inodes=400k")
	for i := 0; i < 200; i++ {
		dir := filepath.Join(tree, fmt.Sprintf("d%03d", i))
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 1000; j++ {
			f, err := os.Create(filepath.Join(dir, fmt.Sprintf("f%04d", j))) //nolint:gosec // test temp dir
			if err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
		}
	}
	remount(t, tree, syscall.MS_RDONLY)
	return tree
}

// restartRecovered starts a service on the site and waits until its recovery is
// complete. Recover runs before the service publishes scanner_status, and it
// fails the running jobs before it deletes orphan files, so the status row (cleared
// here first) is the only signal that both steps are done.
func (s *site) restartRecovered(t *testing.T, env ...string) *service {
	t.Helper()
	s.exec(`DELETE FROM scanner_status`)
	svc := startService(t, s.data, env...)
	deadline := time.Now().Add(30 * time.Second)
	for {
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM scanner_status`).Scan(&n); err == nil && n == 1 {
			return svc
		}
		if time.Now().After(deadline) {
			t.Fatalf("the restarted service did not publish its status\n%s", svc.out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestService_KillNineRecovery(t *testing.T) {
	requireRoot(t)
	s := newSite(t)
	tree := bigTree(t)
	root := s.addRoot(tree)
	genFile := func(scan int64) string { return fmt.Sprintf("%d-%d.db", root, scan) }

	// 1. Killed in a phase the test holds open with HOGWATCH_TEST_PAUSE_PHASE,
	// so the kill is deterministic:
	//   walk:     the generation file exists, the walk has not started;
	//   finalize: the walk is done, the job says "finalize", the file is partial.
	// Each time the partial file must exist at the kill, and after the restart
	// not one file of any name may be left in scans/.
	phases := []struct {
		name string
		wait func(svc *service, job int64)
	}{
		{"walk", func(svc *service, job int64) {
			deadline := time.Now().Add(60 * time.Second)
			for len(s.scansDir()) == 0 {
				if time.Now().After(deadline) {
					t.Fatalf("no generation file appeared\n%s", svc.out)
				}
				time.Sleep(time.Millisecond)
			}
		}},
		{"finalize", func(svc *service, job int64) {
			s.waitProgress(svc, job, 60*time.Second, func(p db.Progress) bool { return p.Phase == "finalize" })
		}},
	}
	for i, ph := range phases {
		scan := int64(i + 1)
		job := s.queueScan(root)
		svc := startService(t, s.data, "HOGWATCH_TEST_PAUSE_PHASE="+ph.name)
		ph.wait(svc, job)
		if _, err := os.Stat(gen.Path(s.data, root, scan)); err != nil {
			t.Fatalf("%s: no partial generation right before the kill: %v", ph.name, err)
		}
		logScans(t, s, ph.name+": before the kill")
		svc.kill()

		svc = s.restartRecovered(t)
		if st, msg, _ := s.job(job); st != "failed" || msg != "scanner restarted" {
			t.Fatalf("%s: killed job = %q (%s), want failed 'scanner restarted'", ph.name, st, msg)
		}
		if left := s.scansDir(); len(left) != 0 { // not one file of any name
			t.Fatalf("%s: recovery left %v in scans/", ph.name, left)
		}
		if st, msg := scanRow(t, s, scan); st != "failed" || msg != "scanner restarted" {
			t.Errorf("%s: killed scan row = %q (%s), want failed 'scanner restarted'", ph.name, st, msg)
		}
		if _, ok := s.currentScan(root); ok {
			t.Errorf("%s: a killed scan was published", ph.name)
		}
		svc.stop()
	}

	// 2. A queued scan completes after the recoveries.
	svc := startService(t, s.data)
	job := s.queueScan(root)
	if st, msg := s.waitJob(svc, job, 90*time.Second, "done", "failed"); st != "done" {
		t.Fatalf("re-queued scan = %q (%s)\n%s", st, msg, svc.out)
	}
	old := int64(len(phases) + 1)
	s.waitScansDir(genFile(old))
	if cur, ok := s.currentScan(root); !ok || cur != old {
		t.Fatalf("current_scan_id = %d, %v, want %d", cur, ok, old)
	}
	svc.stop()

	// 3. Killed in phase publish, before the publish transaction. The old
	// generation must survive and stay current.
	svc = startService(t, s.data, "HOGWATCH_TEST_PAUSE_PHASE=publish")
	pubJob := s.queueScan(root)
	s.waitProgress(svc, pubJob, 90*time.Second, func(p db.Progress) bool { return p.Phase == "publish" })
	if _, err := os.Stat(gen.Path(s.data, root, old+1)); err != nil {
		t.Fatalf("publish: the new generation is missing right before the kill: %v", err)
	}
	svc.kill()

	svc = s.restartRecovered(t)
	if st, msg, _ := s.job(pubJob); st != "failed" || msg != "scanner restarted" {
		t.Fatalf("job killed in publish = %q (%s), want failed 'scanner restarted'", st, msg)
	}
	cur, ok := s.currentScan(root)
	if !ok || (cur != old && cur != old+1) {
		t.Fatalf("current_scan_id = %d, %v, want the old scan %d or the new scan %d", cur, ok, old, old+1)
	}
	// The current scan always has its file; nothing else is left.
	if got := s.scansDir(); !slices.Equal(got, []string{genFile(cur)}) {
		t.Fatalf("scans/ = %v, want only %s", got, genFile(cur))
	}
	if cur != old {
		t.Errorf("current_scan_id = %d: the publish was paused before its transaction, so scan %d must still be current", cur, old)
	}
	svc.stop()
}

func scanRow(t *testing.T, s *site, id int64) (status, errMsg string) {
	t.Helper()
	var e sql.NullString
	if err := s.db.QueryRow(`SELECT status, error FROM scans WHERE id = ?`, id).Scan(&status, &e); err != nil {
		t.Fatal(err)
	}
	return status, e.String
}

// logScans logs the entries of the scans directory with their sizes.
func logScans(t *testing.T, s *site, when string) {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(s.data, "scans"))
	for _, e := range ents {
		fi, _ := e.Info()
		var size int64
		if fi != nil {
			size = fi.Size()
		}
		t.Logf("%s: scans/%s (%d bytes)", when, e.Name(), size)
	}
}
