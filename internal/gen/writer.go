package gen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vinRosso/hogwatch/internal/cube"
	"github.com/vinRosso/hogwatch/internal/db"
)

// writerQuery holds the per-connection settings of a file under construction.
// The file is not live until published, so it trades crash safety for speed;
// Finish fsyncs it once at the end.
const writerQuery = "_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)"

var (
	errClosed = errors.New("gen: writer is closed")
	errNoRow  = errors.New("gen: no such dir")
)

type cubeRow struct {
	dirID int64
	blob  []byte
}

type prePost struct{ dirID, pre, post int64 }

// Writer builds one generation file. It is not safe for concurrent use: the
// scanner drives it from its single writer goroutine.
//
// Every Add* call buffers its row; once BatchRows rows are buffered across all
// tables they are written in one transaction. The file has no rollback
// journal, so after any error the Writer refuses further work and the caller
// must Abort it.
type Writer struct {
	path string
	conn *sql.DB

	dirs         []Dir
	cubes        []cubeRow
	topFiles     []TopFile
	topExts      []TopExt
	sharedInodes []SharedInode
	sharedLinks  []SharedLink
	owners       []Owner
	errs         []ErrorRow
	prePosts     []prePost
	buffered     int

	errStored, errDropped int64

	err      error // sticky: the first failure
	closed   bool
	finished bool
}

// File and directory modes: the scanner writes generation files
// as root with group 10001 (setgid ./data, umask 002) and the web, uid 10001,
// reads them through the group.
const (
	fileMode = 0o640
	dirMode  = 0o770
)

// Create creates a new generation file at path, creating its directory
// (./data/scans) if needed. It refuses an existing file (O_EXCL), applies the
// bulk-load pragmas, creates the tables without secondary indexes and sets
// user_version to SchemaVersion. The file is mode 0640 whatever the umask.
func Create(ctx context.Context, path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return nil, fmt.Errorf("gen: create %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, fileMode) //nolint:gosec // path comes from gen.Path, never from request input
	if err != nil {
		return nil, fmt.Errorf("gen: create %s: %w", path, err)
	}
	// Chmod: the open mode is filtered by the umask.
	if err := f.Chmod(fileMode); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("gen: create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("gen: create %s: %w", path, err)
	}
	w := &Writer{path: path}
	if err := w.init(ctx); err != nil {
		_ = w.Abort()
		return nil, fmt.Errorf("gen: create %s: %w", path, err)
	}
	return w, nil
}

func (w *Writer) init(ctx context.Context) error {
	uri, err := db.FileURI(w.path, writerQuery)
	if err != nil {
		return err
	}
	conn, err := sql.Open("sqlite", uri)
	if err != nil {
		return err
	}
	w.conn = conn
	// One connection: the pragmas and the schema live on a single handle,
	// and nothing else ever writes the file.
	conn.SetMaxOpenConns(1)
	// The index sort spills to temp files. The scanner runs in a read-only
	// container with no writable /tmp, so keep them beside the generation
	// file, the one place that is writable by construction. The setting is
	// process-global, which holds while one writer at a time uses ./data/scans.
	tmp := strings.ReplaceAll(filepath.Dir(w.path), "'", "''")
	if _, err := conn.ExecContext(ctx, "PRAGMA temp_store_directory = '"+tmp+"'"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, schemaDDL); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion))
	return err
}

func (w *Writer) usable() error {
	switch {
	case w.err != nil:
		return w.err
	case w.closed:
		return errClosed
	}
	return nil
}

// clone copies a caller's byte slice, which the caller may reuse, and maps
// nil to an empty BLOB so NOT NULL name columns accept it.
func clone(b []byte) []byte { return append(make([]byte, 0, len(b)), b...) }

// added counts one buffered row and writes the batch once it is full.
func (w *Writer) added(ctx context.Context) error {
	w.buffered++
	if w.buffered >= BatchRows {
		return w.Flush(ctx)
	}
	return nil
}

// AddDir buffers one dirs row.
func (w *Writer) AddDir(ctx context.Context, d Dir) error {
	if err := w.usable(); err != nil {
		return err
	}
	d.Name = clone(d.Name)
	w.dirs = append(w.dirs, d)
	return w.added(ctx)
}

// AddCube buffers the cube blob of one directory.
func (w *Writer) AddCube(ctx context.Context, dirID int64, b cube.Blob) error {
	if err := w.usable(); err != nil {
		return err
	}
	w.cubes = append(w.cubes, cubeRow{dirID: dirID, blob: clone(b)})
	return w.added(ctx)
}

// AddTopFile buffers one top_files row.
func (w *Writer) AddTopFile(ctx context.Context, f TopFile) error {
	if err := w.usable(); err != nil {
		return err
	}
	if f.Kind != "largest" && f.Kind != "oldest" {
		return fmt.Errorf("gen: top file kind %q", f.Kind)
	}
	f.Name = clone(f.Name)
	w.topFiles = append(w.topFiles, f)
	return w.added(ctx)
}

// AddTopExt buffers one top_exts row.
func (w *Writer) AddTopExt(ctx context.Context, e TopExt) error {
	if err := w.usable(); err != nil {
		return err
	}
	w.topExts = append(w.topExts, e)
	return w.added(ctx)
}

// AddSharedInode buffers one shared_inodes row. A second row for the same
// (dev, ino) is ignored: the walk meets one inode once per link. Which link's
// row wins is arbitrary, so Size, Alloc, Mtime and CubeCell of the row are
// placeholders until SetSharedCells overwrites them from the winning link.
func (w *Writer) AddSharedInode(ctx context.Context, s SharedInode) error {
	if err := w.usable(); err != nil {
		return err
	}
	w.sharedInodes = append(w.sharedInodes, s)
	return w.added(ctx)
}

// AddSharedLink buffers one shared_links row.
func (w *Writer) AddSharedLink(ctx context.Context, l SharedLink) error {
	if err := w.usable(); err != nil {
		return err
	}
	l.Name = clone(l.Name)
	w.sharedLinks = append(w.sharedLinks, l)
	return w.added(ctx)
}

// AddOwner buffers one owners row.
func (w *Writer) AddOwner(ctx context.Context, o Owner) error {
	if err := w.usable(); err != nil {
		return err
	}
	if o.Kind != "uid" && o.Kind != "gid" {
		return fmt.Errorf("gen: owner kind %q", o.Kind)
	}
	w.owners = append(w.owners, o)
	return w.added(ctx)
}

// AddError buffers one errors row. Past MaxErrorRows stored rows it only
// counts the error; see ErrorCounts.
func (w *Writer) AddError(ctx context.Context, e ErrorRow) error {
	if err := w.usable(); err != nil {
		return err
	}
	switch e.Op {
	case "readdir", "lstat", "open":
	default:
		return fmt.Errorf("gen: error op %q", e.Op)
	}
	if w.errStored >= maxErrorRows {
		w.errDropped++
		return nil
	}
	w.errStored++
	e.Name = clone(e.Name)
	w.errs = append(w.errs, e)
	return w.added(ctx)
}

// ErrorCounts reports how many error rows were stored and how many were only
// counted because the cap was reached.
func (w *Writer) ErrorCounts() (stored, dropped int64) { return w.errStored, w.errDropped }

// SetPrePost buffers the DFS numbering of one directory, applied as a batched
// UPDATE after the batch's inserts. Flush fails if dirID was never added.
func (w *Writer) SetPrePost(ctx context.Context, dirID, pre, post int64) error {
	if err := w.usable(); err != nil {
		return err
	}
	w.prePosts = append(w.prePosts, prePost{dirID: dirID, pre: pre, post: post})
	return w.added(ctx)
}

// execAll runs q once per row through one prepared statement.
func execAll[T any](ctx context.Context, tx *sql.Tx, q string, rows []T, args func(T) []any) error {
	if len(rows) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, args(r)...); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) writeBatch(ctx context.Context, tx *sql.Tx) error {
	steps := []func() error{
		func() error {
			return execAll(ctx, tx, `INSERT INTO dirs (id, parent_id, name, depth_anchor, dev, ino, mtime, uid, gid,
				own_files, own_alloc, files, dirs, bytes_logical, bytes_alloc, flags, error_count)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, w.dirs, func(d Dir) []any {
				return []any{d.ID, d.ParentID, d.Name, d.DepthAnchor, d.Dev, d.Ino, d.Mtime, d.UID, d.GID,
					d.OwnFiles, d.OwnAlloc, d.Files, d.Dirs, d.BytesLogical, d.BytesAlloc, d.Flags, d.ErrorCount}
			})
		},
		func() error {
			return execAll(ctx, tx, `INSERT INTO cube (dir_id, blob) VALUES (?, ?)`, w.cubes,
				func(c cubeRow) []any { return []any{c.dirID, c.blob} })
		},
		func() error {
			return execAll(ctx, tx, `INSERT INTO top_files (anchor_id, kind, rank, parent_dir_id, name, size, alloc, mtime, uid)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, w.topFiles, func(f TopFile) []any {
				return []any{f.AnchorID, f.Kind, f.Rank, f.ParentDirID, f.Name, f.Size, f.Alloc, f.Mtime, f.UID}
			})
		},
		func() error {
			return execAll(ctx, tx, `INSERT INTO top_exts (dir_id, ext, bytes, files) VALUES (?, ?, ?, ?)`, w.topExts,
				func(e TopExt) []any { return []any{e.DirID, e.Ext, e.Bytes, e.Files} })
		},
		func() error {
			return execAll(ctx, tx, `INSERT OR IGNORE INTO shared_inodes (dev, ino, size, alloc, mtime, cube_cell)
				VALUES (?, ?, ?, ?, ?, ?)`, w.sharedInodes, func(s SharedInode) []any {
				return []any{s.Dev, s.Ino, s.Size, s.Alloc, s.Mtime, s.CubeCell}
			})
		},
		func() error {
			return execAll(ctx, tx, `INSERT INTO shared_links (dev, ino, dir_id, name, size, alloc, mtime) VALUES (?, ?, ?, ?, ?, ?, ?)`, w.sharedLinks,
				func(l SharedLink) []any { return []any{l.Dev, l.Ino, l.DirID, l.Name, l.Size, l.Alloc, l.Mtime} })
		},
		func() error {
			return execAll(ctx, tx, `INSERT INTO owners (kind, id, name) VALUES (?, ?, ?)`, w.owners,
				func(o Owner) []any { return []any{o.Kind, o.ID, o.Name} })
		},
		func() error {
			return execAll(ctx, tx, `INSERT INTO errors (dir_id, name, op, errno, message) VALUES (?, ?, ?, ?, ?)`, w.errs,
				func(e ErrorRow) []any { return []any{e.DirID, e.Name, e.Op, e.Errno, e.Message} })
		},
		func() error { return w.writePrePost(ctx, tx) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// writePrePost runs after the inserts, so it can number dirs of this batch.
func (w *Writer) writePrePost(ctx context.Context, tx *sql.Tx) error {
	if len(w.prePosts) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE dirs SET pre = ?, post = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, p := range w.prePosts {
		res, err := stmt.ExecContext(ctx, p.pre, p.post, p.dirID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("%w: SetPrePost(%d)", errNoRow, p.dirID)
		}
	}
	return nil
}

func (w *Writer) resetBuffers() {
	clear(w.dirs)
	clear(w.cubes)
	clear(w.topFiles)
	clear(w.sharedLinks)
	clear(w.errs)
	w.dirs, w.cubes, w.topFiles, w.topExts = w.dirs[:0], w.cubes[:0], w.topFiles[:0], w.topExts[:0]
	w.sharedInodes, w.sharedLinks, w.owners = w.sharedInodes[:0], w.sharedLinks[:0], w.owners[:0]
	w.errs, w.prePosts = w.errs[:0], w.prePosts[:0]
	w.buffered = 0
}

// fail records the first error; the Writer is unusable afterwards.
func (w *Writer) fail(err error) error {
	w.err = fmt.Errorf("gen: write %s: %w", w.path, err)
	return w.err
}

// Flush writes every buffered row in one transaction.
func (w *Writer) Flush(ctx context.Context) error {
	if err := w.usable(); err != nil {
		return err
	}
	if w.buffered == 0 {
		return nil
	}
	tx, err := w.conn.BeginTx(ctx, nil)
	if err != nil {
		return w.fail(err)
	}
	if err := w.writeBatch(ctx, tx); err != nil {
		_ = tx.Rollback()
		return w.fail(err)
	}
	if err := tx.Commit(); err != nil {
		return w.fail(err)
	}
	w.resetBuffers()
	return nil
}

// BuildIndexes flushes, then creates the secondary indexes. It fails when two
// dirs share a parent and a name.
func (w *Writer) BuildIndexes(ctx context.Context) error {
	if err := w.Flush(ctx); err != nil {
		return err
	}
	if _, err := w.conn.ExecContext(ctx, indexDDL); err != nil {
		return w.fail(fmt.Errorf("build indexes: %w", err))
	}
	return nil
}

// Finish flushes, writes the single meta row, closes the database and fsyncs
// the file and its directory. Afterwards the directory holds only the file,
// and Abort is a no-op.
func (w *Writer) Finish(ctx context.Context, m Meta) error {
	if err := w.Flush(ctx); err != nil {
		return err
	}
	stats := m.Stats
	if len(stats) == 0 {
		stats = json.RawMessage("{}")
	}
	if !json.Valid(stats) {
		return w.fail(errors.New("meta stats is not valid JSON"))
	}
	if _, err := w.conn.ExecContext(ctx, `INSERT INTO meta (id, scan_id, root_id, scanner_version, schema_version,
		started_at, finished_at, stats) VALUES (1, ?, ?, ?, ?, ?, ?, ?)`,
		m.ScanID, m.RootID, m.ScannerVersion, SchemaVersion,
		m.StartedAt.Unix(), m.FinishedAt.Unix(), string(stats)); err != nil {
		return w.fail(fmt.Errorf("meta: %w", err))
	}
	w.closed = true
	if err := w.conn.Close(); err != nil {
		return w.fail(fmt.Errorf("close: %w", err))
	}
	if err := syncPath(w.path); err != nil {
		return w.fail(err)
	}
	if err := syncPath(filepath.Dir(w.path)); err != nil {
		return w.fail(err)
	}
	w.finished = true
	return nil
}

// syncPath fsyncs a file or directory.
func syncPath(path string) error {
	f, err := os.Open(path) //nolint:gosec // the generation file or its directory, from gen.Path
	if err != nil {
		return fmt.Errorf("fsync: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync %s: %w", path, err)
	}
	return f.Close()
}

// Abort closes the database and removes the file. After a successful Finish
// it does nothing, so callers can defer it.
func (w *Writer) Abort() error {
	if w.finished {
		return nil
	}
	if !w.closed && w.conn != nil {
		_ = w.conn.Close()
	}
	w.closed = true
	if err := os.Remove(w.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gen: abort %s: %w", w.path, err)
	}
	return nil
}
