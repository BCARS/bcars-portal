-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ?;

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: CreateUser :one
INSERT INTO users (email, password_hash, password_algo_params, person_id)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: UpdateUserPassword :one
UPDATE users
SET password_hash = ?, password_algo_params = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: UpdateUserLastLogin :exec
UPDATE users
SET last_login_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    failed_login_count = 0,
    locked_until = NULL,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: IncrementFailedLogin :exec
UPDATE users
SET failed_login_count = failed_login_count + 1,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: LockUser :exec
UPDATE users
SET locked_until = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: ListUsers :many
SELECT id, email, person_id, is_active, last_login_at, created_at
FROM users
ORDER BY email
LIMIT ? OFFSET ?;

-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at, ip_hash, user_agent)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetSession :one
SELECT * FROM sessions WHERE id = ? AND revoked_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = ? WHERE id = ?;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: RevokeAllSessionsForUser :exec
UPDATE sessions SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE user_id = ? AND revoked_at IS NULL;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: CreateEmailLink :one
INSERT INTO email_links (purpose, user_id, email, token_hash, created_at, expires_at, requested_ip_hash)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetEmailLinkByTokenHash :one
SELECT * FROM email_links
WHERE token_hash = ? AND consumed_at IS NULL AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: ConsumeEmailLink :exec
UPDATE email_links SET consumed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;
