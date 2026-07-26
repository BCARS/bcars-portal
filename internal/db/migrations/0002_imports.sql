-- +goose Up

-- §3.7 Import staging

CREATE TABLE import_runs (
    id                  INTEGER PRIMARY KEY,
    source_kind         TEXT    NOT NULL,
    source_filename     TEXT    NOT NULL,
    source_sha256       TEXT    NOT NULL,
    uploaded_by         INTEGER NOT NULL REFERENCES users(id),
    uploaded_at         TEXT    NOT NULL,
    status              TEXT    NOT NULL,
    idempotency_key     TEXT    NOT NULL,
    committed_by        INTEGER REFERENCES users(id),
    committed_at        TEXT,
    result_summary_json TEXT,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version             INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX ux_import_runs_idem ON import_runs(idempotency_key);

CREATE TABLE staged_import_rows (
    id                     INTEGER PRIMARY KEY,
    import_run_id          INTEGER NOT NULL REFERENCES import_runs(id) ON DELETE CASCADE,
    source_row_index       INTEGER NOT NULL,
    source_external_id     TEXT,
    raw_json               TEXT    NOT NULL,
    normalized_json        TEXT    NOT NULL,
    match_person_id        INTEGER REFERENCES persons(id),
    match_method           TEXT,
    proposed_action        TEXT    NOT NULL,
    proposed_changes_json  TEXT,
    validation_errors_json TEXT,
    requires_manual        INTEGER NOT NULL DEFAULT 0,
    manual_reason          TEXT,
    created_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_staged_rows_run ON staged_import_rows(import_run_id, source_row_index);

CREATE TABLE reconciliation_decisions (
    id                   INTEGER PRIMARY KEY,
    staged_import_row_id INTEGER NOT NULL REFERENCES staged_import_rows(id) ON DELETE CASCADE,
    decided_by           INTEGER NOT NULL REFERENCES users(id),
    decided_at           TEXT    NOT NULL,
    action               TEXT    NOT NULL,
    payload_json         TEXT,
    created_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_reconcile_row ON reconciliation_decisions(staged_import_row_id);

-- +goose Down

DROP TABLE IF EXISTS reconciliation_decisions;
DROP TABLE IF EXISTS staged_import_rows;
DROP TABLE IF EXISTS import_runs;
