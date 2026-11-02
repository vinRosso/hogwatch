-- Queries of the admin API (M8).

-- name: ListUsersPage :many
SELECT id, username, display_name, email, admin_local, admin_oidc, can_list_files, quota_bytes,
       quota_basis, disabled, CAST(oidc_subject IS NOT NULL AS INTEGER) AS oidc_linked,
       CAST(password_hash IS NOT NULL AS INTEGER) AS has_password, created_at, last_login_at
FROM users WHERE id > sqlc.arg(after_id) ORDER BY id LIMIT sqlc.arg(lim);

-- name: GetAdminUser :one
SELECT id, username, display_name, email, admin_local, admin_oidc, can_list_files, quota_bytes,
       quota_basis, disabled, CAST(oidc_subject IS NOT NULL AS INTEGER) AS oidc_linked,
       CAST(password_hash IS NOT NULL AS INTEGER) AS has_password, created_at, last_login_at
FROM users WHERE id = sqlc.arg(id);

-- UpdateUserAdmin writes the columns an admin may change; the handler merges
-- the patch into the current row first. It never touches the OIDC columns.
-- name: UpdateUserAdmin :execrows
UPDATE users
SET display_name = sqlc.arg(display_name), email = sqlc.narg(email), admin_local = sqlc.arg(admin_local),
    can_list_files = sqlc.arg(can_list_files), quota_bytes = sqlc.narg(quota_bytes),
    quota_basis = sqlc.arg(quota_basis), disabled = sqlc.arg(disabled)
WHERE id = sqlc.arg(id);

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = sqlc.arg(id);

-- CountLoginAdmins counts the enabled admins who can sign in: a local admin
-- needs a password, or (with OIDC on) an OIDC link; an OIDC admin counts only
-- with OIDC on.
-- name: CountLoginAdmins :one
SELECT count(*) FROM users
WHERE disabled = 0
  AND ((admin_local = 1 AND (password_hash IS NOT NULL
                             OR (CAST(sqlc.arg(oidc_on) AS INTEGER) = 1 AND oidc_subject IS NOT NULL)))
       OR (CAST(sqlc.arg(oidc_on) AS INTEGER) = 1 AND admin_oidc = 1));

-- name: CountEnabledLocalAdmins :one
SELECT count(*) FROM users WHERE disabled = 0 AND admin_local = 1;

-- name: ListUserPathsOfUser :many
SELECT id, user_id, root_id, rel_path, created_at FROM user_paths
WHERE user_id = sqlc.arg(user_id) ORDER BY id;

-- name: ListUserPathsOnRootAll :many
SELECT p.id, p.user_id, u.username, p.rel_path FROM user_paths AS p
JOIN users AS u ON u.id = p.user_id
WHERE p.root_id = sqlc.arg(root_id) ORDER BY p.id;

-- name: InsertUserPath :one
INSERT INTO user_paths (user_id, root_id, rel_path, created_by, created_at)
VALUES (sqlc.arg(user_id), sqlc.arg(root_id), sqlc.arg(rel_path), sqlc.narg(created_by), sqlc.arg(created_at))
RETURNING id, user_id, root_id, rel_path, created_at;

-- name: GetUserPath :one
SELECT id, user_id, root_id, rel_path, created_at FROM user_paths
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: DeleteUserPath :execrows
DELETE FROM user_paths WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: DeleteUsageForUser :exec
DELETE FROM usage WHERE scan_id = sqlc.arg(scan_id) AND user_id = sqlc.arg(user_id);

-- name: DeleteUsageForUserAll :exec
DELETE FROM usage WHERE user_id = sqlc.arg(user_id);

-- ListPathStatsZFS reads the ZFS columns of the user's path_stats rows of a
-- scan, so a recompute can copy them (the web has no ./zfs mount).
-- name: ListPathStatsZFS :many
SELECT s.user_path_id, s.zfs_dataset, s.zfs_used, s.zfs_avail, s.zfs_quota, s.zfs_used_snapshots
FROM path_stats AS s JOIN user_paths AS p ON p.id = s.user_path_id
WHERE s.scan_id = sqlc.arg(scan_id) AND p.user_id = sqlc.arg(user_id);

-- name: DeletePathStatsForUser :exec
DELETE FROM path_stats
WHERE scan_id = sqlc.arg(scan_id)
  AND user_path_id IN (SELECT id FROM user_paths WHERE user_id = sqlc.arg(user_id));

-- name: DeleteDupIssuesForPath :exec
DELETE FROM config_issues
WHERE kind = 'cross_root_duplicate' AND json_extract(details, '$.user_path_id') = CAST(sqlc.arg(user_path_id) AS INTEGER);

-- name: DeleteDupIssuesForRoot :exec
DELETE FROM config_issues
WHERE kind = 'cross_root_duplicate' AND json_extract(details, '$.other_root_id') = CAST(sqlc.arg(root_id) AS INTEGER);

-- name: ListAdminRoots :many
SELECT * FROM scan_roots ORDER BY id;

-- name: GetAdminRoot :one
SELECT * FROM scan_roots WHERE id = sqlc.arg(id);

-- name: InsertRoot :one
INSERT INTO scan_roots (name, container_path, host_path, cross_fs, excludes, schedule, enabled)
VALUES (sqlc.arg(name), sqlc.arg(container_path), sqlc.arg(host_path), sqlc.arg(cross_fs),
        sqlc.arg(excludes), sqlc.arg(schedule), sqlc.arg(enabled))
RETURNING id;

-- name: UpdateRoot :execrows
UPDATE scan_roots
SET name = sqlc.arg(name), cross_fs = sqlc.arg(cross_fs), excludes = sqlc.arg(excludes),
    schedule = sqlc.arg(schedule), enabled = sqlc.arg(enabled)
WHERE id = sqlc.arg(id);

-- name: DeleteRoot :execrows
DELETE FROM scan_roots WHERE id = sqlc.arg(id);

-- name: CountUserPathsOnRoot :one
SELECT count(*) FROM user_paths WHERE root_id = sqlc.arg(root_id);

-- name: GetJobAdmin :one
SELECT j.id, j.kind, j.root_id, j.status, j.progress, j.error, j.created_at, j.started_at, j.finished_at,
       s.id AS scan_id
FROM jobs AS j LEFT JOIN scans AS s ON s.job_id = j.id WHERE j.id = sqlc.arg(id);

-- ListScansAdminPage pages by (started_at, id) descending; a NULL cursor
-- starts at the newest scan.
-- name: ListScansAdminPage :many
SELECT s.id, s.job_id, s.root_id, s.status, s.trigger, s.started_at, s.finished_at, s.dirs, s.files,
       s.bytes_alloc, s.error, s.error_count, j.progress
FROM scans AS s LEFT JOIN jobs AS j ON j.id = s.job_id
WHERE (sqlc.narg(root_id) IS NULL OR s.root_id = sqlc.narg(root_id))
  AND (sqlc.narg(before_started) IS NULL
       OR s.started_at < sqlc.narg(before_started)
       OR (s.started_at = sqlc.narg(before_started) AND s.id < sqlc.narg(before_id)))
ORDER BY s.started_at DESC, s.id DESC LIMIT sqlc.arg(lim);

-- RankingRows has one row per user, with zeros for a user without usage.
-- name: RankingRows :many
SELECT u.id AS user_id, u.username, u.quota_bytes, u.quota_basis,
       CAST(COALESCE(SUM(us.bytes_logical), 0) AS INTEGER) AS bytes_logical,
       CAST(COALESCE(SUM(us.bytes_alloc), 0) AS INTEGER) AS bytes_alloc,
       CAST(COALESCE(SUM(us.files), 0) AS INTEGER) AS files,
       CAST(COALESCE(SUM(us.shared_hardlink_bytes), 0) AS INTEGER) AS shared_hardlink_bytes
FROM users AS u
LEFT JOIN usage AS us ON us.user_id = u.id
 AND us.scan_id IN (SELECT current_scan_id FROM scan_roots WHERE current_scan_id IS NOT NULL)
GROUP BY u.id ORDER BY u.id;

-- GrowthBase returns, per (user_id, root_id) of one scope, the latest history
-- point at or before since: the one definition of a growth base. Points of
-- different tiers can share a ts; the largest wins so the result is deterministic.
-- name: GrowthBase :many
SELECT h.user_id, h.root_id, CAST(MAX(h.bytes_alloc) AS INTEGER) AS bytes_alloc FROM history_totals AS h
WHERE h.scope = sqlc.arg(scope)
  AND h.ts = (SELECT MAX(h2.ts) FROM history_totals AS h2
              WHERE h2.scope = h.scope AND h2.user_id IS h.user_id AND h2.root_id IS h.root_id
                AND h2.ts <= sqlc.arg(since))
GROUP BY h.user_id, h.root_id;

-- GrowthBaseForUser is GrowthBase restricted to one user, so a single user's view
-- reads only that user's rows through the (scope, user_id, root_id, ts) index.
-- name: GrowthBaseForUser :many
SELECT h.user_id, h.root_id, CAST(MAX(h.bytes_alloc) AS INTEGER) AS bytes_alloc FROM history_totals AS h
WHERE h.scope = sqlc.arg(scope) AND h.user_id = sqlc.arg(user_id)
  AND h.ts = (SELECT MAX(h2.ts) FROM history_totals AS h2
              WHERE h2.scope = h.scope AND h2.user_id = h.user_id AND h2.root_id IS h.root_id
                AND h2.ts <= sqlc.arg(since))
GROUP BY h.user_id, h.root_id;

-- RankingRowsForRoot is RankingRows restricted to one root's current scan.
-- name: RankingRowsForRoot :many
SELECT u.id AS user_id, u.username, u.quota_bytes, u.quota_basis,
       CAST(COALESCE(SUM(us.bytes_logical), 0) AS INTEGER) AS bytes_logical,
       CAST(COALESCE(SUM(us.bytes_alloc), 0) AS INTEGER) AS bytes_alloc,
       CAST(COALESCE(SUM(us.files), 0) AS INTEGER) AS files,
       CAST(COALESCE(SUM(us.shared_hardlink_bytes), 0) AS INTEGER) AS shared_hardlink_bytes
FROM users AS u
LEFT JOIN usage AS us ON us.user_id = u.id AND us.root_id = sqlc.arg(root_id)
 AND us.scan_id IN (SELECT current_scan_id FROM scan_roots WHERE id = sqlc.arg(root_id) AND current_scan_id IS NOT NULL)
GROUP BY u.id ORDER BY u.id;

-- RankingSnapshotsForRoot is RankingSnapshots restricted to one root.
-- name: RankingSnapshotsForRoot :many
SELECT p.user_id, CAST(COALESCE(SUM(s.zfs_used_snapshots), 0) AS INTEGER) AS snapshots
FROM path_stats AS s
JOIN user_paths AS p ON p.id = s.user_path_id
JOIN scan_roots AS r ON r.id = p.root_id AND r.current_scan_id = s.scan_id
WHERE s.found = 1 AND s.redundant = 0 AND p.root_id = sqlc.arg(root_id)
GROUP BY p.user_id;

-- name: ListMountRows :many
SELECT r.id AS root_id, r.name AS root_name, s.bytes_alloc AS scan_bytes_alloc,
       m.fs_total, m.fs_free, m.zfs_used, m.zfs_avail, m.zfs_quota, m.zfs_used_snapshots
FROM scan_roots AS r
LEFT JOIN scans AS s ON s.id = r.current_scan_id
LEFT JOIN mount_stats AS m ON m.scan_id = r.current_scan_id
ORDER BY r.id;

-- name: ListStoredIssues :many
SELECT id, root_id, kind, details, created_at FROM config_issues ORDER BY root_id, kind, id;

-- name: ListPathStatsShared :many
SELECT s.user_path_id, p.user_id, p.root_id, s.shared_hardlink_bytes
FROM path_stats AS s
JOIN user_paths AS p ON p.id = s.user_path_id
JOIN scan_roots AS r ON r.id = p.root_id AND r.current_scan_id = s.scan_id
WHERE s.shared_hardlink_bytes > 0 ORDER BY p.root_id, s.user_path_id;

-- name: UpsertCategory :exec
INSERT INTO file_categories (ext, category) VALUES (sqlc.arg(ext), sqlc.arg(category))
ON CONFLICT (ext) DO UPDATE SET category = excluded.category;

-- name: DeleteCategory :execrows
DELETE FROM file_categories WHERE ext = sqlc.arg(ext);

-- ListAuditPage pages by id descending; a NULL cursor starts at the newest.
-- name: ListAuditPage :many
SELECT id, ts, actor_id, action, target, details FROM audit_log
WHERE sqlc.narg(before_id) IS NULL OR id < sqlc.narg(before_id)
ORDER BY id DESC LIMIT sqlc.arg(lim);

-- RankingSnapshots sums, per user, the snapshot bytes of found, non-redundant
-- paths on current scans: the part quota.include_snapshots adds.
-- name: RankingSnapshots :many
SELECT p.user_id, CAST(COALESCE(SUM(s.zfs_used_snapshots), 0) AS INTEGER) AS snapshots
FROM path_stats AS s
JOIN user_paths AS p ON p.id = s.user_path_id
JOIN scan_roots AS r ON r.id = p.root_id AND r.current_scan_id = s.scan_id
WHERE s.found = 1 AND s.redundant = 0
GROUP BY p.user_id;

-- name: CountActiveRootJobs :one
-- Scans and listings that are queued or running: deleting the root would cascade
-- them away under the worker that holds them.
SELECT count(*) FROM jobs
WHERE kind IN ('scan', 'listing') AND root_id = sqlc.arg(root_id) AND status IN ('queued', 'running');
