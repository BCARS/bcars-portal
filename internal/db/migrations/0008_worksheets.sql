-- +goose Up

-- Renewal worksheets (docs/phase-2-design.md).
--
-- The printed worksheet is a supported workflow, not decoration. A treasurer
-- prints a sheet, carries it to a meeting, writes on it, and enters the results
-- later. For that to work the sheet must be reproducible: the run records what
-- was asked for, and the rows record what was printed, in the order it printed.
-- Regenerating months later must not silently produce a different sheet because
-- the underlying data moved.

CREATE TABLE dues_worksheet_runs (
    id             INTEGER PRIMARY KEY,
    label          TEXT,
    -- The date standing was judged against, which makes the sheet reproducible.
    as_of          TEXT    NOT NULL
                       CHECK (as_of GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    -- owes: everyone not currently covered.
    -- active: every active membership, covered or not.
    -- unpaid_since_run: rows from an earlier sheet with no payment posted since.
    filter_kind    TEXT    NOT NULL
                       CHECK (filter_kind IN ('owes', 'active', 'unpaid_since_run')),
    -- The earlier run this one follows up on, for filter_kind unpaid_since_run.
    source_run_id  INTEGER REFERENCES dues_worksheet_runs(id),
    sort_order     TEXT    NOT NULL
                       CHECK (sort_order IN ('last_name', 'call_sign', 'longest_overdue')),
    -- Contact columns are requested here and authorized server-side; a run can
    -- never carry a column the requester was not allowed to see.
    include_email  INTEGER NOT NULL DEFAULT 0 CHECK (include_email IN (0, 1)),
    include_phone  INTEGER NOT NULL DEFAULT 0 CHECK (include_phone IN (0, 1)),
    warning_days   INTEGER NOT NULL DEFAULT 60 CHECK (warning_days > 0),
    generated_by   INTEGER NOT NULL REFERENCES users(id),
    generated_at   TEXT    NOT NULL,
    row_count      INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK ((filter_kind = 'unpaid_since_run') = (source_run_id IS NOT NULL))
);
CREATE INDEX ix_worksheet_runs_generated ON dues_worksheet_runs(generated_at DESC, id DESC);

-- One row per member as printed. This is a snapshot, not a view: it is never
-- rewritten, so reprinting an old sheet reproduces the paper the treasurer
-- actually carried.

CREATE TABLE dues_worksheet_rows (
    id             INTEGER PRIMARY KEY,
    run_id         INTEGER NOT NULL REFERENCES dues_worksheet_runs(id) ON DELETE CASCADE,
    -- Stable print order. A batch created from this run reuses it, so the grid
    -- and the paper stay in step.
    ordinal        INTEGER NOT NULL,
    membership_id  INTEGER NOT NULL REFERENCES memberships(id),
    display_name   TEXT    NOT NULL,
    call_sign      TEXT,
    base_type      TEXT    NOT NULL,
    dues_status    TEXT    NOT NULL,
    paid_through   TEXT,
    -- Contact values as they stood when the sheet was generated. The run's
    -- generated_at is the "good as of" stamp a reader needs to judge them.
    email          TEXT,
    phone          TEXT,
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_worksheet_rows_ordinal ON dues_worksheet_rows(run_id, ordinal);
CREATE UNIQUE INDEX ux_worksheet_rows_membership ON dues_worksheet_rows(run_id, membership_id);
CREATE INDEX ix_worksheet_rows_membership ON dues_worksheet_rows(membership_id);

-- pma.1 deliberately left this column out until the worksheet tables existed.
-- A batch created from a sheet names it, so a later print can tell which rows
-- have since been entered.
ALTER TABLE payment_batches ADD COLUMN worksheet_run_id INTEGER
    REFERENCES dues_worksheet_runs(id);

-- +goose Down

ALTER TABLE payment_batches DROP COLUMN worksheet_run_id;

DROP TABLE IF EXISTS dues_worksheet_rows;
DROP TABLE IF EXISTS dues_worksheet_runs;
