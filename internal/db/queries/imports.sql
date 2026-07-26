-- name: CreateImportRun :one
INSERT INTO import_runs (source_kind, source_filename, source_sha256, uploaded_by, uploaded_at, status, idempotency_key)
VALUES (?, ?, ?, ?, ?, 'uploaded', ?)
RETURNING *;

-- name: GetImportRun :one
SELECT * FROM import_runs WHERE id = ?;

-- name: ListImportRuns :many
SELECT * FROM import_runs ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: UpdateImportRunStatus :one
UPDATE import_runs
SET status = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: CommitImportRun :one
UPDATE import_runs
SET status = 'committed',
    committed_by = ?,
    committed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    result_summary_json = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: CreateStagedRow :one
INSERT INTO staged_import_rows (import_run_id, source_row_index, source_external_id, raw_json, normalized_json, match_person_id, match_method, proposed_action, proposed_changes_json, validation_errors_json, requires_manual, manual_reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListStagedRows :many
SELECT * FROM staged_import_rows
WHERE import_run_id = ?
ORDER BY source_row_index
LIMIT ? OFFSET ?;

-- name: ListStagedRowsRequiringManual :many
SELECT * FROM staged_import_rows
WHERE import_run_id = ? AND requires_manual = 1
ORDER BY source_row_index
LIMIT ? OFFSET ?;

-- name: GetStagedRow :one
SELECT * FROM staged_import_rows WHERE id = ?;

-- name: CreateReconciliationDecision :one
INSERT INTO reconciliation_decisions (staged_import_row_id, decided_by, decided_at, action, payload_json)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: ListDecisionsForRow :many
SELECT * FROM reconciliation_decisions
WHERE staged_import_row_id = ?
ORDER BY decided_at;

-- name: CreateExternalID :one
INSERT INTO external_ids (entity_kind, entity_id, system, external_id)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: FindExternalID :one
SELECT * FROM external_ids WHERE system = ? AND external_id = ?;

-- name: ListExternalIDsForEntity :many
SELECT * FROM external_ids WHERE entity_kind = ? AND entity_id = ?;
