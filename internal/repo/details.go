package repo

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

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

// TopScope says which files Top ranks.
type TopScope int

const (
	// TopSubtree ranks every file below the node.
	TopSubtree TopScope = iota
	// TopDir ranks only the files directly in the node.
	TopDir
	// TopBoth is TopSubtree and TopDir in one list: the files below the node plus
	// the ones directly in it that the subtree answer lacks.
	TopBoth
)

// Top returns the stored largest or oldest files of ref in rank order: those
// below it, or with TopDir only those directly in it. stored is false when no
// list holds a file of ref (a listing is needed).
//
// For TopSubtree, the node's own list is used when it has one. A node outside the
// storage rule (too deep or too small) has none. For both scopes, a caller who
// may list files is then answered from their newest finished listing of exactly
// this node that holds every file in question (not cut at the row cap, not
// expired, no filters; subtree mode for TopSubtree, any mode for TopDir). Failing
// that, the answer comes from the nearest directory with a list (for TopDir, ref
// itself counts), cut to the files in question. Every such file that ranks in
// that list is in the answer, so it is a prefix of the exact list, not
// necessarily all of it. A directory with no files of its own is answered as an
// empty, stored list for TopDir.
func (r *Repo) Top(ctx context.Context, c Caller, s Scope, ref noderef.Ref, kind string, scope TopScope, now time.Time) (items []TopFile, stored bool, err error) {
	if kind != "largest" && kind != "oldest" {
		return nil, false, ErrBadKind
	}
	if scope == TopBoth {
		return r.topBoth(ctx, c, s, ref, kind, now)
	}
	e, err := r.openVisible(ctx, s, ref)
	if err != nil {
		return nil, false, err
	}
	defer r.release(e)
	direct := scope == TopDir
	if !direct {
		items, err = topRows(ctx, e, s, ref, ref.DirID, kind, topFilter{})
		if err != nil {
			return nil, false, r.fix(ref.ScanID, err)
		}
		if len(items) > 0 {
			return items, true, nil
		}
	}
	if items, err = r.topFromListing(ctx, e, c, ref, kind, direct, now); err != nil {
		return nil, false, r.fix(ref.ScanID, err)
	}
	if len(items) > 0 {
		return items, true, nil
	}
	anchor, pre, post, found, err := nearestList(ctx, e, ref.DirID, kind, direct)
	if err != nil {
		return nil, false, r.fix(ref.ScanID, err)
	}
	if found {
		f := topFilter{within: &[2]int64{pre, post}}
		if direct {
			f = topFilter{parent: ref.DirID}
		}
		if items, err = topRows(ctx, e, s, ref, anchor, kind, f); err != nil {
			return nil, false, r.fix(ref.ScanID, err)
		}
		if len(items) > 0 {
			return items, true, nil
		}
	}
	if direct {
		var own int64
		if err := e.db.QueryRowContext(ctx, `SELECT own_files FROM dirs WHERE id = ?`, ref.DirID).Scan(&own); err != nil {
			return nil, false, r.fix(ref.ScanID, err)
		}
		if own == 0 {
			return nil, true, nil
		}
	}
	return nil, false, nil
}

// topBoth merges the subtree and the direct answers into one ranking. Each answer
// is a prefix of its exact list, so a file directly in the node that is too small
// for the subtree list still shows, and one in both shows once. Ranks are
// renumbered over the merged list.
func (r *Repo) topBoth(ctx context.Context, c Caller, s Scope, ref noderef.Ref, kind string, now time.Time) ([]TopFile, bool, error) {
	below, stored, err := r.Top(ctx, c, s, ref, kind, TopSubtree, now)
	if err != nil {
		return nil, false, err
	}
	here, _, err := r.Top(ctx, c, s, ref, kind, TopDir, now)
	if err != nil {
		return nil, false, err
	}
	type key struct {
		dir  int64
		name string
	}
	seen := make(map[key]bool, len(below)+len(here))
	items := make([]TopFile, 0, len(below)+len(here))
	for _, list := range [][]TopFile{below, here} {
		for _, f := range list {
			k := key{f.DirRef.DirID, string(f.Name)}
			if !seen[k] {
				seen[k] = true
				items = append(items, f)
			}
		}
	}
	slices.SortFunc(items, func(a, b TopFile) int {
		if kind == "largest" {
			return cmp.Or(cmp.Compare(b.Size, a.Size), bytes.Compare(a.Name, b.Name), cmp.Compare(a.DirRef.DirID, b.DirRef.DirID))
		}
		return cmp.Or(cmp.Compare(a.Mtime, b.Mtime), cmp.Compare(b.Size, a.Size), bytes.Compare(a.Name, b.Name), cmp.Compare(a.DirRef.DirID, b.DirRef.DirID))
	})
	for i := range items {
		items[i].Rank = i + 1
	}
	return items, stored || len(items) > 0, nil
}

// nearestList finds the closest directory above dir (dir itself too with self) that
// carries a list of this kind, and the pre-order range of dir.
func nearestList(ctx context.Context, e *genEntry, dir int64, kind string, self bool) (anchor, pre, post int64, found bool, err error) {
	start := 0
	if self {
		start = 1
	}
	err = e.db.QueryRowContext(ctx, `WITH RECURSIVE up(id, n) AS (
		   SELECT CASE WHEN ?3 = 1 THEN id ELSE parent_id END, CASE WHEN ?3 = 1 THEN 0 ELSE 1 END FROM dirs WHERE id = ?1
		   UNION ALL
		   SELECT d.parent_id, up.n + 1 FROM dirs AS d JOIN up ON d.id = up.id WHERE up.n < 4096
		 )
		 SELECT up.id, own.pre, own.post FROM up, dirs AS own
		 WHERE own.id = ?1 AND up.id > 0
		   AND EXISTS (SELECT 1 FROM top_files AS t WHERE t.anchor_id = up.id AND t.kind = ?2)
		 ORDER BY up.n LIMIT 1`, dir, kind, start).Scan(&anchor, &pre, &post)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, false, nil
	}
	return anchor, pre, post, err == nil, err
}

// topFilter narrows the rows of a list: within keeps the files whose directory
// lies in the pre-order range [within[0], within[1]], parent only those directly
// in that directory.
type topFilter struct {
	within *[2]int64
	parent int64
}

// topRows reads one anchor's list in rank order, keeping the files whose
// directory s can see and that pass f.
func topRows(ctx context.Context, e *genEntry, s Scope, ref noderef.Ref, anchor int64, kind string, f topFilter) ([]TopFile, error) {
	pred, pargs := s.where("d.pre")
	args := []any{anchor, kind}
	tail := ``
	if f.parent != 0 {
		tail += ` AND t.parent_dir_id = ?`
		pargs = append(pargs, f.parent)
	}
	if f.within != nil {
		tail += ` AND d.pre BETWEEN ? AND ?`
		pargs = append(pargs, f.within[0], f.within[1])
	}
	tail += ` ORDER BY t.rank`
	rs, err := e.db.QueryContext(ctx,
		scoped(`SELECT t.rank, t.name, t.parent_dir_id, t.size, t.alloc, t.mtime
		 FROM top_files AS t JOIN dirs AS d ON d.id = t.parent_dir_id
		 WHERE t.anchor_id = ? AND t.kind = ? AND `, pred, tail),
		append(args, pargs...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var items []TopFile
	for rs.Next() {
		var t TopFile
		var dir int64
		if err := rs.Scan(&t.Rank, &t.Name, &dir, &t.Size, &t.Alloc, &t.Mtime); err != nil {
			return nil, err
		}
		t.DirRef = noderef.Ref{ScanID: ref.ScanID, DirID: dir}
		items = append(items, t)
	}
	return items, rs.Err()
}

// listingTopN is how many files a listing answers a Top request with, the default N of the scan lists.
const listingTopN = 50

// topFromListing ranks the files of the caller's newest complete listing of ref:
// every file below it, or with direct only those directly in it (a listing of
// either mode has them). It gives nothing for a caller who cannot list files, or
// when no such listing exists.
func (r *Repo) topFromListing(ctx context.Context, e *genEntry, c Caller, ref noderef.Ref, kind string, direct bool, now time.Time) ([]TopFile, error) {
	if !c.CanList {
		return nil, nil
	}
	flag := int64(0)
	if direct {
		flag = 1
	}
	id, err := r.q.LatestUnfilteredListing(ctx, dbq.LatestUnfilteredListingParams{
		TargetNode:  sql.NullString{String: ref.String(), Valid: true},
		RequestedBy: sql.NullInt64{Int64: c.UserID, Valid: true},
		AnyMode:     flag,
		Now:         sql.NullInt64{Int64: now.Unix(), Valid: true},
	})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("repo: top listing: %w", err)
	}
	type file struct {
		rel                []byte
		size, alloc, mtime int64
	}
	var files []file
	if kind == "largest" {
		rows, err := r.q.TopListingLargest(ctx, dbq.TopListingLargestParams{JobID: id, Direct: flag, RowLimit: listingTopN})
		if err != nil {
			return nil, fmt.Errorf("repo: top listing rows: %w", err)
		}
		for _, x := range rows {
			files = append(files, file{x.RelName, x.Size, x.Alloc, x.Mtime})
		}
	} else {
		rows, err := r.q.TopListingOldest(ctx, dbq.TopListingOldestParams{JobID: id, Direct: flag, RowLimit: listingTopN})
		if err != nil {
			return nil, fmt.Errorf("repo: top listing rows: %w", err)
		}
		for _, x := range rows {
			files = append(files, file{x.RelName, x.Size, x.Alloc, x.Mtime})
		}
	}
	items := make([]TopFile, 0, len(files))
	for i, f := range files {
		dir, name, err := dirBelow(ctx, e, ref.DirID, f.rel)
		if err != nil {
			return nil, err
		}
		items = append(items, TopFile{
			Rank: i + 1, Name: name, DirRef: noderef.Ref{ScanID: ref.ScanID, DirID: dir},
			Size: f.size, Alloc: f.alloc, Mtime: f.mtime,
		})
	}
	return items, nil
}

// dirBelow follows the directory names of rel (joined by "/") down from dir and
// returns the directory that holds the file and the file's own name. A directory
// made after the scan is not in it, so the deepest one the scan knows stands in.
// No Scope check is needed here: the caller has already checked that dir is
// visible, and a scope is a set of whole subtrees, so everything below it is too.
func dirBelow(ctx context.Context, e *genEntry, dir int64, rel []byte) (int64, []byte, error) {
	parts := bytes.Split(rel, []byte("/"))
	name := parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		var id int64
		err := e.db.QueryRowContext(ctx, `SELECT id FROM dirs WHERE parent_id = ? AND name = ?`, dir, p).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return 0, nil, err
		}
		dir = id
	}
	return dir, name, nil
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
