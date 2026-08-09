-- Draft payment batches and their mutable entries.
--
-- Nothing in this file writes a payment or a coverage event. A draft batch is
-- deliberately inert until it is posted.

-- name: ListPaymentBatches :many
SELECT * FROM payment_batches
WHERE (CAST(sqlc.arg(state_filter) AS TEXT) = ''
       OR state = CAST(sqlc.arg(state_filter) AS TEXT))
ORDER BY opened_at DESC, id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: UpdatePaymentBatchDefaults :one
-- Version-guarded, and refuses a terminal batch: a posted or abandoned batch
-- accepts no further changes.
UPDATE payment_batches
SET label = ?, default_amount_cents = ?, default_paid_through = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ? AND state = 'open'
RETURNING *;

-- name: TouchPaymentBatch :one
-- Bumps the batch version after an entry mutation, so a browser holding a
-- stale batch ETag cannot post a batch whose rows have since changed.
UPDATE payment_batches
SET version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND state = 'open'
RETURNING *;

-- name: GetPaymentBatchEntry :one
SELECT * FROM payment_batch_entries WHERE id = ?;

-- name: NextPaymentBatchEntrySequence :one
-- Sequence is stable and server-assigned: removing row 2 never renumbers row 3,
-- so a printed worksheet and the grid keep agreeing.
SELECT CAST(COALESCE(MAX(sequence), 0) + 1 AS INTEGER) AS next_sequence
FROM payment_batch_entries WHERE batch_id = ?;

-- name: UpdatePaymentBatchEntry :one
UPDATE payment_batch_entries
SET membership_id = ?, amount_cents = ?, method = ?, reference = ?,
    received_on = ?, received_by_officer = ?, paid_through = ?, treasurer_note = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ?
RETURNING *;

-- name: GetPaymentBatchTotals :one
-- Totals are always calculated by the server. A client never submits one.
SELECT
    COUNT(*)                                                          AS entry_count,
    CAST(COALESCE(SUM(amount_cents), 0) AS INTEGER)                   AS net_total_cents,
    CAST(COALESCE(SUM(CASE WHEN method = 'cash'  THEN 1 ELSE 0 END), 0) AS INTEGER)            AS cash_count,
    CAST(COALESCE(SUM(CASE WHEN method = 'cash'  THEN amount_cents ELSE 0 END), 0) AS INTEGER) AS cash_total_cents,
    CAST(COALESCE(SUM(CASE WHEN method = 'check' THEN 1 ELSE 0 END), 0) AS INTEGER)            AS check_count,
    CAST(COALESCE(SUM(CASE WHEN method = 'check' THEN amount_cents ELSE 0 END), 0) AS INTEGER) AS check_total_cents,
    CAST(COALESCE(SUM(CASE WHEN method = 'other' THEN 1 ELSE 0 END), 0) AS INTEGER)            AS other_count,
    CAST(COALESCE(SUM(CASE WHEN method = 'other' THEN amount_cents ELSE 0 END), 0) AS INTEGER) AS other_total_cents
FROM payment_batch_entries
WHERE batch_id = ?;

-- name: CompleteIdempotencyRecord :exec
-- Records which resource a claimed key produced, so a retry can return the
-- original result instead of doing the work twice.
UPDATE idempotency_records
SET resource_kind = ?, resource_id = ?
WHERE id = ?;
