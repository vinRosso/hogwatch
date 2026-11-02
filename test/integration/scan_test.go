//go:build integration && linux

package integration

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// buildBinary compiles cmd/hogwatch into a temp dir.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "hogwatch")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/hogwatch") //nolint:gosec // fixed arguments
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// runBinary runs bin and returns its exit code and stderr.
func runBinary(t *testing.T, bin string, args ...string) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(bin, args...) //nolint:gosec // the binary this test built
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), stderr.String()
	}
	t.Fatalf("run %s: %v", bin, err)
	return -1, ""
}

func TestScanOnce_EndToEnd(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("run via make test-integration")
	}
	bin := buildBinary(t)
	tree := t.TempDir()
	if err := syscall.Mount("tmpfs", tree, "tmpfs", 0, "size=16m"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Unmount(tree, 0) })
	for _, d := range []string{"a", "a/b", "c"} {
		if err := os.MkdirAll(filepath.Join(tree, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for p, body := range map[string]string{"f1.txt": "hello", "a/f2.log": "world!", "a/b/f3": "", "c/f4.txt": "x"} {
		if err := os.WriteFile(filepath.Join(tree, p), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(tree, "f1.txt"), filepath.Join(tree, "c", "hard")); err != nil {
		t.Fatal(err)
	}

	// Writable: refused.
	data := t.TempDir()
	code, stderr := runBinary(t, bin, "scan", "--once", "--root", tree, "--data", data, "--verify")
	if code != 1 || !strings.Contains(stderr, "root filesystem is writable") {
		t.Fatalf("writable root: exit %d, stderr %q, want 1 and 'root filesystem is writable'", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(data, "scans", "1-1.db")); !os.IsNotExist(err) {
		t.Errorf("a refused scan left a file (stat err %v)", err)
	}

	// Read-only: scanned and verified without --allow-writable-mounts.
	if err := syscall.Mount("", tree, "", syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
		t.Fatalf("remount read-only: %v", err)
	}
	code, stderr = runBinary(t, bin, "scan", "--once", "--root", tree, "--data", data, "--verify")
	if code != 0 {
		t.Fatalf("read-only root: exit %d, stderr %q, want 0", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(data, "scans", "1-1.db")); err != nil {
		t.Errorf("generation missing: %v", err)
	}
}
