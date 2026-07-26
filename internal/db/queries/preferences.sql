-- name: GetLatestVisibility :one
SELECT * FROM contact_method_visibility_events
WHERE contact_method_id = ?
ORDER BY effective_at DESC
LIMIT 1;

-- name: ListVisibilityHistory :many
SELECT * FROM contact_method_visibility_events
WHERE contact_method_id = ?
ORDER BY effective_at DESC;

-- name: CreateVisibilityEvent :one
INSERT INTO contact_method_visibility_events (contact_method_id, audience, source, effective_at, actor_user_id, note)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetLatestAcsAresSharing :one
SELECT * FROM acs_ares_sharing_events
WHERE person_id = ?
ORDER BY effective_at DESC
LIMIT 1;

-- name: ListAcsAresSharingHistory :many
SELECT * FROM acs_ares_sharing_events
WHERE person_id = ?
ORDER BY effective_at DESC;

-- name: CreateAcsAresSharingEvent :one
INSERT INTO acs_ares_sharing_events (person_id, participates, source, effective_at, actor_user_id, reason)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;
