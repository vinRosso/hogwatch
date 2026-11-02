//go:build linux

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/gen"
)

func TestCleanup_MissingFileAndExpiredJobs(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	o.Grace = time.Minute
	root := addRootAt(t, o.DB, "r", t.TempDir())
	// A superseded scan whose file is already gone, and a current scan.
	exec(t, o.DB, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (1, ?, '1-1.db', 'done', 'manual', 1, 2)`, root)
	exec(t, o.DB, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (2, ?, '1-2.db', 'done', 'manual', 3, ?)`, root, t0.Unix())
	exec(t, o.DB, `UPDATE scan_roots SET current_scan_id = 2 WHERE id = ?`, root)
	must(t, os.MkdirAll(filepath.Join(o.DataDir, "scans"), 0o750))
	must(t, os.WriteFile(gen.Path(o.DataDir, root, 2), []byte("x"), 0o600))
	// Jobs: expired done, not yet expired done, expired but still running.
	exec(t, o.DB, `INSERT INTO jobs (id, kind, root_id, status, created_at, expires_at) VALUES (10, 'listing', ?, 'done', 1, ?)`, root, t0.Unix()-1)
	exec(t, o.DB, `INSERT INTO jobs (id, kind, root_id, status, created_at, expires_at) VALUES (11, 'listing', ?, 'done', 1, ?)`, root, t0.Add(2*time.Hour).Unix())
	exec(t, o.DB, `INSERT INTO jobs (id, kind, root_id, status, created_at, expires_at) VALUES (12, 'listing', ?, 'running', 1, ?)`, root, t0.Unix()-1)
	s := newService(o)

	s.cleanup(context.Background(), t0.Add(time.Hour))
	if st, _ := scanStatus(t, o.DB, 1); st != "deleted" {
		t.Errorf("scan 1 = %q, want deleted even though its file was already gone", st)
	}
	if st, _ := scanStatus(t, o.DB, 2); st != "done" {
		t.Errorf("scan 2 = %q, want done", st)
	}
	if _, err := os.Stat(gen.Path(o.DataDir, root, 2)); err != nil {
		t.Errorf("current generation: %v", err)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM jobs WHERE id = 10`); n != 0 {
		t.Error("expired job 10 survived cleanup")
	}
	if n := count(t, o.DB, `SELECT count(*) FROM jobs WHERE id IN (11, 12)`); n != 2 {
		t.Error("cleanup deleted a live or unexpired job")
	}
}
