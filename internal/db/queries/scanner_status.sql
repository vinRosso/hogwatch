-- name: UpsertHeartbeat :exec
INSERT INTO scanner_status (id, heartbeat_at, version)
VALUES (1, sqlc.arg(heartbeat_at), sqlc.arg(version))
ON CONFLICT (id) DO UPDATE SET
    heartbeat_at = excluded.heartbeat_at,
    version = excluded.version;

-- SetAvailableMounts upserts the single row, so mounts published before the
-- first heartbeat are not lost. Publishing is a sign of life: it also sets
-- heartbeat_at and version.
-- name: SetAvailableMounts :exec
INSERT INTO scanner_status (id, heartbeat_at, version, available_mounts)
VALUES (1, sqlc.arg(heartbeat_at), sqlc.arg(version), sqlc.arg(available_mounts))
ON CONFLICT (id) DO UPDATE SET
    heartbeat_at = excluded.heartbeat_at,
    version = excluded.version,
    available_mounts = excluded.available_mounts;

-- name: GetScannerStatus :one
SELECT * FROM scanner_status WHERE id = 1;
