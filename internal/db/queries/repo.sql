-- Queries of the Scope repository (M5).

-- name: GetServableScan :one
SELECT id, root_id, status, file, started_at FROM scans WHERE id = sqlc.arg(id);

-- name: ListUserPathsOnRoot :many
SELECT id, rel_path FROM user_paths
WHERE user_id = sqlc.arg(user_id) AND root_id = sqlc.arg(root_id) ORDER BY id;
