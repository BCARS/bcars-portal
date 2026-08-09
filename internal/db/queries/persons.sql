-- name: GetPerson :one
SELECT * FROM persons WHERE id = ?;

-- name: ListPersons :many
SELECT id, display_name, sort_name, call_sign, deceased_at, deactivated_at, version, created_at, updated_at
FROM persons
WHERE deactivated_at IS NULL
ORDER BY sort_name
LIMIT ? OFFSET ?;

-- name: ListPersonsByName :many
SELECT id, display_name, sort_name, call_sign, deceased_at, deactivated_at, version, created_at, updated_at
FROM persons
WHERE (display_name LIKE '%' || ? || '%' OR sort_name LIKE '%' || ? || '%')
  AND deactivated_at IS NULL
ORDER BY sort_name
LIMIT ? OFFSET ?;

-- name: GetPersonByCallSign :one
SELECT * FROM persons WHERE call_sign = ?;

-- name: CreatePerson :one
INSERT INTO persons (display_name, sort_name, call_sign)
VALUES (?, ?, ?)
RETURNING *;

-- name: UpdatePerson :one
UPDATE persons
SET display_name = ?, sort_name = ?, call_sign = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: DeactivatePerson :one
UPDATE persons
SET deactivated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: ReactivatePerson :one
UPDATE persons
SET deactivated_at = NULL,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: MarkDeceased :one
UPDATE persons
SET deceased_at = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;
