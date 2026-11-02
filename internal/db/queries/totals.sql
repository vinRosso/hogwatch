-- SumCurrentUsageByUser sums each user usage over every root current scan.
-- name: SumCurrentUsageByUser :many
SELECT u.user_id,
       CAST(SUM(u.bytes_logical) AS INTEGER) AS bytes_logical,
       CAST(SUM(u.bytes_alloc) AS INTEGER) AS bytes_alloc,
       CAST(SUM(u.files) AS INTEGER) AS files
FROM usage AS u JOIN scan_roots AS r ON r.current_scan_id = u.scan_id
GROUP BY u.user_id
ORDER BY u.user_id;
