package repo

import (
	"bytes"
	"context"
	"fmt"

	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/noderef"
)

// Stream yields rows one at a time from an open generation. Close releases the
// generation; Each may be called once.
type Stream[T any] interface {
	Each(func(T) error) error
	Close()
}

// TreeRow is one directory of an exported subtree. RelPath is relative to the
// exported node (empty for the node itself).
type TreeRow struct {
	RelPath                                      []byte
	Files, Dirs, BytesLogical, BytesAlloc, Mtime int64
}

type treeStream struct {
	r      *Repo
	e      *genEntry
	s      Scope
	ref    noderef.Ref
	pre    int64
	post   int64
	closed bool
}

type pathFrame struct {
	post int64
	name []byte
}

// ExportTree opens ref for streaming: the node and every directory below it
// that is in scope, in pre order. The node is checked here, so a 404 happens
// before the caller writes anything.
func (r *Repo) ExportTree(ctx context.Context, s Scope, ref noderef.Ref) (Stream[TreeRow], error) {
	e, err := r.openVisible(ctx, s, ref)
	if err != nil {
		return nil, err
	}
	d, err := gen.DirByID(ctx, e.db, ref.DirID)
	if err != nil {
		r.release(e)
		return nil, r.fix(ref.ScanID, err)
	}
	return &treeStream{r: r, e: e, s: s, ref: ref, pre: d.Pre, post: d.Post}, nil
}

func (t *treeStream) Close() {
	if !t.closed {
		t.closed = true
		t.r.release(t.e)
	}
}

func (t *treeStream) Each(fn func(TreeRow) error) error {
	ctx := context.Background()
	pred, pargs := t.s.where("d.pre")
	rs, err := t.e.db.QueryContext(ctx,
		scoped(`SELECT d.id, d.name, d.pre, d.post, d.files, d.dirs, d.bytes_logical, d.bytes_alloc, d.mtime
		 FROM dirs AS d WHERE d.pre BETWEEN ? AND ? AND `, pred, ` ORDER BY d.pre`),
		append([]any{t.pre, t.post}, pargs...)...)
	if err != nil {
		return t.r.fix(t.ref.ScanID, err)
	}
	defer func() { _ = rs.Close() }()
	var stack []pathFrame
	for rs.Next() {
		var id, pre, post int64
		var name []byte
		var row TreeRow
		if err := rs.Scan(&id, &name, &pre, &post, &row.Files, &row.Dirs, &row.BytesLogical, &row.BytesAlloc, &row.Mtime); err != nil {
			return t.r.fix(t.ref.ScanID, err)
		}
		for len(stack) > 0 && stack[len(stack)-1].post < pre {
			stack = stack[:len(stack)-1]
		}
		if id != t.ref.DirID {
			stack = append(stack, pathFrame{post: post, name: name})
			names := make([][]byte, 0, len(stack))
			for _, f := range stack[1:] {
				names = append(names, f.name)
			}
			row.RelPath = bytes.Join(names, []byte("/"))
		} else {
			stack = append(stack[:0], pathFrame{post: post})
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return t.r.fix(t.ref.ScanID, rs.Err())
}

// TopExportRow is one stored top-N file with the path of its directory
// relative to the exported node.
type TopExportRow struct {
	Kind   string
	Rank   int
	Name   []byte
	DirRel []byte
	Size   int64
	Alloc  int64
	Mtime  int64
}

// ExportTop returns the stored largest and then oldest files below ref.
func (r *Repo) ExportTop(ctx context.Context, s Scope, ref noderef.Ref) ([]TopExportRow, error) {
	e, err := r.openVisible(ctx, s, ref)
	if err != nil {
		return nil, err
	}
	nodeSegs, err := gen.DirPath(ctx, e.db, ref.DirID)
	r.release(e)
	if err != nil {
		return nil, r.fix(ref.ScanID, err)
	}
	var out []TopExportRow
	rels := map[int64][]byte{}
	for _, kind := range []string{"largest", "oldest"} {
		items, _, err := r.Top(ctx, s, ref, kind)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			rel, ok := rels[it.DirRef.DirID]
			if !ok {
				if rel, err = r.relPath(ctx, it.DirRef, len(nodeSegs)); err != nil {
					return nil, err
				}
				rels[it.DirRef.DirID] = rel
			}
			out = append(out, TopExportRow{Kind: kind, Rank: it.Rank, Name: it.Name, DirRel: rel, Size: it.Size, Alloc: it.Alloc, Mtime: it.Mtime})
		}
	}
	return out, nil
}

// relPath is the path of ref with the first skip segments dropped. Every
// caller passes a ref it got from the scope-checked Top, and the lookup reads
// names only.
func (r *Repo) relPath(ctx context.Context, ref noderef.Ref, skip int) ([]byte, error) {
	e, err := r.open(ctx, ref.ScanID)
	if err != nil {
		return nil, err
	}
	defer r.release(e)
	segs, err := gen.DirPath(ctx, e.db, ref.DirID)
	if err != nil {
		return nil, r.fix(ref.ScanID, err)
	}
	if skip > len(segs) {
		skip = len(segs)
	}
	return bytes.Join(segs[skip:], []byte("/")), nil
}

// UsageExportRow is one path assignment inside the exported node.
type UsageExportRow struct {
	Username                        string
	HostPath                        []byte
	Found, Redundant                bool
	Files, BytesLogical, BytesAlloc int64
}

// ExportUsage lists the path assignments on the node's root that lie inside
// the node: the caller's own, or everyone's for an admin.
func (r *Repo) ExportUsage(ctx context.Context, c Caller, s Scope, ref noderef.Ref) ([]UsageExportRow, error) {
	n, err := r.Node(ctx, s, ref)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListPathStatsOnRoot(ctx, dbq.ListPathStatsOnRootParams{RootID: n.RootID, ScanID: ref.ScanID})
	if err != nil {
		return nil, fmt.Errorf("repo: export usage: %w", err)
	}
	out := []UsageExportRow{}
	for _, x := range rows {
		if !c.Admin && x.UserID != c.UserID {
			continue
		}
		if !within(x.RelPath, n.RelPath) {
			continue
		}
		out = append(out, UsageExportRow{
			Username: x.Username, HostPath: hostPath(x.HostPath, x.RelPath), Found: x.Found != 0, Redundant: x.Redundant != 0,
			Files: x.Files, BytesLogical: x.BytesLogical, BytesAlloc: x.BytesAlloc,
		})
	}
	return out, nil
}

// within reports whether path equals node or lies below it.
func within(path, node []byte) bool {
	if len(node) == 0 {
		return true
	}
	return bytes.Equal(path, node) || bytes.HasPrefix(path, append(append([]byte(nil), node...), '/'))
}
