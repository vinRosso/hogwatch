package walk

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"golang.org/x/sys/unix"
)

// recSink records every row. Scan calls it from one goroutine, and the tests
// read it after Scan returned, so it needs no locking.
type recSink struct {
	dirs   []gen.Dir
	cubes  map[int64]cube.Blob
	tops   []gen.TopFile
	exts   []gen.TopExt
	inodes map[[2]int64]gen.SharedInode // deduplicated like the generation's INSERT OR IGNORE
	links  []gen.SharedLink
	errs   []gen.ErrorRow

	onDir func(n int) error // called after each AddDir with the count so far
}

func newRec() *recSink {
	return &recSink{cubes: map[int64]cube.Blob{}, inodes: map[[2]int64]gen.SharedInode{}}
}

func (r *recSink) AddDir(_ context.Context, d gen.Dir) error {
	r.dirs = append(r.dirs, d)
	if r.onDir != nil {
		return r.onDir(len(r.dirs))
	}
	return nil
}

func (r *recSink) AddCube(_ context.Context, id int64, b cube.Blob) error {
	if _, dup := r.cubes[id]; dup {
		return fmt.Errorf("two cube rows for dir %d", id)
	}
	r.cubes[id] = slices.Clone(b)
	return nil
}

func (r *recSink) AddTopFile(_ context.Context, f gen.TopFile) error {
	r.tops = append(r.tops, f)
	return nil
}

func (r *recSink) AddTopExt(_ context.Context, e gen.TopExt) error {
	r.exts = append(r.exts, e)
	return nil
}

func (r *recSink) AddSharedInode(_ context.Context, s gen.SharedInode) error {
	k := [2]int64{s.Dev, s.Ino}
	if _, ok := r.inodes[k]; !ok {
		r.inodes[k] = s
	}
	return nil
}

func (r *recSink) AddSharedLink(_ context.Context, l gen.SharedLink) error {
	r.links = append(r.links, l)
	return nil
}

func (r *recSink) AddError(_ context.Context, e gen.ErrorRow) error {
	r.errs = append(r.errs, e)
	return nil
}

// paths maps every dir id to its path below the root ("" for the root).
func (r *recSink) paths(t *testing.T) map[int64]string {
	t.Helper()
	byID := make(map[int64]gen.Dir, len(r.dirs))
	for _, d := range r.dirs {
		byID[d.ID] = d
	}
	out := make(map[int64]string, len(r.dirs))
	var resolve func(id int64) string
	resolve = func(id int64) string {
		if p, ok := out[id]; ok {
			return p
		}
		d, ok := byID[id]
		if !ok {
			t.Fatalf("dir %d has no row", id)
		}
		p := ""
		if d.ParentID != 0 {
			p = resolve(d.ParentID)
			if p != "" {
				p += "/"
			}
			p += string(d.Name)
		}
		out[id] = p
		return p
	}
	for _, d := range r.dirs {
		resolve(d.ID)
	}
	return out
}

// byPath returns the dir rows keyed by path.
func (r *recSink) byPath(t *testing.T) map[string]gen.Dir {
	t.Helper()
	paths := r.paths(t)
	out := make(map[string]gen.Dir, len(r.dirs))
	for _, d := range r.dirs {
		out[paths[d.ID]] = d
	}
	return out
}

// checkIDs asserts the id rules: one row per id, dense 1..N, parent_id < id,
// the root alone has parent 0, and every row has a cube blob.
func (r *recSink) checkIDs(t *testing.T) {
	t.Helper()
	n := int64(len(r.dirs))
	seen := make(map[int64]bool, n)
	for _, d := range r.dirs {
		if d.ID < 1 || d.ID > n || seen[d.ID] {
			t.Fatalf("dir id %d is out of 1..%d or repeated", d.ID, n)
		}
		seen[d.ID] = true
		if d.ID == 1 {
			if d.ParentID != 0 || len(d.Name) != 0 || d.DepthAnchor != 0 {
				t.Fatalf("root row = %+v, want parent 0, empty name, depth_anchor 0", d)
			}
		} else if d.ParentID < 1 || d.ParentID >= d.ID {
			t.Fatalf("dir %d has parent_id %d, want 1 <= parent_id < id", d.ID, d.ParentID)
		}
		if _, ok := r.cubes[d.ID]; !ok {
			t.Fatalf("dir %d has no cube row", d.ID)
		}
	}
	if len(r.cubes) != len(r.dirs) {
		t.Fatalf("%d cube rows for %d dirs", len(r.cubes), len(r.dirs))
	}
	// error_count is the subtree total: it must equal the error rows keyed to the
	// directory or any directory below it.
	parent := make(map[int64]int64, n)
	for _, d := range r.dirs {
		parent[d.ID] = d.ParentID
	}
	keyed := make(map[int64]int64, n)
	for _, e := range r.errs {
		for id := e.DirID; id != 0; id = parent[id] {
			keyed[id]++
		}
	}
	for _, d := range r.dirs {
		if d.ErrorCount != keyed[d.ID] {
			t.Fatalf("dir %d: error_count = %d, but %d error rows are keyed to its subtree", d.ID, d.ErrorCount, keyed[d.ID])
		}
	}
}

// testOpts are the options every test starts from. A temp dir is writable.
func testOpts(workers int) Options {
	return Options{Workers: workers, AllowWritable: true, ScanStart: time.Unix(1_800_000_000, 0)}
}

// settle waits for goroutines started by Scan to be gone.
func settle(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("%d goroutines, want <= %d:\n%s", runtime.NumGoroutine(), base, buf)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestScan_Totals(t *testing.T) {
	tree := makeTree(t, 1)
	want := oracle(t, tree.root)
	for _, workers := range []int{1, 2, 8} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			before := openFDs(t)
			rec := newRec()
			res, err := Scan(context.Background(), tree.root, testOpts(workers), rec)
			if err != nil {
				t.Fatal(err)
			}
			rec.checkIDs(t)
			root := rec.byPath(t)[""]
			if root.Files != want.Files || root.BytesLogical != want.BytesLogical ||
				root.BytesAlloc != want.BytesAlloc || root.Dirs != want.Dirs-1 {
				t.Fatalf("root row = files %d logical %d alloc %d dirs %d\nwant files %d logical %d alloc %d dirs %d",
					root.Files, root.BytesLogical, root.BytesAlloc, root.Dirs,
					want.Files, want.BytesLogical, want.BytesAlloc, want.Dirs-1)
			}
			s := res.Stats
			if s.Dirs != want.Dirs || s.Files != want.Files || s.Symlinks != want.Symlinks ||
				s.BytesLogical != want.BytesLogical || s.BytesAlloc != want.BytesAlloc {
				t.Fatalf("stats = %+v, want %+v", s, want)
			}
			if res.DirRows != want.Dirs || int64(len(rec.dirs)) != want.Dirs {
				t.Fatalf("DirRows = %d, rows = %d, want %d", res.DirRows, len(rec.dirs), want.Dirs)
			}
			if s.Errnos != nil || s.Errors != 0 || len(rec.errs) != 0 {
				t.Fatalf("errors: %d %v %v, want none", s.Errors, s.Errnos, rec.errs)
			}
			if res.FSType == 0 || res.RootIno == 0 {
				t.Errorf("FSType = %#x, RootIno = %d, want both set", res.FSType, res.RootIno)
			}
			if after := openFDs(t); after != before {
				t.Fatalf("open fds: %d before, %d after", before, after)
			}
		})
	}
}

func TestScan_RowsPerDir(t *testing.T) {
	tree := makeTree(t, 5)
	rec := newRec()
	if _, err := Scan(context.Background(), tree.root, testOpts(4), rec); err != nil {
		t.Fatal(err)
	}
	rec.checkIDs(t)
	rows := rec.byPath(t)
	children := map[int64][]gen.Dir{}
	for _, d := range rec.dirs {
		children[d.ParentID] = append(children[d.ParentID], d)
	}
	for p, d := range rows {
		ents, err := os.ReadDir(filepath.Join(tree.root, p))
		if err != nil {
			t.Fatal(err)
		}
		var ownFiles, ownAlloc int64
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				t.Fatal(err)
			}
			ownFiles++
			ownAlloc += blocksOf(fi) * 512
		}
		if d.OwnFiles != ownFiles || d.OwnAlloc != ownAlloc {
			t.Errorf("%q: own files/alloc = %d/%d, want %d/%d", p, d.OwnFiles, d.OwnAlloc, ownFiles, ownAlloc)
		}
		files, dirs := d.OwnFiles, int64(0)
		for _, c := range children[d.ID] {
			files += c.Files
			dirs += c.Dirs + 1
		}
		if d.Files != files || d.Dirs != dirs {
			t.Errorf("%q: files/dirs = %d/%d, want %d/%d from its children", p, d.Files, d.Dirs, files, dirs)
		}
		acc, err := cube.Decode(rec.cubes[d.ID])
		if err != nil {
			t.Fatal(err)
		}
		var cf, cb int64
		for _, c := range acc {
			cf += c.Files
			cb += c.Bytes
		}
		if cf != d.Files || cb != d.BytesAlloc {
			t.Errorf("%q: cube files/bytes = %d/%d, row has %d/%d", p, cf, cb, d.Files, d.BytesAlloc)
		}
	}
}

func TestScan_HostileNames(t *testing.T) {
	names := []string{"a\nb", "\xff\xfe", "\xe2\x80\xaeevil", "=cmd", "+1", "-2", "@x", "\x01\x1b[31m", strings.Repeat("x", 255)}
	root := t.TempDir()
	for _, sub := range []string{"D", "F", "H"} {
		if err := os.Mkdir(filepath.Join(root, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for i, n := range names {
		d := filepath.Join(root, "D", n)
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(d, "in"), 100)
		f := filepath.Join(root, "F", n)
		writeFile(t, f, 1000+i) // distinct sizes, so every name is in a top list
		if err := os.Link(f, filepath.Join(root, "H", n)); err != nil {
			t.Fatal(err)
		}
	}
	opts := testOpts(4)
	opts.Limits = agg.Limits{TopN: 50, TopMaxDepth: 5, TopMinAlloc: 0, TopExts: 5}
	rec := newRec()
	if _, err := Scan(context.Background(), root, opts, rec); err != nil {
		t.Fatal(err)
	}
	rec.checkIDs(t)
	paths := rec.paths(t)
	idOf := map[string]int64{}
	for id, p := range paths {
		idOf[p] = id
	}

	disk := func(sub string) []string {
		ents, err := os.ReadDir(filepath.Join(root, sub))
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(ents))
		for _, e := range ents {
			out = append(out, e.Name())
		}
		sort.Strings(out)
		return out
	}
	var gotDirs []string
	for _, d := range rec.dirs {
		if d.ParentID == idOf["D"] {
			gotDirs = append(gotDirs, string(d.Name))
		}
	}
	sort.Strings(gotDirs)
	if !reflect.DeepEqual(gotDirs, disk("D")) {
		t.Errorf("dir names differ from disk:\n got %q\nwant %q", gotDirs, disk("D"))
	}

	var gotTop, gotLinks []string
	for _, f := range rec.tops {
		if f.AnchorID == 1 && f.Kind == "largest" && f.ParentDirID == idOf["F"] {
			gotTop = append(gotTop, string(f.Name))
		}
	}
	for _, l := range rec.links {
		if l.DirID == idOf["H"] {
			gotLinks = append(gotLinks, string(l.Name))
		}
	}
	sort.Strings(gotTop)
	sort.Strings(gotLinks)
	if !reflect.DeepEqual(gotTop, disk("F")) {
		t.Errorf("top file names differ from disk:\n got %q\nwant %q", gotTop, disk("F"))
	}
	if !reflect.DeepEqual(gotLinks, disk("H")) {
		t.Errorf("shared link names differ from disk:\n got %q\nwant %q", gotLinks, disk("H"))
	}
}

func TestScan_Symlinks(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(root, p), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	mk("real")
	mk("plain")
	for i := range 3 {
		writeFile(t, filepath.Join(root, "real", fmt.Sprintf("f%d", i)), 100)
	}
	writeFile(t, filepath.Join(root, "plain", "p"), 100)
	for name, target := range map[string]string{"lnk": "real", "loop1": "loop2", "loop2": "loop1"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	rec := newRec()
	res, err := Scan(context.Background(), root, testOpts(2), rec)
	if err != nil {
		t.Fatal(err)
	}
	rec.checkIDs(t)
	if len(rec.dirs) != 3 || res.Stats.Dirs != 3 || res.Stats.Errors != 0 {
		t.Fatalf("rows = %d, Dirs = %d, Errors = %d; want 3, 3, 0 (a symlink must not be entered)",
			len(rec.dirs), res.Stats.Dirs, res.Stats.Errors)
	}
	want := oracle(t, root)
	rows := rec.byPath(t)
	if rows[""].Files != want.Files || rows[""].BytesLogical != want.BytesLogical || res.Stats.Symlinks != 3 {
		t.Fatalf("root = %+v, symlinks %d; want files %d logical %d symlinks 3",
			rows[""], res.Stats.Symlinks, want.Files, want.BytesLogical)
	}
	if rows[""].Flags&gen.FlagSymlink == 0 {
		t.Error("root lacks FlagSymlink")
	}
	for _, p := range []string{"real", "plain"} {
		if rows[p].Flags&gen.FlagSymlink != 0 {
			t.Errorf("%s has FlagSymlink, it holds no symlink", p)
		}
	}
}

func TestScan_Unreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable directories")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "a", "locked")
	if err := os.MkdirAll(locked, 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(locked, "x"), 10)
	writeFile(t, filepath.Join(root, "a", "ok"), 10)
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // a directory needs its x bit back for cleanup

	rec := newRec()
	res, err := Scan(context.Background(), root, testOpts(2), rec)
	if err != nil {
		t.Fatal(err)
	}
	rec.checkIDs(t)
	rows := rec.byPath(t)
	l := rows["a/locked"]
	if l.Flags&gen.FlagUnreadable == 0 || l.ErrorCount != 1 || l.Files != 0 {
		t.Errorf("locked row = %+v, want FlagUnreadable, error_count 1, no files", l)
	}
	for _, p := range []string{"", "a"} {
		if rows[p].ErrorCount != 1 || rows[p].Flags&gen.FlagUnreadable != 0 {
			t.Errorf("%q: error_count = %d, flags = %#x; want 1 and no FlagUnreadable", p, rows[p].ErrorCount, rows[p].Flags)
		}
	}
	if rows[""].Dirs != 2 || rows[""].Files != 1 {
		t.Errorf("root dirs/files = %d/%d, want 2/1", rows[""].Dirs, rows[""].Files)
	}
	if len(rec.errs) != 1 {
		t.Fatalf("error rows = %+v, want one", rec.errs)
	}
	if e := rec.errs[0]; e.Op != "open" || e.Errno != int64(unix.EACCES) || len(e.Name) != 0 || e.DirID != l.ID {
		t.Errorf("error row = %+v, want op open, EACCES, no name, keyed to the locked dir %d", e, l.ID)
	}
	if res.Stats.Errors != 1 || res.Stats.Errnos["EACCES"] != 1 || res.Stats.Dirs != 2 {
		t.Errorf("stats = %+v, want 1 error EACCES and 2 dirs opened", res.Stats)
	}
}

func TestScan_Anchors(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a/b/c", "a/x", "z"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, d, "f"), 100)
	}
	run := func(lim agg.Limits) (map[string]gen.Dir, *recSink) {
		opts := testOpts(2)
		opts.Anchors = []string{"a/b"}
		opts.Limits = lim
		rec := newRec()
		if _, err := Scan(context.Background(), root, opts, rec); err != nil {
			t.Fatal(err)
		}
		rec.checkIDs(t)
		return rec.byPath(t), rec
	}

	rows, rec := run(agg.Limits{TopN: 5, TopMaxDepth: 1, TopMinAlloc: 0, TopExts: 5})
	for p, depth := range map[string]int64{"": 0, "a": 1, "a/b": 0, "a/b/c": 1, "a/x": 2, "z": 1} {
		if rows[p].DepthAnchor != depth {
			t.Errorf("%q: depth_anchor = %d, want %d", p, rows[p].DepthAnchor, depth)
		}
	}
	gotAnchors := map[int64]bool{}
	for _, f := range rec.tops {
		gotAnchors[f.AnchorID] = true
	}
	for _, e := range rec.exts {
		gotAnchors[e.DirID] = true
	}
	wantAnchors := map[int64]bool{}
	for _, p := range []string{"", "a", "a/b", "a/b/c", "z"} { // depth <= 1
		wantAnchors[rows[p].ID] = true
	}
	if !reflect.DeepEqual(gotAnchors, wantAnchors) {
		t.Errorf("top rows for dirs %v, want %v (a/x is at depth 2)", gotAnchors, wantAnchors)
	}
	// The root's lists cover its whole subtree: 3 files, ranks 1..3.
	var ranks []int64
	for _, f := range rec.tops {
		if f.AnchorID == 1 && f.Kind == "largest" {
			ranks = append(ranks, f.Rank)
		}
	}
	slices.Sort(ranks)
	if !reflect.DeepEqual(ranks, []int64{1, 2, 3}) {
		t.Errorf("root largest ranks = %v, want 1..3", ranks)
	}

	_, rec = run(agg.Limits{TopN: 5, TopMaxDepth: 5, TopMinAlloc: 1 << 40, TopExts: 5})
	if len(rec.tops) != 0 || len(rec.exts) != 0 {
		t.Errorf("top rows %d/%d with an unreachable TopMinAlloc, want none", len(rec.tops), len(rec.exts))
	}
}

func TestScan_AnchorsRejectsBadPaths(t *testing.T) {
	for _, a := range []string{"/a", "a/", "a/../b", "./a"} {
		opts := testOpts(1)
		opts.Anchors = []string{a}
		if _, err := Scan(context.Background(), t.TempDir(), opts, Discard); err == nil {
			t.Errorf("anchor %q accepted", a)
		}
	}
}

func TestScan_HardlinkRecords(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"d1", "d2", "d3"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "d1", "f"), 5000)
	if err := os.Link(filepath.Join(root, "d1", "f"), filepath.Join(root, "d2", "f")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "d3", "plain"), 100)

	opts := testOpts(2)
	rec := newRec()
	res, err := Scan(context.Background(), root, opts, rec)
	if err != nil {
		t.Fatal(err)
	}
	rows := rec.byPath(t)
	if len(rec.inodes) != 1 || len(rec.links) != 2 {
		t.Fatalf("shared inodes/links = %d/%d, want 1/2", len(rec.inodes), len(rec.links))
	}
	var linkDirs []int64
	for _, l := range rec.links {
		if string(l.Name) != "f" {
			t.Errorf("link name %q, want f", l.Name)
		}
		linkDirs = append(linkDirs, l.DirID)
	}
	slices.Sort(linkDirs)
	want := []int64{rows["d1"].ID, rows["d2"].ID}
	slices.Sort(want)
	if !slices.Equal(linkDirs, want) {
		t.Errorf("link dirs = %v, want %v", linkDirs, want)
	}
	fi, err := os.Lstat(filepath.Join(root, "d1", "f"))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range rec.inodes {
		cell := agg.CellFor([]byte("f"), fi.Size(), fi.ModTime().Unix(), opts.ScanStart, cube.DefaultCategories())
		if in.Size != 5000 || in.Alloc != blocksOf(fi)*512 || in.CubeCell != int64(cell.Index()) {
			t.Errorf("shared inode = %+v, want size 5000, alloc %d, cell %d", in, blocksOf(fi)*512, cell.Index())
		}
	}
	// Walk time: every link counts in its own directory and in the root.
	if rows["d1"].Files != 1 || rows["d2"].Files != 1 || rows[""].Files != 3 || res.Stats.HardlinkFiles != 2 {
		t.Errorf("files d1/d2/root = %d/%d/%d, HardlinkFiles = %d; want 1/1/3 and 2",
			rows["d1"].Files, rows["d2"].Files, rows[""].Files, res.Stats.HardlinkFiles)
	}
}

// makeChain builds depth nested directories named d below root, descriptor
// relative, so the path may exceed PATH_MAX.
func makeChain(t *testing.T, root string, depth int) {
	t.Helper()
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for range depth {
		if err := unix.Mkdirat(fd, "d", 0o750); err != nil {
			_ = unix.Close(fd)
			t.Fatal(err)
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

// The early close of a directory's descriptor once its last subdirectory is
// open only matters when the child is walked inline: a queued child lets the
// parent return and close it anyway. forceInline makes every child inline, and
// onDir samples the fd count on the way down, so a parent that kept its
// descriptor would show up as one extra fd per level.
func TestScan_DeepChainFDs(t *testing.T) {
	root := t.TempDir()
	makeChain(t, root, 2000)
	for _, inline := range []bool{true, false} {
		for _, workers := range []int{1, 4} {
			t.Run(fmt.Sprintf("inline=%v/workers=%d", inline, workers), func(t *testing.T) {
				before := openFDs(t)
				var peak atomic.Int64
				opts := testOpts(workers)
				opts.forceInline = inline
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
				if res.Stats.Dirs != 2001 || res.Stats.Errors != 0 || len(rec.dirs) != 2001 {
					t.Fatalf("Dirs = %d, Errors = %d, rows = %d; want 2001, 0, 2001", res.Stats.Dirs, res.Stats.Errors, len(rec.dirs))
				}
				rec.checkIDs(t)
				if root := rec.byPath(t)[""]; root.Dirs != 2000 {
					t.Fatalf("root.Dirs = %d, want 2000", root.Dirs)
				}
				if limit := int64(before + workers + 4); peak.Load() > limit {
					t.Fatalf("peak open fds %d during the descent, want <= %d (baseline %d + workers %d + 4)", peak.Load(), limit, before, workers)
				}
				if after := openFDs(t); after != before {
					t.Fatalf("open fds: %d before, %d after", before, after)
				}
			})
		}
	}
}

func TestScan_Cancel(t *testing.T) {
	root := makeDirs(t, 2000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before, g := openFDs(t), runtime.NumGoroutine()
	res, err := Scan(ctx, root, testOpts(4), newRec())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Stats.Dirs > 1 {
		t.Fatalf("Dirs = %d, want <= 1", res.Stats.Dirs)
	}
	if after := openFDs(t); after != before {
		t.Fatalf("open fds: %d before, %d after", before, after)
	}
	settle(t, g)
}

func TestScan_CancelMidWalk(t *testing.T) {
	root := makeDirs(t, 2000)
	total := oracle(t, root).Dirs
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before, g := openFDs(t), runtime.NumGoroutine()
	var entered atomic.Int64
	opts := testOpts(4)
	opts.onDir = func() {
		if entered.Add(1) == 10 {
			cancel()
		}
	}
	res, err := Scan(ctx, root, opts, newRec())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (never nil)", err)
	}
	if res.Stats.Dirs >= total {
		t.Fatalf("Dirs = %d, want fewer than the tree's %d", res.Stats.Dirs, total)
	}
	if after := openFDs(t); after != before {
		t.Fatalf("open fds: %d before, %d after", before, after)
	}
	settle(t, g)
}

func TestScan_CancelFromSink(t *testing.T) {
	root := makeDirs(t, 2000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := newRec()
	rec.onDir = func(n int) error {
		if n == 50 {
			cancel()
		}
		return nil
	}
	before, g := openFDs(t), runtime.NumGoroutine()
	_, err := Scan(ctx, root, testOpts(4), rec)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if after := openFDs(t); after != before {
		t.Fatalf("open fds: %d before, %d after", before, after)
	}
	settle(t, g)
}

func TestScan_SinkError(t *testing.T) {
	tree := makeTree(t, 3)
	errBoom := errors.New("boom")
	rec := newRec()
	var failed atomic.Bool
	calls := 0
	rec.onDir = func(n int) error {
		if failed.Load() {
			calls++
		}
		if n == 10 {
			failed.Store(true)
			return errBoom
		}
		return nil
	}
	before, g := openFDs(t), runtime.NumGoroutine()
	_, err := Scan(context.Background(), tree.root, testOpts(4), rec)
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want the sink's error", err)
	}
	if calls != 0 || len(rec.dirs) != 10 {
		t.Fatalf("sink got %d AddDir calls (%d after the failure), want exactly 10", len(rec.dirs), calls)
	}
	if after := openFDs(t); after != before {
		t.Fatalf("open fds: %d before, %d after", before, after)
	}
	settle(t, g)
}

func TestScan_Progress(t *testing.T) {
	root := makeDirs(t, 2000)
	var snaps []Stats
	opts := testOpts(4)
	opts.ProgressEvery = time.Millisecond
	opts.Progress = func(s Stats) { snaps = append(snaps, s) }
	res, err := Scan(context.Background(), root, opts, Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) == 0 {
		t.Fatal("Progress was never called")
	}
	f := res.Stats
	for i, s := range snaps {
		if s.Dirs > f.Dirs || s.Files > f.Files || s.BytesLogical > f.BytesLogical || s.BytesAlloc > f.BytesAlloc {
			t.Fatalf("snapshot %d = %+v exceeds the final %+v", i, s, f)
		}
	}
	if last := snaps[len(snaps)-1]; last.Dirs != f.Dirs || last.Files != f.Files {
		t.Fatalf("last snapshot = %+v, want the final totals %+v", last, f)
	}
}

func TestScan_RootErrors(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()
	before := openFDs(t)

	_, err := Scan(ctx, filepath.Join(tmp, "missing"), testOpts(1), Discard)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing root: err = %v, want fs.ErrNotExist", err)
	}

	if err := os.Mkdir(filepath.Join(tmp, "real"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(tmp, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(ctx, filepath.Join(tmp, "link"), testOpts(1), Discard); err == nil {
		t.Error("symlinked root: want an error")
	}

	writeFile(t, filepath.Join(tmp, "file"), 10)
	if _, err := Scan(ctx, filepath.Join(tmp, "file"), testOpts(1), Discard); !errors.Is(err, unix.ENOTDIR) {
		t.Errorf("file root: err = %v, want ENOTDIR", err)
	}

	if _, err := Scan(ctx, filepath.Join(tmp, "real"), Options{AllowWritable: true}, Discard); err == nil {
		t.Error("Workers == 0: want an error")
	}

	rec := newRec()
	_, err = Scan(ctx, filepath.Join(tmp, "real"), Options{Workers: 1}, rec)
	if !errors.Is(err, ErrWritableRoot) {
		t.Errorf("writable root: err = %v, want ErrWritableRoot", err)
	}
	if len(rec.dirs) != 0 {
		t.Errorf("a refused root produced %d rows", len(rec.dirs))
	}

	if after := openFDs(t); after != before {
		t.Fatalf("open fds: %d before, %d after", before, after)
	}
}

// canon reduces a recording to strings that do not depend on ids or on the
// order rows arrived in.
func canon(t *testing.T, r *recSink) []string {
	t.Helper()
	p := r.paths(t)
	var out []string
	for _, d := range r.dirs {
		out = append(out, fmt.Sprintf("dir %q depth=%d dev=%d ino=%d mtime=%d uid=%d gid=%d own=%d/%d files=%d dirs=%d logical=%d alloc=%d flags=%d err=%d cube=%x",
			p[d.ID], d.DepthAnchor, d.Dev, d.Ino, d.Mtime, d.UID, d.GID, d.OwnFiles, d.OwnAlloc,
			d.Files, d.Dirs, d.BytesLogical, d.BytesAlloc, d.Flags, d.ErrorCount, []byte(r.cubes[d.ID])))
	}
	for _, f := range r.tops {
		out = append(out, fmt.Sprintf("top %q %s #%d in %q %q size=%d alloc=%d mtime=%d uid=%d",
			p[f.AnchorID], f.Kind, f.Rank, p[f.ParentDirID], f.Name, f.Size, f.Alloc, f.Mtime, f.UID))
	}
	for _, e := range r.exts {
		out = append(out, fmt.Sprintf("ext %q %q bytes=%d files=%d", p[e.DirID], e.Ext, e.Bytes, e.Files))
	}
	for _, in := range r.inodes {
		out = append(out, fmt.Sprintf("inode %+v", in))
	}
	for _, l := range r.links {
		out = append(out, fmt.Sprintf("link %d/%d %q %q", l.Dev, l.Ino, p[l.DirID], l.Name))
	}
	for _, e := range r.errs {
		out = append(out, fmt.Sprintf("err %q %q %s %d %s", p[e.DirID], e.Name, e.Op, e.Errno, e.Message))
	}
	sort.Strings(out)
	return out
}

func TestScan_DeterministicAcrossWorkers(t *testing.T) {
	tree := makeTree(t, 3)
	var first []string
	for i, workers := range []int{1, 2, 8, 8, 3} {
		opts := testOpts(workers)
		opts.Limits = agg.Limits{TopN: 3, TopMaxDepth: 5, TopMinAlloc: 0, TopExts: 3}
		rec := newRec()
		if _, err := Scan(context.Background(), tree.root, opts, rec); err != nil {
			t.Fatal(err)
		}
		rec.checkIDs(t)
		got := canon(t, rec)
		if i == 0 {
			first = got
			if len(rec.tops) == 0 || len(rec.exts) == 0 || len(rec.links) == 0 {
				t.Fatalf("fixture produced tops %d, exts %d, links %d; want all non-empty", len(rec.tops), len(rec.exts), len(rec.links))
			}
			continue
		}
		if !slices.Equal(got, first) {
			t.Fatalf("workers=%d (run %d) differs from workers=1:\n%s", workers, i, firstDiff(got, first))
		}
	}
}

func firstDiff(a, b []string) string {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return fmt.Sprintf("line %d\n got %s\nwant %s", i, a[i], b[i])
		}
	}
	return fmt.Sprintf("lengths %d and %d", len(a), len(b))
}

// A wide tree makes many nodes finish while their parents are still listing
// and dispatching, which is what -race has to see.
func TestScan_ManyDirsConcurrent(t *testing.T) {
	root := makeDirs(t, 3000)
	want := oracle(t, root)
	for range 3 {
		rec := newRec()
		opts := testOpts(8)
		opts.Limits = agg.Limits{TopN: 2, TopMaxDepth: 5, TopMinAlloc: 0, TopExts: 2}
		res, err := Scan(context.Background(), root, opts, rec)
		if err != nil {
			t.Fatal(err)
		}
		rec.checkIDs(t)
		if r := rec.byPath(t)[""]; r.Files != want.Files || r.Dirs != want.Dirs-1 || res.Stats.Dirs != want.Dirs {
			t.Fatalf("root files/dirs = %d/%d, Dirs = %d; want %d/%d and %d", r.Files, r.Dirs, res.Stats.Dirs, want.Files, want.Dirs-1, want.Dirs)
		}
	}
}

// The real sink: hostile names must survive Scan, gen.Writer and SQLite as the
// same bytes, and the unique (parent_id, name) index must accept the result.
func TestScan_IntoGenWriter(t *testing.T) {
	names := []string{"a\nb", "\xff\xfe", "\xe2\x80\xaeevil", "=cmd", strings.Repeat("x", 255)}
	root := t.TempDir()
	for i, n := range names {
		if err := os.Mkdir(filepath.Join(root, n), 0o750); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, n, n), 100+i)
		if err := os.Link(filepath.Join(root, n, n), filepath.Join(root, n, "link")); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	genPath := gen.Path(t.TempDir(), 1, 1)
	w, err := gen.Create(ctx, genPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Abort() }()
	opts := testOpts(4)
	opts.Limits = agg.Limits{TopN: 10, TopMaxDepth: 5, TopMinAlloc: 0, TopExts: 5}
	if _, err := Scan(ctx, root, opts, w); err != nil {
		t.Fatal(err)
	}
	if err := w.BuildIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(ctx, gen.Meta{ScanID: 1, RootID: 1, StartedAt: time.Now(), FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	db, _, err := gen.OpenReadOnly(ctx, genPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	col := func(q string) []string {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var b []byte
			if err := rows.Scan(&b); err != nil {
				t.Fatal(err)
			}
			out = append(out, string(b))
		}
		sort.Strings(out)
		return out
	}
	want := slices.Clone(names)
	sort.Strings(want)
	if got := col(`SELECT name FROM dirs WHERE parent_id = 1`); !slices.Equal(got, want) {
		t.Errorf("dirs.name = %q, want %q", got, want)
	}
	wantTop := append(slices.Clone(want), "link")
	sort.Strings(wantTop)
	if got := col(`SELECT DISTINCT name FROM top_files WHERE anchor_id = 1 AND kind = 'largest'`); !slices.Equal(got, wantTop) {
		t.Errorf("top_files.name = %q, want %q", got, wantTop)
	}
	wantLinks := append(slices.Clone(want), slices.Repeat([]string{"link"}, len(names))...)
	sort.Strings(wantLinks)
	if got := col(`SELECT name FROM shared_links`); !slices.Equal(got, wantLinks) {
		t.Errorf("shared_links.name = %q, want %q", got, wantLinks)
	}
	// Each link carries the values the walk saw through it.
	if got := col(`SELECT count(*) FROM shared_links WHERE mtime = 0`); !slices.Equal(got, []string{"0"}) {
		t.Errorf("links without an mtime: %q", got)
	}
}
