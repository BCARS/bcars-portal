-- Derived dues standing and coverage reads.
--
-- Standing is never stored. It is computed as of an explicit date so that
-- tests, worksheets, and reports are deterministic. `expiring` in particular is
-- a classification of this query, not a state any row carries.

-- name: ListDuesStanding :many
--
-- One row per membership with its effective coverage decision, any active
-- honorary waiver, and the derived status. Serves both the filtered list and
-- the single-membership lookup: pass membership_id = 0 to list, or a specific
-- id to fetch one. include_ended = 1 keeps resigned/rejected/deceased rows,
-- which the single lookup needs and the working list does not want.
--
-- The status filter below repeats the CASE arms as predicates rather than
-- filtering on the alias, which SQLite does not allow in WHERE. Each arm must
-- stay the exact complement of its CASE arm, or a filtered list would return
-- rows labelled with a status other than the one that was asked for.
WITH params AS (
    -- sqlc's SQLite analyzer cannot resolve a table alias inside a CASE that
    -- also contains a bound parameter, so the two dates are bound once here
    -- and joined in as ordinary columns.
    SELECT CAST(sqlc.arg(as_of)        AS TEXT) AS as_of_date,
           CAST(sqlc.arg(warn_through) AS TEXT) AS warn_date
),
effective_coverage AS (
    -- The effective decision is the newest coverage event that nothing
    -- supersedes. Superseded events stay readable as history.
    SELECT c.membership_id AS ec_membership_id,
           c.id            AS ec_coverage_event_id,
           c.paid_through  AS ec_paid_through
      FROM coverage_events c
     WHERE NOT EXISTS (SELECT 1 FROM coverage_events s WHERE s.supersedes_event_id = c.id)
       AND c.id = (
           SELECT c2.id FROM coverage_events c2
            WHERE c2.membership_id = c.membership_id
              AND NOT EXISTS (SELECT 1 FROM coverage_events s2 WHERE s2.supersedes_event_id = c2.id)
            ORDER BY c2.decided_at DESC, c2.id DESC
            LIMIT 1
       )
),
active_honorary AS (
    -- An honorary waiver is active when it has started, has not ended, and was
    -- never revoked, all judged as of the requested date.
    SELECT g.membership_id                                    AS ah_membership_id,
           MAX(g.is_lifetime)                                 AS ah_is_lifetime,
           MAX(CASE WHEN g.ends_on IS NULL THEN 1 ELSE 0 END) AS ah_is_open_ended,
           MAX(g.ends_on)                                     AS ah_ends_on
      FROM honorary_grants g
     WHERE g.revoked_at IS NULL
       AND g.starts_on <= (SELECT as_of_date FROM params)
       AND (g.ends_on IS NULL OR g.ends_on >= (SELECT as_of_date FROM params))
     GROUP BY g.membership_id
)
SELECT m.id           AS membership_id,
       m.person_id    AS person_id,
       m.base_type    AS base_type,
       m.lifecycle    AS lifecycle,
       p.display_name AS display_name,
       p.call_sign    AS call_sign,
       ec.ec_coverage_event_id AS coverage_event_id,
       ec.ec_paid_through      AS paid_through,
       CAST(COALESCE(ah.ah_is_lifetime, 0)   AS INTEGER) AS honorary_is_lifetime,
       CAST(COALESCE(ah.ah_is_open_ended, 0) AS INTEGER) AS honorary_is_open_ended,
       CAST(COALESCE(ah.ah_ends_on, '')      AS TEXT)    AS honorary_ends_on,
       CASE
           -- An honorary waiver decides dues standing outright. It never
           -- changes the underlying Full or Associate membership rights,
           -- which base_type still reports.
           WHEN ah.ah_membership_id IS NOT NULL      THEN 'honorary_waived'
           WHEN ec.ec_paid_through IS NULL           THEN 'unknown'
           WHEN ec.ec_paid_through < params.as_of_date THEN 'expired'
           WHEN ec.ec_paid_through <= params.warn_date  THEN 'expiring'
           ELSE 'current'
       END AS status
  FROM memberships m
  CROSS JOIN params
  JOIN persons p ON p.id = m.person_id
  LEFT JOIN effective_coverage ec ON ec.ec_membership_id = m.id
  LEFT JOIN active_honorary    ah ON ah.ah_membership_id = m.id
 WHERE (CAST(sqlc.arg(include_ended) AS INTEGER) = 1
        OR m.lifecycle NOT IN ('rejected', 'resigned', 'deceased'))
   AND (CAST(sqlc.arg(membership_id) AS INTEGER) = 0
        OR m.id = CAST(sqlc.arg(membership_id) AS INTEGER))
   AND (CAST(sqlc.arg(search) AS TEXT) = ''
        OR p.display_name LIKE '%' || CAST(sqlc.arg(search) AS TEXT) || '%'
        OR COALESCE(p.call_sign, '') LIKE '%' || CAST(sqlc.arg(search) AS TEXT) || '%')
   AND (CAST(sqlc.arg(status_filter) AS TEXT) = ''
        OR (CAST(sqlc.arg(status_filter) AS TEXT) = 'honorary_waived'
            AND ah.ah_membership_id IS NOT NULL)
        OR (CAST(sqlc.arg(status_filter) AS TEXT) = 'unknown'
            AND ah.ah_membership_id IS NULL
            AND ec.ec_paid_through IS NULL)
        OR (CAST(sqlc.arg(status_filter) AS TEXT) = 'expired'
            AND ah.ah_membership_id IS NULL
            AND ec.ec_paid_through IS NOT NULL
            AND ec.ec_paid_through < params.as_of_date)
        OR (CAST(sqlc.arg(status_filter) AS TEXT) = 'expiring'
            AND ah.ah_membership_id IS NULL
            AND ec.ec_paid_through >= params.as_of_date
            AND ec.ec_paid_through <= params.warn_date)
        OR (CAST(sqlc.arg(status_filter) AS TEXT) = 'current'
            AND ah.ah_membership_id IS NULL
            AND ec.ec_paid_through > params.warn_date))
 ORDER BY p.sort_name, m.id
 LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: ListCoverageEventsPage :many
SELECT * FROM coverage_events
WHERE membership_id = ?
ORDER BY decided_at DESC, id DESC
LIMIT ? OFFSET ?;

-- name: GetCoverageEvent :one
SELECT * FROM coverage_events WHERE id = ?;

-- name: InsertDuesRate :one
INSERT INTO dues_rates (year, amount_cents, note, set_by, set_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateDuesRate :one
-- Version-guarded so a revision cannot silently overwrite another officer's.
UPDATE dues_rates
SET amount_cents = ?, note = ?, set_by = ?, set_at = ?,
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE year = ? AND version = ?
RETURNING *;
