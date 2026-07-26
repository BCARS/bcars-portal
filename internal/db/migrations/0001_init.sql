-- +goose Up

-- §3.1 Identity

CREATE TABLE persons (
    id              INTEGER PRIMARY KEY,
    display_name    TEXT    NOT NULL,
    sort_name       TEXT    NOT NULL,
    call_sign       TEXT,
    deceased_at     TEXT,
    deactivated_at  TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version         INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX ux_persons_callsign ON persons(call_sign) WHERE call_sign IS NOT NULL;

CREATE TABLE external_ids (
    id          INTEGER PRIMARY KEY,
    entity_kind TEXT    NOT NULL,
    entity_id   INTEGER NOT NULL,
    system      TEXT    NOT NULL,
    external_id TEXT    NOT NULL,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX ux_external_ids_lookup ON external_ids(system, external_id);
CREATE INDEX ix_external_ids_entity ON external_ids(entity_kind, entity_id);

CREATE TABLE contact_methods (
    id                  INTEGER PRIMARY KEY,
    person_id           INTEGER NOT NULL REFERENCES persons(id),
    kind                TEXT    NOT NULL,
    label               TEXT,
    value_raw           TEXT    NOT NULL,
    value_norm          TEXT    NOT NULL,
    is_primary          INTEGER NOT NULL DEFAULT 0,
    archived_at         TEXT,
    postal_line1        TEXT,
    postal_line2        TEXT,
    postal_city         TEXT,
    postal_state        TEXT,
    postal_postal_code  TEXT,
    postal_country      TEXT,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version             INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX ix_contact_methods_person ON contact_methods(person_id);
CREATE INDEX ix_contact_methods_norm ON contact_methods(kind, value_norm) WHERE archived_at IS NULL;

-- §3.2 Preference history pattern

CREATE TABLE contact_method_visibility_events (
    id                INTEGER PRIMARY KEY,
    contact_method_id INTEGER NOT NULL REFERENCES contact_methods(id),
    audience          TEXT    NOT NULL,
    source            TEXT    NOT NULL,
    effective_at      TEXT    NOT NULL,
    actor_user_id     INTEGER REFERENCES users(id),
    note              TEXT,
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_cmv_latest ON contact_method_visibility_events(contact_method_id, effective_at DESC);

CREATE TABLE acs_ares_sharing_events (
    id            INTEGER PRIMARY KEY,
    person_id     INTEGER NOT NULL REFERENCES persons(id),
    participates  INTEGER NOT NULL,
    source        TEXT    NOT NULL,
    effective_at  TEXT    NOT NULL,
    actor_user_id INTEGER REFERENCES users(id),
    reason        TEXT,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_acs_latest ON acs_ares_sharing_events(person_id, effective_at DESC);

-- §3.3 Memberships

CREATE TABLE memberships (
    id                        INTEGER PRIMARY KEY,
    person_id                 INTEGER NOT NULL REFERENCES persons(id),
    base_type                 TEXT    NOT NULL,
    lifecycle                 TEXT    NOT NULL,
    joined_on                 TEXT,
    ended_on                  TEXT,
    legacy_current_until      TEXT,
    legacy_current_until_note TEXT,
    created_at                TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at                TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version                   INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX ix_memberships_person ON memberships(person_id);
CREATE INDEX ix_memberships_lifecycle ON memberships(lifecycle);

CREATE TABLE membership_approvals (
    id            INTEGER PRIMARY KEY,
    membership_id INTEGER NOT NULL REFERENCES memberships(id),
    decision      TEXT    NOT NULL,
    approved_type TEXT,
    decided_by    INTEGER NOT NULL REFERENCES users(id),
    decided_at    TEXT    NOT NULL,
    reason        TEXT,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_membership_approvals_membership ON membership_approvals(membership_id);

CREATE TABLE fcc_verifications (
    id                  INTEGER PRIMARY KEY,
    membership_id       INTEGER NOT NULL REFERENCES memberships(id),
    call_sign           TEXT    NOT NULL,
    license_class       TEXT,
    verification_source TEXT    NOT NULL,
    verified_by         INTEGER NOT NULL REFERENCES users(id),
    verified_at         TEXT    NOT NULL,
    expires_at          TEXT,
    revoked_at          TEXT,
    notes               TEXT,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_fcc_verifications_membership ON fcc_verifications(membership_id);

CREATE TABLE honorary_grants (
    id            INTEGER PRIMARY KEY,
    membership_id INTEGER NOT NULL REFERENCES memberships(id),
    starts_on     TEXT    NOT NULL,
    ends_on       TEXT,
    is_lifetime   INTEGER NOT NULL DEFAULT 0,
    reason        TEXT    NOT NULL,
    approved_by   INTEGER NOT NULL REFERENCES users(id),
    approved_at   TEXT    NOT NULL,
    revoked_at    TEXT,
    revoked_by    INTEGER REFERENCES users(id),
    revoke_reason TEXT,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version       INTEGER NOT NULL DEFAULT 1,
    CHECK ((is_lifetime = 1 AND ends_on IS NULL) OR (is_lifetime = 0))
);
CREATE INDEX ix_honorary_membership ON honorary_grants(membership_id);

-- §3.4 Notes

CREATE TABLE notes (
    id           INTEGER PRIMARY KEY,
    subject_kind TEXT    NOT NULL,
    subject_id   INTEGER NOT NULL,
    category     TEXT    NOT NULL,
    visibility   TEXT    NOT NULL,
    body         TEXT    NOT NULL,
    author_id    INTEGER NOT NULL REFERENCES users(id),
    source       TEXT    NOT NULL,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version      INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX ix_notes_subject ON notes(subject_kind, subject_id);

CREATE TABLE note_revisions (
    id        INTEGER PRIMARY KEY,
    note_id   INTEGER NOT NULL REFERENCES notes(id),
    body      TEXT    NOT NULL,
    edited_by INTEGER NOT NULL REFERENCES users(id),
    edited_at TEXT    NOT NULL,
    reason    TEXT
);
CREATE INDEX ix_note_revisions_note ON note_revisions(note_id);

-- §3.5 Authentication & Authorization

CREATE TABLE users (
    id                   INTEGER PRIMARY KEY,
    email                TEXT    NOT NULL,
    email_verified_at    TEXT,
    password_hash        TEXT,
    password_algo_params TEXT,
    person_id            INTEGER REFERENCES persons(id),
    is_active            INTEGER NOT NULL DEFAULT 1,
    last_login_at        TEXT,
    failed_login_count   INTEGER NOT NULL DEFAULT 0,
    locked_until         TEXT,
    created_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    version              INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX ux_users_email ON users(email);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    created_at   TEXT    NOT NULL,
    last_seen_at TEXT    NOT NULL,
    expires_at   TEXT    NOT NULL,
    ip_hash      TEXT,
    user_agent   TEXT,
    revoked_at   TEXT
);
CREATE INDEX ix_sessions_user ON sessions(user_id);

CREATE TABLE email_links (
    id               INTEGER PRIMARY KEY,
    purpose          TEXT    NOT NULL,
    user_id          INTEGER REFERENCES users(id),
    email            TEXT    NOT NULL,
    token_hash       TEXT    NOT NULL,
    created_at       TEXT    NOT NULL,
    expires_at       TEXT    NOT NULL,
    consumed_at      TEXT,
    requested_ip_hash TEXT
);
CREATE UNIQUE INDEX ux_email_links_token ON email_links(token_hash);
CREATE INDEX ix_email_links_user ON email_links(user_id);

CREATE TABLE capabilities (
    code        TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    category    TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE roles (
    code        TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    kind        TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE role_capabilities (
    role_code       TEXT NOT NULL REFERENCES roles(code),
    capability_code TEXT NOT NULL REFERENCES capabilities(code),
    PRIMARY KEY (role_code, capability_code)
);

CREATE TABLE user_role_grants (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    role_code  TEXT    NOT NULL REFERENCES roles(code),
    granted_by INTEGER NOT NULL REFERENCES users(id),
    granted_at TEXT    NOT NULL,
    revoked_at TEXT,
    revoked_by INTEGER REFERENCES users(id),
    reason     TEXT,
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_user_role_grants_user ON user_role_grants(user_id);

CREATE TABLE user_capability_grants (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id),
    capability_code TEXT    NOT NULL REFERENCES capabilities(code),
    granted_by      INTEGER NOT NULL REFERENCES users(id),
    granted_at      TEXT    NOT NULL,
    revoked_at      TEXT,
    revoked_by      INTEGER REFERENCES users(id),
    reason          TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- §3.6 Audit

CREATE TABLE audit_events (
    id               INTEGER PRIMARY KEY,
    occurred_at      TEXT    NOT NULL,
    request_id       TEXT,
    actor_user_id    INTEGER REFERENCES users(id),
    actor_role_codes TEXT,
    action           TEXT    NOT NULL,
    resource_kind    TEXT,
    resource_id      INTEGER,
    outcome          TEXT    NOT NULL,
    reason_code      TEXT,
    detail_json      TEXT,
    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX ix_audit_events_actor ON audit_events(actor_user_id, occurred_at DESC);
CREATE INDEX ix_audit_events_resource ON audit_events(resource_kind, resource_id, occurred_at DESC);
CREATE INDEX ix_audit_events_action ON audit_events(action, occurred_at DESC);

-- +goose Down

DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS user_capability_grants;
DROP TABLE IF EXISTS user_role_grants;
DROP TABLE IF EXISTS role_capabilities;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS capabilities;
DROP TABLE IF EXISTS email_links;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS note_revisions;
DROP TABLE IF EXISTS notes;
DROP TABLE IF EXISTS honorary_grants;
DROP TABLE IF EXISTS fcc_verifications;
DROP TABLE IF EXISTS membership_approvals;
DROP TABLE IF EXISTS memberships;
DROP TABLE IF EXISTS acs_ares_sharing_events;
DROP TABLE IF EXISTS contact_method_visibility_events;
DROP TABLE IF EXISTS contact_methods;
DROP TABLE IF EXISTS external_ids;
DROP TABLE IF EXISTS persons;
