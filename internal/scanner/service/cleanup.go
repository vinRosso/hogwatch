//go:build linux

package service

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/vinRosso/hogwatch/internal/gen"
)

// cleanup deletes the generation files of scans a newer scan of their root
// superseded more than Options.Grace ago, marks those scans deleted, and
// sweeps generation files no live scan names (those of a deleted root), and
// deletes expired finished jobs (with their listing rows). A file that cannot
// be removed is logged and retried on the next run; a file already gone is
// fine.
func (s *service) cleanup(ctx context.Context, now time.Time) {
	log := s.o.Log
	old, err := s.q.SupersededScans(ctx, sql.NullInt64{Int64: now.Add(-s.o.Grace).Unix(), Valid: true})
	if err != nil {
		if ctx.Err() == nil {
			log.Error("cleanup: list superseded scans", "err", err)
		}
		old = nil
	}
	for _, sc := range old {
		if err := os.Remove(gen.Path(s.o.DataDir, sc.RootID, sc.ID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Error("cleanup: cannot delete a superseded generation", "scan", sc.ID, "err", err)
			continue
		}
		if _, err := s.q.MarkScanDeleted(ctx, sc.ID); err != nil {
			if ctx.Err() == nil {
				log.Error("cleanup: mark scan deleted", "scan", sc.ID, "err", err)
			}
			continue
		}
		log.Info("deleted a superseded generation", "scan", sc.ID, "root", sc.RootID)
	}
	if err := removeOrphans(ctx, s.o, s.q, true); err != nil && ctx.Err() == nil {
		log.Error("cleanup: sweep orphan generations", "err", err)
	}
	if _, err := s.q.DeleteExpiredJobs(ctx, sql.NullInt64{Int64: now.Unix(), Valid: true}); err != nil && ctx.Err() == nil {
		log.Error("cleanup: delete expired jobs", "err", err)
	}
}
