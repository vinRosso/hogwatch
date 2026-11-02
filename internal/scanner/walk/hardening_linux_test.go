package walk

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
	"golang.org/x/sys/unix"
)

// countGate is a Gate that never blocks; it counts acquires and the most
// tokens held at once.
type countGate struct {
	acquires, held, maxHeld atomic.Int64
}

func (g *countGate) Acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.acquires.Add(1)
	n := g.held.Add(1)
	for m := g.maxHeld.Load(); n > m && !g.maxHeld.CompareAndSwap(m, n); m = g.maxHeld.Load() {
	}
	return nil
}

func (g *countGate) Release() { g.held.Add(-1) }

func TestScan_GateHeld(t *testing.T) {
	tree := makeTree(t, 3)
	for _, inline := range []bool{false, true} {
		for _, workers := range []int{1, 4} {
			t.Run(fmt.Sprintf("inline=%v/workers=%d", inline, workers), func(t *testing.T) {
				var cg countGate
				opts := testOpts(workers)
				opts.Gate = &cg
				opts.forceInline = inline
				res, err := Scan(context.Background(), tree.root, opts, newRec())
				if err != nil {
					t.Fatal(err)
				}
				if got := cg.acquires.Load(); got != res.Stats.Dirs {
					t.Fatalf("acquires = %d, want one per directory (%d)", got, res.Stats.Dirs)
				}
				if m := cg.maxHeld.Load(); m > int64(workers) {
					t.Fatalf("max tokens held at once = %d, want <= Workers (%d)", m, workers)
				}
				if h := cg.held.Load(); h != 0 {
					t.Fatalf("%d tokens still held after Scan", h)
				}
			})
		}
	}
}

// failGate refuses every acquire after the first n.
type failGate struct {
	left, held atomic.Int64
}

func (g *failGate) Acquire(context.Context) error {
	if g.left.Add(-1) < 0 {
		return context.Canceled
	}
	g.held.Add(1)
	return nil
}

func (g *failGate) Release() { g.held.Add(-1) }

// A failed Acquire leaves the directory unlisted, closes its descriptor and
// holds no token.
func TestScan_GateAcquireFails(t *testing.T) {
	root := makeDirs(t, 200)
	before, g := openFDs(t), runtime.NumGoroutine()
	fg := &failGate{}
	fg.left.Store(5)
	opts := testOpts(2)
	opts.Gate = fg
	res, _ := Scan(context.Background(), root, opts, newRec())
	if res.Stats.Dirs > 5 {
		t.Fatalf("Dirs = %d, want <= 5 (the tokens granted)", res.Stats.Dirs)
	}
	if h := fg.held.Load(); h != 0 {
		t.Fatalf("%d tokens still held", h)
	}
	if after := openFDs(t); after != before {
		t.Fatalf("open fds: %d before, %d after", before, after)
	}
	settle(t, g)
}

func TestScan_Excludes(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a/b/sub", "a/c", "d"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "a/b/f1"), 1000)
	writeFile(t, filepath.Join(root, "a/b/sub/f2"), 2000)
	writeFile(t, filepath.Join(root, "a/c/f3"), 3000)
	writeFile(t, filepath.Join(root, "d/f4"), 4000)
	for _, workers := range []int{1, 4} {
		opts := testOpts(workers)
		opts.Excludes = []string{"a/b"}
		rec := newRec()
		res, err := Scan(context.Background(), root, opts, rec)
		if err != nil {
			t.Fatal(err)
		}
		rec.checkIDs(t)
		rows := rec.byPath(t)
		b, ok := rows["a/b"]
		if !ok {
			t.Fatalf("no row for the excluded directory; rows: %v", slices.Collect(maps.Keys(rows)))
		}
		if b.Flags&gen.FlagExcluded == 0 {
			t.Errorf("flags = %#x, want FlagExcluded", b.Flags)
		}
		if b.Files != 0 || b.OwnFiles != 0 || b.BytesAlloc != 0 || b.Dirs != 0 {
			t.Errorf("excluded row has counts: %+v", b)
		}
		if _, ok := rows["a/b/sub"]; ok {
			t.Errorf("row for a/b/sub below an excluded directory")
		}
		if res.Stats.Excluded != 1 {
			t.Errorf("Excluded = %d, want 1", res.Stats.Excluded)
		}
		if res.Stats.Files != 2 {
			t.Errorf("Files = %d, want 2 (a/c/f3 and d/f4 only)", res.Stats.Files)
		}
		if res.Stats.Dirs != 4 { // root, a, a/c, d
			t.Errorf("Dirs = %d, want 4 opened", res.Stats.Dirs)
		}
		if a := rows["a"]; a.Files != 1 || a.Dirs != 2 {
			t.Errorf("a: files = %d, dirs = %d, want 1 and 2 (b counts as a skipped dir)", a.Files, a.Dirs)
		}
	}
}

func TestScan_ExcludesRejectsBadPaths(t *testing.T) {
	for _, ex := range []string{"", "/", "../x", "a//b", "a/./b"} {
		opts := testOpts(1)
		opts.Excludes = []string{ex}
		if _, err := Scan(context.Background(), t.TempDir(), opts, newRec()); err == nil {
			t.Errorf("Excludes %q: want an error", ex)
		}
	}
}

// multiSink hands every row to both sinks.
type multiSink struct{ a, b Sink }

func (m multiSink) AddDir(ctx context.Context, d gen.Dir) error {
	return errors.Join(m.a.AddDir(ctx, d), m.b.AddDir(ctx, d))
}

func (m multiSink) AddCube(ctx context.Context, id int64, bl cube.Blob) error {
	return errors.Join(m.a.AddCube(ctx, id, bl), m.b.AddCube(ctx, id, bl))
}

func (m multiSink) AddTopFile(ctx context.Context, f gen.TopFile) error {
	return errors.Join(m.a.AddTopFile(ctx, f), m.b.AddTopFile(ctx, f))
}

func (m multiSink) AddTopExt(ctx context.Context, e gen.TopExt) error {
	return errors.Join(m.a.AddTopExt(ctx, e), m.b.AddTopExt(ctx, e))
}

func (m multiSink) AddSharedInode(ctx context.Context, s gen.SharedInode) error {
	return errors.Join(m.a.AddSharedInode(ctx, s), m.b.AddSharedInode(ctx, s))
}

func (m multiSink) AddSharedLink(ctx context.Context, l gen.SharedLink) error {
	return errors.Join(m.a.AddSharedLink(ctx, l), m.b.AddSharedLink(ctx, l))
}

func (m multiSink) AddError(ctx context.Context, e gen.ErrorRow) error {
	return errors.Join(m.a.AddError(ctx, e), m.b.AddError(ctx, e))
}

func TestScan_DuplicateSubdirName(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "x", "f"), 100)
	writeFile(t, filepath.Join(root, "dup"), 50)
	ctx := context.Background()
	genPath := gen.Path(t.TempDir(), 1, 1)
	w, err := gen.Create(ctx, genPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Abort() }()
	opts := testOpts(2)
	// getdents racing a rename can return the same name twice.
	opts.readNames = func(d *fsx.Dir, fn func(string) error) error {
		return d.ReadNames(func(name string) error {
			if err := fn(name); err != nil {
				return err
			}
			if name == "x" || name == "dup" {
				return fn(name)
			}
			return nil
		})
	}
	rec := newRec()
	res, err := Scan(ctx, root, opts, multiSink{w, rec})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.DupNames != 1 {
		t.Errorf("DupNames = %d, want 1", res.Stats.DupNames)
	}
	if n := len(rec.dirs); n != 2 {
		t.Errorf("dir rows = %d, want 2 (root and x)", n)
	}
	// A duplicate file name is still counted twice: a known race.
	if res.Stats.Files != 3 { // dup twice, x/f once
		t.Errorf("Files = %d, want 3", res.Stats.Files)
	}
	if err := w.BuildIndexes(ctx); err != nil {
		t.Fatalf("BuildIndexes: %v (the unique (parent_id, name) index must hold)", err)
	}
}

func TestScan_MaxExtKeys(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.txt", "b.go", "c.png", "d.mp4"} {
		writeFile(t, filepath.Join(root, n), 10)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "sub", "e.txt"), 10)
	res, err := Scan(context.Background(), root, testOpts(2), newRec())
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.MaxExtKeys != 4 {
		t.Fatalf("MaxExtKeys = %d, want 4", res.Stats.MaxExtKeys)
	}
}

// makeSiblingChain builds depth levels, each holding the next level "d" and a
// leaf sibling "s", descriptor relative.
func makeSiblingChain(t *testing.T, root string, depth int) {
	t.Helper()
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for range depth {
		for _, name := range []string{"s", "d"} {
			if err := unix.Mkdirat(fd, name, 0o750); err != nil {
				_ = unix.Close(fd)
				t.Fatal(err)
			}
		}
		next, err := unix.Openat(fd, "d", unix.O_RDONLY|unix.O_DIRECTORY, 0)
		_ = unix.Close(fd)
		if err != nil {
			t.Fatal(err)
		}
		fd = next
	}
	_ = unix.Close(fd)
}

// Inline walking holds the parent's descriptor while a non-last child is
// walked, one fd per level: bounded by depth, not by width. This pins that M3
// watch item.
func TestScan_SiblingChainFDs(t *testing.T) {
	const depth = 500
	root := t.TempDir()
	makeSiblingChain(t, root, depth)
	for _, workers := range []int{1, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			before := openFDs(t)
			var peak atomic.Int64
			opts := testOpts(workers)
			opts.forceInline = true
			opts.onDir = func() {
				n := int64(openFDs(t))
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
			}
			rec := newRec()
			res, err := Scan(context.Background(), root, opts, rec)
			if err != nil {
				t.Fatal(err)
			}
			if want := int64(2*depth + 1); res.Stats.Dirs != want || int64(len(rec.dirs)) != want {
				t.Fatalf("Dirs = %d, rows = %d, want %d", res.Stats.Dirs, len(rec.dirs), want)
			}
			if limit := int64(before + depth + workers + 4); peak.Load() > limit {
				t.Fatalf("peak open fds %d, want <= %d (baseline %d + depth %d + workers %d + 4)", peak.Load(), limit, before, depth, workers)
			}
			if after := openFDs(t); after != before {
				t.Fatalf("open fds: %d before, %d after", before, after)
			}
		})
	}
}

// "/" has no parent: ".." is "/" itself, on the same device. Such a root is a
// filesystem root, as is a bind-mounted one (statx mount-root attribute).
func TestRootWhere_SlashIsFSRoot(t *testing.T) {
	st := fsx.Stat{Dev: 7, Ino: 2}
	cases := []struct {
		name string
		up   fsx.Stat
		err  error
		st   fsx.Stat
		want bool
	}{
		{"dotdot is the root itself", fsx.Stat{Dev: 7, Ino: 2}, nil, st, true},
		{"other device", fsx.Stat{Dev: 8, Ino: 99}, nil, st, true},
		{"mount root attribute", fsx.Stat{Dev: 7, Ino: 99}, nil, fsx.Stat{Dev: 7, Ino: 2, MountRoot: true}, true},
		{"unexaminable parent", fsx.Stat{}, unix.EACCES, st, true},
		{"ordinary directory", fsx.Stat{Dev: 7, Ino: 99}, nil, st, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyRoot(c.st, c.up, c.err, 0x1234); got.fsRoot != c.want {
				t.Fatalf("fsRoot = %v, want %v", got.fsRoot, c.want)
			}
		})
	}
	// The real thing.
	d, err := fsx.OpenRoot("/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	rst, err := d.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if got := rootWhere(d, rst, 0); !got.fsRoot {
		t.Fatal("rootWhere on \"/\" has fsRoot = false, want true")
	}
}
