package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/noderef"
)

// MaxActiveListings is how many queued or running listing jobs one user may
// have at once.
const MaxActiveListings = 2

// ErrTooManyListings is returned when the user is at MaxActiveListings.
var ErrTooManyListings = errors.New("repo: too many active listings")

// CreateListing queues a file listing for a directory. The node is resolved
// through the scope first, so a ref outside the scope fails with ErrNotFound
// before anything is written. The limit check and the insert share one
// transaction.
func (r *Repo) CreateListing(ctx context.Context, s Scope, userID int64, ref noderef.Ref, p db.ListingParams, now time.Time) (int64, error) {
	n, err := r.Node(ctx, s, ref)
	if err != nil {
		return 0, err
	}
	params, err := json.Marshal(p)
	if err != nil {
		return 0, fmt.Errorf("repo: listing params: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("repo: create listing: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := r.q.WithTx(tx)
	active, err := q.CountActiveJobsByUser(ctx, dbq.CountActiveJobsByUserParams{
		Kind: string(db.KindListing), RequestedBy: sql.NullInt64{Int64: userID, Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("repo: create listing: %w", err)
	}
	if active >= MaxActiveListings {
		return 0, ErrTooManyListings
	}
	id, err := q.InsertJob(ctx, dbq.InsertJobParams{
		Kind: string(db.KindListing), RootID: n.RootID, TargetNode: sql.NullString{String: ref.String(), Valid: true},
		Params: string(params), RequestedBy: sql.NullInt64{Int64: userID, Valid: true}, CreatedAt: now.Unix(),
	})
	if err != nil {
		return 0, fmt.Errorf("repo: create listing: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("repo: create listing: %w", err)
	}
	return id, nil
}

// MaxListingPage is how many rows one Listing call returns.
const MaxListingPage = 500

// listingErrors are the messages a listing job may show; anything else is
// replaced, because jobs.error is not trusted to be free of host paths.
var listingErrors = map[string]bool{
	"not permitted": true, "changed since last scan": true, "scan no longer available": true,
	"target is on another filesystem": true, "scanner stopped": true,
}

// ListingRow is one file of a finished or running listing.
type ListingRow struct {
	Seq                     int64
	Name                    []byte
	Type                    string
	Size, Alloc, Mtime, UID int64
}

// ListingPage is a listing job with one page of its results. Next is the seq
// to pass back as afterSeq; 0 means the last page.
type ListingPage struct {
	ID        int64
	Status    string
	Truncated bool
	Error     *string
	Items     []ListingRow
	Next      int64
}

func listingVisible(c Caller, kind string, owner sql.NullInt64) bool {
	if kind != string(db.KindListing) {
		return false
	}
	return c.Admin || (owner.Valid && owner.Int64 == c.UserID)
}

// Listing reads a listing job and the rows after afterSeq. A missing job, a job
// of another kind and another user's job are all ErrNotFound; an admin may read
// any listing.
func (r *Repo) Listing(ctx context.Context, c Caller, id, afterSeq int64, limit int, now time.Time) (ListingPage, error) {
	if limit <= 0 || limit > MaxListingPage {
		limit = MaxListingPage
	}
	job, err := r.q.GetJob(ctx, id)
	if isNoRows(err) {
		return ListingPage{}, ErrNotFound
	}
	if err != nil {
		return ListingPage{}, fmt.Errorf("repo: listing %d: %w", id, err)
	}
	if !listingVisible(c, job.Kind, job.RequestedBy) || expired(job.ExpiresAt, now) {
		return ListingPage{}, ErrNotFound
	}
	page := ListingPage{ID: job.ID, Status: job.Status, Items: []ListingRow{}}
	if job.Progress.Valid {
		var p db.ListingProgress
		if err := json.Unmarshal([]byte(job.Progress.String), &p); err != nil {
			// A listing whose cap cannot be read must not look complete.
			r.log.Warn("unreadable listing progress", "job", id, "err", err)
			page.Truncated = true
		} else {
			page.Truncated = p.Truncated
		}
	}
	if job.Status == string(db.StatusFailed) || job.Error.Valid {
		msg := "listing failed"
		if job.Error.Valid && listingErrors[job.Error.String] {
			msg = job.Error.String
		}
		if job.Status == string(db.StatusFailed) {
			page.Error = &msg
		}
	}
	rows, err := r.q.ListListingResults(ctx, dbq.ListListingResultsParams{JobID: id, AfterSeq: afterSeq, RowLimit: int64(limit) + 1})
	if err != nil {
		return ListingPage{}, fmt.Errorf("repo: listing %d rows: %w", id, err)
	}
	if len(rows) > limit {
		rows = rows[:limit]
		page.Next = rows[limit-1].Seq
	}
	for _, x := range rows {
		page.Items = append(page.Items, ListingRow{Seq: x.Seq, Name: x.RelName, Type: x.Type, Size: x.Size, Alloc: x.Alloc, Mtime: x.Mtime, UID: x.Uid})
	}
	return page, nil
}

// CancelListing asks the scanner to stop a queued or running listing by
// inserting a cancel job. The job's own status is never touched here. A job
// that already ended, or that already has an active cancel, inserts nothing.
func (r *Repo) CancelListing(ctx context.Context, c Caller, id int64, now time.Time) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("repo: cancel listing: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := r.q.WithTx(tx)
	job, err := q.GetJob(ctx, id)
	if isNoRows(err) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("repo: cancel listing: %w", err)
	}
	if !listingVisible(c, job.Kind, job.RequestedBy) || expired(job.ExpiresAt, now) {
		return false, ErrNotFound
	}
	if job.Status != string(db.StatusQueued) && job.Status != string(db.StatusRunning) {
		return false, nil
	}
	active, err := q.HasActiveCancel(ctx, id)
	if err != nil {
		return false, fmt.Errorf("repo: cancel listing: %w", err)
	}
	if active != 0 {
		return false, nil
	}
	params, err := json.Marshal(db.CancelParams{JobID: id})
	if err != nil {
		return false, fmt.Errorf("repo: cancel listing: %w", err)
	}
	if _, err := q.InsertJob(ctx, dbq.InsertJobParams{
		Kind: string(db.KindCancel), RootID: job.RootID, Params: string(params),
		RequestedBy: sql.NullInt64{Int64: c.UserID, Valid: true}, CreatedAt: now.Unix(),
	}); err != nil {
		return false, fmt.Errorf("repo: cancel listing: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("repo: cancel listing: %w", err)
	}
	return true, nil
}

// expired reports whether a job is past its expiry; the scanner deletes such
// jobs, but until it does they must not be served.
func expired(at sql.NullInt64, now time.Time) bool { return at.Valid && at.Int64 < now.Unix() }
