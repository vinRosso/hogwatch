//go:build linux

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecover_FailsAndCleans(t *testing.T) {
	d, dir := newDB(t)
	clk := newClock(time.Unix(1_700_000_000, 0))
	root := addRoot(t, d, "a", "", true)
	scansDir := filepath.Join(dir, "scans")
	if err := os.MkdirAll(scansDir, 0o750); err != nil {
		t.Fatal(err)
	}
	touch := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(scansDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	scanJob := addJob(t, d, "scan", root, "running", `{"trigger":"manual"}`)
	exec(t, d, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, job_id) VALUES (1, ?, '1-1.db', 'running', 'manual', 1, ?)`, root, scanJob)
	exec(t, d, `INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (2, ?, '1-2.db', 'done', 'manual', 1)`, root)
	touch("1-1.db")
	touch("1-2.db")
	touch("9-99.db")
	touch("notes.txt")
	// Scan id 2 exists but under another root: not the done row's file.
	touch("2-2.db")

	listing := addJob(t, d, "listing", root, "running", `{"mode":"children"}`)
	for i := range 3 {
		exec(t, d, `INSERT INTO listing_results (job_id, seq, rel_name, type, size, alloc, mtime, uid) VALUES (?, ?, x'66', 'file', 1, 1, 1, 0)`, listing, i)
	}
	queued := addJob(t, d, "scan", root, "queued", `{"trigger":"manual"}`)

	o := Options{DB: d, DataDir: dir, ListingTTL: time.Hour, Now: clk.Now, Log: quietLog()}
	if err := Recover(context.Background(), o); err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{scanJob, listing} {
		if st, msg := jobStatus(t, d, id); st != "failed" || msg != "scanner restarted" {
			t.Errorf("job %d = %q %q, want failed/scanner restarted", id, st, msg)
		}
	}
	if st, _ := jobStatus(t, d, queued); st != "queued" {
		t.Errorf("queued job = %q, want untouched", st)
	}
	if n := count(t, d, `SELECT count(*) FROM scans WHERE id = 1 AND status = 'failed'`); n != 1 {
		t.Error("running scans row not failed")
	}
	if n := count(t, d, `SELECT count(*) FROM scans WHERE id = 2 AND status = 'done'`); n != 1 {
		t.Error("done scans row changed")
	}
	for name, want := range map[string]bool{"1-1.db": false, "9-99.db": false, "2-2.db": false, "1-2.db": true, "notes.txt": true} {
		_, err := os.Stat(filepath.Join(scansDir, name))
		if got := err == nil; got != want {
			t.Errorf("%s exists = %v, want %v", name, got, want)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM listing_results WHERE job_id = ?`, listing); n != 0 {
		t.Errorf("listing rows = %d, want 0", n)
	}
	if n := count(t, d, `SELECT count(*) FROM jobs WHERE id = ? AND expires_at = ?`, listing, clk.Now().Add(time.Hour).Unix()); n != 1 {
		t.Error("listing expires_at not set to now + ListingTTL")
	}
}

func TestRecover_NoScansDir(t *testing.T) {
	d, dir := newDB(t)
	o := Options{DB: d, DataDir: dir, ListingTTL: time.Hour, Now: time.Now, Log: quietLog()}
	if err := Recover(context.Background(), o); err != nil {
		t.Fatalf("Recover without a scans dir = %v", err)
	}
}

func TestRecover_CanonicalNamesOnly(t *testing.T) {
	d, dir := newDB(t)
	root := addRoot(t, d, "a", "", true)
	exec(t, d, `INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (2, ?, '1-2.db', 'done', 'manual', 1)`, root)
	scansDir := filepath.Join(dir, "scans")
	if err := os.MkdirAll(scansDir, 0o750); err != nil {
		t.Fatal(err)
	}
	names := []string{"1-2.db", "01-2.db", "1-02.db", "99999999999999999999-1.db", "etilqs_abc123"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(scansDir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	o := Options{DB: d, DataDir: dir, ListingTTL: time.Hour, Now: time.Now, Log: quietLog()}
	if err := Recover(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		_, err := os.Stat(filepath.Join(scansDir, n))
		if got, want := err == nil, n == "1-2.db"; got != want {
			t.Errorf("%s exists = %v, want %v", n, got, want)
		}
	}
}
