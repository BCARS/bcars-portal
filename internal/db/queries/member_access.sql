-- Member record access grants.
--
-- This file is the ONLY way authorization learns which person records a user
-- may see. Nothing here joins users.person_id, contact_methods.value_norm,
-- person_relationships, or role_capabilities: an access decision comes from an
-- explicit unrevoked grant or it does not exist.
--
-- Keep every comment in this file ASCII: sqlc substitutes sqlc.arg() by byte
-- offset, so a multi-byte character above a query corrupts the SQL it parses.

-- name: GrantMemberAccess :one
INSERT INTO member_access_grants (
    user_id, person_id, access_kind, reason, granted_by, granted_at
) VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetMemberAccessGrant :one
SELECT * FROM member_access_grants WHERE id = ?;

-- name: ListActiveAccessGrantsForUser :many
--
-- The authorization read. Loaded per request, so revoking a grant takes effect
-- inside a session that is already open.
SELECT g.id          AS grant_id,
       g.user_id     AS user_id,
       g.person_id   AS person_id,
       g.access_kind AS access_kind,
       g.granted_at  AS granted_at,
       p.display_name AS display_name,
       p.call_sign    AS call_sign
  FROM member_access_grants g
  JOIN persons p ON p.id = g.person_id
 WHERE g.user_id = ?
   AND g.revoked_at IS NULL
 ORDER BY p.sort_name, g.person_id;

-- name: CountActiveAccessGrant :one
--
-- The single-record authorization probe: does this user hold access to this
-- person right now. Returns 0 or 1 because of ux_member_access_active.
SELECT count(*) FROM member_access_grants
 WHERE user_id = ? AND person_id = ? AND revoked_at IS NULL;

-- name: ListAccessGrantsForPerson :many
--
-- Officer view. Includes revoked rows so the history of who could see a record
-- is readable, not just the current state.
SELECT g.*, u.email AS user_email
  FROM member_access_grants g
  JOIN users u ON u.id = g.user_id
 WHERE g.person_id = ?
 ORDER BY g.revoked_at IS NOT NULL, g.granted_at DESC, g.id DESC;

-- name: RevokeMemberAccess :one
--
-- Version-guarded, so a concurrent revoke cannot silently no-op. A caller that
-- gets sql.ErrNoRows maps it to db.ErrStale after confirming the row exists.
UPDATE member_access_grants
   SET revoked_at = sqlc.arg(revoked_at),
       revoked_by = sqlc.arg(revoked_by),
       revoke_reason = sqlc.arg(revoke_reason),
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
       version = version + 1
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
   AND revoked_at IS NULL
RETURNING *;
