// Package gen writes and opens scan generation files: one SQLite
// file per scan at <dataDir>/scans/<root_id>-<scan_id>.db. The scanner builds
// it once with Create and a Writer; afterwards it is immutable and read only
// through OpenReadOnly.
package gen

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// SchemaVersion is the generation file layout version, stored both as
// PRAGMA user_version and in meta.schema_version.
const SchemaVersion = 2

// BatchRows is the number of buffered rows, across all tables, that triggers
// one write transaction.
const BatchRows = 10_000

// MaxErrorRows is the number of error rows stored; later errors are only
// counted.
const MaxErrorRows = 100_000

// maxErrorRows is MaxErrorRows, lowered by tests through export_test.go.
var maxErrorRows int64 = MaxErrorRows

// Directory flags (dirs.flags).
const (
	FlagUnreadable           = 1 << 0
	FlagExcluded             = 1 << 1
	FlagMountpointSkipped    = 1 << 2
	FlagNestedRootSkipped    = 1 << 3
	FlagWritableMountRefused = 1 << 4
	FlagSymlink              = 1 << 5
)

var (
	// ErrNotExist is returned by OpenReadOnly when the file does not exist.
	ErrNotExist = errors.New("gen: generation file does not exist")
	// ErrSchemaVersion is returned by OpenReadOnly when the file is not a
	// usable generation of this SchemaVersion: written with another version,
	// incomplete (no meta row, e.g. a crash leftover) or not a database.
	ErrSchemaVersion = errors.New("gen: generation file schema version mismatch")
)

// Path returns the generation file path for (rootID, scanID). It is the only
// place that path is built; it never takes request input.
func Path(dataDir string, rootID, scanID int64) string {
	return filepath.Join(dataDir, "scans", fmt.Sprintf("%d-%d.db", rootID, scanID))
}

// Dir is one dirs row. pre/post are set later through Writer.SetPrePost.
// ParentID is 0 for the scan root. Dev and Ino hold the stat values
// bit-cast to int64. Mtime is Unix seconds.
type Dir struct {
	ID, ParentID int64
	Name         []byte
	DepthAnchor  int64
	Dev, Ino     int64
	Mtime        int64
	UID, GID     int64
	OwnFiles     int64
	OwnAlloc     int64
	Files        int64
	Dirs         int64
	BytesLogical int64
	BytesAlloc   int64
	Flags        int64
	ErrorCount   int64
	// Pre and Post are the subtree numbers; they are read back by the
	// helpers in reader.go and ignored by Writer.AddDir (see SetPrePost).
	Pre, Post int64
}

// TopFile is one top_files row; Kind is "largest" or "oldest".
type TopFile struct {
	AnchorID    int64
	Kind        string
	Rank        int64
	ParentDirID int64
	Name        []byte
	Size, Alloc int64
	Mtime       int64
	UID         int64
}

// TopExt is one top_exts row.
type TopExt struct {
	DirID        int64
	Ext          string
	Bytes, Files int64
}

// SharedInode is one shared_inodes row: an inode linked from ≥ 2 directories.
type SharedInode struct {
	Dev, Ino    int64
	Size, Alloc int64
	Mtime       int64
	CubeCell    int64
	// WinnerDirID is the dir_id of the winning link. SetSharedCells writes
	// it; AddSharedInode ignores it (the column starts at 0).
	WinnerDirID int64
}

// SharedLink is one shared_links row: a name linking a shared inode, with the
// size, allocation and mtime the walk saw through that name. They are the
// values the walk counted in the link's directory, so the hardlink pass
// subtracts exactly what was added even when the inode changed between the
// lstats of its links.
type SharedLink struct {
	Dev, Ino    int64
	DirID       int64
	Name        []byte
	Size, Alloc int64
	Mtime       int64
}

// Owner is one owners row; Kind is "uid" or "gid".
type Owner struct {
	Kind string
	ID   int64
	Name string
}

// ErrorRow is one errors row; Op is "readdir", "lstat" or "open".
type ErrorRow struct {
	DirID   int64
	Name    []byte
	Op      string
	Errno   int64
	Message string
}

// Meta is the single meta row. Times are stored as Unix seconds UTC; Stats
// is a JSON value ("{}" when empty).
type Meta struct {
	ScanID, RootID        int64
	ScannerVersion        string
	StartedAt, FinishedAt time.Time
	Stats                 json.RawMessage
}

// schemaDDL creates every table of a generation file, without secondary indexes
// (BuildIndexes adds them once the bulk load is done). Conventions follow the
// main DB: STRICT tables, raw-byte names as BLOB, *_at as Unix seconds,
// JSON as TEXT checked with json_valid.
const schemaDDL = `
CREATE TABLE dirs (
    id            INTEGER PRIMARY KEY CHECK (id > 0),
    parent_id     INTEGER NOT NULL,
    name          BLOB    NOT NULL,
    pre           INTEGER,
    post          INTEGER,
    depth_anchor  INTEGER NOT NULL,
    dev           INTEGER NOT NULL,
    ino           INTEGER NOT NULL,
    mtime         INTEGER NOT NULL,
    uid           INTEGER NOT NULL,
    gid           INTEGER NOT NULL,
    own_files     INTEGER NOT NULL,
    own_alloc     INTEGER NOT NULL,
    files         INTEGER NOT NULL,
    dirs          INTEGER NOT NULL,
    bytes_logical INTEGER NOT NULL,
    bytes_alloc   INTEGER NOT NULL,
    flags         INTEGER NOT NULL DEFAULT 0,
    error_count   INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE cube (
    dir_id INTEGER PRIMARY KEY,
    blob   BLOB    NOT NULL
) STRICT;

CREATE TABLE top_files (
    anchor_id     INTEGER NOT NULL,
    kind          TEXT    NOT NULL CHECK (kind IN ('largest', 'oldest')),
    rank          INTEGER NOT NULL,
    parent_dir_id INTEGER NOT NULL,
    name          BLOB    NOT NULL,
    size          INTEGER NOT NULL,
    alloc         INTEGER NOT NULL,
    mtime         INTEGER NOT NULL,
    uid           INTEGER NOT NULL,
    PRIMARY KEY (anchor_id, kind, rank)
) STRICT;

CREATE TABLE top_exts (
    dir_id INTEGER NOT NULL,
    ext    TEXT    NOT NULL,
    bytes  INTEGER NOT NULL,
    files  INTEGER NOT NULL,
    PRIMARY KEY (dir_id, ext)
) STRICT;

CREATE TABLE shared_inodes (
    dev       INTEGER NOT NULL,
    ino       INTEGER NOT NULL,
    size      INTEGER NOT NULL,
    alloc     INTEGER NOT NULL,
    mtime     INTEGER NOT NULL,
    cube_cell INTEGER NOT NULL,
    winner_dir_id INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (dev, ino)
) STRICT;

CREATE TABLE shared_links (
    dev    INTEGER NOT NULL,
    ino    INTEGER NOT NULL,
    dir_id INTEGER NOT NULL,
    name   BLOB    NOT NULL,
    size   INTEGER NOT NULL,
    alloc  INTEGER NOT NULL,
    mtime  INTEGER NOT NULL
) STRICT;

CREATE TABLE owners (
    kind TEXT    NOT NULL CHECK (kind IN ('uid', 'gid')),
    id   INTEGER NOT NULL,
    name TEXT    NOT NULL,
    PRIMARY KEY (kind, id)
) STRICT;

CREATE TABLE errors (
    dir_id  INTEGER NOT NULL,
    name    BLOB    NOT NULL,
    op      TEXT    NOT NULL CHECK (op IN ('readdir', 'lstat', 'open')),
    errno   INTEGER NOT NULL,
    message TEXT    NOT NULL
) STRICT;

CREATE TABLE meta (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    scan_id         INTEGER NOT NULL,
    root_id         INTEGER NOT NULL,
    scanner_version TEXT    NOT NULL,
    schema_version  INTEGER NOT NULL,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER NOT NULL,
    stats           TEXT    NOT NULL CHECK (json_valid(stats))
) STRICT;
`

// indexDDL is run by BuildIndexes after the bulk load.
const indexDDL = `
CREATE UNIQUE INDEX dirs_parent_name ON dirs (parent_id, name);
CREATE INDEX dirs_pre ON dirs (pre);
CREATE INDEX errors_dir ON errors (dir_id);
CREATE INDEX shared_links_dir ON shared_links (dir_id);
CREATE INDEX shared_links_inode ON shared_links (dev, ino);
`
