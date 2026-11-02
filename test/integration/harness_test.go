//go:build integration && linux

package integration

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

const tmpfsMagic = 0x01021994

func TestHarness_PrivilegedTmpfs(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("run via make test-integration")
	}
	dir := t.TempDir()
	if err := syscall.Mount("tmpfs", dir, "tmpfs", 0, "size=1m"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Unmount(dir, 0) })

	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	if st.Type != tmpfsMagic {
		t.Fatalf("fs type = %#x, want tmpfs %#x", st.Type, tmpfsMagic)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}
