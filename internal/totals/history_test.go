//go:build linux

package totals_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
	"github.com/vinRosso/hogwatch/internal/totals"
)

func golden(rootID int64, rel []byte) int64 {
	b := binary.BigEndian.AppendUint64(nil, uint64(rootID)) //nolint:gosec // G115: bit-cast
	return int64(xxhash.Sum64(append(b, rel...)))           //nolint:gosec // G115: bit-cast
}

func TestPathHash_Golden(t *testing.T) {
	if got, want := totals.PathHash(1, nil), golden(1, nil); got != want {
		t.Errorf("PathHash(1, nil) = %d, want %d", got, want)
	}
	rel := []byte("a/\xff")
	if got, want := totals.PathHash(7, rel), golden(7, rel); got != want {
		t.Errorf("PathHash(7, a/\\xff) = %d, want %d", got, want)
	}
	if totals.PathHash(1, []byte("a")) == totals.PathHash(2, []byte("a")) {
		t.Error("roots 1 and 2 hash the same path alike")
	}
	if totals.PathHash(1, nil) != totals.PathHash(1, []byte{}) {
		t.Error("nil and empty rel differ")
	}
}

func TestHistoryDirs(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a/x/1", 100)
	put(t, root, "b/2", 200)
	res, err := build.Run(context.Background(), build.Options{
		DataDir: t.TempDir(), RootID: 4, ScanID: 1, Root: root, ScannerVersion: "test",
		Walk: walk.Options{
			Workers: 2, AllowWritable: true,
			Limits: agg.Limits{TopN: 3, TopMaxDepth: 1, TopMinAlloc: 0, TopExts: 4},
		},
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	})
	must(t, err)
	d, _, err := gen.OpenReadOnly(context.Background(), res.Path)
	must(t, err)
	t.Cleanup(func() { _ = d.Close() })

	got, err := totals.HistoryDirs(context.Background(), d)
	must(t, err)
	if len(got) != 3 {
		t.Fatalf("HistoryDirs = %+v, want root, a, b", got)
	}
	want := map[string]bool{"": true, "a": true, "b": true}
	for _, h := range got {
		if !want[string(h.Rel)] {
			t.Errorf("unexpected dir %q", h.Rel)
		}
		delete(want, string(h.Rel))
		if h.PathHash != totals.PathHash(4, h.Rel) {
			t.Errorf("dir %q: hash %d, want %d", h.Rel, h.PathHash, totals.PathHash(4, h.Rel))
		}
		if h.Files == 0 || h.BytesAlloc == 0 {
			t.Errorf("dir %q: empty totals %+v", h.Rel, h)
		}
	}
}

// fixture writes a finished generation from raw rows and opens it.
func fixture(t *testing.T, scanID, rootID int64, dirs []gen.Dir, errs []gen.ErrorRow) *sql.DB {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "g.db")
	w, err := gen.Create(ctx, path)
	must(t, err)
	t.Cleanup(func() { _ = w.Abort() })
	parent := map[int64]int64{}
	for _, d := range dirs {
		parent[d.ID] = d.ParentID
	}
	desc := map[int64]int64{}
	for _, d := range dirs {
		for p := d.ParentID; p != 0; p = parent[p] {
			desc[p]++
		}
	}
	for _, d := range dirs {
		d.Dirs = desc[d.ID]
		must(t, w.AddDir(ctx, d))
	}
	for _, e := range errs {
		must(t, w.AddError(ctx, e))
	}
	must(t, w.BuildIndexes(ctx))
	must(t, w.NumberPrePost(ctx))
	must(t, w.Finish(ctx, gen.Meta{
		ScanID: scanID, RootID: rootID, ScannerVersion: "test",
		StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0),
	}))
	d, _, err := gen.OpenReadOnly(ctx, path)
	must(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func fdir(id, parent int64, name string, dev, ino, flags int64) gen.Dir {
	return gen.Dir{ID: id, ParentID: parent, Name: []byte(name), Dev: dev, Ino: ino, Flags: flags}
}

func TestScanIssues(t *testing.T) {
	ctx := context.Background()
	d := fixture(t, 9, 1, []gen.Dir{
		fdir(1, 0, "", 1, 1, 0),
		fdir(2, 1, "w", 1, 2, gen.FlagWritableMountRefused),
		fdir(3, 1, "e", 1, 3, 0),
	}, []gen.ErrorRow{
		{DirID: 3, Name: []byte("f"), Op: "lstat", Errno: 5, Message: "input/output error"},
		{DirID: 3, Name: []byte("g"), Op: "lstat", Errno: 13, Message: "denied"},
	})
	w, e, err := totals.ScanIssues(ctx, d, 9)
	must(t, err)
	if w == nil || w.Count != 1 || len(w.Nodes) != 1 || w.Nodes[0] != "9.2" {
		t.Errorf("writable = %+v", w)
	}
	if e == nil || e.Count != 1 || len(e.Nodes) != 1 || e.Nodes[0] != "9.3" {
		t.Errorf("eio = %+v", e)
	}

	clean := fixture(t, 9, 1, []gen.Dir{fdir(1, 0, "", 1, 1, 0)}, nil)
	w, e, err = totals.ScanIssues(ctx, clean, 9)
	must(t, err)
	if w != nil || e != nil {
		t.Errorf("clean gen: %+v %+v", w, e)
	}
}

func TestScanIssues_NodeCap(t *testing.T) {
	dirs := []gen.Dir{fdir(1, 0, "", 1, 1, 0)}
	for i := int64(2); i < 16; i++ {
		dirs = append(dirs, fdir(i, 1, string(rune('a'+i)), 1, i, gen.FlagWritableMountRefused))
	}
	w, _, err := totals.ScanIssues(context.Background(), fixture(t, 3, 1, dirs, nil), 3)
	must(t, err)
	if w.Count != 14 || len(w.Nodes) != 10 || w.Nodes[0] != "3.2" || w.Nodes[9] != "3.11" {
		t.Errorf("writable = %+v", w)
	}
}

func TestCrossRoot_Duplicate(t *testing.T) {
	ctx := context.Background()
	build2 := func(flags int64) (map[int64]*sql.DB, map[int64]int64) {
		g1 := fixture(t, 11, 1, []gen.Dir{fdir(1, 0, "", 5, 100, 0), fdir(2, 1, "home", 5, 200, 0)}, nil)
		g2 := fixture(t, 22, 2, []gen.Dir{fdir(1, 0, "", 6, 1, 0), fdir(2, 1, "mnt", 5, 200, flags)}, nil)
		return map[int64]*sql.DB{1: g1, 2: g2}, map[int64]int64{1: 11, 2: 22}
	}
	paths := []totals.RootedPath{
		{Path: totals.Path{ID: 7, UserID: 1, Rel: "home"}, RootID: 1},
		{Path: totals.Path{ID: 8, UserID: 1, Rel: "missing"}, RootID: 1},
	}
	gens, scans := build2(0)
	got, err := totals.CrossRootDuplicates(ctx, gens, scans, paths)
	must(t, err)
	want := totals.Duplicate{UserPathID: 7, OtherRootID: 2, Node: "22.2"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("dups = %+v, want %+v", got, want)
	}
	for _, f := range []int64{gen.FlagMountpointSkipped, gen.FlagNestedRootSkipped, gen.FlagWritableMountRefused, gen.FlagExcluded} {
		gens, scans = build2(f)
		got, err = totals.CrossRootDuplicates(ctx, gens, scans, paths)
		must(t, err)
		if len(got) != 0 {
			t.Errorf("flag %d: dups = %+v", f, got)
		}
	}
}

func TestCrossRoot_SameRootIgnored(t *testing.T) {
	g := fixture(t, 1, 1, []gen.Dir{fdir(1, 0, "", 5, 1, 0), fdir(2, 1, "a", 5, 2, 0)}, nil)
	got, err := totals.CrossRootDuplicates(context.Background(), map[int64]*sql.DB{1: g}, map[int64]int64{1: 1},
		[]totals.RootedPath{{Path: totals.Path{ID: 1, UserID: 1, Rel: "a"}, RootID: 1}})
	must(t, err)
	if len(got) != 0 {
		t.Errorf("dups = %+v", got)
	}
}
