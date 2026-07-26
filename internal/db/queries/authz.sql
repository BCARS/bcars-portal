-- name: ListCapabilities :many
SELECT * FROM capabilities ORDER BY code;

-- name: ListRoles :many
SELECT * FROM roles ORDER BY code;

-- name: ListRoleCapabilities :many
SELECT rc.role_code, rc.capability_code
FROM role_capabilities rc
ORDER BY rc.role_code, rc.capability_code;

-- name: EffectiveCapabilities :many
SELECT DISTINCT c.code
FROM capabilities c
WHERE c.code IN (
    -- From active role grants
    SELECT rc.capability_code
    FROM user_role_grants urg
    JOIN role_capabilities rc ON rc.role_code = urg.role_code
    WHERE urg.user_id = ? AND urg.revoked_at IS NULL
    UNION
    -- From direct capability grants
    SELECT ucg.capability_code
    FROM user_capability_grants ucg
    WHERE ucg.user_id = ? AND ucg.revoked_at IS NULL
)
ORDER BY c.code;

-- name: ActiveRolesForUser :many
SELECT urg.id, urg.role_code, urg.granted_at, r.description AS role_description
FROM user_role_grants urg
JOIN roles r ON r.code = urg.role_code
WHERE urg.user_id = ? AND urg.revoked_at IS NULL
ORDER BY urg.role_code;

-- name: CreateRoleGrant :one
INSERT INTO user_role_grants (user_id, role_code, granted_by, granted_at, reason)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: RevokeRoleGrant :exec
UPDATE user_role_grants
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    revoked_by = ?
WHERE id = ? AND revoked_at IS NULL;

-- name: CreateCapabilityGrant :one
INSERT INTO user_capability_grants (user_id, capability_code, granted_by, granted_at, reason)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: RevokeCapabilityGrant :exec
UPDATE user_capability_grants
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    revoked_by = ?
WHERE id = ? AND revoked_at IS NULL;
