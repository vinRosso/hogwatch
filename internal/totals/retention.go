package totals

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Retention windows: raw points for rawDays, then one point per
// ISO week (UTC) up to weeklyDays, then one per calendar month (UTC).
const (
	rawDays    = 30
	weeklyDays = 365
)

// RetainStats counts what one Retain pass changed: rows moved to a coarser
// tier, rows deleted, and history_paths rows no history_dirs row references.
type RetainStats struct {
	Promoted, Deleted, PathsDeleted int64
}

// histRow is one history row as retention sees it.
type histRow struct {
	rowid int64
	ts    int64
	tier  string
}

// bucket identifies a tier and the period inside that tier.
type bucket struct {
	tier   string
	period [2]int // ISO year and week, or year and month
}

func bucketOf(ts, now time.Time) (b bucket, collapses bool) {
	switch {
	case !ts.Before(now.AddDate(0, 0, -rawDays)):
		return bucket{tier: "raw"}, false
	case !ts.Before(now.AddDate(0, 0, -weeklyDays)):
		y, w := ts.ISOWeek()
		return bucket{"weekly", [2]int{y, w}}, true
	}
	return bucket{"monthly", [2]int{ts.Year(), int(ts.Month())}}, true
}

// Retain applies the retention tiers to history_totals, keyed by (scope,
// user_id, root_id), and to history_dirs, keyed by (root_id, path_hash). A
// row older than 30 days belongs to the weekly tier, one older than 365 days
// to the monthly tier. In each bucket of a tier the latest row survives with
// that tier and the others are deleted. Rows still inside the raw window are
// untouched. It then deletes history_paths rows no history_dirs row
// references. It is idempotent for a fixed now.
func Retain(ctx context.Context, tx *sql.Tx, now time.Time) (RetainStats, error) {
	now = now.UTC()
	var st RetainStats
	for _, t := range []struct {
		table, key string
	}{
		{"history_totals", "scope, coalesce(user_id, -1), coalesce(root_id, -1)"},
		{"history_dirs", "root_id, path_hash"},
	} {
		if err := retainTable(ctx, tx, now, t.table, t.key, &st); err != nil {
			return RetainStats{}, err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM history_paths WHERE NOT EXISTS
		(SELECT 1 FROM history_dirs d WHERE d.path_hash = history_paths.path_hash)`)
	if err != nil {
		return RetainStats{}, fmt.Errorf("totals: retain: orphan paths: %w", err)
	}
	if st.PathsDeleted, err = res.RowsAffected(); err != nil {
		return RetainStats{}, fmt.Errorf("totals: retain: orphan paths: %w", err)
	}
	return st, nil
}

func retainTable(ctx context.Context, tx *sql.Tx, now time.Time, table, key string, st *RetainStats) error {
	cutoff := now.AddDate(0, 0, -rawDays).Unix()
	rs, err := tx.QueryContext(ctx, fmt.Sprintf(
		`SELECT rowid, ts, tier, %s FROM %s WHERE ts < ? ORDER BY rowid`, key, table), cutoff)
	if err != nil {
		return fmt.Errorf("totals: retain %s: %w", table, err)
	}
	type group struct {
		scope string
		a, b  int64
		bkt   bucket
	}
	groups := map[group][]histRow{}
	for rs.Next() {
		var (
			r histRow
			g group
		)
		if table == "history_totals" {
			err = rs.Scan(&r.rowid, &r.ts, &r.tier, &g.scope, &g.a, &g.b)
		} else {
			err = rs.Scan(&r.rowid, &r.ts, &r.tier, &g.a, &g.b)
		}
		if err != nil {
			_ = rs.Close()
			return fmt.Errorf("totals: retain %s: %w", table, err)
		}
		b, collapses := bucketOf(time.Unix(r.ts, 0).UTC(), now)
		if !collapses {
			continue
		}
		g.bkt = b
		groups[g] = append(groups[g], r)
	}
	err = rs.Err()
	_ = rs.Close()
	if err != nil {
		return fmt.Errorf("totals: retain %s: %w", table, err)
	}

	for g, rows := range groups {
		keep := rows[0]
		for _, r := range rows[1:] {
			if r.ts > keep.ts || (r.ts == keep.ts && r.rowid > keep.rowid) {
				keep = r
			}
		}
		for _, r := range rows {
			if r.rowid == keep.rowid {
				continue
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE rowid = ?`, table), r.rowid); err != nil {
				return fmt.Errorf("totals: retain %s: %w", table, err)
			}
			st.Deleted++
		}
		if keep.tier != g.bkt.tier {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET tier = ? WHERE rowid = ?`, table), g.bkt.tier, keep.rowid); err != nil {
				return fmt.Errorf("totals: retain %s: %w", table, err)
			}
			st.Promoted++
		}
	}
	return nil
}
