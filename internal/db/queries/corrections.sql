-- Posted-payment corrections.
--
-- Nothing here updates or deletes a payment. A correction appends a signed
-- reversal and a positive replacement and links the three together, so the
-- original stays exactly as it was recorded.

-- Walking a chain forward uses GetPaymentCorrectionByOriginal from
-- treasury.sql: the correction that superseded a payment is the one whose
-- original_payment_id is that payment.

-- name: GetBatchLedgerTotals :one
-- Net totals over the posted ledger rather than the frozen draft entries. After
-- a correction the two legitimately differ: the entries record what was typed,
-- the ledger records what the club actually holds.
SELECT
    CAST(COUNT(*) AS INTEGER)                                                                  AS payment_count,
    CAST(COALESCE(SUM(amount_cents), 0) AS INTEGER)                                            AS net_total_cents,
    CAST(COALESCE(SUM(CASE WHEN method = 'cash'  THEN amount_cents ELSE 0 END), 0) AS INTEGER) AS cash_total_cents,
    CAST(COALESCE(SUM(CASE WHEN method = 'check' THEN amount_cents ELSE 0 END), 0) AS INTEGER) AS check_total_cents,
    CAST(COALESCE(SUM(CASE WHEN method = 'other' THEN amount_cents ELSE 0 END), 0) AS INTEGER) AS other_total_cents
FROM payments
WHERE batch_id = ?;

-- name: GetCoverageEventByPayment :one
-- The coverage decision a given payment granted, if it granted one.
SELECT * FROM coverage_events
WHERE payment_id = ?
ORDER BY id DESC
LIMIT 1;
