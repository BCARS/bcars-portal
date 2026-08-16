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
-- The officer queue. Every filter is optional: pass NULL for status, source, or
-- requester to ignore it, and 0 for untargeted_only to include linked requests.
--
-- Ordering is submitted_at DESC with an id tie-breaker so a page boundary is
-- deterministic. Without the tie-breaker two requests recorded in the same
-- millisecond could swap places between calls and hide a row from a caller
-- paging through the queue.
SELECT r.*,
       p.display_name AS target_display_name
  FROM member_change_requests r
  LEFT JOIN persons p ON p.id = r.target_person_id
 WHERE (sqlc.narg(status) IS NULL OR r.status = sqlc.narg(status))
   AND (sqlc.narg(source) IS NULL OR r.source = sqlc.narg(source))
   AND (sqlc.narg(requester_user_id) IS NULL
        OR r.requester_user_id = sqlc.narg(requester_user_id))
   AND (sqlc.arg(untargeted_only) = 0
        OR (r.target_person_id IS NULL
            AND r.status NOT IN ('resolved', 'withdrawn')))
 ORDER BY r.submitted_at DESC, r.id DESC
 -- Named, not bare `?`. sqlc numbers named parameters (?3, ?4, ...) and leaves
 -- a bare `?` positional, so mixing the two in one query makes SQLite expect
 -- arguments at indices the generated call never binds. That fails at runtime
 -- with "missing argument with index N", not at generation time.
 LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

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
--
-- applied_value is what reached the record, which since ADR-0014 need not be
-- what the member proposed. Always pass it, using the empty string for the
-- operations that set no value: NULL in this column means "applied before the
-- portal recorded this" and must keep meaning only that (migration 0016).
UPDATE member_change_request_items
   SET applied_at = sqlc.arg(applied_at),
       applied_value = sqlc.arg(applied_value),
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
