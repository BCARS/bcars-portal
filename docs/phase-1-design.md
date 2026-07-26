# Phase 1 Technical Design

Companion to `../PLANNING.md`. This document freezes the technical shape of the
Phase 1 Administrative Membership MVP so the implementation plan
(`phase-1-plan.md`) can be executed mechanically. All decisions here are meant
to survive into later phases without a schema or API rewrite; anything that is
explicitly Phase 2+ is called out so the seams stay clean.

## 1. Tech Baseline

| Area | Choice | Notes |
| --- | --- | --- |
| Language | Go 1.26 (latest stable at implementation start) | Pinned via `go.mod` `go` directive and toolchain. |
| HTTP framework | [Huma v2](https://huma.rocks) on `net/http` (Go 1.22+ router) | Typed operations, generated OpenAPI. |
| Router | `net/http.ServeMux` (Go 1.22+ patterns) via Huma adapter | No third-party router. |
| Templating | `html/template` server-rendered pages | `templ` is not required for Phase 1. |
| Progressive enhancement | HTMX (vendored, no CDN) | Server owns all state transitions. |
| Database | SQLite (`modernc.org/sqlite`, pure Go) in WAL mode | Foreign keys ON, `busy_timeout=5000`. |
| Migrations | Goose (embedded, applied at startup after `--migrate` gate) | SQL migrations only; no Go migrations. |
| Queries | sqlc (`sqlite` engine) | All non-trivial queries reviewed and generated. |
| Session store | SQLite table (`sessions`), signed opaque cookie carrying only session id | HttpOnly, Secure, SameSite=Lax. |
| Password hashing | `argon2id` (`golang.org/x/crypto/argon2`) with per-install pepper | Parameters recorded per hash for future migration. |
| Email | Interface `mail.Sender`; Phase 1 ships an SMTP implementation for Google Workspace and a `filelog` implementation for local dev/tests | No provider SDK dependency. |
| Logging | `log/slog` JSON handler, request-scoped logger with request-id | Redaction wrapper for PII fields. |
| Config | Env-first via `envconfig`-style struct; sample `.env` in repo | Secrets never through the API. |
| Testing | `go test ./...`, `testify/require`, table-driven authz matrix, `sqlite` in-memory per-test | No network in unit tests. |
| Lint/format | `gofmt`, `go vet`, `staticcheck`, `golangci-lint` (default set) | CI blocks on all. |
| CI | GitHub Actions (assumed private repo): matrix build, tests, lint, `sqlc diff`, OpenAPI diff, migration up/down round-trip. | |

ADRs to write in Workstream 1 (one page each):

- ADR-0001 SQLite for Phase 1.
- ADR-0002 Huma + `net/http` (no chi/gin).
- ADR-0003 Server-side sessions in SQLite; opaque cookie.
- ADR-0004 Argon2id with pepper for officer passwords.
- ADR-0005 Capability-based authorization; no title-string checks.
- ADR-0006 Preference history pattern for contact-method visibility and ACS/ARES.
- ADR-0007 Coverage/payment split (records the shape even though tables land in Phase 2).
- ADR-0008 Import staging model.

## 2. Module Layout

```
bcars-portal/
  cmd/
    portal/            # main HTTP server
    portalctl/         # admin CLI (bootstrap, import, backup verify)
  internal/
    app/               # composition root, DI wiring
    config/            # env loading
    db/
      migrations/      # goose .sql files
      queries/         # sqlc .sql inputs
      sqlc/            # generated code (checked in)
    domain/
      people/          # People + contact methods + preferences
      membership/      # Memberships, approvals, FCC verifications, honorary
      import/          # Staging, matching, reconciliation, commit
      audit/           # Audit event writer + search
      authn/           # Sessions, password, email links
      authz/           # Capability catalog, policy engine
      notes/           # Categorized permissioned notes
    httpapi/           # Huma operation registrations (thin)
    web/               # HTML pages + HTMX partials
      templates/
      static/
    mail/              # Sender interface + smtp + filelog impls
    obs/               # slog setup, request middleware, request-id
    testsupport/       # fixtures, in-memory DB, principal builders
  docs/
    phase-1-design.md
    phase-1-plan.md
    adr/
  fixtures/            # fake Groups.io JSON/CSV; NO real member data
  .env.sample
  Makefile
  sqlc.yaml
  go.mod
```

Rule: `httpapi/` and `web/` only translate to/from `domain/*` services. Business
rules and authorization live in `domain/`. Both the officer UI and (future) AI
tools call the same service methods.

## 3. Database Schema

SQLite. All tables have `created_at`, `updated_at` (`TEXT` ISO-8601 UTC),
and mutable rows have a monotonically increasing `version INTEGER NOT NULL
DEFAULT 1` used for optimistic concurrency. Foreign keys are declared and
`PRAGMA foreign_keys = ON` at every connection.

### 3.1 Identity

`persons` — a human, distinct from a login.

```
persons (
  id                INTEGER PRIMARY KEY,
  display_name      TEXT NOT NULL,        -- "Contact Name" from Groups.io
  sort_name         TEXT NOT NULL,        -- normalized for search
  call_sign         TEXT,                 -- normalized upper; may be NULL
  deceased_at       TEXT,                 -- date, nullable
  deactivated_at    TEXT,                 -- nullable; hides from active lists
  created_at, updated_at, version
)
CREATE UNIQUE INDEX ux_persons_callsign ON persons(call_sign) WHERE call_sign IS NOT NULL;
```

`external_ids` — preserved upstream identifiers.

```
external_ids (
  id            INTEGER PRIMARY KEY,
  entity_kind   TEXT NOT NULL,      -- 'person' | 'membership' | ...
  entity_id     INTEGER NOT NULL,
  system        TEXT NOT NULL,      -- 'groupsio.contact_row'
  external_id   TEXT NOT NULL,
  created_at
)
CREATE UNIQUE INDEX ux_external_ids_lookup
  ON external_ids(system, external_id);
CREATE INDEX ix_external_ids_entity
  ON external_ids(entity_kind, entity_id);
```

`contact_methods` — email/phone/postal.

```
contact_methods (
  id             INTEGER PRIMARY KEY,
  person_id      INTEGER NOT NULL REFERENCES persons(id),
  kind           TEXT NOT NULL,          -- 'email' | 'phone' | 'postal'
  label          TEXT,                   -- 'home', 'mobile', etc.
  value_raw      TEXT NOT NULL,          -- as entered
  value_norm     TEXT NOT NULL,          -- normalized for match/dedupe
  is_primary     INTEGER NOT NULL DEFAULT 0,
  archived_at    TEXT,                   -- soft archive
  postal_line1, postal_line2, postal_city, postal_state,
  postal_postal_code, postal_country,    -- nullable, only when kind='postal'
  created_at, updated_at, version
)
CREATE INDEX ix_contact_methods_person ON contact_methods(person_id);
CREATE INDEX ix_contact_methods_norm   ON contact_methods(kind, value_norm)
  WHERE archived_at IS NULL;
```

No unique constraint on `value_norm`; shared emails are permitted.

### 3.2 Preference History Pattern (pattern B)

One shared pattern for contact-method audience and ACS/ARES sharing. Each
preference table is append-only; the current value is the latest row for the
subject (or the default when none exists).

`contact_method_visibility_events`

```
contact_method_visibility_events (
  id                  INTEGER PRIMARY KEY,
  contact_method_id   INTEGER NOT NULL REFERENCES contact_methods(id),
  audience            TEXT NOT NULL,   -- 'hidden' | 'full_members' | 'officers_only'
  source              TEXT NOT NULL,   -- 'import_default' | 'officer_ui' | 'member_request'
  effective_at        TEXT NOT NULL,
  actor_user_id       INTEGER REFERENCES users(id),   -- null for import
  note               TEXT,
  created_at
)
CREATE INDEX ix_cmv_latest
  ON contact_method_visibility_events(contact_method_id, effective_at DESC);
```

`acs_ares_sharing_events`

```
acs_ares_sharing_events (
  id             INTEGER PRIMARY KEY,
  person_id      INTEGER NOT NULL REFERENCES persons(id),
  participates   INTEGER NOT NULL,   -- 0|1
  source         TEXT NOT NULL,      -- 'import_default' | 'officer_ui' | 'member_request'
  effective_at   TEXT NOT NULL,
  actor_user_id  INTEGER REFERENCES users(id),
  reason         TEXT,
  created_at
)
CREATE INDEX ix_acs_latest
  ON acs_ares_sharing_events(person_id, effective_at DESC);
```

Reads use a "latest row" view; writes only insert. This gives dated history for
free and lets the importer plant an `import_default` row that a member request
can later supersede without destroying provenance.

Defaults when no event exists:

- Contact-method visibility default is derived from the *current* membership
  type: Full → `full_members`; Associate → `hidden`; unknown → `hidden`.
- ACS/ARES default is derived from Full + verified FCC license → `1`; else `0`.

Defaults are computed in the domain layer, never stored implicitly.

### 3.3 Memberships

`memberships`

```
memberships (
  id                       INTEGER PRIMARY KEY,
  person_id                INTEGER NOT NULL REFERENCES persons(id),
  base_type                TEXT NOT NULL,     -- 'full' | 'associate'
  lifecycle                TEXT NOT NULL,     -- 'pending' | 'approved' | 'rejected'
                                              -- | 'inactive' | 'resigned' | 'deceased'
  joined_on                TEXT,              -- date
  ended_on                 TEXT,              -- date, for inactive/resigned/deceased
  legacy_current_until     TEXT,              -- Phase-2 backfill source only
  legacy_current_until_note TEXT,             -- e.g. 'imported 2024-05 from groupsio row 495'
  created_at, updated_at, version
)
CREATE INDEX ix_memberships_person ON memberships(person_id);
CREATE INDEX ix_memberships_lifecycle ON memberships(lifecycle);
```

`legacy_current_until` exists **only** as a Phase-1 landing spot for the
imported "Current Until" values. Phase 2's first migration converts every
non-null value into a `coverage_events` row and then drops these two columns.
Documented as such in the migration and in ADR-0007.

`membership_approvals` — one row per approval decision.

```
membership_approvals (
  id              INTEGER PRIMARY KEY,
  membership_id   INTEGER NOT NULL REFERENCES memberships(id),
  decision        TEXT NOT NULL,     -- 'approved' | 'rejected'
  approved_type   TEXT,              -- 'full' | 'associate' when approved
  decided_by      INTEGER NOT NULL REFERENCES users(id),
  decided_at      TEXT NOT NULL,
  reason          TEXT,
  created_at
)
CREATE INDEX ix_membership_approvals_membership ON membership_approvals(membership_id);
```

`fcc_verifications` — manual in Phase 1.

```
fcc_verifications (
  id                 INTEGER PRIMARY KEY,
  membership_id      INTEGER NOT NULL REFERENCES memberships(id),
  call_sign          TEXT NOT NULL,
  license_class      TEXT,           -- 'technician' | 'general' | 'extra' | free text
  verification_source TEXT NOT NULL, -- 'manual_ulsdb_lookup' | 'copy_of_license' | 'legacy_import'
  verified_by        INTEGER NOT NULL REFERENCES users(id),
  verified_at        TEXT NOT NULL,
  expires_at         TEXT,           -- optional
  revoked_at         TEXT,
  notes              TEXT,
  created_at
)
CREATE INDEX ix_fcc_verifications_membership ON fcc_verifications(membership_id);
```

`honorary_grants` — dues waiver layered on the base type.

```
honorary_grants (
  id              INTEGER PRIMARY KEY,
  membership_id   INTEGER NOT NULL REFERENCES memberships(id),
  starts_on       TEXT NOT NULL,     -- date
  ends_on         TEXT,              -- date; NULL when lifetime=1
  is_lifetime     INTEGER NOT NULL DEFAULT 0,
  reason          TEXT NOT NULL,     -- 'passed_exam_at_bcars' | 'service' | 'legacy_import' | free text
  approved_by     INTEGER NOT NULL REFERENCES users(id),
  approved_at     TEXT NOT NULL,
  revoked_at      TEXT,
  revoked_by      INTEGER REFERENCES users(id),
  revoke_reason   TEXT,
  created_at, updated_at, version,
  CHECK ((is_lifetime = 1 AND ends_on IS NULL) OR (is_lifetime = 0))
)
CREATE INDEX ix_honorary_membership ON honorary_grants(membership_id);
```

### 3.4 Notes

`notes` — categorized, permissioned, edit-history preserving.

```
notes (
  id             INTEGER PRIMARY KEY,
  subject_kind   TEXT NOT NULL,       -- 'person' | 'membership' | 'membership_approval'
                                      -- | 'honorary_grant' | 'contact_method'
  subject_id     INTEGER NOT NULL,
  category       TEXT NOT NULL,       -- 'general' | 'treasurer' | 'officer' | 'import'
  visibility     TEXT NOT NULL,       -- 'officers' | 'treasurer' | 'executive' | 'member_visible'
  body           TEXT NOT NULL,
  author_id      INTEGER NOT NULL REFERENCES users(id),
  source         TEXT NOT NULL,       -- 'ui' | 'import' | 'api'
  created_at, updated_at, version
)
CREATE INDEX ix_notes_subject ON notes(subject_kind, subject_id);

note_revisions (
  id            INTEGER PRIMARY KEY,
  note_id       INTEGER NOT NULL REFERENCES notes(id),
  body          TEXT NOT NULL,
  edited_by     INTEGER NOT NULL REFERENCES users(id),
  edited_at     TEXT NOT NULL,
  reason        TEXT
)
CREATE INDEX ix_note_revisions_note ON note_revisions(note_id);
```

### 3.5 Authentication & Authorization

`users` — an officer or webmaster login. May or may not be linked to a person.

```
users (
  id                    INTEGER PRIMARY KEY,
  email                 TEXT NOT NULL,          -- normalized lower
  email_verified_at     TEXT,
  password_hash         TEXT,                   -- argon2id encoded string; NULL disables password login
  password_algo_params  TEXT,                   -- JSON for future rotation
  person_id             INTEGER REFERENCES persons(id),  -- optional link
  is_active             INTEGER NOT NULL DEFAULT 1,
  last_login_at         TEXT,
  failed_login_count    INTEGER NOT NULL DEFAULT 0,
  locked_until          TEXT,
  created_at, updated_at, version
)
CREATE UNIQUE INDEX ux_users_email ON users(email);
```

`sessions`

```
sessions (
  id             TEXT PRIMARY KEY,          -- random 32-byte hex; also the cookie value
  user_id        INTEGER NOT NULL REFERENCES users(id),
  created_at     TEXT NOT NULL,
  last_seen_at   TEXT NOT NULL,
  expires_at     TEXT NOT NULL,
  ip_hash        TEXT,                      -- HMAC of IP for abuse review, not the IP
  user_agent     TEXT,
  revoked_at     TEXT
)
CREATE INDEX ix_sessions_user ON sessions(user_id);
```

Sessions are opaque: the cookie carries only `id`. Rotation on privilege change
and on high-impact operations ("recent auth" check ≤ 5 minutes).

`email_links` — password recovery + officer invitation + future member sign-in.

```
email_links (
  id             INTEGER PRIMARY KEY,
  purpose        TEXT NOT NULL,           -- 'password_recovery' | 'invitation' | 'verify_email'
  user_id        INTEGER REFERENCES users(id),
  email          TEXT NOT NULL,
  token_hash     TEXT NOT NULL,           -- sha256 of token; token never stored
  created_at     TEXT NOT NULL,
  expires_at     TEXT NOT NULL,
  consumed_at    TEXT,
  requested_ip_hash TEXT
)
CREATE UNIQUE INDEX ux_email_links_token ON email_links(token_hash);
CREATE INDEX ix_email_links_user ON email_links(user_id);
```

`capabilities` — the catalog. Seeded from code; migrations own the rows.

```
capabilities (
  code           TEXT PRIMARY KEY,        -- e.g. 'member.read', 'membership.approve'
  description    TEXT NOT NULL,
  category       TEXT NOT NULL,           -- 'membership' | 'treasury' | 'audit' | 'system'
  created_at
)

roles (
  code           TEXT PRIMARY KEY,        -- 'president' | 'treasurer' | ... | 'webmaster' | 'administrator'
  description    TEXT NOT NULL,
  kind           TEXT NOT NULL,           -- 'executive' | 'officer' | 'technical'
  created_at
)

role_capabilities (
  role_code       TEXT NOT NULL REFERENCES roles(code),
  capability_code TEXT NOT NULL REFERENCES capabilities(code),
  PRIMARY KEY (role_code, capability_code)
)

user_role_grants (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id),
  role_code     TEXT NOT NULL REFERENCES roles(code),
  granted_by    INTEGER NOT NULL REFERENCES users(id),
  granted_at    TEXT NOT NULL,
  revoked_at    TEXT,
  revoked_by    INTEGER REFERENCES users(id),
  reason        TEXT,
  created_at
)
CREATE INDEX ix_user_role_grants_user ON user_role_grants(user_id);

user_capability_grants (          -- direct capability grants outside a role
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id),
  capability_code TEXT NOT NULL REFERENCES capabilities(code),
  granted_by    INTEGER NOT NULL REFERENCES users(id),
  granted_at    TEXT NOT NULL,
  revoked_at    TEXT,
  revoked_by    INTEGER REFERENCES users(id),
  reason        TEXT,
  created_at
)
```

The set of a user's effective capabilities = union of active role grants'
capabilities and active direct grants. The policy layer takes `(principal,
action, resource)`; there are no title-string checks anywhere else.

### 3.6 Audit

`audit_events` — append-only. Never mutated, never `UPDATE`d, never deleted.

```
audit_events (
  id                   INTEGER PRIMARY KEY,
  occurred_at          TEXT NOT NULL,
  request_id           TEXT,
  actor_user_id        INTEGER REFERENCES users(id),
  actor_role_codes     TEXT,                    -- comma-separated snapshot
  action               TEXT NOT NULL,           -- e.g. 'member.update', 'import.commit'
  resource_kind        TEXT,
  resource_id          INTEGER,
  outcome              TEXT NOT NULL,           -- 'allowed' | 'denied' | 'error'
  reason_code          TEXT,                    -- structured error/deny code
  detail_json          TEXT,                    -- redacted structured detail
  created_at
)
CREATE INDEX ix_audit_events_actor    ON audit_events(actor_user_id, occurred_at DESC);
CREATE INDEX ix_audit_events_resource ON audit_events(resource_kind, resource_id, occurred_at DESC);
CREATE INDEX ix_audit_events_action   ON audit_events(action, occurred_at DESC);
```

Writer forbids logging raw contact values, tokens, or passwords. A helper wraps
common redactions.

### 3.7 Import Staging

`import_runs`

```
import_runs (
  id                   INTEGER PRIMARY KEY,
  source_kind          TEXT NOT NULL,       -- 'groupsio_contact_json' | 'groupsio_contact_csv'
  source_filename      TEXT NOT NULL,
  source_sha256        TEXT NOT NULL,
  uploaded_by          INTEGER NOT NULL REFERENCES users(id),
  uploaded_at          TEXT NOT NULL,
  status               TEXT NOT NULL,       -- 'uploaded' | 'validated' | 'previewed'
                                            -- | 'committed' | 'discarded' | 'failed'
  idempotency_key      TEXT NOT NULL,       -- required from client
  committed_by         INTEGER REFERENCES users(id),
  committed_at         TEXT,
  result_summary_json  TEXT,
  created_at, updated_at, version
)
CREATE UNIQUE INDEX ux_import_runs_idem ON import_runs(idempotency_key);
```

`staged_import_rows` — the row-level parse + match result. Retains raw values.

```
staged_import_rows (
  id                    INTEGER PRIMARY KEY,
  import_run_id         INTEGER NOT NULL REFERENCES import_runs(id) ON DELETE CASCADE,
  source_row_index      INTEGER NOT NULL,        -- 0-based position in file
  source_external_id    TEXT,                    -- groupsio row id when present
  raw_json              TEXT NOT NULL,           -- untouched row as parsed
  normalized_json       TEXT NOT NULL,           -- after normalization
  match_person_id       INTEGER REFERENCES persons(id),
  match_method          TEXT,                    -- 'external_id' | 'call_sign'
                                                 -- | 'email' | 'manual' | 'none'
  proposed_action       TEXT NOT NULL,           -- 'create' | 'update' | 'unchanged'
                                                 -- | 'conflict' | 'invalid' | 'manual'
  proposed_changes_json TEXT,                    -- field-level diffs
  validation_errors_json TEXT,
  requires_manual       INTEGER NOT NULL DEFAULT 0,
  manual_reason         TEXT,                    -- 'ambiguous_match' | 'honorary_type_unspecified'
                                                 -- | 'unknown_lifetime_date' | ...
  created_at
)
CREATE INDEX ix_staged_rows_run ON staged_import_rows(import_run_id, source_row_index);
```

`reconciliation_decisions` — officer choices per row before commit.

```
reconciliation_decisions (
  id                     INTEGER PRIMARY KEY,
  staged_import_row_id   INTEGER NOT NULL REFERENCES staged_import_rows(id) ON DELETE CASCADE,
  decided_by             INTEGER NOT NULL REFERENCES users(id),
  decided_at             TEXT NOT NULL,
  action                 TEXT NOT NULL,          -- 'accept' | 'skip' | 'assign_person'
                                                 -- | 'set_membership_type' | 'grant_lifetime_honorary'
  payload_json           TEXT,                   -- e.g. {"person_id":123} or {"base_type":"associate"}
  created_at
)
CREATE INDEX ix_reconcile_row ON reconciliation_decisions(staged_import_row_id);
```

Commit is transactional: for each `staged_import_row`, apply reconciliation
decisions in order, resolve `proposed_action`, write the canonical rows, and
record the audit event referring to `import_run_id`.

## 4. API Conventions

- Base path: `/api/v1`.
- All responses JSON. Errors are RFC 7807 problem+json with a stable `type`
  URI and a machine `code` field (e.g. `code: "member.stale"`).
- Every response carries `X-Request-Id` (also on error).
- Pagination: cursor-based; `?limit=<int, ≤200>&cursor=<opaque>`; response
  envelope `{data, next_cursor}`.
- Filtering: explicit query params per operation, no generic filter DSL.
- Sorting: `?sort=field,-other` from a per-operation allowlist.
- Optimistic concurrency on mutable resources: `If-Match: "<version>"` required
  on updates; conflict → `409 code=stale`.
- Idempotency on non-GET endpoints that commit external effects: client sends
  `Idempotency-Key` header; server stores it in the relevant table and returns
  the original response for repeats. Required for import commit; recommended
  for member create.
- No trusted principal fields in request bodies. Actor comes from session.
- List endpoints never return contact values a caller isn't permitted to see;
  filtering happens in the SQL layer, not the serializer.

### 4.1 Endpoint Catalog (Phase 1)

Session/identity

- `POST   /api/v1/sessions`                 password sign-in
- `DELETE /api/v1/sessions/current`         sign out
- `GET    /api/v1/sessions/current`         current user + effective capabilities
- `POST   /api/v1/auth/recovery/request`    email recovery
- `POST   /api/v1/auth/recovery/consume`    complete recovery, set new password
- `POST   /api/v1/auth/invitations/consume` first-time officer, sets password

Members / persons

- `GET    /api/v1/members`                              search + filter
- `POST   /api/v1/members`                              create person + pending membership
- `GET    /api/v1/members/{id}`                         detail (server-filtered)
- `PATCH  /api/v1/members/{id}`                         update person fields
- `POST   /api/v1/members/{id}/deactivate`
- `POST   /api/v1/members/{id}/reactivate`
- `GET    /api/v1/members/{id}/timeline`                imports + admin changes

Contact methods

- `GET    /api/v1/members/{id}/contact-methods`
- `POST   /api/v1/members/{id}/contact-methods`
- `PATCH  /api/v1/contact-methods/{id}`
- `POST   /api/v1/contact-methods/{id}/archive`
- `POST   /api/v1/contact-methods/{id}/make-primary`

Sharing preferences (per pattern B, always append-only)

- `GET    /api/v1/contact-methods/{id}/visibility`      current + history
- `POST   /api/v1/contact-methods/{id}/visibility`      new event
- `GET    /api/v1/members/{id}/acs-ares-sharing`
- `POST   /api/v1/members/{id}/acs-ares-sharing`

Membership lifecycle

- `POST   /api/v1/members/{id}/memberships/apply`       creates pending membership
- `POST   /api/v1/memberships/{id}/approve`             body: {base_type}
- `POST   /api/v1/memberships/{id}/reject`
- `POST   /api/v1/memberships/{id}/lifecycle`           body: {to: inactive|resigned|deceased}

FCC verification

- `POST   /api/v1/memberships/{id}/fcc-verifications`
- `POST   /api/v1/fcc-verifications/{id}/revoke`

Honorary grants

- `POST   /api/v1/memberships/{id}/honorary-grants`
- `PATCH  /api/v1/honorary-grants/{id}`
- `POST   /api/v1/honorary-grants/{id}/expire`
- `POST   /api/v1/honorary-grants/{id}/revoke`

Notes (subject-scoped)

- `GET    /api/v1/notes?subject_kind=&subject_id=`
- `POST   /api/v1/notes`
- `PATCH  /api/v1/notes/{id}`                            writes a revision

Imports

- `POST   /api/v1/imports`                               upload (multipart)
- `GET    /api/v1/imports`                               list
- `GET    /api/v1/imports/{id}`
- `GET    /api/v1/imports/{id}/rows`                     paged staged rows
- `POST   /api/v1/imports/{id}/rows/{rowId}/decisions`   reconciliation decision
- `POST   /api/v1/imports/{id}/preview`                  recompute after decisions
- `POST   /api/v1/imports/{id}/commit`                   Idempotency-Key required
- `POST   /api/v1/imports/{id}/discard`

Exports

- `POST   /api/v1/exports/members`                       body: {fields, filter}; audited

Audit

- `GET    /api/v1/audit-events`                          search

Health

- `GET    /healthz`, `GET    /readyz`                    no auth; no private data

Roles & capabilities (webmaster/administrator only)

- `GET    /api/v1/capabilities`                          catalog
- `GET    /api/v1/roles`
- `GET    /api/v1/users`                                 minimal, admin only
- `POST   /api/v1/users/{id}/role-grants`
- `POST   /api/v1/role-grants/{id}/revoke`

Every endpoint entry in the capability catalog file records: operation id,
required capability, resource-authorization rule, confirmation level (none,
recent-auth, explicit-confirm), audit action name, and `ai_tool_eligibility`
(never/read-only/curated).

## 5. Capability Catalog Seed

Codes are stable identifiers; adding a capability requires a migration.

| Code | Category | Description |
| --- | --- | --- |
| `session.self.read` | system | Read own session and capabilities. |
| `member.read` | membership | Read canonical member data (server-filtered by role). |
| `member.export` | membership | Export member data. |
| `member.create` | membership | Create a person + pending membership. |
| `member.update` | membership | Update person fields. |
| `member.deactivate` | membership | Deactivate/reactivate a member. |
| `contact_method.write` | membership | Add/edit/archive contact methods. |
| `sharing_pref.write.officer` | membership | Change directory/ACS-ARES prefs on behalf of a member. |
| `membership.approve` | membership | Approve/reject memberships and set base type. |
| `membership.lifecycle` | membership | Change lifecycle to inactive/resigned/deceased. |
| `fcc.verify` | membership | Record/revoke FCC verification. |
| `honorary.grant` | membership | Create/edit/expire/revoke honorary grants. |
| `notes.write.officer` | membership | Write officer-visibility notes. |
| `notes.write.treasurer` | treasury | Write treasurer-visibility notes. |
| `notes.read.treasurer` | treasury | Read treasurer-visibility notes. |
| `import.upload` | membership | Upload and preview imports. |
| `import.commit` | membership | Commit reconciled imports. |
| `audit.read` | audit | Search audit events. |
| `role.grant` | system | Grant/revoke roles and direct capabilities. |
| `user.invite` | system | Invite a new officer/user. |
| `integration.config.write` | system | Manage non-secret integration settings. |
| `system.admin` | system | Bootstrap and low-level admin. |

Seed role → capability mapping (Phase 1):

- `administrator`: all capabilities.
- `webmaster`: `system.admin`, `integration.config.write`, `audit.read`,
  `user.invite`, `role.grant`, `session.self.read`.
- `president`, `vice_president`, `secretary`: `session.self.read`,
  `member.*` (read/create/update/deactivate/export), `contact_method.write`,
  `sharing_pref.write.officer`, `membership.approve`, `membership.lifecycle`,
  `fcc.verify`, `honorary.grant`, `notes.write.officer`, `import.upload`,
  `import.commit`, `audit.read`.
- `treasurer`: same as president plus `notes.write.treasurer` and
  `notes.read.treasurer`. Payment capabilities appear in Phase 2.
- `trustee`, `activities_manager`: `session.self.read`, `member.read`,
  `contact_method.write` (limited to persons they created — enforced via
  resource authz, not a separate cap in Phase 1), `notes.write.officer`.
- `acs_coordinator`: `session.self.read`, `member.read`. Real ACS caps in
  Phase 7.
- `member`: `session.self.read` only in Phase 1 (no member UI yet).

## 6. Authentication Flow

1. Bootstrap: `portalctl bootstrap-admin --email x@bcars.org` prints a
   one-time invitation link that must be consumed within 24h. No default
   password ever exists. Fails if any active `administrator`-role user exists
   unless `--force` is provided (audited).
2. Sign-in: `POST /api/v1/sessions` with email+password. Argon2id verify;
   constant-time failure regardless of email existence. Failed attempts
   increment `failed_login_count`; ≥10 within 15 min sets `locked_until`.
   On success, session row created and cookie issued.
3. Recovery: `POST /auth/recovery/request` with email. Always returns 204.
   If a user exists, an `email_links` row is created with a random 32-byte
   token (only the sha256 is stored) and mailed to the verified address.
4. Consume: `POST /auth/recovery/consume` with `token` + new password.
   Verifies unexpired unused token, invalidates all sessions for the user,
   rotates cookie.
5. Recent-auth gate: any capability marked `confirmation: recent-auth`
   requires `session.last_seen_at` within 5 minutes of an explicit password
   re-entry (a `POST /sessions/current/reauth`).
6. Self-protection: a user cannot `role.revoke` their own last
   `administrator` grant, delete their own account, or approve their own
   change request (Phase 3).

## 7. Import State Machine

```
uploaded -> validated -> previewed -> committed
    \_______\_________\_ discarded
                      \_ failed (only from validated)
```

Transitions:

- `uploaded → validated`: parser + normalizer runs, staged rows written,
  match candidates computed. Failing rows produce `proposed_action=invalid`.
- `validated → previewed`: preview endpoint recomputes proposed changes
  incorporating any reconciliation decisions. Idempotent.
- `previewed → committed`: commit endpoint requires no rows with
  `requires_manual=1 AND no reconciliation_decision`. Runs inside a
  transaction, writes canonical rows, emits per-row audit events plus a
  summary event, updates `result_summary_json`. Idempotency key ensures a
  retry returns the original outcome.
- `discarded` / `failed`: terminal; staged rows retained for audit unless
  webmaster explicitly purges (audited).

Matching order (per PLANNING): preserved external id → normalized call sign
→ normalized email → manual. Name-only never auto-matches. Ambiguous
matches (>1 candidate) always set `requires_manual=1`.

Honorary handling: any imported `Membership Type = Honorary` sets
`requires_manual=1` with `manual_reason=honorary_type_unspecified` unless
the row is one of the two known 2055 records (identified by preserved
external id list in migration) — those default to a `grant_lifetime_honorary`
proposal with `base_type=associate`, still requiring officer confirmation.

Sentinel dates:
- `01/01/0001` in `Current Until` → normalized to `null`; no manual flag.
- `12/31/2055` → `manual_reason=lifetime_like_date_needs_confirmation`
  unless it is one of the two known rows.

Sharing preference handling (importer):
- For every created contact method, insert a `contact_method_visibility_event`
  with `source=import_default` and audience derived from the reconciled
  membership type (Full → `full_members`, Associate → `hidden`). This is a
  *legacy default*, not consent; the officer UI surfaces "sharing pref needs
  review" for every row that came from import.
- For every person that becomes Full with a verified FCC record (Phase 1
  verification may be `verification_source=legacy_import`), insert an
  `acs_ares_sharing_event` with `participates=1, source=import_default`.

## 8. Audit Event Taxonomy

Actions follow `<resource>.<verb>`:

- `session.signin`, `session.signout`, `session.reauth`
- `auth.recovery.request`, `auth.recovery.consume`, `auth.invite.consume`
- `member.create`, `member.update`, `member.deactivate`, `member.reactivate`,
  `member.export`
- `contact_method.create`, `contact_method.update`, `contact_method.archive`,
  `contact_method.make_primary`
- `sharing_pref.contact_method.set`, `sharing_pref.acs_ares.set`
- `membership.approve`, `membership.reject`, `membership.lifecycle`
- `fcc.verify`, `fcc.revoke`
- `honorary.grant`, `honorary.update`, `honorary.expire`, `honorary.revoke`
- `notes.create`, `notes.update`
- `import.upload`, `import.validate`, `import.preview`,
  `import.reconcile_decide`, `import.commit`, `import.discard`
- `role.grant`, `role.revoke`, `capability.grant`, `capability.revoke`,
  `user.invite`, `user.lock`
- `authz.denied` (paired with the intended action in `detail_json.action`)

Every denied sensitive request writes an `authz.denied` event with
`reason_code` but no field values from the request body.

## 9. Directory Field Filtering

Even though Phase 1 does not ship a member-facing directory UI, the read
endpoints must implement the filter now so Phase 3 is a UI-only add-on.

Effective visibility for a caller `C` viewing person `P`:

1. If `C` has `member.read` capability → full detail permitted (server still
   redacts treasurer-only notes unless `notes.read.treasurer`).
2. Else if `C` is an active Full member AND `P` has an approved,
   non-`inactive`/`resigned`/`deceased` membership:
   - Name and (when present) call sign always visible.
   - For each contact method of `P`:
     - Look up latest `contact_method_visibility_events` row; fall back to
       type-default.
     - Include the value iff audience ∈ {`full_members`}.
3. Else → 404 (never 403; do not leak existence).

The service returns already-filtered structs; DTO serializers cannot promote
a hidden field.

## 10. Coverage / Payment Seams (Phase 2 preview)

Phase 1 does not create `coverage_events`, `dues_rates`, `payments`, or
`payment_batches`. It does:

- Retain imported `Current Until` in `memberships.legacy_current_until`.
- Ship `honorary_grants` in full, including the two lifetime records.
- Reserve capability codes (`payment.*`, `coverage.*`) — added in Phase 2
  migrations, not seeded now.

Phase 2's first migration:

1. Creates `dues_rates`, `coverage_events`, `payments`, `payment_batches`.
2. For each membership with `legacy_current_until IS NOT NULL`, inserts one
   `coverage_events` row with `reason='legacy_import'` and `source_import_run_id`.
3. Drops `legacy_current_until` and `legacy_current_until_note`.

## 11. Testing Strategy

- **Unit** — domain services with an in-memory SQLite per test; no HTTP.
- **Authorization matrix** — table-driven: for each capability code, list
  (role, allowed?) and assert via a fake principal + a representative
  operation. Regenerated snapshot committed.
- **Contract** — golden OpenAPI JSON; CI fails on unreviewed diff.
- **Import** — driven by fixtures in `fixtures/groupsio_contact/*` derived
  by hand (no real data). Cover: happy path, ambiguous email, unknown
  external id, sentinel dates, both known 2055 rows, an unexpected 2055,
  honorary with no specified type, invalid phone, shared email.
- **Migrations** — up + down round-trip test; seed capabilities checked.
- **Backup/restore** — a script that copies the WAL-checkpointed DB to a
  target, then restores into an isolated dir and re-runs a smoke test.

## 12. Configuration Surface (env)

```
PORTAL_LISTEN_ADDR=:8080
PORTAL_DB_PATH=/var/lib/bcars-portal/portal.db
PORTAL_SESSION_COOKIE_NAME=bcars_portal
PORTAL_SESSION_TTL=168h
PORTAL_SESSION_RECENT_AUTH_TTL=5m
PORTAL_PASSWORD_PEPPER=<32 random bytes base64>       # required
PORTAL_MAIL_MODE=smtp|filelog
PORTAL_MAIL_SMTP_HOST=
PORTAL_MAIL_SMTP_PORT=587
PORTAL_MAIL_SMTP_USER=
PORTAL_MAIL_SMTP_PASSWORD=
PORTAL_MAIL_FROM=portal@bcars.org
PORTAL_MAIL_REPLY_TO=qsl@bcars.org
PORTAL_LOG_LEVEL=info
PORTAL_TRUST_PROXY_HEADER=false
```

No secret is ever readable back through the API.

## 13. Deployment Notes

- Single binary. Migrations run at startup only when `--migrate` flag is
  set (default: refuse to start on a mismatched schema version).
- SQLite file lives on a persistent volume; backup script uses
  `sqlite3 .backup` (safe with WAL).
- Health: `/healthz` returns 200 iff process is up. `/readyz` returns 200
  iff DB is reachable and schema version matches build.
- No default admin user. Bootstrap requires operator on the host.

## 14. Open Items Not Blocking Phase 1

- Exact Google Workspace sending method (SMTP relay vs. Gmail API) — the
  `mail.Sender` interface abstracts either. Resolve before recovery emails
  are enabled in production.
- Precise trustee / activities-manager resource-authz rules — Phase 1 ships
  a conservative default (write only records they created); refine after
  officer review.
- Directory UI (Phase 3), payments (Phase 2), files (Phase 4), AI (Phase 5+),
  ACS import (Phase 7).
