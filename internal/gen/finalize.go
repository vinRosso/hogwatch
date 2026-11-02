package gen

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vinRosso/hogwatch/internal/cube"
)

// The primitives below run between BuildIndexes and Finish. Each flushes the
// buffered rows first and refuses a failed Writer. A write that fails marks
// the Writer failed (the file has no rollback journal outside a transaction
// and the scan is abandoned anyway); the caller must Abort.

// prePostBatch is the number of UPDATEs per transaction in NumberPrePost.
const prePostBatch = 10_000

// ready flushes and checks the Writer is usable.
func (w *Writer) ready(ctx context.Context) error {
	if err := w.usable(); err != nil {
		return err
	}
	return w.Flush(ctx)
}

// inTx runs fn in one transaction; any error rolls back and fails the Writer.
func (w *Writer) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := w.conn.BeginTx(ctx, nil)
	if err != nil {
		return w.fail(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return w.fail(err)
	}
	if err := tx.Commit(); err != nil {
		return w.fail(err)
	}
	return nil
}

type dirRow struct{ id, parent, dirs int64 }

// NumberPrePost numbers every directory in DFS order with children sorted by
// name bytes: the root gets pre = 1 and every directory post = pre + dirs
// (dirs is the descendant count, self excluded). The ids must be dense 1..N
// with parent_id < id and only the root (id 1) has parent 0, which is what
// lets one pass in (parent_id, name) order see every parent before its
// children. Needs the dirs_parent_name index for speed, not for correctness.
func (w *Writer) NumberPrePost(ctx context.Context) error {
	if err := w.ready(ctx); err != nil {
		return err
	}
	rows, err := w.readDirRows(ctx)
	if err != nil {
		return w.fail(fmt.Errorf("number pre/post: %w", err))
	}
	n := int64(len(rows))
	if n == 0 {
		return w.fail(errors.New("number pre/post: no dirs"))
	}
	seen := make([]bool, n+1)
	for _, r := range rows {
		switch {
		case r.id < 1 || r.id > n || seen[r.id]:
			return w.fail(fmt.Errorf("number pre/post: dir ids are not dense 1..%d (id %d)", n, r.id))
		case r.id == 1 && r.parent != 0:
			return w.fail(fmt.Errorf("number pre/post: root has parent %d", r.parent))
		case r.id != 1 && (r.parent < 1 || r.parent >= r.id):
			return w.fail(fmt.Errorf("number pre/post: dir %d has parent %d", r.id, r.parent))
		case r.dirs < 0:
			return w.fail(fmt.Errorf("number pre/post: dir %d has dirs %d", r.id, r.dirs))
		}
		seen[r.id] = true
	}

	// pre[id] is assigned when id's row is reached; next[id] is the pre of
	// id's next child.
	pre := make([]int64, n+1)
	next := make([]int64, n+1)
	for _, r := range rows { // parent_id order: a parent's row precedes its children's
		if r.id == 1 {
			pre[1] = 1
		} else {
			if pre[r.parent] == 0 {
				return w.fail(fmt.Errorf("number pre/post: dir %d precedes its parent %d", r.id, r.parent))
			}
			pre[r.id] = next[r.parent]
			next[r.parent] += r.dirs + 1
		}
		next[r.id] = pre[r.id] + 1
	}

	// Every dir's children must exactly fill its range, or sibling ranges
	// would overlap or leave gaps.
	for _, r := range rows {
		if next[r.id] != pre[r.id]+r.dirs+1 {
			return w.fail(fmt.Errorf("number pre/post: dir %d has dirs %d but its children span %d",
				r.id, r.dirs, next[r.id]-pre[r.id]-1))
		}
	}

	for start := 0; start < len(rows); start += prePostBatch {
		batch := rows[start:min(start+prePostBatch, len(rows))]
		err := w.inTx(ctx, func(tx *sql.Tx) error {
			return execAll(ctx, tx, `UPDATE dirs SET pre = ?, post = ? + dirs WHERE id = ?`, batch,
				func(r dirRow) []any { return []any{pre[r.id], pre[r.id], r.id} })
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// readDirRows loads (id, parent_id, dirs) in (parent_id, name) order and
// closes the cursor before returning.
func (w *Writer) readDirRows(ctx context.Context) ([]dirRow, error) {
	rs, err := w.conn.QueryContext(ctx, `SELECT id, parent_id, dirs FROM dirs ORDER BY parent_id, name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var rows []dirRow
	for rs.Next() {
		var r dirRow
		if err := rs.Scan(&r.id, &r.parent, &r.dirs); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, rs.Err()
}

// LinkGroup is one shared inode with all its names, ordered by (dir_id, name).
type LinkGroup struct {
	Dev, Ino int64
	Links    []SharedLink
}

// LinkGroupsAfter returns up to limit shared inodes with (dev, ino) strictly
// after the given key, ordered by (dev, ino), each with its links. Start with
// (math.MinInt64, math.MinInt64) and continue from the last group returned
// until a page is shorter than limit. Needs the shared_links_inode index.
func (w *Writer) LinkGroupsAfter(ctx context.Context, dev, ino int64, limit int) ([]LinkGroup, error) {
	if err := w.ready(ctx); err != nil {
		return nil, err
	}
	groups, err := w.readGroups(ctx, dev, ino, limit)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		g := &groups[i]
		if g.Links, err = w.readLinks(ctx, g.Dev, g.Ino); err != nil {
			return nil, err
		}
	}
	return groups, nil
}

func (w *Writer) readGroups(ctx context.Context, dev, ino int64, limit int) ([]LinkGroup, error) {
	rs, err := w.conn.QueryContext(ctx, `SELECT dev, ino FROM shared_inodes
		WHERE (dev, ino) > (?, ?) ORDER BY dev, ino LIMIT ?`, dev, ino, limit)
	if err != nil {
		return nil, fmt.Errorf("gen: link groups: %w", err)
	}
	defer func() { _ = rs.Close() }()
	var groups []LinkGroup
	for rs.Next() {
		var g LinkGroup
		if err := rs.Scan(&g.Dev, &g.Ino); err != nil {
			return nil, fmt.Errorf("gen: link groups: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("gen: link groups: %w", err)
	}
	return groups, nil
}

func (w *Writer) readLinks(ctx context.Context, dev, ino int64) ([]SharedLink, error) {
	rs, err := w.conn.QueryContext(ctx, `SELECT dir_id, name, size, alloc, mtime FROM shared_links
		WHERE dev = ? AND ino = ? ORDER BY dir_id, name`, dev, ino)
	if err != nil {
		return nil, fmt.Errorf("gen: links of (%d, %d): %w", dev, ino, err)
	}
	defer func() { _ = rs.Close() }()
	var links []SharedLink
	for rs.Next() {
		l := SharedLink{Dev: dev, Ino: ino}
		if err := rs.Scan(&l.DirID, &l.Name, &l.Size, &l.Alloc, &l.Mtime); err != nil {
			return nil, fmt.Errorf("gen: links of (%d, %d): %w", dev, ino, err)
		}
		links = append(links, l)
	}
	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("gen: links of (%d, %d): %w", dev, ino, err)
	}
	return links, nil
}

// DirInfo returns a directory's parent id and name.
func (w *Writer) DirInfo(ctx context.Context, id int64) (parentID int64, name []byte, err error) {
	if err := w.ready(ctx); err != nil {
		return 0, nil, err
	}
	err = w.conn.QueryRowContext(ctx, `SELECT parent_id, name FROM dirs WHERE id = ?`, id).Scan(&parentID, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, fmt.Errorf("%w: %d", errNoRow, id)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("gen: dir %d: %w", id, err)
	}
	return parentID, name, nil
}

// CellDelta adds Bytes and Files to one cube cell.
type CellDelta struct {
	Cell         cube.Cell
	Bytes, Files int64
}

// ExtDelta adds Bytes and Files to the top_exts row of Ext.
type ExtDelta struct {
	Ext          string
	Bytes, Files int64
}

// DirDelta adjusts one directory's totals, cube and extension rows. The
// numbers are added, so removals are negative.
type DirDelta struct {
	DirID                                               int64
	OwnFiles, OwnAlloc, Files, BytesLogical, BytesAlloc int64
	Cells                                               []CellDelta
	Exts                                                []ExtDelta
}

// ApplyDeltas applies every delta in one transaction (hardlink
// re-attribution). A column, cube cell or top_exts row that would
// go negative is an error, as is an unknown directory; the transaction is
// rolled back and the Writer fails. A top_exts row is only updated where
// (dir_id, ext) exists; a delta for an extension outside the stored top is
// dropped.
func (w *Writer) ApplyDeltas(ctx context.Context, deltas []DirDelta) error {
	if err := w.ready(ctx); err != nil {
		return err
	}
	return w.inTx(ctx, func(tx *sql.Tx) error {
		for i := range deltas {
			if err := applyDelta(ctx, tx, &deltas[i]); err != nil {
				return fmt.Errorf("apply delta for dir %d: %w", deltas[i].DirID, err)
			}
		}
		return nil
	})
}

func applyDelta(ctx context.Context, tx *sql.Tx, d *DirDelta) error {
	var ownFiles, ownAlloc, files, logical, alloc int64
	err := tx.QueryRowContext(ctx, `UPDATE dirs SET own_files = own_files + ?, own_alloc = own_alloc + ?,
		files = files + ?, bytes_logical = bytes_logical + ?, bytes_alloc = bytes_alloc + ?
		WHERE id = ? RETURNING own_files, own_alloc, files, bytes_logical, bytes_alloc`,
		d.OwnFiles, d.OwnAlloc, d.Files, d.BytesLogical, d.BytesAlloc, d.DirID).
		Scan(&ownFiles, &ownAlloc, &files, &logical, &alloc)
	if errors.Is(err, sql.ErrNoRows) {
		return errNoRow
	}
	if err != nil {
		return err
	}
	if ownFiles < 0 || ownAlloc < 0 || files < 0 || logical < 0 || alloc < 0 {
		return fmt.Errorf("negative total (own_files %d, own_alloc %d, files %d, bytes_logical %d, bytes_alloc %d)",
			ownFiles, ownAlloc, files, logical, alloc)
	}
	if err := applyCells(ctx, tx, d); err != nil {
		return err
	}
	for _, e := range d.Exts {
		var bytes, files int64
		err := tx.QueryRowContext(ctx, `UPDATE top_exts SET bytes = bytes + ?, files = files + ?
			WHERE dir_id = ? AND ext = ? RETURNING bytes, files`, e.Bytes, e.Files, d.DirID, e.Ext).Scan(&bytes, &files)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if bytes < 0 || files < 0 {
			return fmt.Errorf("negative extension %q (bytes %d, files %d)", e.Ext, bytes, files)
		}
		if bytes == 0 && files == 0 { // fully subtracted: leave no empty row
			if _, err := tx.ExecContext(ctx, `DELETE FROM top_exts WHERE dir_id = ? AND ext = ?`, d.DirID, e.Ext); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyCells(ctx context.Context, tx *sql.Tx, d *DirDelta) error {
	if len(d.Cells) == 0 {
		return nil
	}
	var blob []byte
	var acc cube.Acc
	err := tx.QueryRowContext(ctx, `SELECT blob FROM cube WHERE dir_id = ?`, d.DirID).Scan(&blob)
	switch {
	case errors.Is(err, sql.ErrNoRows): // no cube row yet: start from empty
	case err != nil:
		return err
	default:
		if acc, err = cube.Decode(blob); err != nil {
			return err
		}
	}
	for _, c := range d.Cells {
		acc.Add(c.Cell, c.Bytes, c.Files)
	}
	out, err := acc.Encode() // rejects a negative cell
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO cube (dir_id, blob) VALUES (?, ?)
		ON CONFLICT (dir_id) DO UPDATE SET blob = excluded.blob`, d.DirID, []byte(out))
	return err
}

// SetSharedCells stores size, allocation, mtime, cube cell and winner_dir_id of each shared
// inode, matched by (dev, ino): the values of its winning link, which replace
// the placeholders the first-arriving link left (see AddSharedInode). An
// unknown inode is an error.
func (w *Writer) SetSharedCells(ctx context.Context, inodes []SharedInode) error {
	if err := w.ready(ctx); err != nil {
		return err
	}
	return w.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `UPDATE shared_inodes SET size = ?, alloc = ?, mtime = ?, cube_cell = ?, winner_dir_id = ? WHERE dev = ? AND ino = ?`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, s := range inodes {
			res, err := stmt.ExecContext(ctx, s.Size, s.Alloc, s.Mtime, s.CubeCell, s.WinnerDirID, s.Dev, s.Ino)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n != 1 {
				return fmt.Errorf("set shared cell (%d, %d): no such inode", s.Dev, s.Ino)
			}
		}
		return nil
	})
}

// PruneUnshared deletes every inode whose links span fewer than two distinct
// directories from shared_inodes and shared_links, and returns how many
// inodes it removed. Needs the shared_links_inode index.
func (w *Writer) PruneUnshared(ctx context.Context) (inodes int64, err error) {
	if err := w.ready(ctx); err != nil {
		return 0, err
	}
	err = w.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM shared_inodes WHERE (dev, ino) NOT IN
			(SELECT dev, ino FROM shared_links GROUP BY dev, ino HAVING count(DISTINCT dir_id) >= 2)`)
		if err != nil {
			return err
		}
		if inodes, err = res.RowsAffected(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM shared_links WHERE NOT EXISTS
			(SELECT 1 FROM shared_inodes s WHERE s.dev = shared_links.dev AND s.ino = shared_links.ino)`)
		return err
	})
	if err != nil {
		return 0, err
	}
	return inodes, nil
}
