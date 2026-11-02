package agg

import (
	"bytes"
	"cmp"
	"container/heap"
	"slices"

	"github.com/vinRosso/hogwatch/internal/cube"
)

// File is one regular file as seen by the scanner. Mtime is Unix seconds.
type File struct {
	Name      []byte
	DirID     int64
	ParentIno uint64
	Ino       uint64
	Size      int64
	Alloc     int64
	Mtime     int64
	UID       int64
	Cell      cube.Cell
	Ext       string
}

// rankLargest orders files by size desc, then name asc (bytes), parent-dir ino
// asc, ino asc. A negative result means a ranks before b.
func rankLargest(a, b *File) int {
	if c := cmp.Compare(b.Size, a.Size); c != 0 {
		return c
	}
	return tieBreak(a, b)
}

// rankOldest orders files by mtime asc, then size desc, then the same tie-break.
func rankOldest(a, b *File) int {
	if c := cmp.Compare(a.Mtime, b.Mtime); c != 0 {
		return c
	}
	if c := cmp.Compare(b.Size, a.Size); c != 0 {
		return c
	}
	return tieBreak(a, b)
}

func tieBreak(a, b *File) int {
	if c := bytes.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	if c := cmp.Compare(a.ParentIno, b.ParentIno); c != 0 {
		return c
	}
	return cmp.Compare(a.Ino, b.Ino)
}

// topHeap keeps the best n files under rank. It is a min-heap of the inverse
// order: the root is the worst kept file, so it is the one evicted.
type topHeap struct {
	rank  func(a, b *File) int
	items []File
}

func (h *topHeap) Len() int           { return len(h.items) }
func (h *topHeap) Less(i, j int) bool { return h.rank(&h.items[j], &h.items[i]) < 0 }
func (h *topHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *topHeap) Push(x any)         { h.items = append(h.items, x.(File)) }
func (h *topHeap) Pop() any {
	n := len(h.items) - 1
	f := h.items[n]
	h.items[n] = File{}
	h.items = h.items[:n]
	return f
}

// offer adds f if it ranks within the best n. The name is copied only when f
// is kept, so the caller may reuse its buffer.
func (h *topHeap) offer(f *File, n int) {
	if n <= 0 {
		return
	}
	if len(h.items) >= n {
		if h.rank(f, &h.items[0]) >= 0 {
			return
		}
		kept := *f
		kept.Name = bytes.Clone(f.Name)
		h.items[0] = kept
		heap.Fix(h, 0)
		return
	}
	kept := *f
	kept.Name = bytes.Clone(f.Name)
	heap.Push(h, kept)
}

// absorb moves every file of o into h; o is emptied. Names are already owned.
func (h *topHeap) absorb(o *topHeap, n int) {
	for _, f := range o.items {
		if n <= 0 {
			break
		}
		if len(h.items) >= n {
			if h.rank(&f, &h.items[0]) >= 0 {
				continue
			}
			h.items[0] = f
			heap.Fix(h, 0)
			continue
		}
		heap.Push(h, f)
	}
	o.items = nil
}

// sorted returns the kept files in rank order.
func (h *topHeap) sorted() []File {
	out := slices.Clone(h.items)
	slices.SortFunc(out, func(a, b File) int { return h.rank(&a, &b) })
	return out
}
