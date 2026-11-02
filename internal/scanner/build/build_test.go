//go:build linux

package build_test

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
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
	"golang.org/x/sys/unix"
)

// scanStart is the fixed clock of every build in this package: ages in the
// fixture are relative to it.
var scanStart = time.Unix(1_800_000_000, 0)

// testLimits is the top-N rule of the oracle fixture: three files per list,
// down to anchor depth 5, no size floor, four extensions.
var testLimits = agg.Limits{TopN: 3, TopMaxDepth: 5, TopMinAlloc: 0, TopExts: 4}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func opts(dataDir, root string, workers int, lim agg.Limits) build.Options {
	return build.Options{
		DataDir: dataDir, RootID: 1, ScanID: 1, Root: root,
		Walk:           walk.Options{Workers: workers, AllowWritable: true, Limits: lim},
		ScannerVersion: "test",
		Now:            func() time.Time { return scanStart },
	}
}

// writeFile creates path with size bytes and an mtime of age before scanStart.
func writeFile(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	must(t, os.WriteFile(path, make([]byte, size), 0o600))
	m := scanStart.Add(-age)
	must(t, os.Chtimes(path, m, m))
}

const day = 24 * time.Hour

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		must(t, os.MkdirAll(p, 0o750))
	}
}

func link(t *testing.T, old, new string) {
	t.Helper()
	must(t, os.Link(old, new))
}

func symlink(t *testing.T, target, path string) {
	t.Helper()
	must(t, os.Symlink(target, path))
}

// hostileNames are the names of Review Focus 1: invalid UTF-8, newline, RTL
// override, formula starters, control characters and a 255-byte name.
var hostileNames = []string{
	"a\nb", "\xff\xfe", "\xe2\x80\xaeevil", "=cmd", "+1", "-2", "@x", "\x01\x1b[31m", strings.Repeat("x", 255),
}

// makeFixture builds the tree of the oracle test below a fresh temp dir and
// returns its root. It holds nested dirs, extensions of every kind, hardlinks
// across dirs and within one dir, a link whose other name lies outside the
// root, symlink loops, a fifo, a sparse file, hostile names, a 7-deep chain
// and, unless running as root, an unreadable directory.
func makeFixture(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	mkdirs(t, root, outside,
		filepath.Join(root, "docs"), filepath.Join(root, "media"),
		filepath.Join(root, "h1"), filepath.Join(root, "h2"),
		filepath.Join(root, "a", "b"), filepath.Join(root, "a-b"),
		filepath.Join(root, "empty"),
		filepath.Join(root, "hostile", "D"), filepath.Join(root, "hostile", "F"), filepath.Join(root, "hostile", "H"),
		filepath.Join(root, "deep", "d1", "d2", "d3", "d4", "d5", "d6", "d7"))

	writeFile(t, filepath.Join(root, "top.mkv"), 3000, time.Hour)
	writeFile(t, filepath.Join(root, "Notes.TXT"), 10, 3*day)
	writeFile(t, filepath.Join(root, "noext"), 7000, 20*day)
	writeFile(t, filepath.Join(root, ".hidden"), 1, 60*day)
	writeFile(t, filepath.Join(root, "trailing."), 50, 200*day)
	writeFile(t, filepath.Join(root, "sparse.img"), 0, 400*day)
	must(t, os.Truncate(filepath.Join(root, "sparse.img"), 5<<20)) // size without allocation
	must(t, os.Chtimes(filepath.Join(root, "sparse.img"), scanStart.Add(-400*day), scanStart.Add(-400*day)))
	must(t, unix.Mkfifo(filepath.Join(root, "pipe"), 0o600))
	symlink(t, ".", filepath.Join(root, "loop"))
	symlink(t, "nowhere", filepath.Join(root, "dangling"))

	// A link whose other name is outside the root: one link seen, nlink 2.
	writeFile(t, filepath.Join(root, "o.dat"), 70000, 900*day)
	link(t, filepath.Join(root, "o.dat"), filepath.Join(outside, "o2.dat"))

	writeFile(t, filepath.Join(root, "docs", "a.pdf"), 4000, 2*day)
	writeFile(t, filepath.Join(root, "docs", "b.pdf"), 4000, 2*day) // same size and mtime as a.pdf
	writeFile(t, filepath.Join(root, "docs", "same.txt"), 700, 5*day)
	writeFile(t, filepath.Join(root, "docs", "readme.md"), 200, 6*day)
	writeFile(t, filepath.Join(root, "media", "same.txt"), 700, 5*day) // ties with docs/same.txt on every key but the parent
	writeFile(t, filepath.Join(root, "media", "clip.mp4"), 100000, 100*day)
	writeFile(t, filepath.Join(root, "media", "song.mp3"), 8000, 2000*day)
	symlink(t, "..", filepath.Join(root, "media", "up"))

	// One inode in three places: two dirs and the root.
	writeFile(t, filepath.Join(root, "h1", "x.bin"), 9000, 30*day)
	link(t, filepath.Join(root, "h1", "x.bin"), filepath.Join(root, "h2", "x2.bin"))
	link(t, filepath.Join(root, "h1", "x.bin"), filepath.Join(root, "x3.bin"))
	// Two names in one directory only.
	writeFile(t, filepath.Join(root, "h1", "s1.dat"), 6000, 40*day)
	link(t, filepath.Join(root, "h1", "s1.dat"), filepath.Join(root, "h1", "s2.dat"))
	// Path order is component-wise: a/b/f.log wins over a-b/f.log although
	// "a-b" < "a/b" as strings.
	writeFile(t, filepath.Join(root, "a", "b", "f.log"), 3000, 10*day)
	link(t, filepath.Join(root, "a", "b", "f.log"), filepath.Join(root, "a-b", "f.log"))

	// A chain deeper than the top-list depth, with a file at every level.
	dir := filepath.Join(root, "deep")
	for i := 1; i <= 7; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%d.zip", i)), 1000*i, time.Duration(i)*day)
		dir = filepath.Join(dir, fmt.Sprintf("d%d", i))
	}

	for i, n := range hostileNames {
		mkdirs(t, filepath.Join(root, "hostile", "D", n))
		writeFile(t, filepath.Join(root, "hostile", "D", n, "in"), 100, 9*day)
		f := filepath.Join(root, "hostile", "F", n)
		writeFile(t, f, 1000+i, time.Duration(i+1)*day)
		link(t, f, filepath.Join(root, "hostile", "H", n))
	}

	if os.Geteuid() != 0 { // root reads a 000 directory
		locked := filepath.Join(root, "locked")
		mkdirs(t, locked)
		must(t, os.Chmod(locked, 0))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // a directory must be searchable to be removed
	}
	return root
}

func runOK(t *testing.T, o build.Options) build.Result {
	t.Helper()
	res, err := build.Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func countRows(t *testing.T, d *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	must(t, d.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func TestRun_EmptyRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	mkdirs(t, root)
	res := runOK(t, opts(t.TempDir(), root, 2, testLimits))
	if err := build.Verify(context.Background(), res.Path); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	d, _, err := gen.OpenReadOnly(context.Background(), res.Path)
	must(t, err)
	defer func() { _ = d.Close() }()
	if n := countRows(t, d, "dirs"); n != 1 {
		t.Fatalf("%d dirs rows, want 1", n)
	}
	for _, tbl := range []string{"top_files", "top_exts", "shared_inodes", "shared_links", "errors"} {
		if n := countRows(t, d, tbl); n != 0 {
			t.Errorf("%s has %d rows, want 0", tbl, n)
		}
	}
	var pre, post, files int64
	must(t, d.QueryRow("SELECT pre, post, files FROM dirs").Scan(&pre, &post, &files))
	if pre != 1 || post != 1 || files != 0 {
		t.Errorf("root = pre %d post %d files %d, want 1 1 0", pre, post, files)
	}
}

func TestRun_MetaStats(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	mkdirs(t, filepath.Join(root, "a", "b"), filepath.Join(root, "c"))
	writeFile(t, filepath.Join(root, "a", "x.txt"), 100, day)
	writeFile(t, filepath.Join(root, "c", "y.txt"), 200, day)
	link(t, filepath.Join(root, "a", "x.txt"), filepath.Join(root, "c", "x2.txt"))
	o := opts(t.TempDir(), root, 3, testLimits)
	res := runOK(t, o)

	d, meta, err := gen.OpenReadOnly(context.Background(), res.Path)
	must(t, err)
	defer func() { _ = d.Close() }()
	var got build.Stats
	must(t, json.Unmarshal(meta.Stats, &got))
	rows := countRows(t, d, "dirs")
	if got.DirRows != rows {
		t.Errorf("meta.stats dir_rows = %d, rows = %d", got.DirRows, rows)
	}
	if got.Walk.Dirs != rows || rows != 4 {
		t.Errorf("meta.stats walk.dirs = %d, rows = %d, want both 4", got.Walk.Dirs, rows)
	}
	if got.Workers != 3 || got.TopN != 3 || got.Walk.Files != 3 || got.Hardlinks.LosingLinks != 1 || got.Hardlinks.Inodes != 1 {
		t.Errorf("stats = %+v", got)
	}
	if got.Walk.HardlinkFiles != 2 {
		t.Errorf("walk.hardlink_files = %d, want 2 (walk-time, before subtraction)", got.Walk.HardlinkFiles)
	}
	if got.ErrorRowsStored != 0 || got.ErrorRowsDropped != 0 {
		t.Errorf("error rows stored %d dropped %d, want 0 0", got.ErrorRowsStored, got.ErrorRowsDropped)
	}
	if res.Stats.Walk.Dirs != got.Walk.Dirs || res.Stats.Hardlinks != got.Hardlinks {
		t.Errorf("Result.Stats differs from meta.stats: %+v vs %+v", res.Stats, got)
	}
	var raw map[string]json.RawMessage
	must(t, json.Unmarshal(meta.Stats, &raw))
	for _, k := range []string{"walk", "hardlinks", "error_rows_stored", "error_rows_dropped", "workers", "walk_seconds", "finalize_seconds"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("meta.stats has no %q key: %s", k, meta.Stats)
		}
	}
	if !meta.StartedAt.Equal(scanStart) || meta.ScannerVersion != "test" || meta.ScanID != 1 || meta.RootID != 1 {
		t.Errorf("meta = %+v", meta)
	}
}

func TestRun_CancelRemovesFile(t *testing.T) {
	root := makeFixture(t)
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := opts(dataDir, root, 2, testLimits)
	var calls atomic.Int64
	o.Walk.ProgressEvery = time.Millisecond
	o.Walk.Progress = func(walk.Stats) {
		calls.Add(1)
		cancel()
	}
	_, err := build.Run(ctx, o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if calls.Load() == 0 {
		t.Fatal("progress callback never ran")
	}
	if _, err := os.Stat(gen.Path(dataDir, 1, 1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generation file left behind: stat err = %v", err)
	}
	// A cancelled build leaves nothing in the scans dir.
	ents, err := os.ReadDir(filepath.Join(dataDir, "scans"))
	must(t, err)
	if len(ents) != 0 {
		t.Errorf("scans dir holds %d entries after cancel", len(ents))
	}
}

func TestRun_FailureRemovesFileAndSparesExisting(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	mkdirs(t, root)
	dataDir := t.TempDir()

	bad := opts(dataDir, root, 0, testLimits) // Workers < 1
	if _, err := build.Run(context.Background(), bad); err == nil {
		t.Fatal("Run with Workers 0 succeeded")
	}
	if _, err := os.Stat(gen.Path(dataDir, 1, 1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file left after a failed Run: %v", err)
	}

	writable := opts(dataDir, root, 1, testLimits)
	writable.Walk.AllowWritable = false // a temp dir is writable
	if _, err := build.Run(context.Background(), writable); !errors.Is(err, walk.ErrWritableRoot) {
		t.Fatalf("Run on a writable root = %v, want ErrWritableRoot", err)
	}
	if _, err := os.Stat(gen.Path(dataDir, 1, 1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file left after a refused root: %v", err)
	}

	// Run refuses an existing generation and must not delete it.
	existing := gen.Path(dataDir, 1, 1)
	must(t, os.MkdirAll(filepath.Dir(existing), 0o750))
	must(t, os.WriteFile(existing, []byte("precious"), 0o600))
	if _, err := build.Run(context.Background(), opts(dataDir, root, 1, testLimits)); err == nil {
		t.Fatal("Run over an existing generation succeeded")
	}
	if b, err := os.ReadFile(existing); err != nil || string(b) != "precious" { //nolint:gosec // test path
		t.Fatalf("existing file was touched: %q, %v", b, err)
	}
}

// ---- reading a generation back ----

type dbDir struct {
	ID, Parent                                      int64
	Name                                            []byte
	Pre, Post                                       sql.NullInt64
	Depth, Dev, Ino, Mtime, UID, GID                int64
	OwnFiles, OwnAlloc, Files, Dirs, Logical, Alloc int64
	Flags, ErrCount                                 int64
}

type dbTop struct {
	Anchor, Parent string
	Kind           string
	Rank           int64
	Name           []byte
	Size, Alloc    int64
	Mtime, UID     int64
}

type dbExt struct {
	Dir, Ext     string
	Bytes, Files int64
}

type dbInode struct{ Dev, Ino, Size, Alloc, Mtime, Cell int64 }

type dbLink struct {
	Dev, Ino    int64
	Dir         string
	Name        []byte
	Size, Alloc int64
	Mtime       int64
}

type dbErr struct {
	Dir     string
	Name    []byte
	Op, Msg string
	Errno   int64
}

// genData is a whole generation read back with every id replaced by the path
// it stands for.
type genData struct {
	meta   gen.Meta
	dirs   []dbDir
	path   map[int64]string // id -> path below the root, "" for the root
	byPath map[string]*dbDir
	cubes  map[string][]byte
	tops   []dbTop
	exts   []dbExt
	inodes []dbInode
	links  []dbLink
	errs   []dbErr
}

func readGen(t *testing.T, file string) *genData {
	t.Helper()
	d, meta, err := gen.OpenReadOnly(context.Background(), file)
	must(t, err)
	defer func() { _ = d.Close() }()
	g := &genData{meta: meta, path: map[int64]string{}, byPath: map[string]*dbDir{}, cubes: map[string][]byte{}}

	rs, err := d.Query(`SELECT id, parent_id, name, pre, post, depth_anchor, dev, ino, mtime, uid, gid, own_files, own_alloc,
		files, dirs, bytes_logical, bytes_alloc, flags, error_count FROM dirs ORDER BY id`)
	must(t, err)
	for rs.Next() {
		var r dbDir
		must(t, rs.Scan(&r.ID, &r.Parent, &r.Name, &r.Pre, &r.Post, &r.Depth, &r.Dev, &r.Ino, &r.Mtime, &r.UID, &r.GID,
			&r.OwnFiles, &r.OwnAlloc, &r.Files, &r.Dirs, &r.Logical, &r.Alloc, &r.Flags, &r.ErrCount))
		g.dirs = append(g.dirs, r)
	}
	must(t, rs.Err())
	must(t, rs.Close())
	for i := range g.dirs {
		r := &g.dirs[i]
		p := ""
		if r.Parent != 0 {
			pp, ok := g.path[r.Parent]
			if !ok {
				t.Fatalf("dir %d: parent %d not read before it", r.ID, r.Parent)
			}
			p = string(r.Name)
			if pp != "" {
				p = pp + "/" + p
			}
		}
		g.path[r.ID] = p
		g.byPath[p] = r
	}

	query := func(q string, scan func(rs *sql.Rows)) {
		rs, err := d.Query(q)
		must(t, err)
		for rs.Next() {
			scan(rs)
		}
		must(t, rs.Err())
		must(t, rs.Close())
	}
	query(`SELECT dir_id, blob FROM cube`, func(rs *sql.Rows) {
		var id int64
		var b []byte
		must(t, rs.Scan(&id, &b))
		g.cubes[g.path[id]] = b
	})
	query(`SELECT anchor_id, kind, rank, parent_dir_id, name, size, alloc, mtime, uid FROM top_files`, func(rs *sql.Rows) {
		var r dbTop
		var a, p int64
		must(t, rs.Scan(&a, &r.Kind, &r.Rank, &p, &r.Name, &r.Size, &r.Alloc, &r.Mtime, &r.UID))
		r.Anchor, r.Parent = g.path[a], g.path[p]
		g.tops = append(g.tops, r)
	})
	query(`SELECT dir_id, ext, bytes, files FROM top_exts`, func(rs *sql.Rows) {
		var r dbExt
		var id int64
		must(t, rs.Scan(&id, &r.Ext, &r.Bytes, &r.Files))
		r.Dir = g.path[id]
		g.exts = append(g.exts, r)
	})
	query(`SELECT dev, ino, size, alloc, mtime, cube_cell FROM shared_inodes`, func(rs *sql.Rows) {
		var r dbInode
		must(t, rs.Scan(&r.Dev, &r.Ino, &r.Size, &r.Alloc, &r.Mtime, &r.Cell))
		g.inodes = append(g.inodes, r)
	})
	query(`SELECT dev, ino, dir_id, name, size, alloc, mtime FROM shared_links`, func(rs *sql.Rows) {
		var r dbLink
		var id int64
		must(t, rs.Scan(&r.Dev, &r.Ino, &id, &r.Name, &r.Size, &r.Alloc, &r.Mtime))
		r.Dir = g.path[id]
		g.links = append(g.links, r)
	})
	query(`SELECT dir_id, name, op, errno, message FROM errors`, func(rs *sql.Rows) {
		var r dbErr
		var id int64
		must(t, rs.Scan(&id, &r.Name, &r.Op, &r.Errno, &r.Msg))
		r.Dir = g.path[id]
		g.errs = append(g.errs, r)
	})
	return g
}

// dump is the canonical text of a generation: one line per row, every id
// replaced by a path, sorted. Everything that may legitimately differ between
// runs (ids, parent ids, timings in meta.stats) is left out.
func (g *genData) dump() string {
	var lines []string
	add := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	for _, r := range g.dirs {
		add("dir %q depth=%d dev=%d ino=%d mtime=%d uid=%d gid=%d own_files=%d own_alloc=%d files=%d dirs=%d logical=%d alloc=%d flags=%d errors=%d pre=%d post=%d",
			g.path[r.ID], r.Depth, r.Dev, r.Ino, r.Mtime, r.UID, r.GID, r.OwnFiles, r.OwnAlloc, r.Files, r.Dirs,
			r.Logical, r.Alloc, r.Flags, r.ErrCount, r.Pre.Int64, r.Post.Int64)
	}
	for p, b := range g.cubes {
		add("cube %q %x", p, b)
	}
	for _, r := range g.tops {
		add("top anchor=%q %s rank=%d parent=%q name=%q size=%d alloc=%d mtime=%d uid=%d",
			r.Anchor, r.Kind, r.Rank, r.Parent, r.Name, r.Size, r.Alloc, r.Mtime, r.UID)
	}
	for _, r := range g.exts {
		add("ext dir=%q ext=%q bytes=%d files=%d", r.Dir, r.Ext, r.Bytes, r.Files)
	}
	for _, r := range g.inodes {
		add("inode dev=%d ino=%d size=%d alloc=%d mtime=%d cell=%d", r.Dev, r.Ino, r.Size, r.Alloc, r.Mtime, r.Cell)
	}
	for _, r := range g.links {
		add("link dev=%d ino=%d dir=%q name=%q", r.Dev, r.Ino, r.Dir, r.Name)
	}
	for _, r := range g.errs {
		add("err dir=%q name=%q op=%s errno=%d msg=%q", r.Dir, r.Name, r.Op, r.Errno, r.Msg)
	}
	add("meta scan=%d root=%d version=%q started=%d finished=%d",
		g.meta.ScanID, g.meta.RootID, g.meta.ScannerVersion, g.meta.StartedAt.Unix(), g.meta.FinishedAt.Unix())
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func TestRun_Deterministic(t *testing.T) {
	root := makeFixture(t)
	var first string
	for i, workers := range []int{1, 2, 8, 8} {
		res := runOK(t, opts(t.TempDir(), root, workers, testLimits))
		if err := build.Verify(context.Background(), res.Path); err != nil {
			t.Fatalf("workers=%d: Verify: %v", workers, err)
		}
		dump := readGen(t, res.Path).dump()
		if i == 0 {
			first = dump
			if len(strings.Split(dump, "\n")) < 100 {
				t.Fatalf("dump is suspiciously short (%d lines)", len(strings.Split(dump, "\n")))
			}
			continue
		}
		if dump != first {
			a, b := strings.Split(first, "\n"), strings.Split(dump, "\n")
			for j := 0; j < min(len(a), len(b)); j++ {
				if a[j] != b[j] {
					t.Fatalf("run %d (workers=%d) differs from workers=1 at line %d:\n got %s\nwant %s", i, workers, j, b[j], a[j])
				}
			}
			t.Fatalf("run %d (workers=%d) has %d lines, workers=1 has %d", i, workers, len(b), len(a))
		}
	}
}

// cellIndex mirrors how the oracle places a file, without agg.CellFor.
func cellIndex(name []byte, size, mtime int64) int {
	return cube.Cell{
		Category: uint8(cube.CategoryOf(name, cube.DefaultCategories())),
		Age:      cube.AgeBucket(time.Unix(mtime, 0), scanStart),
		Size:     cube.SizeBucket(size),
	}.Index()
}

func TestRun_MetaStatsDirRowsWithUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 000 directory")
	}
	root := filepath.Join(t.TempDir(), "root")
	mkdirs(t, filepath.Join(root, "a"), filepath.Join(root, "locked"))
	must(t, os.Chmod(filepath.Join(root, "locked"), 0))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked"), 0o700) }) //nolint:gosec // a directory must be searchable to be removed
	res := runOK(t, opts(t.TempDir(), root, 2, testLimits))
	d, meta, err := gen.OpenReadOnly(context.Background(), res.Path)
	must(t, err)
	defer func() { _ = d.Close() }()
	var got build.Stats
	must(t, json.Unmarshal(meta.Stats, &got))
	rows := countRows(t, d, "dirs")
	if rows != 3 || got.DirRows != rows || got.Walk.Dirs != 2 {
		t.Errorf("rows = %d, dir_rows = %d, walk.dirs = %d; want 3, 3, 2 (the unreadable dir has a row but was not opened)",
			rows, got.DirRows, got.Walk.Dirs)
	}
}

// TestRun_OnFinalize pins that OnFinalize runs once, after the walk's last
// progress callback and before the generation file exists in final form.
func TestRun_OnFinalize(t *testing.T) {
	root := makeFixture(t)
	o := opts(t.TempDir(), root, 2, testLimits)
	var order []string
	o.Walk.ProgressEvery = time.Hour // only the final callback fires
	o.Walk.Progress = func(walk.Stats) { order = append(order, "progress") }
	o.OnFinalize = func() { order = append(order, "finalize") }
	if _, err := build.Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "progress" || order[1] != "finalize" {
		t.Fatalf("call order = %v, want [progress finalize]", order)
	}
}
