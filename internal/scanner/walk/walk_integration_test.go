//go:build integration && linux

package walk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
	"golang.org/x/sys/unix"
)

// mountAt mounts a filesystem on dir and unmounts it when the test ends.
func mountAt(t *testing.T, fstype, dir string, flags uintptr) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("run via make test-integration")
	}
	if err := unix.Mount(fstype, dir, fstype, flags, ""); err != nil {
		t.Fatalf("mount %s on %s: %v", fstype, dir, err)
	}
	t.Cleanup(func() { _ = unix.Unmount(dir, 0) })
}

func TestScan_SkipsOtherFS(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	mountAt(t, "tmpfs", sub, 0)
	for i := range 3 {
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%d", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res, err := Scan(context.Background(), root, Options{Workers: 2, AllowWritable: true}, Discard)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.MountsSkipped != 1 || res.Stats.Files != 0 || res.Stats.Dirs != 1 {
		t.Fatalf("CrossFS=false: mounts=%d files=%d dirs=%d, want 1/0/1",
			res.Stats.MountsSkipped, res.Stats.Files, res.Stats.Dirs)
	}

	res, err = Scan(context.Background(), root, Options{Workers: 2, CrossFS: true, AllowWritable: true}, Discard)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.MountsSkipped != 0 || res.Stats.Files != 3 || res.Stats.Dirs != 2 {
		t.Fatalf("CrossFS=true: mounts=%d files=%d dirs=%d, want 0/3/2",
			res.Stats.MountsSkipped, res.Stats.Files, res.Stats.Dirs)
	}
}

func TestScan_SkipsPseudoFS(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "p")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	mountAt(t, "proc", p, 0)

	res, err := Scan(context.Background(), root, Options{Workers: 2, CrossFS: true, AllowWritable: true}, Discard)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Stats
	if s.PseudoSkipped != 1 || s.Dirs != 1 || s.Files != 0 || s.MountsSkipped != 0 {
		t.Fatalf("pseudo=%d dirs=%d files=%d mounts=%d, want 1/1/0/0",
			s.PseudoSkipped, s.Dirs, s.Files, s.MountsSkipped)
	}
}

func TestStatfs_ReadOnlyMount(t *testing.T) {
	dir := t.TempDir()
	mountAt(t, "tmpfs", dir, unix.MS_RDONLY)

	d, err := fsx.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	info, err := d.Statfs()
	if err != nil {
		t.Fatal(err)
	}
	if !info.ReadOnly {
		t.Fatal("ReadOnly = false on an MS_RDONLY tmpfs")
	}
	if info.Type != unix.TMPFS_MAGIC {
		t.Fatalf("Type = %#x, want tmpfs %#x", info.Type, unix.TMPFS_MAGIC)
	}
}

func TestLstat_MountRootOnBindMount(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if os.Geteuid() != 0 {
		t.Fatal("run via make test-integration")
	}
	b := filepath.Join(root, "b")
	if err := unix.Mount(filepath.Join(root, "a"), b, "", unix.MS_BIND, ""); err != nil {
		t.Fatalf("bind mount: %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(b, 0) })

	d, err := fsx.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	parent, err := d.Stat()
	if err != nil {
		t.Fatal(err)
	}
	st, err := d.Lstat("b")
	if err != nil {
		t.Fatal(err)
	}
	if !st.MountRoot {
		t.Fatal("bind mount root not reported as MountRoot")
	}
	if st.Dev != parent.Dev {
		t.Fatalf("Dev = %d, want parent's %d", st.Dev, parent.Dev)
	}
}

// tmpfsBase mounts a tmpfs on a fresh temp dir, creates the directories dirs in
// it, and returns it. The tmpfs is remounted read-only after setup when ro is
// set, so a scan of it needs no AllowWritable. The kernel does not need the
// mount point of a later mount to be writable.
func tmpfsBase(t *testing.T, ro bool, dirs ...string) string {
	t.Helper()
	base := t.TempDir()
	mountAt(t, "tmpfs", base, 0)
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(base, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if ro {
		remountRO(t, base)
	}
	return base
}

func remountRO(t *testing.T, dir string) {
	t.Helper()
	if err := unix.Mount("", dir, "", unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
		t.Fatalf("remount %s read-only: %v", dir, err)
	}
}

func fill(t *testing.T, dir string, n int) {
	t.Helper()
	for i := range n {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScan_SkipsDevtmpfsViaMountinfo(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "dev")
	if err := os.Mkdir(dev, 0o700); err != nil {
		t.Fatal(err)
	}
	// Mounted read-write on purpose: a read-only mount would flag the kernel's
	// shared devtmpfs superblock itself. The scan only reads.
	mountAt(t, "devtmpfs", dev, 0)
	mounts, err := fsx.ReadMounts()
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Workers: 2, CrossFS: true, AllowWritable: true}

	// Negative control: statfs reports tmpfs, so without mountinfo it is entered.
	res, err := Scan(context.Background(), root, opts, Discard)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.PseudoSkipped != 0 || res.Stats.Dirs < 2 {
		t.Fatalf("without Mounts: pseudo=%d dirs=%d, want devtmpfs entered (0 and >=2)", res.Stats.PseudoSkipped, res.Stats.Dirs)
	}

	opts.Mounts = mounts
	rec := newRec()
	res, err = Scan(context.Background(), root, opts, rec)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.PseudoSkipped != 1 || res.Stats.Dirs != 1 || res.Stats.Files != 0 {
		t.Fatalf("with Mounts: pseudo=%d dirs=%d files=%d, want 1/1/0", res.Stats.PseudoSkipped, res.Stats.Dirs, res.Stats.Files)
	}
	if f := rec.byPath(t)["dev"].Flags; f&gen.FlagMountpointSkipped == 0 {
		t.Fatalf("dev row flags = %#x, want FlagMountpointSkipped", f)
	}
}

func TestScan_WritableMountRefused(t *testing.T) {
	root := tmpfsBase(t, true, "rw", "ro")
	rw, ro := filepath.Join(root, "rw"), filepath.Join(root, "ro")
	mountAt(t, "tmpfs", rw, 0)
	fill(t, rw, 3)
	mountAt(t, "tmpfs", ro, 0)
	fill(t, ro, 2)
	remountRO(t, ro)

	rec := newRec()
	res, err := Scan(context.Background(), root, Options{Workers: 2, CrossFS: true}, rec)
	if err != nil {
		t.Fatal(err)
	}
	rows := rec.byPath(t)
	if f := rows["rw"]; f.Flags&gen.FlagWritableMountRefused == 0 || f.Files != 0 {
		t.Errorf("rw row = %+v, want FlagWritableMountRefused and 0 files", f)
	}
	if f := rows["ro"]; f.Flags != 0 || f.Files != 2 {
		t.Errorf("ro row = %+v, want entered with 2 files", f)
	}
	if s := res.Stats; s.WritableRefused != 1 || s.Files != 2 || s.Dirs != 2 {
		t.Errorf("stats = %+v, want writableRefused 1, files 2, dirs 2", s)
	}
}

func TestScan_LateWritableMount(t *testing.T) {
	root := tmpfsBase(t, true, "late")
	mounts, err := fsx.ReadMounts() // snapshot first: the mount below is not in it
	if err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(root, "late")
	mountAt(t, "tmpfs", late, 0)
	fill(t, late, 3)

	rec := newRec()
	res, err := Scan(context.Background(), root, Options{Workers: 2, CrossFS: true, Mounts: mounts}, rec)
	if err != nil {
		t.Fatal(err)
	}
	if f := rec.byPath(t)["late"]; f.Flags&gen.FlagWritableMountRefused == 0 || f.Files != 0 {
		t.Errorf("late row = %+v, want FlagWritableMountRefused and 0 files", f)
	}
	if s := res.Stats; s.WritableRefused != 1 || s.Files != 0 {
		t.Errorf("stats = %+v, want writableRefused 1 and 0 files", s)
	}
}

func TestScan_SameDeviceBindLoop(t *testing.T) {
	root := tmpfsBase(t, false, "x/loop")
	fill(t, root, 1)
	loop := filepath.Join(root, "x", "loop")
	if err := unix.Mount(root, loop, "", unix.MS_BIND, ""); err != nil {
		t.Fatalf("bind mount: %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(loop, 0) })

	rec := newRec()
	res, err := Scan(context.Background(), root, Options{Workers: 2, AllowWritable: true}, rec)
	if err != nil {
		t.Fatal(err)
	}
	if f := rec.byPath(t)["x/loop"]; f.Flags&gen.FlagMountpointSkipped == 0 || res.Stats.MountsSkipped != 1 || res.Stats.Loops != 0 {
		t.Errorf("CrossFS=false: loop row flags %#x, stats %+v, want FlagMountpointSkipped and 1 mount skipped", f.Flags, res.Stats)
	}

	rec = newRec()
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err = Scan(context.Background(), root, Options{Workers: 2, CrossFS: true, AllowWritable: true}, rec)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("scan of a bind loop did not terminate")
	}
	if err != nil {
		t.Fatal(err)
	}
	row := rec.byPath(t)["x/loop"]
	if row.Flags&gen.FlagUnreadable == 0 || row.ErrorCount != 1 || row.Files != 0 {
		t.Errorf("loop row = %+v, want FlagUnreadable, error_count 1, no files", row)
	}
	if len(rec.errs) != 1 || rec.errs[0].Op != "open" || rec.errs[0].Errno != int64(unix.ELOOP) || rec.errs[0].DirID != row.ID {
		t.Errorf("error rows = %+v, want one open/ELOOP keyed to the loop dir %d", rec.errs, row.ID)
	}
	if s := res.Stats; s.Loops != 1 || s.Errors != 1 || s.Dirs != 2 || s.Files != 1 {
		t.Errorf("stats = %+v, want loops 1, errors 1, dirs 2 (root, x), files 1", s)
	}
}
