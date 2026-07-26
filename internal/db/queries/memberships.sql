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

-- name: UpdateHonoraryGrant :one
UPDATE honorary_grants
SET reason = ?, ends_on = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: RevokeHonoraryGrant :exec
UPDATE honorary_grants
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    revoked_by = ?,
    revoke_reason = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?;
