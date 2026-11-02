-- 0001_init: main DB schema.
-- Conventions (M2 plan, Global Constraints): every table is STRICT;
-- timestamps are INTEGER Unix seconds UTC; booleans are INTEGER 0/1;
-- JSON columns are TEXT checked with json_valid; raw-byte names are BLOB.

-- +goose Up

-- identity and configuration

CREATE TABLE users (
    id             INTEGER PRIMARY KEY,
    username       TEXT    NOT NULL UNIQUE,
    display_name   TEXT    NOT NULL DEFAULT '',
    email          TEXT    UNIQUE COLLATE NOCASE,
    password_hash  TEXT,
    oidc_issuer    TEXT,
    oidc_subject   TEXT,
    oidc_linked_at INTEGER,
    admin_local    INTEGER NOT NULL DEFAULT 0 CHECK (admin_local IN (0, 1)),
    admin_oidc     INTEGER NOT NULL DEFAULT 0 CHECK (admin_oidc IN (0, 1)),
    can_list_files INTEGER NOT NULL DEFAULT 0 CHECK (can_list_files IN (0, 1)),
    quota_bytes    INTEGER,
    quota_basis    TEXT    NOT NULL DEFAULT 'allocated' CHECK (quota_basis IN ('allocated', 'logical')),
    disabled       INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
    created_at     INTEGER NOT NULL,
    last_login_at  INTEGER,
    UNIQUE (oidc_issuer, oidc_subject)
) STRICT;

CREATE TABLE sessions (
    id_hash      BLOB    NOT NULL PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    oidc_admin   INTEGER NOT NULL DEFAULT 0 CHECK (oidc_admin IN (0, 1)),
    ip           TEXT    NOT NULL DEFAULT '',
    user_agent   TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX sessions_user_id ON sessions (user_id);
CREATE INDEX sessions_expires_at ON sessions (expires_at);

-- current_scan_id references scans, created below: SQLite resolves foreign
-- keys when a statement runs, so the forward reference is valid.
CREATE TABLE scan_roots (
    id              INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL,
    container_path  TEXT    NOT NULL,
    host_path       TEXT    NOT NULL,
    cross_fs        INTEGER NOT NULL DEFAULT 0 CHECK (cross_fs IN (0, 1)),
    excludes        TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(excludes)),
    schedule        TEXT    NOT NULL DEFAULT '',
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    zfs_dataset     TEXT,
    current_scan_id INTEGER REFERENCES scans (id) ON DELETE SET NULL
) STRICT;

CREATE TABLE user_paths (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    root_id    INTEGER NOT NULL REFERENCES scan_roots (id) ON DELETE CASCADE,
    rel_path   BLOB    NOT NULL,
    created_by INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (root_id, rel_path)
) STRICT;

CREATE INDEX user_paths_user_id ON user_paths (user_id);

-- Extensions are lowercase without the dot. An unmapped extension is
-- other, which has no rows.
CREATE TABLE file_categories (
    ext      TEXT NOT NULL PRIMARY KEY,
    category TEXT NOT NULL CHECK (category IN (
        'video', 'audio', 'image', 'archive', 'disk-image', 'document', 'code', 'other'))
) STRICT;

CREATE TABLE scanner_status (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    heartbeat_at     INTEGER NOT NULL,
    version          TEXT    NOT NULL,
    available_mounts TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(available_mounts))
) STRICT;

-- actor_id has no foreign key: the audit trail outlives the users it names.
CREATE TABLE audit_log (
    id       INTEGER PRIMARY KEY,
    ts       INTEGER NOT NULL,
    actor_id INTEGER,
    action   TEXT    NOT NULL,
    target   TEXT    NOT NULL DEFAULT '',
    details  TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(details))
) STRICT;

CREATE TABLE settings (
    key   TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

-- jobs, scans, listings

CREATE TABLE jobs (
    id           INTEGER PRIMARY KEY,
    kind         TEXT    NOT NULL CHECK (kind IN ('scan', 'listing', 'cancel')),
    root_id      INTEGER NOT NULL REFERENCES scan_roots (id) ON DELETE CASCADE,
    target_node  TEXT,
    params       TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(params)),
    requested_by INTEGER REFERENCES users (id) ON DELETE SET NULL,
    status       TEXT    NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued', 'running', 'done', 'failed', 'cancelled')),
    progress     TEXT    CHECK (json_valid(progress)),
    error        TEXT,
    created_at   INTEGER NOT NULL,
    started_at   INTEGER,
    finished_at  INTEGER,
    expires_at   INTEGER
) STRICT;

CREATE INDEX jobs_status_kind_id ON jobs (status, kind, id);

CREATE TABLE scans (
    id            INTEGER PRIMARY KEY,
    root_id       INTEGER NOT NULL REFERENCES scan_roots (id) ON DELETE CASCADE,
    file          TEXT    NOT NULL,
    status        TEXT    NOT NULL,
    trigger       TEXT    NOT NULL CHECK (trigger IN ('schedule', 'manual')),
    started_at    INTEGER NOT NULL,
    finished_at   INTEGER,
    dirs          INTEGER NOT NULL DEFAULT 0,
    files         INTEGER NOT NULL DEFAULT 0,
    bytes_logical INTEGER NOT NULL DEFAULT 0,
    bytes_alloc   INTEGER NOT NULL DEFAULT 0,
    error_count   INTEGER NOT NULL DEFAULT 0,
    top_n_rows    INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX scans_root_id_finished_at ON scans (root_id, finished_at);

-- Rows are deleted with their job once expires_at passes (default 1 h).
CREATE TABLE listing_results (
    job_id   INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    seq      INTEGER NOT NULL,
    rel_name BLOB    NOT NULL,
    type     TEXT    NOT NULL,
    size     INTEGER NOT NULL,
    alloc    INTEGER NOT NULL,
    mtime    INTEGER NOT NULL,
    uid      INTEGER NOT NULL,
    PRIMARY KEY (job_id, seq)
) STRICT;

-- totals and history

-- user_id has no foreign key: usage history outlives the users it names.
CREATE TABLE usage (
    scan_id               INTEGER NOT NULL REFERENCES scans (id) ON DELETE CASCADE,
    user_id               INTEGER NOT NULL,
    root_id               INTEGER NOT NULL,
    bytes_logical         INTEGER NOT NULL,
    bytes_alloc           INTEGER NOT NULL,
    files                 INTEGER NOT NULL,
    shared_hardlink_bytes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (scan_id, user_id)
) STRICT;

CREATE TABLE mount_stats (
    scan_id            INTEGER NOT NULL PRIMARY KEY REFERENCES scans (id) ON DELETE CASCADE,
    root_id            INTEGER NOT NULL,
    fs_total           INTEGER NOT NULL,
    fs_free            INTEGER NOT NULL,
    zfs_used           INTEGER,
    zfs_avail          INTEGER,
    zfs_quota          INTEGER,
    zfs_used_snapshots INTEGER
) STRICT;

-- user_id has no foreign key: history outlives the users it names.
CREATE TABLE history_totals (
    ts            INTEGER NOT NULL,
    scope         TEXT    NOT NULL CHECK (scope IN ('user', 'mount', 'user_mount')),
    user_id       INTEGER,
    root_id       INTEGER REFERENCES scan_roots (id) ON DELETE CASCADE,
    bytes_logical INTEGER NOT NULL,
    bytes_alloc   INTEGER NOT NULL,
    files         INTEGER NOT NULL,
    tier          TEXT    NOT NULL CHECK (tier IN ('raw', 'weekly', 'monthly'))
) STRICT;

CREATE INDEX history_totals_scope_user_root_ts ON history_totals (scope, user_id, root_id, ts);

-- path_hash = xxhash64(root_id || rel_path), stored as a signed 64-bit integer.
CREATE TABLE history_dirs (
    ts          INTEGER NOT NULL,
    root_id     INTEGER NOT NULL REFERENCES scan_roots (id) ON DELETE CASCADE,
    path_hash   INTEGER NOT NULL,
    bytes_alloc INTEGER NOT NULL,
    files       INTEGER NOT NULL,
    tier        TEXT    NOT NULL CHECK (tier IN ('raw', 'weekly', 'monthly'))
) STRICT;

CREATE INDEX history_dirs_root_hash_ts ON history_dirs (root_id, path_hash, ts);

CREATE TABLE history_paths (
    path_hash INTEGER PRIMARY KEY,
    root_id   INTEGER NOT NULL REFERENCES scan_roots (id) ON DELETE CASCADE,
    rel_path  BLOB    NOT NULL
) STRICT;

-- Seed file_categories. ts maps to video only (MPEG-TS): large .ts files
-- matter more for disk usage than TypeScript sources.

INSERT INTO file_categories (ext, category) VALUES
    ('mp4', 'video'),
    ('mkv', 'video'),
    ('avi', 'video'),
    ('mov', 'video'),
    ('wmv', 'video'),
    ('flv', 'video'),
    ('webm', 'video'),
    ('m4v', 'video'),
    ('mpg', 'video'),
    ('mpeg', 'video'),
    ('ts', 'video'),
    ('m2ts', 'video'),
    ('vob', 'video'),
    ('3gp', 'video');

INSERT INTO file_categories (ext, category) VALUES
    ('mp3', 'audio'),
    ('flac', 'audio'),
    ('wav', 'audio'),
    ('aac', 'audio'),
    ('ogg', 'audio'),
    ('opus', 'audio'),
    ('m4a', 'audio'),
    ('wma', 'audio'),
    ('aiff', 'audio'),
    ('ape', 'audio');

INSERT INTO file_categories (ext, category) VALUES
    ('jpg', 'image'),
    ('jpeg', 'image'),
    ('png', 'image'),
    ('gif', 'image'),
    ('bmp', 'image'),
    ('tif', 'image'),
    ('tiff', 'image'),
    ('webp', 'image'),
    ('heic', 'image'),
    ('heif', 'image'),
    ('raw', 'image'),
    ('cr2', 'image'),
    ('cr3', 'image'),
    ('nef', 'image'),
    ('arw', 'image'),
    ('dng', 'image'),
    ('svg', 'image'),
    ('psd', 'image');

INSERT INTO file_categories (ext, category) VALUES
    ('zip', 'archive'),
    ('rar', 'archive'),
    ('7z', 'archive'),
    ('tar', 'archive'),
    ('gz', 'archive'),
    ('tgz', 'archive'),
    ('bz2', 'archive'),
    ('xz', 'archive'),
    ('zst', 'archive'),
    ('lz4', 'archive'),
    ('lzma', 'archive'),
    ('cab', 'archive');

INSERT INTO file_categories (ext, category) VALUES
    ('iso', 'disk-image'),
    ('img', 'disk-image'),
    ('vmdk', 'disk-image'),
    ('vdi', 'disk-image'),
    ('qcow2', 'disk-image'),
    ('vhd', 'disk-image'),
    ('vhdx', 'disk-image'),
    ('dmg', 'disk-image');

INSERT INTO file_categories (ext, category) VALUES
    ('pdf', 'document'),
    ('doc', 'document'),
    ('docx', 'document'),
    ('xls', 'document'),
    ('xlsx', 'document'),
    ('ppt', 'document'),
    ('pptx', 'document'),
    ('odt', 'document'),
    ('ods', 'document'),
    ('odp', 'document'),
    ('rtf', 'document'),
    ('txt', 'document'),
    ('md', 'document'),
    ('epub', 'document'),
    ('csv', 'document');

INSERT INTO file_categories (ext, category) VALUES
    ('go', 'code'),
    ('c', 'code'),
    ('h', 'code'),
    ('cpp', 'code'),
    ('hpp', 'code'),
    ('rs', 'code'),
    ('py', 'code'),
    ('js', 'code'),
    ('java', 'code'),
    ('kt', 'code'),
    ('rb', 'code'),
    ('php', 'code'),
    ('sh', 'code'),
    ('cs', 'code'),
    ('swift', 'code'),
    ('sql', 'code'),
    ('json', 'code'),
    ('yaml', 'code'),
    ('yml', 'code'),
    ('toml', 'code'),
    ('html', 'code'),
    ('css', 'code');

-- +goose Down

DROP TABLE history_paths;
DROP TABLE history_dirs;
DROP TABLE history_totals;
DROP TABLE mount_stats;
DROP TABLE usage;
DROP TABLE listing_results;
DROP TABLE scans;
DROP TABLE jobs;
DROP TABLE settings;
DROP TABLE audit_log;
DROP TABLE scanner_status;
DROP TABLE file_categories;
DROP TABLE user_paths;
DROP TABLE scan_roots;
DROP TABLE sessions;
DROP TABLE users;
