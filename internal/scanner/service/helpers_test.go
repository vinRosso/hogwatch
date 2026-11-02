//go:build linux

package service

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/db"
)

// fakeClock is a settable clock safe for concurrent use.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newDB returns a migrated main DB and a temp data dir.
func newDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(context.Background(), filepath.Join(dir, "hogwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.EnsureMigrated(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d, dir
}

func exec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func count(t *testing.T, d *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// addRoot inserts a root and returns its id.
func addRoot(t *testing.T, d *sql.DB, name, schedule string, enabled bool) int64 {
	t.Helper()
	en := 0
	if enabled {
		en = 1
	}
	res, err := d.ExecContext(context.Background(),
		`INSERT INTO scan_roots (name, container_path, host_path, schedule, enabled) VALUES (?, ?, ?, ?, ?)`,
		name, "/host/"+name, "/"+name, schedule, en)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// addJob inserts a job and returns its id.
func addJob(t *testing.T, d *sql.DB, kind string, root int64, status, params string) int64 {
	t.Helper()
	res, err := d.ExecContext(context.Background(),
		`INSERT INTO jobs (kind, root_id, params, status, created_at) VALUES (?, ?, ?, ?, 1)`,
		kind, root, params, status)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func jobStatus(t *testing.T, d *sql.DB, id int64) (status, errMsg string) {
	t.Helper()
	var e sql.NullString
	if err := d.QueryRowContext(context.Background(), `SELECT status, error FROM jobs WHERE id = ?`, id).Scan(&status, &e); err != nil {
		t.Fatal(err)
	}
	return status, e.String
}

// waitFor polls cond until it holds or a generous deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForWithin is waitFor with a caller-chosen deadline, for work that is slow
// under a loaded machine (the race detector, parallel test packages).
func waitForWithin(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
