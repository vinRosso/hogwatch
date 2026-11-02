//go:build linux

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

// restartMessage is the jobs.error and scans.error of work a crash cut short.
const restartMessage = "scanner restarted"

// genFileName matches a generation file name, <root_id>-<scan_id>.db.
var genFileName = regexp.MustCompile(`^(\d+)-(\d+)\.db$`)

// Recover cleans up after a scanner that stopped without finishing. It runs
// before the poller starts, so nothing else writes at the same time:
//
//  1. every running job is failed, and the partial rows of each failed
//     listing are deleted;
//  2. every running scan is failed;
//  3. every generation file in <DataDir>/scans without a done scans row of
//     that id and root is deleted. A file whose name is not a generation name
//     is logged and left alone.
func Recover(ctx context.Context, o Options) error {
	o = o.withDefaults()
	q := dbq.New(o.DB)
	now := o.Now()

	failed, err := q.FailRunningJobsAtStartup(ctx, dbq.FailRunningJobsAtStartupParams{
		Error:            sql.NullString{String: restartMessage, Valid: true},
		FinishedAt:       sql.NullInt64{Int64: now.Unix(), Valid: true},
		ListingExpiresAt: sql.NullInt64{Int64: now.Add(o.ListingTTL).Unix(), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("recover: fail running jobs: %w", err)
	}
	for _, j := range failed {
		o.Log.Warn("failed a job the previous scanner left running", "job", j.ID, "kind", j.Kind)
		if j.Kind != string(kindListing) {
			continue
		}
		if err := q.DeleteListingResults(ctx, j.ID); err != nil {
			return fmt.Errorf("recover: delete listing %d results: %w", j.ID, err)
		}
	}

	n, err := q.FailRunningScans(ctx, dbq.FailRunningScansParams{
		Error:      sql.NullString{String: restartMessage, Valid: true},
		FinishedAt: sql.NullInt64{Int64: now.Unix(), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("recover: fail running scans: %w", err)
	}
	if n > 0 {
		o.Log.Warn("failed scans the previous scanner left running", "count", n)
	}

	return removeOrphans(ctx, o, q, false)
}

type genKey struct{ root, scan int64 }

// removeOrphans deletes the generation files in the scans directory that no
// scan row of a servable generation names. At startup nothing is running, so
// only done scans keep their file and sort spill files are stale. Periodically
// (running true) a running scan's file and spill files are in use and stay.
func removeOrphans(ctx context.Context, o Options, q *dbq.Queries, running bool) error {
	dir := filepath.Join(o.DataDir, "scans")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recover: read %s: %w", dir, err)
	}
	var done []dbq.ListScansWithFilesRow
	if running {
		live, err := q.ListLiveScansWithFiles(ctx)
		if err != nil {
			return fmt.Errorf("sweep: list live scans: %w", err)
		}
		for _, l := range live {
			done = append(done, dbq.ListScansWithFilesRow(l))
		}
	} else if done, err = q.ListScansWithFiles(ctx); err != nil { // status = 'done'
		return fmt.Errorf("recover: list done scans: %w", err)
	}
	keep := make(map[genKey]bool, len(done))
	for _, s := range done {
		keep[genKey{s.RootID, s.ID}] = true
	}
	for _, e := range entries {
		// SQLite sort spill files (see gen.Writer) a hard kill left behind.
		if strings.HasPrefix(e.Name(), "etilqs_") && e.Type().IsRegular() {
			if running {
				continue // a running scan may be using it
			}
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				o.Log.Error("cannot delete a stale sort temp file", "name", e.Name(), "err", err)
			}
			continue
		}
		m := genFileName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			o.Log.Info("leaving an unrecognised entry in the scans directory", "name", e.Name())
			continue
		}
		root, err1 := strconv.ParseInt(m[1], 10, 64)
		scan, err2 := strconv.ParseInt(m[2], 10, 64)
		// Only the canonical spelling of a done scan's file is kept: 01-2.db is
		// not 1-2.db, and numbers that overflow int64 name no scan at all.
		if err1 == nil && err2 == nil && keep[genKey{root, scan}] && fmt.Sprintf("%d-%d.db", root, scan) == e.Name() {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Not fatal: a stuck file must not keep the scanner from starting.
			o.Log.Error("cannot delete an orphan generation file", "name", e.Name(), "err", err)
			continue
		}
		o.Log.Warn("deleted an orphan generation file", "name", e.Name())
	}
	return nil
}
