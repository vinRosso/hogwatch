package repo

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/noderef"
)

// ChildSort orders the children of a directory. Ties break on name, ascending.
type ChildSort uint8

// Child sort orders: size, files and mtime are descending, name ascending.
const (
	SortSize ChildSort = iota
	SortName
	SortMtime
	SortFiles
)

// DefaultChildLimit and MaxChildLimit bound a page of children.
const (
	DefaultChildLimit = 100
	MaxChildLimit     = 500
)

// ChildCursor is the position after the last directory of a page: its sort key
// (unused for SortName) and name.
type ChildCursor struct {
	Key  int64
	Name []byte
}

// ChildQuery selects one page of children. A zero Filter means no filter. After
// is nil on the first page.
type ChildQuery struct {
	Sort   ChildSort
	Filter cube.Filter
	After  *ChildCursor
	Limit  int
}

// ChildKind says whether a Child is a subdirectory or the directory's own files.
type ChildKind uint8

// Child kinds.
const (
	ChildDir ChildKind = iota
	ChildFiles
)

// Child is one entry of a directory listing. The own-files entry has no Ref
// and an empty Name. BytesLogical is nil under a filter: the cube holds
// allocated bytes only.
type Child struct {
	Kind         ChildKind
	Ref          *noderef.Ref
	Name         []byte
	Files        int64
	BytesAlloc   int64
	BytesLogical *int64
	Mtime        int64
}

type childRow struct {
	id                                  int64
	name                                []byte
	mtime, files, bytesLogical, bytesAl int64
}

// rowsQuery parameterises childRows.
type rowsQuery struct {
	sort     ChildSort
	filter   cube.Filter
	after    *ChildCursor
	limit    int  // 0 = no limit
	positive bool // only children with bytes_alloc > 0
}

func filtered(f cube.Filter) bool { return f != (cube.Filter{}) }

// childSelect is the subquery that yields (id, name, mtime, files,
// bytes_logical, bytes_alloc) for the children of parent inside s; under a
// filter files and bytes_alloc are the filtered values and children without a
// matching file are left out.
func childSelect(s Scope, parent int64, f cube.Filter) (string, []any) {
	ff, fb := "d.files", "d.bytes_alloc"
	var args []any
	if filtered(f) {
		ff, fb = "hw_cube_files(c.blob,?,?,?)", "hw_cube_bytes(c.blob,?,?,?)"
		args = append(filterArgs(f), filterArgs(f)...)
	}
	pred, pargs := s.where("d.pre")
	q := `SELECT d.id AS id, d.name AS name, d.mtime AS mtime, ` + ff + ` AS files, d.bytes_logical AS bytes_logical, ` +
		fb + ` AS bytes_alloc FROM dirs AS d LEFT JOIN cube AS c ON c.dir_id = d.id WHERE d.parent_id = ? AND ` + pred
	args = append(args, parent)
	args = append(args, pargs...)
	return q, args
}

func (r *Repo) childRows(ctx context.Context, e *genEntry, s Scope, parent int64, q rowsQuery) ([]childRow, error) {
	sub, args := childSelect(s, parent, q.filter)
	var sb strings.Builder
	sb.WriteString(`SELECT id, name, mtime, files, bytes_logical, bytes_alloc FROM (` + sub + `) WHERE 1`)
	if filtered(q.filter) {
		sb.WriteString(` AND files > 0`)
	}
	if q.positive {
		sb.WriteString(` AND bytes_alloc > 0`)
	}
	key := map[ChildSort]string{SortSize: "bytes_alloc", SortMtime: "mtime", SortFiles: "files"}[q.sort]
	switch {
	case q.after != nil && key == "":
		sb.WriteString(` AND name > ?`)
		args = append(args, q.after.Name)
	case q.after != nil:
		sb.WriteString(` AND (` + key + ` < ? OR (` + key + ` = ? AND name > ?))`)
		args = append(args, q.after.Key, q.after.Key, q.after.Name)
	}
	if key == "" {
		sb.WriteString(` ORDER BY name`)
	} else {
		sb.WriteString(` ORDER BY ` + key + ` DESC, name`)
	}
	if q.limit > 0 {
		sb.WriteString(` LIMIT ?`)
		args = append(args, q.limit)
	}
	rs, err := e.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []childRow
	for rs.Next() {
		var c childRow
		if err := rs.Scan(&c.id, &c.name, &c.mtime, &c.files, &c.bytesLogical, &c.bytesAl); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rs.Err()
}

// sumChildren totals the bytes of the children with bytes_alloc > 0 and
// the logical bytes of all children (the latter only without a filter).
func (r *Repo) sumChildren(ctx context.Context, e *genEntry, s Scope, parent int64, f cube.Filter) (bytesAlloc, bytesLogical, files int64, err error) {
	sub, args := childSelect(s, parent, f)
	err = e.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(bytes_alloc), 0), COALESCE(SUM(bytes_logical), 0), COALESCE(SUM(files), 0) FROM (`+sub+`)`, args...).
		Scan(&bytesAlloc, &bytesLogical, &files)
	return
}

// cubeSum returns the filtered cube of directory id; a directory without a cube
// row sums to zero.
func (r *Repo) cubeSum(ctx context.Context, e *genEntry, s Scope, id int64, f cube.Filter) (bytesAlloc, files int64, err error) {
	pred, pargs := s.where("d.pre")
	args := append(append(filterArgs(f), filterArgs(f)...), id)
	args = append(args, pargs...)
	err = e.db.QueryRowContext(ctx,
		`SELECT hw_cube_bytes(c.blob,?,?,?), hw_cube_files(c.blob,?,?,?) FROM dirs AS d LEFT JOIN cube AS c ON c.dir_id = d.id WHERE d.id = ? AND `+pred,
		args...).Scan(&bytesAlloc, &files)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return
}

// ownFiles builds the "own files" entry of d: what is left of d once its
// subdirectories are taken away. ok is false when there are no such files.
func (r *Repo) ownFiles(ctx context.Context, e *genEntry, s Scope, d gen.Dir, f cube.Filter) (c Child, ok bool, err error) {
	c = Child{Kind: ChildFiles, Mtime: d.Mtime}
	if !filtered(f) {
		if d.OwnFiles <= 0 {
			return c, false, nil
		}
		_, kidsLogical, _, err := r.sumChildren(ctx, e, s, d.ID, f)
		if err != nil {
			return c, false, err
		}
		logical := max(d.BytesLogical-kidsLogical, 0)
		c.Files, c.BytesAlloc, c.BytesLogical = d.OwnFiles, d.OwnAlloc, &logical
		return c, true, nil
	}
	pb, pf, err := r.cubeSum(ctx, e, s, d.ID, f)
	if err != nil {
		return c, false, err
	}
	kb, _, kf, err := r.sumChildren(ctx, e, s, d.ID, f)
	if err != nil {
		return c, false, err
	}
	c.Files, c.BytesAlloc = pf-kf, max(pb-kb, 0)
	return c, c.Files > 0, nil
}

// Children returns one page of the children of ref, plus the cursor of the next
// page (nil on the last). The first page also starts with the directory's own
// files entry when it has any (in addition to Limit directories).
func (r *Repo) Children(ctx context.Context, s Scope, ref noderef.Ref, q ChildQuery) ([]Child, *ChildCursor, error) {
	if !s.allows(ref.ScanID) {
		return nil, nil, ErrNotFound
	}
	e, err := r.open(ctx, ref.ScanID)
	if err != nil {
		return nil, nil, err
	}
	defer r.release(e)
	ok, err := visible(ctx, e, s, ref.DirID)
	if err != nil {
		return nil, nil, r.fix(ref.ScanID, err)
	}
	if !ok {
		return nil, nil, ErrNotFound
	}
	d, err := gen.DirByID(ctx, e.db, ref.DirID)
	if err != nil {
		return nil, nil, r.fix(ref.ScanID, err)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultChildLimit
	}
	limit = min(limit, MaxChildLimit)
	rows, err := r.childRows(ctx, e, s, ref.DirID, rowsQuery{sort: q.Sort, filter: q.Filter, after: q.After, limit: limit + 1})
	if err != nil {
		return nil, nil, r.fix(ref.ScanID, err)
	}
	var next *ChildCursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = &ChildCursor{Name: bytes.Clone(last.name)}
		switch q.Sort {
		case SortSize:
			next.Key = last.bytesAl
		case SortFiles:
			next.Key = last.files
		case SortMtime:
			next.Key = last.mtime
		}
	}
	items := make([]Child, 0, len(rows)+1)
	if q.After == nil {
		own, ok, err := r.ownFiles(ctx, e, s, d, q.Filter)
		if err != nil {
			return nil, nil, r.fix(ref.ScanID, err)
		}
		if ok {
			items = append(items, own)
		}
	}
	for _, c := range rows {
		cr := noderef.Ref{ScanID: ref.ScanID, DirID: c.id}
		it := Child{Kind: ChildDir, Ref: &cr, Name: c.name, Files: c.files, BytesAlloc: c.bytesAl, Mtime: c.mtime}
		if !filtered(q.Filter) {
			logical := c.bytesLogical
			it.BytesLogical = &logical
		}
		items = append(items, it)
	}
	return items, next, nil
}
