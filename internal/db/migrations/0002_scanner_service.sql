-- 0002_scanner_service: tables and columns the scanner service (M4) needs.
-- scans.job_id links a scan to its job; scans.error holds a failure message.
-- config_issues holds the scan-derived rows of GET /admin/config-issues;
-- path_stats holds per-assigned-path totals and the ZFS numbers of a path
-- that is a dataset mountpoint (the web has no ./zfs mount).

-- +goose Up

ALTER TABLE scans ADD COLUMN job_id INTEGER REFERENCES jobs (id) ON DELETE SET NULL;
ALTER TABLE scans ADD COLUMN error TEXT;

CREATE TABLE config_issues (
    id         INTEGER PRIMARY KEY,
    root_id    INTEGER NOT NULL REFERENCES scan_roots (id) ON DELETE CASCADE,
    scan_id    INTEGER REFERENCES scans (id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL CHECK (kind IN (
        'cross_root_duplicate', 'writable_mount', 'eio', 'invalid_schedule')),
    details    TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(details)),
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX config_issues_root_kind ON config_issues (root_id, kind);

CREATE TABLE path_stats (
    scan_id               INTEGER NOT NULL REFERENCES scans (id) ON DELETE CASCADE,
    user_path_id          INTEGER NOT NULL REFERENCES user_paths (id) ON DELETE CASCADE,
    found                 INTEGER NOT NULL CHECK (found IN (0, 1)),
    redundant             INTEGER NOT NULL CHECK (redundant IN (0, 1)),
    bytes_logical         INTEGER NOT NULL,
    bytes_alloc           INTEGER NOT NULL,
    files                 INTEGER NOT NULL,
    shared_hardlink_bytes INTEGER NOT NULL,
    zfs_dataset           TEXT,
    zfs_used              INTEGER,
    zfs_avail             INTEGER,
    zfs_quota             INTEGER,
    zfs_used_snapshots    INTEGER,
    PRIMARY KEY (scan_id, user_path_id)
) STRICT;

-- +goose Down

DROP TABLE path_stats;
DROP TABLE config_issues;
ALTER TABLE scans DROP COLUMN error;
ALTER TABLE scans DROP COLUMN job_id;
