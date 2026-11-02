//go:build linux

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/gate"
)

// listingOptions is pipelineOptions with the listing pool wired in.
func listingOptions(t *testing.T, clk *fakeClock) Options {
	t.Helper()
	o := pipelineOptions(t, clk)
	o.hooks.startListing = startListing
	return o
}

// addAdmin inserts an admin user and returns its id.
func addAdmin(t *testing.T, d *sql.DB) int64 {
	t.Helper()
	res, err := d.Exec(`INSERT INTO users (username, admin_local, can_list_files, created_at) VALUES ('admin', 1, 1, 1)`)
	must(t, err)
	id, _ := res.LastInsertId()
	return id
}

// rootNode is the reference of the scan root directory of generation (root, scan).
func rootNode(t *testing.T, dataDir string, root, scan int64) string {
	t.Helper()
	g, _, err := gen.OpenReadOnly(context.Background(), gen.Path(dataDir, root, scan))
	must(t, err)
	defer func() { _ = g.Close() }()
	d, err := gen.Lookup(context.Background(), g, nil)
	must(t, err)
	return fmt.Sprintf("%d.%d", scan, d.ID)
}

// queueListing inserts a queued children listing of node and returns its id.
func queueListing(t *testing.T, d *sql.DB, root, user int64, node string) int64 {
	t.Helper()
	raw, err := json.Marshal(db.ListingParams{Mode: "children"})
	must(t, err)
	id, err := dbq.New(d).InsertJob(context.Background(), dbq.InsertJobParams{
		Kind: "listing", RootID: root, TargetNode: sql.NullString{String: node, Valid: true},
		Params: string(raw), RequestedBy: sql.NullInt64{Int64: user, Valid: true}, CreatedAt: 1,
	})
	must(t, err)
	return id
}

func jobRow(t *testing.T, d *sql.DB, id int64) (status, errMsg, progress string, expires sql.NullInt64) {
	t.Helper()
	var e, p sql.NullString
	must(t, d.QueryRow(`SELECT status, error, progress, expires_at FROM jobs WHERE id = ?`, id).Scan(&status, &e, &p, &expires))
	return status, e.String, p.String, expires
}

func TestService_ListingDuringScan(t *testing.T) {
	clk := newClock(t0)
	o := listingOptions(t, clk)
	o.Gate = gate.New(2)

	small := t.TempDir()
	put(t, small, "a.txt", 10)
	put(t, small, "b.txt", 20)
	rootA := addRootAt(t, o.DB, "small", small)
	admin := addAdmin(t, o.DB)
	if _, err := runScanJob(t, newService(o), rootA); err != nil {
		t.Fatal(err)
	}
	node := rootNode(t, o.DataDir, rootA, 1)

	big := t.TempDir() // 20,000 directories
	for i := 0; i < 200; i++ {
		for j := 0; j < 100; j++ {
			must(t, os.MkdirAll(filepath.Join(big, fmt.Sprintf("d%03d", i), fmt.Sprintf("e%03d", j)), 0o750))
		}
	}
	rootB := addRootAt(t, o.DB, "big", big)

	// The scan is held at the start of its finalize phase until the listing is
	// done, so the scan is certainly still running when the listing finishes.
	listingDone := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(listingDone) }) }
	o.hooks.onPhase = func(phase string) {
		if phase == "finalize" {
			<-listingDone
		}
	}

	scanJob := addJob(t, o.DB, "scan", rootB, "queued", `{"trigger":"manual"}`)
	h := startRun(t, o)
	t.Cleanup(release) // runs before the stop registered by startRun
	waitForWithin(t, time.Minute, "the scan to report progress", func() bool {
		var p sql.NullString
		must(t, o.DB.QueryRow(`SELECT progress FROM jobs WHERE id = ?`, scanJob).Scan(&p))
		return p.Valid
	})
	listJob := queueListing(t, o.DB, rootA, admin, node)

	waitForWithin(t, time.Minute, "the listing to finish", func() bool { st, _ := jobStatus(t, o.DB, listJob); return st == "done" })
	if st, _ := jobStatus(t, o.DB, scanJob); st != "running" {
		t.Errorf("scan job = %q when the listing finished, want running (the listing must not wait for the scan)", st)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM listing_results WHERE job_id = ?`, listJob); n != 2 {
		t.Errorf("listing rows = %d, want 2", n)
	}
	release()
	waitForWithin(t, 2*time.Minute, "the scan to finish", func() bool { st, _ := jobStatus(t, o.DB, scanJob); return st == "done" })
	h.stop(t)
	if m := o.Gate.Peak(); m > 2 {
		t.Errorf("gate peak = %d, want <= 2", m)
	}
}

func TestService_ListingExpiry(t *testing.T) {
	clk := newClock(t0)
	o := listingOptions(t, clk)
	dir := t.TempDir()
	put(t, dir, "a.txt", 10)
	root := addRootAt(t, o.DB, "r", dir)
	admin := addAdmin(t, o.DB)
	if _, err := runScanJob(t, newService(o), root); err != nil {
		t.Fatal(err)
	}
	job := queueListing(t, o.DB, root, admin, rootNode(t, o.DataDir, root, 1))

	h := startRun(t, o)
	waitFor(t, "the listing to finish", func() bool { st, _ := jobStatus(t, o.DB, job); return st == "done" })
	st, msg, progress, expires := jobRow(t, o.DB, job)
	if st != "done" || msg != "" {
		t.Fatalf("listing = %q %q, want done", st, msg)
	}
	if want := t0.Add(o.ListingTTL).Unix(); !expires.Valid || expires.Int64 != want {
		t.Errorf("expires_at = %v, want %d (finish + ListingTTL)", expires, want)
	}
	var p db.ListingProgress
	must(t, json.Unmarshal([]byte(progress), &p))
	if p.Rows != 1 {
		t.Errorf("progress = %+v, want rows 1", p)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM listing_results WHERE job_id = ?`, job); n != 1 {
		t.Fatalf("listing rows = %d, want 1", n)
	}

	clk.Advance(o.ListingTTL + 2*time.Minute) // past expires_at and past the next cleanup
	waitFor(t, "the expired listing to be deleted", func() bool {
		return count(t, o.DB, `SELECT count(*) FROM jobs WHERE id = ?`, job) == 0
	})
	h.stop(t)
	if n := count(t, o.DB, `SELECT count(*) FROM listing_results WHERE job_id = ?`, job); n != 0 {
		t.Errorf("an expired listing left %d rows", n)
	}
}

func TestService_ListingFixedErrors(t *testing.T) {
	clk := newClock(t0)
	o := listingOptions(t, clk)
	dir := t.TempDir()
	put(t, dir, "a.txt", 10)
	root := addRootAt(t, o.DB, "r", dir)
	admin := addAdmin(t, o.DB)
	res, err := o.DB.Exec(`INSERT INTO users (username, can_list_files, created_at) VALUES ('plain', 0, 1)`)
	must(t, err)
	plain, _ := res.LastInsertId()
	if _, err := runScanJob(t, newService(o), root); err != nil {
		t.Fatal(err)
	}
	node := rootNode(t, o.DataDir, root, 1)

	gone := queueListing(t, o.DB, root, admin, "1.99999")
	denied := queueListing(t, o.DB, root, plain, node)
	badMode := queueListingParams(t, o.DB, root, admin, node, `{"mode":"bogus"}`)
	h := startRun(t, o)
	for _, id := range []int64{gone, denied, badMode} {
		waitFor(t, "a listing to end", func() bool { st, _ := jobStatus(t, o.DB, id); return st == "failed" })
	}
	h.stop(t)
	for id, want := range map[int64]string{gone: "scan no longer available", denied: "not permitted", badMode: "listing failed"} {
		if st, msg := jobStatus(t, o.DB, id); st != "failed" || msg != want {
			t.Errorf("job %d = %q %q, want failed %q", id, st, msg, want)
		}
		if _, _, _, exp := jobRow(t, o.DB, id); !exp.Valid {
			t.Errorf("job %d has no expires_at", id)
		}
	}
}

// holdGate takes every token of g and returns a function that gives them back.
func holdGate(t *testing.T, g *gate.Gate, n int) (release func()) {
	t.Helper()
	for i := 0; i < n; i++ {
		must(t, g.High().Acquire(context.Background()))
	}
	return func() {
		for i := 0; i < n; i++ {
			g.High().Release()
		}
	}
}

func TestService_ListingCancelAndShutdown(t *testing.T) {
	clk := newClock(t0)
	o := listingOptions(t, clk)
	o.Gate = gate.New(1)
	dir := t.TempDir()
	put(t, dir, "a.txt", 10)
	root := addRootAt(t, o.DB, "r", dir)
	admin := addAdmin(t, o.DB)
	if _, err := runScanJob(t, newService(o), root); err != nil {
		t.Fatal(err)
	}
	node := rootNode(t, o.DataDir, root, 1)
	release := holdGate(t, o.Gate, 1) // the listing blocks in its first directory read
	defer release()

	cancelled := queueListing(t, o.DB, root, admin, node)
	h := startRun(t, o)
	waitFor(t, "the listing to start", func() bool { st, _ := jobStatus(t, o.DB, cancelled); return st == "running" })
	addJob(t, o.DB, "cancel", root, "queued", `{"job_id":`+itoa(cancelled)+`}`)
	waitFor(t, "the listing to be cancelled", func() bool { st, _ := jobStatus(t, o.DB, cancelled); return st == "cancelled" })
	if st, msg := jobStatus(t, o.DB, cancelled); st != "cancelled" || msg != "" {
		t.Errorf("user-cancelled listing = %q %q, want cancelled with no message", st, msg)
	}
	if _, _, _, exp := jobRow(t, o.DB, cancelled); !exp.Valid {
		t.Error("cancelled listing has no expires_at")
	}

	stopped := queueListing(t, o.DB, root, admin, node)
	waitFor(t, "the second listing to start", func() bool { st, _ := jobStatus(t, o.DB, stopped); return st == "running" })
	h.stop(t) // the service shuts down under the listing
	if st, msg := jobStatus(t, o.DB, stopped); st != "failed" || msg != "scanner stopped" {
		t.Errorf("listing at shutdown = %q %q, want failed 'scanner stopped'", st, msg)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM listing_results`); n != 0 {
		t.Errorf("stopped listings left %d rows", n)
	}
}

// queueListingParams is queueListing with raw params JSON.
func queueListingParams(t *testing.T, d *sql.DB, root, user int64, node, params string) int64 {
	t.Helper()
	id, err := dbq.New(d).InsertJob(context.Background(), dbq.InsertJobParams{
		Kind: "listing", RootID: root, TargetNode: sql.NullString{String: node, Valid: true},
		Params: params, RequestedBy: sql.NullInt64{Int64: user, Valid: true}, CreatedAt: 1,
	})
	must(t, err)
	return id
}
