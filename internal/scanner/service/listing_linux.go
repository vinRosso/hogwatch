//go:build linux

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/scanner/listing"
)

// startListing is the poll loop's startListing hook: it claims one queued
// listing job and spawns its run at once, so a cancel job can find the running
// target on the same or the next poll.
func startListing(ctx context.Context, s *service) bool {
	j, err := s.q.ClaimJob(ctx, dbq.ClaimJobParams{
		Kind: kindListing, StartedAt: sql.NullInt64{Int64: s.o.Now().Unix(), Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		if ctx.Err() == nil {
			s.o.Log.Error("claim listing job", "err", err)
		}
		return false
	}
	s.spawn(ctx, kindListing, j.ID, func(jctx context.Context) { s.runListing(jctx, j) })
	return true
}

// runListing runs one claimed listing job and sets its terminal status:
//
//   - success: done, with the progress (rows, truncated, errors,
//     mounts_skipped);
//   - a cancel job (the context's cause is errUserCancel): cancelled;
//   - any other cancellation, the service shutting down: failed with
//     "scanner stopped";
//   - any other error: failed with one of the fixed messages of the listing
//     package. A raw filesystem or database error is logged by the listing and
//     never stored, because it can name a host path.
//
// The terminal write uses a context that is never cancelled, and sets
// expires_at to the finish time plus ListingTTL.
func (s *service) runListing(ctx context.Context, job dbq.Job) {
	prog, err := s.listing(ctx, job)

	status, msg := db.StatusDone, ""
	switch {
	case err != nil && ctx.Err() != nil:
		// The job was stopped from outside; its rows are gone already
		// (listing.Run deletes them on any error). A listing that finished
		// just before the stop stays done.
		if errors.Is(context.Cause(ctx), errUserCancel) {
			status = db.StatusCancelled
		} else {
			status, msg = db.StatusFailed, "scanner stopped"
		}
	case err != nil:
		status, msg = db.StatusFailed, fixedListingMessage(err)
		if msg == listing.ErrIO.Error() {
			s.o.Log.Warn("listing failed", "job", job.ID, "root", job.RootID, "err", err)
		}
	}

	fctx := context.WithoutCancel(ctx)
	now := s.o.Now()
	if status == db.StatusDone {
		if js, merr := json.Marshal(prog); merr == nil {
			if perr := s.q.UpdateJobProgress(fctx, dbq.UpdateJobProgressParams{
				Progress: sql.NullString{String: string(js), Valid: true}, ID: job.ID,
			}); perr != nil {
				s.o.Log.Warn("cannot write listing progress", "job", job.ID, "err", perr)
			}
		}
	}
	if _, ferr := s.q.FinishListingJob(fctx, dbq.FinishListingJobParams{
		Status:     string(status),
		Error:      sql.NullString{String: msg, Valid: msg != ""},
		FinishedAt: sql.NullInt64{Int64: now.Unix(), Valid: true},
		ExpiresAt:  sql.NullInt64{Int64: now.Add(s.o.ListingTTL).Unix(), Valid: true},
		ID:         job.ID,
	}); ferr != nil {
		s.o.Log.Error("cannot finish a listing job", "job", job.ID, "err", ferr)
	}
}

// listing loads the job's root and runs listing.Run with a high-priority gate.
func (s *service) listing(ctx context.Context, job dbq.Job) (db.ListingProgress, error) {
	root, err := s.q.GetRoot(ctx, job.RootID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return db.ListingProgress{}, listing.ErrGone
	case err != nil:
		return db.ListingProgress{}, err
	}
	return listing.Run(ctx, listing.Deps{
		DB: s.o.DB, DataDir: s.o.DataDir, Gate: s.o.Gate.High(),
		Now: func() time.Time { return s.o.Now() },
	}, job, root)
}

// fixedListingMessage maps an error of a listing to the text stored in
// jobs.error: the message of a listing sentinel, or ErrIO's for anything else.
func fixedListingMessage(err error) string {
	for _, sentinel := range []error{
		listing.ErrNotPermitted, listing.ErrChanged, listing.ErrGone, listing.ErrCrossesMount, listing.ErrIO,
	} {
		if errors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}
	return listing.ErrIO.Error()
}
