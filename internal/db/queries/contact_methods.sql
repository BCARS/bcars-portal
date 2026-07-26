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

-- name: ArchiveContactMethod :exec
UPDATE contact_methods
SET archived_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?;

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
