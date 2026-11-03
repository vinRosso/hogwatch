-- Read queries of the user API (M7).

-- name: ListRootsForUser :many
SELECT r.id, r.name, r.host_path, r.current_scan_id FROM scan_roots AS r
WHERE EXISTS (SELECT 1 FROM user_paths AS p WHERE p.root_id = r.id AND p.user_id = sqlc.arg(user_id))
ORDER BY r.id;

-- name: ListRootsAll :many
SELECT id, name, host_path, current_scan_id FROM scan_roots ORDER BY id;

-- name: RootScanning :one
SELECT CAST(EXISTS (SELECT 1 FROM scans WHERE root_id = sqlc.arg(root_id) AND status = 'running') AS INTEGER);

-- name: GetMountStats :one
SELECT * FROM mount_stats WHERE scan_id = sqlc.arg(scan_id);

-- name: ListUserPathsForUser :many
SELECT p.id, p.root_id, p.rel_path, r.host_path, r.current_scan_id
FROM user_paths AS p JOIN scan_roots AS r ON r.id = p.root_id
WHERE p.user_id = sqlc.arg(user_id) ORDER BY p.id;

-- ListUsageForUser reads the usage rows of each root current scan.
-- name: ListUsageForUser :many
SELECT u.root_id, r.name AS root_name, u.scan_id, u.bytes_logical, u.bytes_alloc, u.files, u.shared_hardlink_bytes
FROM usage AS u JOIN scan_roots AS r ON r.id = u.root_id AND r.current_scan_id = u.scan_id
WHERE u.user_id = sqlc.arg(user_id) ORDER BY u.root_id;

-- ListPathStatsForUser keeps a path without a stats row (never scanned,
-- or no current scan) with found = 0.
-- name: ListPathStatsForUser :many
SELECT p.id AS user_path_id, p.root_id, p.rel_path, r.host_path,
       CAST(COALESCE(s.found, 0) AS INTEGER) AS found,
       CAST(COALESCE(s.redundant, 0) AS INTEGER) AS redundant,
       CAST(COALESCE(s.bytes_logical, 0) AS INTEGER) AS bytes_logical,
       CAST(COALESCE(s.bytes_alloc, 0) AS INTEGER) AS bytes_alloc,
       CAST(COALESCE(s.files, 0) AS INTEGER) AS files,
       CAST(COALESCE(s.zfs_used_snapshots, 0) AS INTEGER) AS zfs_used_snapshots
FROM user_paths AS p
JOIN scan_roots AS r ON r.id = p.root_id
LEFT JOIN path_stats AS s ON s.user_path_id = p.id AND s.scan_id = r.current_scan_id
WHERE p.user_id = sqlc.arg(user_id) ORDER BY p.id;

-- name: ListPathStatsOnRoot :many
SELECT p.id AS user_path_id, p.user_id, u.username, p.rel_path, r.host_path,
       CAST(COALESCE(s.found, 0) AS INTEGER) AS found,
       CAST(COALESCE(s.redundant, 0) AS INTEGER) AS redundant,
       CAST(COALESCE(s.bytes_logical, 0) AS INTEGER) AS bytes_logical,
       CAST(COALESCE(s.bytes_alloc, 0) AS INTEGER) AS bytes_alloc,
       CAST(COALESCE(s.files, 0) AS INTEGER) AS files
FROM user_paths AS p
JOIN users AS u ON u.id = p.user_id
JOIN scan_roots AS r ON r.id = p.root_id
LEFT JOIN path_stats AS s ON s.user_path_id = p.id AND s.scan_id = sqlc.arg(scan_id)
WHERE p.root_id = sqlc.arg(root_id) ORDER BY p.id;

-- name: GetUserQuota :one
SELECT quota_bytes, quota_basis FROM users WHERE id = sqlc.arg(id);

-- ListCurrentAndRunningScans lists, for the roots the user has a path on,
-- the current scan and any running one, newest first.
-- name: ListCurrentAndRunningScans :many
SELECT s.id, s.root_id, s.status, s.trigger, s.started_at, s.finished_at, s.dirs, s.files, s.bytes_alloc
FROM scans AS s JOIN scan_roots AS r ON r.id = s.root_id
WHERE (s.id = r.current_scan_id OR s.status = 'running')
  AND EXISTS (SELECT 1 FROM user_paths AS p WHERE p.root_id = r.id AND p.user_id = sqlc.arg(user_id))
ORDER BY s.started_at DESC, s.id DESC;

-- name: ListScansAdmin :many
SELECT id, root_id, status, trigger, started_at, finished_at, dirs, files, bytes_alloc FROM scans
WHERE (sqlc.narg(root_id) IS NULL OR root_id = sqlc.narg(root_id))
ORDER BY started_at DESC, id DESC LIMIT sqlc.arg(row_limit);

-- name: ListHistoryDirs :many
SELECT ts, bytes_alloc, files FROM history_dirs
WHERE root_id = sqlc.arg(root_id) AND path_hash = sqlc.arg(path_hash) AND ts >= sqlc.arg(since)
ORDER BY ts;

-- name: GetListingJob :one
SELECT id, kind, requested_by, status, progress, error FROM jobs WHERE id = sqlc.arg(id);

-- name: ListListingResults :many
SELECT seq, rel_name, type, size, alloc, mtime, uid FROM listing_results
WHERE job_id = sqlc.arg(job_id) AND seq > sqlc.arg(after_seq)
ORDER BY seq LIMIT sqlc.arg(row_limit);

-- LatestUnfilteredListing finds a user's newest finished listing of a node that holds every
-- file below it (subtree mode), or with any_mode every file directly in it: no filters,
-- not cut at the row cap, not expired.
-- name: LatestUnfilteredListing :one
SELECT id FROM jobs
WHERE kind = 'listing' AND status = 'done'
  AND target_node = sqlc.arg(target_node) AND requested_by = sqlc.arg(requested_by)
  AND (sqlc.arg(any_mode) = 1 OR json_extract(params, '$.mode') = 'subtree')
  AND json_extract(params, '$.exts') IS NULL
  AND json_extract(params, '$.min_size') IS NULL
  AND json_extract(params, '$.min_age_days') IS NULL
  AND json_extract(progress, '$.truncated') = 0
  AND (expires_at IS NULL OR expires_at >= sqlc.arg(now))
ORDER BY id DESC LIMIT 1;

-- With direct = 1 only the files directly in the listed node count (no "/" in the name).
-- name: TopListingLargest :many
SELECT rel_name, size, alloc, mtime FROM listing_results
WHERE job_id = sqlc.arg(job_id) AND type <> 'dir'
  AND (sqlc.arg(direct) = 0 OR instr(rel_name, CAST('/' AS BLOB)) = 0)
ORDER BY size DESC, rel_name LIMIT sqlc.arg(row_limit);

-- name: TopListingOldest :many
SELECT rel_name, size, alloc, mtime FROM listing_results
WHERE job_id = sqlc.arg(job_id) AND type <> 'dir'
  AND (sqlc.arg(direct) = 0 OR instr(rel_name, CAST('/' AS BLOB)) = 0)
ORDER BY mtime, size DESC, rel_name LIMIT sqlc.arg(row_limit);

-- name: HasActiveCancel :one
SELECT CAST(EXISTS (
    SELECT 1 FROM jobs
    WHERE kind = 'cancel' AND status IN ('queued', 'running')
      AND json_extract(params, '$.job_id') = CAST(sqlc.arg(job_id) AS INTEGER)) AS INTEGER);

-- name: GetScanSummary :one
SELECT id, root_id, status, trigger, started_at, finished_at, dirs, files, bytes_alloc FROM scans WHERE id = sqlc.arg(id);
