-- +goose Up

-- Phase 2 ledger foundation (docs/phase-2-design.md, ADR-0007).
--
-- Money received and coverage granted are separate facts. Nothing here derives
-- a paid-through date from an amount; the treasurer always states both.
--
-- Conventions carried from 0001_init.sql: money is integer cents, dates are
-- ISO 'YYYY-MM-DD' text, timestamps are UTC ISO-8601 text.

-- §1 Dues rates ------------------------------------------------------------
--
-- Informs suggestions only. It is never a validation rule: the treasurer may
-- accept any amount for any paid-through date.

CREATE TABLE dues_rates (
    year         INTEGER PRIMARY KEY CHECK (year BETWEEN 1900 AND 2999),
    amount_cents INTEGER NOT NULL CHECK (amount_cents >= 0),
    note         TEXT,
    set_by       INTEGER NOT NULL REFERENCES users(id),
    set_at       TEXT    NOT NULL,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version      INTEGER NOT NULL DEFAULT 1
);

-- §2 Draft batches ---------------------------------------------------------
--
-- open ──post atomically──> posted
--   └────abandon──────────> abandoned
--
-- Only `open` accepts entry or default changes; the other two are terminal.
-- A worksheet source column is deliberately absent: worksheet tables land in a
-- later migration (pma.7) and add the reference then.

CREATE TABLE payment_batches (
    id                   INTEGER PRIMARY KEY,
    label                TEXT    NOT NULL CHECK (length(trim(label)) > 0),
    state                TEXT    NOT NULL DEFAULT 'open'
                             CHECK (state IN ('open', 'posted', 'abandoned')),
    default_amount_cents INTEGER CHECK (default_amount_cents IS NULL OR default_amount_cents > 0),
    default_paid_through TEXT    CHECK (default_paid_through IS NULL
                             OR default_paid_through GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    opened_by            INTEGER NOT NULL REFERENCES users(id),
    opened_at            TEXT    NOT NULL,
    posted_by            INTEGER REFERENCES users(id),
    posted_at            TEXT,
    abandoned_by         INTEGER REFERENCES users(id),
    abandoned_at         TEXT,
    abandon_reason       TEXT,
    created_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version              INTEGER NOT NULL DEFAULT 1,
    -- Terminal states carry their actor and timestamp; open states carry neither.
    CHECK ((state = 'posted') = (posted_at IS NOT NULL)),
    CHECK ((state = 'posted') = (posted_by IS NOT NULL)),
    CHECK ((state = 'abandoned') = (abandoned_at IS NOT NULL)),
    CHECK ((state = 'abandoned') = (abandoned_by IS NOT NULL))
);
CREATE INDEX ix_payment_batches_state ON payment_batches(state, opened_at DESC);

-- Mutable draft rows. These create no payment, no coverage event, and no
-- change in a member's dues standing until the batch is posted.

CREATE TABLE payment_batch_entries (
    id                  INTEGER PRIMARY KEY,
    batch_id            INTEGER NOT NULL REFERENCES payment_batches(id) ON DELETE CASCADE,
    membership_id       INTEGER NOT NULL REFERENCES memberships(id),
    sequence            INTEGER NOT NULL,
    amount_cents        INTEGER NOT NULL CHECK (amount_cents > 0),
    method              TEXT    NOT NULL CHECK (method IN ('cash', 'check', 'other')),
    reference           TEXT,
    received_on         TEXT    NOT NULL
                            CHECK (received_on GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    received_by_officer TEXT,
    paid_through        TEXT    NOT NULL
                            CHECK (paid_through GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    treasurer_note      TEXT,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version             INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX ux_batch_entries_sequence ON payment_batch_entries(batch_id, sequence);
CREATE INDEX ix_batch_entries_membership ON payment_batch_entries(membership_id);

-- §3 Immutable posted ledger ------------------------------------------------
--
-- There is no UPDATE or DELETE path for a posted payment. A correction appends
-- a negative reversal plus a positive replacement and keeps the original.

CREATE TABLE payments (
    id                  INTEGER PRIMARY KEY,
    membership_id       INTEGER NOT NULL REFERENCES memberships(id),
    batch_id            INTEGER REFERENCES payment_batches(id),
    amount_cents        INTEGER NOT NULL CHECK (amount_cents <> 0),
    method              TEXT    NOT NULL CHECK (method IN ('cash', 'check', 'other')),
    reference           TEXT,
    received_on         TEXT    NOT NULL
                            CHECK (received_on GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    received_by_officer TEXT,
    entered_by          INTEGER NOT NULL REFERENCES users(id),
    entered_at          TEXT    NOT NULL,
    receipt_code        TEXT    NOT NULL,
    entry_kind          TEXT    NOT NULL
                            CHECK (entry_kind IN ('original', 'reversal', 'replacement')),
    corrects_payment_id INTEGER REFERENCES payments(id),
    treasurer_note      TEXT,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    -- Only a reversal is negative; originals and replacements are positive.
    CHECK ((entry_kind = 'reversal') = (amount_cents < 0)),
    -- Only an original stands alone; reversals and replacements name their target.
    CHECK ((entry_kind = 'original') = (corrects_payment_id IS NULL))
);
CREATE UNIQUE INDEX ux_payments_receipt ON payments(receipt_code);
-- A payment is reversed at most once and replaced at most once, so a chain
-- never forks; a repeat correction targets the current replacement instead.
CREATE UNIQUE INDEX ux_payments_correction_target
    ON payments(corrects_payment_id, entry_kind) WHERE corrects_payment_id IS NOT NULL;
CREATE INDEX ix_payments_membership ON payments(membership_id, received_on DESC, id DESC);
CREATE INDEX ix_payments_batch ON payments(batch_id);

CREATE TABLE payment_corrections (
    id                     INTEGER PRIMARY KEY,
    original_payment_id    INTEGER NOT NULL REFERENCES payments(id),
    reversal_payment_id    INTEGER NOT NULL REFERENCES payments(id),
    replacement_payment_id INTEGER NOT NULL REFERENCES payments(id),
    reason                 TEXT    NOT NULL CHECK (length(trim(reason)) > 0),
    corrected_by           INTEGER NOT NULL REFERENCES users(id),
    corrected_at           TEXT    NOT NULL,
    created_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (original_payment_id <> reversal_payment_id
       AND original_payment_id <> replacement_payment_id
       AND reversal_payment_id <> replacement_payment_id)
);
CREATE UNIQUE INDEX ux_corrections_original ON payment_corrections(original_payment_id);
CREATE UNIQUE INDEX ux_corrections_reversal ON payment_corrections(reversal_payment_id);
CREATE UNIQUE INDEX ux_corrections_replacement ON payment_corrections(replacement_payment_id);

-- §4 Append-only coverage ---------------------------------------------------
--
-- Every paid-through decision is stated explicitly by whoever made it. The
-- server never infers one from an amount or from note text.
--
-- reason_kind:
--   payment       — granted by posting a payment
--   correction    — a payment correction changed the paid-through date
--   adjustment    — an independent treasurer decision (waiver, fix, goodwill)
--   legacy_import — the one-time ADR-0007 conversion of legacy columns below
--   import        — written by the import commit path (pma.13 cutover)

CREATE TABLE coverage_events (
    id                  INTEGER PRIMARY KEY,
    membership_id       INTEGER NOT NULL REFERENCES memberships(id),
    paid_through        TEXT    NOT NULL
                            CHECK (paid_through GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    reason_kind         TEXT    NOT NULL CHECK (reason_kind IN
                            ('payment', 'correction', 'adjustment', 'legacy_import', 'import')),
    reason              TEXT,
    payment_id          INTEGER REFERENCES payments(id),
    import_run_id       INTEGER REFERENCES import_runs(id),
    supersedes_event_id INTEGER REFERENCES coverage_events(id),
    -- Restricted context preserved from a legacy note or a treasurer remark.
    -- Never returned by the safe dues-standing summary.
    source_note         TEXT,
    decided_by          INTEGER REFERENCES users(id),
    decided_at          TEXT    NOT NULL,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (reason_kind <> 'payment' OR payment_id IS NOT NULL),
    CHECK (reason_kind <> 'correction' OR payment_id IS NOT NULL),
    CHECK (reason_kind NOT IN ('legacy_import', 'import') OR payment_id IS NULL),
    CHECK (id <> supersedes_event_id)
);
-- One superseding event per superseded event keeps the history a chain.
CREATE UNIQUE INDEX ux_coverage_supersedes
    ON coverage_events(supersedes_event_id) WHERE supersedes_event_id IS NOT NULL;
-- The ADR-0007 backfill runs exactly once per membership on any upgrade path.
CREATE UNIQUE INDEX ux_coverage_legacy_backfill
    ON coverage_events(membership_id) WHERE reason_kind = 'legacy_import';
CREATE INDEX ix_coverage_latest ON coverage_events(membership_id, decided_at DESC, id DESC);
CREATE INDEX ix_coverage_payment ON coverage_events(payment_id);

-- §5 Exactly-once writes ----------------------------------------------------
--
-- The database, not process memory, owns replay detection, so a retry after a
-- restart still cannot duplicate a posted batch.

CREATE TABLE idempotency_records (
    id              INTEGER PRIMARY KEY,
    actor_user_id   INTEGER NOT NULL REFERENCES users(id),
    operation       TEXT    NOT NULL,
    idempotency_key TEXT    NOT NULL,
    request_hash    TEXT    NOT NULL,
    resource_kind   TEXT,
    resource_id     INTEGER,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_idempotency_scope
    ON idempotency_records(actor_user_id, operation, idempotency_key);

-- §6 Treasury capabilities --------------------------------------------------
--
-- Mirrors authz.All. 0004_seed_roles.sql granted the administrator every
-- capability that existed at that time, so new codes need explicit grants.

INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('dues.read',              'Read safe dues standing, rates, and suggestions.',            'treasury'),
    ('dues.rate.manage',       'Create or revise the dues rate for a year.',                  'treasury'),
    ('coverage.read',          'Read the append-only paid-through coverage history.',         'treasury'),
    ('coverage.adjust',        'Record an independent paid-through adjustment.',              'treasury'),
    ('payment.read',           'Read payment detail, references, receipts, and corrections.', 'treasury'),
    ('payment.batch.manage',   'Open, edit, and abandon draft payment batches.',              'treasury'),
    ('payment.post',           'Post a batch or a single payment to the ledger.',             'treasury'),
    ('payment.correct',        'Correct a posted payment by reversal and replacement.',       'treasury'),
    ('payment.export',         'Export treasury records to CSV.',                             'treasury'),
    ('dues.worksheet.manage',  'Generate and read renewal worksheet runs.',                   'treasury');

-- treasurer: every Phase 2 treasury capability.
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code)
SELECT 'treasurer', code FROM capabilities WHERE code IN (
    'dues.read', 'dues.rate.manage', 'coverage.read', 'coverage.adjust',
    'payment.read', 'payment.batch.manage', 'payment.post', 'payment.correct',
    'payment.export', 'dues.worksheet.manage'
);

-- administrator: keeps its catalog-wide grant.
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code)
SELECT 'administrator', code FROM capabilities;

-- president, vice_president, secretary: safe standing only. No payment detail,
-- export, batch, correction, or treasurer-note capability by default.
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code) VALUES
    ('president',      'dues.read'),
    ('vice_president', 'dues.read'),
    ('secretary',      'dues.read');

-- Every other role receives no Phase 2 capability.

-- §7 ADR-0007 legacy backfill -----------------------------------------------
--
-- Every non-null memberships.legacy_current_until becomes exactly one
-- coverage event. The legacy note is preserved as restricted source context.
-- The originating import run is linked when it is still recoverable: the most
-- recent committed run that staged a row matching this membership's person.
--
-- Phase 1's importer does not currently write these columns (pma.13 is the
-- real cutover), so on most databases this backfill correctly moves zero rows.
-- It exists for databases populated by an earlier or manual path.

INSERT INTO coverage_events (
    membership_id, paid_through, reason_kind, reason,
    import_run_id, source_note, decided_by, decided_at
)
SELECT
    m.id,
    m.legacy_current_until,
    'legacy_import',
    'Converted from the imported Current Until value by the Phase 2 ledger migration.',
    (SELECT r.id
       FROM import_runs r
       JOIN staged_import_rows s ON s.import_run_id = r.id
      WHERE s.match_person_id = m.person_id
        AND r.committed_at IS NOT NULL
      ORDER BY r.committed_at DESC, r.id DESC
      LIMIT 1),
    m.legacy_current_until_note,
    NULL,
    COALESCE(m.updated_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
FROM memberships m
WHERE m.legacy_current_until IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM coverage_events c
       WHERE c.membership_id = m.id AND c.reason_kind = 'legacy_import'
  );

-- Verify before destroying the source. Every legacy value must now be readable
-- from exactly one coverage event with the same date. A mismatch fails the
-- CHECK, which aborts the migration transaction and leaves the columns intact.

CREATE TABLE _legacy_backfill_guard (ok INTEGER NOT NULL CHECK (ok = 1));

INSERT INTO _legacy_backfill_guard (ok)
SELECT CASE WHEN
    (SELECT count(*) FROM memberships WHERE legacy_current_until IS NOT NULL)
        = (SELECT count(*) FROM coverage_events WHERE reason_kind = 'legacy_import')
    AND NOT EXISTS (
        SELECT 1 FROM memberships m
         WHERE m.legacy_current_until IS NOT NULL
           AND NOT EXISTS (
               SELECT 1 FROM coverage_events c
                WHERE c.membership_id = m.id
                  AND c.reason_kind = 'legacy_import'
                  AND c.paid_through = m.legacy_current_until
                  AND (c.source_note IS m.legacy_current_until_note)
           )
    )
THEN 1 ELSE 0 END;

DROP TABLE _legacy_backfill_guard;

ALTER TABLE memberships DROP COLUMN legacy_current_until;
ALTER TABLE memberships DROP COLUMN legacy_current_until_note;

-- +goose Down

-- Restore the legacy columns and their values before the coverage events that
-- hold them are dropped, so down/up/down loses nothing.

ALTER TABLE memberships ADD COLUMN legacy_current_until TEXT;
ALTER TABLE memberships ADD COLUMN legacy_current_until_note TEXT;

UPDATE memberships
   SET legacy_current_until = (
           SELECT c.paid_through FROM coverage_events c
            WHERE c.membership_id = memberships.id AND c.reason_kind = 'legacy_import'
       ),
       legacy_current_until_note = (
           SELECT c.source_note FROM coverage_events c
            WHERE c.membership_id = memberships.id AND c.reason_kind = 'legacy_import'
       )
 WHERE EXISTS (
           SELECT 1 FROM coverage_events c
            WHERE c.membership_id = memberships.id AND c.reason_kind = 'legacy_import'
       );

DELETE FROM role_capabilities WHERE capability_code IN (
    'dues.read', 'dues.rate.manage', 'coverage.read', 'coverage.adjust',
    'payment.read', 'payment.batch.manage', 'payment.post', 'payment.correct',
    'payment.export', 'dues.worksheet.manage'
);
DELETE FROM capabilities WHERE code IN (
    'dues.read', 'dues.rate.manage', 'coverage.read', 'coverage.adjust',
    'payment.read', 'payment.batch.manage', 'payment.post', 'payment.correct',
    'payment.export', 'dues.worksheet.manage'
);

DROP TABLE IF EXISTS idempotency_records;
DROP TABLE IF EXISTS coverage_events;
DROP TABLE IF EXISTS payment_corrections;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS payment_batch_entries;
DROP TABLE IF EXISTS payment_batches;
DROP TABLE IF EXISTS dues_rates;
