-- name: GetPerson :one
SELECT * FROM persons WHERE id = ?;

-- name: ListPersons :many
--
-- base_type comes from the person's current membership, so the list can say
-- what each person is. It was absent, and the members list rendered a dash in
-- the Type column for everyone while the record page showed the type
-- (bcars-portal-ges).
--
-- The subquery takes the most recent membership that has not ended, which is
-- the one the record page shows. A person with no membership yields NULL, and
-- the list still says "-" for them -- correctly, this time.
SELECT p.id, p.display_name, p.sort_name, p.call_sign, p.deceased_at, p.deactivated_at,
       p.version, p.created_at, p.updated_at,
       CAST(COALESCE((SELECT m.base_type FROM memberships m
                       WHERE m.person_id = p.id AND m.ended_on IS NULL
                       ORDER BY m.created_at DESC, m.id DESC LIMIT 1), '') AS TEXT) AS base_type
FROM persons p
WHERE p.deactivated_at IS NULL
ORDER BY p.sort_name
LIMIT ? OFFSET ?;

-- name: ListPersonsByName :many
--
-- The same columns as ListPersons, including the current membership's
-- base_type; see the note there.
SELECT p.id, p.display_name, p.sort_name, p.call_sign, p.deceased_at, p.deactivated_at,
       p.version, p.created_at, p.updated_at,
       CAST(COALESCE((SELECT m.base_type FROM memberships m
                       WHERE m.person_id = p.id AND m.ended_on IS NULL
                       ORDER BY m.created_at DESC, m.id DESC LIMIT 1), '') AS TEXT) AS base_type
FROM persons p
WHERE (p.display_name LIKE '%' || ? || '%' OR p.sort_name LIKE '%' || ? || '%')
  AND p.deactivated_at IS NULL
ORDER BY p.sort_name
LIMIT ? OFFSET ?;

-- name: GetPersonByCallSign :one
SELECT * FROM persons WHERE call_sign = ?;

-- name: CreatePerson :one
INSERT INTO persons (display_name, sort_name, call_sign, license_class, volunteer_examiner)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdatePerson :one
UPDATE persons
SET display_name = ?, sort_name = ?, call_sign = ?,
    license_class = ?, volunteer_examiner = ?,
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
