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

// The periodic sweep removes the generation files of a deleted root (no scan
// row) and of failed scans, and leaves a done scan, a running scan (its file
// is being written), sort spill files and unrecognised entries alone.
func TestSweepOrphans_KeepsRunning(t *testing.T) {
	clk := newClock(t0)
	o := pipelineOptions(t, clk)
	root := addRootAt(t, o.DB, "r", t.TempDir())
	exec(t, o.DB, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (1, ?, '1-1.db', 'done', 'manual', 1, 2)`, root)
	exec(t, o.DB, `INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (2, ?, '1-2.db', 'running', 'manual', 3)`, root)
	exec(t, o.DB, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (3, ?, '1-3.db', 'failed', 'manual', 4, 5)`, root)
	exec(t, o.DB, `UPDATE scan_roots SET current_scan_id = 1 WHERE id = ?`, root)
	dir := filepath.Join(o.DataDir, "scans")
	must(t, os.MkdirAll(dir, 0o750))
	write := func(name string) {
		t.Helper()
		must(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}
	write(filepath.Base(gen.Path(o.DataDir, root, 1))) // done
	write(filepath.Base(gen.Path(o.DataDir, root, 2))) // running
	write(filepath.Base(gen.Path(o.DataDir, root, 3))) // failed
	write("9-9.db")                                    // its root was deleted: no row
	write("notes.txt")
	write("etilqs_abc")
	s := newService(o)

	s.cleanup(context.Background(), t0.Add(time.Hour))

	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	for name, want := range map[string]bool{
		"1-1.db": true, "1-2.db": true, "1-3.db": false, "9-9.db": false, "notes.txt": true, "etilqs_abc": true,
	} {
		if got := exists(name); got != want {
			t.Errorf("%s exists = %v, want %v", name, got, want)
		}
	}
}
