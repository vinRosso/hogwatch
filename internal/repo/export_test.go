package repo

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

func collectTree(t *testing.T, st Stream[TreeRow]) []string {
	t.Helper()
	var got []string
	if err := st.Each(func(r TreeRow) error { got = append(got, string(r.RelPath)); return nil }); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestExportTree(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	st, err := f.repo.ExportTree(t.Context(), ScopeAll(), f.ref(1, ""))
	if err != nil {
		t.Fatal(err)
	}
	got := collectTree(t, st)
	st.Close()
	want := []string{"", "a", "a/b", "a/b/c", "a/x", "bad\xff", "bad\xff/sub", "d"}
	if !slices.Equal(got, want) {
		t.Errorf("root = %q, want %q", got, want)
	}
	if st, err = f.repo.ExportTree(t.Context(), ScopeAll(), f.ref(1, "a")); err != nil {
		t.Fatal(err)
	}
	got = collectTree(t, st)
	st.Close()
	if want = []string{"", "b", "b/c", "x"}; !slices.Equal(got, want) {
		t.Errorf("a = %q, want %q", got, want)
	}
}

func TestExportTree_Scope(t *testing.T) {
	f := newFixture(t)
	f.scan(1, tree...)
	u := f.user("u")
	f.path(u, "a/b")
	s := f.scope(u, 1)
	if _, err := f.repo.ExportTree(t.Context(), s, f.ref(1, "")); !errors.Is(err, ErrNotFound) {
		t.Errorf("ancestor export: %v", err)
	}
	st, err := f.repo.ExportTree(t.Context(), s, f.ref(1, "a/b"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := collectTree(t, st); !slices.Equal(got, []string{"", "c"}) {
		t.Errorf("a/b export = %q", got)
	}
}

func TestExportTop(t *testing.T) {
	f := detailFixture(t)
	rows, err := f.repo.ExportTop(t.Context(), ScopeAll(), f.ref(1, "p"))
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	if rows[0].Kind != "largest" || string(rows[0].DirRel) != "a" || string(rows[0].Name) != "a.bin" ||
		rows[2].Kind != "oldest" || string(rows[2].Name) != "a.old" || !bytes.Equal(rows[1].DirRel, []byte("b")) {
		t.Errorf("rows = %+v", rows)
	}
}

func TestExportUsage(t *testing.T) {
	f := rootFixture(t)
	owner, other := f.user("owner"), f.user("other")
	f.stats(f.path(owner, "a"), true, false)
	f.stats(f.path(other, "d"), true, false)
	all, err := f.repo.ExportUsage(t.Context(), Caller{Admin: true}, ScopeAll(), f.ref(1, ""))
	if err != nil || len(all) != 2 {
		t.Fatalf("admin = %+v, %v", all, err)
	}
	mine, err := f.repo.ExportUsage(t.Context(), Caller{UserID: owner}, f.scope(owner, 1), f.ref(1, "a"))
	if err != nil || len(mine) != 1 || mine[0].Username != "owner" || string(mine[0].HostPath) != "/srv/r/a" {
		t.Errorf("owner = %+v, %v", mine, err)
	}
	sub, err := f.repo.ExportUsage(t.Context(), Caller{Admin: true}, ScopeAll(), f.ref(1, "d"))
	if err != nil || len(sub) != 1 || sub[0].Username != "other" {
		t.Errorf("inside d = %+v, %v", sub, err)
	}
}

func TestExportUsage_UsesTheRefsScan(t *testing.T) {
	f := rootFixture(t)
	owner := f.user("owner")
	p := f.path(owner, "a")
	f.stats(p, true, false)
	f.exec(`UPDATE path_stats SET bytes_alloc = 111, files = 1 WHERE user_path_id = ?`, p)
	f.scan(2, tree...)
	f.exec(`UPDATE scans SET finished_at = 900 WHERE id = 2`)
	f.exec(`UPDATE scan_roots SET current_scan_id = 2`)
	f.exec(`INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files, shared_hardlink_bytes) VALUES (2, ?, 1, 0, 0, 222, 2, 0)`, p)
	old, err := f.repo.ExportUsage(t.Context(), Caller{Admin: true}, ScopeAll(), f.ref(1, "a"))
	if err != nil || len(old) != 1 || old[0].BytesAlloc != 111 {
		t.Errorf("export on a superseded ref = %+v, %v; want scan 1's numbers", old, err)
	}
	cur, err := f.repo.ExportUsage(t.Context(), Caller{Admin: true}, ScopeAll(), f.ref(2, "a"))
	if err != nil || len(cur) != 1 || cur[0].BytesAlloc != 222 {
		t.Errorf("export on the current ref = %+v, %v", cur, err)
	}
}
