-- name: CreateAuditEvent :one
INSERT INTO audit_events (occurred_at, request_id, actor_user_id, actor_role_codes, action, resource_kind, resource_id, outcome, reason_code, detail_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAuditEvents :many
SELECT * FROM audit_events
ORDER BY occurred_at DESC
LIMIT ? OFFSET ?;

-- SearchAuditEvents is the general filtered listing behind GET /audit-events.
-- Every filter is optional (pass NULL to skip it) and all filters compose.
-- action is matched as a prefix (instr(...) = 1 rather than LIKE, so the
-- caller's value needs no wildcard escaping); the rest are exact matches.
-- Filtering on outcome answers "show me the denials" directly, rather than
-- through the authz.denied action-prefix convention, which only holds for
-- operations that declare no audit action of their own.
-- The tiebreak on id keeps the order total, which offset paging requires.
-- name: SearchAuditEvents :many
SELECT * FROM audit_events
WHERE (sqlc.narg('action_prefix') IS NULL OR instr(action, sqlc.narg('action_prefix')) = 1)
  AND (sqlc.narg('actor_user_id') IS NULL OR actor_user_id = sqlc.narg('actor_user_id'))
  AND (sqlc.narg('resource_kind') IS NULL OR resource_kind = sqlc.narg('resource_kind'))
  AND (sqlc.narg('resource_id') IS NULL OR resource_id = sqlc.narg('resource_id'))
  AND (sqlc.narg('outcome') IS NULL OR outcome = sqlc.narg('outcome'))
ORDER BY occurred_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: ListAuditEventsByResource :many
SELECT * FROM audit_events
WHERE resource_kind = ? AND resource_id = ?
ORDER BY occurred_at DESC
LIMIT ? OFFSET ?;
