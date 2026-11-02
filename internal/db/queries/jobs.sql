-- name: InsertJob :one
INSERT INTO jobs (kind, root_id, target_node, params, requested_by, created_at, expires_at)
VALUES (sqlc.arg(kind), sqlc.arg(root_id), sqlc.arg(target_node), sqlc.arg(params),
        sqlc.arg(requested_by), sqlc.arg(created_at), sqlc.arg(expires_at))
RETURNING id;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = sqlc.arg(id);

-- ClaimJob is one atomic statement: SQLite takes the write lock before the
-- subselect runs, so two claimers never pick the same job.
-- name: ClaimJob :one
UPDATE jobs
SET status = 'running', started_at = sqlc.arg(started_at)
WHERE id = (
    SELECT j.id FROM jobs AS j
    WHERE j.status = 'queued' AND j.kind = sqlc.arg(kind)
    ORDER BY j.id
    LIMIT 1
)
AND status = 'queued'
RETURNING *;

-- name: UpdateJobProgress :exec
UPDATE jobs SET progress = sqlc.arg(progress) WHERE id = sqlc.arg(id);

-- name: FinishJob :execrows
UPDATE jobs
SET status = sqlc.arg(status), error = sqlc.arg(error), finished_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: FailRunningJobs :execrows
UPDATE jobs
SET status = 'failed', error = sqlc.arg(error), finished_at = sqlc.arg(finished_at)
WHERE status = 'running';

-- name: CountActiveJobsByUser :one
SELECT count(*) FROM jobs
WHERE kind = sqlc.arg(kind) AND requested_by = sqlc.arg(requested_by)
  AND status IN ('queued', 'running');

-- DeleteExpiredJobs deletes finished jobs past expires_at. A queued or
-- running job is never deleted under its worker.
-- name: DeleteExpiredJobs :execrows
DELETE FROM jobs
WHERE expires_at < sqlc.arg(now) AND status IN ('done', 'failed', 'cancelled');

-- name: CountActiveScanJobs :one
SELECT count(*) FROM jobs
WHERE kind = 'scan' AND root_id = sqlc.arg(root_id) AND status IN ('queued', 'running');

-- CancelQueuedJob cancels a job that no worker has claimed yet. A running job
-- is cancelled by the worker that holds it.
-- name: CancelQueuedJob :execrows
UPDATE jobs
SET status = 'cancelled', finished_at = unixepoch()
WHERE id = sqlc.arg(id) AND status = 'queued';

-- FinishListingJob sets expires_at so the finished listing is deleted later.
-- name: FinishListingJob :execrows
UPDATE jobs
SET status = sqlc.arg(status), error = sqlc.arg(error), finished_at = sqlc.arg(finished_at),
    expires_at = sqlc.arg(expires_at)
WHERE id = sqlc.arg(id) AND status = 'running';

-- FailRunningJobsAtStartup fails every job the previous scanner left running.
-- A listing also gets expires_at so DeleteExpiredJobs removes its partial rows.
-- name: FailRunningJobsAtStartup :many
UPDATE jobs
SET status = 'failed', error = sqlc.arg(error), finished_at = sqlc.arg(finished_at),
    expires_at = CASE WHEN kind = 'listing' THEN sqlc.arg(listing_expires_at) ELSE expires_at END
WHERE status = 'running'
RETURNING id, kind;
