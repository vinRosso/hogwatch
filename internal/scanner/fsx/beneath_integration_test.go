//go:build integration && linux

package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenBeneath_NoXDev(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("run via make test-integration")
	}
	root := t.TempDir()
	m := filepath.Join(root, "a", "m")
	if err := os.MkdirAll(m, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", m, "tmpfs", 0, ""); err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(m, 0) })
	d, err := OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	for name, errno := range map[string]error{"openat2": nil, "ENOSYS": unix.ENOSYS, "EPERM": unix.EPERM} {
		t.Run(name, func(t *testing.T) {
			if errno != nil {
				withSeam(t, errno)
			}
			x, err := d.OpenBeneath("a/m")
			if err == nil {
				_ = x.Close()
				t.Fatal("crossed a mount")
			}
			if !errors.Is(err, unix.EXDEV) {
				t.Fatalf("err = %v, want EXDEV", err)
			}
			// the parent still opens
			p, err := d.OpenBeneath("a")
			if err != nil {
				t.Fatal(err)
			}
			_ = p.Close()
		})
	}
}

func TestOpenBeneath_SwapRace(t *testing.T) {
	for name, errno := range map[string]error{"openat2": nil, "fallback": unix.ENOSYS} {
		t.Run(name, func(t *testing.T) {
			if errno != nil {
				withSeam(t, errno)
			}
			root := t.TempDir()
			outside := t.TempDir()
			var oi unix.Stat_t
			if err := unix.Stat(outside, &oi); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "d"), 0o750); err != nil {
				t.Fatal(err)
			}
			d, err := OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = d.Close() }()

			dp := filepath.Join(root, "d")
			done := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer close(done)
				for i := range 2000 {
					_ = os.RemoveAll(dp)
					if i%2 == 0 {
						_ = os.Symlink(outside, dp)
					} else {
						_ = os.Mkdir(dp, 0o750)
					}
				}
			}()
			opens := 0
			check := func() {
				x, err := d.OpenBeneath("d")
				if err != nil {
					return
				}
				defer func() { _ = x.Close() }()
				opens++
				st, err := x.Stat()
				if err != nil {
					t.Error(err)
					return
				}
				if st.Ino == oi.Ino && st.Dev == uint64(oi.Dev) {
					t.Error("opened the symlink target outside the root")
				}
			}
			for running := true; running; {
				select {
				case <-done:
					running = false
				default:
				}
				check()
			}
			wg.Wait()
			t.Logf("%d successful opens", opens)
		})
	}
}
