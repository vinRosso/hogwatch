//go:build linux

package totals_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
	"github.com/vinRosso/hogwatch/internal/totals"
	"github.com/vinRosso/hogwatch/internal/zfs"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func put(t *testing.T, root, rel string, size int) string {
	t.Helper()
	p := filepath.Join(root, rel)
	must(t, os.MkdirAll(filepath.Dir(p), 0o750))
	must(t, os.WriteFile(p, make([]byte, size), 0o600))
	return p
}

// alloc is the allocated bytes of a file, as the scanner counts them.
func alloc(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Lstat(p)
	must(t, err)
	return fi.Sys().(*syscall.Stat_t).Blocks * 512
}

// scan builds a generation of root and opens it.
func scan(t *testing.T, root string) *sql.DB {
	t.Helper()
	res, err := build.Run(context.Background(), build.Options{
		DataDir: t.TempDir(), RootID: 1, ScanID: 1, Root: root, ScannerVersion: "test",
		Walk: walk.Options{
			Workers: 2, AllowWritable: true,
			Limits: agg.Limits{TopN: 3, TopMaxDepth: 5, TopExts: 4},
		},
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	})
	must(t, err)
	d, _, err := gen.OpenReadOnly(context.Background(), res.Path)
	must(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestUsage_Cases(t *testing.T) {
	root := t.TempDir()
	a1 := put(t, root, "a/1", 1000) // linked from b/1; the winner is a/1
	a2 := put(t, root, "a/x/2", 2000)
	a3 := put(t, root, "a/3", 300)
	must(t, os.MkdirAll(filepath.Join(root, "b"), 0o750))
	must(t, os.Link(a1, filepath.Join(root, "b/1")))
	b4 := put(t, root, "b/4", 4000)
	b5 := put(t, root, "b/s1/5", 500) // linked twice inside b
	must(t, os.MkdirAll(filepath.Join(root, "b/s2"), 0o750))
	must(t, os.Link(b5, filepath.Join(root, "b/s2/6")))
	e7 := put(t, root, "\xff\n/7", 70)
	d := scan(t, root)

	al := func(ps ...string) (n int64) {
		for _, p := range ps {
			n += alloc(t, p)
		}
		return n
	}
	paths := []totals.Path{
		{ID: 1, UserID: 10, Rel: "a"},
		{ID: 2, UserID: 10, Rel: "a/x"},
		{ID: 3, UserID: 20, Rel: "b"},
		{ID: 4, UserID: 40, Rel: "missing"},
		{ID: 5, UserID: 50, Rel: "\xff\n"},
	}
	usage, prows, err := totals.Usage(context.Background(), d, paths)
	must(t, err)

	wantUsage := []totals.UsageRow{
		// A: a/1 + a/x/2 + a/3. The a/1 inode is A's own winner, so no charge.
		{UserID: 10, BytesLogical: 3300, BytesAlloc: al(a1, a2, a3), Files: 3},
		// B: b/4 and the in-B pair once (rollup), plus the a/1 inode that B links but A wins.
		{UserID: 20, BytesLogical: 4500 + 1000, BytesAlloc: al(b4, b5) + al(a1), Files: 2 + 1, SharedHardlinkBytes: al(a1)},
		{UserID: 40},
		{UserID: 50, BytesLogical: 70, BytesAlloc: al(e7), Files: 1},
	}
	if len(usage) != len(wantUsage) {
		t.Fatalf("usage rows = %+v, want %d rows (user 30 has no paths)", usage, len(wantUsage))
	}
	for i, w := range wantUsage {
		if usage[i] != w {
			t.Errorf("usage[%d] = %+v, want %+v", i, usage[i], w)
		}
	}

	wantPaths := []totals.PathRow{
		{UserPathID: 1, Found: true, BytesLogical: 3300, BytesAlloc: al(a1, a2, a3), Files: 3},
		{UserPathID: 2, Found: true, Redundant: true, BytesLogical: 2000, BytesAlloc: al(a2), Files: 1},
		{UserPathID: 3, Found: true, BytesLogical: 4500, BytesAlloc: al(b4, b5), Files: 2, SharedHardlinkBytes: al(a1)},
		{UserPathID: 4},
		{UserPathID: 5, Found: true, BytesLogical: 70, BytesAlloc: al(e7), Files: 1},
	}
	if len(prows) != len(wantPaths) {
		t.Fatalf("path rows = %+v", prows)
	}
	for i, w := range wantPaths {
		if prows[i] != w {
			t.Errorf("path[%d] = %+v, want %+v", i, prows[i], w)
		}
	}
}

func TestUsage_NestedOnly(t *testing.T) {
	root := t.TempDir()
	a1 := put(t, root, "a/1", 1000)
	a2 := put(t, root, "a/x/2", 2000)
	d := scan(t, root)
	// The nested path is listed first by name but has the higher ID.
	usage, prows, err := totals.Usage(context.Background(), d, []totals.Path{
		{ID: 7, UserID: 1, Rel: "a/x"}, {ID: 3, UserID: 1, Rel: "a"},
	})
	must(t, err)
	want := totals.UsageRow{UserID: 1, BytesLogical: 3000, BytesAlloc: alloc(t, a1) + alloc(t, a2), Files: 2}
	if len(usage) != 1 || usage[0] != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
	if len(prows) != 2 || prows[0].UserPathID != 3 || prows[0].Redundant || prows[1].UserPathID != 7 || !prows[1].Redundant {
		t.Fatalf("path rows = %+v", prows)
	}
}

func TestUsage_ChargesBothUsersAndRoot(t *testing.T) {
	root := t.TempDir()
	x := put(t, root, "u1/f", 100)
	must(t, os.MkdirAll(filepath.Join(root, "u2"), 0o750))
	must(t, os.Link(x, filepath.Join(root, "u2/g")))
	d := scan(t, root)
	// Root-assigned user covers both links, winner included: no charge.
	usage, _, err := totals.Usage(context.Background(), d, []totals.Path{
		{ID: 1, UserID: 1, Rel: ""}, {ID: 2, UserID: 2, Rel: "u2"}, {ID: 3, UserID: 3, Rel: "u1"},
	})
	must(t, err)
	a := alloc(t, x)
	want := []totals.UsageRow{
		{UserID: 1, BytesLogical: 100, BytesAlloc: a, Files: 1},
		{UserID: 2, BytesLogical: 100, BytesAlloc: a, Files: 1, SharedHardlinkBytes: a}, // rollup 0 + charge
		{UserID: 3, BytesLogical: 100, BytesAlloc: a, Files: 1},
	}
	for i, w := range want {
		if usage[i] != w {
			t.Errorf("usage[%d] = %+v, want %+v", i, usage[i], w)
		}
	}
}

// TestUsage_ChargesOncePerInode: b holds two links (b/1, b/2) to an inode whose
// winner a/1 is outside b; b is charged for that inode exactly once.
func TestUsage_ChargesOncePerInode(t *testing.T) {
	root := t.TempDir()
	x := put(t, root, "a/1", 1000)
	must(t, os.MkdirAll(filepath.Join(root, "b"), 0o750))
	must(t, os.Link(x, filepath.Join(root, "b/1")))
	must(t, os.Link(x, filepath.Join(root, "b/2")))
	d := scan(t, root)
	usage, _, err := totals.Usage(context.Background(), d, []totals.Path{
		{ID: 1, UserID: 10, Rel: "a"}, {ID: 2, UserID: 20, Rel: "b"},
	})
	must(t, err)
	a := alloc(t, x)
	want := []totals.UsageRow{
		{UserID: 10, BytesLogical: 1000, BytesAlloc: a, Files: 1},
		{UserID: 20, BytesLogical: 1000, BytesAlloc: a, Files: 1, SharedHardlinkBytes: a},
	}
	if len(usage) != len(want) {
		t.Fatalf("usage = %+v", usage)
	}
	for i, w := range want {
		if usage[i] != w {
			t.Errorf("usage[%d] = %+v, want %+v", i, usage[i], w)
		}
	}
}

func TestApplyZFS(t *testing.T) {
	ds := zfs.ByMountpoint([]zfs.Dataset{
		{Name: "tank/alice", Mountpoint: "/storage/alice", Used: 1, Avail: 2, Quota: 30, Refquota: 20, UsedSnapshots: 4},
	})
	paths := []totals.Path{{ID: 1, UserID: 1, Rel: "alice"}, {ID: 2, UserID: 1, Rel: "alice/sub"}}
	rows := []totals.PathRow{{UserPathID: 1, Found: true}, {UserPathID: 2, Found: true}}
	got := totals.ApplyZFS(rows, paths, "/storage", ds)
	if len(got) != 1 {
		t.Fatalf("ApplyZFS = %+v, want only alice", got)
	}
	g := got[0]
	if g.UserPathID != 1 || g.Dataset != "tank/alice" || g.Used != 1 || g.Avail != 2 || g.UsedSnapshots != 4 ||
		g.Quota == nil || *g.Quota != 20 {
		t.Errorf("got %+v", g)
	}
}
