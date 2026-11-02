package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// benchTree makes a tree of 3 directories (the root included) and 5 files.
func benchTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, p), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"f1", "a/f2", "a/f3", "b/f4", "b/f5"} {
		if err := os.WriteFile(filepath.Join(root, p), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runBench(ctx context.Context, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = bench(ctx, args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestBench_JSON(t *testing.T) {
	code, stdout, stderr := runBench(context.Background(), "-json", "-progress", "0", benchTree(t))
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	var r benchReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not a JSON report: %v\n%s", err, stdout)
	}
	if r.Dirs != 3 || r.Files != 5 {
		t.Errorf("dirs, files = %d, %d, want 3, 5", r.Dirs, r.Files)
	}
	if want := max(1, runtime.GOMAXPROCS(0)/2); r.Workers != want {
		t.Errorf("workers = %d, want %d", r.Workers, want)
	}
	if r.GOMAXPROCS != runtime.GOMAXPROCS(0) {
		t.Errorf("gomaxprocs = %d, want %d", r.GOMAXPROCS, runtime.GOMAXPROCS(0))
	}
	if r.Interrupted {
		t.Error("interrupted = true, want false")
	}
	if r.FSType == "" {
		t.Error("fs_type is empty")
	}
	if r.PeakRSSBytes <= 0 {
		t.Errorf("peak_rss_bytes = %d, want > 0", r.PeakRSSBytes)
	}
}

func TestBench_Text(t *testing.T) {
	code, stdout, stderr := runBench(context.Background(), "-progress", "0", benchTree(t))
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{"dirs", "files/s", "peak RSS"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestBench_Usage(t *testing.T) {
	root := benchTree(t)
	for name, args := range map[string][]string{
		"no path":    {},
		"two paths":  {root, root},
		"workers 0":  {"-workers", "0", root},
		"bad flag":   {"-nope", root},
		"neg period": {"-progress", "-1s", root},
	} {
		code, _, stderr := runBench(context.Background(), args...)
		if code != 2 {
			t.Errorf("%s: exit = %d, want 2 (stderr %q)", name, code, stderr)
		}
		if stderr == "" {
			t.Errorf("%s: stderr is empty, want a message", name)
		}
	}
}

func TestBench_Help(t *testing.T) {
	code, _, stderr := runBench(context.Background(), "-h")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stderr, "-workers") {
		t.Errorf("stderr = %q, want flag usage", stderr)
	}
}

func TestBench_MissingRoot(t *testing.T) {
	code, stdout, stderr := runBench(context.Background(), "-progress", "0", filepath.Join(t.TempDir(), "nope"))
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.HasPrefix(stderr, "hogwatch bench: ") {
		t.Errorf("stderr = %q, want prefix %q", stderr, "hogwatch bench: ")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestBench_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, stdout, stderr := runBench(ctx, "-json", "-progress", "0", benchTree(t))
	if code != 130 {
		t.Fatalf("exit = %d, want 130 (stderr %q)", code, stderr)
	}
	var r benchReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not a JSON report: %v\n%s", err, stdout)
	}
	if !r.Interrupted {
		t.Error("interrupted = false, want true")
	}
}

func TestBench_Progress(t *testing.T) {
	code, _, stderr := runBench(context.Background(), "-json", "-progress", "1ms", benchTree(t))
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	// Scan always makes one final snapshot call.
	if !strings.Contains(stderr, "progress elapsed=") || !strings.Contains(stderr, "dirs/s=") {
		t.Errorf("stderr = %q, want a progress line", stderr)
	}
}

func TestFSTypeName(t *testing.T) {
	for magic, want := range map[int64]string{
		0x2fc12fc1:   "zfs",
		0xef53:       "ext4",
		0x58465342:   "xfs",
		0x9123683e:   "btrfs",
		0x01021994:   "tmpfs",
		0x794c7630:   "overlay",
		0x1234:       "0x1234",
		0x6969:       "0x6969",
		0x65735546:   "0x65735546",
		0x7f7f7f7f7f: "0x7f7f7f7f7f",
	} {
		if got := fsTypeName(magic); got != want {
			t.Errorf("fsTypeName(%#x) = %q, want %q", magic, got, want)
		}
	}
}

func TestGroup(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234",
	} {
		if got := group(in); got != want {
			t.Errorf("group(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestBench_RefusesWritableRoot(t *testing.T) {
	// A temp dir is on a writable filesystem.
	code, stdout, stderr := runBench(context.Background(), "-allow-writable-mounts=false", "-progress", "0", benchTree(t))
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, "writable") || stdout != "" {
		t.Errorf("stderr = %q, stdout = %q; want a writable-root message and no report", stderr, stdout)
	}
}
