package finalize_test

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/finalize"
)

var scanStart = time.Unix(2_000_000_000, 0).UTC()

const fileMtime = 2_000_000_000 - 86_400

func comps(s string) [][]byte {
	var out [][]byte
	for _, p := range strings.Split(s, "/") {
		out = append(out, []byte(p))
	}
	return out
}

func TestComparePaths(t *testing.T) {
	cases := []struct {
		a, b [][]byte
		want int
	}{
		{comps("a/z"), comps("a/b/c"), 1}, // component b < z, not length
		{comps("a/b/c"), comps("a/z"), -1},
		{comps("a"), comps("a/b"), -1}, // a proper prefix first
		{comps("a/b"), comps("a"), 1},
		{comps("a/b"), comps("a/b"), 0},
		{[][]byte{{0xff}}, [][]byte{[]byte("z")}, 1}, // raw 0xff sorts last
		{[][]byte{{'a', 0xff}}, [][]byte{[]byte("a~")}, 1},
		{comps("a-b/f"), comps("a/b/f"), 1}, // component "a" < "a-b" although '-' < '/'
		{comps("a/b/f"), comps("a-b/f"), -1},
	}
	for _, c := range cases {
		if got := finalize.ComparePaths(c.a, c.b); got != c.want {
			t.Errorf("ComparePaths(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if finalize.ComparePaths(nil, nil) != 0 || finalize.ComparePaths(nil, comps("a")) != -1 {
		t.Error("empty path must equal itself and precede everything else")
	}
}

// file is one regular-file name of a fixture. linked marks nlink > 1, which
// makes the walk emit shared_inodes/shared_links rows for it.
type file struct {
	path        string
	dev, ino    int64
	size, alloc int64
	linked      bool
}

type fixture struct {
	dirs   []string // parents before children; the root "" gets id 1, dirs[i] id i+2
	files  []file
	mtimes map[string]int64 // per-path mtime override; the default is fileMtime
}

func (f fixture) mtime(path string) int64 {
	if m, ok := f.mtimes[path]; ok {
		return m
	}
	return fileMtime
}

func parent(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

func base(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }

type acc struct {
	ownFiles, ownAlloc, files, logical, alloc int64
	cube                                      cube.Acc
	exts                                      map[string][2]int64
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// write builds the walk-time generation (every link counted in its own
// directory and all ancestors), with indexes, ready for Hardlinks.
func (f fixture) write(t *testing.T) (*gen.Writer, string) {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "1-1.db")
	w, err := gen.Create(ctx, path)
	must(t, err)
	t.Cleanup(func() { _ = w.Abort() })
	ids := map[string]int64{"": 1}
	accs := map[string]*acc{"": {exts: map[string][2]int64{}}}
	for i, d := range f.dirs {
		ids[d] = int64(i) + 2
		accs[d] = &acc{exts: map[string][2]int64{}}
	}
	cats := cube.DefaultCategories()
	seen := map[[2]int64]bool{}
	for _, fl := range f.files {
		name := base(fl.path)
		cell := agg.CellFor([]byte(name), fl.size, f.mtime(fl.path), scanStart, cats)
		ext := cube.Ext([]byte(name))
		for d, first := parent(fl.path), true; ; d, first = parent(d), false {
			a := accs[d]
			if first {
				a.ownFiles++
				a.ownAlloc += fl.alloc
			}
			a.files++
			a.logical += fl.size
			a.alloc += fl.alloc
			a.cube.Add(cell, fl.alloc, 1)
			e := a.exts[ext]
			a.exts[ext] = [2]int64{e[0] + fl.alloc, e[1] + 1}
			if d == "" {
				break
			}
		}
		if !fl.linked {
			continue
		}
		key := [2]int64{fl.dev, fl.ino}
		if !seen[key] {
			seen[key] = true
			// Like the walk's first-arriving link: its values are the row's.
			must(t, w.AddSharedInode(ctx, gen.SharedInode{Dev: fl.dev, Ino: fl.ino, Size: fl.size, Alloc: fl.alloc, Mtime: f.mtime(fl.path)}))
		}
		must(t, w.AddSharedLink(ctx, gen.SharedLink{Dev: fl.dev, Ino: fl.ino, DirID: ids[parent(fl.path)], Name: []byte(name),
			Size: fl.size, Alloc: fl.alloc, Mtime: f.mtime(fl.path)}))
	}
	for _, d := range append([]string{""}, f.dirs...) {
		a := accs[d]
		var parentID int64
		if d != "" {
			parentID = ids[parent(d)]
		}
		must(t, w.AddDir(ctx, gen.Dir{
			ID: ids[d], ParentID: parentID,
			Name: []byte(base(d)), Dev: 1, Ino: ids[d], Mtime: fileMtime,
			OwnFiles: a.ownFiles, OwnAlloc: a.ownAlloc, Files: a.files, BytesLogical: a.logical, BytesAlloc: a.alloc,
		}))
		if a.files > 0 {
			blob, err := a.cube.Encode()
			must(t, err)
			must(t, w.AddCube(ctx, ids[d], blob))
		}
		for e, v := range a.exts {
			must(t, w.AddTopExt(ctx, gen.TopExt{DirID: ids[d], Ext: e, Bytes: v[0], Files: v[1]}))
		}
	}
	must(t, w.BuildIndexes(ctx))
	return w, path
}

// state is what a finished generation holds for one directory.
type state struct {
	OwnFiles, OwnAlloc, Files, Logical, Alloc int64
	Cube                                      map[cube.Cell][2]int64
	Exts                                      map[string][2]int64
}

type result struct {
	dirs    map[string]state
	inodes  map[[2]int64][4]int64 // size, alloc, mtime, cube_cell
	winners map[[2]int64]string   // winner_dir_id as a directory path
	links   []string              // "dev/ino dirpath name", sorted
}

func finish(t *testing.T, w *gen.Writer, path string) result {
	t.Helper()
	ctx := t.Context()
	must(t, w.Finish(ctx, gen.Meta{ScanID: 1, RootID: 1, ScannerVersion: "t", StartedAt: scanStart, FinishedAt: scanStart}))
	d, _, err := gen.OpenReadOnly(ctx, path)
	must(t, err)
	t.Cleanup(func() { _ = d.Close() })

	r := result{dirs: map[string]state{}, inodes: map[[2]int64][4]int64{}, winners: map[[2]int64]string{}}
	parents := map[int64]int64{}
	own := map[int64][]byte{}
	byID := map[int64]state{}
	rows, err := d.QueryContext(ctx, `SELECT id, parent_id, name, own_files, own_alloc, files, bytes_logical, bytes_alloc FROM dirs ORDER BY id`)
	must(t, err)
	for rows.Next() {
		var id, par int64
		var name []byte
		var s state
		must(t, rows.Scan(&id, &par, &name, &s.OwnFiles, &s.OwnAlloc, &s.Files, &s.Logical, &s.Alloc))
		parents[id], own[id], byID[id] = par, name, s
	}
	must(t, rows.Err())
	_ = rows.Close()
	var pathOf func(id int64) string
	pathOf = func(id int64) string {
		if id == 1 {
			return ""
		}
		if p := pathOf(parents[id]); p != "" {
			return p + "/" + string(own[id])
		}
		return string(own[id])
	}
	names := map[int64]string{}
	for id := range byID {
		names[id] = pathOf(id)
	}
	for id, s := range byID {
		s.Cube, s.Exts = map[cube.Cell][2]int64{}, map[string][2]int64{}
		var blob []byte
		switch err := d.QueryRowContext(ctx, `SELECT blob FROM cube WHERE dir_id = ?`, id).Scan(&blob); {
		case err == nil:
			a, err := cube.Decode(blob)
			must(t, err)
			for i, c := range a {
				if c.Bytes != 0 || c.Files != 0 {
					cell := cube.Cell{
						Size:     uint8(i % cube.NumSizes),                //nolint:gosec // < 6
						Age:      uint8(i / cube.NumSizes % cube.NumAges), //nolint:gosec // < 8
						Category: uint8(i / cube.NumSizes / cube.NumAges), //nolint:gosec // < 8
					}
					s.Cube[cell] = [2]int64{c.Bytes, c.Files}
				}
			}
		case errors.Is(err, sql.ErrNoRows):
		default:
			t.Fatal(err)
		}
		er, err := d.QueryContext(ctx, `SELECT ext, bytes, files FROM top_exts WHERE dir_id = ?`, id)
		must(t, err)
		for er.Next() {
			var e string
			var b, f int64
			must(t, er.Scan(&e, &b, &f))
			s.Exts[e] = [2]int64{b, f}
		}
		must(t, er.Err())
		_ = er.Close()
		r.dirs[names[id]] = s
	}
	ir, err := d.QueryContext(ctx, `SELECT dev, ino, size, alloc, mtime, cube_cell FROM shared_inodes`)
	must(t, err)
	for ir.Next() {
		var dev, ino, sz, al, mt, cc int64
		must(t, ir.Scan(&dev, &ino, &sz, &al, &mt, &cc))
		r.inodes[[2]int64{dev, ino}] = [4]int64{sz, al, mt, cc}
	}
	must(t, ir.Err())
	_ = ir.Close()
	wr, err := d.QueryContext(ctx, `SELECT dev, ino, winner_dir_id FROM shared_inodes`)
	must(t, err)
	for wr.Next() {
		var dev, ino, win int64
		must(t, wr.Scan(&dev, &ino, &win))
		p, ok := names[win]
		if !ok {
			p = fmt.Sprintf("<unknown dir %d>", win)
		}
		r.winners[[2]int64{dev, ino}] = p
	}
	must(t, wr.Err())
	_ = wr.Close()
	lr, err := d.QueryContext(ctx, `SELECT dev, ino, dir_id, name FROM shared_links`)
	must(t, err)
	for lr.Next() {
		var dev, ino, dir int64
		var name []byte
		must(t, lr.Scan(&dev, &ino, &dir, &name))
		r.links = append(r.links, fmt.Sprintf("%d/%d %s %s", dev, ino, names[dir], name))
	}
	must(t, lr.Err())
	_ = lr.Close()
	slices.Sort(r.links)
	return r
}

type cw struct {
	name         string
	bytes, files int64
}

type want struct {
	ownFiles, ownAlloc, files, logical, alloc int64
	cube                                      []cw
	exts                                      map[string][2]int64
}

func (w want) state() state {
	s := state{OwnFiles: w.ownFiles, OwnAlloc: w.ownAlloc, Files: w.files, Logical: w.logical, Alloc: w.alloc,
		Cube: map[cube.Cell][2]int64{}, Exts: map[string][2]int64{}}
	cats := cube.DefaultCategories()
	for _, c := range w.cube {
		cell := agg.CellFor([]byte(c.name), 0, fileMtime, scanStart, cats)
		v := s.Cube[cell]
		s.Cube[cell] = [2]int64{v[0] + c.bytes, v[1] + c.files}
	}
	for k, v := range w.exts {
		s.Exts[k] = v
	}
	return s
}

func cellIdx(name string) int64 {
	return int64(agg.CellFor([]byte(name), 0, fileMtime, scanStart, cube.DefaultCategories()).Index())
}

func checkDirs(t *testing.T, got result, wants map[string]want) {
	t.Helper()
	for p, w := range wants {
		g, ok := got.dirs[p]
		if !ok {
			t.Errorf("dir %q missing", p)
			continue
		}
		if ws := w.state(); !reflect.DeepEqual(g, ws) {
			t.Errorf("dir %q:\n got  %+v\n want %+v", p, g, ws)
		}
	}
	if len(got.dirs) != len(wants) {
		t.Errorf("got %d dirs, want %d", len(got.dirs), len(wants))
	}
	for p, s := range got.dirs {
		for _, v := range []int64{s.OwnFiles, s.OwnAlloc, s.Files, s.Logical, s.Alloc} {
			if v < 0 {
				t.Errorf("dir %q has a negative column: %+v", p, s)
			}
		}
	}
}

func run(t *testing.T, f fixture) (finalize.HardlinkStats, result) {
	t.Helper()
	w, path := f.write(t)
	st, err := finalize.Hardlinks(t.Context(), w, scanStart, cube.DefaultCategories())
	must(t, err)
	return st, finish(t, w, path)
}

func TestHardlinks_Cases(t *testing.T) {
	// Sanity: the extensions used below must land in different cube cells.
	if cellIdx("a.mp4") == cellIdx("a.txt") {
		t.Fatal("mp4 and txt share a cube cell; the cases below need different cells")
	}

	t.Run("SameDir", func(t *testing.T) {
		// d/x.mp4 and d/y.txt are two names of one inode in one directory: the
		// walk counted it twice there. The group is pruned from the shared
		// tables, but the loser (y.txt) must still be subtracted.
		st, r := run(t, fixture{dirs: []string{"d"}, files: []file{
			{"d/x.mp4", 1, 10, 100, 4096, true},
			{"d/y.txt", 1, 10, 100, 4096, true},
		}})
		one := want{files: 1, logical: 100, alloc: 4096, cube: []cw{{"x.mp4", 4096, 1}}, exts: map[string][2]int64{"mp4": {4096, 1}}}
		d := one
		d.ownFiles, d.ownAlloc = 1, 4096
		checkDirs(t, r, map[string]want{"": one, "d": d})
		if len(r.inodes) != 0 || len(r.links) != 0 {
			t.Errorf("shared tables not empty: %v %v", r.inodes, r.links)
		}
		if wantSt := (finalize.HardlinkStats{Inodes: 1, Links: 2, LosingLinks: 1, PrunedInodes: 1}); st != wantSt {
			t.Errorf("stats = %+v, want %+v", st, wantSt)
		}
	})

	t.Run("AcrossDirs", func(t *testing.T) {
		// (1,20) is linked from a and b; (2,20) has the same ino on another
		// device and is one single link in c: separate group, pruned.
		st, r := run(t, fixture{dirs: []string{"a", "b", "c"}, files: []file{
			{"a/f.mp4", 1, 20, 1000, 4096, true},
			{"b/f.txt", 1, 20, 1000, 4096, true},
			{"c/g.bin", 2, 20, 8, 4096, true},
		}})
		checkDirs(t, r, map[string]want{
			"": {files: 2, logical: 1008, alloc: 8192,
				cube: []cw{{"f.mp4", 4096, 1}, {"g.bin", 4096, 1}},
				exts: map[string][2]int64{"mp4": {4096, 1}, "bin": {4096, 1}}},
			"a": {ownFiles: 1, ownAlloc: 4096, files: 1, logical: 1000, alloc: 4096,
				cube: []cw{{"f.mp4", 4096, 1}}, exts: map[string][2]int64{"mp4": {4096, 1}}},
			"b": {},
			"c": {ownFiles: 1, ownAlloc: 4096, files: 1, logical: 8, alloc: 4096,
				cube: []cw{{"g.bin", 4096, 1}}, exts: map[string][2]int64{"bin": {4096, 1}}},
		})
		wantIn := map[[2]int64][4]int64{{1, 20}: {1000, 4096, fileMtime, cellIdx("f.mp4")}}
		if !reflect.DeepEqual(r.inodes, wantIn) {
			t.Errorf("shared_inodes = %v, want %v", r.inodes, wantIn)
		}
		if wantL := []string{"1/20 a f.mp4", "1/20 b f.txt"}; !slices.Equal(r.links, wantL) {
			t.Errorf("shared_links = %q, want %q", r.links, wantL)
		}
		if wantSt := (finalize.HardlinkStats{Inodes: 2, Links: 3, LosingLinks: 1, PrunedInodes: 1, SharedBytes: 4096}); st != wantSt {
			t.Errorf("stats = %+v, want %+v", st, wantSt)
		}
	})

	t.Run("ChainOfAncestors", func(t *testing.T) {
		// Winner p/q/x.mp4; losers p/y/z.txt and w/v.mp4. p holds the winner
		// and one loser, so it loses exactly one copy; root loses two.
		st, r := run(t, fixture{dirs: []string{"p", "p/q", "p/y", "w"}, files: []file{
			{"p/own.bin", 1, 5, 5, 4096, false},
			{"p/q/x.mp4", 1, 30, 10, 4096, true},
			{"p/y/z.txt", 1, 30, 10, 4096, true},
			{"w/v.mp4", 1, 30, 10, 4096, true},
		}})
		sub := []cw{{"own.bin", 4096, 1}, {"x.mp4", 4096, 1}}
		subExts := map[string][2]int64{"bin": {4096, 1}, "mp4": {4096, 1}}
		checkDirs(t, r, map[string]want{
			"":    {files: 2, logical: 15, alloc: 8192, cube: sub, exts: subExts},
			"p":   {ownFiles: 1, ownAlloc: 4096, files: 2, logical: 15, alloc: 8192, cube: sub, exts: subExts},
			"p/q": {ownFiles: 1, ownAlloc: 4096, files: 1, logical: 10, alloc: 4096, cube: []cw{{"x.mp4", 4096, 1}}, exts: map[string][2]int64{"mp4": {4096, 1}}},
			"p/y": {},
			"w":   {},
		})
		wantIn := map[[2]int64][4]int64{{1, 30}: {10, 4096, fileMtime, cellIdx("x.mp4")}}
		if !reflect.DeepEqual(r.inodes, wantIn) {
			t.Errorf("shared_inodes = %v, want %v", r.inodes, wantIn)
		}
		if len(r.links) != 3 {
			t.Errorf("shared_links = %q, want 3 rows", r.links)
		}
		if wantSt := (finalize.HardlinkStats{Inodes: 1, Links: 3, LosingLinks: 2, SharedBytes: 4096}); st != wantSt {
			t.Errorf("stats = %+v, want %+v", st, wantSt)
		}
	})

	t.Run("PathOrderIsComponentWise", func(t *testing.T) {
		// "a-b/f.bin" < "a/b/f.bin" as strings ('-' < '/'), but component
		// "a" < "a-b", so a/b/f.bin wins.
		st, r := run(t, fixture{dirs: []string{"a-b", "a", "a/b"}, files: []file{
			{"a-b/f.bin", 1, 40, 7, 4096, true},
			{"a/b/f.bin", 1, 40, 7, 4096, true},
		}})
		one := []cw{{"f.bin", 4096, 1}}
		oneExt := map[string][2]int64{"bin": {4096, 1}}
		checkDirs(t, r, map[string]want{
			"":    {files: 1, logical: 7, alloc: 4096, cube: one, exts: oneExt},
			"a":   {files: 1, logical: 7, alloc: 4096, cube: one, exts: oneExt},
			"a/b": {ownFiles: 1, ownAlloc: 4096, files: 1, logical: 7, alloc: 4096, cube: one, exts: oneExt},
			"a-b": {},
		})
		if len(r.links) != 2 || len(r.inodes) != 1 {
			t.Errorf("shared tables: %v %v", r.inodes, r.links)
		}
		if st.LosingLinks != 1 {
			t.Errorf("stats = %+v", st)
		}
	})

	t.Run("OtherNameOutsideRoot", func(t *testing.T) {
		// One link seen, nlink 2: a shared row from the walk, no loser.
		st, r := run(t, fixture{dirs: []string{"d"}, files: []file{{"d/f.mp4", 1, 60, 9, 4096, true}}})
		o := []cw{{"f.mp4", 4096, 1}}
		oe := map[string][2]int64{"mp4": {4096, 1}}
		checkDirs(t, r, map[string]want{
			"":  {files: 1, logical: 9, alloc: 4096, cube: o, exts: oe},
			"d": {ownFiles: 1, ownAlloc: 4096, files: 1, logical: 9, alloc: 4096, cube: o, exts: oe},
		})
		if len(r.inodes) != 0 || len(r.links) != 0 {
			t.Errorf("shared tables not empty: %v %v", r.inodes, r.links)
		}
		if wantSt := (finalize.HardlinkStats{Inodes: 1, Links: 1, PrunedInodes: 1}); st != wantSt {
			t.Errorf("stats = %+v, want %+v", st, wantSt)
		}
	})

	t.Run("EqualInoOnTwoDevices", func(t *testing.T) {
		st, r := run(t, fixture{dirs: []string{"a", "b"}, files: []file{
			{"a/f.bin", 1, 50, 3, 4096, true},
			{"b/f.bin", 2, 50, 3, 4096, true},
		}})
		o := []cw{{"f.bin", 4096, 1}}
		oe := map[string][2]int64{"bin": {4096, 1}}
		checkDirs(t, r, map[string]want{
			"":  {files: 2, logical: 6, alloc: 8192, cube: []cw{{"f.bin", 8192, 2}}, exts: map[string][2]int64{"bin": {8192, 2}}},
			"a": {ownFiles: 1, ownAlloc: 4096, files: 1, logical: 3, alloc: 4096, cube: o, exts: oe},
			"b": {ownFiles: 1, ownAlloc: 4096, files: 1, logical: 3, alloc: 4096, cube: o, exts: oe},
		})
		if len(r.inodes) != 0 || len(r.links) != 0 {
			t.Errorf("shared tables not empty: %v %v", r.inodes, r.links)
		}
		if wantSt := (finalize.HardlinkStats{Inodes: 2, Links: 2, PrunedInodes: 2}); st != wantSt {
			t.Errorf("stats = %+v, want %+v", st, wantSt)
		}
	})
}

func TestHardlinks_WinnerIndependentOfIDs(t *testing.T) {
	files := []file{
		{"a-b/f.bin", 1, 40, 7, 4096, true},
		{"a/b/f.bin", 1, 40, 7, 4096, true},
		{"p/q/x.mp4", 1, 30, 10, 4096, true},
		{"p/y/z.txt", 1, 30, 10, 4096, true},
		{"w/v.mp4", 1, 30, 10, 4096, true},
		{"w/own.bin", 1, 31, 10, 4096, false},
		{"z/same1", 1, 32, 1, 512, true},
		{"z/same0", 1, 32, 1, 512, true},
	}
	d1 := []string{"a-b", "a", "a/b", "p", "p/q", "p/y", "w", "z"}
	d2 := []string{"z", "w", "p", "a", "p/y", "a/b", "p/q", "a-b"} // parents still before children
	_, r1 := run(t, fixture{dirs: d1, files: files})
	_, r2 := run(t, fixture{dirs: d2, files: slices.Clone(files)})
	if !reflect.DeepEqual(r1.dirs, r2.dirs) {
		t.Errorf("dirs differ between id assignments:\n%+v\n%+v", r1.dirs, r2.dirs)
	}
	if !reflect.DeepEqual(r1.inodes, r2.inodes) || !slices.Equal(r1.links, r2.links) {
		t.Errorf("shared tables differ:\n%v %v\n%v %v", r1.inodes, r1.links, r2.inodes, r2.links)
	}
	// The same-dir pair in z is counted once.
	if z := r1.dirs["z"]; z.Files != 1 || z.OwnFiles != 1 || z.Alloc != 512 {
		t.Errorf("z = %+v", z)
	}
	if r1.dirs["a/b"].Files != 1 || r1.dirs["a-b"].Files != 0 || r1.dirs["p/q"].Files != 1 || r1.dirs["p/y"].Files != 0 || r1.dirs["w"].Files != 1 {
		t.Errorf("unexpected winners: %+v", r1.dirs)
	}
}

func TestHardlinks_PagingAndFlush(t *testing.T) {
	// 2,500 groups = three pages of 1,000; flushDirs 2 forces many flushes.
	defer finalize.SetFlushDirs(2)()
	var files []file
	for i := range 2500 {
		files = append(files,
			file{fmt.Sprintf("a/f%04d", i), 1, int64(1000 + i), 10, 4096, true},
			file{fmt.Sprintf("b/f%04d", i), 1, int64(1000 + i), 10, 4096, true})
	}
	st, r := run(t, fixture{dirs: []string{"a", "b"}, files: files})
	if a, b := r.dirs["a"], r.dirs["b"]; a.Files != 2500 || a.OwnFiles != 2500 || b.Files != 0 || b.Alloc != 0 || r.dirs[""].Files != 2500 {
		t.Errorf("a=%+v b=%+v root=%+v", a, b, r.dirs[""])
	}
	if len(r.inodes) != 2500 || len(r.links) != 5000 {
		t.Errorf("shared: %d inodes, %d links", len(r.inodes), len(r.links))
	}
	for k, v := range r.inodes {
		if v[3] != cellIdx("f0000") {
			t.Fatalf("inode %v cell = %d", k, v[3])
		}
	}
	if wantSt := (finalize.HardlinkStats{Inodes: 2500, Links: 5000, LosingLinks: 2500, SharedBytes: 2500 * 4096}); st != wantSt {
		t.Errorf("stats = %+v, want %+v", st, wantSt)
	}
}

// TestHardlinks_LinksWithDifferentStats: the links of one inode were stat'd at
// different moments and the inode changed in between, so each link carries
// its own size, allocation and mtime across a size bucket and an age bucket.
// Each loser must be subtracted by what the walk counted through it, in either
// arrival order of the links: nothing may go negative, the cube and extensions
// must be exact, and the shared_inodes row must describe the winning link.
func TestHardlinks_LinksWithDifferentStats(t *testing.T) {
	const mib = 1 << 20
	var (
		m20  = scanStart.Unix() - 20*86_400  // age bucket 1 (older than a week, within a month)
		m400 = scanStart.Unix() - 400*86_400 // age bucket 4 (older than a year, within two)
	)
	cats := cube.DefaultCategories()
	cellOf := func(name string, size, mtime int64) cube.Cell {
		return agg.CellFor([]byte(name), size, mtime, scanStart, cats)
	}
	// Winners by path: a/f.mp4 (100 B, bucket 0/0) and a/g.bin (20 MiB, 2/4).
	// Losers: b/f.txt (5 MiB, 1/1), c/f.bin (20 MiB, 2/4); b/g.txt (100 B, 0/0).
	if cellOf("x.txt", 100, fileMtime) == cellOf("x.txt", 5*mib, m20) || cellOf("x.txt", 5*mib, m20) == cellOf("x.txt", 20*mib, m400) {
		t.Fatal("the links must land in different cube cells")
	}
	files := []file{
		{"a/f.mp4", 1, 70, 100, 4096, true},
		{"b/f.txt", 1, 70, 5 * mib, 5 * mib, true},
		{"c/f.bin", 1, 70, 20 * mib, 20 * mib, true},
		{"a/g.bin", 1, 71, 20 * mib, 20 * mib, true},
		{"b/g.txt", 1, 71, 100, 4096, true},
	}
	mtimes := map[string]int64{"b/f.txt": m20, "c/f.bin": m400, "a/g.bin": m400}
	cF, cG := cellOf("f.mp4", 100, fileMtime), cellOf("g.bin", 20*mib, m400)
	total := state{
		OwnFiles: 0, Files: 2, Logical: 100 + 20*mib, Alloc: 4096 + 20*mib,
		Cube: map[cube.Cell][2]int64{cF: {4096, 1}, cG: {20 * mib, 1}},
		Exts: map[string][2]int64{"mp4": {4096, 1}, "bin": {20 * mib, 1}},
	}
	a := total
	a.OwnFiles, a.OwnAlloc = 2, 4096+20*mib
	wantDirs := map[string]state{"": total, "a": a, "b": {Cube: map[cube.Cell][2]int64{}, Exts: map[string][2]int64{}},
		"c": {Cube: map[cube.Cell][2]int64{}, Exts: map[string][2]int64{}}}
	wantInodes := map[[2]int64][4]int64{
		{1, 70}: {100, 4096, fileMtime, int64(cF.Index())},
		{1, 71}: {20 * mib, 20 * mib, m400, int64(cG.Index())},
	}

	rev := slices.Clone(files)
	slices.Reverse(rev)
	for name, order := range map[string][]file{"forward": files, "reversed": rev} {
		t.Run(name, func(t *testing.T) {
			st, r := run(t, fixture{dirs: []string{"a", "b", "c"}, files: order, mtimes: mtimes})
			if !reflect.DeepEqual(r.dirs, wantDirs) {
				t.Errorf("dirs:\n got  %+v\n want %+v", r.dirs, wantDirs)
			}
			for p, s := range r.dirs {
				for _, v := range []int64{s.OwnFiles, s.OwnAlloc, s.Files, s.Logical, s.Alloc} {
					if v < 0 {
						t.Errorf("dir %q has a negative column: %+v", p, s)
					}
				}
			}
			if !reflect.DeepEqual(r.inodes, wantInodes) {
				t.Errorf("shared_inodes = %v, want %v", r.inodes, wantInodes)
			}
			if len(r.links) != 5 {
				t.Errorf("shared_links = %q, want 5 rows", r.links)
			}
			if wantSt := (finalize.HardlinkStats{Inodes: 2, Links: 5, LosingLinks: 3, SharedBytes: 4096 + 20*mib}); st != wantSt {
				t.Errorf("stats = %+v, want %+v", st, wantSt)
			}
		})
	}
}

// TestHardlinks_WinnerDirID: shared_inodes.winner_dir_id is the directory of
// the winning link, in the Review Focus cases of TestHardlinks_Cases.
func TestHardlinks_WinnerDirID(t *testing.T) {
	t.Run("AcrossDirs", func(t *testing.T) {
		_, r := run(t, fixture{dirs: []string{"a", "b", "c"}, files: []file{
			{"a/f.mp4", 1, 20, 1000, 4096, true},
			{"b/f.txt", 1, 20, 1000, 4096, true},
			{"c/g.bin", 2, 20, 8, 4096, true},
		}})
		if want := map[[2]int64]string{{1, 20}: "a"}; !reflect.DeepEqual(r.winners, want) {
			t.Errorf("winners = %v, want %v", r.winners, want)
		}
	})
	t.Run("ChainOfAncestors", func(t *testing.T) {
		_, r := run(t, fixture{dirs: []string{"p", "p/q", "p/y", "w"}, files: []file{
			{"p/own.bin", 1, 5, 5, 4096, false},
			{"p/q/x.mp4", 1, 30, 10, 4096, true},
			{"p/y/z.txt", 1, 30, 10, 4096, true},
			{"w/v.mp4", 1, 30, 10, 4096, true},
		}})
		if want := map[[2]int64]string{{1, 30}: "p/q"}; !reflect.DeepEqual(r.winners, want) {
			t.Errorf("winners = %v, want %v", r.winners, want)
		}
	})
	t.Run("PathOrderIsComponentWise", func(t *testing.T) {
		_, r := run(t, fixture{dirs: []string{"a-b", "a", "a/b"}, files: []file{
			{"a-b/f.bin", 1, 40, 7, 4096, true},
			{"a/b/f.bin", 1, 40, 7, 4096, true},
		}})
		if want := map[[2]int64]string{{1, 40}: "a/b"}; !reflect.DeepEqual(r.winners, want) {
			t.Errorf("winners = %v, want %v", r.winners, want)
		}
	})
	t.Run("IndependentOfIDs", func(t *testing.T) {
		files := []file{
			{"x/f.bin", 1, 1, 1, 4096, true},
			{"y/f.bin", 1, 1, 1, 4096, true},
		}
		for _, dirs := range [][]string{{"x", "y"}, {"y", "x"}} {
			_, r := run(t, fixture{dirs: dirs, files: slices.Clone(files)})
			if want := map[[2]int64]string{{1, 1}: "x"}; !reflect.DeepEqual(r.winners, want) {
				t.Errorf("dirs %v: winners = %v, want %v", dirs, r.winners, want)
			}
		}
	})
	t.Run("DifferentStats", func(t *testing.T) {
		_, r := run(t, fixture{dirs: []string{"a", "b", "c"}, files: []file{
			{"c/f.bin", 1, 70, 20, 4096, true},
			{"b/f.txt", 1, 70, 5, 4096, true},
			{"a/f.mp4", 1, 70, 100, 4096, true},
		}})
		if want := map[[2]int64]string{{1, 70}: "a"}; !reflect.DeepEqual(r.winners, want) {
			t.Errorf("winners = %v, want %v", r.winners, want)
		}
	})
}
