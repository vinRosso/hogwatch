package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

// seedRoot inserts a scan root and a user and returns their ids.
func seedRoot(t *testing.T, d *sql.DB) (rootID, userID int64) {
	t.Helper()
	mustExec(t, d, `INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (1, 'data', '/mnt/data', '/srv/data')`)
	mustExec(t, d, `INSERT INTO users (id, username, created_at) VALUES (1, 'alice', 100)`)
	return 1, 1
}

func newJob(t *testing.T, kind JobKind, params any, root, user int64, created int64) dbq.InsertJobParams {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return dbq.InsertJobParams{
		Kind:        string(kind),
		RootID:      root,
		Params:      string(b),
		RequestedBy: sql.NullInt64{Int64: user, Valid: true},
		CreatedAt:   created,
	}
}

func claimParams(kind JobKind, at int64) dbq.ClaimJobParams {
	return dbq.ClaimJobParams{Kind: string(kind), StartedAt: sql.NullInt64{Int64: at, Valid: true}}
}

func TestJobs_Lifecycle(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	root, user := seedRoot(t, d)
	q := dbq.New(d)

	id, err := q.InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "manual"}, root, user, 1000))
	if err != nil {
		t.Fatalf("InsertJob: %v", err)
	}
	got, err := q.GetJob(ctx, id)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != string(StatusQueued) {
		t.Fatalf("new job status = %q, want queued", got.Status)
	}
	var sp ScanParams
	if err := json.Unmarshal([]byte(got.Params), &sp); err != nil || sp.Trigger != "manual" {
		t.Fatalf("params = %q (%v)", got.Params, err)
	}

	claimed, err := q.ClaimJob(ctx, claimParams(KindScan, 1001))
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if claimed.ID != id || claimed.Status != string(StatusRunning) || claimed.StartedAt.Int64 != 1001 {
		t.Fatalf("claimed = %+v", claimed)
	}
	if _, err := q.ClaimJob(ctx, claimParams(KindScan, 1002)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second claim err = %v, want sql.ErrNoRows", err)
	}

	eta := int64(42)
	want := Progress{Dirs: 3, Files: 10, BytesLogical: 1 << 40, BytesAlloc: 1<<40 + 4096, Errors: 1, RatePerSec: 12.5, ETASeconds: &eta}
	pj, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateJobProgress(ctx, dbq.UpdateJobProgressParams{Progress: sql.NullString{String: string(pj), Valid: true}, ID: id}); err != nil {
		t.Fatalf("UpdateJobProgress: %v", err)
	}
	got, err = q.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"dirs", "files", "bytes_logical", "bytes_alloc", "errors", "rate_per_sec", "eta_seconds"} {
		if !strings.Contains(got.Progress.String, `"`+key+`"`) {
			t.Errorf("progress JSON %s lacks key %q", got.Progress.String, key)
		}
	}
	var back Progress
	if err := json.Unmarshal([]byte(got.Progress.String), &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, want) {
		t.Fatalf("progress round trip = %+v, want %+v", back, want)
	}

	fin := dbq.FinishJobParams{Status: string(StatusDone), FinishedAt: sql.NullInt64{Int64: 1100, Valid: true}, ID: id}
	n, err := q.FinishJob(ctx, fin)
	if err != nil || n != 1 {
		t.Fatalf("FinishJob = %d, %v; want 1, nil", n, err)
	}
	n, err = q.FinishJob(ctx, fin)
	if err != nil || n != 0 {
		t.Fatalf("repeat FinishJob = %d, %v; want 0, nil", n, err)
	}
	got, _ = q.GetJob(ctx, id)
	if got.Status != string(StatusDone) || got.FinishedAt.Int64 != 1100 {
		t.Fatalf("finished job = %+v", got)
	}
}

func TestClaimJob_FIFOAndKind(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	root, user := seedRoot(t, d)
	q := dbq.New(d)

	listing, err := q.InsertJob(ctx, newJob(t, KindListing, ListingParams{Mode: "children"}, root, user, 1))
	if err != nil {
		t.Fatal(err)
	}
	first, err := q.InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "schedule"}, root, user, 2))
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "manual"}, root, user, 3))
	if err != nil {
		t.Fatal(err)
	}

	c, err := q.ClaimJob(ctx, claimParams(KindScan, 10))
	if err != nil || c.ID != first {
		t.Fatalf("scan claim = %d, %v; want oldest scan %d", c.ID, err, first)
	}
	c, err = q.ClaimJob(ctx, claimParams(KindScan, 10))
	if err != nil || c.ID != second {
		t.Fatalf("scan claim = %d, %v; want %d", c.ID, err, second)
	}
	if _, err := q.ClaimJob(ctx, claimParams(KindScan, 10)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("third scan claim err = %v, want sql.ErrNoRows", err)
	}
	g, _ := q.GetJob(ctx, listing)
	if g.Status != string(StatusQueued) {
		t.Fatalf("listing job status = %q, want queued (scan claims must skip it)", g.Status)
	}
	c, err = q.ClaimJob(ctx, claimParams(KindListing, 10))
	if err != nil || c.ID != listing {
		t.Fatalf("listing claim = %d, %v; want %d", c.ID, err, listing)
	}
}

func TestClaimJob_Concurrent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hogwatch.db")
	d1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d1.Close() })
	if err := Migrate(ctx, d1); err != nil {
		t.Fatal(err)
	}
	d2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d2.Close() })
	root, user := seedRoot(t, d1)
	handles := []*dbq.Queries{dbq.New(d1), dbq.New(d2)}

	for round := range 25 {
		if _, err := handles[0].InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "manual"}, root, user, int64(round))); err != nil {
			t.Fatal(err)
		}
		var wins, empty atomic.Int32
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		start := make(chan struct{})
		for g := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := handles[g%2].ClaimJob(ctx, claimParams(KindScan, 5))
				switch {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, sql.ErrNoRows):
					empty.Add(1)
				default:
					errs <- err
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("round %d: claim error (SQLITE_BUSY?): %v", round, err)
		}
		if wins.Load() != 1 || empty.Load() != 7 {
			t.Fatalf("round %d: wins=%d empty=%d, want 1 and 7", round, wins.Load(), empty.Load())
		}
	}
}

func TestFailRunningJobs(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	root, user := seedRoot(t, d)
	q := dbq.New(d)

	running, _ := q.InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "manual"}, root, user, 1))
	if _, err := q.ClaimJob(ctx, claimParams(KindScan, 2)); err != nil {
		t.Fatal(err)
	}
	queued, _ := q.InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "manual"}, root, user, 3))

	n, err := q.FailRunningJobs(ctx, dbq.FailRunningJobsParams{
		Error:      sql.NullString{String: "scanner restarted", Valid: true},
		FinishedAt: sql.NullInt64{Int64: 50, Valid: true},
	})
	if err != nil || n != 1 {
		t.Fatalf("FailRunningJobs = %d, %v; want 1", n, err)
	}
	g, _ := q.GetJob(ctx, running)
	if g.Status != string(StatusFailed) || g.Error.String != "scanner restarted" || g.FinishedAt.Int64 != 50 {
		t.Fatalf("running job after recovery = %+v", g)
	}
	g, _ = q.GetJob(ctx, queued)
	if g.Status != string(StatusQueued) {
		t.Fatalf("queued job status = %q, want untouched", g.Status)
	}
}

func TestCountActiveJobsByUser(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	root, user := seedRoot(t, d)
	mustExec(t, d, `INSERT INTO users (id, username, created_at) VALUES (2, 'bob', 100)`)
	q := dbq.New(d)

	ins := func(kind JobKind, uid int64) int64 {
		id, err := q.InsertJob(ctx, newJob(t, kind, map[string]string{}, root, uid, 1))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ins(KindListing, user) // id 1: claimed below -> running
	ins(KindListing, user) // id 2: stays queued
	ins(KindListing, user) // id 3: done below
	ins(KindListing, 2)    // other user
	ins(KindScan, user)    // other kind
	if _, err := q.ClaimJob(ctx, claimParams(KindListing, 2)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `UPDATE jobs SET status = 'done' WHERE id = 3`)

	n, err := q.CountActiveJobsByUser(ctx, dbq.CountActiveJobsByUserParams{
		Kind:        string(KindListing),
		RequestedBy: sql.NullInt64{Int64: user, Valid: true},
	})
	if err != nil || n != 2 {
		t.Fatalf("CountActiveJobsByUser = %d, %v; want 2 (one queued, one running)", n, err)
	}
}

func TestDeleteExpiredJobs_CascadesListingResults(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	root, user := seedRoot(t, d)
	q := dbq.New(d)

	expiring := newJob(t, KindListing, ListingParams{Mode: "subtree"}, root, user, 1)
	expiring.ExpiresAt = sql.NullInt64{Int64: 100, Valid: true}
	gone, _ := q.InsertJob(ctx, expiring)
	mustExec(t, d, `UPDATE jobs SET status = 'done' WHERE id = ?`, gone)
	live := newJob(t, KindListing, ListingParams{Mode: "subtree"}, root, user, 1)
	live.ExpiresAt = sql.NullInt64{Int64: 500, Valid: true}
	keepLive, _ := q.InsertJob(ctx, live)
	keepNever, _ := q.InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "manual"}, root, user, 1)) // NULL expires_at
	for _, id := range []int64{gone, keepLive} {
		mustExec(t, d, `INSERT INTO listing_results (job_id, seq, rel_name, type, size, alloc, mtime, uid) VALUES (?, 0, x'612f62', 'f', 1, 4096, 0, 0)`, id)
	}

	n, err := q.DeleteExpiredJobs(ctx, sql.NullInt64{Int64: 200, Valid: true})
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredJobs = %d, %v; want 1", n, err)
	}
	var rows int
	if err := d.QueryRow(`SELECT count(*) FROM listing_results WHERE job_id = ?`, gone).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("listing_results of deleted job = %d, %v; want 0", rows, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM listing_results WHERE job_id = ?`, keepLive).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("listing_results of live job = %d, %v; want 1", rows, err)
	}
	for _, id := range []int64{keepLive, keepNever} {
		if _, err := q.GetJob(ctx, id); err != nil {
			t.Fatalf("job %d should survive: %v", id, err)
		}
	}
	if _, err := q.GetJob(ctx, gone); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired job err = %v, want sql.ErrNoRows", err)
	}
}

func TestInsertJob_RejectsInvalidParams(t *testing.T) {
	d := openMigrated(t)
	root, user := seedRoot(t, d)
	in := newJob(t, KindScan, ScanParams{}, root, user, 1)
	in.Params = "{not json"
	_, err := dbq.New(d).InsertJob(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("InsertJob(invalid JSON) err = %v, want CHECK constraint failure", err)
	}
}

func TestScannerStatus_Heartbeat(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	q := dbq.New(d)

	if _, err := q.GetScannerStatus(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetScannerStatus before any heartbeat err = %v, want sql.ErrNoRows", err)
	}
	if err := q.UpsertHeartbeat(ctx, dbq.UpsertHeartbeatParams{HeartbeatAt: 100, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertHeartbeat(ctx, dbq.UpsertHeartbeatParams{HeartbeatAt: 200, Version: "v2"}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := d.QueryRow(`SELECT count(*) FROM scanner_status`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("scanner_status rows = %d, %v; want 1", rows, err)
	}
	if err := q.SetAvailableMounts(ctx, dbq.SetAvailableMountsParams{HeartbeatAt: 250, Version: "v2", AvailableMounts: `["/mnt/data"]`}); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetScannerStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 1 || got.HeartbeatAt != 250 || got.Version != "v2" || got.AvailableMounts != `["/mnt/data"]` {
		t.Fatalf("scanner_status = %+v", got)
	}
	// A later heartbeat must not reset the mounts.
	if err := q.UpsertHeartbeat(ctx, dbq.UpsertHeartbeatParams{HeartbeatAt: 300, Version: "v2"}); err != nil {
		t.Fatal(err)
	}
	got, _ = q.GetScannerStatus(ctx)
	if got.HeartbeatAt != 300 || got.AvailableMounts != `["/mnt/data"]` {
		t.Fatalf("after third heartbeat = %+v", got)
	}
	if err := q.SetAvailableMounts(ctx, dbq.SetAvailableMountsParams{HeartbeatAt: 400, Version: "v2", AvailableMounts: `not json`}); err == nil {
		t.Fatal("SetAvailableMounts(invalid JSON) succeeded, want CHECK failure")
	}
}

func TestSettings_CRUD(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	q := dbq.New(d)

	if _, err := q.GetSetting(ctx, "k"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetSetting(missing) err = %v, want sql.ErrNoRows", err)
	}
	for _, kv := range [][2]string{{"b", "2"}, {"a", "1"}, {"b", "3"}} {
		if err := q.UpsertSetting(ctx, dbq.UpsertSettingParams{Key: kv[0], Value: kv[1]}); err != nil {
			t.Fatal(err)
		}
	}
	if v, err := q.GetSetting(ctx, "b"); err != nil || v != "3" {
		t.Fatalf("GetSetting(b) = %q, %v; want 3", v, err)
	}
	list, err := q.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Key != "a" || list[1].Key != "b" || list[1].Value != "3" {
		t.Fatalf("ListSettings = %+v", list)
	}
	if err := q.DeleteSetting(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetSetting(ctx, "a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetSetting(deleted) err = %v, want sql.ErrNoRows", err)
	}
}

func TestDeleteExpiredJobs_KeepsActive(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	root, user := seedRoot(t, d)
	q := dbq.New(d)
	ids := map[string]int64{}
	for _, status := range []string{"queued", "running", "done", "failed", "cancelled"} {
		in := newJob(t, KindListing, ListingParams{Mode: "subtree"}, root, user, 1)
		in.ExpiresAt = sql.NullInt64{Int64: 100, Valid: true}
		id, err := q.InsertJob(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, d, `UPDATE jobs SET status = ? WHERE id = ?`, status, id)
		ids[status] = id
	}
	n, err := q.DeleteExpiredJobs(ctx, sql.NullInt64{Int64: 200, Valid: true})
	if err != nil || n != 3 {
		t.Fatalf("DeleteExpiredJobs = %d, %v; want 3", n, err)
	}
	for status, id := range ids {
		_, err := q.GetJob(ctx, id)
		switch status {
		case "queued", "running":
			if err != nil {
				t.Errorf("expired %s job deleted: %v", status, err)
			}
		default:
			if !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("expired %s job err = %v, want sql.ErrNoRows", status, err)
			}
		}
	}
}

func TestScannerStatus_MountsBeforeHeartbeat(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	q := dbq.New(d)
	if err := q.SetAvailableMounts(ctx, dbq.SetAvailableMountsParams{HeartbeatAt: 100, Version: "v1", AvailableMounts: `["/mnt/a"]`}); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetScannerStatus(ctx)
	if err != nil {
		t.Fatalf("GetScannerStatus after SetAvailableMounts: %v", err)
	}
	if got.HeartbeatAt != 100 || got.Version != "v1" || got.AvailableMounts != `["/mnt/a"]` {
		t.Fatalf("scanner_status = %+v", got)
	}
}

func TestQueries_ScanLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	root, _ := seedRoot(t, d)
	q := dbq.New(d)

	finish := func(id, at int64) int64 {
		n, err := q.FinishScan(ctx, dbq.FinishScanParams{
			ID: id, Status: string(ScanDone), FinishedAt: sql.NullInt64{Int64: at, Valid: true},
			Dirs: 3, Files: 4, BytesLogical: 5, BytesAlloc: 6, ErrorCount: 0, TopNRows: 7,
		})
		if err != nil {
			t.Fatalf("FinishScan: %v", err)
		}
		return n
	}
	s1, err := q.InsertScan(ctx, dbq.InsertScanParams{RootID: root, File: "a.db", Trigger: "manual", StartedAt: 10})
	if err != nil {
		t.Fatalf("InsertScan: %v", err)
	}
	if finish(s1, 100) != 1 {
		t.Fatal("first FinishScan affected != 1 row")
	}
	if finish(s1, 101) != 0 {
		t.Fatal("second FinishScan on the same row must affect 0 rows")
	}
	if err := q.SetCurrentScan(ctx, dbq.SetCurrentScanParams{RootID: root, ScanID: sql.NullInt64{Int64: s1, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	s2, _ := q.InsertScan(ctx, dbq.InsertScanParams{RootID: root, File: "b.db", Trigger: "schedule", StartedAt: 150})
	if finish(s2, 200) != 1 {
		t.Fatal("FinishScan s2")
	}
	if err := q.SetCurrentScan(ctx, dbq.SetCurrentScanParams{RootID: root, ScanID: sql.NullInt64{Int64: s2, Valid: true}}); err != nil {
		t.Fatal(err)
	}

	sup := func(cutoff int64) []dbq.SupersededScansRow {
		rows, err := q.SupersededScans(ctx, sql.NullInt64{Int64: cutoff, Valid: true})
		if err != nil {
			t.Fatalf("SupersededScans: %v", err)
		}
		return rows
	}
	if got := sup(199); len(got) != 0 {
		t.Fatalf("SupersededScans(199) = %v, want none", got)
	}
	if got := sup(200); len(got) != 1 || got[0].ID != s1 {
		t.Fatalf("SupersededScans(200) = %v, want [%d]", got, s1)
	}
	if n, err := q.MarkScanDeleted(ctx, s1); err != nil || n != 1 {
		t.Fatalf("MarkScanDeleted = %d, %v", n, err)
	}
	if got := sup(1000); len(got) != 0 {
		t.Fatalf("deleted scan still superseded: %v", got)
	}
	if f, err := q.CurrentScanFiles(ctx, root); err != nil || f != "b.db" {
		t.Fatalf("CurrentScanFiles = %q, %v", f, err)
	}
	if got, err := q.ListScansWithFiles(ctx); err != nil || len(got) != 1 || got[0].ID != s2 {
		t.Fatalf("ListScansWithFiles = %v, %v", got, err)
	}
	if v, err := q.LastScanStart(ctx, root); err != nil || v != 150 {
		t.Fatalf("LastScanStart = %d, %v; want 150", v, err)
	}
}

func TestQueries_FailRunningJobsAtStartup(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	root, user := seedRoot(t, d)
	q := dbq.New(d)

	scanJob, _ := q.InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "manual"}, root, user, 1))
	listJob, _ := q.InsertJob(ctx, newJob(t, KindListing, ListingParams{Mode: "children"}, root, user, 2))
	for _, k := range []JobKind{KindScan, KindListing} {
		if _, err := q.ClaimJob(ctx, claimParams(k, 3)); err != nil {
			t.Fatal(err)
		}
	}
	queued, _ := q.InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "manual"}, root, user, 4))
	runScan, _ := q.InsertScan(ctx, dbq.InsertScanParams{
		RootID: root, JobID: sql.NullInt64{Int64: scanJob, Valid: true}, File: "a.db", Trigger: "manual", StartedAt: 3})

	rows, err := q.FailRunningJobsAtStartup(ctx, dbq.FailRunningJobsAtStartupParams{
		Error:            sql.NullString{String: "scanner restarted", Valid: true},
		FinishedAt:       sql.NullInt64{Int64: 50, Valid: true},
		ListingExpiresAt: sql.NullInt64{Int64: 999, Valid: true},
	})
	if err != nil || len(rows) != 2 {
		t.Fatalf("FailRunningJobsAtStartup = %v, %v; want 2 rows", rows, err)
	}
	if n, err := q.FailRunningScans(ctx, dbq.FailRunningScansParams{
		FinishedAt: sql.NullInt64{Int64: 50, Valid: true}, Error: sql.NullString{String: "scanner restarted", Valid: true},
	}); err != nil || n != 1 {
		t.Fatalf("FailRunningScans = %d, %v", n, err)
	}
	sj, _ := q.GetJob(ctx, scanJob)
	lj, _ := q.GetJob(ctx, listJob)
	qj, _ := q.GetJob(ctx, queued)
	if sj.Status != "failed" || sj.ExpiresAt.Valid {
		t.Fatalf("scan job = %+v, want failed without expires_at", sj)
	}
	if lj.Status != "failed" || lj.ExpiresAt.Int64 != 999 || !lj.ExpiresAt.Valid {
		t.Fatalf("listing job = %+v, want failed with expires_at 999", lj)
	}
	if qj.Status != "queued" {
		t.Fatalf("queued job = %q, want untouched", qj.Status)
	}
	var st string
	if err := d.QueryRowContext(ctx, `SELECT status FROM scans WHERE id = ?`, runScan).Scan(&st); err != nil || st != "failed" {
		t.Fatalf("scan status = %q, %v", st, err)
	}
}

func TestQueries_CancelQueuedJob(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	root, user := seedRoot(t, d)
	q := dbq.New(d)

	a, _ := q.InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "manual"}, root, user, 1))
	if n, err := q.CancelQueuedJob(ctx, a); err != nil || n != 1 {
		t.Fatalf("CancelQueuedJob(queued) = %d, %v; want 1", n, err)
	}
	if g, _ := q.GetJob(ctx, a); g.Status != "cancelled" {
		t.Fatalf("status = %q", g.Status)
	}
	b, _ := q.InsertJob(ctx, newJob(t, KindScan, ScanParams{Trigger: "manual"}, root, user, 2))
	if _, err := q.ClaimJob(ctx, claimParams(KindScan, 3)); err != nil {
		t.Fatal(err)
	}
	if n, err := q.CancelQueuedJob(ctx, b); err != nil || n != 0 {
		t.Fatalf("CancelQueuedJob(running) = %d, %v; want 0", n, err)
	}
}

func TestConfigIssues_KindCheck(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	root, _ := seedRoot(t, d)
	q := dbq.New(d)

	for _, k := range []string{"cross_root_duplicate", "writable_mount", "eio", "invalid_schedule"} {
		if err := q.InsertIssue(ctx, dbq.InsertIssueParams{RootID: root, Kind: k, Details: "{}", CreatedAt: 1}); err != nil {
			t.Fatalf("InsertIssue(%s): %v", k, err)
		}
	}
	if err := q.InsertIssue(ctx, dbq.InsertIssueParams{RootID: root, Kind: "bogus", Details: "{}", CreatedAt: 1}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := q.InsertIssue(ctx, dbq.InsertIssueParams{RootID: root, Kind: "eio", Details: "not json", CreatedAt: 1}); err == nil {
		t.Fatal("invalid details accepted")
	}
	if err := q.DeleteIssues(ctx, dbq.DeleteIssuesParams{RootID: root, Kind: "eio"}); err != nil {
		t.Fatal(err)
	}
	if err := q.DeleteIssuesByKind(ctx, "writable_mount"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM config_issues`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("issues left = %d, %v; want 2", n, err)
	}
}

// seedRead builds the fixture of the M7 read queries: two roots, alice with
// two paths on root 1, bob with none, and one scan per state.
func seedRead(t *testing.T, d *sql.DB) {
	t.Helper()
	mustExec(t, d, `INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (1, 'data', '/mnt/data', '/srv/data')`)
	mustExec(t, d, `INSERT INTO scan_roots (id, name, container_path, host_path) VALUES (2, 'other', '/mnt/other', '/srv/other')`)
	mustExec(t, d, `INSERT INTO users (id, username, created_at, quota_bytes) VALUES (1, 'alice', 100, 5000)`)
	mustExec(t, d, `INSERT INTO users (id, username, created_at) VALUES (2, 'bob', 100)`)
	mustExec(t, d, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at, dirs, files, bytes_alloc) VALUES (1, 1, 'data-1.db', 'done', 'schedule', 1000, 1100, 3, 10, 4096)`)
	mustExec(t, d, `INSERT INTO scans (id, root_id, file, status, trigger, started_at) VALUES (2, 1, 'data-2.db', 'running', 'manual', 2000)`)
	mustExec(t, d, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (3, 2, 'other-3.db', 'done', 'schedule', 1500, 1600)`)
	mustExec(t, d, `INSERT INTO scans (id, root_id, file, status, trigger, started_at, finished_at) VALUES (4, 1, 'data-0.db', 'deleted', 'schedule', 500, 600)`)
	mustExec(t, d, `UPDATE scan_roots SET current_scan_id = 1 WHERE id = 1`)
	mustExec(t, d, `UPDATE scan_roots SET current_scan_id = 3 WHERE id = 2`)
	mustExec(t, d, `INSERT INTO user_paths (id, user_id, root_id, rel_path, created_at) VALUES (1, 1, 1, CAST('a' AS BLOB), 100)`)
	mustExec(t, d, `INSERT INTO user_paths (id, user_id, root_id, rel_path, created_at) VALUES (2, 1, 1, CAST('a/b' AS BLOB), 100)`)
	mustExec(t, d, `INSERT INTO path_stats VALUES (1, 1, 1, 0, 90, 100, 7, 0, NULL, NULL, NULL, NULL, 11)`)
	mustExec(t, d, `INSERT INTO path_stats VALUES (1, 2, 1, 1, 40, 50, 3, 0, NULL, NULL, NULL, NULL, 5)`)
	mustExec(t, d, `INSERT INTO usage VALUES (1, 1, 1, 90, 100, 7, 0)`)
	mustExec(t, d, `INSERT INTO usage VALUES (4, 1, 1, 1, 2, 3, 0)`)
	mustExec(t, d, `INSERT INTO path_stats VALUES (4, 1, 1, 0, 1, 2, 3, 0, NULL, NULL, NULL, NULL, 0)`)
	mustExec(t, d, `INSERT INTO user_paths (id, user_id, root_id, rel_path, created_at) VALUES (3, 2, 2, CAST('x' AS BLOB), 100)`)
	mustExec(t, d, `INSERT INTO usage VALUES (3, 2, 2, 8, 9, 1, 0)`)
	mustExec(t, d, `INSERT INTO path_stats VALUES (3, 3, 1, 0, 8, 9, 1, 0, NULL, NULL, NULL, NULL, 0)`)
	mustExec(t, d, `INSERT INTO mount_stats VALUES (1, 1, 1000, 400, NULL, NULL, NULL, NULL)`)
	mustExec(t, d, `INSERT INTO jobs (id, kind, root_id, params, requested_by, status, progress, error, created_at) VALUES (1, 'listing', 1, '{}', 1, 'running', '{"rows":2,"truncated":true}', NULL, 10)`)
	mustExec(t, d, `INSERT INTO listing_results VALUES (1, 1, CAST('x' AS BLOB), 'file', 1, 1, 1, 0)`)
	mustExec(t, d, `INSERT INTO listing_results VALUES (1, 2, CAST('y' AS BLOB), 'file', 1, 1, 1, 0)`)
	mustExec(t, d, `INSERT INTO listing_results VALUES (1, 3, CAST('z' AS BLOB), 'file', 1, 1, 1, 0)`)
	mustExec(t, d, `INSERT INTO history_dirs VALUES (300, 1, 77, 20, 2, 'raw')`)
	mustExec(t, d, `INSERT INTO history_dirs VALUES (100, 1, 77, 10, 1, 'raw')`)
	mustExec(t, d, `INSERT INTO history_dirs VALUES (50, 1, 77, 5, 1, 'raw')`)
	mustExec(t, d, `INSERT INTO history_dirs VALUES (200, 1, 78, 99, 9, 'raw')`)
}

func TestQueries_ReadM7(t *testing.T) {
	ctx := context.Background()
	d := openMigrated(t)
	seedRead(t, d)
	q := dbq.New(d)

	roots, err := q.ListRootsForUser(ctx, 1)
	if err != nil || len(roots) != 1 || roots[0].ID != 1 || roots[0].CurrentScanID.Int64 != 1 {
		t.Fatalf("ListRootsForUser(alice) = %+v, %v", roots, err)
	}
	if roots, err = q.ListRootsForUser(ctx, 2); err != nil || len(roots) != 1 || roots[0].ID != 2 {
		t.Fatalf("ListRootsForUser(bob) = %+v, %v; want root 2 only", roots, err)
	}
	if all, err := q.ListRootsAll(ctx); err != nil || len(all) != 2 {
		t.Fatalf("ListRootsAll = %+v, %v", all, err)
	}
	if n, err := q.RootScanning(ctx, 1); err != nil || n != 1 {
		t.Fatalf("RootScanning(1) = %d, %v; want 1", n, err)
	}
	if n, err := q.RootScanning(ctx, 2); err != nil || n != 0 {
		t.Fatalf("RootScanning(2) = %d, %v; want 0", n, err)
	}
	if m, err := q.GetMountStats(ctx, 1); err != nil || m.FsTotal != 1000 || m.FsFree != 400 {
		t.Fatalf("GetMountStats = %+v, %v", m, err)
	}
	if _, err := q.GetMountStats(ctx, 3); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetMountStats(no row) err = %v, want ErrNoRows", err)
	}

	ups, err := q.ListUserPathsForUser(ctx, 1)
	if err != nil || len(ups) != 2 || string(ups[1].RelPath) != "a/b" || ups[0].HostPath != "/srv/data" {
		t.Fatalf("ListUserPathsForUser = %+v, %v", ups, err)
	}
	if bp, err := q.ListUserPathsForUser(ctx, 2); err != nil || len(bp) != 1 || string(bp[0].RelPath) != "x" {
		t.Fatalf("ListUserPathsForUser(bob) = %+v, %v; want only x", bp, err)
	}
	if bu, err := q.ListUsageForUser(ctx, 2); err != nil || len(bu) != 1 || bu[0].BytesAlloc != 9 {
		t.Fatalf("ListUsageForUser(bob) = %+v, %v", bu, err)
	}
	if s, err := q.GetScanSummary(ctx, 1); err != nil || s.Dirs != 3 || s.FinishedAt.Int64 != 1100 {
		t.Fatalf("GetScanSummary = %+v, %v", s, err)
	}
	usage, err := q.ListUsageForUser(ctx, 1)
	if err != nil || len(usage) != 1 || usage[0].BytesAlloc != 100 || usage[0].RootName != "data" {
		t.Fatalf("ListUsageForUser = %+v, %v", usage, err)
	}
	ps, err := q.ListPathStatsForUser(ctx, 1)
	if err != nil || len(ps) != 2 || ps[0].Found != 1 || ps[1].Redundant != 1 || ps[1].Files != 3 || ps[0].BytesAlloc != 100 {
		t.Fatalf("ListPathStatsForUser = %+v, %v", ps, err)
	}
	on, err := q.ListPathStatsOnRoot(ctx, dbq.ListPathStatsOnRootParams{RootID: 1, ScanID: 1})
	if err != nil || len(on) != 2 || on[0].Username != "alice" {
		t.Fatalf("ListPathStatsOnRoot = %+v, %v", on, err)
	}
	if qq, err := q.GetUserQuota(ctx, 1); err != nil || qq.QuotaBytes.Int64 != 5000 || qq.QuotaBasis != "allocated" {
		t.Fatalf("GetUserQuota = %+v, %v", qq, err)
	}

	cur, err := q.ListCurrentAndRunningScans(ctx, 1)
	if err != nil || len(cur) != 2 {
		t.Fatalf("ListCurrentAndRunningScans(alice) = %+v, %v; want scans 2 and 1", cur, err)
	}
	if cur, err = q.ListCurrentAndRunningScans(ctx, 2); err != nil || len(cur) != 1 || cur[0].ID != 3 {
		t.Fatalf("ListCurrentAndRunningScans(bob) = %+v, %v; want scan 3 only", cur, err)
	}
	adm, err := q.ListScansAdmin(ctx, dbq.ListScansAdminParams{RowLimit: 500})
	if err != nil || len(adm) != 4 || adm[0].ID != 2 || adm[3].ID != 4 {
		t.Fatalf("ListScansAdmin(all) = %+v, %v; want newest first incl. deleted", adm, err)
	}
	adm, err = q.ListScansAdmin(ctx, dbq.ListScansAdminParams{RootID: sql.NullInt64{Int64: 2, Valid: true}, RowLimit: 500})
	if err != nil || len(adm) != 1 || adm[0].ID != 3 {
		t.Fatalf("ListScansAdmin(root 2) = %+v, %v", adm, err)
	}

	hist, err := q.ListHistoryDirs(ctx, dbq.ListHistoryDirsParams{RootID: 1, PathHash: 77, Since: 100})
	if err != nil || len(hist) != 2 || hist[0].Ts != 100 || hist[1].Ts != 300 {
		t.Fatalf("ListHistoryDirs = %+v, %v; want ts 100,300 ascending", hist, err)
	}

	job, err := q.GetListingJob(ctx, 1)
	if err != nil || job.Kind != "listing" || job.Status != "running" || !job.Progress.Valid {
		t.Fatalf("GetListingJob = %+v, %v", job, err)
	}
	rows, err := q.ListListingResults(ctx, dbq.ListListingResultsParams{JobID: 1, AfterSeq: 1, RowLimit: 10})
	if err != nil || len(rows) != 2 || rows[0].Seq != 2 {
		t.Fatalf("ListListingResults = %+v, %v; want seq 2,3", rows, err)
	}

	if n, err := q.HasActiveCancel(ctx, 1); err != nil || n != 0 {
		t.Fatalf("HasActiveCancel before = %d, %v; want 0", n, err)
	}
	mustExec(t, d, `INSERT INTO jobs (kind, root_id, params, requested_by, created_at) VALUES ('cancel', 1, '{"job_id":1}', 1, 20)`)
	if n, err := q.HasActiveCancel(ctx, 1); err != nil || n != 1 {
		t.Fatalf("HasActiveCancel after = %d, %v; want 1", n, err)
	}
	if n, err := q.HasActiveCancel(ctx, 2); err != nil || n != 0 {
		t.Fatalf("HasActiveCancel(other job) = %d, %v; want 0", n, err)
	}
}
