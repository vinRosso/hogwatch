package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// helperEnv makes the test binary act as the hogwatch binary: TestMain runs
// main's run function with the arguments in the variable and exits with its code.
const helperEnv = "HOGWATCH_TEST_RUN_ARGS"

func TestMain(m *testing.M) {
	if args, ok := os.LookupEnv(helperEnv); ok {
		os.Exit(run(context.Background(), strings.Fields(args), os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// startHogwatch starts this test binary as `hogwatch <args>` with env added.
func startHogwatch(t *testing.T, args string, env ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0]) //nolint:gosec // the running test binary
	cmd.Env = append(os.Environ(), append([]string{helperEnv + "=" + args}, env...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, &out
}

func exitCode(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		switch {
		case err == nil:
			return 0
		case errors.As(err, &ee):
			return ee.ExitCode()
		}
		t.Fatal(err)
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the process did not exit")
	}
	return -1
}

func TestScanCmd_ServiceMode(t *testing.T) {
	data := t.TempDir()
	cmd, out := startHogwatch(t, "scan", "HOGWATCH_DATA_DIR="+data, "HOGWATCH_CONFIG=")

	// The service has started once it has published its status row.
	d, err := sql.Open("sqlite", filepath.Join(data, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM scanner_status`).Scan(&n); err == nil && n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the service did not publish scanner_status; output:\n%s", out)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := exitCode(t, cmd); code != 0 {
		t.Fatalf("exit after SIGTERM = %d, want 0; output:\n%s", code, out)
	}
}

func TestScanCmd_ServiceModeBadConfig(t *testing.T) {
	cmd, out := startHogwatch(t, "scan", "HOGWATCH_DATA_DIR="+t.TempDir(), "HOGWATCH_CONFIG=", "HOGWATCH_SCAN_WORKERS=-1")
	if code := exitCode(t, cmd); code != 1 {
		t.Fatalf("exit with a bad config = %d, want 1; output:\n%s", code, out)
	}
	if !strings.Contains(out.String(), "scan.workers") {
		t.Errorf("output = %q, want it to name scan.workers", out)
	}
}

func TestScanCmd_ServiceModeUnwritableData(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A data dir that is a file cannot hold hogwatch.db: a startup error.
	cmd, out := startHogwatch(t, "scan", "HOGWATCH_DATA_DIR="+file, "HOGWATCH_CONFIG=")
	if code := exitCode(t, cmd); code != 1 {
		t.Fatalf("exit with an unusable data dir = %d, want 1; output:\n%s", code, out)
	}
}

func TestScanCmd_ServiceModeStrayArg(t *testing.T) {
	code, _, stderr := runScan("stray")
	if code != 2 || !strings.Contains(stderr, "unexpected argument") {
		t.Fatalf("exit = %d, stderr %q, want 2 and 'unexpected argument'", code, stderr)
	}
}

func TestScanCmd_ServiceModeSignalDuringStartup(t *testing.T) {
	t.Setenv("HOGWATCH_DATA_DIR", t.TempDir())
	t.Setenv("HOGWATCH_CONFIG", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the signal arrived before the database was open
	var out, errb bytes.Buffer
	if code := scan(ctx, nil, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb.String())
	}
}
