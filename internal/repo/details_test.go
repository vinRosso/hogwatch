package repo

import (
	"fmt"
	"slices"
	"testing"

	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/gen/gentest"
	"github.com/vinRosso/hogwatch/internal/totals"
)

var detailTree = []gentest.Dir{
	{Rel: "p",
		Exts:   []gen.TopExt{{Ext: "bin", Bytes: 5, Files: 1}, {Ext: "txt", Bytes: 900, Files: 2}, {Ext: "zzz", Bytes: 1, Files: 1}},
		Errors: []gen.ErrorRow{{Name: nil, Op: "readdir", Errno: 13}}},
	{Rel: "p/a",
		Top: []gentest.TopFile{
			{Kind: "largest", Rank: 1, Name: "a.bin", Size: 900, Alloc: 1000, Mtime: 5},
			{Kind: "oldest", Rank: 1, Name: "a.old", Size: 1, Alloc: 4, Mtime: 1}},
		Errors: []gen.ErrorRow{{Name: []byte("z"), Op: "lstat", Errno: 5}}},
	{Rel: "p/b", Top: []gentest.TopFile{{Kind: "largest", Rank: 2, Name: "b\xff.bin", Size: 800, Alloc: 900, Mtime: 6}},
		Errors: []gen.ErrorRow{{Name: nil, Op: "readdir", Errno: 7}}},
	{Rel: "p/b/c", Errors: []gen.ErrorRow{{Name: []byte("x"), Op: "open", Errno: 2}}},
	{Rel: "q",
		Top:    []gentest.TopFile{{Kind: "largest", Rank: 3, Name: "q.bin", Size: 700, Alloc: 800, Mtime: 7}},
		Exts:   []gen.TopExt{{Ext: "q", Bytes: 1, Files: 1}},
		Errors: []gen.ErrorRow{{Name: []byte("qq"), Op: "lstat", Errno: 99}}},
}

func detailFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.scanWith(1, gentest.BuildOpts{TopAnchors: []string{"", "p"}}, detailTree...)
	return f
}

func TestTop(t *testing.T) {
	f := detailFixture(t)
	items, stored, err := f.repo.Top(t.Context(), ScopeAll(), f.ref(1, "p"), "largest")
	if err != nil || !stored || len(items) != 2 {
		t.Fatalf("Top(p, largest) = %+v, %v, %v", items, stored, err)
	}
	if items[0].Rank != 1 || string(items[0].Name) != "a.bin" || items[0].DirRef != f.ref(1, "p/a") || items[0].Size != 900 || items[0].Alloc != 1000 || items[0].Mtime != 5 {
		t.Errorf("first = %+v", items[0])
	}
	if items[1].Rank != 2 || string(items[1].Name) != "b\xff.bin" || items[1].DirRef != f.ref(1, "p/b") {
		t.Errorf("second = %+v", items[1])
	}
	items, stored, err = f.repo.Top(t.Context(), ScopeAll(), f.ref(1, "p"), "oldest")
	if err != nil || !stored || len(items) != 1 || string(items[0].Name) != "a.old" {
		t.Errorf("Top(p, oldest) = %+v, %v, %v", items, stored, err)
	}
	// A node without stored rows says so.
	items, stored, err = f.repo.Top(t.Context(), ScopeAll(), f.ref(1, "p/a"), "largest")
	if err != nil || stored || len(items) != 0 {
		t.Errorf("Top(p/a) = %+v, %v, %v; want not stored", items, stored, err)
	}
	if _, _, err := f.repo.Top(t.Context(), ScopeAll(), f.ref(1, "p"), "bogus"); err == nil || err == ErrNotFound {
		t.Errorf("Top(bogus kind) err = %v, want a validation error", err)
	}
	u := f.user("u")
	f.path(u, "q")
	if _, _, err := f.repo.Top(t.Context(), f.scope(u, 1), f.ref(1, "p"), "largest"); err != ErrNotFound {
		t.Errorf("Top out of scope: %v", err)
	}
	// The root anchor lists q's file for an admin, and q's owner sees only
	// what is under q (nothing is stored at q itself).
	items, _, err = f.repo.Top(t.Context(), ScopeAll(), f.ref(1, ""), "largest")
	if err != nil || len(items) != 3 {
		t.Errorf("Top(root) = %+v, %v", items, err)
	}
	if _, _, err := f.repo.Top(t.Context(), f.scope(u, 1), f.ref(1, ""), "largest"); err != ErrNotFound {
		t.Errorf("Top(root) for a q-only user: %v", err)
	}
}

func TestExtensions(t *testing.T) {
	f := detailFixture(t)
	f.exec(`UPDATE file_categories SET category = 'document' WHERE ext = 'txt'`)
	got, err := f.repo.Extensions(t.Context(), ScopeAll(), f.ref(1, "p"))
	if err != nil {
		t.Fatal(err)
	}
	want := []ExtStat{
		{Ext: "txt", Category: "document", Files: 2, Bytes: 900},
		{Ext: "bin", Category: "other", Files: 1, Bytes: 5},
		{Ext: "zzz", Category: "other", Files: 1, Bytes: 1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Extensions(p) = %+v, want %+v", got, want)
	}
	got, err = f.repo.Extensions(t.Context(), ScopeAll(), f.ref(1, "p/a"))
	if err != nil || len(got) != 0 {
		t.Errorf("Extensions(p/a) = %+v, %v; want none", got, err)
	}
	u := f.user("u")
	f.path(u, "q")
	if _, err := f.repo.Extensions(t.Context(), f.scope(u, 1), f.ref(1, "p")); err != ErrNotFound {
		t.Errorf("Extensions out of scope: %v", err)
	}
}

func errNames(items []ErrorItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, fmt.Sprintf("%s:%d", it.RelName, it.Errno))
	}
	return out
}

func TestErrors(t *testing.T) {
	f := detailFixture(t)
	items, next, err := f.repo.Errors(t.Context(), ScopeAll(), f.ref(1, "p"), 0, 10)
	if err != nil || next != 0 {
		t.Fatalf("Errors(p) = %+v, next %d, %v", items, next, err)
	}
	// The directory-level error reports the node itself (empty); q's error is
	// not below p.
	if got, want := errNames(items), []string{":13", "a/z:5", "b:7", "b/c/x:2"}; !slices.Equal(got, want) {
		t.Errorf("Errors(p) = %v, want %v", got, want)
	}
	var all []string
	var after int64
	for i := 0; ; i++ {
		if i > 5 {
			t.Fatal("paging does not end")
		}
		page, n, err := f.repo.Errors(t.Context(), ScopeAll(), f.ref(1, "p"), after, 2)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, errNames(page)...)
		if n == 0 {
			break
		}
		after = n
	}
	if want := []string{":13", "a/z:5", "b:7", "b/c/x:2"}; !slices.Equal(all, want) {
		t.Errorf("paged errors = %v, want %v", all, want)
	}
	u := f.user("u")
	f.path(u, "p/a")
	s := f.scope(u, 1)
	if _, _, err := f.repo.Errors(t.Context(), s, f.ref(1, "p"), 0, 10); err != ErrNotFound {
		t.Errorf("Errors above the anchor: %v", err)
	}
	items, _, err = f.repo.Errors(t.Context(), s, f.ref(1, "p/a"), 0, 10)
	if got, want := errNames(items), []string{"z:5"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("Errors(anchor) = %v, %v; want %v", got, err, want)
	}
}

func TestHistory(t *testing.T) {
	f := detailFixture(t)
	f.exec(`INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'r2', '/mnt/r2', '/srv/r2')`)
	hp, hq := totals.PathHash(1, []byte("p")), totals.PathHash(1, []byte("q"))
	for _, row := range []struct {
		ts, root, hash, bytes int64
	}{{100, 1, hp, 10}, {200, 1, hp, 20}, {300, 1, hp, 30}, {250, 1, hq, 99}, {260, 2, hp, 77}} {
		f.exec(`INSERT INTO history_dirs (ts, root_id, path_hash, bytes_alloc, files, tier) VALUES (?, ?, ?, ?, 1, 'raw')`,
			row.ts, row.root, row.hash, row.bytes)
	}
	got, err := f.repo.History(t.Context(), ScopeAll(), f.ref(1, "p"), 150)
	want := []HistoryPoint{{TS: 200, BytesAlloc: 20, Files: 1}, {TS: 300, BytesAlloc: 30, Files: 1}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("History(p) = %+v, %v; want %+v", got, err, want)
	}
	if got, err = f.repo.History(t.Context(), ScopeAll(), f.ref(1, "p/a"), 0); err != nil || len(got) != 0 {
		t.Errorf("History(p/a) = %+v, %v; want none", got, err)
	}
	u := f.user("u")
	f.path(u, "q")
	if _, err := f.repo.History(t.Context(), f.scope(u, 1), f.ref(1, "p"), 0); err != ErrNotFound {
		t.Errorf("History out of scope: %v", err)
	}
}
