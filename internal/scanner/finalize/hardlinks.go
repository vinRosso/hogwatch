package finalize

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
)

// pageGroups is the number of shared inodes loaded per page.
const pageGroups = 1000

// flushDirs is the number of directories with pending deltas that triggers
// one ApplyDeltas call; tests lower it through export_test.go.
var flushDirs = 50_000

// ComparePaths orders two paths component-wise: each component by
// bytes.Compare, a proper prefix before its extensions. It is not
// the order of the joined strings: "a-b/f" > "a/b/f" although '-' < '/'.
func ComparePaths(a, b [][]byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := bytes.Compare(a[i], b[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// HardlinkStats summarises one Hardlinks run. Inodes and Links count what the
// walk recorded; LosingLinks are the links subtracted; PrunedInodes are the
// inodes dropped from the shared tables (links in fewer than two directories);
// SharedBytes is the allocated size of the inodes that remain, each once.
type HardlinkStats struct {
	Inodes       int64 `json:"inodes"`
	Links        int64 `json:"links"`
	LosingLinks  int64 `json:"losing_links"`
	PrunedInodes int64 `json:"pruned_inodes"`
	SharedBytes  int64 `json:"shared_bytes"`
}

// pending is the accumulated change of one directory. Cube cells and
// extensions are kept in maps so the memory of a huge flush batch is bounded
// by what the losing links touch, not by 384 cells per directory.
type pending struct {
	delta gen.DirDelta
	cells map[cube.Cell]*[2]int64
	exts  map[string]*[2]int64
}

type dirInfo struct {
	parent int64
	name   []byte
}

type linkPath struct {
	chain []int64  // directory ids, the link's own directory first, root last
	comps [][]byte // names from below the root down to the link's name
}

type finalizer struct {
	w         *gen.Writer
	scanStart time.Time
	cats      map[string]cube.Category
	cache     map[int64]dirInfo
	pend      map[int64]*pending
}

// Hardlinks makes every multiply-linked inode count once per root (the
// "subtract" model). The walk counted each link in its own directory; here, for
// every (dev, ino) group the link with the smallest path wins and each other
// link's size, allocation, file count, cube cell and extension are subtracted
// from its directory (own and subtree columns) and from every ancestor.
// Winners depend only on names, never on ids. Groups whose links span fewer
// than two distinct directories are then pruned from the shared tables; a
// same-directory pair is still subtracted because the walk counted both names.
//
// It reads and writes only the generation behind w, between BuildIndexes and
// Finish.
func Hardlinks(ctx context.Context, w *gen.Writer, scanStart time.Time, cats map[string]cube.Category) (HardlinkStats, error) {
	f := &finalizer{w: w, scanStart: scanStart, cats: cats, pend: map[int64]*pending{}}
	var st HardlinkStats
	dev, ino := int64(math.MinInt64), int64(math.MinInt64)
	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		groups, err := w.LinkGroupsAfter(ctx, dev, ino, pageGroups)
		if err != nil {
			return st, err
		}
		if len(groups) == 0 {
			break
		}
		f.cache = map[int64]dirInfo{} // bounded per page
		var cells []gen.SharedInode
		for i := range groups {
			g := &groups[i]
			win, survives, err := f.group(ctx, g, &st)
			if err != nil {
				return st, err
			}
			if survives {
				st.SharedBytes += win.Alloc
				cells = append(cells, win)
			}
			if len(f.pend) >= flushDirs {
				if err := f.flush(ctx); err != nil {
					return st, err
				}
			}
		}
		// Only groups that survive pruning get a cell, and they are set now,
		// before PruneUnshared: afterwards the pruned rows would be unknown.
		if len(cells) > 0 {
			if err := w.SetSharedCells(ctx, cells); err != nil {
				return st, err
			}
		}
		last := groups[len(groups)-1]
		dev, ino = last.Dev, last.Ino
		if len(groups) < pageGroups {
			break
		}
	}
	if err := f.flush(ctx); err != nil {
		return st, err
	}
	pruned, err := w.PruneUnshared(ctx)
	if err != nil {
		return st, err
	}
	st.PrunedInodes = pruned
	return st, nil
}

// group subtracts the losing links of one inode, each by the values the walk
// counted through that link, and reports the winner as the shared_inodes row
// it should leave (the winning link's own size, allocation, mtime and cube
// cell: nothing here depends on which link the writer saw first) and whether
// the group spans at least two directories.
func (f *finalizer) group(ctx context.Context, g *gen.LinkGroup, st *HardlinkStats) (inode gen.SharedInode, survives bool, err error) {
	st.Inodes++
	st.Links += int64(len(g.Links))
	if len(g.Links) == 0 {
		return gen.SharedInode{}, false, nil
	}
	paths := make([]linkPath, len(g.Links))
	win := 0
	dirs := map[int64]struct{}{}
	for i, l := range g.Links {
		if paths[i], err = f.path(ctx, l); err != nil {
			return gen.SharedInode{}, false, err
		}
		dirs[l.DirID] = struct{}{}
		if ComparePaths(paths[i].comps, paths[win].comps) < 0 {
			win = i
		}
	}
	for i, l := range g.Links {
		if i == win {
			continue
		}
		st.LosingLinks++
		f.subtract(l, paths[i].chain)
	}
	w := g.Links[win]
	winCell := agg.CellFor(w.Name, w.Size, w.Mtime, f.scanStart, f.cats)
	return gen.SharedInode{Dev: g.Dev, Ino: g.Ino, Size: w.Size, Alloc: w.Alloc, Mtime: w.Mtime,
		CubeCell: int64(winCell.Index()), WinnerDirID: w.DirID}, len(dirs) >= 2, nil
}

// path resolves a link to its ancestor chain and its component path.
func (f *finalizer) path(ctx context.Context, l gen.SharedLink) (linkPath, error) {
	var p linkPath
	var names [][]byte
	for id := l.DirID; ; {
		info, ok := f.cache[id]
		if !ok {
			parent, name, err := f.w.DirInfo(ctx, id)
			if err != nil {
				return linkPath{}, fmt.Errorf("hardlinks: path of link %q: %w", l.Name, err)
			}
			info = dirInfo{parent: parent, name: name}
			f.cache[id] = info
		}
		p.chain = append(p.chain, id)
		if info.parent == 0 { // the scan root: its name is not part of the path
			break
		}
		if len(p.chain) > 1<<20 {
			return linkPath{}, fmt.Errorf("hardlinks: directory %d: parent chain does not end", l.DirID)
		}
		names = append(names, info.name)
		id = info.parent
	}
	slices.Reverse(names)
	p.comps = append(names, l.Name)
	return p, nil
}

// subtract records the removal of one losing link from its directory and every
// ancestor, by the size, allocation and mtime the walk counted through l.
func (f *finalizer) subtract(l gen.SharedLink, chain []int64) {
	cell := agg.CellFor(l.Name, l.Size, l.Mtime, f.scanStart, f.cats)
	ext := cube.Ext(l.Name)
	for i, id := range chain {
		p := f.pend[id]
		if p == nil {
			p = &pending{delta: gen.DirDelta{DirID: id}, cells: map[cube.Cell]*[2]int64{}, exts: map[string]*[2]int64{}}
			f.pend[id] = p
		}
		if i == 0 {
			p.delta.OwnFiles--
			p.delta.OwnAlloc -= l.Alloc
		}
		p.delta.Files--
		p.delta.BytesLogical -= l.Size
		p.delta.BytesAlloc -= l.Alloc
		c := p.cells[cell]
		if c == nil {
			c = new([2]int64)
			p.cells[cell] = c
		}
		c[0] -= l.Alloc
		c[1]--
		e := p.exts[ext]
		if e == nil {
			e = new([2]int64)
			p.exts[ext] = e
		}
		e[0] -= l.Alloc
		e[1]--
	}
}

// flush applies the pending deltas in directory order and clears them.
func (f *finalizer) flush(ctx context.Context) error {
	if len(f.pend) == 0 {
		return nil
	}
	deltas := make([]gen.DirDelta, 0, len(f.pend))
	for _, p := range f.pend {
		d := p.delta
		for c, v := range p.cells {
			d.Cells = append(d.Cells, gen.CellDelta{Cell: c, Bytes: v[0], Files: v[1]})
		}
		for e, v := range p.exts {
			d.Exts = append(d.Exts, gen.ExtDelta{Ext: e, Bytes: v[0], Files: v[1]})
		}
		deltas = append(deltas, d)
	}
	slices.SortFunc(deltas, func(a, b gen.DirDelta) int {
		switch {
		case a.DirID < b.DirID:
			return -1
		case a.DirID > b.DirID:
			return 1
		}
		return 0
	})
	f.pend = map[int64]*pending{}
	return f.w.ApplyDeltas(ctx, deltas)
}
