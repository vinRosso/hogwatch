package repo

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/gen/gentest"
	"github.com/vinRosso/hogwatch/internal/noderef"
	"github.com/vinRosso/hogwatch/internal/totals"
)

// top calls Top for a caller with no listing rights.
func (f *fixture) top(s Scope, ref noderef.Ref, kind string) ([]TopFile, bool, error) {
	return f.repo.Top(f.t.Context(), Caller{}, s, ref, kind, TopSubtree, time.Unix(100, 0))
}

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
	items, stored, err := f.top(ScopeAll(), f.ref(1, "p"), "largest")
	if err != nil || !stored || len(items) != 2 {
		t.Fatalf("Top(p, largest) = %+v, %v, %v", items, stored, err)
	}
	if items[0].Rank != 1 || string(items[0].Name) != "a.bin" || items[0].DirRef != f.ref(1, "p/a") || items[0].Size != 900 || items[0].Alloc != 1000 || items[0].Mtime != 5 {
		t.Errorf("first = %+v", items[0])
	}
	if items[1].Rank != 2 || string(items[1].Name) != "b\xff.bin" || items[1].DirRef != f.ref(1, "p/b") {
		t.Errorf("second = %+v", items[1])
	}
	items, stored, err = f.top(ScopeAll(), f.ref(1, "p"), "oldest")
	if err != nil || !stored || len(items) != 1 || string(items[0].Name) != "a.old" {
		t.Errorf("Top(p, oldest) = %+v, %v, %v", items, stored, err)
	}
	// A node without a list of its own answers from the nearest ancestor's list,
	// cut to the files below it.
	items, stored, err = f.top(ScopeAll(), f.ref(1, "p/a"), "largest")
	if err != nil || !stored || len(items) != 1 || string(items[0].Name) != "a.bin" || items[0].DirRef != f.ref(1, "p/a") {
		t.Errorf("Top(p/a) = %+v, %v, %v; want a.bin from p's list", items, stored, err)
	}
	items, stored, err = f.top(ScopeAll(), f.ref(1, "p/b"), "largest")
	if err != nil || !stored || len(items) != 1 || string(items[0].Name) != "b\xff.bin" {
		t.Errorf("Top(p/b) = %+v, %v, %v; want only b's file", items, stored, err)
	}
	// It says "not stored" when no file below it is in that list, or when no
	// list of that kind exists above it.
	items, stored, err = f.top(ScopeAll(), f.ref(1, "p/b/c"), "largest")
	if err != nil || stored || len(items) != 0 {
		t.Errorf("Top(p/b/c) = %+v, %v, %v; want not stored", items, stored, err)
	}
	items, stored, err = f.top(ScopeAll(), f.ref(1, "p/b"), "oldest")
	if err != nil || stored || len(items) != 0 {
		t.Errorf("Top(p/b, oldest) = %+v, %v, %v; want not stored", items, stored, err)
	}
	if _, _, err := f.top(ScopeAll(), f.ref(1, "p"), "bogus"); err == nil || err == ErrNotFound {
		t.Errorf("Top(bogus kind) err = %v, want a validation error", err)
	}
	u := f.user("u")
	f.path(u, "q")
	if _, _, err := f.top(f.scope(u, 1), f.ref(1, "p"), "largest"); err != ErrNotFound {
		t.Errorf("Top out of scope: %v", err)
	}
	// The root anchor lists q's file for an admin, and q's owner sees only
	// what is under q (nothing is stored at q itself).
	items, _, err = f.top(ScopeAll(), f.ref(1, ""), "largest")
	if err != nil || len(items) != 3 {
		t.Errorf("Top(root) = %+v, %v", items, err)
	}
	if _, _, err := f.top(f.scope(u, 1), f.ref(1, ""), "largest"); err != ErrNotFound {
		t.Errorf("Top(root) for a q-only user: %v", err)
	}
}

func TestTopDirect(t *testing.T) {
	f := detailFixture(t)
	direct := func(rel, kind string) (names []string, stored bool) {
		t.Helper()
		items, stored, err := f.repo.Top(t.Context(), Caller{}, ScopeAll(), f.ref(1, rel), kind, TopDir, time.Unix(100, 0))
		if err != nil {
			t.Fatalf("Top(%s, %s, dir): %v", rel, kind, err)
		}
		for _, x := range items {
			if x.DirRef != f.ref(1, rel) {
				t.Errorf("Top(%s) item %s is in %v, not directly in the node", rel, x.Name, x.DirRef)
			}
			names = append(names, string(x.Name))
		}
		return names, stored
	}
	// p/a is no anchor; its files come from p's list.
	if got, stored := direct("p/a", "largest"); !stored || !slices.Equal(got, []string{"a.bin"}) {
		t.Errorf("p/a largest = %v, %v", got, stored)
	}
	if got, stored := direct("p/a", "oldest"); !stored || !slices.Equal(got, []string{"a.old"}) {
		t.Errorf("p/a oldest = %v, %v", got, stored)
	}
	// p has a list, but all its files sit in subdirectories: nothing directly in it.
	if got, stored := direct("p", "largest"); !stored || len(got) != 0 {
		t.Errorf("p largest = %v, %v; want stored and empty", got, stored)
	}
	// q has no list; the root's holds q.bin.
	if got, stored := direct("q", "largest"); !stored || !slices.Equal(got, []string{"q.bin"}) {
		t.Errorf("q largest = %v, %v", got, stored)
	}
	// Below p/b only p/b's own file counts, not the one of p/a.
	if got, _ := direct("p/b", "largest"); !slices.Equal(got, []string{"b\xff.bin"}) {
		t.Errorf("p/b largest = %v", got)
	}
}

func TestTopFromListing(t *testing.T) {
	f := detailFixture(t)
	owner, other := f.user("owner"), f.user("other")
	ref := f.ref(1, "p/b")
	job := func(id, user int64, params, progress string, expires int64) {
		f.exec(`INSERT INTO jobs (id, kind, root_id, target_node, params, requested_by, status, progress, created_at, expires_at)
			VALUES (?, 'listing', 1, ?, ?, ?, 'done', ?, 1, ?)`, id, ref.String(), params, user, progress, expires)
	}
	row := func(job, seq int64, rel string, size, mtime int64) {
		f.exec(`INSERT INTO listing_results (job_id, seq, rel_name, type, size, alloc, mtime, uid) VALUES (?, ?, ?, 'file', ?, ?, ?, 0)`,
			job, seq, []byte(rel), size, size, mtime)
	}
	job(7, owner, `{"mode":"subtree"}`, `{"rows":3,"truncated":false}`, 1000)
	row(7, 1, "own.bin", 100, 30)
	row(7, 2, "c/deep.bin", 900, 20)
	row(7, 3, "new/x.bin", 50, 10) // "new" was made after the scan
	// Newer listings that must not be used: one with a filter, one cut at the row cap.
	job(8, owner, `{"mode":"subtree","exts":["bin"]}`, `{"rows":1,"truncated":false}`, 1000)
	row(8, 1, "filtered.bin", 1, 1)
	job(9, owner, `{"mode":"subtree"}`, `{"rows":1,"truncated":true}`, 1000)
	row(9, 1, "cut.bin", 1, 1)

	top := func(c Caller, kind string, now int64) []TopFile {
		t.Helper()
		items, stored, err := f.repo.Top(t.Context(), c, ScopeAll(), ref, kind, TopSubtree, time.Unix(now, 0))
		if err != nil || stored != (len(items) > 0) {
			t.Fatalf("Top(%v, %s) = %+v, %v, %v", c, kind, items, stored, err)
		}
		return items
	}
	names := func(items []TopFile) (names, dirs []string) {
		for _, x := range items {
			names = append(names, string(x.Name))
			dirs = append(dirs, x.DirRef.String())
		}
		return names, dirs
	}
	me := Caller{UserID: owner, CanList: true}
	got, dirs := names(top(me, "largest", 100))
	if want := []string{"deep.bin", "own.bin", "x.bin"}; !slices.Equal(got, want) {
		t.Errorf("largest = %v, want %v", got, want)
	}
	if want := []string{f.ref(1, "p/b/c").String(), ref.String(), ref.String()}; !slices.Equal(dirs, want) {
		t.Errorf("dirs = %v, want %v (a directory the scan lacks falls back to its parent)", dirs, want)
	}
	if got, _ := names(top(me, "oldest", 100)); !slices.Equal(got, []string{"x.bin", "deep.bin", "own.bin"}) {
		t.Errorf("oldest = %v", got)
	}
	// Without the right to list, for another user's listing, or after expiry, the
	// answer is the ancestor's list cut to p/b.
	for name, c := range map[string]struct {
		c   Caller
		now int64
	}{
		"cannot list":  {Caller{UserID: owner}, 100},
		"other user":   {Caller{UserID: other, CanList: true}, 100},
		"expired":      {me, 2000},
		"unknown user": {Caller{UserID: 0, CanList: true}, 100},
	} {
		if got, _ := names(top(c.c, "largest", c.now)); !slices.Equal(got, []string{"b\xff.bin"}) {
			t.Errorf("%s: largest = %q, want the ancestor's b\\xff.bin", name, got)
		}
	}

	// For the files directly in p/b, a listing of either mode counts, and only
	// names without a "/" do. The subtree listing has one such file.
	direct := func(c Caller) []string {
		t.Helper()
		items, _, err := f.repo.Top(t.Context(), c, ScopeAll(), ref, "largest", TopDir, time.Unix(100, 0))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := names(items)
		return got
	}
	if got := direct(me); !slices.Equal(got, []string{"own.bin"}) {
		t.Errorf("direct from the subtree listing = %v", got)
	}
	// A newer children-mode listing is now the source for the direct files, and it
	// leaves the subtree answer alone. Its directory row is not a file.
	job(10, owner, `{"mode":"children"}`, `{"rows":2,"truncated":false}`, 1000)
	row(10, 1, "ch.bin", 7, 1)
	f.exec(`INSERT INTO listing_results (job_id, seq, rel_name, type, size, alloc, mtime, uid) VALUES (10, 2, ?, 'dir', 9999, 9999, 1, 0)`, []byte("sub"))
	if got := direct(me); !slices.Equal(got, []string{"ch.bin"}) {
		t.Errorf("direct from the children listing = %v", got)
	}
	if got, _ := names(top(me, "largest", 100)); !slices.Equal(got, []string{"deep.bin", "own.bin", "x.bin"}) {
		t.Errorf("subtree after a children listing = %v", got)
	}
}

func TestTopBoth(t *testing.T) {
	f := detailFixture(t)
	both := func(c Caller, rel, kind string) (names []string, stored bool) {
		t.Helper()
		items, stored, err := f.repo.Top(t.Context(), c, ScopeAll(), f.ref(1, rel), kind, TopBoth, time.Unix(100, 0))
		if err != nil {
			t.Fatalf("Top(%s, %s, both): %v", rel, kind, err)
		}
		for i, x := range items {
			if x.Rank != i+1 {
				t.Errorf("Top(%s) item %d has rank %d", rel, i, x.Rank)
			}
			names = append(names, string(x.Name))
		}
		return names, stored
	}
	// p holds no file directly; both of its list's files are below it.
	if got, stored := both(Caller{}, "p", "largest"); !stored || !slices.Equal(got, []string{"a.bin", "b\xff.bin"}) {
		t.Errorf("p largest = %q, %v", got, stored)
	}
	// A file that is in both answers shows once.
	if got, _ := both(Caller{}, "p/b", "largest"); !slices.Equal(got, []string{"b\xff.bin"}) {
		t.Errorf("p/b largest = %q", got)
	}
	// Nothing in either answer: not stored, as for the subtree alone.
	if got, stored := both(Caller{}, "p/b/c", "largest"); stored || len(got) != 0 {
		t.Errorf("p/b/c largest = %q, %v; want not stored", got, stored)
	}
	// From a listing: the subtree files and the direct one, ordered by size, the
	// direct file (own.bin) once.
	owner := f.user("owner")
	ref := f.ref(1, "p/b")
	f.exec(`INSERT INTO jobs (id, kind, root_id, target_node, params, requested_by, status, progress, created_at, expires_at)
		VALUES (7, 'listing', 1, ?, '{"mode":"subtree"}', ?, 'done', '{"rows":3,"truncated":false}', 1, 1000)`, ref.String(), owner)
	for i, r := range []struct {
		rel         string
		size, mtime int64
	}{{"own.bin", 100, 30}, {"c/deep.bin", 900, 20}, {"new/x.bin", 50, 10}} {
		f.exec(`INSERT INTO listing_results (job_id, seq, rel_name, type, size, alloc, mtime, uid) VALUES (7, ?, ?, 'file', ?, ?, ?, 0)`,
			i+1, []byte(r.rel), r.size, r.size, r.mtime)
	}
	me := Caller{UserID: owner, CanList: true}
	if got, _ := both(me, "p/b", "largest"); !slices.Equal(got, []string{"deep.bin", "own.bin", "x.bin"}) {
		t.Errorf("listing largest = %q", got)
	}
	if got, _ := both(me, "p/b", "oldest"); !slices.Equal(got, []string{"x.bin", "deep.bin", "own.bin"}) {
		t.Errorf("listing oldest = %q", got)
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
