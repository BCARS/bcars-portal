-- name: CreateAuditEvent :one
INSERT INTO audit_events (occurred_at, request_id, actor_user_id, actor_role_codes, action, resource_kind, resource_id, outcome, reason_code, detail_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAuditEvents :many
SELECT * FROM audit_events
ORDER BY occurred_at DESC
LIMIT ? OFFSET ?;

-- name: ListAuditEventsByActor :many
SELECT * FROM audit_events
WHERE actor_user_id = ?
ORDER BY occurred_at DESC
LIMIT ? OFFSET ?;

-- name: ListAuditEventsByResource :many
SELECT * FROM audit_events
WHERE resource_kind = ? AND resource_id = ?
ORDER BY occurred_at DESC
LIMIT ? OFFSET ?;

-- name: ListAuditEventsByAction :many
SELECT * FROM audit_events
WHERE action = ?
ORDER BY occurred_at DESC
LIMIT ? OFFSET ?;
