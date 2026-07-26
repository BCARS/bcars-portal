-- name: ListNotes :many
SELECT * FROM notes
WHERE subject_kind = ? AND subject_id = ?
ORDER BY created_at DESC
LIMIT ? OFFSET ?;

-- name: GetNote :one
SELECT * FROM notes WHERE id = ?;

-- name: CreateNote :one
INSERT INTO notes (subject_kind, subject_id, category, visibility, body, author_id, source)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateNote :one
UPDATE notes
SET body = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: CreateNoteRevision :one
INSERT INTO note_revisions (note_id, body, edited_by, edited_at, reason)
VALUES (?, ?, ?, ?, ?)
RETURNING *;
