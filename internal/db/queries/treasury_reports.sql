-- Treasury history and reporting reads.
--
-- Every query here is treasury-only. Nothing in this file is reachable without
-- payment.read or payment.export, and no result feeds a safe dues-standing
-- response.

-- name: ListLedgerPayments :many
--
-- The books view. effective_only = 1 hides the rows a correction has already
-- settled (reversals, and originals that were replaced), leaving what the club
-- currently holds. effective_only = 0 shows the whole audit trail.
--
-- Filters are all optional: 0 or '' means "no filter". They bind once in the
-- params CTE and join in as columns, which keeps each sqlc.arg out of the CASE
-- and WHERE expressions sqlc's SQLite analyzer struggles with.
--
-- Keep every comment in this file ASCII. sqlc substitutes sqlc.arg() by byte
-- offset, so a multi-byte character anywhere above a query shifts the offsets
-- and silently corrupts the SQL it hands the parser, which then fails on a
-- mangled LIMIT or OFFSET far from the real cause.
--
-- The correction join cannot fan out: the schema allows at most one correction
-- record per payment.
WITH params AS (
    SELECT CAST(sqlc.arg(membership_id)  AS INTEGER) AS want_membership_id,
           CAST(sqlc.arg(batch_id)       AS INTEGER) AS want_batch_id,
           CAST(sqlc.arg(method)         AS TEXT)    AS want_method,
           CAST(sqlc.arg(receipt_code)   AS TEXT)    AS want_receipt_code,
           CAST(sqlc.arg(received_from)  AS TEXT)    AS want_received_from,
           CAST(sqlc.arg(received_to)    AS TEXT)    AS want_received_to,
           CAST(sqlc.arg(effective_only) AS INTEGER) AS want_effective_only
)
SELECT p.id                  AS payment_id,
       p.membership_id       AS membership_id,
       p.batch_id            AS batch_id,
       p.amount_cents        AS amount_cents,
       p.method              AS method,
       p.reference           AS reference,
       p.received_on         AS received_on,
       p.received_by_officer AS received_by_officer,
       p.entered_by          AS entered_by,
       p.entered_at          AS entered_at,
       p.receipt_code        AS receipt_code,
       p.entry_kind          AS entry_kind,
       p.corrects_payment_id AS corrects_payment_id,
       p.treasurer_note      AS treasurer_note,
       per.display_name      AS display_name,
       per.call_sign         AS call_sign,
       CAST(CASE WHEN sup.id IS NULL THEN 0 ELSE 1 END AS INTEGER) AS is_superseded
  FROM payments p
  CROSS JOIN params
  JOIN memberships m ON m.id = p.membership_id
  JOIN persons per   ON per.id = m.person_id
  LEFT JOIN payment_corrections sup ON sup.original_payment_id = p.id
 WHERE (params.want_membership_id = 0 OR p.membership_id = params.want_membership_id)
   AND (params.want_batch_id = 0 OR p.batch_id = params.want_batch_id)
   AND (params.want_method = '' OR p.method = params.want_method)
   AND (params.want_receipt_code = '' OR p.receipt_code = params.want_receipt_code)
   AND (params.want_received_from = '' OR p.received_on >= params.want_received_from)
   AND (params.want_received_to = '' OR p.received_on <= params.want_received_to)
   AND (params.want_effective_only = 0
        OR (p.entry_kind <> 'reversal' AND sup.id IS NULL))
 ORDER BY p.received_on DESC, p.id DESC
 LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: GetPaymentWithMember :one
SELECT p.*,
       per.display_name AS display_name,
       per.call_sign    AS call_sign,
       m.base_type      AS base_type
  FROM payments p
  JOIN memberships m ON m.id = p.membership_id
  JOIN persons per   ON per.id = m.person_id
 WHERE p.id = ?;

-- name: ListCorrectionsByBatch :many
-- The corrections that touched a batch, for its plain-language activity log.
SELECT c.id                     AS correction_id,
       c.original_payment_id    AS original_payment_id,
       c.reversal_payment_id    AS reversal_payment_id,
       c.replacement_payment_id AS replacement_payment_id,
       c.reason                 AS reason,
       c.corrected_by           AS corrected_by,
       c.corrected_at           AS corrected_at,
       orig.receipt_code        AS original_receipt_code,
       orig.amount_cents        AS original_amount_cents,
       repl.amount_cents        AS replacement_amount_cents,
       per.display_name         AS display_name
  FROM payment_corrections c
  JOIN payments orig ON orig.id = c.original_payment_id
  JOIN payments repl ON repl.id = c.replacement_payment_id
  JOIN memberships m ON m.id = orig.membership_id
  JOIN persons per   ON per.id = m.person_id
 WHERE orig.batch_id = ?
 ORDER BY c.id;
