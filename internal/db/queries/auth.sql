-- Queries of local accounts, sessions and the audit log (M5).

-- name: InsertSession :exec
INSERT INTO sessions (id_hash, user_id, created_at, expires_at, last_seen_at, oidc_admin, ip, user_agent)
VALUES (sqlc.arg(id_hash), sqlc.arg(user_id), sqlc.arg(created_at), sqlc.arg(expires_at),
        sqlc.arg(last_seen_at), sqlc.arg(oidc_admin), sqlc.arg(ip), sqlc.arg(user_agent));

-- name: GetSessionUser :one
SELECT s.expires_at, s.last_seen_at, s.oidc_admin, u.notice, u.prefs, u.id, u.username, u.display_name, u.email,
       u.admin_local, u.admin_oidc, u.can_list_files, u.disabled,
       CAST(u.password_hash IS NOT NULL AS INTEGER) AS has_password,
       CAST(u.oidc_subject IS NOT NULL AS INTEGER) AS oidc_linked
FROM sessions AS s JOIN users AS u ON u.id = s.user_id
WHERE s.id_hash = sqlc.arg(id_hash);

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = sqlc.arg(last_seen_at) WHERE id_hash = sqlc.arg(id_hash);

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id_hash = sqlc.arg(id_hash);

-- name: DeleteUserSessions :execrows
DELETE FROM sessions WHERE user_id = sqlc.arg(user_id);

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions
WHERE expires_at <= sqlc.arg(now) OR last_seen_at <= sqlc.arg(idle_cutoff);

-- name: GetUserByUsername :one
SELECT id, username, display_name, email, password_hash, admin_local, admin_oidc,
       can_list_files, disabled
FROM users WHERE username = sqlc.arg(username);

-- name: GetUserByID :one
SELECT id, username, display_name, email, password_hash, admin_local, admin_oidc,
       can_list_files, disabled
FROM users WHERE id = sqlc.arg(id);

-- name: SetPassword :execrows
UPDATE users SET password_hash = sqlc.arg(password_hash) WHERE id = sqlc.arg(id);

-- name: SetLastLogin :exec
UPDATE users SET last_login_at = sqlc.arg(ts) WHERE id = sqlc.arg(id);

-- name: CountEnabledAdmins :one
SELECT count(*) FROM users WHERE disabled = 0 AND (admin_local = 1 OR admin_oidc = 1);

-- InsertAdminIfNone creates the first admin. The NOT EXISTS runs inside the
-- same write transaction as the insert, so two racing setups create one.
-- name: InsertAdminIfNone :one
INSERT INTO users (username, display_name, email, password_hash, admin_local, created_at)
SELECT CAST(sqlc.arg(username) AS TEXT), CAST(sqlc.arg(display_name) AS TEXT),
       sqlc.narg(email), CAST(sqlc.arg(password_hash) AS TEXT), 1, CAST(sqlc.arg(created_at) AS INTEGER)
WHERE NOT EXISTS (
    SELECT 1 FROM users WHERE disabled = 0 AND (admin_local = 1 OR admin_oidc = 1))
RETURNING id;

-- name: InsertUser :one
INSERT INTO users (username, display_name, email, password_hash, admin_local, can_list_files, created_at)
VALUES (sqlc.arg(username), sqlc.arg(display_name), sqlc.narg(email), sqlc.arg(password_hash),
        sqlc.arg(admin_local), sqlc.arg(can_list_files), sqlc.arg(created_at))
RETURNING id;

-- name: InsertAudit :exec
INSERT INTO audit_log (ts, actor_id, action, target, details)
VALUES (sqlc.arg(ts), sqlc.narg(actor_id), sqlc.arg(action), sqlc.arg(target), sqlc.arg(details));

-- name: GetUserByOIDC :one
SELECT id, username, disabled, admin_local, admin_oidc
FROM users WHERE oidc_issuer = sqlc.arg(issuer) AND oidc_subject = sqlc.arg(subject);

-- GetUserByEmail matches case-insensitively (the column is COLLATE NOCASE and
-- UNIQUE, so there is at most one row).
-- name: GetUserByEmail :one
SELECT id, admin_local, disabled, CAST(oidc_subject IS NOT NULL AS INTEGER) AS linked
FROM users WHERE email = sqlc.arg(email);

-- name: InsertOIDCUser :one
INSERT INTO users (username, display_name, email, oidc_issuer, oidc_subject, oidc_linked_at,
                   admin_oidc, created_at)
VALUES (sqlc.arg(username), sqlc.arg(display_name), sqlc.narg(email), sqlc.arg(oidc_issuer),
        sqlc.arg(oidc_subject), sqlc.arg(oidc_linked_at), sqlc.arg(admin_oidc), sqlc.arg(created_at))
RETURNING id;

-- LinkOIDC only touches an unlinked account, so a racing second link affects 0 rows.
-- name: LinkOIDC :execrows
UPDATE users SET oidc_issuer = sqlc.arg(issuer), oidc_subject = sqlc.arg(subject),
                 oidc_linked_at = sqlc.arg(at), notice = 'oidc_linked'
WHERE id = sqlc.arg(id) AND oidc_subject IS NULL;

-- name: GetOIDCLink :one
SELECT oidc_issuer, oidc_subject FROM users WHERE id = sqlc.arg(id);

-- name: UnlinkOIDC :execrows
UPDATE users SET oidc_issuer = NULL, oidc_subject = NULL, oidc_linked_at = NULL,
                 email = NULL, admin_oidc = 0, notice = 'oidc_unlinked'
WHERE id = sqlc.arg(id) AND oidc_subject IS NOT NULL;

-- name: SetAdminOIDC :execrows
UPDATE users SET admin_oidc = sqlc.arg(v) WHERE id = sqlc.arg(id);

-- name: ClearNotice :exec
UPDATE users SET notice = NULL WHERE id = sqlc.arg(id);
