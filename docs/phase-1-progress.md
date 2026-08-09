# Phase 1 Progress and Execution Map

Last reconciled: 2026-08-08 (against observed behavior, not bead-closure state)

This is the human-readable map of Phase 1 work. Beads is the durable source of
truth: run `bd bootstrap --yes` on a fresh clone, then `bd ready` and
`bd show <id>`.

## Target outcome

An authorized officer can sign in, manage canonical member records, stage and
reconcile a Groups.io CSV/JSON export, commit it atomically, inspect audit
history, and operate a documented single-instance deployment with tested
backup/restore. Phase 1 is local and API-first.

## Status: 🔄 RECONCILIATION IN PROGRESS

This document previously read "✅ COMPLETE" on the strength of every bead being
closed. A review of `main` at `f743d08` found that closure did not mean the
acceptance criteria were met: all seven mechanical gates passed against a tree
where the production API could not resolve a signed-in principal at all, and a
fresh installation had no route to a working administrator.

**Bead closure is not evidence of working software.** The gates that were green
throughout could not see assembly defects, because every test built its own
router rather than starting the one that ships. `bcars-portal-fmc` tracks the
reconciliation; `make smoke` is the gate added to close that blind spot.

### Completion gates

| Gate | Bead | Status |
| --- | --- | --- |
| Phase 1 member operations | `bcars-portal-5eg` | Closed, pending re-evaluation |
| Phase 1 administrative MVP | `bcars-portal-dz0` | Closed, pending re-evaluation |
| Production-assembly smoke test | `bcars-portal-fmc.12` | ✅ `make smoke`, running in CI |

Re-evaluation of the first two gates is deliberately withheld until every
P0/P1 child of `bcars-portal-fmc` is closed.

### Reconciliation status (`bcars-portal-fmc`)

| Bead | Scope | Status |
| --- | --- | --- |
| `fmc.1` | Capability enforcement + generic audit (P0) | ✅ PR #32 |
| `fmc.2` | Production wiring: middleware + email links | ✅ PR #33 |
| `fmc.3` | bootstrap-admin produces a working administrator | ✅ PR #36 |
| `fmc.4` | Recovery/invitation flows (5 defects) | ✅ PR #37 |
| `fmc.5` | Honorary grant update/expire | ✅ PR #35 |
| `fmc.7` | Audit filters and cursor pagination | ✅ PR #34 |
| `fmc.10` | Renderer error naming | ✅ PR #37 |
| `fmc.12` | Production-assembly smoke test | ✅ this change |
| `fmc.6` | Backup encryption, manifest validation, runbook | ⬜ open (P1) |
| `fmc.9` | `hashIP` hashes a timestamp, not the client address | ⬜ open (P1) |
| `fmc.11` | Guard `seed-demo` in production builds | ⬜ open (P1) |
| `fmc.13` | Web session cookies lack a `Secure` flag | ⬜ open (P1) |
| `fmc.14` | Password pepper is nil in the production assembly | ⬜ open (P1) |
| `fmc.15` | `RevokeHonoraryGrant` ignores version conflicts | ⬜ open (P1) |
| `fmc.8` | Deployment packaging and environment variables | ⬜ open (P2) |
| `fmc.16` | Audit API omits `reason_code` / outcome filter | ⬜ open (P2) |

### Known gaps not yet bead-covered

- No API operation creates an invitation; only consumption is exposed. A fresh
  installation can bootstrap exactly one administrator and has no supported
  route to onboard a second officer. The `user.invite` capability exists in the
  catalog with no endpoint behind it.

## Completed work

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

### Not yet planned (Phase 2)

The following are described in ADR-0007 and the Phase 1 design doc (Section 10)
but have no beads yet:

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
