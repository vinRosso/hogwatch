package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

func TestHealthKind(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		err  bool
	}{
		{"serve", "/hogwatch\x00serve\x00", "serve", false},
		{"scan with flags", "/hogwatch\x00scan\x00--allow-writable\x00", "scan", false},
		{"user create", "/hogwatch\x00user\x00create\x00", "", true},
		{"no subcommand", "/hogwatch\x00", "", true},
		{"empty", "", "", true},
		{"no NUL", "hogwatch serve", "", true},
	}
	for _, c := range cases {
		got, err := healthKind([]byte(c.in))
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%s: healthKind = %q, %v; want %q, err=%v", c.name, got, err, c.want, c.err)
		}
	}
}

func TestListenToURL(t *testing.T) {
	for in, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080",
		"127.0.0.1:9000": "http://127.0.0.1:9000",
		"[::1]:8080":     "http://[::1]:8080",
		"0.0.0.0:8080":   "http://127.0.0.1:8080",
		"[::]:8080":      "http://127.0.0.1:8080",
	} {
		got, err := listenToURL(in)
		if err != nil || got != want {
			t.Errorf("listenToURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := listenToURL("nonsense"); err == nil {
		t.Error("listenToURL(nonsense) = nil error")
	}
}

func TestCheckWeb(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("path = %q, want /healthz", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	listen := strings.TrimPrefix(srv.URL, "http://")
	ctx := context.Background()
	if err := checkWeb(ctx, listen, srv.Client()); err != nil {
		t.Errorf("200: %v", err)
	}
	status = http.StatusInternalServerError
	if err := checkWeb(ctx, listen, srv.Client()); err == nil {
		t.Error("500 passed")
	}
	srv.Close()
	if err := checkWeb(ctx, listen, http.DefaultClient); err == nil {
		t.Error("nothing listening passed")
	}
}

func openMigrated(t *testing.T) (string, *dbq.Queries) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(context.Background(), filepath.Join(dir, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return dir, dbq.New(d)
}

func TestCheckScanner(t *testing.T) {
	dir, q := openMigrated(t)
	ro, err := db.OpenReadOnly(context.Background(), filepath.Join(dir, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	now := time.Unix(1_000_000, 0)
	ctx := context.Background()

	if err := checkScanner(ctx, ro, now); err == nil {
		t.Error("no heartbeat row passed")
	}
	beat := func(age time.Duration) {
		t.Helper()
		if err := q.UpsertHeartbeat(ctx, dbq.UpsertHeartbeatParams{HeartbeatAt: now.Add(-age).Unix(), Version: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	beat(59 * time.Second)
	if err := checkScanner(ctx, ro, now); err != nil {
		t.Errorf("59 s old: %v", err)
	}
	beat(61 * time.Second)
	if err := checkScanner(ctx, ro, now); err == nil {
		t.Error("61 s old passed")
	}
	beat(-time.Hour)
	if err := checkScanner(ctx, ro, now); err == nil {
		t.Error("a heartbeat an hour in the future passed")
	}
	beat(-2 * time.Second)
	if err := checkScanner(ctx, ro, now); err != nil {
		t.Errorf("2 s of clock skew: %v", err)
	}
}

func TestOpenReadOnly_NeverCreates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hogwatch.db")
	if d, err := db.OpenReadOnly(context.Background(), path); err == nil {
		_ = d.Close()
		t.Fatal("OpenReadOnly of a missing file succeeded")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("OpenReadOnly left files behind: %v, %v", entries, err)
	}
}

func TestOpenReadOnly_NoShmNoCreate(t *testing.T) {
	dir, q := openMigrated(t)
	if err := q.UpsertHeartbeat(context.Background(), dbq.UpsertHeartbeatParams{HeartbeatAt: 1, Version: "t"}); err != nil {
		t.Fatal(err)
	}
	// Close the writer so SQLite checkpoints and removes -wal and -shm.
	d, err := db.Open(context.Background(), filepath.Join(dir, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(filepath.Join(dir, "hogwatch.db"+suffix))
	}
	before, _ := os.ReadDir(dir)
	if ro, err := db.OpenReadOnly(context.Background(), filepath.Join(dir, "hogwatch.db")); err == nil {
		_ = ro.Close()
		t.Fatal("OpenReadOnly succeeded with no -wal and -shm")
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before) {
		t.Fatalf("a read-only open changed the directory: %v -> %v", before, after)
	}
}

func runHealth(t *testing.T, cmdline string) (int, string) {
	t.Helper()
	old := procCmdline
	procCmdline = func() ([]byte, error) { return []byte(cmdline), nil }
	t.Cleanup(func() { procCmdline = old })
	var out, errb bytes.Buffer
	code := healthcheck(context.Background(), nil, &out, &errb)
	return code, errb.String()
}

func TestHealthcheck_ExitCodes(t *testing.T) {
	cleanEnv(t)
	dir, q := openMigrated(t)
	t.Setenv("HOGWATCH_DATA_DIR", dir)

	// Scanner: stale then fresh heartbeat.
	if code, msg := runHealth(t, "/hogwatch\x00scan\x00"); code != 1 || msg == "" {
		t.Errorf("scan without heartbeat: exit %d, stderr %q; want 1 with a message", code, msg)
	}
	if err := q.UpsertHeartbeat(context.Background(), dbq.UpsertHeartbeatParams{HeartbeatAt: time.Now().Unix(), Version: "t"}); err != nil {
		t.Fatal(err)
	}
	if code, msg := runHealth(t, "/hogwatch\x00scan\x00"); code != 0 {
		t.Errorf("scan with heartbeat: exit %d: %s", code, msg)
	}

	// Web: a live /healthz, then nothing listening.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Setenv("HOGWATCH_LISTEN", strings.TrimPrefix(srv.URL, "http://"))
	if code, msg := runHealth(t, "/hogwatch\x00serve\x00"); code != 0 {
		t.Errorf("serve up: exit %d: %s", code, msg)
	}
	srv.Close()
	if code, _ := runHealth(t, "/hogwatch\x00serve\x00"); code != 1 {
		t.Errorf("serve down: exit %d, want 1", code)
	}

	// Other commands are not a service.
	if code, _ := runHealth(t, "/hogwatch\x00user\x00create\x00"); code != 1 {
		t.Errorf("user: exit %d, want 1", code)
	}
}

func TestVersionCommand(t *testing.T) {
	code, out, _ := runCmd("version")
	if code != 0 || !strings.HasPrefix(out, "hogwatch ") {
		t.Fatalf("version: exit %d, stdout %q", code, out)
	}
}
