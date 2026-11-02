package walk

import (
	"context"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
)

// Sink receives the rows of one scan. Scan calls it from a single goroutine,
// so an implementation needs no locking; *gen.Writer is one.
type Sink interface {
	AddDir(ctx context.Context, d gen.Dir) error
	AddCube(ctx context.Context, dirID int64, b cube.Blob) error
	AddTopFile(ctx context.Context, f gen.TopFile) error
	AddTopExt(ctx context.Context, e gen.TopExt) error
	AddSharedInode(ctx context.Context, s gen.SharedInode) error
	AddSharedLink(ctx context.Context, l gen.SharedLink) error
	AddError(ctx context.Context, e gen.ErrorRow) error
}

var _ Sink = (*gen.Writer)(nil)

// DevIno identifies a directory by device and inode.
type DevIno struct{ Dev, Ino uint64 }

type discardSink struct{}

func (discardSink) AddDir(context.Context, gen.Dir) error                 { return nil }
func (discardSink) AddCube(context.Context, int64, cube.Blob) error       { return nil }
func (discardSink) AddTopFile(context.Context, gen.TopFile) error         { return nil }
func (discardSink) AddTopExt(context.Context, gen.TopExt) error           { return nil }
func (discardSink) AddSharedInode(context.Context, gen.SharedInode) error { return nil }
func (discardSink) AddSharedLink(context.Context, gen.SharedLink) error   { return nil }
func (discardSink) AddError(context.Context, gen.ErrorRow) error          { return nil }

// Discard is a Sink that drops every row; bench uses it to time the walk alone.
var Discard Sink = discardSink{}

// rows is one message to the writer goroutine: the rows a worker produced at
// one point, as values the worker no longer touches.
type rows struct {
	dirs   []gen.Dir
	cubes  []cubeRow
	tops   []gen.TopFile
	exts   []gen.TopExt
	inodes []gen.SharedInode
	links  []gen.SharedLink
	errs   []gen.ErrorRow
}

type cubeRow struct {
	dirID int64
	blob  cube.Blob
}

func (r *rows) len() int {
	return len(r.dirs) + len(r.cubes) + len(r.tops) + len(r.exts) + len(r.inodes) + len(r.links) + len(r.errs)
}

// write hands every row of r to s and stops at the first error. Dirs go
// first, so a reader of an interrupted file never sees a cube without a row.
func (r *rows) write(ctx context.Context, s Sink) error {
	for _, d := range r.dirs {
		if err := s.AddDir(ctx, d); err != nil {
			return err
		}
	}
	for _, c := range r.cubes {
		if err := s.AddCube(ctx, c.dirID, c.blob); err != nil {
			return err
		}
	}
	for _, f := range r.tops {
		if err := s.AddTopFile(ctx, f); err != nil {
			return err
		}
	}
	for _, e := range r.exts {
		if err := s.AddTopExt(ctx, e); err != nil {
			return err
		}
	}
	for _, i := range r.inodes {
		if err := s.AddSharedInode(ctx, i); err != nil {
			return err
		}
	}
	for _, l := range r.links {
		if err := s.AddSharedLink(ctx, l); err != nil {
			return err
		}
	}
	for _, e := range r.errs {
		if err := s.AddError(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// Gate bounds how many directories are listed at once. Scan holds one token
// while it reads a single directory and releases it before it walks any child,
// so a token is never held across a recursion. A failed Acquire holds nothing.
// The gate package implements it, with priorities.
type Gate interface {
	Acquire(ctx context.Context) error
	Release()
}
