package agg

import (
	"slices"
	"strings"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
)

// Limits are the top-N storage rule.
type Limits struct {
	TopN        int   // files kept per kind (largest, oldest)
	TopMaxDepth int64 // deepest depth_anchor that carries top lists
	TopMinAlloc int64 // smallest subtree bytes_alloc that carries top lists
	TopExts     int   // extension totals kept
}

// DefaultLimits returns N=50, depth 5, 1 GiB, 20 extensions.
func DefaultLimits() Limits {
	return Limits{TopN: 50, TopMaxDepth: 5, TopMinAlloc: 1 << 30, TopExts: 20}
}

// Qualifies reports whether a directory gets top lists: depthAnchor is at most
// TopMaxDepth and its subtree bytesAlloc is at least TopMinAlloc.
func (l Limits) Qualifies(depthAnchor, bytesAlloc int64) bool {
	return depthAnchor <= l.TopMaxDepth && bytesAlloc >= l.TopMinAlloc
}

// ExtTotal is the allocated bytes and file count of one extension.
type ExtTotal struct {
	Ext          string
	Bytes, Files int64
}

// Node accumulates one directory's subtree until its children finish. Own*
// cover the directory's direct files only; the rest cover the whole subtree.
type Node struct {
	OwnFiles, OwnAlloc                            int64
	Files, Dirs, BytesLogical, BytesAlloc, Errors int64
	Cube                                          cube.Acc

	lim     Limits
	largest topHeap
	oldest  topHeap
	exts    map[string]*ExtTotal
}

// NewNode returns an empty accumulator bounded by l.
func NewNode(l Limits) *Node {
	return &Node{
		lim:     l,
		largest: topHeap{rank: rankLargest},
		oldest:  topHeap{rank: rankOldest},
	}
}

// AddFile counts one direct file of this directory. Bytes in the cube and the
// extension totals are allocated bytes. f.Name is copied if it is kept.
func (n *Node) AddFile(f File) {
	n.OwnFiles++
	n.OwnAlloc += f.Alloc
	n.Files++
	n.BytesLogical += f.Size
	n.BytesAlloc += f.Alloc
	n.Cube.Add(f.Cell, f.Alloc, 1)
	n.largest.offer(&f, n.lim.TopN)
	n.oldest.offer(&f, n.lim.TopN)
	n.addExt(f.Ext, f.Alloc, 1)
}

func (n *Node) addExt(ext string, bytes, files int64) {
	if n.exts == nil {
		n.exts = make(map[string]*ExtTotal)
	}
	e := n.exts[ext]
	if e == nil {
		e = &ExtTotal{Ext: ext}
		n.exts[ext] = e
	}
	e.Bytes += bytes
	e.Files += files
}

// AddError counts one error in this directory.
func (n *Node) AddError() { n.Errors++ }

// AddSkippedDir counts a flagged child directory that was not walked.
func (n *Node) AddSkippedDir() { n.Dirs++ }

// Merge folds a finished child into n. The child's heaps and extension map are
// drained and must not be used afterwards.
func (n *Node) Merge(child *Node) {
	n.Files += child.Files
	n.Dirs += child.Dirs + 1
	n.BytesLogical += child.BytesLogical
	n.BytesAlloc += child.BytesAlloc
	n.Errors += child.Errors
	n.Cube.Merge(&child.Cube)
	n.largest.absorb(&child.largest, n.lim.TopN)
	n.oldest.absorb(&child.oldest, n.lim.TopN)
	for _, e := range child.exts {
		n.addExt(e.Ext, e.Bytes, e.Files)
	}
	child.exts = nil
}

// Largest returns the kept largest files in rank order.
func (n *Node) Largest() []File { return n.largest.sorted() }

// Oldest returns the kept oldest files in rank order.
func (n *Node) Oldest() []File { return n.oldest.sorted() }

// TopExts returns up to TopExts extension totals, bytes desc then ext asc.
func (n *Node) TopExts() []ExtTotal {
	out := make([]ExtTotal, 0, len(n.exts))
	for _, e := range n.exts {
		out = append(out, *e)
	}
	slices.SortFunc(out, func(a, b ExtTotal) int {
		if a.Bytes != b.Bytes {
			if a.Bytes > b.Bytes {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Ext, b.Ext)
	})
	if len(out) > n.lim.TopExts {
		out = out[:max(n.lim.TopExts, 0)]
	}
	return out
}

// CellFor places a file in the cube. mtime is Unix seconds; the category comes
// from cats, the size bucket from size (logical bytes).
func CellFor(name []byte, size, mtime int64, scanStart time.Time, cats map[string]cube.Category) cube.Cell {
	return cube.Cell{
		Category: uint8(cube.CategoryOf(name, cats)),
		Age:      cube.AgeBucket(time.Unix(mtime, 0), scanStart),
		Size:     cube.SizeBucket(size),
	}
}

// ExtKeys is the number of distinct extensions held right now: the size of the
// map that grows with the variety of names in the subtree.
func (n *Node) ExtKeys() int { return len(n.exts) }
