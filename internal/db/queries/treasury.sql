-- Phase 2 ledger primitives. Domain services compose these; no query here
-- derives a paid-through date from an amount.

-- name: GetDuesRate :one
SELECT * FROM dues_rates WHERE year = ?;

-- name: ListDuesRates :many
SELECT * FROM dues_rates ORDER BY year DESC;

-- name: UpsertDuesRate :one
INSERT INTO dues_rates (year, amount_cents, note, set_by, set_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (year) DO UPDATE
SET amount_cents = excluded.amount_cents,
    note         = excluded.note,
    set_by       = excluded.set_by,
    set_at       = excluded.set_at,
    version      = dues_rates.version + 1,
    updated_at   = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
RETURNING *;

-- name: CreatePaymentBatch :one
INSERT INTO payment_batches (label, default_amount_cents, default_paid_through, opened_by, opened_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPaymentBatch :one
SELECT * FROM payment_batches WHERE id = ?;

-- name: ListPaymentBatchesByState :many
SELECT * FROM payment_batches WHERE state = ? ORDER BY opened_at DESC, id DESC;

-- name: MarkPaymentBatchPosted :one
UPDATE payment_batches
SET state = 'posted', posted_by = ?, posted_at = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ? AND state = 'open'
RETURNING *;

-- name: MarkPaymentBatchAbandoned :one
UPDATE payment_batches
SET state = 'abandoned', abandoned_by = ?, abandoned_at = ?, abandon_reason = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND version = ? AND state = 'open'
RETURNING *;

-- name: CreatePaymentBatchEntry :one
INSERT INTO payment_batch_entries (
    batch_id, membership_id, sequence, amount_cents, method, reference,
    received_on, received_by_officer, paid_through, treasurer_note
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListPaymentBatchEntries :many
SELECT * FROM payment_batch_entries WHERE batch_id = ? ORDER BY sequence, id;

-- name: DeletePaymentBatchEntry :execresult
DELETE FROM payment_batch_entries WHERE id = ? AND version = ?;

-- name: SumPaymentBatchEntries :one
SELECT COUNT(*) AS entry_count, COALESCE(SUM(amount_cents), 0) AS total_cents
FROM payment_batch_entries WHERE batch_id = ?;

-- name: CreatePayment :one
INSERT INTO payments (
    membership_id, batch_id, amount_cents, method, reference, received_on,
    received_by_officer, entered_by, entered_at, receipt_code, entry_kind,
    corrects_payment_id, treasurer_note
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = ?;

-- name: ListPaymentsByMembership :many
SELECT * FROM payments WHERE membership_id = ? ORDER BY received_on DESC, id DESC;

-- name: ListPaymentsByBatch :many
SELECT * FROM payments WHERE batch_id = ? ORDER BY id;

-- name: CreatePaymentCorrection :one
INSERT INTO payment_corrections (
    original_payment_id, reversal_payment_id, replacement_payment_id,
    reason, corrected_by, corrected_at
) VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPaymentCorrectionByOriginal :one
SELECT * FROM payment_corrections WHERE original_payment_id = ?;

-- name: CreateCoverageEvent :one
INSERT INTO coverage_events (
    membership_id, paid_through, reason_kind, reason, payment_id,
    import_run_id, supersedes_event_id, source_note, decided_by, decided_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListCoverageEventsByMembership :many
SELECT * FROM coverage_events
WHERE membership_id = ?
ORDER BY decided_at DESC, id DESC;

-- name: GetEffectiveCoverageEvent :one
-- The currently effective decision is the newest event that nothing supersedes.
SELECT * FROM coverage_events c
WHERE c.membership_id = ?
  AND NOT EXISTS (SELECT 1 FROM coverage_events s WHERE s.supersedes_event_id = c.id)
ORDER BY c.decided_at DESC, c.id DESC
LIMIT 1;

-- name: GetIdempotencyRecord :one
SELECT * FROM idempotency_records
WHERE actor_user_id = ? AND operation = ? AND idempotency_key = ?;

-- name: CreateIdempotencyRecord :one
INSERT INTO idempotency_records (
    actor_user_id, operation, idempotency_key, request_hash, resource_kind, resource_id
) VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;
