package gen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/vinRosso/hogwatch/internal/db"
)

// readerQuery opens a finished generation file read-only. immutable=1 tells
// SQLite the file never changes, so it takes no locks and creates no
// journal or shm file next to it.
const readerQuery = "mode=ro&immutable=1"

// OpenReadOnly opens a finished generation file and returns its meta row.
//
// A missing file gives ErrNotExist: it is checked before opening, so SQLite
// never creates one, and again when the first query fails, so a file deleted
// in between (the previous generation is deleted after a switch) gives
// ErrNotExist too. A file that is not a usable generation of this
// SchemaVersion gives ErrSchemaVersion: another version, a crash leftover
// without its meta row, or not a SQLite database at all. Both mean the same
// to a caller (the file cannot be served; use another generation), so they
// share one error and the message says which.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, Meta, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, Meta{}, fmt.Errorf("%w: %s", ErrNotExist, path)
		}
		return nil, Meta{}, fmt.Errorf("gen: open %s: %w", path, err)
	}
	afterStat()
	uri, err := db.FileURI(path, readerQuery)
	if err != nil {
		return nil, Meta{}, fmt.Errorf("gen: open %s: %w", path, err)
	}
	d, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, Meta{}, fmt.Errorf("gen: open %s: %w", path, err)
	}
	m, err := readMeta(ctx, d)
	if err != nil {
		_ = d.Close()
		if _, serr := os.Stat(path); errors.Is(serr, fs.ErrNotExist) {
			return nil, Meta{}, fmt.Errorf("%w: %s", ErrNotExist, path)
		}
		return nil, Meta{}, fmt.Errorf("gen: open %s: %w", path, err)
	}
	return d, m, nil
}

// unusable maps the errors of a file that is not a complete generation to
// ErrSchemaVersion and returns any other error unchanged.
func unusable(err error) error {
	var se *sqlite.Error
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: incomplete, no meta row", ErrSchemaVersion)
	case errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_NOTADB:
		return fmt.Errorf("%w: not a database", ErrSchemaVersion)
	case strings.Contains(err.Error(), "no such table"):
		return fmt.Errorf("%w: %v", ErrSchemaVersion, err)
	}
	return err
}

func readMeta(ctx context.Context, d *sql.DB) (Meta, error) {
	var uv int64
	if err := d.QueryRowContext(ctx, "PRAGMA user_version").Scan(&uv); err != nil {
		return Meta{}, unusable(err)
	}
	if uv != SchemaVersion {
		return Meta{}, fmt.Errorf("%w: user_version %d, want %d", ErrSchemaVersion, uv, SchemaVersion)
	}
	var (
		m              Meta
		schema         int64
		started, ended int64
		stats          string
	)
	err := d.QueryRowContext(ctx, `SELECT scan_id, root_id, scanner_version, schema_version,
		started_at, finished_at, stats FROM meta WHERE id = 1`).
		Scan(&m.ScanID, &m.RootID, &m.ScannerVersion, &schema, &started, &ended, &stats)
	if err != nil {
		return Meta{}, fmt.Errorf("meta: %w", unusable(err))
	}
	if schema != SchemaVersion {
		return Meta{}, fmt.Errorf("%w: meta.schema_version %d, want %d", ErrSchemaVersion, schema, SchemaVersion)
	}
	m.StartedAt = time.Unix(started, 0).UTC()
	m.FinishedAt = time.Unix(ended, 0).UTC()
	m.Stats = json.RawMessage(stats)
	return m, nil
}

// afterStat is a test seam run between OpenReadOnly's Stat and its open.
var afterStat = func() {}

// ErrNoDir is returned by Lookup, DirByID and DirPath for a directory the
// generation does not hold. It is the only signal for "not there": a caller
// maps it to the same 404 as a node outside the caller's scope.
var ErrNoDir = errors.New("gen: no such directory")

// rootID is the id of the scan root's dirs row.
const rootID = 1

const dirColumns = `id, parent_id, name, depth_anchor, dev, ino, mtime, uid, gid,
	own_files, own_alloc, files, dirs, bytes_logical, bytes_alloc, flags, error_count,
	coalesce(pre, 0), coalesce(post, 0)`

type rowScanner interface{ Scan(dest ...any) error }

func scanDir(r rowScanner) (Dir, error) {
	var d Dir
	err := r.Scan(&d.ID, &d.ParentID, &d.Name, &d.DepthAnchor, &d.Dev, &d.Ino, &d.Mtime, &d.UID, &d.GID,
		&d.OwnFiles, &d.OwnAlloc, &d.Files, &d.Dirs, &d.BytesLogical, &d.BytesAlloc, &d.Flags, &d.ErrorCount,
		&d.Pre, &d.Post)
	return d, err
}

// Lookup resolves a path of raw name segments, below the scan root, to its
// dirs row by walking (parent_id, name) from id 1. No segments gives the root.
// A missing segment gives ErrNoDir. It never touches the filesystem: names
// are compared as bytes against the generation only.
func Lookup(ctx context.Context, d *sql.DB, segments [][]byte) (Dir, error) {
	id := int64(rootID)
	for _, seg := range segments {
		err := d.QueryRowContext(ctx, `SELECT id FROM dirs WHERE parent_id = ? AND name = ?`, id, seg).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return Dir{}, ErrNoDir
		case err != nil:
			return Dir{}, fmt.Errorf("gen: lookup: %w", err)
		}
	}
	return DirByID(ctx, d, id)
}

// DirByID returns the dirs row with the given id, or ErrNoDir.
func DirByID(ctx context.Context, d *sql.DB, id int64) (Dir, error) {
	r, err := scanDir(d.QueryRowContext(ctx, `SELECT `+dirColumns+` FROM dirs WHERE id = ?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Dir{}, ErrNoDir
	case err != nil:
		return Dir{}, fmt.Errorf("gen: dir %d: %w", id, err)
	}
	return r, nil
}

// DirPath returns the name segments from the scan root down to dir id: empty
// for the root, ErrNoDir for an unknown id. It is the inverse of Lookup.
func DirPath(ctx context.Context, d *sql.DB, id int64) ([][]byte, error) {
	var rev [][]byte
	for steps := 0; ; steps++ {
		var parent int64
		var name []byte
		err := d.QueryRowContext(ctx, `SELECT parent_id, name FROM dirs WHERE id = ?`, id).Scan(&parent, &name)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, ErrNoDir
		case err != nil:
			return nil, fmt.Errorf("gen: dir path %d: %w", id, err)
		}
		if parent == 0 { // the scan root: its name is not part of the path
			break
		}
		if steps > 1<<20 {
			return nil, fmt.Errorf("gen: dir %d: parent chain does not end", id)
		}
		rev = append(rev, name)
		id = parent
	}
	slices.Reverse(rev)
	return rev, nil
}

// ForEachDir calls fn for every dirs row in id order, with Pre and Post
// filled. It stops at the first error fn returns and returns it.
func ForEachDir(ctx context.Context, d *sql.DB, fn func(Dir) error) error {
	rs, err := d.QueryContext(ctx, `SELECT `+dirColumns+` FROM dirs ORDER BY id`)
	if err != nil {
		return fmt.Errorf("gen: list dirs: %w", err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		r, err := scanDir(rs)
		if err != nil {
			return fmt.Errorf("gen: list dirs: %w", err)
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	if err := rs.Err(); err != nil {
		return fmt.Errorf("gen: list dirs: %w", err)
	}
	return nil
}
