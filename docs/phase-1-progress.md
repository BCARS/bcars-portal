# Phase 1 Progress and Execution Map

Last reconciled: 2026-08-09 (against observed behavior, not bead-closure state)

This is the human-readable map of Phase 1 work. Beads is the durable source of
truth: run `bd bootstrap --yes` on a fresh clone, then `bd ready` and
`bd show <id>`.

## Target outcome

An authorized officer can sign in, manage canonical member records, stage and
reconcile a Groups.io CSV/JSON export, commit it atomically, inspect audit
history, and perform tested encrypted backup/restore. Phase 1 is local and
API-first. Production packaging and deployment automation are explicitly
deferred to `bcars-portal-fmc.8`.

## Status: COMPLETE, after reconciliation

### What happened, and why it is written down

On 2026-08-08 this document read "COMPLETE" because every Phase 1 bead was
closed. A review of `main` at `f743d08` found that closure did not mean the
acceptance criteria were met. All then-existing mechanical gates passed against
a tree where:

- the production API could not resolve a signed-in principal at all — nothing
  wrapped the router in `authn.Middleware`, so every authenticated call 401'd
- declared capabilities were recorded but never enforced, so any signed-in
  user could read the audit log, export members, grant roles, and commit
  imports
- a fresh installation had no route to a working administrator
- password recovery was unreachable on both surfaces

The gates could not see any of it, because every test built its own router
instead of starting the one that ships.

**Bead closure is not evidence of working software.** That sentence is the
reason this section exists. The reconciliation is finished; the lesson is kept
so the next reader does not repeat the inference.

### Completion gates

| Gate | Bead | Status |
| --- | --- | --- |
| Phase 1 member operations | `bcars-portal-5eg` | Met — re-evaluated 2026-08-09 |
| Phase 1 administrative MVP | `bcars-portal-dz0` | Met — re-evaluated 2026-08-09 |
| Production-assembly smoke test | `bcars-portal-fmc.12`, `.23` | Met — `make smoke` in CI |

Re-evaluation was withheld until every P0/P1 child of `bcars-portal-fmc` was
closed. That condition was met on 2026-08-09.

**On "no Phase 1 501 stubs".** Re-evaluating `dz0` on 2026-08-09 found one:
`GET /members/{id}/acs-ares-sharing` was an unconditional 501, so a sharing
preference could be set and never read back. It was fixed as
`bcars-portal-fmc.24` rather than recorded as an exception, at the owner's
direction, on the grounds that it is a functional gap rather than hardening.
No unconditional 501 remains; every other `ErrNotImplemented` in the tree is a
nil-dependency guard that resolves once its service is wired, which the
production assembly does.

`dz0` also requires "documented backup/restore and deployment procedures".
Backup and restore are implemented, encrypted, and have a runbook. Deployment
is documented but has no checked-in packaging — deferred to
`bcars-portal-fmc.8` at the owner's direction, on the basis that deployability
was never intended to be part of Phase 1.

The deployment caveat is stated here so the gate's status is legible rather
than inferred. That inference is what produced the 2026-08-08 failure.

### The gate that was missing

`make smoke` builds both binaries, runs them as processes from a directory
outside the source tree, and drives the real HTTP surface: migrate,
`bootstrap-admin`, consume the invitation, a capability-guarded read as
administrator (200), the same read for an under-privileged principal (403),
anonymous (401), and a password-recovery round trip.

It is verified load-bearing rather than merely green. Reverting each defect in
turn makes it fail:

| Reverted | Failure |
| --- | --- |
| Remove the `authn.Middleware` wrap | 401 on session lookup |
| Disable the capability check | under-privileged read not denied |
| `bootstrapRoleCode = ""` | 403 on session lookup |
| Add a repo-relative file dependency | server never starts |

### Reconciliation (`bcars-portal-fmc`) — closed

Every finding and its follow-ups, 2026-08-08/09, PRs #32-#48:

| Bead | PR | Scope |
| --- | --- | --- |
| `fmc.1` | #32 | Capability enforcement + generic audit emission (P0) |
| `fmc.2` | #33 | Production wiring: `authn.Middleware`, `EmailLinkService` |
| `fmc.3` | #36 | `bootstrap-admin` produces a working administrator |
| `fmc.4` | #37 | Recovery and invitation flows — five distinct defects |
| `fmc.5` | #35 | Honorary grant update and expire |
| `fmc.6` | #42 | Backup encryption (age), manifest verification, runbook |
| `fmc.7` | #34 | Audit event filters and cursor pagination |
| `fmc.9` | #46 | Client address hashed keyed, not a timestamp |
| `fmc.10` | #37 | Renderer names the template it could not find |
| `fmc.11` | #45 | `seed-demo` compiled out of production builds |
| `fmc.12` | #38 | Production-assembly smoke test |
| `fmc.13` | #44 | Secure session cookies from one shared configuration |
| `fmc.14` | #39 | Password pepper loaded from the environment |
| `fmc.15` | #43 | Honorary revoke detects version conflicts |
| `fmc.17` | #40 | Invitation creation endpoint |
| `fmc.18` | #41 | Invitation role rule relaxed to `role.grant` |
| `fmc.19` | #47 | Version-conflict detection across five more mutations |
| `fmc.23` | #48 | Smoke test runs the binaries outside the source tree |
| `fmc.24` | #50 | ACS/ARES sharing preference readable through the API |

Ten of those were found while fixing something else, not by the original
review. The recurring shapes were worth more than the individual fixes:

- **Optimistic concurrency that silently no-ops.** Six `:exec` queries bound a
  `version` parameter and could not detect a mismatch, so a stale write
  reported success and changed nothing. Three were live.
  `scripts/check-version-conflicts.sh` now fails the build on a seventh.
- **Two places that had to agree, with nothing forcing them to.** Cookie
  attributes, purpose constants, link paths, and template registration each
  drifted this way. Each is now derived from one source; `authn.Purpose` is a
  struct specifically so a string literal fails to compile.
- **Checks that could never have run.** `PRAGMA foreign_key_check` was scanned
  as an integer; `hashIP` hashed a timestamp; a template test passed a struct
  missing a field while render errors were being swallowed.

### Configuration introduced during reconciliation

Three secrets are now required at runtime. None may be committed or baked into
an image. See [deployment.md](deployment.md) and
[runbooks/backup-restore.md](runbooks/backup-restore.md).

| Variable | Required by | If lost |
| --- | --- | --- |
| `PORTAL_PASSWORD_PEPPER` | the server, for every password hash | every account goes through recovery |
| `PORTAL_BACKUP_PASSPHRASE` | `portalctl backup` / `restore` | every existing backup is unreadable |
| `PORTAL_SMTP_PASSWORD` | outbound mail | recovery and invitation mail stops |

`ExpectedMigrationVersion` is 6. `/readyz` reports 503 until migrations run,
so a deployment must migrate before its health check can pass.

### Quality gates

Seven, all required in CI:

```bash
make build
make test
make lint              # includes check-no-secrets and check-version-conflicts
make migration-updown
make sqlc-diff
make openapi-diff
make smoke             # starts the real binaries, outside the source tree
```

Only `make smoke` exercises what deploys. The others verify libraries and
generated artifacts.

## Deferred to Phase 3 (`bcars-portal-6q6`)

Delivery prep and production hardening. None of these gate Phase 1 or Phase 2,
and deployability was never intended to be part of Phase 1.

| Bead | Scope |
| --- | --- |
| `fmc.8` | Dockerfile, systemd unit, environment-variable support |
| `fmc.16` | Audit API `reason_code`, `actor_role_codes`, outcome filter |
| `fmc.20` | Rate limiting on password-recovery requests |
| `fmc.21` | Admin UI recovery records no client address |
| `fmc.22` | No diagnostic when Secure cookies meet a plaintext base URL |

New delivery-prep findings land under `bcars-portal-6q6` by default so they can
float. The exception is anything cheaper to bake in early than to retrofit —
the password pepper and the middleware/capability ordering were both pulled
into Phase 1 for exactly that reason.

## Completed work

The tables below are the original delivery waves. A tick means the PR merged,
NOT that the bead's acceptance criteria were met — the 2026-08-08 review found
several of these incomplete, and the reconciliation above lists the corrections.
Beads specifically revisited: `sv7`, `02f`, `g34`, `8hm`, `ypg`, `909`, `34s`,
`8kz`, `8kf`, `k4f`, `hvv`, `bzy`.

### Wave 0 — foundation (PRs #1–#8)

| Bead | PR | Scope | Status |
| --- | --- | --- | --- |
| `bcars-portal-05w` | #8 | CI pipeline hardening, green required-check baseline | ✅ |
| `bcars-portal-sbd` | #7 | Standalone clone validation, Beads hydration, tools | ✅ |

### Wave 1 — domain/API and operations (PRs #9–#24)

| Bead | PR | Scope | Status |
| --- | --- | --- | --- |
| `bcars-portal-7qe` | #9 | Import domain ADR-0008 compliance, state machine | ✅ |
| `bcars-portal-a9h` | #10 | Health/readiness endpoints | ✅ |
| `bcars-portal-909` | #11 | Session sign-in/sign-out/current API | ✅ |
| `bcars-portal-sx1` | #12 | Person CRUD and member-list API | ✅ |
| `bcars-portal-sv7` | #13 | Capability, role, user, role-grant APIs | ✅ |
| `bcars-portal-e3q` | #14 | Membership lifecycle API | ✅ |
| `bcars-portal-8hm` | #15 | Wire all import API handlers | ✅ |
| `bcars-portal-ypg` | #16 | Import UI POST routes (upload/commit/discard) | ✅ |
| `bcars-portal-81o` | #19 | Notes list/create/update API | ✅ |
| `bcars-portal-d00` | #20 | Contact methods and sharing-preference APIs | ✅ |
| `bcars-portal-02f` | #21 | Audit-event query API | ✅ |
| `bcars-portal-g34` | #22 | Authorized member export (CSV/JSON) | ✅ |
| `bcars-portal-cme` | #23 | FCC verification record APIs | ✅ |
| `bcars-portal-8kf` | #23 | Honorary grant domain/APIs | ✅ |
| `bcars-portal-exo` | #24 | Member timeline and search | ✅ |

### Wave 2 — adapters and operations (PRs #25–#28)

| Bead | PR | Scope | Status |
| --- | --- | --- | --- |
| `bcars-portal-149` | #25 | Error templates for admin UI | ✅ |
| `bcars-portal-34s` | #26 | Recovery/invitation API handlers | ✅ |
| `bcars-portal-k4f` | #27 | portalctl backup + restore | ✅ |
| `bcars-portal-xvk` | #28 | Log redaction and retention docs | ✅ |

### Wave 3 — UI and handoff (PRs #29–#31)

| Bead | PR | Scope | Status |
| --- | --- | --- | --- |
| `bcars-portal-hvv` | #29 | Recovery/invitation UI pages | ✅ |
| `bcars-portal-52g` | #30 | Remaining member UI pages | ✅ |
| `bcars-portal-bzy` | #30 | Deployment package and docs | ✅ |
| `bcars-portal-8kz` | #31 | Officer handoff checklist | ✅ |
| `bcars-portal-h6h` | #31 | Fixture validation tests | ✅ |

### Additional PRs (import improvements)

| PR | Scope |
| --- | --- |
| #17 | Manual row decisions + flash messages for import UI |
| #18 | Import notes with sentence-level deduplication |

### Meta tasks

| Bead | Status |
| --- | --- |
| `bcars-portal-9sr` | ✅ Closed — backlog reconciliation complete |
| `bcars-portal-5eg` | ✅ Closed — member operations gate |
| `bcars-portal-dz0` | ✅ Closed — admin MVP gate |

## Deferred interactive/external work (Phase 2+)

These beads are deliberately excluded from autonomous MVP execution. Agents
must not start them until the repository owner participates and supplies any
required access out of band.

| Bead | Why deferred |
| --- | --- |
| `bcars-portal-go6` | Explore Groups.io APIs, permissions, rate limits, and live synchronization |
| `bcars-portal-66i` | Select and explore an authoritative FCC data source |
| `bcars-portal-8ou` | Validate production SMTP with temporary owner-provided credentials |
| `bcars-portal-eet` | Deploy to an owner-selected production host |
| `bcars-portal-g21` | Run the real export through a human-supervised reconciliation and import |
| `bcars-portal-6pz` | Collaborative UI design and visual-polish session |

### Planned next (Phase 2)

The focused treasurer workflow is specified in
[phase-2-design.md](phase-2-design.md) and sequenced in
[phase-2-plan.md](phase-2-plan.md). Beads is the live task graph.

- **Treasurer/payment workflow**: `dues_rates`, `coverage_events`, `payments`,
  `payment_batches` tables; payment recording; coverage period calculation;
  membership status derived from coverage (replacing hardcoded "approved")
- **Membership renewal**: expiration tracking, renewal reminders, lapsed status
- **Treasurer dashboard**: payment history, dues standing, batch operations
- **Legacy backfill migration**: convert `legacy_current_until` → `coverage_events`

## Definition of completion for every implementation bead

The bead-specific acceptance criteria must be satisfied. Before opening a PR,
the agent runs:

```bash
make build
make test
make lint
make migration-updown
make sqlc-diff
make openapi-diff
make smoke
```

`make smoke` is the gate that starts the real binaries. The others verify
libraries and generated artifacts; only this one exercises what deploys.

The agent reviews the diff and secret/PII scan, commits on a scoped branch,
pushes, opens a PR, waits for every required CI job, fixes any failure, and
squash-merges only when CI is green. The bead is closed and its Dolt state is
pushed after the merge, not before.
