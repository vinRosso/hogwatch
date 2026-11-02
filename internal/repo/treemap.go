package repo

import (
	"context"
	"errors"
	"slices"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/noderef"
)

// TreemapMaxNodes caps the nodes of one treemap, root included. A parent whose
// children do not fit keeps its largest ones and merges the others into one
// TreeRest node.
const TreemapMaxNodes = 2000

// TreeKind says what a TreeNode stands for.
type TreeKind uint8

// Tree node kinds.
const (
	TreeDir TreeKind = iota
	TreeFiles
	TreeRest
	TreeExt
)

// TreeNode is one rectangle of the treemap. Files, Rest and Ext nodes have no
// Ref. An Ext node is the directory's own files with one extension (Name, ""
// for none); a Files node is own files not attributed to an extension.
type TreeNode struct {
	Kind       TreeKind
	Ref        *noderef.Ref
	Name       []byte
	BytesAlloc int64
	Files      int64
	Children   []TreeNode
}

// Treemap returns the tree below ref, depth levels deep and at most
// TreemapMaxNodes nodes, largest children first, filled breadth first.
func (r *Repo) Treemap(ctx context.Context, s Scope, ref noderef.Ref, depth int, f cube.Filter) (TreeNode, error) {
	if !s.allows(ref.ScanID) {
		return TreeNode{}, ErrNotFound
	}
	e, err := r.open(ctx, ref.ScanID)
	if err != nil {
		return TreeNode{}, err
	}
	defer r.release(e)
	ok, err := visible(ctx, e, s, ref.DirID)
	if err != nil {
		return TreeNode{}, r.fix(ref.ScanID, err)
	}
	if !ok {
		return TreeNode{}, ErrNotFound
	}
	d, err := gen.DirByID(ctx, e.db, ref.DirID)
	if err != nil {
		return TreeNode{}, r.fix(ref.ScanID, err)
	}
	root := TreeNode{Kind: TreeDir, Ref: &ref, Name: d.Name, BytesAlloc: d.BytesAlloc, Files: d.Files}
	if filtered(f) {
		if root.BytesAlloc, root.Files, err = r.cubeSum(ctx, e, s, d.ID, f); err != nil {
			return TreeNode{}, r.fix(ref.ScanID, err)
		}
	}
	count := 1
	frontier := []*TreeNode{&root}
	for level := 1; level <= depth && len(frontier) > 0 && count < TreemapMaxNodes; level++ {
		var next []*TreeNode
		for _, p := range frontier {
			if count >= TreemapMaxNodes {
				break
			}
			kids, err := r.treeChildren(ctx, e, s, *p.Ref, TreemapMaxNodes-count, f)
			if err != nil {
				return TreeNode{}, r.fix(ref.ScanID, err)
			}
			p.Children = kids
			count += len(kids)
			for i := range p.Children {
				if p.Children[i].Kind == TreeDir {
					next = append(next, &p.Children[i])
				}
			}
		}
		frontier = next
	}
	return root, nil
}

// treeChildren returns the children of ref as treemap nodes: directories and
// the own-files node by bytes descending, zero-byte ones dropped, at most room
// of them (the last a TreeRest when some do not fit).
func (r *Repo) treeChildren(ctx context.Context, e *genEntry, s Scope, ref noderef.Ref, room int, f cube.Filter) ([]TreeNode, error) {
	d, err := gen.DirByID(ctx, e.db, ref.DirID)
	if err != nil {
		return nil, err
	}
	rows, err := r.childRows(ctx, e, s, ref.DirID, rowsQuery{sort: SortSize, filter: f, limit: room + 1, positive: true})
	if err != nil {
		return nil, err
	}
	var items []TreeNode
	own, hasOwn, err := r.ownFiles(ctx, e, s, d, f)
	if err != nil {
		return nil, err
	}
	if hasOwn && own.BytesAlloc > 0 {
		blocks := []TreeNode{{Kind: TreeFiles, BytesAlloc: own.BytesAlloc, Files: own.Files}}
		if !filtered(f) {
			if blocks, err = r.ownExts(ctx, e, s, d); err != nil {
				return nil, err
			}
		}
		items = append(items, blocks...)
	}
	for _, c := range rows {
		cr := noderef.Ref{ScanID: ref.ScanID, DirID: c.id}
		items = append(items, TreeNode{Kind: TreeDir, Ref: &cr, Name: c.name, BytesAlloc: c.bytesAl, Files: c.files})
	}
	slices.SortStableFunc(items, func(a, b TreeNode) int {
		switch {
		case a.BytesAlloc > b.BytesAlloc:
			return -1
		case a.BytesAlloc < b.BytesAlloc:
			return 1
		}
		return 0
	})
	if len(items) <= room {
		return items, nil
	}
	// Overflow: keep room-1 and merge everything else, however many children
	// that is, into one rest node.
	kept := items[:room-1]
	var keptBytes, keptFiles int64
	for _, k := range kept {
		keptBytes += k.BytesAlloc
		keptFiles += k.Files
	}
	all, _, allFiles, err := r.sumChildren(ctx, e, s, ref.DirID, f)
	if err != nil {
		return nil, err
	}
	if hasOwn {
		all += own.BytesAlloc
		allFiles += own.Files
	}
	rest := TreeNode{Kind: TreeRest, BytesAlloc: max(all-keptBytes, 0), Files: max(allFiles-keptFiles, 0)}
	return append(slices.Clone(kept), rest), nil
}

// ownExts splits d's own files by extension from top_exts: d's row for an
// extension minus its children's rows. top_exts rows are exact where present
// but a directory may not have them all; a child's list is
// complete when its rows' file counts add up to the child's files, and then a
// missing row means none. An extension is attributed only when every child
// either lists it or has a complete list; what is left over (extensions
// outside d's rows, or ones a child may hold unlisted) is one TreeFiles node.
// Without rows for d the whole of it is that one node.
func (r *Repo) ownExts(ctx context.Context, e *genEntry, s Scope, d gen.Dir) ([]TreeNode, error) {
	type ext struct {
		name         string
		bytes, files int64
		kidB, kidF   int64
		kidOpen      int64 // children with an incomplete list that hold a row for it
	}
	rs, err := e.db.QueryContext(ctx, `SELECT ext, bytes, files FROM top_exts WHERE dir_id = ? ORDER BY bytes DESC, ext`, d.ID)
	if err != nil {
		return nil, err
	}
	var exts []*ext
	byName := map[string]*ext{}
	for rs.Next() {
		x := &ext{}
		if err := rs.Scan(&x.name, &x.bytes, &x.files); err != nil {
			_ = rs.Close()
			return nil, err
		}
		exts = append(exts, x)
		byName[x.name] = x
	}
	if err := errors.Join(rs.Err(), rs.Close()); err != nil {
		return nil, err
	}
	var open int64 // children with an incomplete list
	if len(exts) > 0 && d.Dirs > 0 {
		pred, pargs := s.where("d.pre")
		// Whether a child's list is complete is worked out once per child, not once per
		// joined extension row.
		const kids = `WITH kids AS MATERIALIZED (
			SELECT d.id, d.files <> COALESCE((SELECT SUM(y.files) FROM top_exts AS y WHERE y.dir_id = d.id), 0) AS open
			FROM dirs AS d WHERE d.parent_id = ? AND `
		args := append([]any{d.ID}, pargs...)
		if err := e.db.QueryRowContext(ctx,
			scoped(kids, pred, `) SELECT COALESCE(SUM(open), 0) FROM kids`), args...).Scan(&open); err != nil {
			return nil, err
		}
		rs, err := e.db.QueryContext(ctx,
			scoped(kids, pred, `) SELECT x.ext, SUM(x.bytes), SUM(x.files), SUM(k.open)
			 FROM kids AS k JOIN top_exts AS x ON x.dir_id = k.id GROUP BY x.ext`), args...)
		if err != nil {
			return nil, err
		}
		for rs.Next() {
			var name string
			var b, n, o int64
			if err := rs.Scan(&name, &b, &n, &o); err != nil {
				_ = rs.Close()
				return nil, err
			}
			if x := byName[name]; x != nil {
				x.kidB, x.kidF, x.kidOpen = b, n, o
			}
		}
		if err := errors.Join(rs.Err(), rs.Close()); err != nil {
			return nil, err
		}
	}
	var out []TreeNode
	restB, restF := d.OwnAlloc, d.OwnFiles
	for _, x := range exts {
		if x.kidOpen < open {
			continue // some child may hold it without a row
		}
		b := min(max(x.bytes-x.kidB, 0), restB)
		n := min(max(x.files-x.kidF, 0), restF)
		if b <= 0 {
			continue
		}
		out = append(out, TreeNode{Kind: TreeExt, Name: []byte(x.name), BytesAlloc: b, Files: n})
		restB, restF = restB-b, restF-n
	}
	if restB > 0 {
		out = append(out, TreeNode{Kind: TreeFiles, BytesAlloc: restB, Files: restF})
	}
	return out, nil
}
