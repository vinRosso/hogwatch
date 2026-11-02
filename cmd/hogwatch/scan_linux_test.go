package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/vinRosso/hogwatch/internal/config"
	_ "modernc.org/sqlite"
)

func runScan(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = scan(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

// scanTree makes 3 directories (the root included) and 5 files.
func scanTree(t *testing.T) string { return benchTree(t) }

func TestScanCmd_Usage(t *testing.T) {
	tree, data := scanTree(t), t.TempDir()
	tooMany := strconv.Itoa(config.WorkerCap(runtime.GOMAXPROCS(0)) + 1)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"service mode with once-only flag", []string{"--root", tree}, "only applies with --once"},
		{"no root", []string{"--once", "--data", data}, "--root is required"},
		{"extra arg", []string{"--once", "--root", tree, "stray"}, "unexpected argument"},
		{"workers zero", []string{"--once", "--root", tree, "--workers", "0"}, "--workers must be between"},
		{"workers above cap", []string{"--once", "--root", tree, "--workers", tooMany}, "--workers must be between"},
		{"bad anchor", []string{"--once", "--root", tree, "--anchor", "a/../b"}, "--anchor"},
		{"bad root id", []string{"--once", "--root", tree, "--root-id", "0"}, "must be >= 1"},
		{"bad top-n", []string{"--once", "--root", tree, "--top-n", "0"}, "--top-n"},
		{"unknown flag", []string{"--once", "--nope"}, "flag provided but not defined"},
	}
	for _, c := range cases {
		code, _, stderr := runScan(append([]string{"--data", data}, c.args...)...) // --data first: no case may fall back to /data
		if code != 2 {
			t.Errorf("%s: exit = %d, want 2 (stderr %q)", c.name, code, stderr)
		}
		if !strings.Contains(stderr, c.want) {
			t.Errorf("%s: stderr = %q, want it to contain %q", c.name, stderr, c.want)
		}
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Errorf("a usage error created %d entries in the data dir", len(entries))
	}
}

func TestScanCmd_HappyRun(t *testing.T) {
	tree, data := scanTree(t), t.TempDir()
	code, stdout, stderr := runScan("--once", "--root", tree, "--data", data,
		"--allow-writable-mounts", "--verify", "--json", "--anchor", "a")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	var rep scanReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if rep.PeakRSSBytes < 1<<20 {
		t.Errorf("peak_rss_bytes = %d, want at least 1 MiB for a Go process", rep.PeakRSSBytes)
	}
	if !strings.Contains(stdout, `"peak_rss_bytes"`) {
		t.Errorf("JSON has no peak_rss_bytes key:\n%s", stdout)
	}
	want := filepath.Join(data, "scans", "1-1.db")
	if rep.Path != want || !rep.Verified {
		t.Errorf("path, verified = %q, %v, want %q, true", rep.Path, rep.Verified, want)
	}
	if rep.Stats.Walk.Files != 5 || rep.Stats.Walk.Dirs != 3 {
		t.Errorf("files, dirs = %d, %d, want 5, 3", rep.Stats.Walk.Files, rep.Stats.Walk.Dirs)
	}

	db, err := sql.Open("sqlite", "file:"+want+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int64
	if err := db.QueryRow("SELECT count(*) FROM dirs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 || rep.Stats.DirRows != n {
		t.Errorf("count(dirs) = %d, JSON dir_rows = %d, want 3 both", n, rep.Stats.DirRows)
	}
}

func TestScanCmd_TextOutput(t *testing.T) {
	code, stdout, stderr := runScan("--once", "--root", scanTree(t), "--data", t.TempDir(), "--allow-writable-mounts")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	for _, s := range []string{"generation", "dir rows", "files"} {
		if !strings.Contains(stdout, s) {
			t.Errorf("text output lacks %q:\n%s", s, stdout)
		}
	}
}

func TestScanCmd_ExistingGeneration(t *testing.T) {
	tree, data := scanTree(t), t.TempDir()
	path := filepath.Join(data, "scans", "1-1.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runScan("--once", "--root", tree, "--data", data, "--allow-writable-mounts")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr %q)", code, stderr)
	}
	got, _ := os.ReadFile(path) //nolint:gosec // test temp dir
	if string(got) != "keep me" {
		t.Errorf("existing generation changed: %q", got)
	}
}

func TestScanCmd_Interrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := t.TempDir()
	var out, errb bytes.Buffer
	code := scan(ctx, []string{"--once", "--root", scanTree(t), "--data", data, "--allow-writable-mounts"}, &out, &errb)
	if code != 130 {
		t.Fatalf("exit = %d, want 130 (stderr %q)", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(data, "scans", "1-1.db")); !os.IsNotExist(err) {
		t.Errorf("an interrupted scan left a file (stat err %v)", err)
	}
}

func TestScanCmd_InterruptedDuringVerify(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := verifyGeneration
	t.Cleanup(func() { verifyGeneration = old })
	verifyGeneration = func(ctx context.Context, _ string) error {
		cancel() // the signal arrives while the invariants are checked
		return fmt.Errorf("build: verify: %w", ctx.Err())
	}
	data := t.TempDir()
	var out, errb bytes.Buffer
	code := scan(ctx, []string{"--once", "--verify", "--root", scanTree(t), "--data", data, "--allow-writable-mounts"}, &out, &errb)
	if code != 130 {
		t.Fatalf("exit = %d, want 130 (stderr %q)", code, errb.String())
	}
}

func TestScanCmd_MissingRoot(t *testing.T) {
	code, _, _ := runScan("--once", "--root", filepath.Join(t.TempDir(), "nope"), "--data", t.TempDir())
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
