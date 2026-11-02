package repo

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/gen/gentest"
	"github.com/vinRosso/hogwatch/internal/noderef"
	"github.com/vinRosso/hogwatch/internal/pathx"
)

type fixture struct {
	t       testing.TB
	db      *sql.DB
	dataDir string
	repo    *Repo
	gens    map[int64]*sql.DB
}

func newFixture(t testing.TB) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(dir, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: d, dataDir: dir, gens: map[int64]*sql.DB{}}
	f.repo = New(d, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = f.repo.Close() })
	f.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (1, 'r', '/mnt/r', '/srv/r')`)
	return f
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *fixture) user(name string) int64 {
	f.t.Helper()
	res, err := f.db.Exec(`INSERT INTO users (username, created_at) VALUES (?, 1)`, name)
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (f *fixture) path(user int64, rel string) int64 {
	f.t.Helper()
	res, err := f.db.Exec(`INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, 1, ?, 1)`, user, []byte(rel))
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// scan builds a done generation for the root.
func (f *fixture) scan(id int64, dirs ...gentest.Dir) {
	f.t.Helper()
	f.scanWith(id, gentest.BuildOpts{}, dirs...)
}

// scanWith is scan with top-N anchors.
func (f *fixture) scanWith(id int64, opts gentest.BuildOpts, dirs ...gentest.Dir) {
	f.t.Helper()
	file := gentest.BuildWith(f.t, f.dataDir, 1, id, dirs, opts)
	f.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (?, 1, ?, 'done', 'manual', 1)`, id, filepath.Base(file))
	d, _, err := gen.OpenReadOnly(f.t.Context(), file)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = d.Close() })
	f.gens[id] = d
}

func (f *fixture) ref(scan int64, rel string) noderef.Ref {
	f.t.Helper()
	var segs [][]byte
	for _, s := range pathx.Segments(rel) {
		segs = append(segs, []byte(s))
	}
	d, err := gen.Lookup(f.t.Context(), f.gens[scan], segs)
	if err != nil {
		f.t.Fatalf("ref %q: %v", rel, err)
	}
	return noderef.Ref{ScanID: scan, DirID: d.ID}
}

func (f *fixture) scope(user, scan int64) Scope {
	f.t.Helper()
	s, err := f.repo.ScopeFor(f.t.Context(), user, scan)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) visible(s Scope, scan int64, rel string) bool {
	f.t.Helper()
	_, err := f.repo.Node(f.t.Context(), s, f.ref(scan, rel))
	switch err {
	case nil:
		return true
	case ErrNotFound:
		return false
	}
	f.t.Fatalf("Node(%q): %v", rel, err)
	return false
}

var tree = []gentest.Dir{
	{Rel: "a", Files: 1, BytesAlloc: 10}, {Rel: "a/b", Files: 2, BytesAlloc: 20},
	{Rel: "a/b/c", Files: 3, BytesAlloc: 30}, {Rel: "a/x", Files: 4, BytesAlloc: 40},
	{Rel: "d", Files: 5, BytesAlloc: 50}, {Rel: "bad\xff", Files: 6, BytesAlloc: 60},
	{Rel: "bad\xff/sub"},
}

func TestScopeZero_Denies(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	if _, err := f.repo.Node(t.Context(), Scope{}, f.ref(1, "")); err != ErrNotFound {
		t.Fatalf("zero scope: %v", err)
	}
	if pred, _ := (Scope{}).where("pre"); pred != "0" {
		t.Fatalf("zero predicate %q", pred)
	}
}

func TestScopeFor_Basic(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	u := f.user("u")
	f.path(u, "a/b")
	s := f.scope(u, 1)
	for rel, want := range map[string]bool{"a/b": true, "a/b/c": true, "a": false, "": false, "a/x": false, "d": false} {
		if got := f.visible(s, 1, rel); got != want {
			t.Errorf("visible(%q) = %v, want %v", rel, got, want)
		}
	}
	n, err := f.repo.Node(t.Context(), s, f.ref(1, "a/b"))
	if err != nil || n.ParentRef != nil || n.BytesAlloc != 50 || n.Files != 5 || n.RootID != 1 || string(n.Name) != "b" {
		t.Fatalf("anchor node %+v, %v", n, err)
	}
	c, err := f.repo.Node(t.Context(), s, f.ref(1, "a/b/c"))
	if err != nil || c.ParentRef == nil || *c.ParentRef != f.ref(1, "a/b") {
		t.Fatalf("child node %+v, %v", c, err)
	}
}

func TestScopeFor_Degenerate(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)

	none := f.user("none")
	if s := f.scope(none, 1); f.visible(s, 1, "") || f.visible(s, 1, "a") {
		t.Fatal("user without paths sees data")
	}

	missing := f.user("missing")
	f.path(missing, "nope/deeper")
	if s := f.scope(missing, 1); f.visible(s, 1, "") || f.visible(s, 1, "a") || f.visible(s, 1, "d") {
		t.Fatal("missing path leaks ancestors or siblings")
	}

	nested := f.user("nested")
	f.path(nested, "a")
	f.path(nested, "a/b")
	s := f.scope(nested, 1)
	if !f.visible(s, 1, "a") || !f.visible(s, 1, "a/b/c") || !f.visible(s, 1, "a/x") || f.visible(s, 1, "d") || f.visible(s, 1, "") {
		t.Fatal("nested paths")
	}

	rootUser := f.user("root")
	f.path(rootUser, "")
	s = f.scope(rootUser, 1)
	for _, d := range tree {
		if !f.visible(s, 1, d.Rel) {
			t.Fatalf("root path hides %q", d.Rel)
		}
	}

	odd := f.user("odd")
	f.path(odd, "bad\xff")
	s = f.scope(odd, 1)
	if !f.visible(s, 1, "bad\xff") || !f.visible(s, 1, "bad\xff/sub") || f.visible(s, 1, "d") || f.visible(s, 1, "") {
		t.Fatal("invalid-UTF-8 path")
	}

	// A path that vanished in the latest scan: scan 2 has no "d".
	vanished := f.user("vanished")
	f.path(vanished, "d")
	f.scan(2, gentest.Dir{Rel: "a"})
	s = f.scope(vanished, 2)
	if f.visible(s, 2, "a") || f.visible(s, 2, "") {
		t.Fatal("vanished path leaks the new scan")
	}
}

func TestScopeFor_PathEditChangesKey(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	u := f.user("u")
	f.path(u, "a/b")
	if f.visible(f.scope(u, 1), 1, "d") {
		t.Fatal("d visible before the edit")
	}
	id := f.path(u, "d")
	if !f.visible(f.scope(u, 1), 1, "d") {
		t.Fatal("new path not picked up")
	}
	f.exec(`DELETE FROM user_paths WHERE id = ?`, id)
	if f.visible(f.scope(u, 1), 1, "d") {
		t.Fatal("deleted path still visible")
	}
}

func TestScope_OtherScanDenies(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	f.scan(2, tree...)
	u := f.user("u")
	f.path(u, "a")
	s := f.scope(u, 1)
	if _, err := f.repo.Node(t.Context(), s, f.ref(2, "a")); err != ErrNotFound {
		t.Fatalf("scope of scan 1 on scan 2: %v", err)
	}
	if _, err := f.repo.Node(t.Context(), ScopeAll(), f.ref(2, "a")); err != nil {
		t.Fatalf("admin scope: %v", err)
	}
}

func TestRepo_StaleRef(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	u := f.user("u")
	f.path(u, "a")
	s := f.scope(u, 1)
	ref := f.ref(1, "a")
	if _, err := f.repo.Node(t.Context(), s, ref); err != nil {
		t.Fatal(err)
	}

	// Unknown scan.
	if _, err := f.repo.Node(t.Context(), ScopeAll(), noderef.Ref{ScanID: 99, DirID: 1}); err != ErrNotFound {
		t.Fatalf("unknown scan: %v", err)
	}
	if _, err := f.repo.ScopeFor(t.Context(), u, 99); err != ErrNotFound {
		t.Fatalf("ScopeFor unknown scan: %v", err)
	}
	// Unknown dir.
	if _, err := f.repo.Node(t.Context(), ScopeAll(), noderef.Ref{ScanID: 1, DirID: 9999}); err != ErrNotFound {
		t.Fatalf("unknown dir: %v", err)
	}

	// The file goes away under an open cache entry, then the row is marked deleted.
	if err := os.Remove(filepath.Join(f.dataDir, "scans", "1-1.db")); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE scans SET status = 'deleted' WHERE id = 1`)
	if len(f.repo.gens) != 1 {
		t.Fatal("expected a cached handle before Sweep")
	}
	f.repo.Sweep(t.Context())
	if len(f.repo.gens) != 0 {
		t.Fatal("Sweep kept the handle")
	}
	if _, err := f.repo.Node(t.Context(), s, ref); err != ErrNotFound {
		t.Fatalf("deleted scan: %v", err)
	}

	// Failed scan.
	f.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (5, 1, 'x', 'failed', 'manual', 1)`)
	if _, err := f.repo.Node(t.Context(), ScopeAll(), noderef.Ref{ScanID: 5, DirID: 1}); err != ErrNotFound {
		t.Fatalf("failed scan: %v", err)
	}
	// Done row whose file is missing, with a cold cache.
	f.exec(`INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (6, 1, 'x', 'done', 'manual', 1)`)
	if _, err := f.repo.Node(t.Context(), ScopeAll(), noderef.Ref{ScanID: 6, DirID: 1}); err != ErrNotFound {
		t.Fatalf("missing file: %v", err)
	}
}

func TestNotFound_Identical(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	u := f.user("u")
	f.path(u, "a")
	s := f.scope(u, 1)
	_, outOfScope := f.repo.Node(t.Context(), s, f.ref(1, "d"))
	_, missing := f.repo.Node(t.Context(), s, noderef.Ref{ScanID: 1, DirID: 9999})
	if outOfScope != missing || outOfScope != ErrNotFound {
		t.Fatalf("out of scope %v, missing %v", outOfScope, missing)
	}
}

// TestRepo_ReusedScanID pins that a scan id reused by another generation
// (SQLite reuses the highest rowid after a delete) never serves the old file
// or the old scope.
func TestRepo_ReusedScanID(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(dir, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (1, 'r1', '/a', '/a'), (2, 'r2', '/b', '/b')`,
		`INSERT INTO users (id, username, created_at) VALUES (1, 'u', 1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// Generation one: root 1, scan 7, directory "home" with a "u" below it.
	f1 := gentest.Build(t, dir, 1, 7, []gentest.Dir{{Rel: "home/u", Files: 1, BytesAlloc: 10}})
	mustExec(t, d, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (7, 1, ?, 'done', 'manual', 1, 2)`, filepath.Base(f1))
	mustExec(t, d, `INSERT INTO user_paths (id, user_id, root_id, rel_path, created_at) VALUES (12, 1, 1, CAST('home/u' AS BLOB), 1)`)
	r := New(d, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = r.Close() }()
	s1, err := r.ScopeFor(ctx, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Node(ctx, s1, noderef.Ref{ScanID: 7, DirID: 3}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Delete root 1 (cascade), then reuse scan id 7 and path id 12 on root 2
	// with a different tree in which "home/u" is a different node.
	mustExec(t, d, `DELETE FROM scan_roots WHERE id = 1`)
	f2 := gentest.Build(t, dir, 2, 7, []gentest.Dir{{Rel: "other", Files: 1, BytesAlloc: 5}, {Rel: "home/u", Files: 1, BytesAlloc: 10}, {Rel: "home/v", Files: 9, BytesAlloc: 90}})
	mustExec(t, d, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (7, 2, ?, 'done', 'manual', 3, 4)`, filepath.Base(f2))
	mustExec(t, d, `INSERT INTO user_paths (id, user_id, root_id, rel_path, created_at) VALUES (12, 1, 2, CAST('home/u' AS BLOB), 1)`)
	s2, err := r.ScopeFor(ctx, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	// In the new tree the scope must cover exactly the new "home/u" and not "home/v".
	seg := func(name string) noderef.Ref {
		g, _, err := gen.OpenReadOnly(ctx, f2)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = g.Close() }()
		n, err := gen.Lookup(ctx, g, [][]byte{[]byte("home"), []byte(name)})
		if err != nil {
			t.Fatal(err)
		}
		return noderef.Ref{ScanID: 7, DirID: n.ID}
	}
	if _, err := r.Node(ctx, s2, seg("u")); err != nil {
		t.Fatalf("new home/u not visible: %v", err)
	}
	if _, err := r.Node(ctx, s2, seg("v")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sibling home/v visible through a stale scope: %v", err)
	}
}

func mustExec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// Evicting a generation while a request holds it must not break that request:
// the handle closes when the last reference is released.
func TestRepo_EvictWhileInUse(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	e, err := f.repo.open(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	f.repo.evict(1)
	var one int
	if err := e.db.QueryRowContext(t.Context(), `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("a query on an evicted but held handle failed: %v", err)
	}
	f.repo.release(e)
	if err := e.db.QueryRowContext(t.Context(), `SELECT 1`).Scan(&one); err == nil {
		t.Fatal("the handle stayed open after the last release")
	}
	// The next request reopens the generation.
	if _, err := f.repo.Node(t.Context(), ScopeAll(), f.ref(1, "a")); err != nil {
		t.Fatalf("Node after eviction: %v", err)
	}
}

func TestRepo_CloseWaitsForHolders(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	e, err := f.repo.open(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Close(); err != nil {
		t.Fatal(err)
	}
	var one int
	if err := e.db.QueryRowContext(t.Context(), `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("Close broke a request that still held the handle: %v", err)
	}
	f.repo.release(e)
	if err := e.db.QueryRowContext(t.Context(), `SELECT 1`).Scan(&one); err == nil {
		t.Fatal("the handle stayed open after the last release")
	}
}

// Concurrent readers and evictions (run with -race) never see a false 404.
func TestRepo_EvictRace(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	ref := f.ref(1, "a")
	stop := make(chan struct{})
	var evictor sync.WaitGroup
	evictor.Add(1)
	go func() {
		defer evictor.Done()
		for {
			select {
			case <-stop:
				return
			default:
				f.repo.evict(1)
			}
		}
	}()
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 200 {
				if _, err := f.repo.Node(context.Background(), ScopeAll(), ref); err != nil {
					t.Errorf("Node = %v", err)
					return
				}
			}
		}()
	}
	readers.Wait()
	close(stop)
	evictor.Wait()
}
