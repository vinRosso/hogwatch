package totals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"

	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/pathx"
	"github.com/vinRosso/hogwatch/internal/zfs"
)

// Path is a path assigned to a user on the root a generation was built for.
// Rel is pathx-normalised and may hold any bytes but "/" and NUL; "" is the
// root itself.
type Path struct {
	ID, UserID int64
	Rel        string
}

// UsageRow is one user's usage on the root: the sum of the rollups of their
// effective paths plus the hardlink charge. SharedHardlinkBytes is the
// alloc part of that charge: what the user's tree shows less than their usage.
type UsageRow struct {
	UserID                          int64
	BytesLogical, BytesAlloc, Files int64
	SharedHardlinkBytes             int64
}

// PathRow is the rollup of one assigned path. A path missing from the
// generation has Found false and zeros. A redundant path (nested in, or a
// duplicate of, another path of the same user) carries its own rollup but is
// not added to the user's usage. SharedHardlinkBytes is the alloc of the
// shared inodes with a link inside this path and the winning link outside it.
type PathRow struct {
	UserPathID                      int64
	Found, Redundant                bool
	BytesLogical, BytesAlloc, Files int64
	SharedHardlinkBytes             int64
}

// span is the closed pre-order interval of one directory subtree.
type span struct{ pre, post int64 }

// target is a set of disjoint spans (a user's effective paths, or a single
// path) that shared inodes are tested against, and what was charged to it.
type target struct {
	spans []span // sorted by pre
	has   bool   // a link of the current inode group lies inside

	size, alloc, files int64 // charged for shared inodes whose winner is outside
}

func (t *target) contains(pre int64) bool {
	i := sort.Search(len(t.spans), func(i int) bool { return t.spans[i].post >= pre })
	return i < len(t.spans) && t.spans[i].pre <= pre
}

// Usage computes the usage rows and path rows of paths against one
// generation. Users without paths get no row. Rows are ordered
// by UserID, and path rows by path ID.
//
// An effective path is one not nested in another path of the same user (of
// two equal paths the lower ID is effective). A shared inode with a link
// inside a user's effective paths and its winning link outside them is
// charged once to that user: its size, its alloc and one file.
func Usage(ctx context.Context, genDB *sql.DB, paths []Path) ([]UsageRow, []PathRow, error) {
	ps := slices.Clone(paths)
	slices.SortFunc(ps, func(a, b Path) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})

	byUser := map[int64][]int{} // user -> indexes into ps
	for i, p := range ps {
		byUser[p.UserID] = append(byUser[p.UserID], i)
	}
	userIDs := make([]int64, 0, len(byUser))
	for u := range byUser {
		userIDs = append(userIDs, u)
	}
	slices.Sort(userIDs)

	rows := make([]PathRow, len(ps))
	dirs := make([]gen.Dir, len(ps))
	for i, p := range ps {
		rows[i].UserPathID = p.ID
		d, err := gen.Lookup(ctx, genDB, segments(p.Rel))
		switch {
		case errors.Is(err, gen.ErrNoDir):
			continue
		case err != nil:
			return nil, nil, fmt.Errorf("totals: path %d: %w", p.ID, err)
		}
		dirs[i] = d
		rows[i].Found = true
		rows[i].BytesLogical, rows[i].BytesAlloc, rows[i].Files = d.BytesLogical, d.BytesAlloc, d.Files
	}

	var (
		users   = make([]target, len(userIDs))
		pathTgt = make([]target, len(ps))
		all     []*target
	)
	for ui, u := range userIDs {
		idx := byUser[u]
		for _, i := range idx {
			rows[i].Redundant = redundant(ps, idx, i)
		}
		for _, i := range idx {
			if !rows[i].Found {
				continue
			}
			s := span{dirs[i].Pre, dirs[i].Post}
			pathTgt[i].spans = []span{s}
			all = append(all, &pathTgt[i])
			if !rows[i].Redundant {
				users[ui].spans = append(users[ui].spans, s)
			}
		}
		slices.SortFunc(users[ui].spans, func(a, b span) int {
			switch {
			case a.pre < b.pre:
				return -1
			case a.pre > b.pre:
				return 1
			}
			return 0
		})
		if len(users[ui].spans) > 0 {
			all = append(all, &users[ui])
		}
	}

	if err := chargeShared(ctx, genDB, all); err != nil {
		return nil, nil, err
	}

	usage := make([]UsageRow, len(userIDs))
	for ui, u := range userIDs {
		r := UsageRow{UserID: u}
		for _, i := range byUser[u] {
			if rows[i].Found && !rows[i].Redundant {
				r.BytesLogical += rows[i].BytesLogical
				r.BytesAlloc += rows[i].BytesAlloc
				r.Files += rows[i].Files
			}
		}
		t := &users[ui]
		r.BytesLogical += t.size
		r.BytesAlloc += t.alloc
		r.Files += t.files
		r.SharedHardlinkBytes = t.alloc
		usage[ui] = r
	}
	for i := range rows {
		rows[i].SharedHardlinkBytes = pathTgt[i].alloc
	}
	return usage, rows, nil
}

// segments splits a normalised path into raw name segments.
func segments(rel string) [][]byte {
	var out [][]byte
	for _, s := range pathx.Segments(rel) {
		out = append(out, []byte(s))
	}
	return out
}

// redundant reports whether ps[i] is nested in, or a duplicate of, another
// path of the same user (idx lists that user's paths; ps is sorted by ID, so
// of two equal paths the earlier one stays effective).
func redundant(ps []Path, idx []int, i int) bool {
	for _, j := range idx {
		if j == i {
			continue
		}
		switch pathx.Compare(ps[i].Rel, ps[j].Rel) {
		case pathx.AInsideB:
			return true
		case pathx.Equal:
			if j < i {
				return true
			}
		}
	}
	return false
}

// chargeShared streams every shared link in (dev, ino) order, and after each
// inode group charges every target that has a link inside it but not the
// winning link.
func chargeShared(ctx context.Context, genDB *sql.DB, all []*target) error {
	if len(all) == 0 {
		return nil
	}
	rs, err := genDB.QueryContext(ctx, `
		SELECT l.dev, l.ino, coalesce(d.pre, 0), coalesce(w.pre, 0), s.size, s.alloc
		FROM shared_links l
		JOIN dirs d ON d.id = l.dir_id
		JOIN shared_inodes s ON s.dev = l.dev AND s.ino = l.ino
		LEFT JOIN dirs w ON w.id = s.winner_dir_id
		ORDER BY l.dev, l.ino`)
	if err != nil {
		return fmt.Errorf("totals: shared links: %w", err)
	}
	defer func() { _ = rs.Close() }()

	var (
		cur                 [2]int64
		started             bool
		winPre, size, alloc int64
		touched             []*target
	)
	flush := func() {
		for _, t := range touched {
			if !t.contains(winPre) {
				t.size += size
				t.alloc += alloc
				t.files++
			}
			t.has = false
		}
		touched = touched[:0]
	}
	for rs.Next() {
		var dev, ino, pre, wp, sz, al int64
		if err := rs.Scan(&dev, &ino, &pre, &wp, &sz, &al); err != nil {
			return fmt.Errorf("totals: shared links: %w", err)
		}
		if k := [2]int64{dev, ino}; !started || k != cur {
			if started {
				flush()
			}
			cur, started = k, true
			winPre, size, alloc = wp, sz, al
		}
		for _, t := range all {
			if !t.has && t.contains(pre) {
				t.has = true
				touched = append(touched, t)
			}
		}
	}
	if err := rs.Err(); err != nil {
		return fmt.Errorf("totals: shared links: %w", err)
	}
	if started {
		flush()
	}
	return nil
}

// PathZFS is the ZFS numbers of a path that is exactly a dataset mountpoint.
type PathZFS struct {
	UserPathID                 int64
	Dataset                    string
	Used, Avail, UsedSnapshots int64
	Quota                      *int64
}

// ApplyZFS matches each found path whose host location, path.Join of
// rootHostPath and Rel, equals a dataset mountpoint.
func ApplyZFS(rows []PathRow, paths []Path, rootHostPath string, ds map[string]zfs.Dataset) []PathZFS {
	rel := make(map[int64]string, len(paths))
	for _, p := range paths {
		rel[p.ID] = p.Rel
	}
	var out []PathZFS
	for _, r := range rows {
		rp, ok := rel[r.UserPathID]
		if !ok || !r.Found {
			continue
		}
		d, ok := ds[path.Join(rootHostPath, rp)]
		if !ok {
			continue
		}
		out = append(out, PathZFS{
			UserPathID: r.UserPathID, Dataset: d.Name,
			Used: d.Used, Avail: d.Avail, UsedSnapshots: d.UsedSnapshots, Quota: d.EffectiveQuota(),
		})
	}
	return out
}
