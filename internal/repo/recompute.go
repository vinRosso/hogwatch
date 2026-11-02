package repo

import (
	"context"
	"fmt"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/totals"
)

// UserUsage is one user's usage on one root and the rollup of each of their
// paths, as a publish would write them. Usage is nil for a user without paths.
type UserUsage struct {
	Usage *totals.UsageRow
	Paths []totals.PathRow
}

// ComputeUserUsage computes the usage of the user who owns paths (all of them
// on one root) against scan scanID. It reads only the generation, never the
// main database, so a caller can run it outside a write transaction. totals
// computes each user independently of the others, so the result equals the
// rows a publish writes for that user. A scan that cannot be served gives
// ErrNotFound.
func (r *Repo) ComputeUserUsage(ctx context.Context, scanID int64, paths []totals.Path) (UserUsage, error) {
	if len(paths) == 0 {
		return UserUsage{}, nil
	}
	e, err := r.open(ctx, scanID)
	if err != nil {
		return UserUsage{}, err
	}
	defer r.release(e)
	usage, rows, err := totals.Usage(ctx, e.db, paths)
	if err != nil {
		return UserUsage{}, r.fix(scanID, err)
	}
	out := UserUsage{Paths: rows}
	if len(usage) == 1 {
		out.Usage = &usage[0]
	}
	return out, nil
}

// ApplyUserUsage replaces the user's usage row and path_stats rows of scan
// scanID with u, inside the transaction q is bound to. The ZFS columns of a
// path that already had a row are kept: the web has no ZFS file, so it cannot
// recompute them, and a new path gets NULLs until the next scan.
func ApplyUserUsage(ctx context.Context, q *dbq.Queries, scanID, rootID, userID int64, u UserUsage) error {
	old, err := q.ListPathStatsZFS(ctx, dbq.ListPathStatsZFSParams{ScanID: scanID, UserID: userID})
	if err != nil {
		return fmt.Errorf("repo: read zfs columns: %w", err)
	}
	zfs := make(map[int64]dbq.ListPathStatsZFSRow, len(old))
	for _, o := range old {
		zfs[o.UserPathID] = o
	}
	if err := q.DeleteUsageForUser(ctx, dbq.DeleteUsageForUserParams{ScanID: scanID, UserID: userID}); err != nil {
		return fmt.Errorf("repo: delete usage: %w", err)
	}
	if err := q.DeletePathStatsForUser(ctx, dbq.DeletePathStatsForUserParams{ScanID: scanID, UserID: userID}); err != nil {
		return fmt.Errorf("repo: delete path stats: %w", err)
	}
	if u.Usage != nil {
		err := q.InsertUsage(ctx, dbq.InsertUsageParams{
			ScanID: scanID, UserID: userID, RootID: rootID, BytesLogical: u.Usage.BytesLogical,
			BytesAlloc: u.Usage.BytesAlloc, Files: u.Usage.Files, SharedHardlinkBytes: u.Usage.SharedHardlinkBytes,
		})
		if err != nil {
			return fmt.Errorf("repo: insert usage: %w", err)
		}
	}
	for _, p := range u.Paths {
		ps := dbq.InsertPathStatsParams{
			ScanID: scanID, UserPathID: p.UserPathID, Found: b2i(p.Found), Redundant: b2i(p.Redundant),
			BytesLogical: p.BytesLogical, BytesAlloc: p.BytesAlloc, Files: p.Files, SharedHardlinkBytes: p.SharedHardlinkBytes,
		}
		if z, ok := zfs[p.UserPathID]; ok {
			ps.ZfsDataset, ps.ZfsUsed, ps.ZfsAvail = z.ZfsDataset, z.ZfsUsed, z.ZfsAvail
			ps.ZfsQuota, ps.ZfsUsedSnapshots = z.ZfsQuota, z.ZfsUsedSnapshots
		}
		if err := q.InsertPathStats(ctx, ps); err != nil {
			return fmt.Errorf("repo: insert path stats: %w", err)
		}
	}
	return nil
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
