//go:build linux

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/schedule"
)

// Scheduler queues a scan job for each root whose schedule has fired. A
// schedule is a standard 5-field cron expression (or @daily and the like),
// in UTC unless it starts with CRON_TZ=<zone>.
//
// A root is due when schedule.Next(lastCheck) <= now. lastCheck starts at the
// root's latest scans.started_at (zero if it was never scanned, which is due
// at once), so a root that missed any number of fire times while the scanner
// was down gets exactly one scan. A due root sets lastCheck to now, whether or
// not a job was queued: a scan job already queued or running for the root
// covers the fire.
type Scheduler struct {
	d   *sql.DB
	q   *dbq.Queries
	log *slog.Logger

	mu    sync.Mutex
	roots map[int64]*rootState // by root id; entries of deleted roots are dropped
}

type rootState struct {
	text      string        // the schedule text last evaluated
	sched     cron.Schedule // nil when text is empty or invalid
	lastCheck time.Time
	seeded    bool
}

// NewScheduler returns a Scheduler on the main DB.
func NewScheduler(d *sql.DB, log *slog.Logger) *Scheduler {
	return &Scheduler{d: d, q: dbq.New(d), log: log, roots: map[int64]*rootState{}}
}

// Tick evaluates every root at time now. A failure on one root is logged and
// the other roots are still evaluated; the first error is returned.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) error {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	roots, err := s.q.ListRoots(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: list roots: %w", err)
	}
	live := make(map[int64]bool, len(roots))
	var first error
	for _, r := range roots {
		live[r.ID] = true
		if err := s.tickRoot(ctx, r, now); err != nil {
			s.log.Error("schedule evaluation failed", "root", r.ID, "err", err)
			if first == nil {
				first = err
			}
		}
	}
	for id := range s.roots {
		if !live[id] {
			delete(s.roots, id)
		}
	}
	return first
}

func (s *Scheduler) tickRoot(ctx context.Context, r dbq.ScanRoot, now time.Time) error {
	st := s.roots[r.ID]
	if st == nil {
		st = &rootState{}
		s.roots[r.ID] = st
		st.text = "\x00" // never equal to a real schedule: forces the first evaluation
	}
	if st.text != r.Schedule {
		if err := s.evaluate(ctx, r, st, now); err != nil {
			st.text = "\x00" // retry next tick
			return err
		}
	}
	if r.Enabled == 0 || st.sched == nil {
		return nil
	}

	if !st.seeded {
		last, err := s.q.LastScanStart(ctx, r.ID)
		if err != nil {
			return fmt.Errorf("last scan start: %w", err)
		}
		if last != 0 {
			st.lastCheck = time.Unix(last, 0).UTC()
		}
		st.seeded = true
	}
	if !st.lastCheck.IsZero() {
		next := st.sched.Next(st.lastCheck)
		if next.IsZero() || next.After(now) {
			return nil
		}
	}

	active, err := s.q.CountActiveScanJobs(ctx, r.ID)
	if err != nil {
		return fmt.Errorf("count active scan jobs: %w", err)
	}
	if active == 0 {
		params, err := json.Marshal(db.ScanParams{Trigger: "schedule"})
		if err != nil {
			return err
		}
		if _, err := s.q.InsertJob(ctx, dbq.InsertJobParams{
			Kind: string(kindScan), RootID: r.ID, Params: string(params), CreatedAt: now.Unix(),
		}); err != nil {
			return fmt.Errorf("queue scheduled scan: %w", err)
		}
		s.log.Info("queued a scheduled scan", "root", r.ID)
	}
	st.lastCheck = now
	return nil
}

// evaluate parses the root's schedule and replaces its invalid_schedule
// issue: one issue when the text is invalid, none otherwise.
func (s *Scheduler) evaluate(ctx context.Context, r dbq.ScanRoot, st *rootState, now time.Time) error {
	var sched cron.Schedule
	var parseErr error
	if r.Schedule != "" {
		sched, parseErr = schedule.Parse(r.Schedule)
	}

	tx, err := s.d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if err := q.DeleteIssues(ctx, dbq.DeleteIssuesParams{RootID: r.ID, Kind: string(db.IssueInvalidSchedule)}); err != nil {
		return fmt.Errorf("delete invalid_schedule issue: %w", err)
	}
	if parseErr != nil {
		details, err := json.Marshal(db.InvalidScheduleDetails{Schedule: r.Schedule, Error: parseErr.Error()})
		if err != nil {
			return err
		}
		if err := q.InsertIssue(ctx, dbq.InsertIssueParams{
			RootID: r.ID, Kind: string(db.IssueInvalidSchedule), Details: string(details), CreatedAt: now.Unix(),
		}); err != nil {
			return fmt.Errorf("insert invalid_schedule issue: %w", err)
		}
		s.log.Warn("invalid schedule", "root", r.ID, "err", parseErr)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	st.text, st.sched = r.Schedule, sched
	if parseErr != nil {
		st.sched = nil
	}
	return nil
}
