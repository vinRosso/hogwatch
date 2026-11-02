package zfs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinRosso/hogwatch/internal/zfs"
)

func TestParse(t *testing.T) {
	in := "tank/alice\t/storage/alice\t100\t200\t1000\t-\t5\n" +
		"tank/alice@snap1\t-\t7\t-\t-\t-\t-\n" +
		"\n" +
		"tank/hidden\tnone\t-\tnone\t-\t-\t-\n"
	ds, err := zfs.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []zfs.Dataset{
		{Name: "tank/alice", Mountpoint: "/storage/alice", Used: 100, Avail: 200, Quota: 1000, UsedSnapshots: 5},
		{Name: "tank/hidden", Mountpoint: "none"},
	}
	if len(ds) != len(want) || ds[0] != want[0] || ds[1] != want[1] {
		t.Fatalf("got %+v, want %+v", ds, want)
	}
	m := zfs.ByMountpoint(ds)
	if len(m) != 1 || m["/storage/alice"].Name != "tank/alice" {
		t.Fatalf("ByMountpoint = %+v", m)
	}

	_, err = zfs.Parse(strings.NewReader("a\t/a\t1\t2\t3\t4\t5\nb\t/b\t1\t2\t3\t4\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("six fields: err = %v, want one naming line 2", err)
	}
	_, err = zfs.Parse(strings.NewReader("a\t/a\tx\t2\t3\t4\t5\n"))
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("bad number: err = %v", err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	ds, err := zfs.Load(filepath.Join(dir, "zfs.tsv"))
	if ds != nil || err != nil {
		t.Fatalf("missing file: %v, %v", ds, err)
	}
	p := filepath.Join(dir, "zfs.tsv")
	if err := os.WriteFile(p, []byte("a\t/a\t1\t2\t3\t4\t5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ds, err = zfs.Load(p)
	if err != nil || len(ds) != 1 || ds[0].Used != 1 {
		t.Fatalf("Load = %+v, %v", ds, err)
	}
}

func TestEffectiveQuota(t *testing.T) {
	for _, c := range []struct {
		q, r int64
		want int64 // -1 means nil
	}{{0, 0, -1}, {10, 0, 10}, {10, 5, 5}, {0, 7, 7}} {
		got := zfs.Dataset{Quota: c.q, Refquota: c.r}.EffectiveQuota()
		switch {
		case c.want < 0 && got != nil, c.want >= 0 && (got == nil || *got != c.want):
			t.Errorf("(%d,%d) = %v, want %d", c.q, c.r, got, c.want)
		}
	}
}
