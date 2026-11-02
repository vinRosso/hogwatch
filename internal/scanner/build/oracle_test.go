//go:build linux

package build_test

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"github.com/vinRosso/hogwatch/internal/scanner/finalize"
)

// The oracle recounts a tree with filepath.WalkDir and os.Lstat and its own
// hardlink dedupe (finalize.ComparePaths only orders paths). It shares no
// code with the walk, agg or the generation writer beyond the cube bucket
// functions, and it works on paths: a directory is its path below the root
// ("" for the root, names joined by "/", which no name can contain).

type oFile struct {
	dir       string // path of the containing directory
	name      string
	comps     [][]byte // path components below the root, the name last
	size      int64
	alloc     int64
	mtime     int64
	uid       int64
	dev, ino  uint64
	nlink     uint64
	parentIno uint64
	ext       string
	cell      int
	lost      bool // a losing hardlink: subtracted from the totals
	symlink   bool
}

type oDir struct {
	path       string
	depth      int64
	dev, ino   uint64
	mtime      int64
	uid, gid   int64
	unreadable bool
	hasSymlink bool

	// Subtree totals, before (w*) and after hardlink subtraction.
	wFiles, wLogical, wAlloc int64
	ownFiles, ownAlloc       int64
	files, logical, alloc    int64
	dirs                     int64
	errors                   int64
	cube                     map[int][2]int64 // true cube: cell -> bytes, files
	wExts, exts              map[string][2]int64
}

type oracle struct {
	t      *testing.T
	dirs   map[string]*oDir
	files  []*oFile
	groups map[[2]uint64][]*oFile
}

func parentOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

func splitComps(p string) [][]byte {
	if p == "" {
		return nil
	}
	var out [][]byte
	for _, s := range strings.Split(p, "/") {
		out = append(out, []byte(s))
	}
	return out
}

func depthOf(p string) int64 {
	if p == "" {
		return 0
	}
	return int64(strings.Count(p, "/")) + 1
}

func newOracle(t *testing.T, root string) *oracle {
	t.Helper()
	o := &oracle{t: t, dirs: map[string]*oDir{}, groups: map[[2]uint64][]*oFile{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		if err != nil {
			if d != nil && d.IsDir() && o.dirs[rel] != nil {
				o.dirs[rel].unreadable = true // ReadDir failed: the directory is a leaf with an error
				return nil
			}
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st := fi.Sys().(*syscall.Stat_t)
		if fi.IsDir() {
			o.dirs[rel] = &oDir{
				path: rel, depth: depthOf(rel), dev: st.Dev, ino: st.Ino, mtime: fi.ModTime().Unix(),
				uid: int64(st.Uid), gid: int64(st.Gid),
				cube: map[int][2]int64{}, wExts: map[string][2]int64{}, exts: map[string][2]int64{},
			}
			return nil
		}
		dir := parentOf(rel)
		f := &oFile{
			dir: dir, name: fi.Name(), comps: append(splitComps(dir), []byte(fi.Name())),
			size: fi.Size(), alloc: st.Blocks * 512, mtime: fi.ModTime().Unix(), uid: int64(st.Uid),
			dev: st.Dev, ino: st.Ino, nlink: st.Nlink, symlink: fi.Mode()&fs.ModeSymlink != 0,
		}
		f.cell = cellIndex([]byte(f.name), f.size, f.mtime)
		f.ext = cube.Ext([]byte(f.name))
		o.files = append(o.files, f)
		return nil
	})
	if err != nil {
		t.Fatalf("oracle walk: %v", err)
	}
	for _, f := range o.files {
		f.parentIno = o.dirs[f.dir].ino
		if f.nlink > 1 {
			k := [2]uint64{f.dev, f.ino}
			o.groups[k] = append(o.groups[k], f)
		}
	}
	o.dedupe()
	o.total()
	return o
}

// dedupe marks every link of an inode but the one with the smallest path lost.
func (o *oracle) dedupe() {
	for _, g := range o.groups {
		win := g[0]
		for _, f := range g[1:] {
			if finalize.ComparePaths(f.comps, win.comps) < 0 {
				win = f
			}
		}
		for _, f := range g {
			f.lost = f != win
		}
	}
}

// total fills the per-directory totals, walk-time and final.
func (o *oracle) total() {
	for _, d := range o.dirs {
		if d.unreadable {
			d.errors = 1
		}
	}
	for _, f := range o.files {
		if f.symlink {
			o.dirs[f.dir].hasSymlink = true
		}
		for p := f.dir; ; p = parentOf(p) {
			d := o.dirs[p]
			d.wFiles++
			d.wLogical += f.size
			d.wAlloc += f.alloc
			add(d.wExts, f.ext, f.alloc, 1)
			if !f.lost {
				d.files++
				d.logical += f.size
				d.alloc += f.alloc
				add(d.exts, f.ext, f.alloc, 1)
				c := d.cube[f.cell]
				d.cube[f.cell] = [2]int64{c[0] + f.alloc, c[1] + 1}
				if p == f.dir {
					d.ownFiles++
					d.ownAlloc += f.alloc
				}
			}
			if p == "" {
				break
			}
		}
	}
	for p, d := range o.dirs {
		for q := p; q != ""; {
			q = parentOf(q)
			o.dirs[q].dirs++
			if d.unreadable {
				o.dirs[q].errors++
			}
		}
	}
}

func add(m map[string][2]int64, k string, bytes, files int64) {
	v := m[k]
	m[k] = [2]int64{v[0] + bytes, v[1] + files}
}

// flags is the expected dirs.flags of a directory.
func (d *oDir) flags() int64 {
	switch {
	case d.unreadable:
		return gen.FlagUnreadable
	case d.hasSymlink:
		return gen.FlagSymlink
	}
	return 0
}

// subtree lists the files below (and in) dir p, walk-time: every link.
func (o *oracle) subtree(p string) []*oFile {
	var out []*oFile
	for _, f := range o.files {
		if p == "" || f.dir == p || strings.HasPrefix(f.dir, p+"/") {
			out = append(out, f)
		}
	}
	return out
}

func (f *oFile) largestBefore(g *oFile) int {
	return cmp.Or(cmp.Compare(g.size, f.size), strings.Compare(f.name, g.name),
		cmp.Compare(f.parentIno, g.parentIno), cmp.Compare(f.ino, g.ino))
}

func (f *oFile) oldestBefore(g *oFile) int {
	return cmp.Or(cmp.Compare(f.mtime, g.mtime), cmp.Compare(g.size, f.size), strings.Compare(f.name, g.name),
		cmp.Compare(f.parentIno, g.parentIno), cmp.Compare(f.ino, g.ino))
}

// qualifies is the walk-time top-N rule. The walk decides it from the
// pre-subtraction allocation, which only matters for a size floor; the oracle
// fixture has none.
func (d *oDir) qualifies(l agg.Limits) bool {
	return !d.unreadable && d.depth <= l.TopMaxDepth && d.wAlloc >= l.TopMinAlloc
}

// compareToOracle checks a generation against the recount of root. exactExts
// asserts that top_exts lists every extension of every qualifying directory
// (true when the limit exceeds the number of extensions).
func compareToOracle(t *testing.T, g *genData, root string, lim agg.Limits) {
	t.Helper()
	o := newOracle(t, root)
	bad := 0
	fail := func(format string, args ...any) {
		t.Helper()
		if bad++; bad <= 40 {
			t.Errorf(format, args...)
		}
	}

	// dirs rows: same set of paths, every column.
	if len(g.dirs) != len(o.dirs) {
		fail("%d dirs rows, oracle has %d dirs", len(g.dirs), len(o.dirs))
	}
	for p, od := range o.dirs {
		r := g.byPath[p]
		if r == nil {
			fail("dir %q: no row", p)
			continue
		}
		want := dirCols{
			Depth: od.depth, Dev: i64(od.dev), Ino: i64(od.ino), Mtime: od.mtime, UID: od.uid, GID: od.gid,
			OwnFiles: od.ownFiles, OwnAlloc: od.ownAlloc, Files: od.files, Dirs: od.dirs,
			Logical: od.logical, Alloc: od.alloc, Flags: od.flags(), ErrCount: od.errors,
		}
		if od.unreadable {
			want.OwnFiles, want.OwnAlloc, want.Files, want.Logical, want.Alloc = 0, 0, 0, 0, 0
		}
		if got := r.cols(); got != want {
			fail("dir %q:\n got %+v\nwant %+v", p, got, want)
		}
		// cube
		acc, err := cube.Decode(g.cubes[p])
		if err != nil {
			fail("dir %q: cube: %v", p, err)
			continue
		}
		for i, c := range acc {
			w := od.cube[i]
			if od.unreadable {
				w = [2]int64{}
			}
			if c.Bytes != w[0] || c.Files != w[1] {
				fail("dir %q cube cell %d: got %d bytes %d files, want %d / %d", p, i, c.Bytes, c.Files, w[0], w[1])
			}
		}
	}

	// top_files: every link of the subtree is a candidate (walk-time), the
	// qualifying directories only.
	wantTop := map[string]dbTop{}
	for p, od := range o.dirs {
		if !od.qualifies(lim) {
			continue
		}
		sub := o.subtree(p)
		for _, k := range []struct {
			kind   string
			before func(a, b *oFile) int
		}{{"largest", (*oFile).largestBefore}, {"oldest", (*oFile).oldestBefore}} {
			s := slices.Clone(sub)
			slices.SortFunc(s, k.before)
			for i, f := range s[:min(lim.TopN, len(s))] {
				wantTop[fmt.Sprintf("%q|%s|%d", p, k.kind, i+1)] = dbTop{
					Anchor: p, Parent: f.dir, Kind: k.kind, Rank: int64(i + 1), Name: []byte(f.name),
					Size: f.size, Alloc: f.alloc, Mtime: f.mtime, UID: f.uid,
				}
			}
		}
	}
	gotTop := map[string]dbTop{}
	for _, r := range g.tops {
		gotTop[fmt.Sprintf("%q|%s|%d", r.Anchor, r.Kind, r.Rank)] = r
	}
	if len(gotTop) != len(g.tops) {
		fail("duplicate top_files keys")
	}
	for k, w := range wantTop {
		r, ok := gotTop[k]
		if !ok {
			fail("top_files: missing %s", k)
		} else if !bytes.Equal(r.Name, w.Name) || r.Parent != w.Parent || r.Size != w.Size || r.Alloc != w.Alloc ||
			r.Mtime != w.Mtime || r.UID != w.UID {
			fail("top_files %s:\n got %+v\nwant %+v", k, r, w)
		}
	}
	for k := range gotTop {
		if _, ok := wantTop[k]; !ok {
			fail("top_files: unexpected %s", k)
		}
	}

	// top_exts. The walk keeps the TopExts largest extensions of a directory
	// by its walk-time totals; hardlink subtraction then lowers (or, at 0/0,
	// deletes) the rows that exist, but never promotes an extension that did
	// not make the cut. So the stored rows are an exact-valued SUBSET of the
	// true per-directory totals, and are the whole of them whenever the
	// directory had no more extensions than the limit at walk time.
	perDir := map[string]map[string]dbExt{}
	for _, r := range g.exts {
		if perDir[r.Dir] == nil {
			perDir[r.Dir] = map[string]dbExt{}
		}
		if _, dup := perDir[r.Dir][r.Ext]; dup {
			fail("top_exts: duplicate row %q %q", r.Dir, r.Ext)
		}
		perDir[r.Dir][r.Ext] = r
	}
	for p, rows := range perDir {
		od := o.dirs[p]
		if od == nil || !od.qualifies(lim) {
			fail("top_exts rows for non-qualifying dir %q", p)
			continue
		}
		if len(rows) > lim.TopExts {
			fail("top_exts: dir %q has %d rows, limit %d", p, len(rows), lim.TopExts)
		}
		for ext, r := range rows {
			w, ok := od.exts[ext]
			if !ok || r.Bytes != w[0] || r.Files != w[1] {
				fail("top_exts %q %q: got %d bytes %d files, true totals %v (present %v)", p, ext, r.Bytes, r.Files, w, ok)
			}
		}
	}
	for p, od := range o.dirs {
		if !od.qualifies(lim) || len(od.wExts) > lim.TopExts {
			continue
		}
		for ext, w := range od.exts { // every extension survived the walk-time cut
			if w[0] == 0 && w[1] == 0 {
				continue
			}
			if _, ok := perDir[p][ext]; !ok {
				fail("top_exts: dir %q lacks extension %q (true %v) though it had only %d extensions", p, ext, w, len(od.wExts))
			}
		}
	}

	// Ordering: a stored extension never has fewer walk-time bytes than an
	// extension that was dropped (ties are free). Extensions whose true total is
	// zero were stored then deleted by subtraction, so they are not "dropped".
	for p, od := range o.dirs {
		if !od.qualifies(lim) || len(od.wExts) <= lim.TopExts {
			continue
		}
		minKept, maxDropped := int64(-1), int64(-1)
		for ext, w := range od.wExts {
			if _, kept := perDir[p][ext]; kept {
				if minKept < 0 || w[0] < minKept {
					minKept = w[0]
				}
			} else if _, live := od.exts[ext]; live {
				maxDropped = max(maxDropped, w[0])
			}
		}
		if minKept >= 0 && maxDropped > minKept {
			fail("top_exts: dir %q kept an extension with %d walk-time bytes but dropped one with %d", p, minKept, maxDropped)
		}
	}

	// Shared tables: inodes linked from at least two directories.
	wantInodes := map[[2]int64]dbInode{}
	wantLinks := map[string]bool{}
	for _, grp := range o.groups {
		dirs := map[string]bool{}
		win := grp[0]
		for _, f := range grp {
			dirs[f.dir] = true
			if !f.lost {
				win = f
			}
		}
		if len(dirs) < 2 {
			continue
		}
		wantInodes[[2]int64{i64(win.dev), i64(win.ino)}] = dbInode{
			Dev: i64(win.dev), Ino: i64(win.ino), Size: win.size, Alloc: win.alloc, Mtime: win.mtime,
			Cell: int64(win.cell),
		}
		for _, f := range grp {
			wantLinks[fmt.Sprintf("%d|%d|%q|%q|%d|%d|%d", f.dev, f.ino, f.dir, f.name, f.size, f.alloc, f.mtime)] = true
		}
	}
	gotInodes := map[[2]int64]dbInode{}
	for _, r := range g.inodes {
		gotInodes[[2]int64{r.Dev, r.Ino}] = r
	}
	if len(gotInodes) != len(wantInodes) {
		fail("%d shared inodes, oracle has %d", len(gotInodes), len(wantInodes))
	}
	for k, w := range wantInodes {
		if r, ok := gotInodes[k]; !ok || r != w {
			fail("shared inode %v:\n got %+v (present %v)\nwant %+v", k, r, ok, w)
		}
	}
	gotLinks := map[string]bool{}
	for _, r := range g.links {
		gotLinks[fmt.Sprintf("%d|%d|%q|%q|%d|%d|%d", r.Dev, r.Ino, r.Dir, r.Name, r.Size, r.Alloc, r.Mtime)] = true
	}
	for k := range wantLinks {
		if !gotLinks[k] {
			fail("shared_links: missing %s", k)
		}
	}
	for k := range gotLinks {
		if !wantLinks[k] {
			fail("shared_links: unexpected %s", k)
		}
	}

	// errors: one row per unreadable directory, keyed (child, "", open).
	var wantErrs, gotErrs []string
	for p, od := range o.dirs {
		if od.unreadable {
			wantErrs = append(wantErrs, fmt.Sprintf("%q|%q|open|%d", p, "", int64(syscall.EACCES)))
		}
	}
	for _, r := range g.errs {
		gotErrs = append(gotErrs, fmt.Sprintf("%q|%q|%s|%d", r.Dir, r.Name, r.Op, r.Errno))
	}
	slices.Sort(wantErrs)
	slices.Sort(gotErrs)
	if !slices.Equal(wantErrs, gotErrs) {
		fail("errors rows:\n got %q\nwant %q", gotErrs, wantErrs)
	}
}

func TestRun_MatchesOracle(t *testing.T) {
	root := makeFixture(t)
	for _, c := range []struct {
		name string
		lim  agg.Limits
	}{
		// Four extensions: smaller than the root's, so the walk-time cut and
		// the subset rule are exercised.
		{"topexts=4", testLimits},
		// Fifty: every extension is kept, so top_exts must equal the recount.
		{"topexts=50", agg.Limits{TopN: 3, TopMaxDepth: 5, TopMinAlloc: 0, TopExts: 50}},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := runOK(t, opts(t.TempDir(), root, 4, c.lim))
			if err := build.Verify(context.Background(), res.Path); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			g := readGen(t, res.Path)
			compareToOracle(t, g, root, c.lim)
			if os.Geteuid() != 0 {
				if r := g.byPath["locked"]; r == nil || r.Flags != gen.FlagUnreadable || r.ErrCount != 1 {
					t.Errorf("locked row = %+v", r)
				}
			}
			// The fixture is only a test of the oracle if it holds what it
			// claims to.
			if len(g.inodes) < 4 || len(g.exts) == 0 || len(g.tops) == 0 {
				t.Errorf("fixture too thin: %d shared inodes, %d exts, %d tops", len(g.inodes), len(g.exts), len(g.tops))
			}
			if len(g.byPath["hostile/D"+"/"+hostileNames[1]].Name) == 0 {
				t.Errorf("hostile directory name %q not stored", hostileNames[1])
			}
		})
	}
}

// dirCols are the comparable columns of a dirs row (pre and post are checked
// by Verify, ids and names by the path they build).
type dirCols struct {
	Depth, Dev, Ino, Mtime, UID, GID                int64
	OwnFiles, OwnAlloc, Files, Dirs, Logical, Alloc int64
	Flags, ErrCount                                 int64
}

func (r *dbDir) cols() dirCols {
	return dirCols{r.Depth, r.Dev, r.Ino, r.Mtime, r.UID, r.GID,
		r.OwnFiles, r.OwnAlloc, r.Files, r.Dirs, r.Logical, r.Alloc, r.Flags, r.ErrCount}
}

// i64 bit-casts a stat value, as the generation stores it.
func i64(u uint64) int64 { return int64(u) } //nolint:gosec // bit-cast by design
