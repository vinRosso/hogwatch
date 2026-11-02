//go:build integration && linux

package build_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/build"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
	"golang.org/x/sys/unix"
)

// TestRun_DirCountMemory measures the peak heap of build.Run on trees of 20k
// and 200k directories, logs both, and bounds the growth to 32 MiB. Measured:
// 5 MiB and 18 MiB (delta 13 MiB, about 75 B per directory), so the bound has
// 2.5x headroom. It guards against the writer buffering every row until
// Finish: that mutation (BatchRows raised above the row count) is recorded in
// the task 8 fix report. It does not prove memory scales with the walk
// frontier: the per-directory growth is still there, and M4's host
// run checks it at scale.
func TestRun_DirCountMemory(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("run via make test-integration")
	}
	base := t.TempDir()
	if err := unix.Mount("tmpfs", base, "tmpfs", 0, "size=2g,nr_inodes=400000"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(base, 0) })
	mk := func(name string, groups, per int) string {
		root := filepath.Join(base, name)
		for g := range groups {
			for i := range per {
				if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("g%d", g), fmt.Sprintf("d%d", i)), 0o750); err != nil {
					t.Fatal(err)
				}
			}
		}
		return root
	}
	small, big := mk("small", 100, 200), mk("big", 400, 500) // 20k and 200k leaf dirs

	run := func(root string, scanID int64) (dirRows int64, peak uint64) {
		data := t.TempDir()
		runtime.GC()
		var pk atomic.Uint64
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			tk := time.NewTicker(10 * time.Millisecond)
			defer tk.Stop()
			for {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapInuse > pk.Load() {
					pk.Store(m.HeapInuse)
				}
				select {
				case <-stop:
					return
				case <-tk.C:
				}
			}
		}()
		res, err := build.Run(context.Background(), build.Options{
			DataDir: data, RootID: 1, ScanID: scanID, Root: root,
			Walk: walk.Options{Workers: 2, AllowWritable: true, Limits: agg.DefaultLimits()},
		})
		close(stop)
		<-done
		if err != nil {
			t.Fatalf("Run %s: %v", root, err)
		}
		return res.Stats.DirRows, pk.Load()
	}
	r1, p1 := run(small, 1)
	r2, p2 := run(big, 2)
	t.Logf("build.Run peak HeapInuse: %d dirs = %d MiB, %d dirs = %d MiB", r1, p1>>20, r2, p2>>20)
	if r1 != 20_000+100+1 || r2 != 200_000+400+1 {
		t.Fatalf("dir rows = %d, %d, want 20101, 200401", r1, r2)
	}
	if p2 >= p1+32<<20 {
		t.Errorf("peak heap grew from %d to %d MiB for 180k more directories, want < 32 MiB more", p1>>20, p2>>20)
	}
}
