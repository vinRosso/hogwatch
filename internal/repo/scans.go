package repo

import (
	"context"
	"fmt"

	"database/sql"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

// maxScanRows caps the scan list an admin gets.
const maxScanRows = 500

// ScanRow is a scan as users see it. It has no error text: scan errors can
// hold host paths.
type ScanRow struct {
	ID, RootID              int64
	Status, Trigger         string
	StartedAt               int64
	FinishedAt              *int64
	Dirs, Files, BytesAlloc int64
}

// Scans lists scans newest first. An admin sees every scan (deleted ones
// included) up to maxScanRows; a user sees the current and any running scan of
// the roots they have a path on. root, when set, narrows either list; a root
// the user has no path on gives an empty list.
func (r *Repo) Scans(ctx context.Context, c Caller, root *int64) ([]ScanRow, error) {
	var out []ScanRow
	if c.Admin {
		arg := sql.NullInt64{}
		if root != nil {
			arg = sql.NullInt64{Int64: *root, Valid: true}
		}
		rows, err := r.q.ListScansAdmin(ctx, dbq.ListScansAdminParams{RootID: arg, RowLimit: maxScanRows})
		if err != nil {
			return nil, fmt.Errorf("repo: scans: %w", err)
		}
		for _, x := range rows {
			out = append(out, ScanRow{x.ID, x.RootID, x.Status, x.Trigger, x.StartedAt,
				nullable(x.FinishedAt.Valid, x.FinishedAt.Int64), x.Dirs, x.Files, x.BytesAlloc})
		}
		return out, nil
	}
	rows, err := r.q.ListCurrentAndRunningScans(ctx, c.UserID)
	if err != nil {
		return nil, fmt.Errorf("repo: scans: %w", err)
	}
	for _, x := range rows {
		if root != nil && x.RootID != *root {
			continue
		}
		out = append(out, ScanRow{x.ID, x.RootID, x.Status, x.Trigger, x.StartedAt,
			nullable(x.FinishedAt.Valid, x.FinishedAt.Int64), x.Dirs, x.Files, x.BytesAlloc})
	}
	return out, nil
}
