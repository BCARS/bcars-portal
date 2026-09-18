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

-- name: CountUnresolvedManualRows :one
SELECT count(*) FROM staged_import_rows
WHERE import_run_id = ? AND requires_manual = 1
AND id NOT IN (
    SELECT DISTINCT staged_import_row_id FROM reconciliation_decisions
);

-- name: GetImportRunByIdempotencyKey :one
SELECT * FROM import_runs WHERE idempotency_key = ?;

-- name: UpdateStagedRowAction :one
UPDATE staged_import_rows
SET proposed_action = ?, requires_manual = ?, manual_reason = ?
WHERE id = ?
RETURNING *;

-- name: CountStagedRowsByAction :many
SELECT proposed_action, count(*) as cnt FROM staged_import_rows
WHERE import_run_id = ?
GROUP BY proposed_action;

-- name: CreateExternalID :one
INSERT INTO external_ids (entity_kind, entity_id, system, external_id)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: FindExternalID :one
SELECT * FROM external_ids WHERE system = ? AND external_id = ?;

-- name: ListExternalIDsForEntity :many
SELECT * FROM external_ids WHERE entity_kind = ? AND entity_id = ?;

-- name: FindImportCoverageEvent :one
-- Guards the import cutover against re-importing the same paid-through value:
-- a second import of unchanged data must not append a duplicate decision.
SELECT * FROM coverage_events
WHERE membership_id = ? AND paid_through = ? AND reason_kind = 'import'
ORDER BY id DESC
LIMIT 1;

-- name: ListRunDecisions :many
--
-- The officer decisions recorded against a run's staged rows
-- (bcars-portal-7kp).
--
-- Recording a decision clears requires_manual on the row, which is right --
-- the row no longer needs one -- but it left the import page unable to tell a
-- row the matcher resolved from a row a person ruled on. Both read as "Auto",
-- the Auto-Resolvable tile counted them together, and the decision was visible
-- nowhere. The page an officer reviews before committing real member data
-- overstated how much of the run was automatic.
--
-- The decider is joined in by email because "decided by an officer" without
-- saying which one is only half an audit trail on the screen that matters.
SELECT d.staged_import_row_id AS staged_import_row_id,
       d.action               AS decision_action,
       d.decided_at           AS decided_at,
       d.decided_by           AS decided_by,
       u.email                AS decided_by_email
  FROM reconciliation_decisions d
  JOIN staged_import_rows s ON s.id = d.staged_import_row_id
  JOIN users u ON u.id = d.decided_by
 WHERE s.import_run_id = ?
 ORDER BY d.decided_at, d.id;
