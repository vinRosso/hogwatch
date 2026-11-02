package repo

import (
	"bytes"
	"context"
	"fmt"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/noderef"
	"github.com/vinRosso/hogwatch/internal/totals"
)

// ErrBadKind is returned by Top for a kind other than largest or oldest.
var ErrBadKind = fmt.Errorf("repo: top kind must be largest or oldest")

// openVisible opens the generation of ref and checks that ref is inside s. The
// caller releases the entry; on error nothing is held.
func (r *Repo) openVisible(ctx context.Context, s Scope, ref noderef.Ref) (*genEntry, error) {
	if !s.allows(ref.ScanID) {
		return nil, ErrNotFound
	}
	e, err := r.open(ctx, ref.ScanID)
	if err != nil {
		return nil, err
	}
	ok, err := visible(ctx, e, s, ref.DirID)
	if err != nil {
		r.release(e)
		return nil, r.fix(ref.ScanID, err)
	}
	if !ok {
		r.release(e)
		return nil, ErrNotFound
	}
	return e, nil
}

// TopFile is one stored top-N entry of a node. DirRef is its parent directory.
type TopFile struct {
	Rank        int
	Name        []byte
	DirRef      noderef.Ref
	Size, Alloc int64
	Mtime       int64
}

// Top returns the stored largest or oldest files below ref in rank order.
// stored is false when the node has no such rows (a listing is needed).
func (r *Repo) Top(ctx context.Context, s Scope, ref noderef.Ref, kind string) (items []TopFile, stored bool, err error) {
	if kind != "largest" && kind != "oldest" {
		return nil, false, ErrBadKind
	}
	e, err := r.openVisible(ctx, s, ref)
	if err != nil {
		return nil, false, err
	}
	defer r.release(e)
	pred, pargs := s.where("d.pre")
	rs, err := e.db.QueryContext(ctx,
		scoped(`SELECT t.rank, t.name, t.parent_dir_id, t.size, t.alloc, t.mtime
		 FROM top_files AS t JOIN dirs AS d ON d.id = t.parent_dir_id
		 WHERE t.anchor_id = ? AND t.kind = ? AND `, pred, ` ORDER BY t.rank`),
		append([]any{ref.DirID, kind}, pargs...)...)
	if err != nil {
		return nil, false, r.fix(ref.ScanID, err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		var t TopFile
		var dir int64
		if err := rs.Scan(&t.Rank, &t.Name, &dir, &t.Size, &t.Alloc, &t.Mtime); err != nil {
			return nil, false, r.fix(ref.ScanID, err)
		}
		t.DirRef = noderef.Ref{ScanID: ref.ScanID, DirID: dir}
		items = append(items, t)
	}
	if err := rs.Err(); err != nil {
		return nil, false, r.fix(ref.ScanID, err)
	}
	return items, len(items) > 0, nil
}

// ExtStat is the usage of one file extension at a node.
type ExtStat struct {
	Ext, Category string
	Files, Bytes  int64
}

// Extensions returns the stored per-extension totals of ref, largest first. A
// node without rows gives an empty list.
func (r *Repo) Extensions(ctx context.Context, s Scope, ref noderef.Ref) ([]ExtStat, error) {
	e, err := r.openVisible(ctx, s, ref)
	if err != nil {
		return nil, err
	}
	defer r.release(e)
	cats, err := r.q.ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo: categories: %w", err)
	}
	category := make(map[string]string, len(cats))
	for _, c := range cats {
		category[c.Ext] = c.Category
	}
	pred, pargs := s.where("d.pre")
	rs, err := e.db.QueryContext(ctx,
		scoped(`SELECT x.ext, x.bytes, x.files FROM top_exts AS x JOIN dirs AS d ON d.id = x.dir_id
		 WHERE x.dir_id = ? AND `, pred, ` ORDER BY x.bytes DESC, x.ext`),
		append([]any{ref.DirID}, pargs...)...)
	if err != nil {
		return nil, r.fix(ref.ScanID, err)
	}
	defer func() { _ = rs.Close() }()
	var out []ExtStat
	for rs.Next() {
		var x ExtStat
		if err := rs.Scan(&x.Ext, &x.Bytes, &x.Files); err != nil {
			return nil, r.fix(ref.ScanID, err)
		}
		if x.Category = category[x.Ext]; x.Category == "" {
			x.Category = "other"
		}
		out = append(out, x)
	}
	return out, r.fix(ref.ScanID, rs.Err())
}

// ErrorItem is one scan error below a node. RelName is the path of the failing
// entry relative to the node (the directory's own path for a directory-level
// error).
type ErrorItem struct {
	RelName []byte
	Errno   int64
}

// Errors returns up to limit scan errors in the subtree of ref with rowid
// greater than afterRowID, in rowid order. next is the rowid to continue from,
// or 0 on the last page.
func (r *Repo) Errors(ctx context.Context, s Scope, ref noderef.Ref, afterRowID int64, limit int) (items []ErrorItem, next int64, err error) {
	if limit <= 0 {
		limit = 500
	}
	e, err := r.openVisible(ctx, s, ref)
	if err != nil {
		return nil, 0, err
	}
	defer r.release(e)
	node, err := gen.DirByID(ctx, e.db, ref.DirID)
	if err != nil {
		return nil, 0, r.fix(ref.ScanID, err)
	}
	nodeSegs, err := gen.DirPath(ctx, e.db, ref.DirID)
	if err != nil {
		return nil, 0, r.fix(ref.ScanID, err)
	}
	pred, pargs := s.where("d.pre")
	rs, err := e.db.QueryContext(ctx,
		scoped(`SELECT x.rowid, x.dir_id, x.name, x.errno FROM errors AS x JOIN dirs AS d ON d.id = x.dir_id
		 WHERE d.pre BETWEEN ? AND ? AND `, pred, ` AND x.rowid > ? ORDER BY x.rowid LIMIT ?`),
		append(append([]any{node.Pre, node.Post}, pargs...), afterRowID, limit+1)...)
	if err != nil {
		return nil, 0, r.fix(ref.ScanID, err)
	}
	type row struct {
		rowid, dir, errno int64
		name              []byte
	}
	var rows []row
	for rs.Next() {
		var x row
		if err := rs.Scan(&x.rowid, &x.dir, &x.name, &x.errno); err != nil {
			_ = rs.Close()
			return nil, 0, r.fix(ref.ScanID, err)
		}
		rows = append(rows, x)
	}
	if err := rs.Err(); err != nil {
		_ = rs.Close()
		return nil, 0, r.fix(ref.ScanID, err)
	}
	_ = rs.Close()
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].rowid
	}
	paths := map[int64][][]byte{}
	for _, x := range rows {
		segs, ok := paths[x.dir]
		if !ok {
			if segs, err = gen.DirPath(ctx, e.db, x.dir); err != nil {
				return nil, 0, r.fix(ref.ScanID, err)
			}
			paths[x.dir] = segs
		}
		rel := append([][]byte(nil), segs[len(nodeSegs):]...)
		if len(x.name) > 0 {
			rel = append(rel, x.name)
		}
		items = append(items, ErrorItem{RelName: bytes.Join(rel, []byte("/")), Errno: x.errno})
	}
	return items, next, nil
}

// HistoryPoint is one stored size sample of a directory.
type HistoryPoint struct{ TS, BytesAlloc, Files int64 }

// History returns the samples of the node's path since the given Unix time,
// oldest first. History is keyed by path, so it spans generations; a node
// without top-N has none.
func (r *Repo) History(ctx context.Context, s Scope, ref noderef.Ref, since int64) ([]HistoryPoint, error) {
	e, err := r.openVisible(ctx, s, ref)
	if err != nil {
		return nil, err
	}
	defer r.release(e)
	segs, err := gen.DirPath(ctx, e.db, ref.DirID)
	if err != nil {
		return nil, r.fix(ref.ScanID, err)
	}
	rows, err := r.q.ListHistoryDirs(ctx, dbq.ListHistoryDirsParams{
		RootID: e.rootID, PathHash: totals.PathHash(e.rootID, bytes.Join(segs, []byte("/"))), Since: since,
	})
	if err != nil {
		return nil, fmt.Errorf("repo: history: %w", err)
	}
	out := make([]HistoryPoint, 0, len(rows))
	for _, h := range rows {
		out = append(out, HistoryPoint{TS: h.Ts, BytesAlloc: h.BytesAlloc, Files: h.Files})
	}
	return out, nil
}

// scoped joins the fixed parts of a query around a Scope.where predicate, which
// is fixed text with ? placeholders.
func scoped(head, pred, tail string) string { return head + pred + tail }
