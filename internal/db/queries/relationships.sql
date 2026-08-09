-- Informational person relationships.
--
-- These queries are read and write only against person_relationships. None of
-- them touches member_access_grants, sessions, users, or role_capabilities,
-- because a relationship confers no authority. Adding a spouse does not let
-- either person see the other's record; an officer grants that separately.
--
-- Keep every comment in this file ASCII: sqlc substitutes sqlc.arg() by byte
-- offset, so a multi-byte character above a query corrupts the SQL it parses.

-- name: CreatePersonRelationship :one
INSERT INTO person_relationships (
    from_person_id, to_person_id, kind, context, created_by
) VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPersonRelationship :one
SELECT * FROM person_relationships WHERE id = ?;

-- name: ListRelationshipsForPerson :many
--
-- Both directions, active only. The related person's name comes along so a
-- caller does not need a second query; nothing else about them is exposed.
SELECT r.id             AS relationship_id,
       r.kind           AS kind,
       r.context        AS context,
       r.from_person_id AS from_person_id,
       r.to_person_id   AS to_person_id,
       r.created_at     AS created_at,
       r.version        AS version,
       CAST(CASE WHEN r.from_person_id = sqlc.arg(person_id) THEN 'outgoing'
                 ELSE 'incoming' END AS TEXT) AS direction,
       other.display_name AS other_display_name,
       other.call_sign    AS other_call_sign
  FROM person_relationships r
  JOIN persons other
    ON other.id = CASE WHEN r.from_person_id = sqlc.arg(person_id)
                       THEN r.to_person_id ELSE r.from_person_id END
 WHERE (r.from_person_id = sqlc.arg(person_id) OR r.to_person_id = sqlc.arg(person_id))
   AND r.archived_at IS NULL
 ORDER BY other.sort_name, r.id;

-- name: UpdatePersonRelationship :one
UPDATE person_relationships
   SET kind = sqlc.arg(kind),
       context = sqlc.narg(context),
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
       version = version + 1
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
   AND archived_at IS NULL
RETURNING *;

-- name: ArchivePersonRelationship :one
UPDATE person_relationships
   SET archived_at = sqlc.arg(archived_at),
       archived_by = sqlc.arg(archived_by),
       archive_reason = sqlc.narg(archive_reason),
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
       version = version + 1
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
   AND archived_at IS NULL
RETURNING *;
