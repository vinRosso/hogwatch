-- name: GetUserPrefs :one
SELECT prefs FROM users WHERE id = sqlc.arg(id);

-- Merges the keys of a JSON object into the stored document (RFC 7386: a null value removes a key).
-- name: MergeUserPrefs :execrows
UPDATE users SET prefs = json_patch(prefs, sqlc.arg(patch)) WHERE id = sqlc.arg(id);
