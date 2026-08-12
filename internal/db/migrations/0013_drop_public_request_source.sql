-- +goose NO TRANSACTION
-- +goose Up

-- Remove 'public' from the change-request source constraint
-- (bcars-portal-4ux.6, ADR-0013).
--
-- WHY A TABLE REBUILD
--
-- 0009 wrote 'public' into the source CHECK while an anonymous correction form
-- was still planned. That plan was withdrawn: correction suggestions are
-- authenticated, and an anonymous caller cannot submit at all. The application
-- already refuses the value, but a constraint that still permits it is a
-- standing invitation -- to a future writer, to a hand-run UPDATE, and to the
-- next reader trying to work out which of the two sources of truth is real.
--
-- SQLite cannot drop a CHECK constraint in place, so the table is rebuilt by
-- the documented procedure: build the replacement, copy every row, drop the
-- original, rename, recreate the indexes, and verify the foreign keys still
-- resolve. This migration runs OUTSIDE a transaction because the procedure
-- needs PRAGMA foreign_keys, which is a no-op inside one.
--
-- The child table member_change_request_items is not touched. Its rows keep
-- pointing at the same request ids, and the foreign_key_check below is what
-- proves it rather than the comment claiming it.
--
-- NO ROW IS REWRITTEN. The copy is a straight column-for-column move, so
-- historical audit provenance survives exactly as recorded. A pre-existing
-- 'public' row would fail the new CHECK and abort the migration rather than be
-- silently relabelled -- which is the correct outcome, because no supported
-- version of this application could have created one, and one appearing means
-- something happened that an operator needs to look at rather than have
-- quietly rewritten.
--
-- Keep every comment in this file ASCII: sqlc reads this directory as its
-- schema.

PRAGMA foreign_keys = OFF;

CREATE TABLE member_change_requests_rebuilt (
    id                  INTEGER PRIMARY KEY,
    source              TEXT    NOT NULL CHECK (source IN (
                            'officer_phone', 'officer_email', 'officer_mail',
                            'officer_meeting', 'member')),
    status              TEXT    NOT NULL DEFAULT 'submitted' CHECK (status IN (
                            'draft', 'submitted', 'in_review', 'resolved', 'withdrawn')),
    requester_user_id   INTEGER REFERENCES users(id),
    target_person_id    INTEGER REFERENCES persons(id),
    supplied_name       TEXT,
    supplied_call_sign  TEXT,
    supplied_contact    TEXT,
    stated_relationship TEXT,
    summary             TEXT    NOT NULL CHECK (length(trim(summary)) > 0),
    received_by         INTEGER REFERENCES users(id),
    submitted_at        TEXT    NOT NULL,
    triaged_by          INTEGER REFERENCES users(id),
    triaged_at          TEXT,
    resolved_at         TEXT,
    withdrawn_at        TEXT,
    source_ip_hash      TEXT,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version             INTEGER NOT NULL DEFAULT 1,
    -- An authenticated member request names its requester. The companion rule
    -- 0009 carried -- that a 'public' request must NOT name one -- is gone with
    -- the source it constrained.
    CHECK (source <> 'member' OR requester_user_id IS NOT NULL),
    -- Terminal states record when they became terminal.
    CHECK (status <> 'resolved'  OR resolved_at  IS NOT NULL),
    CHECK (status <> 'withdrawn' OR withdrawn_at IS NOT NULL),
    -- Triage provenance travels together.
    CHECK ((triaged_by IS NULL) = (triaged_at IS NULL))
);

INSERT INTO member_change_requests_rebuilt (
    id, source, status, requester_user_id, target_person_id,
    supplied_name, supplied_call_sign, supplied_contact, stated_relationship,
    summary, received_by, submitted_at, triaged_by, triaged_at,
    resolved_at, withdrawn_at, source_ip_hash, created_at, updated_at, version
)
SELECT id, source, status, requester_user_id, target_person_id,
       supplied_name, supplied_call_sign, supplied_contact, stated_relationship,
       summary, received_by, submitted_at, triaged_by, triaged_at,
       resolved_at, withdrawn_at, source_ip_hash, created_at, updated_at, version
  FROM member_change_requests;

DROP TABLE member_change_requests;

ALTER TABLE member_change_requests_rebuilt RENAME TO member_change_requests;

CREATE INDEX ix_change_requests_status ON member_change_requests(status, submitted_at DESC);
CREATE INDEX ix_change_requests_target ON member_change_requests(target_person_id, submitted_at DESC);
CREATE INDEX ix_change_requests_requester ON member_change_requests(requester_user_id, submitted_at DESC);
-- A request whose target no officer has resolved yet is the triage queue, so it
-- gets its own partial index. Members submitting about someone else are now the
-- reason that queue exists.
CREATE INDEX ix_change_requests_untargeted
    ON member_change_requests(submitted_at DESC) WHERE target_person_id IS NULL;

PRAGMA foreign_key_check;

PRAGMA foreign_keys = ON;

-- +goose Down

PRAGMA foreign_keys = OFF;

CREATE TABLE member_change_requests_rebuilt (
    id                  INTEGER PRIMARY KEY,
    source              TEXT    NOT NULL CHECK (source IN (
                            'officer_phone', 'officer_email', 'officer_mail',
                            'officer_meeting', 'member', 'public')),
    status              TEXT    NOT NULL DEFAULT 'submitted' CHECK (status IN (
                            'draft', 'submitted', 'in_review', 'resolved', 'withdrawn')),
    requester_user_id   INTEGER REFERENCES users(id),
    target_person_id    INTEGER REFERENCES persons(id),
    supplied_name       TEXT,
    supplied_call_sign  TEXT,
    supplied_contact    TEXT,
    stated_relationship TEXT,
    summary             TEXT    NOT NULL CHECK (length(trim(summary)) > 0),
    received_by         INTEGER REFERENCES users(id),
    submitted_at        TEXT    NOT NULL,
    triaged_by          INTEGER REFERENCES users(id),
    triaged_at          TEXT,
    resolved_at         TEXT,
    withdrawn_at        TEXT,
    source_ip_hash      TEXT,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version             INTEGER NOT NULL DEFAULT 1,
    CHECK (source <> 'member' OR requester_user_id IS NOT NULL),
    CHECK (source <> 'public' OR requester_user_id IS NULL),
    CHECK (status <> 'resolved'  OR resolved_at  IS NOT NULL),
    CHECK (status <> 'withdrawn' OR withdrawn_at IS NOT NULL),
    CHECK ((triaged_by IS NULL) = (triaged_at IS NULL))
);

INSERT INTO member_change_requests_rebuilt (
    id, source, status, requester_user_id, target_person_id,
    supplied_name, supplied_call_sign, supplied_contact, stated_relationship,
    summary, received_by, submitted_at, triaged_by, triaged_at,
    resolved_at, withdrawn_at, source_ip_hash, created_at, updated_at, version
)
SELECT id, source, status, requester_user_id, target_person_id,
       supplied_name, supplied_call_sign, supplied_contact, stated_relationship,
       summary, received_by, submitted_at, triaged_by, triaged_at,
       resolved_at, withdrawn_at, source_ip_hash, created_at, updated_at, version
  FROM member_change_requests;

DROP TABLE member_change_requests;

ALTER TABLE member_change_requests_rebuilt RENAME TO member_change_requests;

CREATE INDEX ix_change_requests_status ON member_change_requests(status, submitted_at DESC);
CREATE INDEX ix_change_requests_target ON member_change_requests(target_person_id, submitted_at DESC);
CREATE INDEX ix_change_requests_requester ON member_change_requests(requester_user_id, submitted_at DESC);
CREATE INDEX ix_change_requests_untargeted
    ON member_change_requests(submitted_at DESC) WHERE target_person_id IS NULL;

PRAGMA foreign_key_check;

PRAGMA foreign_keys = ON;
