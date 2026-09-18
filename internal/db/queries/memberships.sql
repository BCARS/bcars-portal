-- name: GetMembership :one
SELECT * FROM memberships WHERE id = ?;

-- name: ListMembershipsByPerson :many
SELECT * FROM memberships WHERE person_id = ? ORDER BY created_at DESC;

-- name: CreateMembership :one
INSERT INTO memberships (person_id, base_type, lifecycle)
VALUES (?, ?, 'pending')
RETURNING *;

-- name: ApproveMembership :one
UPDATE memberships
SET lifecycle = 'approved', base_type = ?, joined_on = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: RejectMembership :one
UPDATE memberships
SET lifecycle = 'rejected',
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: TransitionLifecycle :one
UPDATE memberships
SET lifecycle = ?, ended_on = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: CreateMembershipApproval :one
INSERT INTO membership_approvals (membership_id, decision, approved_type, decided_by, decided_at, reason)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetFCCVerification :one
SELECT * FROM fcc_verifications WHERE id = ?;

-- name: ListFCCVerificationsByMembership :many
SELECT * FROM fcc_verifications WHERE membership_id = ? ORDER BY verified_at DESC;

-- name: CreateFCCVerification :one
INSERT INTO fcc_verifications (membership_id, call_sign, license_class, verification_source, verified_by, verified_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: RevokeFCCVerification :exec
UPDATE fcc_verifications
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    notes = ?
WHERE id = ?;

-- name: GetHonoraryGrant :one
SELECT * FROM honorary_grants WHERE id = ?;

-- name: ListHonoraryGrantsByMembership :many
SELECT * FROM honorary_grants WHERE membership_id = ? ORDER BY starts_on DESC;

-- name: CreateHonoraryGrant :one
INSERT INTO honorary_grants (membership_id, starts_on, ends_on, is_lifetime, reason, approved_by, approved_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- is_lifetime is written explicitly because the table CHECK forbids a lifetime
-- grant from carrying an end date; giving a grant an end date converts it to a
-- term grant.
-- name: UpdateHonoraryGrant :one
UPDATE honorary_grants
SET reason = ?, ends_on = ?, is_lifetime = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: ExpireHonoraryGrant :one
UPDATE honorary_grants
SET ends_on = date('now'),
    is_lifetime = 0,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: RevokeHonoraryGrant :one
UPDATE honorary_grants
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    revoked_by = ?,
    revoke_reason = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- A membership awaiting an officer's decision (bcars-portal-ges).
--
-- The predicate is `lifecycle = 'pending' AND ended_on IS NULL`, and it is
-- written twice on purpose: once here and once in CountPendingMemberships.
-- The dashboard tile and this list MUST agree, because a count that says 2
-- beside a list that shows 3 teaches an officer to trust neither. The test
-- that holds them together compares the count against the length of the list
-- over a fixture built to disagree -- an ended pending row, a rejected one, a
-- deactivated person's -- rather than trusting that two similar-looking WHERE
-- clauses stay similar.
--
-- A deactivated or deceased person's pending membership IS listed. It still
-- needs a decision, and the row says so, which is better than a queue that
-- quietly holds fewer rows than the number above it.
--
-- name: ListPendingMemberships :many
SELECT m.id          AS membership_id,
       m.person_id   AS person_id,
       m.base_type   AS base_type,
       m.created_at  AS requested_at,
       m.version     AS version,
       p.display_name,
       p.call_sign,
       p.deactivated_at,
       p.deceased_at
  FROM memberships m
  JOIN persons p ON p.id = m.person_id
 WHERE m.lifecycle = 'pending'
   AND m.ended_on IS NULL
 ORDER BY m.created_at, m.id
 LIMIT ? OFFSET ?;

-- name: CountPendingMemberships :one
--
-- The same population as ListPendingMemberships. See the note there.
SELECT count(*)
  FROM memberships m
  JOIN persons p ON p.id = m.person_id
 WHERE m.lifecycle = 'pending'
   AND m.ended_on IS NULL;
