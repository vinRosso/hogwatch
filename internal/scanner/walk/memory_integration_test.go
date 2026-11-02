//go:build integration && linux

package walk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// peakHeap runs fn while sampling HeapInuse every 10 ms and returns the highest
// sample, after a GC so earlier garbage does not count.
func peakHeap(fn func()) uint64 {
	runtime.GC()
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	sample := func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.HeapInuse > peak.Load() {
			peak.Store(m.HeapInuse)
		}
	}
	go func() {
		defer close(done)
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				sample()
				return
			case <-t.C:
				sample()
			}
		}
	}()
	fn()
	close(stop)
	<-done
	return peak.Load()
}

// fillParallel creates n empty files named f<i> in dir.
func fillParallel(t *testing.T, dir string, n int) {
	t.Helper()
	const par = 8
	var wg sync.WaitGroup
	errs := make(chan error, par)
	for p := range par {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := p; i < n; i += par {
				f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // test temp dir
				if err != nil {
					errs <- err
					return
				}
				_ = f.Close()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// TestScan_MillionEntryDirMemory scans one directory of 100k and one of 1M
// empty files with Discard and bounds the extra heap the larger one needs to
// 12 MiB (observed delta: about 0 MiB, peaks of 4 MiB each). Mutation check:
// retaining every entry name in a slice (a []string of the 1M names) raised
// the 1M peak from 8 MiB to 87 MiB and failed this test; recorded in the
// task 8 fix report. Only files are covered: the names of subdirectories are
// held by design, so a directory with millions of subdirectories
// grows by their number. This does not cover gen.Writer; see the build
// memory test.
func TestScan_MillionEntryDirMemory(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("run via make test-integration")
	}
	base := t.TempDir()
	if err := unix.Mount("tmpfs", base, "tmpfs", 0, "size=4g,nr_inodes=1500000"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(base, 0) })
	small, big := filepath.Join(base, "small"), filepath.Join(base, "big")
	for _, d := range []string{small, big} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	fillParallel(t, small, 100_000)
	fillParallel(t, big, 1_000_000)

	scanFiles := func(dir string) (files int64, peak uint64) {
		peak = peakHeap(func() {
			res, err := Scan(context.Background(), dir, Options{Workers: 2, AllowWritable: true}, Discard)
			if err != nil {
				t.Errorf("Scan %s: %v", dir, err)
			}
			files = res.Stats.Files
		})
		return files, peak
	}
	f1, p1 := scanFiles(small)
	f2, p2 := scanFiles(big)
	if f1 != 100_000 || f2 != 1_000_000 {
		t.Fatalf("files = %d, %d, want 100000, 1000000", f1, f2)
	}
	t.Logf("peak HeapInuse: 100k = %d MiB, 1M = %d MiB", p1>>20, p2>>20)
	if p2 >= p1+12<<20 {
		t.Errorf("peak heap grew from %d to %d bytes with 900k more entries in one directory, want < 12 MiB more", p1, p2)
	}
}
