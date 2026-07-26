# Phase 1 Implementation Plan

Companion to `phase-1-design.md`. Tasks are grouped by the eight workstreams
from `../PLANNING.md`. Each task lists prerequisites, deliverable, and the
verification that must pass before it is called done. Tasks within a workstream
are ordered; workstreams themselves can run partly in parallel after WS1 lands.

Legend: `[blocks: WSx.y]` means a downstream task depends on this one.

## WS1 — Repository and Engineering Foundation

**WS1.1 Repository skeleton**
- Create `bcars-portal/` layout per §2 of the design.
- Add `README.md` (build, run, test, bootstrap), `LICENSE` decision recorded
  in an ADR draft, `.gitignore`, `.gitattributes`, `.editorconfig`.
- Add `.env.sample` with every variable from design §12; no real values.
- Add `Makefile` targets: `build`, `test`, `lint`, `sqlc`, `migrate`,
  `openapi`, `run`.
- Done: `make build` produces `cmd/portal` and `cmd/portalctl` binaries that
  print `--help`. `git status` clean.
- [blocks: everything]

**WS1.2 PII & secret ignore + pre-commit check**
- `.gitignore` covers: `*.db`, `*.db-wal`, `*.db-shm`, `data/**`,
  `backups/**`, `uploads/**`, `extracted/**`, `.env`, `.env.*`
  (except `.env.sample`), `transcripts/**`.
- Add a `scripts/check-no-secrets.sh` that greps for a small set of patterns
  (email regex hits inside `fixtures/` are allowed only when the file lives
  under `fixtures/synthetic/`). Wired into `make lint` and CI.
- Done: script returns non-zero when a fake `.env` with a value is added,
  zero on the clean repo.

**WS1.3 Toolchain pinning**
- `go.mod` uses Go 1.26. `go.work` not used.
- Pin: Huma v2, `modernc.org/sqlite`, `pressly/goose/v3`, `sqlc-dev/sqlc`
  (dev tool via `tools.go`), `golang.org/x/crypto`, `stretchr/testify`.
- `sqlc.yaml` configured for sqlite engine, output under
  `internal/db/sqlc/`.
- Done: `go build ./...` and `sqlc generate` succeed on empty query set.

**WS1.4 Structured logging + request-id middleware**
- `internal/obs`: slog JSON handler wiring, level from env, redaction
  helpers.
- Middleware injects request id (from `X-Request-Id` if trusted, else new
  ULID), populates context, adds it to every audit event.
- Done: unit test asserts that a request without an incoming id gets one
  and that the logger includes it.

**WS1.5 ADRs**
- Add ADR-0001..0008 stubs from design §1. Each 1 page.
- Done: files exist under `docs/adr/`, referenced from the design doc.

**WS1.6 CI pipeline**
- GitHub Actions workflow: `go test -race ./...`, `staticcheck`,
  `golangci-lint`, `make sqlc-diff` (fails on drift), `make openapi-diff`,
  migration up/down round-trip against a temp SQLite file, secrets check.
- Done: pipeline green on WS1.1..WS1.5 baseline.

**WS1.7 Synthetic fixtures**
- `fixtures/synthetic/groupsio_contact.json` and `.csv` covering: 8 clean
  Full members, 4 Associate, 2 lifetime honorary (matching the 2055
  pattern with fabricated external ids we own), 1 ambiguous email (shared
  by two persons), 1 unknown external id + sentinel `01/01/0001`,
  1 unexpected `12/31/2055`, 1 Honorary with unspecified type, 1 invalid
  phone. No real BCARS data.
- Done: fixtures parse; documented in `fixtures/README.md`.

## WS2 — API & Application Contracts

**WS2.1 Huma app scaffold**
- `internal/httpapi.NewRouter()` builds a Huma API with base path
  `/api/v1`, error middleware producing RFC 7807, request-id propagation.
- Done: `GET /openapi.json` returns a valid document; `GET /healthz` returns 200.

**WS2.2 Cross-cutting conventions**
- Pagination envelope, cursor codec, `If-Match`/`ETag` helpers, idempotency
  middleware (backed by a `idempotency_keys` sub-table shared across
  endpoints).
- Structured error codes: `stale`, `validation`, `forbidden`,
  `not_found_or_forbidden`, `conflict`, `idempotency_mismatch`.
- Done: contract tests exercise each helper via a dummy endpoint.

**WS2.3 Capability catalog file + registration**
- `internal/authz/catalog.go` declares each capability with metadata
  (code, description, category, ai_tool_eligibility).
- Each Huma operation registers a `RequiredCapability` and audit action;
  a startup check refuses to boot if any operation is missing metadata.
- `make openapi` emits `docs/openapi.json` and
  `docs/capability-catalog.json`. CI diffs them.
- Done: any new operation without metadata fails the build.

**WS2.4 OpenAPI baseline**
- Register a stub for every operation in design §4.1 returning
  501-not-implemented. Populate types.
- Done: `docs/openapi.json` contains all endpoints; CI passes.

## WS3 — Database & Migration Baseline

**WS3.1 Initial schema migrations**
- Goose migration `0001_init.sql` creating tables from design §3.1..3.6
  (identity, preferences, memberships, notes, authn/authz, audit),
  minus imports.
- Goose migration `0002_imports.sql` for §3.7.
- `PRAGMA foreign_keys=ON`, `journal_mode=WAL`, `busy_timeout=5000` applied
  in DB open helper (not in migrations).
- Down migrations mirror.
- Done: `go test ./internal/db/... -run Migrations` runs up → down → up
  and passes.

**WS3.2 Capability & role seed**
- `0003_seed_capabilities.sql` inserts capability rows from design §5.
- `0004_seed_roles.sql` inserts roles and `role_capabilities`.
- Seed migrations are idempotent (`INSERT ... ON CONFLICT DO NOTHING` or
  guarded selects).
- Done: after `up`, a query proves the expected role → capability edges
  match a checked-in golden JSON.

**WS3.3 sqlc query surface**
- Write queries needed for WS4 and WS6 read/write paths in
  `internal/db/queries/*.sql` with sqlc annotations. Generated code under
  `internal/db/sqlc/`.
- Done: `sqlc generate` clean; `make sqlc-diff` passes in CI.

**WS3.4 Optimistic concurrency helper**
- Repository layer wraps updates: `UPDATE ... WHERE id=? AND version=?`
  bumping `version=version+1`; returns typed `ErrStale` on 0 rows affected.
- Done: unit test covers concurrent update conflict.

## WS4 — Officer Authentication & Authorization

**WS4.1 Password hashing**
- `internal/authn/password`: argon2id encode/verify, pepper from env,
  stored params per hash for future rotation.
- Done: property test — verify(hash(x)) == true, verify(hash(x), other) == false,
  constant-time compare used.

**WS4.2 Session store**
- SQLite-backed sessions with rotation, expiry, and revocation.
- Cookie: HttpOnly, Secure, SameSite=Lax, name from env, no data.
- Middleware resolves session → principal (user + effective capabilities),
  attaches to context.
- Done: unit + integration tests for issue/rotate/revoke/expire.

**WS4.3 Sign-in / sign-out / current-session endpoints**
- `POST /sessions`, `DELETE /sessions/current`, `GET /sessions/current`.
- Constant-time failure regardless of email existence. Lockout counters.
- Audit events: `session.signin` (allowed/denied), `session.signout`.
- Done: authz-matrix test + integration test using in-process HTTP.

**WS4.4 Email service interface + SMTP + filelog impls**
- `internal/mail.Sender` with a `Send(ctx, msg)` signature; `msg` carries a
  template id + typed payload (never raw HTML at the call site).
- SMTP impl for Google Workspace relay; `filelog` writes JSON files under
  a configured dir for dev/tests (no PII in application logs).
- Templates for recovery, invitation, verify-email under
  `internal/mail/templates/`.
- Done: `filelog` sender produces a message from a test fixture; SMTP impl
  has a unit test with a stub server (`mhog` or a bare net.Listen).

**WS4.5 Recovery & invitation flows**
- `POST /auth/recovery/request`, `/auth/recovery/consume`,
  `/auth/invitations/consume`.
- `email_links` rows; token generated with `crypto/rand`, only sha256 stored.
- Consuming an invitation sets initial password and marks email verified.
- Done: end-to-end test — request → capture filelog message → consume →
  new session works; second consume fails.

**WS4.6 Policy layer**
- `internal/authz.Policy` exposes `Authorize(ctx, principal, action,
  resource)`. Loads effective capabilities once per request; checks
  capability + resource rule. Denies default.
- Resource rules: for the trustee/activities-manager conservative rule
  ("write only records they created"), the repository returns
  `created_by_user_id` and the policy compares.
- Done: authz-matrix table test covers every (role, capability) pair.

**WS4.7 `portalctl bootstrap-admin`**
- Command generates an invitation-token URL for a supplied email; refuses
  if an active administrator already exists unless `--force` (audited).
- Done: integration test against an empty DB creates the admin after
  consuming the printed URL.

## WS5 — Staged Groups.io Import

**WS5.1 Parsers**
- `internal/domain/import/parse`: strict JSON parser matching the Groups.io
  table envelope; CSV parser using the exact header order documented in
  the design.
- Reject files that don't match the expected schema with a structured error.
- Done: fixture tests for both formats.

**WS5.2 Normalization**
- Call sign upper + trim; email lower + trim; phone → digits with country
  handling; date parser accepting the observed Groups.io formats;
  membership type case-fold; checkbox to bool; sentinel dates → null +
  reason code.
- Done: table-driven unit tests, including the two 2055 rows (recognized
  by preserved external id list stored in a migration seed table or
  compile-time constant — pick constant for now, documented in the
  importer package).

**WS5.3 Match engine**
- Match order: external id → call sign → email → manual. Ambiguity always
  sets `requires_manual=1`.
- Done: fixture tests cover each match method and the ambiguous case.

**WS5.4 Staging write path**
- `POST /api/v1/imports` (multipart) writes the raw file's sha256, stores
  the parsed rows as `staged_import_rows` inside one transaction, and
  transitions `uploaded → validated`.
- Raw file body is not persisted after parsing.
- Done: integration test end-to-end from upload → validated with all
  expected rows.

**WS5.5 Reconciliation & preview**
- `POST /imports/{id}/rows/{rowId}/decisions` appends a decision.
- `POST /imports/{id}/preview` recomputes `proposed_action` and
  `proposed_changes_json` per row given the current decisions.
- Done: integration test toggles decisions and sees preview change.

**WS5.6 Commit**
- `POST /imports/{id}/commit` with `Idempotency-Key`. Transactional; writes
  persons, contact methods, memberships, `membership_approvals` (auto for
  legacy imports with `verification_source=legacy_import`),
  `fcc_verifications` for Full members with a call sign,
  `honorary_grants` for the two 2055 rows, default sharing preference
  events per design §7, and per-row + summary audit events.
- Refuses to commit if any row has `requires_manual=1 AND no decision`.
- Done: full-fixture integration test yields the expected canonical state
  and audit trail; a second commit with the same idempotency key returns
  the original response and creates no new rows.

**WS5.7 Discard + list + inspect**
- `POST /imports/{id}/discard` transitions terminal.
- `GET /imports`, `GET /imports/{id}`, `GET /imports/{id}/rows` list and
  paginate.
- Done: integration tests + authz-matrix coverage.

## WS6 — Administrative Member Operations

**WS6.1 Person + membership CRUD (server-filtered)**
- `POST /members`, `PATCH /members/{id}`, `POST /members/{id}/deactivate`,
  `POST /members/{id}/reactivate`.
- Field filtering per design §9 applied server-side even for officers
  reading their own view.
- Done: integration tests including a Full-member principal seeing the
  filtered detail vs. an officer seeing full detail.

**WS6.2 Contact methods**
- CRUD + `archive` + `make-primary`. No unique constraint on value.
- Done: shared-email test proves two persons can carry the same email.

**WS6.3 Sharing preference endpoints**
- `GET`/`POST /contact-methods/{id}/visibility`;
- `GET`/`POST /members/{id}/acs-ares-sharing`.
- Writes always insert a new event; reads return current + history.
- Done: append-only test — mutating twice yields two events, current
  reflects the latest.

**WS6.4 Membership lifecycle**
- Apply / approve / reject / lifecycle change. Approval requires a base
  type; rejection requires a reason. Deactivate/reactivate updates
  `lifecycle` but not history.
- Done: state-machine test covers legal and illegal transitions.

**WS6.5 FCC verification**
- Manual verification create + revoke; `verification_source` values
  restricted to the design's list.
- Done: authz test — non-officer can't call; officer succeeds.

**WS6.6 Honorary grants**
- CRUD + expire + revoke. Constraint enforced: `is_lifetime=1` implies
  `ends_on IS NULL`.
- Done: integration test that a lifetime grant can be revoked and audit
  reflects the reason.

**WS6.7 Notes**
- Create + edit (edit writes a `note_revisions` row); listing filtered by
  caller's visibility caps (treasurer notes hidden without
  `notes.read.treasurer`).
- Done: authz test matrix.

**WS6.8 Member search + timeline**
- `GET /members` — filter by name/call sign/type/lifecycle/data-quality
  flag; cursor pagination.
- `GET /members/{id}/timeline` — merged view of import runs, approvals,
  contact-method changes, honorary events, sharing-preference events.
- Done: integration test against the committed fixture import.

**WS6.9 Export**
- `POST /exports/members` — synchronous CSV/JSON produced from a filtered
  query, respecting caller's field visibility. Every export emits a
  `member.export` audit event including the field list and row count.
- Done: audit assertion + shape assertion.

## WS7 — Administrative UI

**WS7.1 Layout + auth pages**
- Base template with skip-link, plain typography, no external assets.
  Sign-in, sign-out, "you were signed out", recovery request/consume,
  invitation-consume pages. HTMX loaded from `internal/web/static/`.
- Done: accessibility spot-check with keyboard-only navigation; each page
  renders without JS enabled (HTMX enhances, not required).

**WS7.2 Dashboard**
- Cards linking to Member search, Import review (with count of
  requires-manual rows across active runs), Data-quality flags list,
  Recent audit activity (last 20 events for authorized viewers).
- Done: renders using the same service layer as APIs.

**WS7.3 Member search + detail**
- Server-rendered list with HTMX-enhanced filters; detail page shows
  timeline, contact methods, sharing prefs, honorary grants, notes.
  Edit forms round-trip through the same API operations.
- Done: end-to-end browser test (`chromedp` or plain HTTP with
  form-follow) covers a search-edit-save cycle.

**WS7.4 Import review UI**
- Upload page; run detail page listing staged rows with filters
  (requires-manual, invalid, conflict, unchanged); per-row decision UI;
  preview and commit buttons; result summary page.
- Done: fixture-driven test walks from upload to commit and asserts DOM
  markers for each step.

**WS7.5 Officer-friendly error rendering**
- Common error templates for validation, stale-version, forbidden,
  not-found. No stack traces; friendly wording.
- Done: snapshot tests for each error class.

## WS8 — Operations & Recovery

**WS8.1 Health/readiness**
- `/healthz`, `/readyz` per design §13. `/readyz` checks DB reachable and
  schema version matches build.
- Done: integration test — stop DB access, `/readyz` fails.

**WS8.2 Backup + restore procedure**
- `portalctl backup --to <dir>` runs `sqlite3 .backup`, gpg-encrypts with a
  configured recipient key, writes a manifest with sha256 + timestamp.
- `portalctl restore --from <path> --into <dir>` restores an encrypted
  backup into an isolated directory and runs a smoke migration check.
- Documented runbook at `docs/runbooks/backup-restore.md`.
- Done: end-to-end script produces a backup, restores into a temp dir,
  and passes a smoke query.

**WS8.3 Log retention & redaction**
- `internal/obs` redaction wrapper; a golden test asserts that a message
  containing an email or phone gets redacted before serialization.
- Documented log rotation guidance in the ops runbook.
- Done: golden test + runbook.

**WS8.4 Deployment package**
- `Dockerfile` (multi-stage, distroless final) or `systemd` unit +
  install script — pick one and document.
- `docs/runbooks/deploy.md` covers config, secret rotation, upgrade,
  rollback (previous binary + no destructive migration in Phase 1).
- Done: a smoke deploy on a scratch host succeeds and `/readyz` is green.

**WS8.5 Officer handoff checklist**
- `docs/runbooks/handoff.md`: who owns backups, how to bootstrap a new
  admin, how to run an import, how to inspect the audit log, how to
  restore.
- Done: checklist reviewed by a second person unfamiliar with the code.

## Cross-Workstream Gates

**Gate G1 — Foundation ready** (blocks WS3+): WS1.1..WS1.6 green.
**Gate G2 — Data ready** (blocks WS5/WS6): WS3.1..WS3.4 green.
**Gate G3 — Auth ready** (blocks WS5/WS6/WS7 protected endpoints):
WS4.1..WS4.6 green.
**Gate G4 — Import ready** (blocks Phase 1 acceptance): WS5.1..WS5.6 green.
**Gate G5 — Officer ops ready**: WS6.1..WS6.9 green.
**Gate G6 — UI ready**: WS7.1..WS7.5 green.
**Gate G7 — Ops ready**: WS8.1..WS8.5 green.

## Phase 1 Acceptance Mapping

For each bullet in PLANNING's "Phase 1 Verification and Acceptance", the
test that proves it:

| Acceptance bullet | Test |
| --- | --- |
| Clean env migrate + bootstrap first admin | WS3.1 round-trip + WS4.7 |
| Officer authenticates; unauth cannot access member APIs | WS4.3 + WS4.6 authz matrix |
| 62-row export staged without changing canonical data | WS5.4 fixture test |
| Preview identifies creates/updates/conflicts/sentinels/manual | WS5.5 fixture test |
| Two 2055 rows → lifetime honorary Associate grants; others require classification | WS5.2 + WS5.6 fixture assertions |
| Commit permits shared emails and no orphan logins | WS5.6 shared-email fixture; WS4 users unchanged by import |
| Search/create/update/deactivate/reactivate/export via API + UI | WS6.1/6.8/6.9 + WS7.3 |
| Concurrent edits and repeat commits cannot silently overwrite | WS3.4 + WS5.6 idempotency test |
| Every sensitive read/export/import/mutation produces audit event | Per-endpoint audit assertions across WS4..WS6 |
| Authz tests cover allowed + denied cases | WS4.6 matrix + per-endpoint tests |
| OpenAPI + capability catalog checked in CI | WS2.3 + WS2.4 diff jobs |
| Backup restore is understandable and works | WS8.2 + WS8.5 |
| No real member data / creds / db / logs in Git | WS1.2 secrets-check job |

## Explicit Deferrals (Reiterated)

Not built in Phase 1: dues rates, coverage events, payments, payment batches,
member-facing directory browsing, self-service, change requests, files,
AI, ACS resource management, Groups.io writes, calendar editing.

Seams preserved: capability categories `treasury` / `system` present but
under-populated; audit action namespace open; contact-method visibility
history and ACS/ARES history already dated; `memberships.legacy_current_until`
lands the imported Current Until values for Phase 2 backfill.
