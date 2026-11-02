package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/pathx"
	"github.com/vinRosso/hogwatch/internal/settings"
)

// withSnapshots adds snapshot bytes to the allocated total when
// quota.include_snapshots is on. It is the one place that rule lives.
func withSnapshots(t Totals, snapshots int64, include bool) Totals {
	if include {
		t.BytesAlloc += snapshots
	}
	return t
}

// quotaUsed is the figure a quota is measured against: allocated bytes, whatever the
// stored quota_basis says (the UI never offers the logical basis).
func quotaUsed(t Totals, _ string) int64 {
	return t.BytesAlloc
}

// RankRow is one user of the admin ranking.
type RankRow struct {
	UserID              int64
	Username            string
	Total               Totals
	SharedHardlinkBytes int64
	QuotaBytes          *int64
	QuotaPct            *float64
	GrowthBytes         *int64
}

// growthBases returns the latest history point at or before since for every
// (user, root) of one scope, keyed by [user_id, root_id] with 0 for NULL. It is
// the single definition of a growth base.
func (r *Repo) growthBases(ctx context.Context, scope string, since int64) (map[[2]int64]int64, error) {
	rows, err := r.q.GrowthBase(ctx, dbq.GrowthBaseParams{Scope: scope, Since: since})
	if err != nil {
		return nil, fmt.Errorf("repo: growth: %w", err)
	}
	out := make(map[[2]int64]int64, len(rows))
	for _, b := range rows {
		out[[2]int64{b.UserID.Int64, b.RootID.Int64}] = b.BytesAlloc
	}
	return out, nil
}

// growthBasesForUser is growthBases for one user's rows, so a per-user view does not
// read every other user's history. Both queries define a base the same way.
func (r *Repo) growthBasesForUser(ctx context.Context, scope string, userID, since int64) (map[[2]int64]int64, error) {
	rows, err := r.q.GrowthBaseForUser(ctx, dbq.GrowthBaseForUserParams{Scope: scope, UserID: sql.NullInt64{Int64: userID, Valid: true}, Since: since})
	if err != nil {
		return nil, fmt.Errorf("repo: growth: %w", err)
	}
	out := make(map[[2]int64]int64, len(rows))
	for _, b := range rows {
		out[[2]int64{b.UserID.Int64, b.RootID.Int64}] = b.BytesAlloc
	}
	return out, nil
}

// rankSrc is one user's usage figures before snapshots.
type rankSrc struct {
	userID                        int64
	username, basis               string
	quota                         sql.NullInt64
	logical, alloc, files, shared int64
}

// Ranking lists every user with their usage on the current scans, sorted by
// "usage" (allocated), "quota_pct" or "growth", largest first, nulls last, ties
// by username. rootID 0 ranks the all-roots totals; a root ID ranks usage and
// growth on that root only, while quota_pct stays the all-roots figure because
// a quota is a total. Growth is the allocated figure now minus the latest
// history point at or before now-window (user rows, or user_mount rows for a root).
func (r *Repo) Ranking(ctx context.Context, sortBy string, window time.Duration, rootID int64, now time.Time) ([]RankRow, error) {
	allRows, err := r.q.RankingRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo: ranking: %w", err)
	}
	allSnaps, err := r.q.RankingSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo: ranking snapshots: %w", err)
	}
	allSnap := map[int64]int64{}
	for _, s := range allSnaps {
		allSnap[s.UserID] = s.Snapshots
	}
	allOf := map[int64]Totals{}
	shown := make([]rankSrc, 0, len(allRows))
	for _, x := range allRows {
		allOf[x.UserID] = Totals{Files: x.Files, BytesLogical: x.BytesLogical, BytesAlloc: x.BytesAlloc}
		shown = append(shown, rankSrc{x.UserID, x.Username, x.QuotaBasis, x.QuotaBytes, x.BytesLogical, x.BytesAlloc, x.Files, x.SharedHardlinkBytes})
	}
	shownSnap, scope := allSnap, "user"
	rootScanned := true
	if rootID != 0 {
		root, err := r.q.GetRoot(ctx, rootID)
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("repo: ranking root: %w", err)
		}
		rootScanned = root.CurrentScanID.Valid
		rootRows, err := r.q.RankingRowsForRoot(ctx, rootID)
		if err != nil {
			return nil, fmt.Errorf("repo: ranking root: %w", err)
		}
		rs, err := r.q.RankingSnapshotsForRoot(ctx, rootID)
		if err != nil {
			return nil, fmt.Errorf("repo: ranking root snapshots: %w", err)
		}
		shownSnap, scope = map[int64]int64{}, "user_mount"
		for _, s := range rs {
			shownSnap[s.UserID] = s.Snapshots
		}
		shown = shown[:0]
		for _, x := range rootRows {
			shown = append(shown, rankSrc{x.UserID, x.Username, x.QuotaBasis, x.QuotaBytes, x.BytesLogical, x.BytesAlloc, x.Files, x.SharedHardlinkBytes})
		}
	}
	base, err := r.growthBases(ctx, scope, now.Add(-window).Unix())
	if err != nil {
		return nil, err
	}
	include := r.boolSetting(ctx, settings.IncludeSnapshots)
	out := make([]RankRow, 0, len(shown))
	for _, x := range shown {
		t := withSnapshots(Totals{Files: x.files, BytesLogical: x.logical, BytesAlloc: x.alloc}, shownSnap[x.userID], include)
		row := RankRow{UserID: x.userID, Username: x.username, Total: t, SharedHardlinkBytes: x.shared}
		if x.quota.Valid {
			q := x.quota.Int64
			row.QuotaBytes = &q
			if q > 0 {
				whole := withSnapshots(allOf[x.userID], allSnap[x.userID], include)
				pct := float64(quotaUsed(whole, x.basis)) / float64(q) * 100
				row.QuotaPct = &pct
			}
		}
		if b, ok := base[[2]int64{x.userID, rootID}]; ok && rootScanned {
			g := x.alloc - b
			row.GrowthBytes = &g
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch sortBy {
		case "quota_pct":
			if c := cmpNil(a.QuotaPct, b.QuotaPct); c != 0 {
				return c > 0
			}
		case "growth":
			if c := cmpNil(a.GrowthBytes, b.GrowthBytes); c != 0 {
				return c > 0
			}
		default:
			if a.Total.BytesAlloc != b.Total.BytesAlloc {
				return a.Total.BytesAlloc > b.Total.BytesAlloc
			}
		}
		return a.Username < b.Username
	})
	return out, nil
}

// cmpNil orders pointers largest first with nil last: positive means a ranks
// before b.
func cmpNil[T int64 | float64](a, b *T) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case *a > *b:
		return 1
	case *a < *b:
		return -1
	}
	return 0
}

// MountRow is one root with the numbers of its current scan.
type MountRow struct {
	RootID                                    int64
	RootName                                  string
	BytesAlloc, FSTotal, FSFree               *int64
	ZFSUsed, ZFSAvail, ZFSQuota, ZFSSnapshots *int64
	GrowthBytes                               *int64
}

// Mounts lists every root; a root without a current scan has nil numbers.
// Growth is the root's allocated bytes now minus the latest mount history point
// at or before now-window.
func (r *Repo) Mounts(ctx context.Context, window time.Duration, now time.Time) ([]MountRow, error) {
	rows, err := r.q.ListMountRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo: mounts: %w", err)
	}
	base, err := r.growthBases(ctx, "mount", now.Add(-window).Unix())
	if err != nil {
		return nil, err
	}
	out := make([]MountRow, 0, len(rows))
	for _, x := range rows {
		out = append(out, MountRow{
			RootID: x.RootID, RootName: x.RootName,
			BytesAlloc: nullable(x.ScanBytesAlloc.Valid, x.ScanBytesAlloc.Int64),
			FSTotal:    nullable(x.FsTotal.Valid, x.FsTotal.Int64), FSFree: nullable(x.FsFree.Valid, x.FsFree.Int64),
			ZFSUsed: nullable(x.ZfsUsed.Valid, x.ZfsUsed.Int64), ZFSAvail: nullable(x.ZfsAvail.Valid, x.ZfsAvail.Int64),
			ZFSQuota: nullable(x.ZfsQuota.Valid, x.ZfsQuota.Int64), ZFSSnapshots: nullable(x.ZfsUsedSnapshots.Valid, x.ZfsUsedSnapshots.Int64),
		})
		if b, ok := base[[2]int64{0, x.RootID}]; ok && x.ScanBytesAlloc.Valid {
			g := x.ScanBytesAlloc.Int64 - b
			out[len(out)-1].GrowthBytes = &g
		}
	}
	return out, nil
}

// Issue is a configuration issue: stored by the scanner (ID set) or derived on
// read from the current assignments and stats (ID nil).
type Issue struct {
	ID        *int64
	RootID    int64
	Kind      string
	Details   json.RawMessage
	CreatedAt int64
}

// ConfigIssues returns the stored issues, minus cross_root_duplicate rows whose
// path is gone, plus the redundant_path, overlap and shared_hardlinks issues
// derived now. Order: root, kind, stored before derived, id, details.
func (r *Repo) ConfigIssues(ctx context.Context, now time.Time) ([]Issue, error) {
	paths, err := r.q.ListAllUserPaths(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo: issues paths: %w", err)
	}
	sort.SliceStable(paths, func(i, j int) bool { return paths[i].RootID < paths[j].RootID })
	alive := map[int64]bool{}
	for _, p := range paths {
		alive[p.ID] = true
	}
	stored, err := r.q.ListStoredIssues(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo: issues: %w", err)
	}
	var out []Issue
	for _, s := range stored {
		if s.Kind == "cross_root_duplicate" {
			var d struct {
				UserPathID int64 `json:"user_path_id"`
			}
			if json.Unmarshal([]byte(s.Details), &d) != nil || !alive[d.UserPathID] {
				continue
			}
		}
		id := s.ID
		out = append(out, Issue{ID: &id, RootID: s.RootID, Kind: s.Kind, Details: json.RawMessage(s.Details), CreatedAt: s.CreatedAt})
	}
	add := func(root int64, kind string, d any) error {
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		out = append(out, Issue{RootID: root, Kind: kind, Details: b, CreatedAt: now.Unix()})
		return nil
	}
	for i, a := range paths {
		for _, b := range paths[i+1:] {
			if a.RootID != b.RootID {
				break // ordered by root
			}
			rel := pathx.Compare(string(a.RelPath), string(b.RelPath))
			if rel == pathx.Disjoint {
				continue
			}
			var err error
			switch {
			case a.UserID != b.UserID:
				err = add(a.RootID, "overlap", map[string]any{"user_path_ids": []int64{a.ID, b.ID}})
			case rel == pathx.AInsideB:
				err = add(a.RootID, "redundant_path", map[string]any{"user_id": a.UserID, "user_path_id": a.ID, "inside_user_path_id": b.ID})
			default: // BInsideA; equal paths cannot exist (UNIQUE)
				err = add(a.RootID, "redundant_path", map[string]any{"user_id": a.UserID, "user_path_id": b.ID, "inside_user_path_id": a.ID})
			}
			if err != nil {
				return nil, err
			}
		}
	}
	shared, err := r.q.ListPathStatsShared(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo: issues shared: %w", err)
	}
	for _, s := range shared {
		if err := add(s.RootID, "shared_hardlinks", map[string]any{"user_id": s.UserID, "user_path_id": s.UserPathID, "bytes": s.SharedHardlinkBytes}); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.RootID != b.RootID {
			return a.RootID < b.RootID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if (a.ID == nil) != (b.ID == nil) {
			return a.ID != nil
		}
		if a.ID != nil && *a.ID != *b.ID {
			return *a.ID < *b.ID
		}
		return string(a.Details) < string(b.Details)
	})
	return out, nil
}
