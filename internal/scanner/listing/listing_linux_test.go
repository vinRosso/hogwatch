//go:build linux

package listing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
)

// base is the clock of every build and listing here.
var base = time.Unix(1_800_000_000, 0)

const day = 24 * time.Hour

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mkdirs(t *testing.T, root string, rel ...string) {
	t.Helper()
	for _, r := range rel {
		must(t, os.MkdirAll(filepath.Join(root, r), 0o750))
	}
}

// writeFile creates root/rel with size bytes and an mtime of age before base.
func writeFile(t *testing.T, root, rel string, size int64, age time.Duration) {
	t.Helper()
	p := filepath.Join(root, rel)
	must(t, os.WriteFile(p, nil, 0o600))
	must(t, os.Truncate(p, size))
	m := base.Add(-age)
	must(t, os.Chtimes(p, m, m))
}

type env struct {
	t    *testing.T
	main *sql.DB
	data string
	root string // the scanned directory, root 1
	row  dbq.ScanRoot
}

// newEnv builds the tree with populate, scans it into generation 1 and
// registers root 1 in a fresh main database.
func newEnv(t *testing.T, populate func(root string), mut ...func(*build.Options)) *env {
	t.Helper()
	ctx := context.Background()
	e := &env{t: t, data: t.TempDir(), root: t.TempDir()}
	populate(e.root)
	bo := build.Options{
		DataDir: e.data, RootID: 1, ScanID: 1, Root: e.root,
		Walk:           walk.Options{Workers: 2, AllowWritable: true, Limits: agg.DefaultLimits()},
		ScannerVersion: "test",
		Now:            func() time.Time { return base },
	}
	for _, m := range mut {
		m(&bo)
	}
	_, err := build.Run(ctx, bo)
	must(t, err)
	e.main, err = db.Open(ctx, filepath.Join(t.TempDir(), "hogwatch.db"))
	must(t, err)
	t.Cleanup(func() { _ = e.main.Close() })
	must(t, db.Migrate(ctx, e.main))
	_, err = e.main.Exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (1, 'r', ?, '/h')`, e.root)
	must(t, err)
	e.row, err = dbq.New(e.main).GetRoot(ctx, 1)
	must(t, err)
	return e
}

func (e *env) user(name string, admin, canList, disabled bool, paths ...string) int64 {
	e.t.Helper()
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	res, err := e.main.Exec(`INSERT INTO users (username, admin_local, can_list_files, disabled, created_at)
		VALUES (?, ?, ?, ?, 0)`, name, b(admin), b(canList), b(disabled))
	must(e.t, err)
	id, err := res.LastInsertId()
	must(e.t, err)
	for _, p := range paths {
		_, err = e.main.Exec(`INSERT INTO user_paths (user_id, root_id, rel_path, created_at) VALUES (?, 1, ?, 0)`, id, []byte(p))
		must(e.t, err)
	}
	return id
}

// node returns the reference of the directory at segs below the root.
func (e *env) node(segs ...string) string {
	e.t.Helper()
	gdb, _, err := gen.OpenReadOnly(context.Background(), gen.Path(e.data, 1, 1))
	must(e.t, err)
	defer func() { _ = gdb.Close() }()
	raw := make([][]byte, len(segs))
	for i, s := range segs {
		raw[i] = []byte(s)
	}
	d, err := gen.Lookup(context.Background(), gdb, raw)
	must(e.t, err)
	return fmt.Sprintf("1.%d", d.ID)
}

func (e *env) job(user int64, node string, p db.ListingParams) dbq.Job {
	e.t.Helper()
	raw, err := json.Marshal(p)
	must(e.t, err)
	q := dbq.New(e.main)
	id, err := q.InsertJob(context.Background(), dbq.InsertJobParams{
		Kind: "listing", RootID: 1, TargetNode: sql.NullString{String: node, Valid: true},
		Params: string(raw), RequestedBy: sql.NullInt64{Int64: user, Valid: true}, CreatedAt: base.Unix(),
	})
	must(e.t, err)
	j, err := q.GetJob(context.Background(), id)
	must(e.t, err)
	return j
}

func (e *env) deps() Deps {
	return Deps{DB: e.main, DataDir: e.data, Now: func() time.Time { return base }}
}

type row struct {
	Name        string
	Type        string
	Size, Alloc int64
	Mtime, UID  int64
}

func (e *env) rows(jobID int64) []row {
	e.t.Helper()
	rs, err := e.main.Query(`SELECT rel_name, type, size, alloc, mtime, uid FROM listing_results
		WHERE job_id = ? ORDER BY seq`, jobID)
	must(e.t, err)
	defer func() { _ = rs.Close() }()
	var out []row
	for rs.Next() {
		var r row
		var n []byte
		must(e.t, rs.Scan(&n, &r.Type, &r.Size, &r.Alloc, &r.Mtime, &r.UID))
		r.Name = string(n)
		out = append(out, r)
	}
	must(e.t, rs.Err())
	return out
}

func names(rs []row) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	slices.Sort(out)
	return out
}

func TestListing_Children(t *testing.T) {
	hostile := "\xff\xfe bad\nname"
	e := newEnv(t, func(root string) {
		mkdirs(t, root, "a/sub")
		must(t, os.WriteFile(filepath.Join(root, "a/f1"), make([]byte, 3000), 0o600)) // real blocks
		writeFile(t, root, "a/"+hostile, 10, day)
		must(t, os.Symlink("f1", filepath.Join(root, "a/ln")))
		must(t, unix.Mkfifo(filepath.Join(root, "a/pipe"), 0o600))
	})
	admin := e.user("admin", true, false, false)
	j := e.job(admin, e.node("a"), db.ListingParams{Mode: "children"})
	prog, err := Run(context.Background(), e.deps(), j, e.row)
	must(t, err)
	got := e.rows(j.ID)

	wantNames := []string{"f1", "ln", "pipe", "sub", hostile}
	slices.Sort(wantNames)
	if !slices.Equal(names(got), wantNames) {
		t.Fatalf("names = %q, want %q", names(got), wantNames)
	}
	wantType := map[string]string{"f1": "file", "ln": "symlink", "pipe": "other", "sub": "dir", hostile: "file"}
	for _, r := range got {
		fi, err := os.Lstat(filepath.Join(e.root, "a", r.Name))
		must(t, err)
		st := fi.Sys().(*syscall.Stat_t) //nolint:forcetypeassert // linux
		if r.Type != wantType[r.Name] {
			t.Errorf("%q: type = %q, want %q", r.Name, r.Type, wantType[r.Name])
		}
		if r.Size != st.Size || r.Alloc != st.Blocks*512 || r.Mtime != st.Mtim.Sec || r.UID != int64(st.Uid) {
			t.Errorf("%q: row %+v does not match lstat size=%d blocks=%d mtime=%d uid=%d",
				r.Name, r, st.Size, st.Blocks, st.Mtim.Sec, st.Uid)
		}
	}
	if r := byName(got, "f1"); r.Size != 3000 || r.Alloc == 0 {
		t.Errorf("f1: size %d alloc %d, want 3000 and a nonzero alloc", r.Size, r.Alloc)
	}
	if prog.Rows != 5 || prog.Truncated || prog.Errors != 0 || prog.MountsSkipped != 0 {
		t.Errorf("progress = %+v, want 5 rows and nothing else", prog)
	}
}

func byName(rs []row, name string) row {
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	return row{}
}

func TestListing_SubtreeFilters(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "secret.mkv", 5<<20, 90*day)
	e := newEnv(t, func(root string) {
		mkdirs(t, root, "a/b/deep", "a/c")
		writeFile(t, root, "a/x.mkv", 2<<20, 40*day)        // match
		writeFile(t, root, "a/b/y.MKV", 3<<20, 60*day)      // match, extension is case-folded
		writeFile(t, root, "a/b/deep/z.mkv", 4<<20, 31*day) // match, two levels down
		writeFile(t, root, "a/small.mkv", 1000, 90*day)     // too small
		writeFile(t, root, "a/new.mkv", 2<<20, 1*day)       // too young
		writeFile(t, root, "a/c/w.mp4", 2<<20, 90*day)      // wrong extension
		writeFile(t, root, "a/nodot", 2<<20, 90*day)        // no extension
		must(t, os.Symlink(outside, filepath.Join(root, "a/evil")))
	})
	admin := e.user("admin", true, false, false)
	j := e.job(admin, e.node("a"), db.ListingParams{
		Mode: "subtree", Exts: []string{"mkv"}, MinSize: 1 << 20, MinAgeDays: 30,
	})
	prog, err := Run(context.Background(), e.deps(), j, e.row)
	must(t, err)
	want := []string{"b/deep/z.mkv", "b/y.MKV", "x.mkv"}
	if got := names(e.rows(j.ID)); !slices.Equal(got, want) {
		t.Errorf("rel_names = %q, want %q", got, want)
	}
	if prog.Rows != 3 || prog.Truncated {
		t.Errorf("progress = %+v", prog)
	}
}

func TestListing_Truncates(t *testing.T) {
	e := newEnv(t, func(root string) {
		mkdirs(t, root, "five", "six")
		for i := range 5 {
			writeFile(t, root, fmt.Sprintf("five/f%d", i), 1, day)
		}
		for i := range 6 {
			writeFile(t, root, fmt.Sprintf("six/f%d", i), 1, day)
		}
	})
	admin := e.user("admin", true, false, false)
	for _, mode := range []string{"children", "subtree"} {
		d := e.deps()
		d.MaxRows = 5
		j5 := e.job(admin, e.node("five"), db.ListingParams{Mode: mode})
		p, err := Run(context.Background(), d, j5, e.row)
		must(t, err)
		if p.Rows != 5 || p.Truncated || len(e.rows(j5.ID)) != 5 {
			t.Errorf("%s, 5 entries: progress %+v, %d rows; want 5 rows, not truncated", mode, p, len(e.rows(j5.ID)))
		}
		j6 := e.job(admin, e.node("six"), db.ListingParams{Mode: mode})
		p, err = Run(context.Background(), d, j6, e.row)
		must(t, err)
		if p.Rows != 5 || !p.Truncated || len(e.rows(j6.ID)) != 5 {
			t.Errorf("%s, 6 entries: progress %+v, %d rows; want 5 rows, truncated", mode, p, len(e.rows(j6.ID)))
		}
	}
}

func TestListing_ReplacedBySymlink(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace string // the path below the root that becomes a symlink
	}{
		{"target", "a/b"},
		{"parent of the target", "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, func(root string) {
				mkdirs(t, root, "a/b")
				writeFile(t, root, "a/b/real", 1, day)
			})
			admin := e.user("admin", true, false, false)
			j := e.job(admin, e.node("a", "b"), db.ListingParams{Mode: "subtree"})
			outside := t.TempDir()
			writeFile(t, outside, "sentinel", 1, day)
			mkdirs(t, outside, "b")
			writeFile(t, outside, "b/sentinel", 1, day)
			victim := filepath.Join(e.root, tc.replace)
			must(t, os.Rename(victim, victim+".moved"))
			must(t, os.Symlink(outside, victim)) // for "a", a/b resolves to outside/b

			_, err := Run(context.Background(), e.deps(), j, e.row)
			if !errors.Is(err, ErrChanged) && !errors.Is(err, unix.ELOOP) {
				t.Fatalf("err = %v, want ErrChanged or ELOOP", err)
			}
			if rs := e.rows(j.ID); len(rs) != 0 {
				t.Errorf("%d rows written, want 0: %q", len(rs), names(rs))
			}
		})
	}
}

func TestListing_ReplacedDir(t *testing.T) {
	e := newEnv(t, func(root string) {
		mkdirs(t, root, "a/b")
		writeFile(t, root, "a/b/f", 1, day)
	})
	admin := e.user("admin", true, false, false)
	j := e.job(admin, e.node("a", "b"), db.ListingParams{Mode: "children"})
	// Create the replacement before removing the original so it cannot reuse
	// the inode number, then move it into place.
	repl := filepath.Join(e.root, "a", "b.new")
	must(t, os.Mkdir(repl, 0o750))
	must(t, os.Remove(filepath.Join(e.root, "a/b/f")))
	must(t, os.Remove(filepath.Join(e.root, "a/b")))
	must(t, os.Rename(repl, filepath.Join(e.root, "a/b")))
	_, err := Run(context.Background(), e.deps(), j, e.row)
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if rs := e.rows(j.ID); len(rs) != 0 {
		t.Errorf("%d rows written, want 0", len(rs))
	}
}

func TestListing_Gone(t *testing.T) {
	e := newEnv(t, func(root string) { mkdirs(t, root, "a") })
	admin := e.user("admin", true, false, false)
	p := db.ListingParams{Mode: "children"}

	unknown := e.job(admin, "1.99999", p)
	if _, err := Run(context.Background(), e.deps(), unknown, e.row); !errors.Is(err, ErrGone) {
		t.Errorf("unknown dir id: err = %v, want ErrGone", err)
	}
	other := e.job(admin, "7.2", p)
	if _, err := Run(context.Background(), e.deps(), other, e.row); !errors.Is(err, ErrGone) {
		t.Errorf("unknown scan: err = %v, want ErrGone", err)
	}
	bad := e.job(admin, "not-a-node", p)
	if _, err := Run(context.Background(), e.deps(), bad, e.row); !errors.Is(err, ErrGone) {
		t.Errorf("malformed node: err = %v, want ErrGone", err)
	}
	j := e.job(admin, e.node("a"), p)
	must(t, os.Remove(gen.Path(e.data, 1, 1)))
	if _, err := Run(context.Background(), e.deps(), j, e.row); !errors.Is(err, ErrGone) {
		t.Errorf("deleted generation: err = %v, want ErrGone", err)
	}
}

func TestListing_Anchor(t *testing.T) {
	e := newEnv(t, func(root string) {
		mkdirs(t, root, "a/b", "ab", "c")
		writeFile(t, root, "a/b/f", 1, day)
		writeFile(t, root, "ab/f", 1, day)
		writeFile(t, root, "c/f", 1, day)
	})
	p := db.ListingParams{Mode: "children"}
	user := e.user("u", false, true, false, "a")
	noFlag := e.user("noflag", false, false, false, "c")
	off := e.user("off", false, true, true, "ab")
	nopath := e.user("nopath", false, true, false)
	admin := e.user("admin", true, false, false)
	ghost := e.job(user, e.node("a"), p) // reused below with another requester
	ghost.RequestedBy = sql.NullInt64{}

	tests := []struct {
		name string
		user int64
		node []string
		want error
	}{
		{"assigned path itself", user, []string{"a"}, nil},
		{"below the assigned path", user, []string{"a", "b"}, nil},
		{"outside the assigned path", user, []string{"c"}, ErrNotPermitted},
		{"sibling sharing a name prefix", user, []string{"ab"}, ErrNotPermitted},
		{"the root", user, nil, ErrNotPermitted},
		{"no can_list_files", noFlag, []string{"c"}, ErrNotPermitted},
		{"disabled", off, []string{"ab"}, ErrNotPermitted},
		{"no path", nopath, []string{"a"}, ErrNotPermitted},
		{"admin outside any path", admin, []string{"c"}, nil},
		{"admin root", admin, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := e.job(tc.user, e.node(tc.node...), p)
			_, err := Run(context.Background(), e.deps(), j, e.row)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want != nil && len(e.rows(j.ID)) != 0 {
				t.Errorf("rows written for a refused job")
			}
			if tc.want == nil && len(e.rows(j.ID)) == 0 {
				t.Errorf("no rows for a permitted job")
			}
		})
	}
	if _, err := Run(context.Background(), e.deps(), ghost, e.row); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("no requester: err = %v, want ErrNotPermitted", err)
	}
}

func TestListing_CancelDeletesRows(t *testing.T) {
	e := newEnv(t, func(root string) {
		mkdirs(t, root, "a")
		for i := range 6 {
			writeFile(t, root, fmt.Sprintf("a/f%d", i), 1, day)
		}
	})
	admin := e.user("admin", true, false, false)
	j := e.job(admin, e.node("a"), db.ListingParams{Mode: "children"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var flushed int
	afterFlush = func() {
		flushed++
		if n := len(e.rows(j.ID)); n != 2 {
			t.Errorf("rows after the first batch = %d, want 2", n)
		}
		cancel()
	}
	t.Cleanup(func() { afterFlush = func() {} })
	d := e.deps()
	d.BatchRows = 2
	_, err := Run(ctx, d, j, e.row)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if flushed != 1 {
		t.Errorf("flushed %d batches, want 1", flushed)
	}
	if n := len(e.rows(j.ID)); n != 0 {
		t.Errorf("%d rows left after cancel, want 0", n)
	}
}

// countGate counts tokens and the most held at once.
type countGate struct {
	acquired, released atomic.Int64
	held, max          atomic.Int64
}

func (g *countGate) Acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.acquired.Add(1)
	n := g.held.Add(1)
	for {
		m := g.max.Load()
		if n <= m || g.max.CompareAndSwap(m, n) {
			return nil
		}
	}
}

func (g *countGate) Release() { g.released.Add(1); g.held.Add(-1) }

func TestListing_GatePerDirectory(t *testing.T) {
	e := newEnv(t, func(root string) {
		mkdirs(t, root, "a/b", "a/c")
		writeFile(t, root, "a/b/f", 1, day)
	})
	admin := e.user("admin", true, false, false)
	j := e.job(admin, e.node("a"), db.ListingParams{Mode: "subtree"})
	g := &countGate{}
	d := e.deps()
	d.Gate = g
	_, err := Run(context.Background(), d, j, e.row)
	must(t, err)
	if g.acquired.Load() != 3 || g.released.Load() != 3 || g.max.Load() != 1 {
		t.Errorf("gate: acquired %d, released %d, max held %d; want 3, 3, 1",
			g.acquired.Load(), g.released.Load(), g.max.Load())
	}
}

func TestListing_HiddenDirs(t *testing.T) {
	e := newEnv(t, func(root string) {
		mkdirs(t, root, "a/ex/deep", "a/ok")
		writeFile(t, root, "a/top", 1, day)
		writeFile(t, root, "a/ex/hidden1", 1, day)
		writeFile(t, root, "a/ex/deep/hidden2", 1, day)
		writeFile(t, root, "a/ok/shown", 1, day)
	}, func(o *build.Options) { o.Walk.Excludes = []string{"a/ex"} })
	admin := e.user("admin", true, false, false)
	user := e.user("u", false, true, false, "a")
	p := db.ListingParams{Mode: "subtree"}

	j := e.job(admin, e.node("a"), p)
	prog, err := Run(context.Background(), e.deps(), j, e.row)
	must(t, err)
	if got, want := names(e.rows(j.ID)), []string{"ok/shown", "top"}; !slices.Equal(got, want) {
		t.Errorf("rel_names = %q, want %q", got, want)
	}
	if prog.MountsSkipped != 1 {
		t.Errorf("MountsSkipped = %d, want 1 (a/ex)", prog.MountsSkipped)
	}

	// The same through an anchor, from the anchor's own subtree.
	j = e.job(user, e.node("a"), p)
	_, err = Run(context.Background(), e.deps(), j, e.row)
	must(t, err)
	if got := names(e.rows(j.ID)); slices.Contains(got, "ex/hidden1") || slices.Contains(got, "ex/deep/hidden2") {
		t.Errorf("excluded files listed: %q", got)
	}

	// An excluded directory, or one below it that has a row, is not a target.
	for _, mode := range []string{"children", "subtree"} {
		j = e.job(admin, e.node("a", "ex"), db.ListingParams{Mode: mode})
		if _, err := Run(context.Background(), e.deps(), j, e.row); !errors.Is(err, ErrGone) {
			t.Errorf("excluded target, %s: err = %v, want ErrGone", mode, err)
		}
	}
}

func TestListing_NestedRootTarget(t *testing.T) {
	// Build a second scan where a/nest is another root's top directory.
	var dev, ino uint64
	e := newEnv(t, func(root string) {
		mkdirs(t, root, "a/nest")
		writeFile(t, root, "a/nest/f", 1, day)
		var st unix.Stat_t
		must(t, unix.Stat(filepath.Join(root, "a/nest"), &st))
		dev, ino = uint64(st.Dev), uint64(st.Ino) //nolint:unconvert,gosec // width differs by arch
	})
	// Rescan as generation 2 with the nested root registered.
	_, err := build.Run(context.Background(), build.Options{
		DataDir: e.data, RootID: 1, ScanID: 2, Root: e.root,
		Walk: walk.Options{Workers: 2, AllowWritable: true, Limits: agg.DefaultLimits(),
			NestedRoots: []walk.DevIno{{Dev: dev, Ino: ino}}},
		ScannerVersion: "test", Now: func() time.Time { return base },
	})
	must(t, err)
	gdb, _, err := gen.OpenReadOnly(context.Background(), gen.Path(e.data, 1, 2))
	must(t, err)
	defer func() { _ = gdb.Close() }()
	nest, err := gen.Lookup(context.Background(), gdb, [][]byte{[]byte("a"), []byte("nest")})
	must(t, err)
	id := nest.ID
	admin := e.user("admin", true, false, false)
	j := e.job(admin, fmt.Sprintf("2.%d", id), db.ListingParams{Mode: "children"})
	if _, err := Run(context.Background(), e.deps(), j, e.row); !errors.Is(err, ErrGone) {
		t.Fatalf("nested-root target: err = %v, want ErrGone", err)
	}
	a2, err := gen.Lookup(context.Background(), gdb, [][]byte{[]byte("a")})
	must(t, err)
	j = e.job(admin, fmt.Sprintf("2.%d", a2.ID), db.ListingParams{Mode: "subtree"})
	prog, err := Run(context.Background(), e.deps(), j, e.row)
	must(t, err)
	if prog.MountsSkipped != 1 || len(e.rows(j.ID)) != 0 {
		t.Errorf("subtree over a nested root: progress %+v, rows %q", prog, names(e.rows(j.ID)))
	}
}

func TestListing_IOErrorHasNoPath(t *testing.T) {
	e := newEnv(t, func(root string) { mkdirs(t, root, "a") })
	admin := e.user("admin", true, false, false)
	j := e.job(admin, e.node("a"), db.ListingParams{Mode: "children"})
	row := e.row
	row.ContainerPath = filepath.Join(e.root, "gone-secret-dir")
	_, err := Run(context.Background(), e.deps(), j, row)
	if !errors.Is(err, ErrIO) {
		t.Fatalf("err = %v, want ErrIO", err)
	}
	if err.Error() != "listing failed" || strings.Contains(err.Error(), "gone-secret-dir") {
		t.Errorf("error text %q must be fixed and name no path", err.Error())
	}
}

// A target on another filesystem than its root (reachable only with cross_fs)
// is not listed: the open refuses to cross the mount (RESOLVE_NO_XDEV, EXDEV)
// and the job fails with the fixed "target is on another filesystem" message.
// A real mount needs privileges, so this pins the mapping of the open error.
func TestListing_TargetOnAnotherFilesystem(t *testing.T) {
	err := openErr(context.Background(), fmt.Errorf("openat2: %w", unix.EXDEV))
	if !errors.Is(err, ErrCrossesMount) {
		t.Fatalf("err = %v, want ErrCrossesMount", err)
	}
	if err.Error() != "target is on another filesystem" {
		t.Errorf("error text = %q", err.Error())
	}
}
