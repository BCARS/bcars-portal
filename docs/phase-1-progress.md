# Phase 1 Progress and Execution Map

Last reconciled: 2026-08-08

This is the human-readable map of completed and remaining Phase 1 work. Beads
is the durable source of truth: run `bd bootstrap --yes` on a fresh clone, then
`bd ready` and `bd show <id>`. Every implementation bead contains its own scope,
acceptance criteria, dependencies, and required PR/CI completion gate.

## Target outcome

An authorized officer can sign in, manage canonical member records, stage and
reconcile a Groups.io CSV/JSON export, commit it atomically, inspect audit
history, and operate a documented single-instance deployment with tested
backup/restore. Phase 1 is local and API-first. It does not require live
Groups.io access, automated FCC lookup, production hosting credentials, or a
finished visual design system.

## Completed foundation

| Area | Implemented evidence |
| --- | --- |
| Repository and safety | Go module, Makefile, secret/PII guard, ignored private-data paths, pre-push protection |
| Architecture | Product plan, Phase 1 design/plan, ADRs 0000–0009 |
| API contracts | Huma router, operation metadata guard, errors, pagination, idempotency/If-Match helpers, generated OpenAPI and capability catalog |
| Database | SQLite WAL/foreign keys, Goose up/down migrations, sqlc queries, capability/role seeds, optimistic concurrency tests |
| Authentication domain | Argon2id+pepper passwords, SQLite sessions, email-link service, filelog/SMTP adapters, policy layer, bootstrap-admin |
| Import foundation | Synthetic CSV/JSON parsers, normalization, matching, staging schema, initial upload/commit/discard service |
| Member domain | Person/member operations, contacts, memberships, notes, manual FCC records, honorary grant basics, auditing |
| Base admin UI | Login/logout, dashboard, member search/detail/edit, basic membership/contact/note actions, import list/detail GET pages |
| Test baseline | Package tests cover authentication, database, policy, imports, members, HTTP metadata/contracts, mail, logging, versioning, and web handlers |

The synthetic fixtures are implemented and tested; `bcars-portal-h6h` remains
open only to revalidate and close after the CI baseline is green. The CI
workflow exists, but `bcars-portal-05w` remains open to pin tools, reconcile all
gates, and prove a green PR baseline before autonomous implementation begins.

## Remaining local MVP work

### Wave 0 — make autonomous work reliable

| Bead | Scope |
| --- | --- |
| `bcars-portal-05w` | Harden and verify the CI pipeline; establish a green required-check baseline |
| `bcars-portal-sbd` | Validate a fresh standalone clone, Beads hydration, tools, tests, and absence of sibling dependencies |

All implementation work is blocked by the standalone-clone gate, which itself
depends on the CI baseline.

### Wave 1 — independent domain/API and operations slices

| Bead | Scope |
| --- | --- |
| `bcars-portal-7qe` | Complete reconciliation, preview, state machine, atomic commit, and idempotency required by ADR-0008 |
| `bcars-portal-909` | Session sign-in/sign-out/current API |
| `bcars-portal-sx1` | Person CRUD and member-list API |
| `bcars-portal-d00` | Contact methods and sharing-preference domain/API |
| `bcars-portal-e3q` | Membership lifecycle API |
| `bcars-portal-cme` | Manual/offline FCC verification-record API |
| `bcars-portal-8kf` | Honorary-grant domain/API completion |
| `bcars-portal-81o` | Notes API |
| `bcars-portal-exo` | Member timeline and expanded search |
| `bcars-portal-g34` | Authorized local member export |
| `bcars-portal-sv7` | Capability, role, user, and role-grant API |
| `bcars-portal-02f` | Audit-event query API |
| `bcars-portal-149` | Functional accessible HTML/HTMX error rendering |
| `bcars-portal-a9h` | Real database/schema readiness check |
| `bcars-portal-k4f` | Encrypted backup/restore CLI and runbook |
| `bcars-portal-xvk` | Log redaction and retention guidance |

The member-operation completion gate is `bcars-portal-5eg`; it becomes ready
only after all eight scoped member child tasks merge.

### Wave 2 — adapters that depend on Wave 1

| Bead | Dependency | Scope |
| --- | --- | --- |
| `bcars-portal-8hm` | `7qe` | Wire all eight import API operations |
| `bcars-portal-34s` | `909` | Recovery and invitation API |
| `bcars-portal-bzy` | `a9h` | Reproducible local deployment package and runbook |

### Wave 3 — functional MVP UI and handoff

| Bead | Dependency | Scope |
| --- | --- | --- |
| `bcars-portal-ypg` | `8hm` | Import upload/commit/discard UI routes |
| `bcars-portal-hvv` | `34s` | Recovery/invitation UI |
| `bcars-portal-52g` | `5eg` | Remaining member workflows in the base admin UI |
| `bcars-portal-8kz` | Core UI and operations | Non-developer officer handoff guide |

`bcars-portal-dz0` is the Phase 1 completion gate. It closes only after every
non-deferred local MVP dependency has merged and main is green.

## Deferred interactive/external work

These beads are deliberately excluded from autonomous MVP execution. Agents
must not start them until the repository owner participates and supplies any
required access out of band.

| Bead | Why deferred |
| --- | --- |
| `bcars-portal-go6` | Explore Groups.io APIs, permissions, rate limits, and live synchronization after the file-import MVP |
| `bcars-portal-66i` | Select and explore an authoritative FCC data source; manual officer verification remains the MVP path |
| `bcars-portal-8ou` | Validate production SMTP with temporary owner-provided credentials |
| `bcars-portal-eet` | Deploy to an owner-selected production host |
| `bcars-portal-g21` | Run the real export through a human-supervised reconciliation and import |
| `bcars-portal-6pz` | Collaborative UI design and visual-polish session after functional workflows exist |

Real exports, databases, credentials, backups, uploaded content, and logs with
member data remain outside Git. The real import uses an ignored local `data/`
directory and is never converted into a committed fixture.

## Definition of completion for every implementation bead

The bead-specific acceptance criteria must be satisfied. Before opening a PR,
the agent runs:

```bash
make build
make test
make lint
make sqlc-diff
make openapi-diff
```

The agent reviews the diff and secret/PII scan, commits on a scoped branch,
pushes, opens a PR, waits for every required CI job, fixes any failure, and
squash-merges only when CI is green. The bead is closed and its Dolt state is
pushed after the merge, not before.
