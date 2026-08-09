-- Member change requests and their typed items.
--
-- No query in this file writes persons, contact_methods, memberships, or
-- preference events. A request records what someone PROPOSED; only the review
-- and apply path (bcars-portal-4ux.3) calls the domain services that change
-- canonical data, and only for an approved item.
--
-- Keep every comment in this file ASCII: sqlc substitutes sqlc.arg() by byte
-- offset, so a multi-byte character above a query corrupts the SQL it parses.

-- name: CreateChangeRequest :one
INSERT INTO member_change_requests (
    source, status, requester_user_id, target_person_id,
    supplied_name, supplied_call_sign, supplied_contact, stated_relationship,
    summary, received_by, submitted_at, source_ip_hash
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetChangeRequest :one
SELECT * FROM member_change_requests WHERE id = ?;

-- name: ListChangeRequests :many
--
-- The officer queue. Pass a NULL status or source to ignore that filter.
SELECT * FROM member_change_requests
 WHERE (sqlc.narg(status) IS NULL OR status = sqlc.narg(status))
   AND (sqlc.narg(source) IS NULL OR source = sqlc.narg(source))
 ORDER BY submitted_at DESC, id DESC
 LIMIT ? OFFSET ?;

-- name: ListUntargetedChangeRequests :many
--
-- Blind public intake awaiting an officer link.
SELECT * FROM member_change_requests
 WHERE target_person_id IS NULL
   AND status NOT IN ('resolved', 'withdrawn')
 ORDER BY submitted_at DESC, id DESC
 LIMIT ? OFFSET ?;

-- name: ListChangeRequestsForRequester :many
--
-- A member's own request history. Scoped by requester, never by target, so it
-- cannot become a read of someone else's record.
SELECT * FROM member_change_requests
 WHERE requester_user_id = ?
 ORDER BY submitted_at DESC, id DESC
 LIMIT ? OFFSET ?;

-- name: SetChangeRequestTarget :one
--
-- Officer triage links a supplied hint to a canonical person. The supplied_*
-- snapshot is untouched: what the submitter said stays on the record.
UPDATE member_change_requests
   SET target_person_id = sqlc.arg(target_person_id),
       triaged_by = sqlc.arg(triaged_by),
       triaged_at = sqlc.arg(triaged_at),
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
       version = version + 1
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
RETURNING *;

-- name: SetChangeRequestStatus :one
UPDATE member_change_requests
   SET status = sqlc.arg(status),
       resolved_at = sqlc.narg(resolved_at),
       withdrawn_at = sqlc.narg(withdrawn_at),
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
       version = version + 1
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
RETURNING *;

-- name: CreateChangeRequestItem :one
INSERT INTO member_change_request_items (
    request_id, ordinal, operation, proposed_value,
    target_kind, target_id, target_version, sensitivity
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetChangeRequestItem :one
SELECT * FROM member_change_request_items WHERE id = ?;

-- name: ListChangeRequestItems :many
SELECT * FROM member_change_request_items
 WHERE request_id = ?
 ORDER BY ordinal;

-- name: CountPendingChangeRequestItems :one
--
-- A request resolves only when this reaches zero.
SELECT count(*) FROM member_change_request_items
 WHERE request_id = ? AND status = 'pending';

-- name: DecideChangeRequestItem :one
--
-- Records a decision without applying anything. Guarded on the item version
-- AND on still being pending, so a second reviewer deciding the same item gets
-- no rows rather than overwriting the first decision.
UPDATE member_change_request_items
   SET status = sqlc.arg(status),
       reviewed_by = sqlc.arg(reviewed_by),
       reviewed_at = sqlc.arg(reviewed_at),
       decision_reason = sqlc.narg(decision_reason),
       verification_note = sqlc.narg(verification_note),
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
       version = version + 1
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
   AND status = 'pending'
RETURNING *;

-- name: MarkChangeRequestItemApplied :one
--
-- Stamps the resource an approved item produced. `applied_at IS NULL` in the
-- WHERE clause is what makes apply exactly-once: a replay updates no row, and
-- the caller returns the already-recorded outcome instead of applying twice.
UPDATE member_change_request_items
   SET applied_at = sqlc.arg(applied_at),
       applied_resource_kind = sqlc.arg(applied_resource_kind),
       applied_resource_id = sqlc.arg(applied_resource_id),
       applied_resource_version = sqlc.narg(applied_resource_version),
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
       version = version + 1
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
   AND status = 'approved'
   AND applied_at IS NULL
RETURNING *;
