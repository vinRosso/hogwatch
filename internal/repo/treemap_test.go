package repo

import (
	"fmt"
	"slices"
	"testing"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/gen/gentest"
)

func countNodes(n TreeNode) int {
	c := 1
	for _, k := range n.Children {
		c += countNodes(k)
	}
	return c
}

func treeNames(n TreeNode) []string {
	var out []string
	for _, c := range n.Children {
		switch c.Kind {
		case TreeFiles:
			out = append(out, "<files>")
		case TreeRest:
			out = append(out, "<rest>")
		case TreeExt:
			out = append(out, "."+string(c.Name))
		default:
			out = append(out, string(c.Name))
		}
	}
	return out
}

func TestTreemap_Shape(t *testing.T) {
	f := newFixture(t)
	f.scan(1, append(slices.Clone(kids), gentest.Dir{Rel: "p/z"}, gentest.Dir{Rel: "p/a/s", Files: 1, BytesAlloc: 3})...)
	root, err := f.repo.Treemap(t.Context(), ScopeAll(), f.ref(1, "p"), 1, cube.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if root.Kind != TreeDir || root.Ref == nil || string(root.Name) != "p" {
		t.Fatalf("root = %+v", root)
	}
	// Sizes: b50 c50 e40 a33 (30 + s3) d10 files20; z (0 bytes) is dropped.
	want := []string{"b", "c", "e", "a", "<files>", "d"}
	if got := treeNames(root); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("depth 1 children = %v, want %v", got, want)
	}
	for _, c := range root.Children {
		if len(c.Children) != 0 {
			t.Errorf("%q has children at depth 1", c.Name)
		}
	}
	root, err = f.repo.Treemap(t.Context(), ScopeAll(), f.ref(1, "p"), 2, cube.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var a TreeNode
	for _, c := range root.Children {
		if string(c.Name) == "a" {
			a = c
		}
	}
	if got := treeNames(a); fmt.Sprint(got) != "[<files> s]" {
		t.Errorf("depth 2: a children = %v", got)
	}
}

func TestTreemap_Cap(t *testing.T) {
	const n = 5000
	dirs := make([]gentest.Dir, 0, n)
	var total int64
	for i := 1; i <= n; i++ {
		dirs = append(dirs, gentest.Dir{Rel: fmt.Sprintf("p/d%05d", i), Files: 1, BytesAlloc: int64(i)})
		total += int64(i)
	}
	f := newFixture(t)
	f.scan(1, dirs...)
	root, err := f.repo.Treemap(t.Context(), ScopeAll(), f.ref(1, "p"), 3, cube.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := countNodes(root); got != TreemapMaxNodes {
		t.Fatalf("%d nodes, want %d", got, TreemapMaxNodes)
	}
	last := root.Children[len(root.Children)-1]
	if last.Kind != TreeRest || last.Ref != nil {
		t.Fatalf("last child = %+v, want a rest node", last)
	}
	var shown int64
	for _, c := range root.Children[:len(root.Children)-1] {
		shown += c.BytesAlloc
	}
	if last.BytesAlloc != total-shown {
		t.Errorf("rest = %d, want %d", last.BytesAlloc, total-shown)
	}
	// Largest first: the first child is the biggest directory.
	if root.Children[0].BytesAlloc != n {
		t.Errorf("first child = %d bytes, want %d", root.Children[0].BytesAlloc, n)
	}
}

func TestTreemap_Filter(t *testing.T) {
	f := newFixture(t)
	f.scan(1, kids...)
	vf := cube.Filter{Categories: 1 << cube.Video}
	root, err := f.repo.Treemap(t.Context(), ScopeAll(), f.ref(1, "p"), 1, vf)
	if err != nil {
		t.Fatal(err)
	}
	if got := treeNames(root); fmt.Sprint(got) != "[b e a <files>]" {
		t.Errorf("filtered children = %v", got)
	}
	if root.BytesAlloc != 115 {
		t.Errorf("filtered root = %d bytes, want 115", root.BytesAlloc)
	}
}

// sizes lists "name bytes/files" for each child, in order.
func sizes(n TreeNode) []string {
	var out []string
	for i, name := range treeNames(n) {
		out = append(out, fmt.Sprintf("%s %d/%d", name, n.Children[i].BytesAlloc, n.Children[i].Files))
	}
	return out
}

func TestTreemap_OwnFilesByExtension(t *testing.T) {
	// p's own files: mp4 50, jpg 15 and one without an extension (5).
	// p/a lists every extension it holds, so subtracting it from p is exact.
	p := gentest.Dir{Rel: "p", Files: 3, BytesAlloc: 70, Cells: map[cube.Cell][2]int64{{Category: uint8(cube.Other)}: {70, 3}},
		Exts: []gen.TopExt{{Ext: "mp4", Bytes: 50, Files: 1}, {Ext: "jpg", Bytes: 15, Files: 1}, {Ext: "", Bytes: 5, Files: 1}}}
	a := gentest.Dir{Rel: "p/a", Files: 2, BytesAlloc: 40, Exts: []gen.TopExt{
		{Ext: "mp4", Bytes: 30, Files: 1}, {Ext: "txt", Bytes: 10, Files: 1},
	}}
	opts := gentest.BuildOpts{ExtAnchors: []string{"p", "p/a"}}

	f := newFixture(t)
	f.scanWith(1, opts, p, a)
	root, err := f.repo.Treemap(t.Context(), ScopeAll(), f.ref(1, "p"), 1, cube.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(sizes(root)), "[.mp4 50/1 a 40/2 .jpg 15/1 . 5/1]"; got != want {
		t.Errorf("complete lists: %s, want %s", got, want)
	}

	// p/b holds a jpg (listed) and one more file it has no row for: only jpg
	// can be pinned down, the rest of p's own files is one generic block.
	b := gentest.Dir{Rel: "p/b", Files: 2, BytesAlloc: 25, Exts: []gen.TopExt{{Ext: "jpg", Bytes: 20, Files: 1}}}
	f = newFixture(t)
	f.scanWith(1, opts, p, a, b)
	root, err = f.repo.Treemap(t.Context(), ScopeAll(), f.ref(1, "p"), 1, cube.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(sizes(root)), "[<files> 55/2 a 40/2 b 25/2 .jpg 15/1]"; got != want {
		t.Errorf("incomplete list: %s, want %s", got, want)
	}

	// Under a filter top_exts does not apply: one block.
	root, err = f.repo.Treemap(t.Context(), ScopeAll(), f.ref(1, "p"), 1, cube.Filter{Categories: 1 << cube.Other})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(sizes(root)), "[<files> 70/3]"; got != want {
		t.Errorf("filtered: %s, want %s", got, want)
	}
}

func TestTreemap_Scope(t *testing.T) {
	f := newFixture(t)
	f.scan(1, kids...)
	u := f.user("u")
	f.path(u, "p/a")
	if _, err := f.repo.Treemap(t.Context(), f.scope(u, 1), f.ref(1, "p"), 1, cube.Filter{}); err != ErrNotFound {
		t.Errorf("Treemap above the anchor: %v", err)
	}
}
