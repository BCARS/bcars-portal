-- +goose Up

-- Phase 3 request, access, and relationship foundation
-- (docs/phase-3-design.md, ADR-0010).
--
-- Three separate facts that this schema deliberately keeps separate:
--
--   1. a person record             (persons, from 0001_init.sql)
--   2. who may SEE that record     (member_access_grants, below)
--   3. who is RELATED to that person (person_relationships, below)
--
-- Nothing here derives (2) from (3), from a contact method value, or from a
-- role. An officer grants record access explicitly, and only an explicit
-- unrevoked grant confers it.
--
-- Conventions carried from 0001_init.sql: dates are ISO 'YYYY-MM-DD' text,
-- timestamps are UTC ISO-8601 text, and mutable rows carry a version column.
-- Keep every comment in this file ASCII: sqlc reads this directory as its
-- schema, and multi-byte characters have corrupted its output before.

-- Section 1: member record access -------------------------------------------
--
-- A grant maps one authenticated user to one person record. One user may hold
-- several grants, which is how a shared household mailbox reaches more than one
-- record; the user chooses among exactly those records after signing in.
--
-- granted_by is nullable only so the migration backfill below can carry
-- existing links forward without inventing an officer who did not act.

CREATE TABLE member_access_grants (
    id            INTEGER PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users(id),
    person_id     INTEGER NOT NULL REFERENCES persons(id),
    access_kind   TEXT    NOT NULL DEFAULT 'self'
                      CHECK (access_kind IN ('self', 'delegate')),
    reason        TEXT,
    granted_by    INTEGER REFERENCES users(id),
    granted_at    TEXT    NOT NULL,
    revoked_at    TEXT,
    revoked_by    INTEGER REFERENCES users(id),
    revoke_reason TEXT,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version       INTEGER NOT NULL DEFAULT 1,
    -- Revocation provenance travels together. A grant cannot record who
    -- revoked it without recording that it was revoked.
    CHECK (revoked_at IS NOT NULL OR (revoked_by IS NULL AND revoke_reason IS NULL))
);

-- At most one ACTIVE grant per (user, person). Revoked rows stay for history,
-- and re-granting later is a new row rather than an undelete.
CREATE UNIQUE INDEX ux_member_access_active
    ON member_access_grants(user_id, person_id) WHERE revoked_at IS NULL;
CREATE INDEX ix_member_access_user ON member_access_grants(user_id) WHERE revoked_at IS NULL;
CREATE INDEX ix_member_access_person ON member_access_grants(person_id) WHERE revoked_at IS NULL;

-- Section 2: member change requests -----------------------------------------
--
-- One model for every intake channel. Source affects provenance and triage,
-- never which canonical validation rules apply on approval.
--
--   draft -> submitted -> in_review -> resolved
--                      \-> withdrawn
--
-- A blind public request may arrive with no canonical target; an officer links
-- one later without rewriting what the submitter actually supplied. The
-- supplied_* columns are a snapshot of the claim, not canonical data, and no
-- code path promotes them into persons or contact_methods.

CREATE TABLE member_change_requests (
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
    -- An authenticated member request names its requester; a blind public
    -- request must not carry one, because the public form performs no lookup
    -- and authenticates nobody.
    CHECK (source <> 'member' OR requester_user_id IS NOT NULL),
    CHECK (source <> 'public' OR requester_user_id IS NULL),
    -- Terminal states record when they became terminal.
    CHECK (status <> 'resolved'  OR resolved_at  IS NOT NULL),
    CHECK (status <> 'withdrawn' OR withdrawn_at IS NOT NULL),
    -- Triage provenance travels together.
    CHECK ((triaged_by IS NULL) = (triaged_at IS NULL))
);
CREATE INDEX ix_change_requests_status ON member_change_requests(status, submitted_at DESC);
CREATE INDEX ix_change_requests_target ON member_change_requests(target_person_id, submitted_at DESC);
CREATE INDEX ix_change_requests_requester ON member_change_requests(requester_user_id, submitted_at DESC);
-- Unlinked public intake is the triage queue, so it gets its own partial index.
CREATE INDEX ix_change_requests_untargeted
    ON member_change_requests(submitted_at DESC) WHERE target_person_id IS NULL;

-- Section 3: typed request items and their decisions ------------------------
--
-- Every proposal is one allowlisted operation. There is no arbitrary field
-- path, no generic update, and no prose-to-record mutation: an operation this
-- table does not name cannot be expressed at all.
--
-- 'other' is the escape hatch for a suggestion outside the supported set
-- (membership lifecycle, FCC, dues, honorary status). It stays visible to
-- officers, and the CHECK below makes it permanently unapprovable, so it can
-- never reach an apply path. An officer uses the existing specialized workflow
-- instead.
--
--   pending -> approved | rejected | needs_verification
--
-- Review is per item. Approved, rejected, and needs-verification items coexist
-- in one request, and the request resolves only when every item is terminal.

CREATE TABLE member_change_request_items (
    id                       INTEGER PRIMARY KEY,
    request_id               INTEGER NOT NULL REFERENCES member_change_requests(id),
    ordinal                  INTEGER NOT NULL CHECK (ordinal >= 0),
    operation                TEXT    NOT NULL CHECK (operation IN (
                                 'person.display_name.set',
                                 'person.call_sign.set',
                                 'contact_method.add',
                                 'contact_method.update',
                                 'contact_method.archive',
                                 'contact_method.set_primary',
                                 'contact_method.visibility.set',
                                 'sharing_pref.acs_ares.set',
                                 'relationship.add',
                                 'relationship.correct',
                                 'other')),
    proposed_value           TEXT,
    target_kind              TEXT    CHECK (target_kind IS NULL OR target_kind IN (
                                 'person', 'contact_method', 'membership', 'relationship')),
    target_id                INTEGER,
    target_version           INTEGER,
    sensitivity              TEXT    NOT NULL DEFAULT 'ordinary'
                                 CHECK (sensitivity IN ('ordinary', 'sensitive')),
    status                   TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN (
                                 'pending', 'approved', 'rejected', 'needs_verification')),
    reviewed_by              INTEGER REFERENCES users(id),
    reviewed_at              TEXT,
    decision_reason          TEXT,
    verification_note        TEXT,
    applied_at               TEXT,
    applied_resource_kind    TEXT,
    applied_resource_id      INTEGER,
    applied_resource_version INTEGER,
    created_at               TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at               TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version                  INTEGER NOT NULL DEFAULT 1,
    -- An unsupported suggestion is never approvable through any path.
    CHECK (operation <> 'other' OR status <> 'approved'),
    -- A decision names its reviewer; a pending item has not been decided.
    CHECK (status = 'pending' OR (reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL)),
    CHECK (status <> 'pending' OR (reviewed_by IS NULL AND reviewed_at IS NULL)),
    -- A sensitive approval carries the verification note the design requires.
    CHECK (status <> 'approved' OR sensitivity <> 'sensitive' OR verification_note IS NOT NULL),
    -- Only an approved item is ever applied, and applying records what it
    -- produced. A pending or rejected item with an applied resource would mean
    -- canonical data changed without a decision.
    CHECK (applied_at IS NULL OR status = 'approved'),
    CHECK ((applied_at IS NULL) = (applied_resource_kind IS NULL)),
    CHECK ((applied_at IS NULL) = (applied_resource_id IS NULL)),
    -- A target reference is complete or absent.
    CHECK ((target_kind IS NULL) = (target_id IS NULL)),
    CHECK (target_version IS NULL OR target_id IS NOT NULL)
);
CREATE UNIQUE INDEX ux_change_request_item_ordinal
    ON member_change_request_items(request_id, ordinal);
CREATE INDEX ix_change_request_items_request ON member_change_request_items(request_id);
CREATE INDEX ix_change_request_items_pending
    ON member_change_request_items(request_id) WHERE status = 'pending';

-- Section 4: informational relationships ------------------------------------
--
-- A relationship explains why one person is suggesting a change for another.
-- It confers nothing. Note what this table does NOT have: any user_id column
-- other than actor provenance, and any reference to member_access_grants. If
-- BCARS wants a helper to act for a related record, an officer creates a
-- separate revocable grant in section 1.
--
-- Relationships are directional and versioned. Archiving keeps the history.

CREATE TABLE person_relationships (
    id             INTEGER PRIMARY KEY,
    from_person_id INTEGER NOT NULL REFERENCES persons(id),
    to_person_id   INTEGER NOT NULL REFERENCES persons(id),
    kind           TEXT    NOT NULL CHECK (kind IN (
                       'spouse_partner', 'parent_guardian', 'child_dependent',
                       'household', 'other')),
    context        TEXT,
    created_by     INTEGER REFERENCES users(id),
    archived_at    TEXT,
    archived_by    INTEGER REFERENCES users(id),
    archive_reason TEXT,
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version        INTEGER NOT NULL DEFAULT 1,
    CHECK (from_person_id <> to_person_id),
    CHECK (archived_at IS NOT NULL OR (archived_by IS NULL AND archive_reason IS NULL))
);
CREATE UNIQUE INDEX ux_person_relationship_active
    ON person_relationships(from_person_id, to_person_id, kind) WHERE archived_at IS NULL;
CREATE INDEX ix_person_relationships_from
    ON person_relationships(from_person_id) WHERE archived_at IS NULL;
CREATE INDEX ix_person_relationships_to
    ON person_relationships(to_person_id) WHERE archived_at IS NULL;

-- Section 5: Phase 3 capabilities -------------------------------------------
--
-- Mirrors authz.All. 0004_seed_roles.sql granted the administrator every
-- capability that existed at that time, so new codes need explicit grants.

INSERT OR IGNORE INTO capabilities (code, description, category) VALUES
    ('change_request.manage',     'Capture, list, and triage member change requests from any channel.', 'membership'),
    ('change_request.review',     'Decide and apply individual change-request items.',                  'membership'),
    ('member_access.manage',      'Grant or revoke a member user access to person records.',            'membership'),
    ('relationship.manage',       'Maintain informational person relationships.',                       'membership'),
    ('profile.self.read',         'Read own explicitly granted safe profile and dues standing.',        'member'),
    ('change_request.submit.self','Submit, track, and withdraw own change requests.',                   'member'),
    ('directory.read',            'Attempt the member directory; eligibility is checked separately.',   'member');

-- Officers who already run member operations gain request review, access
-- provisioning, and relationship maintenance. The treasurer is included because
-- a treasurer taking a correction at a meeting is ordinary BCARS practice and
-- the treasurer already holds member.update and contact_method.write.
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code)
SELECT role, cap FROM (
    SELECT 'president' AS role UNION ALL
    SELECT 'vice_president'    UNION ALL
    SELECT 'secretary'         UNION ALL
    SELECT 'treasurer'
) roles
CROSS JOIN (
    SELECT 'change_request.manage' AS cap UNION ALL
    SELECT 'change_request.review'        UNION ALL
    SELECT 'member_access.manage'         UNION ALL
    SELECT 'relationship.manage'
) caps;

-- administrator: keeps its catalog-wide grant.
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code)
SELECT 'administrator', code FROM capabilities;

-- The member role gets own-profile, own-request, and directory entry, and
-- nothing else. It deliberately does NOT receive member.read or dues.read:
-- those are the broad administrative reads, and a member holding either would
-- see every record and every treasury standing in the club.
--
-- directory.read only lets the caller ATTEMPT the directory. Eligibility (an
-- active approved Full membership) is a separate resource policy, which is how
-- an Associate can hold this capability and still be refused the listing.
INSERT OR IGNORE INTO role_capabilities (role_code, capability_code) VALUES
    ('member', 'profile.self.read'),
    ('member', 'change_request.submit.self'),
    ('member', 'directory.read');

-- The member role is not an officer role. 0004_seed_roles.sql classified it
-- 'officer' when no member-facing surface existed; Phase 3 gives it one.
UPDATE roles
   SET kind = 'member',
       description = 'Club member with optional passwordless self-service access.'
 WHERE code = 'member';

-- Every other role receives no Phase 3 capability.

-- Section 6: users.person_id compatibility backfill --------------------------
--
-- ADR-0010. users.person_id stays as the officer identity link, but it stops
-- being an access authority: from here on, only member_access_grants answers
-- "which records may this user see". Carrying the existing links forward now
-- means the two never disagree at the moment of cutover.
--
-- The backfill is deterministic: exactly one 'self' grant per non-null
-- users.person_id, attributed to no officer because none acted.

INSERT INTO member_access_grants (user_id, person_id, access_kind, reason, granted_by, granted_at)
SELECT u.id,
       u.person_id,
       'self',
       'Carried forward from users.person_id by the Phase 3 access migration.',
       NULL,
       COALESCE(u.created_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
  FROM users u
 WHERE u.person_id IS NOT NULL
   AND NOT EXISTS (
       SELECT 1 FROM member_access_grants g
        WHERE g.user_id = u.id AND g.person_id = u.person_id AND g.revoked_at IS NULL
   );

-- Verify before anything depends on it. Every linked user must now be readable
-- from exactly one active grant for the same person. A mismatch fails the
-- CHECK, which aborts the migration transaction rather than leaving an
-- identity half-migrated.

CREATE TABLE _access_backfill_guard (ok INTEGER NOT NULL CHECK (ok = 1));

INSERT INTO _access_backfill_guard (ok)
SELECT CASE WHEN NOT EXISTS (
    SELECT 1 FROM users u
     WHERE u.person_id IS NOT NULL
       AND (SELECT count(*) FROM member_access_grants g
             WHERE g.user_id = u.id
               AND g.person_id = u.person_id
               AND g.revoked_at IS NULL) <> 1
) THEN 1 ELSE 0 END;

DROP TABLE _access_backfill_guard;

-- +goose Down

DELETE FROM role_capabilities WHERE capability_code IN (
    'change_request.manage', 'change_request.review', 'member_access.manage',
    'relationship.manage', 'profile.self.read', 'change_request.submit.self',
    'directory.read'
);
DELETE FROM capabilities WHERE code IN (
    'change_request.manage', 'change_request.review', 'member_access.manage',
    'relationship.manage', 'profile.self.read', 'change_request.submit.self',
    'directory.read'
);

UPDATE roles
   SET kind = 'officer',
       description = 'Regular member (no admin UI in P1).'
 WHERE code = 'member';

-- users.person_id was never modified on the way up, so the links the backfill
-- read are still there and re-running the up migration reproduces the same
-- grants. Dropping the table is therefore lossless for carried-forward rows.
DROP TABLE IF EXISTS person_relationships;
DROP TABLE IF EXISTS member_change_request_items;
DROP TABLE IF EXISTS member_change_requests;
DROP TABLE IF EXISTS member_access_grants;
