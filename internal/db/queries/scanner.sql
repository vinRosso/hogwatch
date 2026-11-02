-- Queries of the scanner service (M4).

-- name: ListRoots :many
SELECT * FROM scan_roots ORDER BY id;

-- name: GetRoot :one
SELECT * FROM scan_roots WHERE id = sqlc.arg(id);

-- name: ListUserPathsByRoot :many
SELECT id, user_id, root_id, rel_path FROM user_paths
WHERE root_id = sqlc.arg(root_id) ORDER BY id;

-- name: ListAllUserPaths :many
SELECT id, user_id, root_id, rel_path FROM user_paths ORDER BY id;

-- name: GetUserAccess :one
SELECT admin_local, admin_oidc, can_list_files, disabled FROM users WHERE id = sqlc.arg(id);

-- name: ListCategories :many
SELECT ext, category FROM file_categories ORDER BY ext;

-- name: InsertScan :one
INSERT INTO scans (root_id, job_id, file, status, trigger, started_at)
VALUES (sqlc.arg(root_id), sqlc.arg(job_id), sqlc.arg(file), 'running', sqlc.arg(trigger), sqlc.arg(started_at))
RETURNING id;

-- FinishScan only moves a scan out of running, so a second call is a no-op.
-- name: FinishScan :execrows
UPDATE scans
SET status = sqlc.arg(status), finished_at = sqlc.arg(finished_at), dirs = sqlc.arg(dirs),
    files = sqlc.arg(files), bytes_logical = sqlc.arg(bytes_logical),
    bytes_alloc = sqlc.arg(bytes_alloc), error_count = sqlc.arg(error_count),
    top_n_rows = sqlc.arg(top_n_rows), error = sqlc.arg(error)
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: SetCurrentScan :exec
UPDATE scan_roots SET current_scan_id = sqlc.arg(scan_id) WHERE id = sqlc.arg(root_id);

-- LastScanStart is 0 for a root that was never scanned.
-- name: LastScanStart :one
SELECT CAST(COALESCE(MAX(started_at), 0) AS INTEGER) FROM scans WHERE root_id = sqlc.arg(root_id);

-- name: CurrentScanFiles :one
SELECT s.file FROM scan_roots AS r JOIN scans AS s ON s.id = r.current_scan_id
WHERE r.id = sqlc.arg(root_id);

-- name: FailRunningScans :execrows
UPDATE scans
SET status = 'failed', error = sqlc.arg(error), finished_at = sqlc.arg(finished_at)
WHERE status = 'running';

-- name: ListScansWithFiles :many
SELECT id, root_id, file FROM scans WHERE status = 'done' ORDER BY id;

-- ListLiveScansWithFiles lists the scans whose generation file is in use or
-- servable: done, and running (its file is being written).
-- name: ListLiveScansWithFiles :many
SELECT id, root_id, file FROM scans WHERE status IN ('done', 'running') ORDER BY id;

-- SupersededScans lists done scans that are no longer current once the
-- current scan of their root finished at or before cutoff.
-- name: SupersededScans :many
SELECT s.id, s.root_id, s.file FROM scans AS s
WHERE s.status = 'done'
  AND s.id NOT IN (SELECT current_scan_id FROM scan_roots WHERE current_scan_id IS NOT NULL)
  AND EXISTS (
      SELECT 1 FROM scan_roots AS r JOIN scans AS c ON c.id = r.current_scan_id
      WHERE r.id = s.root_id AND c.finished_at <= sqlc.arg(cutoff))
ORDER BY s.id;

-- name: MarkScanDeleted :execrows
UPDATE scans SET status = 'deleted' WHERE id = sqlc.arg(id) AND status = 'done';

-- name: InsertListingResult :exec
INSERT INTO listing_results (job_id, seq, rel_name, type, size, alloc, mtime, uid)
VALUES (sqlc.arg(job_id), sqlc.arg(seq), sqlc.arg(rel_name), sqlc.arg(type), sqlc.arg(size),
        sqlc.arg(alloc), sqlc.arg(mtime), sqlc.arg(uid));

-- name: DeleteListingResults :exec
DELETE FROM listing_results WHERE job_id = sqlc.arg(job_id);

-- name: InsertUsage :exec
INSERT INTO usage (scan_id, user_id, root_id, bytes_logical, bytes_alloc, files, shared_hardlink_bytes)
VALUES (sqlc.arg(scan_id), sqlc.arg(user_id), sqlc.arg(root_id), sqlc.arg(bytes_logical),
        sqlc.arg(bytes_alloc), sqlc.arg(files), sqlc.arg(shared_hardlink_bytes));

-- name: InsertMountStats :exec
INSERT INTO mount_stats (scan_id, root_id, fs_total, fs_free, zfs_used, zfs_avail, zfs_quota, zfs_used_snapshots)
VALUES (sqlc.arg(scan_id), sqlc.arg(root_id), sqlc.arg(fs_total), sqlc.arg(fs_free),
        sqlc.arg(zfs_used), sqlc.arg(zfs_avail), sqlc.arg(zfs_quota), sqlc.arg(zfs_used_snapshots));

-- name: InsertPathStats :exec
INSERT INTO path_stats (scan_id, user_path_id, found, redundant, bytes_logical, bytes_alloc, files,
                        shared_hardlink_bytes, zfs_dataset, zfs_used, zfs_avail, zfs_quota, zfs_used_snapshots)
VALUES (sqlc.arg(scan_id), sqlc.arg(user_path_id), sqlc.arg(found), sqlc.arg(redundant),
        sqlc.arg(bytes_logical), sqlc.arg(bytes_alloc), sqlc.arg(files),
        sqlc.arg(shared_hardlink_bytes), sqlc.arg(zfs_dataset), sqlc.arg(zfs_used),
        sqlc.arg(zfs_avail), sqlc.arg(zfs_quota), sqlc.arg(zfs_used_snapshots));

-- name: InsertHistoryTotal :exec
INSERT INTO history_totals (ts, scope, user_id, root_id, bytes_logical, bytes_alloc, files, tier)
VALUES (sqlc.arg(ts), sqlc.arg(scope), sqlc.arg(user_id), sqlc.arg(root_id), sqlc.arg(bytes_logical),
        sqlc.arg(bytes_alloc), sqlc.arg(files), 'raw');

-- name: InsertHistoryDir :exec
INSERT INTO history_dirs (ts, root_id, path_hash, bytes_alloc, files, tier)
VALUES (sqlc.arg(ts), sqlc.arg(root_id), sqlc.arg(path_hash), sqlc.arg(bytes_alloc), sqlc.arg(files), 'raw');

-- name: UpsertHistoryPath :exec
INSERT INTO history_paths (path_hash, root_id, rel_path)
VALUES (sqlc.arg(path_hash), sqlc.arg(root_id), sqlc.arg(rel_path))
ON CONFLICT (path_hash) DO UPDATE SET root_id = excluded.root_id, rel_path = excluded.rel_path;

-- name: DeleteIssues :exec
DELETE FROM config_issues WHERE root_id = sqlc.arg(root_id) AND kind = sqlc.arg(kind);

-- name: DeleteIssuesByKind :exec
DELETE FROM config_issues WHERE kind = sqlc.arg(kind);

-- name: InsertIssue :exec
INSERT INTO config_issues (root_id, scan_id, kind, details, created_at)
VALUES (sqlc.arg(root_id), sqlc.arg(scan_id), sqlc.arg(kind), sqlc.arg(details), sqlc.arg(created_at));
