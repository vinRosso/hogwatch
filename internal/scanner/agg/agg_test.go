package agg

import (
	"bytes"
	"cmp"
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
)

func mkFile(name string, dirIno, ino uint64, size, mtime int64) File {
	return File{
		Name: []byte(name), ParentIno: dirIno, Ino: ino, Size: size, Alloc: size, Mtime: mtime,
		Cell: cube.Cell{Category: uint8(cube.Other)}, Ext: string(cube.Ext([]byte(name))),
	}
}

func sumCube(a *cube.Acc) (bytes, files int64) {
	for _, c := range a {
		bytes += c.Bytes
		files += c.Files
	}
	return
}

func TestNode_AddMerge(t *testing.T) {
	l := DefaultLimits()
	root := NewNode(l)
	root.AddFile(mkFile("r1", 1, 10, 100, 5))
	root.AddFile(mkFile("r2.txt", 1, 11, 200, 6))

	c1 := NewNode(l)
	c1.AddFile(mkFile("a", 2, 20, 1000, 7))
	g := NewNode(l)
	g.AddFile(mkFile("g", 3, 30, 50, 8))
	g.AddError()
	c1.Merge(g)
	c2 := NewNode(l)
	c2.AddFile(mkFile("b.txt", 4, 40, 7, 9))
	c2.AddSkippedDir()
	root.Merge(c1)
	root.Merge(c2)

	if root.OwnFiles != 2 || root.OwnAlloc != 300 {
		t.Errorf("own = %d/%d", root.OwnFiles, root.OwnAlloc)
	}
	if root.Files != 5 || root.BytesAlloc != 1357 || root.BytesLogical != 1357 {
		t.Errorf("files=%d alloc=%d logical=%d", root.Files, root.BytesAlloc, root.BytesLogical)
	}
	// c1 + g + c2 + c2's skipped dir
	if root.Dirs != 4 {
		t.Errorf("Dirs = %d, want 4", root.Dirs)
	}
	if root.Errors != 1 {
		t.Errorf("Errors = %d", root.Errors)
	}
	b, f := sumCube(&root.Cube)
	if f != root.Files || b != root.BytesAlloc {
		t.Errorf("cube sums %d/%d vs %d/%d", f, b, root.Files, root.BytesAlloc)
	}
	if got := len(root.Largest()); got != 5 {
		t.Errorf("Largest len = %d", got)
	}
}

func randFiles(r *rand.Rand, n int) []File {
	fs := make([]File, n)
	for i := range fs {
		// small ranges force ties on every key
		fs[i] = mkFile(fmt.Sprintf("n%d", r.Intn(6)), r.Uint64()%4, uint64(i)+1, int64(r.Intn(8)), int64(r.Intn(8)))
	}
	return fs
}

func wantTop(fs []File, rank func(a, b *File) int, n int) []File {
	s := slices.Clone(fs)
	slices.SortFunc(s, func(a, b File) int { return rank(&a, &b) })
	return s[:min(n, len(s))]
}

func sameFiles(a, b []File) bool {
	return slices.EqualFunc(a, b, func(x, y File) bool {
		return bytes.Equal(x.Name, y.Name) && x.ParentIno == y.ParentIno && x.Ino == y.Ino &&
			x.Size == y.Size && x.Mtime == y.Mtime
	})
}

func TestTop_OrderAndBound(t *testing.T) {
	// The order is pinned independently of the implementation.
	largest := func(a, b *File) int {
		return cmp.Or(cmp.Compare(b.Size, a.Size), bytes.Compare(a.Name, b.Name),
			cmp.Compare(a.ParentIno, b.ParentIno), cmp.Compare(a.Ino, b.Ino))
	}
	oldest := func(a, b *File) int {
		return cmp.Or(cmp.Compare(a.Mtime, b.Mtime), cmp.Compare(b.Size, a.Size), bytes.Compare(a.Name, b.Name),
			cmp.Compare(a.ParentIno, b.ParentIno), cmp.Compare(a.Ino, b.Ino))
	}
	for seed := int64(0); seed < 100; seed++ {
		r := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic test data, not security
		fs := randFiles(r, 200)
		l := DefaultLimits()

		direct := NewNode(l)
		for _, f := range fs {
			direct.AddFile(f)
		}
		if got, want := direct.Largest(), wantTop(fs, largest, 50); len(got) != 50 || !sameFiles(got, want) {
			t.Fatalf("seed %d: Largest mismatch", seed)
		}
		if got, want := direct.Oldest(), wantTop(fs, oldest, 50); len(got) != 50 || !sameFiles(got, want) {
			t.Fatalf("seed %d: Oldest mismatch", seed)
		}

		// random split across children merged in random order
		parts := make([]*Node, 1+r.Intn(6))
		for i := range parts {
			parts[i] = NewNode(l)
		}
		for _, f := range fs {
			parts[r.Intn(len(parts))].AddFile(f)
		}
		r.Shuffle(len(parts), func(i, j int) { parts[i], parts[j] = parts[j], parts[i] })
		merged := NewNode(l)
		for _, p := range parts {
			merged.Merge(p)
		}
		if !sameFiles(merged.Largest(), direct.Largest()) || !sameFiles(merged.Oldest(), direct.Oldest()) {
			t.Fatalf("seed %d: merged differs from direct", seed)
		}
	}
}

func TestTop_NameCopied(t *testing.T) {
	n := NewNode(DefaultLimits())
	buf := []byte("abc")
	f := mkFile("x", 1, 1, 1, 1)
	f.Name = buf
	n.AddFile(f)
	buf[0] = 'z'
	if string(n.Largest()[0].Name) != "abc" {
		t.Fatal("kept name aliases the caller's buffer")
	}
}

func TestTop_TiesDeterministic(t *testing.T) {
	n := NewNode(DefaultLimits())
	n.AddFile(mkFile("same", 9, 1, 10, 1))
	n.AddFile(mkFile("same", 3, 2, 10, 1))
	n.AddFile(mkFile("same", 3, 1, 10, 1))
	for _, got := range [][]File{n.Largest(), n.Oldest()} {
		if got[0].ParentIno != 3 || got[0].Ino != 1 || got[1].ParentIno != 3 || got[1].Ino != 2 || got[2].ParentIno != 9 {
			t.Fatalf("tie order wrong: %+v", got)
		}
	}
}

func TestTopExts(t *testing.T) {
	l := DefaultLimits()
	l.TopExts = 3
	n := NewNode(l)
	var ino uint64
	add := func(name string, size int64) { ino++; n.AddFile(mkFile(name, 1, ino, size, 1)) }
	add("a.zz", 10)
	add("b.aa", 10)  // ties with zz: aa first
	add("noext", 10) // "" is a valid key and sorts first on ties
	add("c.big", 100)
	add("d.small", 1)
	got := n.TopExts()
	want := []ExtTotal{{"big", 100, 1}, {"", 10, 1}, {"aa", 10, 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	child := NewNode(l)
	child.AddFile(mkFile("e.zz", 1, 99, 5, 1))
	n.Merge(child)
	if got := n.TopExts()[1]; got != (ExtTotal{"zz", 15, 2}) {
		t.Fatalf("after merge: %+v", got)
	}
}

func TestQualifies(t *testing.T) {
	l := DefaultLimits()
	for _, tc := range []struct {
		depth, alloc int64
		want         bool
	}{
		{5, 1 << 30, true}, {6, 1 << 30, false}, {5, 1<<30 - 1, false}, {0, 1 << 40, true},
	} {
		if got := l.Qualifies(tc.depth, tc.alloc); got != tc.want {
			t.Errorf("Qualifies(%d,%d) = %v", tc.depth, tc.alloc, got)
		}
	}
}

func TestCellFor(t *testing.T) {
	start := time.Unix(2_000_000_000, 0)
	got := CellFor([]byte("a.MKV"), 5<<20, start.Unix()-3600, start, cube.DefaultCategories())
	want := cube.Cell{Category: uint8(cube.Video), Age: 0, Size: 1}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
