package repo

import (
	"fmt"
	"runtime"
	"slices"
	"testing"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen/gentest"
)

var (
	cVideo = cube.Cell{Category: uint8(cube.Video)}
	cImage = cube.Cell{Category: uint8(cube.Image)}
	cDoc   = cube.Cell{Category: uint8(cube.Document)}
)

// kids has sort orders that all differ:
//
//	size:  b(50) c(50) e(40) a(30) d(10)   files: a5 d4 c3 e2 b1
//	mtime: e d a c b                       name:  a b c d e
var kids = []gentest.Dir{
	{Rel: "p", Files: 2, BytesAlloc: 20, BytesLogical: 19, Mtime: 900,
		Cells: map[cube.Cell][2]int64{cVideo: {5, 1}, cDoc: {15, 1}}},
	{Rel: "p/b", Files: 1, BytesAlloc: 50, BytesLogical: 50, Mtime: 100, Cells: map[cube.Cell][2]int64{cVideo: {50, 1}}},
	{Rel: "p/a", Files: 5, BytesAlloc: 30, BytesLogical: 30, Mtime: 300, Cells: map[cube.Cell][2]int64{cVideo: {20, 2}, cImage: {10, 3}}},
	{Rel: "p/c", Files: 3, BytesAlloc: 50, BytesLogical: 50, Mtime: 200, Cells: map[cube.Cell][2]int64{cImage: {50, 3}}},
	{Rel: "p/d", Files: 4, BytesAlloc: 10, BytesLogical: 10, Mtime: 400, Cells: map[cube.Cell][2]int64{cImage: {10, 4}}},
	{Rel: "p/e", Files: 2, BytesAlloc: 40, BytesLogical: 40, Mtime: 500, Cells: map[cube.Cell][2]int64{cVideo: {40, 2}}},
	{Rel: "p/a/s", Files: 0},
}

func names(items []Child) []string {
	var out []string
	for _, c := range items {
		if c.Kind == ChildFiles {
			out = append(out, "<files>")
			continue
		}
		out = append(out, string(c.Name))
	}
	return out
}

func page(t *testing.T, f *fixture, s Scope, rel string, q ChildQuery) ([]Child, *ChildCursor) {
	t.Helper()
	items, next, err := f.repo.Children(t.Context(), s, f.ref(1, rel), q)
	if err != nil {
		t.Fatalf("Children(%q, %+v): %v", rel, q, err)
	}
	return items, next
}

func TestChildren_Sorts(t *testing.T) {
	f := newFixture(t)
	f.scan(1, kids...)
	for _, c := range []struct {
		sort ChildSort
		want []string
	}{
		{SortSize, []string{"<files>", "b", "c", "e", "a", "d"}},
		{SortFiles, []string{"<files>", "a", "d", "c", "e", "b"}},
		{SortMtime, []string{"<files>", "e", "d", "a", "c", "b"}},
		{SortName, []string{"<files>", "a", "b", "c", "d", "e"}},
	} {
		items, next := page(t, f, ScopeAll(), "p", ChildQuery{Sort: c.sort, Limit: 100})
		if got := names(items); !slices.Equal(got, c.want) || next != nil {
			t.Errorf("sort %d = %v (next %v), want %v", c.sort, got, next, c.want)
		}
	}
}

func TestChildren_Paging(t *testing.T) {
	f := newFixture(t)
	f.scan(1, kids...)
	for _, sort := range []ChildSort{SortSize, SortFiles, SortMtime, SortName} {
		full, _ := page(t, f, ScopeAll(), "p", ChildQuery{Sort: sort, Limit: 100})
		var got []string
		var after *ChildCursor
		for i := 0; ; i++ {
			if i > 10 {
				t.Fatalf("sort %d does not end", sort)
			}
			items, next := page(t, f, ScopeAll(), "p", ChildQuery{Sort: sort, Limit: 2, After: after})
			got = append(got, names(items)...)
			if next == nil {
				break
			}
			after = next
		}
		if want := names(full); !slices.Equal(got, want) {
			t.Errorf("sort %d paged = %v, want %v", sort, got, want)
		}
	}
}

func TestChildren_FilesEntry(t *testing.T) {
	f := newFixture(t)
	f.scan(1, kids...)
	items, next := page(t, f, ScopeAll(), "p", ChildQuery{Sort: SortName, Limit: 1})
	if len(items) != 2 || items[0].Kind != ChildFiles || items[0].Ref != nil || len(items[0].Name) != 0 {
		t.Fatalf("first page = %+v", items)
	}
	own := items[0]
	if own.Files != 2 || own.BytesAlloc != 20 || own.Mtime != 900 || own.BytesLogical == nil || *own.BytesLogical != 19 {
		t.Errorf("own-files entry = %+v", own)
	}
	items, _ = page(t, f, ScopeAll(), "p", ChildQuery{Sort: SortName, Limit: 1, After: next})
	if len(items) != 1 || items[0].Kind != ChildDir {
		t.Errorf("second page = %+v", items)
	}
	// A directory with no own files has no such entry (p/a/s holds none).
	items, _ = page(t, f, ScopeAll(), "p/a/s", ChildQuery{Sort: SortName, Limit: 10})
	if got := names(items); !slices.Equal(got, []string{}) {
		t.Errorf("p/a/s children = %v", got)
	}
}

func TestChildren_Filter(t *testing.T) {
	f := newFixture(t)
	f.scan(1, kids...)
	vf := cube.Filter{Categories: 1 << cube.Video}
	items, _ := page(t, f, ScopeAll(), "p", ChildQuery{Sort: SortSize, Filter: vf, Limit: 100})
	if got, want := names(items), []string{"<files>", "b", "e", "a"}; !slices.Equal(got, want) {
		t.Fatalf("filtered size order = %v, want %v", got, want)
	}
	for _, c := range items {
		if c.BytesLogical != nil {
			t.Errorf("%q has bytes_logical under a filter", c.Name)
		}
	}
	if own := items[0]; own.BytesAlloc != 5 || own.Files != 1 {
		t.Errorf("filtered own-files = %+v, want 5 bytes, 1 file", own)
	}
	if a := items[3]; a.BytesAlloc != 20 || a.Files != 2 {
		t.Errorf("filtered a = %+v, want 20 bytes, 2 files", a)
	}
	items, _ = page(t, f, ScopeAll(), "p", ChildQuery{Sort: SortFiles, Filter: vf, Limit: 100})
	if got, want := names(items), []string{"<files>", "a", "e", "b"}; !slices.Equal(got, want) {
		t.Errorf("filtered files order = %v, want %v", got, want)
	}
	// Nothing matches: no entries at all.
	none := cube.Filter{Categories: 1 << cube.Archive}
	if items, next := page(t, f, ScopeAll(), "p", ChildQuery{Sort: SortSize, Filter: none, Limit: 100}); len(items) != 0 || next != nil {
		t.Errorf("empty filter result = %+v", items)
	}
}

func TestChildren_Scope(t *testing.T) {
	f := newFixture(t)
	f.scan(1, kids...)
	u := f.user("u")
	f.path(u, "p/a")
	s := f.scope(u, 1)
	if _, _, err := f.repo.Children(t.Context(), s, f.ref(1, "p"), ChildQuery{Limit: 10}); err != ErrNotFound {
		t.Errorf("Children of an ancestor of the anchor: %v", err)
	}
	items, _ := page(t, f, s, "p/a", ChildQuery{Sort: SortName, Limit: 10})
	if got := names(items); !slices.Equal(got, []string{"<files>", "s"}) {
		t.Errorf("anchor children = %v", got)
	}
	if _, _, err := f.repo.Children(t.Context(), Scope{}, f.ref(1, "p"), ChildQuery{Limit: 10}); err != ErrNotFound {
		t.Errorf("zero scope: %v", err)
	}
}

func TestChildren_HugeDir(t *testing.T) {
	if testing.Short() {
		t.Skip("100k directories")
	}
	const n = 100_000
	dirs := make([]gentest.Dir, 0, n)
	for i := range n {
		dirs = append(dirs, gentest.Dir{Rel: fmt.Sprintf("big/d%06d", i), Files: int64(i % 7), BytesAlloc: int64(i * 7919 % 1000)})
	}
	f := newFixture(t)
	f.scan(1, dirs...)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for _, sort := range []ChildSort{SortSize, SortName} {
		seen := make(map[string]bool, n)
		var cur *ChildCursor
		for pages := 0; ; pages++ {
			if pages > n/500+3 {
				t.Fatalf("sort %d: too many pages", sort)
			}
			items, next := page(t, f, ScopeAll(), "big", ChildQuery{Sort: sort, Limit: 500, After: cur})
			for _, c := range items {
				if c.Kind != ChildDir {
					continue
				}
				if seen[string(c.Name)] {
					t.Fatalf("sort %d: %q twice", sort, c.Name)
				}
				seen[string(c.Name)] = true
			}
			if next == nil {
				break
			}
			cur = next
		}
		if len(seen) != n {
			t.Fatalf("sort %d visited %d of %d children", sort, len(seen), n)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc+64<<20 {
		t.Errorf("heap grew from %d to %d MiB", before.HeapAlloc>>20, after.HeapAlloc>>20)
	}
}

// Pages of one entry put every tie (b and c at 50 bytes) on a page boundary,
// with and without a filter.
func TestChildren_PagingFilteredAndTies(t *testing.T) {
	f := newFixture(t)
	f.scan(1, append(slices.Clone(kids), gentest.Dir{Rel: "p/\xff\xfe"}, gentest.Dir{Rel: "p/\xff"})...)
	for _, flt := range []cube.Filter{{}, {Categories: 1 << cube.Video}, {Categories: 1<<cube.Video | 1<<cube.Image}} {
		for _, sort := range []ChildSort{SortSize, SortFiles, SortMtime, SortName} {
			full, _ := page(t, f, ScopeAll(), "p", ChildQuery{Sort: sort, Filter: flt, Limit: 100})
			var got []string
			var after *ChildCursor
			for i := 0; ; i++ {
				if i > 20 {
					t.Fatalf("filter %v sort %d does not end", flt, sort)
				}
				items, next := page(t, f, ScopeAll(), "p", ChildQuery{Sort: sort, Filter: flt, Limit: 1, After: after})
				got = append(got, names(items)...)
				if next == nil {
					break
				}
				after = next
			}
			if want := names(full); !slices.Equal(got, want) {
				t.Errorf("filter %v sort %d paged = %q, want %q", flt, sort, got, want)
			}
		}
	}
	// Names compare as raw bytes: "\xff" sorts before "\xff\xfe".
	items, _ := page(t, f, ScopeAll(), "p", ChildQuery{Sort: SortName, Limit: 100})
	got := names(items)
	if n := len(got); got[n-2] != "\xff" || got[n-1] != "\xff\xfe" {
		t.Errorf("name order = %q", got)
	}
}
