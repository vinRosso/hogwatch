package build

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
)

// ErrInvariant is wrapped by every error Verify returns for a violated
// invariant. The message names the invariant by its letter.
var ErrInvariant = errors.New("build: generation invariant violated")

func violated(inv, format string, args ...any) error {
	return fmt.Errorf("%w: (%s) %s", ErrInvariant, inv, fmt.Sprintf(format, args...))
}

// Verify opens the finished generation at path read-only and checks, in order,
// the invariants below. It returns an error naming the first one violated, or
// nil. Empty results are valid: an empty root, no qualifying top-N, no shared
// inodes.
//
//	(a) rollup: per row, files = own_files + Σ child files, likewise
//	    bytes_alloc with own_alloc, dirs = Σ(child dirs + 1), and
//	    bytes_logical and error_count ≥ the children's sums
//	(b) cube: Σ cube files = files and Σ cube bytes = bytes_alloc, per row
//	(c) numbering: root pre = 1 and post = row count, post − pre = dirs, a
//	    child's range inside its parent's (pre after it, post at most
//	    equal: post is the last pre of the subtree), pre unique
//	(d) shared: every shared inode spans ≥ 2 directories and has links,
//	    every link has an inode
//	(e) top lists: ranks contiguous from 1 per (anchor, kind) and ≤ top_n
//	    (when meta.stats records it), equal lengths for largest and oldest,
//	    extension rows bounded by their directory
//	(f) no negative column
//	(g) hardlink winner: every shared inode's winner_dir_id is the dir_id of
//	    one of its own links
//
// top_exts is not required to list every extension of a directory: hardlink
// subtraction can change which extensions would qualify, so only the rows
// present are bounded.
func Verify(ctx context.Context, path string) error {
	d, meta, err := gen.OpenReadOnly(ctx, path)
	if err != nil {
		return fmt.Errorf("build: verify: %w", err)
	}
	defer func() { _ = d.Close() }()
	var ms struct {
		TopN *int64 `json:"top_n"`
	}
	_ = json.Unmarshal(meta.Stats, &ms) // absent or foreign stats: no rank bound
	checks := []func(context.Context, *sql.DB) error{
		checkRollup, checkCube, checkNumbering, checkShared, checkWinner,
		func(ctx context.Context, d *sql.DB) error { return checkTop(ctx, d, ms.TopN) },
		checkNonNegative,
	}
	for _, c := range checks {
		if err := c(ctx, d); err != nil {
			return err
		}
	}
	return nil
}

// firstRow runs a query selecting the offending key columns and reports
// whether a row came back.
func firstRow(ctx context.Context, d *sql.DB, q string, dest ...any) (bool, error) {
	err := d.QueryRowContext(ctx, q).Scan(dest...)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("build: verify: %w", err)
	}
	return true, nil
}

func checkRollup(ctx context.Context, d *sql.DB) error {
	const q = `SELECT d.id, d.own_files, d.files, d.own_alloc, d.bytes_alloc, d.dirs, d.bytes_logical, d.error_count,
	    coalesce(c.f, 0), coalesce(c.a, 0), coalesce(c.n, 0), coalesce(c.l, 0), coalesce(c.e, 0)
	  FROM dirs d LEFT JOIN (
	    SELECT parent_id, sum(files) f, sum(bytes_alloc) a, sum(dirs + 1) n, sum(bytes_logical) l, sum(error_count) e
	    FROM dirs WHERE parent_id > 0 GROUP BY parent_id) c ON c.parent_id = d.id
	  WHERE d.files != d.own_files + coalesce(c.f, 0)
	     OR d.bytes_alloc != d.own_alloc + coalesce(c.a, 0)
	     OR d.dirs != coalesce(c.n, 0)
	     OR d.bytes_logical < coalesce(c.l, 0)
	     OR d.error_count < coalesce(c.e, 0)
	  ORDER BY d.id LIMIT 1`
	var id, ownFiles, files, ownAlloc, alloc, dirs, logical, errs, cf, ca, cn, cl, ce int64
	found, err := firstRow(ctx, d, q, &id, &ownFiles, &files, &ownAlloc, &alloc, &dirs, &logical, &errs, &cf, &ca, &cn, &cl, &ce)
	if err != nil || !found {
		return err
	}
	switch {
	case files != ownFiles+cf:
		return violated("a", "dir %d: files %d != own_files %d + children %d", id, files, ownFiles, cf)
	case alloc != ownAlloc+ca:
		return violated("a", "dir %d: bytes_alloc %d != own_alloc %d + children %d", id, alloc, ownAlloc, ca)
	case dirs != cn:
		return violated("a", "dir %d: dirs %d != Σ(child dirs + 1) %d", id, dirs, cn)
	case logical < cl:
		return violated("a", "dir %d: bytes_logical %d < children %d", id, logical, cl)
	}
	return violated("a", "dir %d: error_count %d < children %d", id, errs, ce)
}

func checkCube(ctx context.Context, d *sql.DB) error {
	var orphan int64
	found, err := firstRow(ctx, d, `SELECT dir_id FROM cube WHERE dir_id NOT IN (SELECT id FROM dirs) LIMIT 1`, &orphan)
	if err != nil {
		return err
	}
	if found {
		return violated("b", "cube row for unknown dir %d", orphan)
	}
	rs, err := d.QueryContext(ctx, `SELECT d.id, d.files, d.bytes_alloc, c.blob FROM dirs d LEFT JOIN cube c ON c.dir_id = d.id ORDER BY d.id`)
	if err != nil {
		return fmt.Errorf("build: verify: %w", err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		var id, files, alloc int64
		var blob []byte
		if err := rs.Scan(&id, &files, &alloc, &blob); err != nil {
			return fmt.Errorf("build: verify: %w", err)
		}
		if blob == nil {
			return violated("b", "dir %d has no cube row", id)
		}
		acc, err := cube.Decode(blob)
		if err != nil {
			return violated("b", "dir %d: %v", id, err)
		}
		var sf, sb int64
		for _, c := range acc {
			sf += c.Files
			sb += c.Bytes
		}
		if sf != files || sb != alloc {
			return violated("b", "dir %d: cube holds %d files / %d bytes, row says %d / %d", id, sf, sb, files, alloc)
		}
	}
	if err := rs.Err(); err != nil {
		return fmt.Errorf("build: verify: %w", err)
	}
	return nil
}

func checkNumbering(ctx context.Context, d *sql.DB) error {
	var n, roots, nulls int64
	if err := d.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE parent_id = 0),
		count(*) FILTER (WHERE pre IS NULL OR post IS NULL) FROM dirs`).Scan(&n, &roots, &nulls); err != nil {
		return fmt.Errorf("build: verify: %w", err)
	}
	switch {
	case n == 0:
		return violated("c", "no dirs rows")
	case roots != 1:
		return violated("c", "%d root rows, want 1", roots)
	case nulls != 0:
		return violated("c", "%d rows without pre/post", nulls)
	}
	var id, pre, post int64
	if err := d.QueryRowContext(ctx, `SELECT id, pre, post FROM dirs WHERE parent_id = 0`).Scan(&id, &pre, &post); err != nil {
		return fmt.Errorf("build: verify: %w", err)
	}
	if pre != 1 || post != n {
		return violated("c", "root %d has pre %d, post %d; want 1 and %d", id, pre, post, n)
	}
	checks := []struct{ q, msg string }{
		{fmt.Sprintf(`SELECT id FROM dirs WHERE post - pre != dirs OR pre < 1 OR post > %d ORDER BY id LIMIT 1`, n),
			"dir %d: post - pre != dirs, or pre/post out of range"},
		{`SELECT c.id FROM dirs c JOIN dirs p ON p.id = c.parent_id
			WHERE NOT (c.pre > p.pre AND c.post <= p.post) ORDER BY c.id LIMIT 1`,
			"dir %d: range is not inside its parent's"},
		{`SELECT pre FROM dirs GROUP BY pre HAVING count(*) > 1 LIMIT 1`,
			"pre %d is used by more than one dir"},
	}
	for _, c := range checks {
		found, err := firstRow(ctx, d, c.q, &id)
		if err != nil {
			return err
		}
		if found {
			return violated("c", c.msg, id)
		}
	}
	return nil
}

func checkShared(ctx context.Context, d *sql.DB) error {
	var dev, ino int64
	checks := []struct{ q, msg string }{
		{`SELECT dev, ino FROM shared_links GROUP BY dev, ino HAVING count(DISTINCT dir_id) < 2 LIMIT 1`,
			"inode (%d, %d) is linked from fewer than 2 dirs"},
		{`SELECT dev, ino FROM shared_links WHERE (dev, ino) NOT IN (SELECT dev, ino FROM shared_inodes) LIMIT 1`,
			"links of inode (%d, %d) have no shared_inodes row"},
		{`SELECT dev, ino FROM shared_inodes WHERE (dev, ino) NOT IN (SELECT dev, ino FROM shared_links) LIMIT 1`,
			"shared inode (%d, %d) has no links"},
		{`SELECT dev, ino FROM shared_inodes WHERE cube_cell < 0 OR cube_cell >= 384 LIMIT 1`,
			"shared inode (%d, %d) has a cube_cell out of range"},
		{`SELECT dev, ino FROM shared_links WHERE dir_id NOT IN (SELECT id FROM dirs) LIMIT 1`,
			"a link of inode (%d, %d) is in an unknown dir"},
	}
	for _, c := range checks {
		found, err := firstRow(ctx, d, c.q, &dev, &ino)
		if err != nil {
			return err
		}
		if found {
			return violated("d", c.msg, dev, ino)
		}
	}
	return nil
}

func checkTop(ctx context.Context, d *sql.DB, topN *int64) error {
	var anchor, a, b int64
	var kind string
	found, err := firstRow(ctx, d, `SELECT anchor_id, kind FROM top_files GROUP BY anchor_id, kind
		HAVING min(rank) != 1 OR max(rank) != count(*) LIMIT 1`, &anchor, &kind)
	if err != nil {
		return err
	}
	if found {
		return violated("e", "anchor %d %s: ranks are not contiguous from 1", anchor, kind)
	}
	if topN != nil {
		var rank int64
		q := fmt.Sprintf(`SELECT anchor_id, rank FROM top_files WHERE rank > %d LIMIT 1`, *topN)
		if found, err = firstRow(ctx, d, q, &anchor, &rank); err != nil {
			return err
		}
		if found {
			return violated("e", "anchor %d: rank %d exceeds top_n %d", anchor, rank, *topN)
		}
	}
	if found, err = firstRow(ctx, d, `SELECT anchor_id, sum(kind = 'largest'), sum(kind = 'oldest') FROM top_files
		GROUP BY anchor_id HAVING sum(kind = 'largest') != sum(kind = 'oldest') LIMIT 1`, &anchor, &a, &b); err != nil {
		return err
	}
	if found {
		return violated("e", "anchor %d: %d largest but %d oldest rows", anchor, a, b)
	}
	if found, err = firstRow(ctx, d, `SELECT anchor_id FROM top_files WHERE anchor_id NOT IN (SELECT id FROM dirs)
		OR parent_dir_id NOT IN (SELECT id FROM dirs) LIMIT 1`, &anchor); err != nil {
		return err
	}
	if found {
		return violated("e", "top file of anchor %d refers to an unknown dir", anchor)
	}
	if found, err = firstRow(ctx, d, `SELECT e.dir_id FROM top_exts e LEFT JOIN dirs d ON d.id = e.dir_id
		WHERE d.id IS NULL OR e.bytes > d.bytes_alloc OR e.files > d.files LIMIT 1`, &anchor); err != nil {
		return err
	}
	if found {
		return violated("e", "top_exts row of dir %d is missing its dir or exceeds its totals", anchor)
	}
	return nil
}

// checkNonNegative skips dev and ino (bit-cast stat values) and mtime (may
// predate 1970).
func checkNonNegative(ctx context.Context, d *sql.DB) error {
	cols := []struct {
		table string
		cols  []string
	}{
		{"dirs", []string{"depth_anchor", "uid", "gid", "own_files", "own_alloc", "files", "dirs", "bytes_logical", "bytes_alloc", "flags", "error_count"}},
		{"top_files", []string{"rank", "size", "alloc", "uid"}},
		{"top_exts", []string{"bytes", "files"}},
		{"shared_inodes", []string{"size", "alloc"}},
		{"shared_links", []string{"size", "alloc"}},
		{"errors", []string{"errno"}},
	}
	for _, t := range cols {
		for _, c := range t.cols {
			var one int64
			found, err := firstRow(ctx, d, fmt.Sprintf(`SELECT 1 FROM %s WHERE %s < 0 LIMIT 1`, t.table, c), &one)
			if err != nil {
				return err
			}
			if found {
				return violated("f", "negative %s.%s", t.table, c)
			}
		}
	}
	return nil
}

func checkWinner(ctx context.Context, d *sql.DB) error {
	var dev, ino, winner int64
	found, err := firstRow(ctx, d, `SELECT s.dev, s.ino, s.winner_dir_id FROM shared_inodes s
		WHERE NOT EXISTS (SELECT 1 FROM shared_links l
			WHERE l.dev = s.dev AND l.ino = s.ino AND l.dir_id = s.winner_dir_id)
		ORDER BY s.dev, s.ino LIMIT 1`, &dev, &ino, &winner)
	if err != nil || !found {
		return err
	}
	return violated("g", "shared inode (%d, %d): winner_dir_id %d is not the dir of one of its links", dev, ino, winner)
}
