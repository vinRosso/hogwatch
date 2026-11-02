//go:build linux

package listing

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/db"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
	"github.com/vinRosso/hogwatch/internal/gen"
	"github.com/vinRosso/hogwatch/internal/noderef"
	"github.com/vinRosso/hogwatch/internal/scanner/fsx"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
)

// The failures a listing job can end with. Their messages are fixed: they are
// stored in jobs.error and shown to the requester, so they carry no path.
var (
	ErrNotPermitted = errors.New("not permitted")
	ErrChanged      = errors.New("changed since last scan")
	ErrGone         = errors.New("scan no longer available")
	ErrCrossesMount = errors.New("target is on another filesystem")
	// ErrIO stands for every other failure; the detail is only logged, because
	// it can name a host path.
	ErrIO = errors.New("listing failed")
)

// Defaults for Deps fields left at zero.
const (
	DefaultMaxRows   = 100_000
	DefaultBatchRows = 1_000
)

// Deps is what Run needs.
type Deps struct {
	DB        *sql.DB   // the main database
	DataDir   string    // holds scans/<root>-<scan>.db
	Gate      walk.Gate // nil means no gate; a token is held per directory read
	Now       func() time.Time
	MaxRows   int // rows kept; the listing is truncated beyond it
	BatchRows int // rows per insert transaction
}

// Row types of listing_results.type.
const (
	typeFile    = "file"
	typeDir     = "dir"
	typeSymlink = "symlink"
	typeOther   = "other"
)

// errStop ends the walk once the row cap is hit. It never leaves Run.
var errStop = errors.New("listing: row cap reached")

// afterFlush is a test seam called after each committed batch.
var afterFlush = func() {}

// Run executes a listing job and writes its rows, numbered from 1 in seq. It
// returns the progress and leaves the job's terminal status to the caller. On
// any error, including cancellation, the rows already written are deleted.
func Run(ctx context.Context, d Deps, job dbq.Job, root dbq.ScanRoot) (prog db.ListingProgress, err error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.MaxRows <= 0 {
		d.MaxRows = DefaultMaxRows
	}
	if d.BatchRows <= 0 {
		d.BatchRows = DefaultBatchRows
	}
	q := dbq.New(d.DB)
	defer func() {
		if err != nil {
			prog = db.ListingProgress{}
			// The context may be cancelled; the cleanup must still run.
			_ = q.DeleteListingResults(context.WithoutCancel(ctx), job.ID)
		}
	}()

	// 1. Access.
	if !job.RequestedBy.Valid {
		return prog, ErrNotPermitted
	}
	acc, err := q.GetUserAccess(ctx, job.RequestedBy.Int64)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return prog, ErrNotPermitted
	case err != nil:
		return prog, ioErr(ctx, fmt.Errorf("listing: access: %w", err))
	}
	isAdmin := acc.AdminLocal != 0 || acc.AdminOidc != 0
	if acc.Disabled != 0 || (!isAdmin && acc.CanListFiles == 0) {
		return prog, ErrNotPermitted
	}

	// 2. Parameters and target; the scan must belong to the job's root.
	var params db.ListingParams
	if err := json.Unmarshal([]byte(job.Params), &params); err != nil {
		return prog, fmt.Errorf("listing: params: %w", err)
	}
	if params.Mode != "children" && params.Mode != "subtree" {
		return prog, fmt.Errorf("listing: unknown mode %q", params.Mode)
	}
	ref, perr := noderef.Parse(job.TargetNode.String)
	if perr != nil || !job.TargetNode.Valid || root.ID != job.RootID {
		return prog, ErrGone
	}

	// 3. The generation, the node and its path.
	gdb, meta, err := gen.OpenReadOnly(ctx, gen.Path(d.DataDir, job.RootID, ref.ScanID))
	if err != nil {
		if errors.Is(err, gen.ErrNotExist) || errors.Is(err, gen.ErrSchemaVersion) {
			return prog, ErrGone
		}
		return prog, ioErr(ctx, err)
	}
	defer func() { _ = gdb.Close() }()
	if meta.RootID != job.RootID || meta.ScanID != ref.ScanID {
		return prog, ErrGone
	}
	node, err := gen.DirByID(ctx, gdb, ref.DirID)
	if err != nil {
		if errors.Is(err, gen.ErrNoDir) {
			return prog, ErrGone
		}
		return prog, ioErr(ctx, err)
	}
	segs, err := gen.DirPath(ctx, gdb, ref.DirID)
	if err != nil {
		if errors.Is(err, gen.ErrNoDir) {
			return prog, ErrGone
		}
		return prog, ioErr(ctx, err)
	}
	// Excluded and nested-root directories are not part of this root's data.
	if node.Flags&hiddenFlags != 0 {
		return prog, ErrGone
	}
	var skip map[string]struct{}
	if params.Mode == "subtree" {
		if skip, err = hiddenDirs(ctx, gdb, segs); err != nil {
			return prog, err
		}
	}

	// 4. The anchor: the root for an admin, else the deepest assigned path
	// that contains the node.
	anchorN := 0 // number of leading segments of the node path inside the anchor
	if !isAdmin {
		n, ok, aerr := deepestAnchor(ctx, q, job, segs)
		if aerr != nil {
			return prog, aerr
		}
		if !ok {
			return prog, ErrNotPermitted
		}
		anchorN = n
	}

	// 5. Open root, anchor, node. No symlink is followed, no mount crossed.
	rootDir, err := fsx.OpenRoot(root.ContainerPath)
	if err != nil {
		return prog, ioErr(ctx, fmt.Errorf("listing: open root: %w", err))
	}
	defer func() { _ = rootDir.Close() }()
	anchorDir, err := rootDir.OpenBeneath(joinSegs(segs[:anchorN]))
	if err != nil {
		return prog, openErr(ctx, err)
	}
	defer func() { _ = anchorDir.Close() }()
	target, err := anchorDir.OpenBeneath(joinSegs(segs[anchorN:]))
	if err != nil {
		return prog, openErr(ctx, err)
	}
	defer func() { _ = target.Close() }()

	// 6. Still the directory the scan saw?
	st, err := target.Stat()
	if err != nil {
		return prog, ioErr(ctx, fmt.Errorf("listing: stat target: %w", err))
	}
	if i64(st.Dev) != node.Dev || i64(st.Ino) != node.Ino || !st.IsDir() {
		return prog, ErrChanged
	}

	// 7. Emit.
	l := &lister{
		d: d, q: q, job: job.ID, params: params,
		exts: lowerSet(params.Exts), subtree: params.Mode == "subtree",
		skip: skip, base: joinSegs(segs),
	}
	if params.MinAgeDays > 0 {
		l.cutoff = d.Now().AddDate(0, 0, -params.MinAgeDays).Unix()
		l.hasCutoff = true
	}
	err = l.walk(ctx, target, nil)
	if errors.Is(err, errStop) {
		err = nil
	}
	if err == nil {
		err = l.flush(ctx)
	}
	if err != nil {
		return prog, err
	}
	return l.prog, nil
}

// deepestAnchor finds the requester's longest assigned path on the job's root
// that contains the node path, as a count of leading segments.
func deepestAnchor(ctx context.Context, q *dbq.Queries, job dbq.Job, segs [][]byte) (int, bool, error) {
	paths, err := q.ListUserPathsByRoot(ctx, job.RootID)
	if err != nil {
		return 0, false, ioErr(ctx, fmt.Errorf("listing: user paths: %w", err))
	}
	best, found := 0, false
	for _, p := range paths {
		if p.UserID != job.RequestedBy.Int64 {
			continue
		}
		n, ok := prefixSegments(p.RelPath, segs)
		if ok && (!found || n > best) {
			best, found = n, true
		}
	}
	return best, found, nil
}

// prefixSegments reports whether rel, a "/"-joined path ("" is the root), is a
// segment-wise prefix of segs, and how many segments it spans.
func prefixSegments(rel []byte, segs [][]byte) (int, bool) {
	if len(rel) == 0 {
		return 0, true
	}
	off := 0
	for n, s := range segs {
		if !bytes.HasPrefix(rel[off:], s) {
			return 0, false
		}
		off += len(s)
		if off == len(rel) {
			return n + 1, true
		}
		if rel[off] != '/' {
			return 0, false
		}
		off++
	}
	return 0, false
}

func joinSegs(segs [][]byte) string { return string(bytes.Join(segs, []byte("/"))) }

// openErr maps a failed open to the fixed error of a changed or crossing
// target; anything else keeps its detail for the log.
func openErr(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, unix.EXDEV):
		return ErrCrossesMount
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR), errors.Is(err, unix.ENOENT):
		return ErrChanged
	}
	return ioErr(ctx, fmt.Errorf("listing: open target: %w", err))
}

// ioErr returns the context's error when it is done, ErrIO if err already is
// one, and otherwise logs err (it may name a host path) and returns ErrIO.
func ioErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if errors.Is(err, ErrIO) {
		return err
	}
	slog.Warn("listing failed", "err", err)
	return ErrIO
}

func i64(v uint64) int64 { return int64(v) } //nolint:gosec // bit-cast, as the generation stores it

func lowerSet(exts []string) map[string]struct{} {
	if len(exts) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(exts))
	for _, e := range exts {
		m[strings.ToLower(strings.TrimPrefix(e, "."))] = struct{}{}
	}
	return m
}

type lister struct {
	d         Deps
	q         *dbq.Queries
	job       int64
	params    db.ListingParams
	exts      map[string]struct{}
	cutoff    int64
	hasCutoff bool
	subtree   bool
	skip      map[string]struct{} // full paths of excluded and nested-root directories
	base      string              // path of the target below the scan root

	prog  db.ListingProgress
	seq   int64
	batch []dbq.InsertListingResultParams
}

type entry struct {
	name string
	st   fsx.Stat
}

// walk lists dir, whose path below the target is prefix, and in subtree mode
// recurses into its subdirectories.
func (l *lister) walk(ctx context.Context, dir *fsx.Dir, prefix []byte) error {
	files, subdirs, err := l.read(ctx, dir)
	if err != nil {
		if len(prefix) == 0 || ctx.Err() != nil {
			return ioErr(ctx, err)
		}
		l.prog.Errors++ // an unreadable subdirectory is counted, not fatal
		return nil
	}
	for _, e := range files {
		if err := l.add(ctx, relName(prefix, e.name), e.st); err != nil {
			return err
		}
	}
	for _, name := range subdirs {
		if _, hidden := l.skip[l.full(prefix, name)]; hidden {
			l.prog.MountsSkipped++ // counted with the mounts: no separate counter exists
			continue
		}
		child, err := dir.OpenBeneath(name)
		if err != nil {
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case errors.Is(err, unix.EXDEV):
				l.prog.MountsSkipped++
			default:
				l.prog.Errors++
			}
			continue
		}
		err = l.walk(ctx, child, relName(prefix, name))
		_ = child.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// read lists one directory under a gate token. It returns the entries to emit
// (children mode: every entry, directories included; subtree mode:
// non-directories that pass the filters) and, in subtree mode, the names of
// the subdirectories to enter. It stops collecting once more rows than the
// cap allows are in hand, because the listing is truncated then anyway.
func (l *lister) read(ctx context.Context, dir *fsx.Dir) (emit []entry, subdirs []string, err error) {
	if g := l.d.Gate; g != nil {
		if err := g.Acquire(ctx); err != nil {
			return nil, nil, err
		}
		defer g.Release()
	}
	var names []string
	if err := dir.ReadNames(func(n string) error {
		names = append(names, n)
		return ctx.Err()
	}); err != nil {
		return nil, nil, ioErr(ctx, fmt.Errorf("listing: read directory: %w", err))
	}
	slices.Sort(names)
	room := int64(l.d.MaxRows) - l.prog.Rows + 1 // rows that make the listing truncate
	zfs := -1                                    // lazily: is this directory on ZFS?
	for _, n := range names {
		if n == ".zfs" {
			if zfs < 0 {
				zfs = 0
				if fi, serr := dir.Statfs(); serr != nil || fi.Type == fsx.MagicZFS { // fail closed
					zfs = 1
				}
			}
			if zfs == 1 {
				continue // the snapshot control directory is never listed
			}
		}
		if int64(len(emit)) >= room {
			break
		}
		st, serr := dir.Lstat(n)
		if serr != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			l.prog.Errors++
			continue
		}
		if st.IsDir() {
			if l.subtree {
				subdirs = append(subdirs, n)
			} else {
				emit = append(emit, entry{n, st})
			}
			continue
		}
		if l.passes(n, st) {
			emit = append(emit, entry{n, st})
		}
	}
	return emit, subdirs, nil
}

// passes applies the listing filters to a non-directory.
func (l *lister) passes(name string, st fsx.Stat) bool {
	if l.exts != nil {
		if _, ok := l.exts[cube.Ext([]byte(name))]; !ok {
			return false
		}
	}
	if st.Size < l.params.MinSize {
		return false
	}
	return !l.hasCutoff || st.Mtime <= l.cutoff
}

// relName joins a prefix and a name with "/". Both are raw bytes.
func relName(prefix []byte, name string) []byte {
	if len(prefix) == 0 {
		return []byte(name)
	}
	out := make([]byte, 0, len(prefix)+1+len(name))
	out = append(out, prefix...)
	out = append(out, '/')
	return append(out, name...)
}

func entryType(st fsx.Stat) string {
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return typeFile
	case unix.S_IFDIR:
		return typeDir
	case unix.S_IFLNK:
		return typeSymlink
	}
	return typeOther
}

// add queues one row, or ends the walk with errStop when the cap is full.
func (l *lister) add(ctx context.Context, rel []byte, st fsx.Stat) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.prog.Rows >= int64(l.d.MaxRows) {
		l.prog.Truncated = true
		return errStop
	}
	l.seq++
	l.prog.Rows++
	l.batch = append(l.batch, dbq.InsertListingResultParams{
		JobID: l.job, Seq: l.seq, RelName: rel, Type: entryType(st),
		Size: st.Size, Alloc: st.Blocks * 512, Mtime: st.Mtime, Uid: int64(st.UID),
	})
	if len(l.batch) >= l.d.BatchRows {
		return l.flush(ctx)
	}
	return nil
}

// flush inserts the queued rows in one transaction.
func (l *lister) flush(ctx context.Context) error {
	if len(l.batch) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := l.d.DB.BeginTx(ctx, nil)
	if err != nil {
		return ioErr(ctx, fmt.Errorf("listing: begin: %w", err))
	}
	qt := l.q.WithTx(tx)
	for _, r := range l.batch {
		if err := qt.InsertListingResult(ctx, r); err != nil {
			_ = tx.Rollback()
			return ioErr(ctx, fmt.Errorf("listing: insert: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return ioErr(ctx, fmt.Errorf("listing: commit: %w", err))
	}
	l.batch = l.batch[:0]
	afterFlush()
	return nil
}

// hiddenFlags marks directories the scan did not enter because they are
// excluded or belong to another root; a listing never enters them either.
const hiddenFlags = gen.FlagExcluded | gen.FlagNestedRootSkipped

// hiddenDirs returns the full paths (below the scan root, "/"-joined raw
// bytes) of the hidden directories under the target, loaded once per Run.
func hiddenDirs(ctx context.Context, gdb *sql.DB, target [][]byte) (map[string]struct{}, error) {
	rs, err := gdb.QueryContext(ctx, `SELECT id FROM dirs WHERE flags & ? != 0`, hiddenFlags)
	if err != nil {
		return nil, ioErr(ctx, fmt.Errorf("listing: hidden dirs: %w", err))
	}
	var ids []int64
	for rs.Next() {
		var id int64
		if err := rs.Scan(&id); err != nil {
			_ = rs.Close()
			return nil, ioErr(ctx, fmt.Errorf("listing: hidden dirs: %w", err))
		}
		ids = append(ids, id)
	}
	err = rs.Err()
	_ = rs.Close()
	if err != nil {
		return nil, ioErr(ctx, fmt.Errorf("listing: hidden dirs: %w", err))
	}
	out := make(map[string]struct{})
	for _, id := range ids {
		p, err := gen.DirPath(ctx, gdb, id)
		if err != nil {
			return nil, ioErr(ctx, fmt.Errorf("listing: hidden dirs: %w", err))
		}
		if len(p) > len(target) && prefixEqual(p, target) {
			out[joinSegs(p)] = struct{}{}
		}
	}
	return out, nil
}

func prefixEqual(p, prefix [][]byte) bool {
	for i, s := range prefix {
		if !bytes.Equal(p[i], s) {
			return false
		}
	}
	return true
}

// full is the path below the scan root of entry name in the directory at
// prefix below the target.
func (l *lister) full(prefix []byte, name string) string {
	return joinPath(l.base, string(prefix), name)
}

func joinPath(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}
