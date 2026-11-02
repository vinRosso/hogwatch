package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func isNoFollow(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR)
}

func TestOpenRoot_RefusesSymlink(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "real"), 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink("real", link); err != nil {
		t.Fatal(err)
	}
	d, err := OpenRoot(link)
	if err == nil {
		_ = d.Close()
		t.Fatal("OpenRoot followed a symlink")
	}
	if !isNoFollow(err) {
		t.Fatalf("err = %v, want ELOOP or ENOTDIR", err)
	}
}

func TestOpenChild_RefusesSymlink(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "real"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(tmp, "link")); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	if c, err := root.OpenChild("real"); err != nil {
		t.Fatalf("OpenChild(real): %v", err)
	} else {
		_ = c.Close()
	}
	c, err := root.OpenChild("link")
	if err == nil {
		_ = c.Close()
		t.Fatal("OpenChild followed a symlink")
	}
	if !isNoFollow(err) {
		t.Fatalf("err = %v, want ELOOP or ENOTDIR", err)
	}
}

func collect(t *testing.T, d *Dir) []string {
	t.Helper()
	var got []string
	err := d.ReadNames(func(name string) error {
		got = append(got, name)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadNames: %v", err)
	}
	slices.Sort(got)
	return got
}

func TestReadNames_StreamsAllEntries(t *testing.T) {
	tmp := t.TempDir()
	const n = 5000
	pad := strings.Repeat("p", 190)
	want := make([]string, 0, n)
	for i := range n {
		name := fmt.Sprintf("%s%05d", pad, i) // 195 bytes: 5000 entries exceed the 32 KiB buffer many times over
		want = append(want, name)
		if err := os.WriteFile(filepath.Join(tmp, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(want)

	d, err := OpenRoot(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	got := collect(t, d)
	if !slices.Equal(got, want) {
		t.Fatalf("got %d names, want %d (or contents differ)", len(got), len(want))
	}
	for _, name := range got {
		if name == "." || name == ".." {
			t.Fatalf("ReadNames returned %q", name)
		}
	}
}

func TestReadNames_HostileNames(t *testing.T) {
	tmp := t.TempDir()
	// "\xe2\x80\xae" is U+202E (right-to-left override), spelled as bytes to keep lint quiet.
	want := []string{"a\nb", "\xff\xfe", "\xe2\x80\xaeevil", "=cmd", strings.Repeat("x", 255)}
	for _, name := range want {
		if err := os.WriteFile(filepath.Join(tmp, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(want)

	d, err := OpenRoot(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	got := collect(t, d)
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, name := range got {
		if _, err := d.Lstat(name); err != nil {
			t.Errorf("Lstat(%q): %v", name, err)
		}
	}
}

func TestReadNames_StopsOnError(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(tmp, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d, err := OpenRoot(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	sentinel := errors.New("stop")
	calls := 0
	err = d.ReadNames(func(string) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
}

func TestLstat_Fields(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "file"), make([]byte, 10000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmp, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	const target = "file"
	if err := os.Symlink(target, filepath.Join(tmp, "link")); err != nil {
		t.Fatal(err)
	}
	d, err := OpenRoot(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	f, err := d.Lstat("file")
	if err != nil {
		t.Fatal(err)
	}
	if f.Size != 10000 || f.Blocks <= 0 || f.IsDir() || f.IsSymlink() || f.Ino == 0 || f.Nlink != 1 || f.Mtime == 0 {
		t.Errorf("file stat = %+v", f)
	}
	s, err := d.Lstat("sub")
	if err != nil {
		t.Fatal(err)
	}
	if !s.IsDir() || s.IsSymlink() {
		t.Errorf("subdir stat = %+v", s)
	}
	l, err := d.Lstat("link")
	if err != nil {
		t.Fatal(err)
	}
	if !l.IsSymlink() || l.IsDir() || l.Size != int64(len(target)) {
		t.Errorf("symlink stat = %+v", l)
	}

	self, err := d.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !self.IsDir() || self.Ino == 0 {
		t.Errorf("dir Stat = %+v", self)
	}
	child, err := d.OpenChild("sub")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Close() }()
	cs, err := child.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if cs.Ino != s.Ino || cs.Dev != s.Dev {
		t.Errorf("child Stat (dev,ino) = (%d,%d), Lstat = (%d,%d)", cs.Dev, cs.Ino, s.Dev, s.Ino)
	}
}

func TestLstat_Vanished(t *testing.T) {
	d, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.Lstat("gone"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestStatfs_TempDir(t *testing.T) {
	d, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	info, err := d.Statfs()
	if err != nil {
		t.Fatal(err)
	}
	if info.Type == 0 || info.ReadOnly {
		t.Errorf("Statfs = %+v", info)
	}
}

func TestIsPseudo(t *testing.T) {
	pseudo := map[string]int64{
		"proc":       unix.PROC_SUPER_MAGIC,
		"sysfs":      unix.SYSFS_MAGIC,
		"cgroup":     unix.CGROUP_SUPER_MAGIC,
		"cgroup2":    unix.CGROUP2_SUPER_MAGIC,
		"debugfs":    unix.DEBUGFS_MAGIC,
		"tracefs":    unix.TRACEFS_MAGIC,
		"securityfs": unix.SECURITYFS_MAGIC,
		"bpf":        unix.BPF_FS_MAGIC,
		"pstore":     unix.PSTOREFS_MAGIC,
		"overlayfs":  unix.OVERLAYFS_SUPER_MAGIC,
	}
	for name, magic := range pseudo {
		if !IsPseudo(magic) {
			t.Errorf("IsPseudo(%s) = false", name)
		}
	}
	real := map[string]int64{
		"zfs":   MagicZFS,
		"tmpfs": unix.TMPFS_MAGIC,
		"ext4":  unix.EXT4_SUPER_MAGIC,
	}
	for name, magic := range real {
		if IsPseudo(magic) {
			t.Errorf("IsPseudo(%s) = true", name)
		}
	}
}

func TestLstat_Owners(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	st, err := root.Lstat("f")
	if err != nil {
		t.Fatal(err)
	}
	if st.UID != uint32(os.Getuid()) || st.GID != uint32(os.Getgid()) { //nolint:gosec // ids are non-negative
		t.Fatalf("uid/gid = %d/%d, want %d/%d", st.UID, st.GID, os.Getuid(), os.Getgid())
	}
}

func TestLstat_MountRootFalse(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	st, err := root.Lstat("sub")
	if err != nil {
		t.Fatal(err)
	}
	if st.MountRoot {
		t.Fatal("plain subdirectory reported as a mount root")
	}
}

func TestStat_MatchesLstat(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	want, err := root.Lstat("x")
	if err != nil {
		t.Fatal(err)
	}
	c, err := root.OpenChild("x")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	got, err := c.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if got.Dev != want.Dev || got.Ino != want.Ino || got.UID != want.UID || got.GID != want.GID {
		t.Fatalf("Stat = %+v, Lstat = %+v", got, want)
	}
}

func TestIsPseudoType(t *testing.T) {
	for _, n := range []string{"proc", "sysfs", "devtmpfs", "cgroup", "cgroup2", "debugfs",
		"tracefs", "securityfs", "bpf", "pstore", "overlay"} {
		if !IsPseudoType(n) {
			t.Errorf("IsPseudoType(%q) = false", n)
		}
	}
	for _, n := range []string{"tmpfs", "zfs", "ext4"} {
		if IsPseudoType(n) {
			t.Errorf("IsPseudoType(%q) = true", n)
		}
	}
}

// beneathTree builds a/b, a/link -> b (dir symlink) and a/b/c, plus
// outside/ that the symlink-escape cases would reach.
func beneathTree(t *testing.T) (root string, d *Dir) {
	t.Helper()
	root = t.TempDir()
	for _, p := range []string{"a/b/c", "outside"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("b", filepath.Join(root, "a", "link")); err != nil {
		t.Fatal(err)
	}
	d, err := OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return root, d
}

func withSeam(t *testing.T, errno error) {
	t.Helper()
	old := openat2
	openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, errno }
	t.Cleanup(func() { openat2 = old })
}

func testOpenBeneathPath(t *testing.T) {
	_, d := beneathTree(t)
	a, err := d.OpenChild("a")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	want, err := a.Lstat("b")
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.OpenBeneath("a/b")
	if err != nil {
		t.Fatalf("OpenBeneath(a/b): %v", err)
	}
	defer func() { _ = b.Close() }()
	got, err := b.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if got.Dev != want.Dev || got.Ino != want.Ino {
		t.Fatalf("(dev,ino) = (%d,%d), want (%d,%d)", got.Dev, got.Ino, want.Dev, want.Ino)
	}
	self, err := d.OpenBeneath("")
	if err != nil {
		t.Fatalf("OpenBeneath(\"\"): %v", err)
	}
	defer func() { _ = self.Close() }()
	s1, _ := self.Stat()
	s2, _ := d.Stat()
	if s1.Ino != s2.Ino || s1.Dev != s2.Dev {
		t.Fatal("OpenBeneath(\"\") is not d itself")
	}
}

func testOpenBeneathRejectsSymlink(t *testing.T) {
	_, d := beneathTree(t)
	for _, rel := range []string{"a/link", "a/link/c"} {
		x, err := d.OpenBeneath(rel)
		if err == nil {
			_ = x.Close()
			t.Fatalf("OpenBeneath(%q) followed a symlink", rel)
		}
		if !errors.Is(err, unix.ELOOP) {
			t.Fatalf("OpenBeneath(%q) err = %v, want ELOOP", rel, err)
		}
		var pe *fs.PathError
		if !errors.As(err, &pe) || pe.Op != "openat2" {
			t.Fatalf("OpenBeneath(%q) err = %#v, want *fs.PathError op openat2", rel, err)
		}
	}
}

func testOpenBeneathRejectsDotDot(t *testing.T) {
	_, d := beneathTree(t)
	for _, rel := range []string{"..", "a/../b", "/a", "a//b", "a/./b", "a\x00", "a/", "."} {
		x, err := d.OpenBeneath(rel)
		if err == nil {
			_ = x.Close()
			t.Fatalf("OpenBeneath(%q) succeeded", rel)
		}
		if !errors.Is(err, unix.EINVAL) {
			t.Fatalf("OpenBeneath(%q) err = %v, want EINVAL", rel, err)
		}
	}
}

func TestOpenBeneath_Path(t *testing.T)           { testOpenBeneathPath(t) }
func TestOpenBeneath_RejectsSymlink(t *testing.T) { testOpenBeneathRejectsSymlink(t) }
func TestOpenBeneath_RejectsDotDot(t *testing.T)  { testOpenBeneathRejectsDotDot(t) }

func TestOpenBeneath_Fallback(t *testing.T) {
	for name, errno := range map[string]error{"ENOSYS": unix.ENOSYS, "EPERM": unix.EPERM} {
		t.Run(name, func(t *testing.T) {
			withSeam(t, errno)
			t.Run("Path", testOpenBeneathPath)
			t.Run("RejectsSymlink", testOpenBeneathRejectsSymlink)
			t.Run("RejectsDotDot", testOpenBeneathRejectsDotDot)
		})
	}
}

func TestOpenBeneath_OtherErrorNotMasked(t *testing.T) {
	withSeam(t, unix.EACCES)
	_, d := beneathTree(t)
	if x, err := d.OpenBeneath("a"); err == nil || !errors.Is(err, unix.EACCES) {
		if x != nil {
			_ = x.Close()
		}
		t.Fatalf("err = %v, want EACCES with no fallback", err)
	}
}

func TestStatx_EPERMFallback(t *testing.T) {
	root, d := beneathTree(t)
	want, err := d.Lstat("a")
	if err != nil {
		t.Fatal(err)
	}
	for _, errno := range []error{unix.EPERM, unix.ENOSYS} {
		old := statx
		statx = func(int, string, int, int, *unix.Statx_t) error { return errno }
		got, err := d.Lstat("a")
		self, err2 := d.Stat()
		statx = old
		if err != nil || err2 != nil {
			t.Fatalf("%v: Lstat err=%v Stat err=%v", errno, err, err2)
		}
		if got.Ino != want.Ino || !got.IsDir() {
			t.Fatalf("%v: Lstat = %+v, want ino %d", errno, got, want.Ino)
		}
		if !self.IsDir() || self.Ino == 0 {
			t.Fatalf("%v: Stat = %+v", errno, self)
		}
	}
	_ = root
}

func TestStatfs_Capacity(t *testing.T) {
	d, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	info, err := d.Statfs()
	if err != nil {
		t.Fatal(err)
	}
	if info.Avail <= 0 || info.Total < info.Avail {
		t.Fatalf("Total=%d Avail=%d", info.Total, info.Avail)
	}
}
