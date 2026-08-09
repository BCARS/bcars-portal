-- Renewal worksheet runs and their snapshot rows.
--
-- Keep every comment in this file ASCII: sqlc substitutes sqlc.arg() by byte
-- offset, so a multi-byte character above a query corrupts the SQL it parses.

-- name: CreateWorksheetRun :one
INSERT INTO dues_worksheet_runs (
    label, as_of, filter_kind, source_run_id, sort_order,
    include_email, include_phone, warning_days, generated_by, generated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: SetWorksheetRunRowCount :exec
UPDATE dues_worksheet_runs SET row_count = ? WHERE id = ?;

-- name: GetWorksheetRun :one
SELECT * FROM dues_worksheet_runs WHERE id = ?;

-- name: ListWorksheetRuns :many
SELECT * FROM dues_worksheet_runs
ORDER BY generated_at DESC, id DESC
LIMIT ? OFFSET ?;

-- name: CreateWorksheetRow :one
INSERT INTO dues_worksheet_rows (
    run_id, ordinal, membership_id, display_name, call_sign,
    base_type, dues_status, paid_through, email, phone
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListWorksheetRows :many
--
-- Rows as printed, plus whether a payment has been posted for that member since
-- the sheet was generated. The snapshot itself is never rewritten; this flag is
-- computed at read time, so an old sheet keeps saying what it said while still
-- telling the treasurer which lines are already done.
SELECT r.id            AS row_id,
       r.run_id        AS run_id,
       r.ordinal       AS ordinal,
       r.membership_id AS membership_id,
       r.display_name  AS display_name,
       r.call_sign     AS call_sign,
       r.base_type     AS base_type,
       r.dues_status   AS dues_status,
       r.paid_through  AS paid_through,
       r.email         AS email,
       r.phone         AS phone,
       CAST(CASE WHEN EXISTS (
           SELECT 1 FROM payments pay
            WHERE pay.membership_id = r.membership_id
              AND pay.entry_kind <> 'reversal'
              AND pay.entered_at > run.generated_at
       ) THEN 1 ELSE 0 END AS INTEGER) AS entered_since
  FROM dues_worksheet_rows r
  JOIN dues_worksheet_runs run ON run.id = r.run_id
 WHERE r.run_id = ?
 ORDER BY r.ordinal
 LIMIT ? OFFSET ?;

-- name: ListWorksheetMembershipsUnpaidSince :many
-- The memberships on an earlier sheet with no payment posted since it ran.
SELECT r.membership_id AS membership_id
  FROM dues_worksheet_rows r
  JOIN dues_worksheet_runs run ON run.id = r.run_id
 WHERE r.run_id = ?
   AND NOT EXISTS (
       SELECT 1 FROM payments pay
        WHERE pay.membership_id = r.membership_id
          AND pay.entry_kind <> 'reversal'
          AND pay.entered_at > run.generated_at
   )
 ORDER BY r.ordinal;

-- name: GetPrimaryContact :one
--
-- One active contact per kind for the worksheet snapshot: the one marked
-- primary, or failing that the lowest active id.
--
-- The previous version took MAX over every active value and never read
-- is_primary, so a member with two active addresses had whichever sorted later
-- printed on the sheet. Lexical order is not a proxy for what the member asked
-- to be contacted on. The id fallback is arbitrary but stable and explainable:
-- the earliest recorded contact wins when nobody has chosen.
SELECT
    CAST(COALESCE((
        SELECT c.value_norm FROM contact_methods c
         WHERE c.person_id = sqlc.arg(person_id)
           AND c.kind = 'email'
           AND c.archived_at IS NULL
         ORDER BY c.is_primary DESC, c.id
         LIMIT 1
    ), '') AS TEXT) AS email,
    CAST(COALESCE((
        SELECT c.value_raw FROM contact_methods c
         WHERE c.person_id = sqlc.arg(person_id)
           AND c.kind = 'phone'
           AND c.archived_at IS NULL
         ORDER BY c.is_primary DESC, c.id
         LIMIT 1
    ), '') AS TEXT) AS phone;

-- name: SetPaymentBatchWorksheetRun :exec
UPDATE payment_batches SET worksheet_run_id = ? WHERE id = ?;
