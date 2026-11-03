package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/vinRosso/hogwatch/internal/settings"
)

// Caller identifies who asks for main-database data that is not tied to a
// generation: roots, usage, scans and listings.
type Caller struct {
	UserID int64
	Admin  bool
	// CanList is set for a user who may request file listings (can_list_files, or an admin).
	CanList bool
}

// boolSetting reads a boolean setting. A missing row gives the registry
// default; an unreadable row gives false and is logged; an unparsable value
// gives the default.
func (r *Repo) boolSetting(ctx context.Context, key string) bool {
	d, _ := settings.Lookup(key)
	def := d.Default == "true"
	v, err := r.q.GetSetting(ctx, key)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return def
	case err != nil:
		r.log.Error("read setting", "key", key, "err", err)
		return false // fail closed: a setting that cannot be read hides capacity and snapshots
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.log.Warn("setting is not a boolean", "key", key)
		return def
	}
	return b
}

// hostPath is the host path label of a root-relative path.
func hostPath(root string, rel []byte) []byte {
	if len(rel) == 0 {
		return []byte(root)
	}
	return append(append([]byte(root), '/'), rel...)
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// pathRow is one path assignment of a user with the stats of its root's
// current scan (zeros and found = false without one).
type pathRow struct {
	id, rootID                       int64
	rel                              []byte
	host                             string
	found, redundant                 bool
	files, logical, alloc, snapshots int64
}

func (r *Repo) pathRows(ctx context.Context, userID int64) ([]pathRow, error) {
	rows, err := r.q.ListPathStatsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("repo: user paths: %w", err)
	}
	out := make([]pathRow, 0, len(rows))
	for _, x := range rows {
		out = append(out, pathRow{
			id: x.UserPathID, rootID: x.RootID, rel: x.RelPath, host: x.HostPath,
			found: x.Found != 0, redundant: x.Redundant != 0,
			files: x.Files, logical: x.BytesLogical, alloc: x.BytesAlloc, snapshots: x.ZfsUsedSnapshots,
		})
	}
	return out, nil
}
