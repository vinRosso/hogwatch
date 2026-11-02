//go:build linux

package service

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
)

type runHandle struct {
	cancel context.CancelFunc
	done   chan error
}

func startRun(t *testing.T, o Options) *runHandle {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := &runHandle{cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- Run(ctx, o) }()
	t.Cleanup(func() { h.stop(t) })
	return h
}

// stop cancels Run and checks it returns nil. It is idempotent.
func (h *runHandle) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
		h.done <- nil
	case <-time.After(10 * time.Second):
		t.Error("Run did not return after cancel")
	}
}

func testOptions(t *testing.T, clk *fakeClock) Options {
	d, dir := newDB(t)
	return Options{
		DB: d, DataDir: dir, HostPrefix: "/host", Version: "test",
		Workers: 1, ListingWorkers: 1,
		Poll: 10 * time.Millisecond, ProgressEvery: 10 * time.Millisecond,
		HeartbeatEvery: 10 * time.Millisecond, Grace: time.Minute, ListingTTL: time.Hour,
		Now: clk.Now, Log: quietLog(),
		Mounts: func() ([]fsx.Mount, error) {
			return []fsx.Mount{
				{MountPoint: "/host", FSType: "ext4", ReadOnly: true},
				{MountPoint: "/host/proc", FSType: "proc"},
				{MountPoint: "/data", FSType: "ext4"},
			}, nil
		},
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestRun_CancelQueued(t *testing.T) {
	clk := newClock(t0)
	o := testOptions(t, clk)
	root := addRoot(t, o.DB, "a", "", true)
	scan := addJob(t, o.DB, "scan", root, "queued", `{"trigger":"manual"}`)
	cancel := addJob(t, o.DB, "cancel", root, "queued", `{"job_id":`+itoa(scan)+`}`)

	h := startRun(t, o)
	waitFor(t, "cancel job done", func() bool { st, _ := jobStatus(t, o.DB, cancel); return st == "done" })
	h.stop(t)

	if st, _ := jobStatus(t, o.DB, scan); st != "cancelled" {
		t.Errorf("scan job = %q, want cancelled", st)
	}
	if n := count(t, o.DB, `SELECT count(*) FROM scans`); n != 0 {
		t.Errorf("%d scans started, want 0", n)
	}
}

func TestRun_CancelBadParams(t *testing.T) {
	clk := newClock(t0)
	o := testOptions(t, clk)
	root := addRoot(t, o.DB, "a", "", true)
	unknown := addJob(t, o.DB, "cancel", root, "queued", `{"job_id":424242}`)
	bad := addJob(t, o.DB, "cancel", root, "queued", `{"nope":1}`)
	h := startRun(t, o)
	waitFor(t, "cancel jobs finished", func() bool {
		a, _ := jobStatus(t, o.DB, unknown)
		b, _ := jobStatus(t, o.DB, bad)
		return a == "done" && b == "failed"
	})
	h.stop(t)
}

// TestRun_CancelRunning pins that a cancel job cancels the context of a job
// a hook spawned, and that Run waits for that job before it returns.
func TestRun_CancelRunning(t *testing.T) {
	clk := newClock(t0)
	o := testOptions(t, clk)
	root := addRoot(t, o.DB, "a", "", true)
	job := addJob(t, o.DB, "scan", root, "running", `{"trigger":"manual"}`)

	var cancelled atomic.Bool
	started := make(chan struct{})
	var once atomic.Bool
	o.hooks.startScan = func(ctx context.Context, s *service) bool {
		if !once.CompareAndSwap(false, true) {
			return false
		}
		s.spawn(ctx, kindScan, job, func(jctx context.Context) {
			close(started)
			<-jctx.Done()
			cancelled.Store(true)
		})
		return true
	}
	h := startRun(t, o)
	<-started
	cancel := addJob(t, o.DB, "cancel", root, "queued", `{"job_id":`+itoa(job)+`}`)
	waitFor(t, "cancel job done", func() bool { st, _ := jobStatus(t, o.DB, cancel); return st == "done" })
	waitFor(t, "job ctx cancelled", cancelled.Load)
	h.stop(t)
}

func TestRun_ShutdownWaitsForJobs(t *testing.T) {
	clk := newClock(t0)
	o := testOptions(t, clk)
	root := addRoot(t, o.DB, "a", "", true)
	job := addJob(t, o.DB, "scan", root, "running", `{"trigger":"manual"}`)
	var finished atomic.Bool
	started := make(chan struct{})
	var once atomic.Bool
	o.hooks.startScan = func(ctx context.Context, s *service) bool {
		if !once.CompareAndSwap(false, true) {
			return false
		}
		s.spawn(ctx, kindScan, job, func(jctx context.Context) {
			close(started)
			<-jctx.Done()
			time.Sleep(50 * time.Millisecond)
			finished.Store(true)
		})
		return true
	}
	h := startRun(t, o)
	<-started
	h.stop(t)
	if !finished.Load() {
		t.Error("Run returned before the running job finished its cancellation")
	}
}

func TestRun_Heartbeat(t *testing.T) {
	clk := newClock(t0)
	o := testOptions(t, clk)
	h := startRun(t, o)

	heartbeat := func() int64 {
		var v int64
		_ = o.DB.QueryRow(`SELECT heartbeat_at FROM scanner_status WHERE id = 1`).Scan(&v)
		return v
	}
	waitFor(t, "first heartbeat", func() bool { return heartbeat() != 0 })
	advances := 0
	last := heartbeat()
	deadline := time.Now().Add(10 * time.Second)
	for advances < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat advanced %d times, want at least 2", advances)
		}
		clk.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
		if v := heartbeat(); v > last {
			advances++
			last = v
		}
	}
	h.stop(t)

	var mounts, version string
	if err := o.DB.QueryRow(`SELECT available_mounts, version FROM scanner_status WHERE id = 1`).Scan(&mounts, &version); err != nil {
		t.Fatal(err)
	}
	if want := `[{"container_path":"/host","host_path":"/","fstype":"ext4","read_only":true}]`; mounts != want {
		t.Errorf("available_mounts = %s, want %s", mounts, want)
	}
	if version != "test" {
		t.Errorf("version = %q", version)
	}
}

func TestRun_HooksRun(t *testing.T) {
	clk := newClock(t0)
	o := testOptions(t, clk)
	var scans, listings, cleanups atomic.Int32
	o.hooks.startScan = func(context.Context, *service) bool { scans.Add(1); return false }
	o.hooks.startListing = func(context.Context, *service) bool { listings.Add(1); return false }
	o.hooks.cleanup = func(context.Context, *service, time.Time) { cleanups.Add(1) }
	h := startRun(t, o)
	waitFor(t, "hooks", func() bool { return scans.Load() >= 3 && listings.Load() >= 3 && cleanups.Load() >= 1 })
	if c := cleanups.Load(); c != 1 {
		t.Errorf("cleanup ran %d times with a frozen clock, want once", c)
	}
	clk.Advance(61 * time.Second)
	waitFor(t, "second cleanup", func() bool { return cleanups.Load() >= 2 })
	h.stop(t)
}

// TestRun_StartupRecovers pins that Run recovers before it polls.
func TestRun_StartupRecovers(t *testing.T) {
	clk := newClock(t0)
	o := testOptions(t, clk)
	root := addRoot(t, o.DB, "a", "", true)
	job := addJob(t, o.DB, "scan", root, "running", `{"trigger":"manual"}`)
	h := startRun(t, o)
	waitFor(t, "job failed", func() bool { st, _ := jobStatus(t, o.DB, job); return st == "failed" })
	h.stop(t)
}

// TestRun_CancelJobFailureDoesNotStrand pins that when applying a cancel job
// fails, that job is finished failed (not left running) and the remaining
// cancel jobs are still applied.
func TestRun_CancelJobFailureDoesNotStrand(t *testing.T) {
	clk := newClock(t0)
	o := testOptions(t, clk)
	root := addRoot(t, o.DB, "a", "", true)
	poison := addJob(t, o.DB, "scan", root, "queued", `{"trigger":"manual"}`)
	fine := addJob(t, o.DB, "scan", root, "queued", `{"trigger":"manual"}`)
	// Cancelling the poisoned job fails inside CancelQueuedJob.
	exec(t, o.DB, `CREATE TRIGGER poison BEFORE UPDATE ON jobs WHEN NEW.id = `+itoa(poison)+
		` AND NEW.status = 'cancelled' BEGIN SELECT RAISE(ABORT, 'poisoned'); END`)
	c1 := addJob(t, o.DB, "cancel", root, "queued", `{"job_id":`+itoa(poison)+`}`)
	c2 := addJob(t, o.DB, "cancel", root, "queued", `{"job_id":`+itoa(fine)+`}`)

	s := newService(o.withDefaults())
	if err := s.applyCancels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, msg := jobStatus(t, o.DB, c1); st != "failed" || !strings.Contains(msg, "poisoned") {
		t.Errorf("poisoned cancel job = %q %q, want failed with the error text", st, msg)
	}
	if st, _ := jobStatus(t, o.DB, c2); st != "done" {
		t.Errorf("second cancel job = %q, want done", st)
	}
	if st, _ := jobStatus(t, o.DB, fine); st != "cancelled" {
		t.Errorf("second target = %q, want cancelled", st)
	}
}
