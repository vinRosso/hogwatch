//go:build linux

package build_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"golang.org/x/sys/unix"
)

func TestRun_RootInfo(t *testing.T) {
	root := makeFixture(t)
	res := runOK(t, opts(t.TempDir(), root, 2, testLimits))
	var st unix.Stat_t
	must(t, unix.Lstat(root, &st))
	if res.RootIno != st.Ino {
		t.Errorf("RootIno = %d, want the root's lstat ino %d", res.RootIno, st.Ino)
	}
	if res.RootDev != uint64(st.Dev) { //nolint:unconvert,gosec // Dev is uint32 on some architectures
		t.Errorf("RootDev = %d, want %d", res.RootDev, st.Dev)
	}
	if res.FSTotal <= 0 {
		t.Errorf("FSTotal = %d, want > 0", res.FSTotal)
	}
	if res.FSAvail < 0 || res.FSAvail > res.FSTotal {
		t.Errorf("FSAvail = %d, want within 0..FSTotal (%d)", res.FSAvail, res.FSTotal)
	}
}

func TestRun_Excludes(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, filepath.Join(root, "a", "b", "sub"), filepath.Join(root, "a", "c"))
	writeFile(t, filepath.Join(root, "a", "b", "f1"), 1000, 0)
	writeFile(t, filepath.Join(root, "a", "b", "sub", "f2"), 2000, 0)
	writeFile(t, filepath.Join(root, "a", "c", "f3"), 3000, 0)
	o := opts(t.TempDir(), root, 2, testLimits)
	o.Walk.Excludes = []string{"a/b"}
	res := runOK(t, o)
	if res.Stats.Walk.Excluded != 1 {
		t.Errorf("Walk.Excluded = %d, want 1", res.Stats.Walk.Excluded)
	}
	must(t, build.Verify(context.Background(), res.Path))
	g := readGen(t, res.Path)
	found := false
	for _, d := range g.dirs {
		if string(d.Name) == "b" {
			found = true
			if d.Flags&gen.FlagExcluded == 0 || d.Files != 0 {
				t.Errorf("b: flags = %#x, files = %d, want FlagExcluded and 0", d.Flags, d.Files)
			}
		}
		if string(d.Name) == "sub" {
			t.Error("a row for sub below the excluded directory")
		}
	}
	if !found {
		t.Error("no row for the excluded directory")
	}
	_ = os.Remove(res.Path)
}
