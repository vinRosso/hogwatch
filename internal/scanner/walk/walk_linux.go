package walk

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/scanner/agg"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
	"golang.org/x/sys/unix"
)

// ctxCheckEvery is how many directory entries a worker handles between
// context checks.
const ctxCheckEvery = 256

// flushEvery is how many side rows (shared links, errors) a listing buffers
// before handing them to the writer, so a huge directory of hard links holds a
// bounded number of them.
const flushEvery = 1024

// subdir is a subdirectory found while listing its parent.
type subdir struct {
	name string
	st   fsx.Stat
}

// trieNode is one segment of the anchor paths. A node reached while walking is
// an anchor itself when anchor is set.
type trieNode struct {
	children map[string]*trieNode
	anchor   bool
	excluded bool // the directory is not entered (FlagExcluded)
}

// node is one directory in flight. Its accumulator exists from the moment a
// worker starts listing it until it has been merged into its parent.
//
// Ownership: the listing goroutine owns acc until it has dispatched the first
// child. Afterwards every writer of acc (a finishing child's merge, or the
// listing goroutine's own AddSkippedDir and AddError) holds mu. flags is
// written by the listing goroutine only and read by whoever drops pending to
// zero, which that atomic decrement orders after the write.
type node struct {
	parent      *node
	id          int64
	name        string // raw bytes
	st          fsx.Stat
	depthAnchor int64
	anchor      *trieNode // position in the anchor trie; nil when off it
	at          where
	dir         *fsx.Dir // open while the node's subdirectories are being opened

	acc     *agg.Node
	mu      sync.Mutex
	pending atomic.Int64 // 1 for the listing, 1 per dispatched subdirectory
	flags   int64
}

// item is a directory handed to a worker: the node and its open descriptor.
type item struct {
	n   *node
	dir *fsx.Dir
}

type walker struct {
	ctx    context.Context
	cancel func()

	// Test hooks: walk every subdirectory inline, and observe each directory entered.
	forceInline bool
	onDir       func()
	readNames   func(*fsx.Dir, func(string) error) error // nil means the directory's own ReadNames
	gate        Gate                                     // nil means no gate
	ch          chan item
	wg          sync.WaitGroup // directories in flight
	msgs        chan *rows

	crossFS, allowWritable bool
	nested                 map[DevIno]struct{} // other roots' top directories
	mountTypes             map[uint64]string   // st_dev -> mountinfo fstype

	// Test seams: stat and statfs of an opened child.
	statChild   func(*fsx.Dir) (fsx.Stat, error)
	statfsChild func(*fsx.Dir) (fsx.FSInfo, error)
	scanStart   time.Time
	cats        map[string]cube.Category
	lim         agg.Limits
	nextID      atomic.Int64

	dirs, files, symlinks, bytesLogical, bytesAlloc, hardlinkFiles atomic.Int64
	mountsSkipped, pseudoSkipped, zfsCtlSkipped, nestedSkipped     atomic.Int64
	writableRefused, changed, loops                                atomic.Int64
	errs, excluded, dupNames, maxExtKeys                           atomic.Int64

	mu     sync.Mutex
	errnos map[string]int64
	fatal  error // first sink or encoding error
}

// setFatal records the first fatal error and stops the walk.
func (w *walker) setFatal(err error) {
	w.mu.Lock()
	if w.fatal == nil {
		w.fatal = err
	}
	w.mu.Unlock()
	w.cancel()
}

func (w *walker) fatalErr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fatal
}

// send hands r to the writer goroutine, or drops it once the walk is over.
func (w *walker) send(r *rows) {
	if r.len() == 0 {
		return
	}
	select {
	case w.msgs <- r:
	case <-w.ctx.Done():
	}
}

// writeLoop is the one goroutine that calls the Sink. After a failure it keeps
// draining so producers never block on a full channel.
func (w *walker) writeLoop(s Sink, done chan<- struct{}) {
	defer close(done)
	failed := false
	for r := range w.msgs {
		if failed || w.ctx.Err() != nil {
			continue
		}
		if err := r.write(w.ctx, s); err != nil {
			failed = true
			w.setFatal(err)
		}
	}
}

// process lists one directory, dispatches its subdirectories and drops the
// listing's claim on its pending count. It owns d and closes it.
func (w *walker) process(n *node, d *fsx.Dir) {
	if w.ctx.Err() != nil {
		_ = d.Close()
		return
	}
	if w.gate != nil {
		if err := w.gate.Acquire(w.ctx); err != nil {
			_ = d.Close()
			return
		}
	}
	n.dir = d
	n.acc = agg.NewNode(w.lim)
	n.pending.Store(1)
	w.dirs.Add(1)
	if w.onDir != nil {
		w.onDir()
	}
	subs := w.list(n)
	if w.gate != nil {
		// Released before any child is walked: with inline recursion a token
		// held across it would starve the pool.
		w.gate.Release()
	}
	for i, s := range subs {
		if w.ctx.Err() != nil {
			break
		}
		child, at, flag, skip, cerr := w.cross(n, s)
		if i == len(subs)-1 {
			// The last sibling is open, so n.dir is no longer needed; closing it
			// keeps a deep chain at a constant number of descriptors.
			w.closeDir(n)
		}
		w.visit(n, s, child, at, flag, skip, cerr)
	}
	w.closeDir(n)
	if w.ctx.Err() != nil {
		return
	}
	w.done(n)
}

func (w *walker) closeDir(n *node) {
	if n.dir != nil {
		_ = n.dir.Close()
		n.dir = nil
	}
}

// visit turns the outcome of cross for subdirectory s of n into a row or a
// dispatched node. It assigns the id, so ids stay dense: only a subdirectory
// that gets a row gets one.
func (w *walker) visit(n *node, s subdir, child *fsx.Dir, at where, flag int64, skip skipKind, cerr error) {
	if skip == skipDrop {
		return
	}
	id := w.nextID.Add(1)
	cn := &node{parent: n, id: id, name: s.name, st: s.st, at: at, flags: flag}
	if skip == skipNone {
		cn.st = at.st
	}
	cn.depthAnchor, cn.anchor = childPosition(n, s.name)
	if skip == skipNone {
		n.pending.Add(1)
		w.dispatch(cn, child)
		return
	}
	// A subdirectory that is not walked: a flagged row with zero counts.
	d := w.dirRow(cn, &agg.Node{})
	if skip == skipError {
		d.ErrorCount = 1
	}
	blob, err := (&cube.Acc{}).Encode()
	if err != nil {
		w.setFatal(err)
		return
	}
	r := &rows{dirs: []gen.Dir{d}, cubes: []cubeRow{{dirID: id, blob: blob}}}
	if skip == skipError {
		r.errs = []gen.ErrorRow{errorRow(id, "", "open", cerr)}
	}
	w.send(r)
	n.mu.Lock()
	n.acc.AddSkippedDir()
	if skip == skipError {
		n.acc.AddError()
	}
	n.mu.Unlock()
}

// childPosition is the depth_anchor of subdirectory name of n and its place in
// the anchor trie: 0 at an anchor, else one more than n's.
func childPosition(n *node, name string) (int64, *trieNode) {
	var t *trieNode
	if n.anchor != nil {
		t = n.anchor.children[name]
	}
	if t != nil && t.anchor {
		return 0, t
	}
	return n.depthAnchor + 1, t
}

// done drops one claim on n. The goroutine that drops the last one finishes the
// node: its atomic decrement is ordered after every earlier decrement, so all
// writes to n.acc (the listing's, and each child's merge under n.mu) are
// visible to it. It then emits n's rows, merges n into its parent under the
// parent's mutex and drops the claim n held on the parent, and so on upwards.
func (w *walker) done(n *node) {
	for n != nil {
		if n.pending.Add(-1) != 0 {
			return
		}
		if w.ctx.Err() != nil {
			return
		}
		w.emit(n)
		p := n.parent
		if p != nil {
			p.mu.Lock()
			p.acc.Merge(n.acc)
			p.mu.Unlock()
		}
		n.acc = nil // free the accumulator; the node itself goes with its last reference
		n = p
	}
}

// emit sends the dir row, cube blob, top lists and extension totals of a
// finished node. It must run before n.acc is merged into the parent, which
// drains the heaps.
func (w *walker) emit(n *node) {
	a := n.acc
	for k, m := int64(a.ExtKeys()), w.maxExtKeys.Load(); k > m && !w.maxExtKeys.CompareAndSwap(m, k); m = w.maxExtKeys.Load() {
	}
	blob, err := a.Cube.Encode()
	if err != nil {
		w.setFatal(err)
		return
	}
	r := &rows{
		dirs:  []gen.Dir{w.dirRow(n, a)},
		cubes: []cubeRow{{dirID: n.id, blob: blob}},
	}
	if w.lim.TopN > 0 && w.lim.Qualifies(n.depthAnchor, a.BytesAlloc) {
		for _, k := range [...]struct {
			kind  string
			files []agg.File
		}{{"largest", a.Largest()}, {"oldest", a.Oldest()}} {
			for i, f := range k.files {
				r.tops = append(r.tops, gen.TopFile{
					AnchorID: n.id, Kind: k.kind, Rank: int64(i + 1), ParentDirID: f.DirID,
					Name: f.Name, Size: f.Size, Alloc: f.Alloc, Mtime: f.Mtime, UID: f.UID,
				})
			}
		}
		for _, e := range a.TopExts() {
			r.exts = append(r.exts, gen.TopExt{DirID: n.id, Ext: e.Ext, Bytes: e.Bytes, Files: e.Files})
		}
	}
	w.send(r)
}

// i64 bit-casts a stat value for storage, as gen.Dir documents.
func i64(u uint64) int64 { return int64(u) } //nolint:gosec // bit-cast by design

func (w *walker) dirRow(n *node, a *agg.Node) gen.Dir {
	var parentID int64
	if n.parent != nil {
		parentID = n.parent.id
	}
	return gen.Dir{
		ID: n.id, ParentID: parentID, Name: []byte(n.name), DepthAnchor: n.depthAnchor,
		Dev: i64(n.st.Dev), Ino: i64(n.st.Ino), Mtime: n.st.Mtime,
		UID: int64(n.st.UID), GID: int64(n.st.GID),
		OwnFiles: a.OwnFiles, OwnAlloc: a.OwnAlloc, Files: a.Files, Dirs: a.Dirs,
		BytesLogical: a.BytesLogical, BytesAlloc: a.BytesAlloc,
		Flags: n.flags, ErrorCount: a.Errors,
	}
}

// localCounts batches the per-entry counters of one listing into a few atomic
// adds.
type localCounts struct {
	files, symlinks, bytesLogical, bytesAlloc, hardlinks int64
}

func (w *walker) addCounts(c *localCounts) {
	w.files.Add(c.files)
	w.symlinks.Add(c.symlinks)
	w.bytesLogical.Add(c.bytesLogical)
	w.bytesAlloc.Add(c.bytesAlloc)
	w.hardlinkFiles.Add(c.hardlinks)
	*c = localCounts{}
}

// list reads n.dir once, folding its non-directory entries into n.acc and
// returning its subdirectories. The caller has not dispatched any child yet, so
// n.acc is still exclusively the listing's.
func (w *walker) list(n *node) []subdir {
	var (
		subs []subdir
		subN map[string]struct{} // subdirectory names seen, for the duplicate check
		buf  []byte              // reused for every file name; agg copies a name it keeps
		seen int
		c    localCounts
		r    = &rows{}
	)
	flush := func() {
		w.addCounts(&c)
		w.send(r)
		r = &rows{}
	}
	read := n.dir.ReadNames
	if w.readNames != nil {
		read = func(fn func(string) error) error { return w.readNames(n.dir, fn) }
	}
	err := read(func(name string) error {
		seen++
		if seen%ctxCheckEvery == 0 {
			if err := w.ctx.Err(); err != nil {
				return err
			}
			w.addCounts(&c) // keep progress live inside a huge directory
		}
		st, err := n.dir.Lstat(name)
		if err != nil {
			w.fail(err)
			r.errs = append(r.errs, errorRow(n.id, name, "lstat", err))
			n.acc.AddError()
			return nil
		}
		if st.IsDir() {
			// getdents racing a rename can return a name twice. A second row for
			// it would break the unique (parent_id, name) index.
			if subN == nil {
				subN = make(map[string]struct{})
			}
			if _, dup := subN[name]; dup {
				w.dupNames.Add(1)
				return nil
			}
			subN[name] = struct{}{}
			subs = append(subs, subdir{name: name, st: st})
			return nil
		}
		buf = append(buf[:0], name...)
		alloc := st.Blocks * 512
		cell := agg.CellFor(buf, st.Size, st.Mtime, w.scanStart, w.cats)
		n.acc.AddFile(agg.File{
			Name: buf, DirID: n.id, ParentIno: n.st.Ino, Ino: st.Ino, Size: st.Size, Alloc: alloc,
			Mtime: st.Mtime, UID: int64(st.UID), Cell: cell, Ext: cube.Ext(buf),
		})
		c.files++
		c.bytesLogical += st.Size
		c.bytesAlloc += alloc
		if st.IsSymlink() {
			c.symlinks++
			n.flags |= gen.FlagSymlink
		}
		if st.Nlink > 1 {
			c.hardlinks++
			r.inodes = append(r.inodes, gen.SharedInode{
				Dev: i64(st.Dev), Ino: i64(st.Ino), Size: st.Size, Alloc: alloc,
				Mtime: st.Mtime, CubeCell: int64(cell.Index()),
			})
			r.links = append(r.links, gen.SharedLink{
				Dev: i64(st.Dev), Ino: i64(st.Ino), DirID: n.id, Name: []byte(name),
				Size: st.Size, Alloc: alloc, Mtime: st.Mtime,
			})
		}
		if r.len() >= flushEvery {
			flush()
		}
		return nil
	})
	if err != nil && w.ctx.Err() == nil {
		w.fail(err)
		r.errs = append(r.errs, errorRow(n.id, "", "readdir", err))
		n.acc.AddError()
		n.flags |= gen.FlagUnreadable
	}
	flush()
	return subs
}

// dispatch hands child to an idle worker, or walks it inline when none is free.
// Walking inline is what keeps the walk depth-first when the pool is saturated,
// which bounds the number of live nodes and their accumulators.
func (w *walker) dispatch(cn *node, child *fsx.Dir) {
	if w.forceInline {
		w.process(cn, child)
		return
	}
	w.wg.Add(1)
	select {
	case w.ch <- item{n: cn, dir: child}:
	default:
		w.wg.Done()
		w.process(cn, child)
	}
}

// errnoOf returns the errno inside err, or 0.
func errnoOf(err error) unix.Errno {
	var errno unix.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return 0
}

// errorRow builds the errors row for a failure on entry name of directory
// dirID (an empty name means the directory itself). The message never carries
// the path error's text, which would repeat the raw name.
func errorRow(dirID int64, name, op string, err error) gen.ErrorRow {
	errno := errnoOf(err)
	msg := err.Error()
	var ce changedErr
	switch {
	case errors.As(err, &ce):
		msg = ce.Error() // already free of the raw name
	case errno != 0:
		msg = errno.Error()
	}
	return gen.ErrorRow{
		DirID: dirID, Name: []byte(name), Op: op, Errno: int64(errno), //nolint:gosec // errno values are small
		Message: strings.ToValidUTF8(msg, "�"),
	}
}

// fail counts err, keyed by errno name.
func (w *walker) fail(err error) {
	key := "other"
	if errno := errnoOf(err); errno != 0 {
		if name := unix.ErrnoName(errno); name != "" {
			key = name
		}
	}
	w.mu.Lock()
	if w.errnos == nil {
		w.errnos = make(map[string]int64)
	}
	w.errnos[key]++
	w.mu.Unlock()
	w.errs.Add(1) // after the map, so a snapshot never has Errors > 0 with nil Errnos
}

func (w *walker) snapshot() Stats {
	s := Stats{
		Dirs:            w.dirs.Load(),
		Files:           w.files.Load(),
		Symlinks:        w.symlinks.Load(),
		BytesLogical:    w.bytesLogical.Load(),
		BytesAlloc:      w.bytesAlloc.Load(),
		HardlinkFiles:   w.hardlinkFiles.Load(),
		MountsSkipped:   w.mountsSkipped.Load(),
		PseudoSkipped:   w.pseudoSkipped.Load(),
		ZFSCtlSkipped:   w.zfsCtlSkipped.Load(),
		NestedSkipped:   w.nestedSkipped.Load(),
		WritableRefused: w.writableRefused.Load(),
		Excluded:        w.excluded.Load(),
		DupNames:        w.dupNames.Load(),
		MaxExtKeys:      w.maxExtKeys.Load(),
		Changed:         w.changed.Load(),
		Loops:           w.loops.Load(),
		Errors:          w.errs.Load(),
	}
	w.mu.Lock()
	if len(w.errnos) > 0 {
		s.Errnos = make(map[string]int64, len(w.errnos))
		for k, v := range w.errnos {
			s.Errnos[k] = v
		}
	}
	w.mu.Unlock()
	return s
}
