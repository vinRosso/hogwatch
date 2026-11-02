//go:build linux

// Package service is the long-running scanner: startup recovery, heartbeat,
// the available-mounts list, the job poller and the scheduler. The
// scan pipeline and the listing pool plug into the poller through the hooks
// documented on Options.hooks.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/vinRosso/hogwatch/internal/config"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
	"github.com/vinRosso/hogwatch/internal/scanner/gate"
)

// Job kinds, as stored in jobs.kind.
const (
	kindScan    = string(db.KindScan)
	kindListing = string(db.KindListing)
	kindCancel  = string(db.KindCancel)
)

// cleanupEvery is how often the poller runs the cleanup hook.
const cleanupEvery = time.Minute

// Options configures the service. Every duration is an Options field so tests
// can shorten it.
type Options struct {
	DB                      *sql.DB
	DataDir, HostPrefix     string
	ZFSFile, Version        string
	Workers, ListingWorkers int
	AllowWritable           bool
	Poll, ProgressEvery     time.Duration
	HeartbeatEvery, Grace   time.Duration
	ListingTTL              time.Duration
	// PublishBackoff is the first wait before a publish that hit SQLITE_BUSY is
	// retried; each retry waits twice as long as the one before, for five
	// retries (1, 2, 4, 8, 16 s by default). Zero means one second.
	PublishBackoff time.Duration
	Now            func() time.Time
	Log            *slog.Logger
	Mounts         func() ([]fsx.Mount, error)
	Gate           *gate.Gate

	// hooks are the points where later tasks plug into the poll loop. A nil
	// hook is skipped.
	hooks hooks
}

// hooks are called from the single poll goroutine, so they must not block for
// long: start work with service.spawn and return.
type hooks struct {
	// startScan is called on a poll when no scan job is running. It claims at
	// most one queued scan job (db ClaimJob), spawns its pipeline with
	// spawn(ctx, kindScan, jobID, ...) at once, and reports whether it
	// started one. (Task 9.)
	startScan func(ctx context.Context, s *service) bool
	// startListing is called while fewer than Options.ListingWorkers listing
	// jobs run. It claims one queued listing job, spawns it with
	// spawn(ctx, kindListing, jobID, ...) and reports whether it started one;
	// false ends the round. (Task 10.)
	startListing func(ctx context.Context, s *service) bool
	// cleanup runs once per minute of Options.Now, first on the first poll:
	// delete superseded generations and expired jobs. (Task 9.)
	cleanup func(ctx context.Context, s *service, now time.Time)

	// Test seams of the scan pipeline; nil means the real behaviour.
	freeBytes     func(dir string) (int64, error)                             // free bytes in the data dir
	publish       func(ctx context.Context, s *service, p *publication) error // one publish attempt
	beforePublish func()                                                      // called before the first publish attempt
	onProgress    func(ctx context.Context, p db.Progress)                    // called after each walk progress write
	onPhase       func(phase string)                                          // called as the walk and finalize phases begin
}

// DefaultOptions fills Options from the configuration with the timings of the
// plan's Global Constraints. Version is left for the caller.
func DefaultOptions(cfg config.Config, d *sql.DB, log *slog.Logger) Options {
	o := Options{
		DB:             d,
		DataDir:        cfg.DataDir,
		HostPrefix:     cfg.Scan.HostPrefix,
		ZFSFile:        cfg.Scan.ZFSFile,
		Workers:        cfg.Scan.Workers,
		ListingWorkers: cfg.Scan.ListingWorkers,
		AllowWritable:  cfg.Scan.AllowWritableMounts,
		Poll:           time.Second,
		ProgressEvery:  2 * time.Second,
		HeartbeatEvery: 10 * time.Second,
		Grace:          cfg.Scan.GenerationGrace,
		ListingTTL:     time.Hour,
		PublishBackoff: time.Second,
		Now:            time.Now,
		Log:            log,
		Mounts:         fsx.ReadMounts,
		Gate:           gate.New(config.WorkerCap(runtime.GOMAXPROCS(0))),
		hooks: hooks{
			startScan:    startScan,
			startListing: startListing,
			cleanup:      func(ctx context.Context, s *service, now time.Time) { s.cleanup(ctx, now) },
		},
	}
	if applyTestHooks != nil {
		applyTestHooks(&o.hooks)
	}
	return o
}

// applyTestHooks is set only by a file built with the "integration" tag, so a
// release binary has no test hooks and reads no test environment variable.
var applyTestHooks func(*hooks)

// withDefaults fills the fields a caller left zero.
func (o Options) withDefaults() Options {
	if o.Poll <= 0 {
		o.Poll = time.Second
	}
	if o.ProgressEvery <= 0 {
		o.ProgressEvery = 2 * time.Second
	}
	if o.HeartbeatEvery <= 0 {
		o.HeartbeatEvery = 10 * time.Second
	}
	if o.ListingTTL <= 0 {
		o.ListingTTL = time.Hour
	}
	if o.PublishBackoff <= 0 {
		o.PublishBackoff = time.Second
	}
	if o.Grace <= 0 {
		o.Grace = 5 * time.Minute
	}
	if o.ListingWorkers < 1 {
		o.ListingWorkers = 1
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Mounts == nil {
		o.Mounts = fsx.ReadMounts
	}
	if o.HostPrefix == "" {
		o.HostPrefix = "/host"
	}
	if o.Gate == nil {
		o.Gate = gate.New(config.WorkerCap(runtime.GOMAXPROCS(0)))
	}
	return o
}

// service is the state of one Run.
type service struct {
	o     Options
	q     *dbq.Queries
	sched *Scheduler

	mu      sync.Mutex
	running map[int64]runningJob // jobs spawned and not yet finished, by job id
	wg      sync.WaitGroup

	lastCleanup time.Time // poll goroutine only
}

type runningJob struct {
	kind   string
	cancel context.CancelCauseFunc
}

func newService(o Options) *service {
	return &service{
		o:       o,
		q:       dbq.New(o.DB),
		sched:   NewScheduler(o.DB, o.Log),
		running: map[int64]runningJob{},
	}
}

// spawn runs fn for job jobID in its own goroutine under a context that is
// cancelled when the service stops or a cancel job targets jobID. The job is
// counted by running() from before spawn returns. fn owns the job's final
// status; on shutdown its ctx is already cancelled, so it must write that
// status with a context of its own.
func (s *service) spawn(ctx context.Context, kind string, jobID int64, fn func(ctx context.Context)) {
	jctx, cancel := context.WithCancelCause(ctx)
	s.mu.Lock()
	s.running[jobID] = runningJob{kind: kind, cancel: cancel}
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.running, jobID)
			s.mu.Unlock()
			cancel(context.Canceled)
		}()
		fn(jctx)
	}()
}

// runningCount is the number of spawned jobs of kind that have not finished.
func (s *service) runningCount(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, j := range s.running {
		if j.kind == kind {
			n++
		}
	}
	return n
}

// Run recovers from a previous run, publishes the available mounts, starts
// the heartbeat and polls until ctx is cancelled. It then waits for the jobs
// it started to finish their cancellation and returns nil.
func Run(ctx context.Context, o Options) error {
	o = o.withDefaults()
	// A stop request during startup is a clean stop, not a failure.
	stopped := func(err error) bool { return ctx.Err() != nil && errors.Is(err, context.Canceled) }
	if err := Recover(ctx, o); err != nil {
		if stopped(err) {
			return nil
		}
		return err
	}
	if err := publishMounts(ctx, o); err != nil {
		if stopped(err) {
			return nil
		}
		return err
	}

	s := newService(o)
	var hb sync.WaitGroup
	hb.Add(1)
	go func() {
		defer hb.Done()
		heartbeat(ctx, o)
	}()

	tk := time.NewTicker(o.Poll)
	defer tk.Stop()
	for {
		s.poll(ctx)
		select {
		case <-ctx.Done():
			s.wg.Wait()
			hb.Wait()
			return nil
		case <-tk.C:
		}
	}
}

func publishMounts(ctx context.Context, o Options) error {
	ms, err := o.Mounts()
	if err != nil {
		return fmt.Errorf("read mountinfo: %w", err)
	}
	js, err := json.Marshal(AvailableMounts(ms, o.HostPrefix))
	if err != nil {
		return err
	}
	return dbq.New(o.DB).SetAvailableMounts(ctx, dbq.SetAvailableMountsParams{
		HeartbeatAt: o.Now().Unix(), Version: o.Version, AvailableMounts: string(js),
	})
}

// heartbeat writes scanner_status every HeartbeatEvery. A failure is logged:
// the web sees a stale heartbeat, which is the right signal.
func heartbeat(ctx context.Context, o Options) {
	q := dbq.New(o.DB)
	tk := time.NewTicker(o.HeartbeatEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		if err := q.UpsertHeartbeat(ctx, dbq.UpsertHeartbeatParams{HeartbeatAt: o.Now().Unix(), Version: o.Version}); err != nil && ctx.Err() == nil {
			o.Log.Error("heartbeat failed", "err", err)
		}
	}
}

// poll is one round of the loop. A failing step is logged and does not stop
// the later ones.
func (s *service) poll(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	log := s.o.Log
	logErr := func(what string, err error) {
		if err != nil && ctx.Err() == nil {
			log.Error(what, "err", err)
		}
	}

	logErr("apply cancel jobs", s.applyCancels(ctx))
	now := s.o.Now()
	logErr("scheduler", s.sched.Tick(ctx, now))

	if h := s.o.hooks.startScan; h != nil && s.runningCount(kindScan) == 0 {
		h(ctx, s)
	}
	if h := s.o.hooks.startListing; h != nil {
		for i := 0; i < s.o.ListingWorkers && s.runningCount(kindListing) < s.o.ListingWorkers; i++ {
			if !h(ctx, s) {
				break
			}
		}
	}
	if h := s.o.hooks.cleanup; h != nil && (s.lastCleanup.IsZero() || now.Sub(s.lastCleanup) >= cleanupEvery) {
		s.lastCleanup = now
		h(ctx, s, now)
	}
}

// applyCancels claims every queued cancel job and applies it: the context of
// a running target is cancelled (its worker records the cancelled status), a
// queued target is cancelled in place, and any other target needs nothing. A
// cancel job with unusable params fails.
func (s *service) applyCancels(ctx context.Context) error {
	for {
		now := s.o.Now().Unix()
		j, err := s.q.ClaimJob(ctx, dbq.ClaimJobParams{Kind: kindCancel, StartedAt: sql.NullInt64{Int64: now, Valid: true}})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}

		s.finishCancel(ctx, j)
	}
}

// finishCancel applies one claimed cancel job and records its outcome. If
// applying or recording fails, the job is finished failed with the error text
// so it is never left running; if even that fails it is logged. Either way the
// caller goes on to the next cancel job.
func (s *service) finishCancel(ctx context.Context, j dbq.Job) {
	finish := func(status string, msg sql.NullString) error {
		_, err := s.q.FinishJob(ctx, dbq.FinishJobParams{
			Status: status, Error: msg, FinishedAt: sql.NullInt64{Int64: s.o.Now().Unix(), Valid: true}, ID: j.ID,
		})
		return err
	}
	fail := func(cause error) {
		s.o.Log.Error("cancel job failed", "job", j.ID, "err", cause)
		if err := finish("failed", sql.NullString{String: cause.Error(), Valid: true}); err != nil {
			s.o.Log.Error("cannot fail a cancel job", "job", j.ID, "err", err)
		}
	}

	var p db.CancelParams
	if err := json.Unmarshal([]byte(j.Params), &p); err != nil || p.JobID <= 0 {
		fail(errors.New("invalid cancel job"))
		return
	}
	if err := s.cancelJob(ctx, p.JobID); err != nil {
		fail(err)
		return
	}
	if err := finish("done", sql.NullString{}); err != nil {
		fail(err)
	}
}

func (s *service) cancelJob(ctx context.Context, id int64) error {
	s.mu.Lock()
	rj, ok := s.running[id]
	s.mu.Unlock()
	if ok {
		rj.cancel(errUserCancel)
		return nil
	}
	_, err := s.q.CancelQueuedJob(ctx, id)
	return err
}

// errUserCancel is the cancellation cause of a job a cancel job stopped. A
// job whose context is cancelled with any other cause was stopped by the
// service shutting down.
var errUserCancel = errors.New("cancelled by a cancel job")
