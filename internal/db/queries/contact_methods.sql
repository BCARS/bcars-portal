-- name: ListContactMethods :many
SELECT * FROM contact_methods
WHERE person_id = ? AND archived_at IS NULL
ORDER BY is_primary DESC, kind, id;

-- name: GetContactMethod :one
SELECT * FROM contact_methods WHERE id = ?;

-- name: CreateContactMethod :one
INSERT INTO contact_methods (person_id, kind, label, value_raw, value_norm, is_primary,
    postal_line1, postal_line2, postal_city, postal_state, postal_postal_code, postal_country)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateContactMethod :one
UPDATE contact_methods
SET label = ?, value_raw = ?, value_norm = ?,
    postal_line1 = ?, postal_line2 = ?, postal_city = ?,
    postal_state = ?, postal_postal_code = ?, postal_country = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: ArchiveContactMethod :one
UPDATE contact_methods
SET archived_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- ClearPrimaryForPerson and SetPrimary are DELIBERATELY unconditional: they
-- are the two halves of "make this contact method the primary one", a
-- set-valued operation with no single row whose version the caller could hold.
-- Adding a version parameter would check one row while the sweep across the
-- others stayed unchecked, which is worse than checking nothing.
--
-- They still bump version, and that is correct: a caller holding a stale token
-- for an affected row must fail on their NEXT targeted update rather than
-- overwrite a change they never saw.
--
-- Every OTHER mutation in this file that takes a version parameter must be
-- :one with RETURNING so a conflict is detectable; see scripts/check-version-conflicts.sh.
-- name: ClearPrimaryForPerson :exec
UPDATE contact_methods
SET is_primary = 0,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE person_id = ? AND is_primary = 1 AND archived_at IS NULL;

-- name: SetPrimary :exec
UPDATE contact_methods
SET is_primary = 1,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: FindContactMethodByNorm :many
SELECT * FROM contact_methods
WHERE kind = ? AND value_norm = ? AND archived_at IS NULL;
