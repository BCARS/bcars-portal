# Phase 1 Progress

Last updated: 2026-08-08

This document tracks implementation status against the [phase-1-plan.md](phase-1-plan.md)
workstreams. It is meant to be read by agents picking up work and by humans
checking overall progress.

## Status Legend

- **Done** — implemented, tested, merged
- **Domain done / API stubbed** — domain service works, HTTP handler returns 501
- **Partial** — some pieces landed, gaps noted
- **Not started** — no implementation exists

---

## WS1 — Repository & Engineering Foundation

| Task | Status | Notes |
|------|--------|-------|
| WS1.1 Repo skeleton | Done | Makefile, binaries, layout all in place |
| WS1.2 PII/secret ignore | Done | .gitignore, check-no-secrets.sh wired to `make lint` |
| WS1.3 Toolchain pinning | Done | Go 1.26, Huma v2, modernc/sqlite, goose, sqlc |
| WS1.4 Structured logging | Done | internal/obs with slog, request-id middleware |
| WS1.5 ADRs | Done | docs/adr/0001-0008 |
| WS1.6 CI pipeline | **Not started** | `bcars-portal-05w` |
| WS1.7 Synthetic fixtures | **Not started** | `bcars-portal-h6h` |

**Gate G1 (Foundation ready):** Mostly done. CI and fixtures remain.

---

## WS2 — API & Application Contracts

| Task | Status | Notes |
|------|--------|-------|
| WS2.1 Huma app scaffold | Done | NewRouter(), /healthz, /api/v1/openapi.json all working |
| WS2.2 Cross-cutting conventions | Done | Pagination, cursor codec, ETag/If-Match, idempotency plumbing, error codes |
| WS2.3 Capability catalog | Done | 22 capabilities, startup guard, docs/capability-catalog.json |
| WS2.4 OpenAPI baseline | Done | 4700+ line generated doc, --dump-openapi flag |

**Gate G2 prerequisite satisfied.**

---

## WS3 — Database & Migration Baseline

| Task | Status | Notes |
|------|--------|-------|
| WS3.1 Initial schema migrations | Done | 0001_init.sql, 0002_imports.sql, up/down tested |
| WS3.2 Capability & role seed | Done | 0003, 0004 seed migrations |
| WS3.3 sqlc query surface | Done | Queries for WS4/WS6 generated |
| WS3.4 Optimistic concurrency | Done | version-based updates, ErrStale |

**Gate G2 (Data ready): Done.**

---

## WS4 — Officer Authentication & Authorization

| Task | Status | Notes |
|------|--------|-------|
| WS4.1 Password hashing | Done | Argon2id, pepper, constant-time |
| WS4.2 Session store + middleware | Done | SQLite-backed, rotate/revoke/expire |
| WS4.3 Sign-in/sign-out API | Domain done / **API stubbed** | Web UI works; API returns 501. `bcars-portal-909` |
| WS4.4 Mail sender | Done | filelog + SMTP implementations |
| WS4.5 Recovery & invitation | Domain done / **API stubbed** | EmailLinkService complete; API returns 501. `bcars-portal-34s` |
| WS4.6 Policy layer | Done | Default-deny, capability-based |
| WS4.7 bootstrap-admin | Done | portalctl command, creates invitation |

**Gate G3 (Auth ready):** Domain layer done. API handlers need wiring for programmatic access.

---

## WS5 — Staged Groups.io Import

| Task | Status | Notes |
|------|--------|-------|
| WS5.1 Parsers | Done | JSON + CSV parsers with fixture tests |
| WS5.2 Normalization | Done | Call sign, email, phone, dates, sentinels |
| WS5.3 Match engine | Done | external_id > call_sign > email > manual |
| WS5.4 Staging write path | Domain done / **API stubbed** | `bcars-portal-8hm` |
| WS5.5 Reconciliation & preview | Domain done / **API stubbed** | `bcars-portal-8hm` |
| WS5.6 Commit | Domain done / **API stubbed** | `bcars-portal-8hm` |
| WS5.7 Discard + list + inspect | Domain done / **API stubbed** | `bcars-portal-8hm` |

**Gate G4 (Import ready):** Domain pipeline complete and tested. Not usable end-to-end — API handlers and UI POST routes still need wiring.

---

## WS6 — Administrative Member Operations

| Task | Status | Notes |
|------|--------|-------|
| WS6.1 Person + membership CRUD | Domain done / **API stubbed** | Web UI works. `bcars-portal-sx1` |
| WS6.2 Contact methods | Domain done (no Update) / **API stubbed** | Web UI: create only. `bcars-portal-sx1`, `bcars-portal-5eg` |
| WS6.3 Sharing preferences | **Partial** — write-only domain, no reads | `bcars-portal-5eg` |
| WS6.4 Membership lifecycle | Domain done / **API stubbed** | Web UI: approve only. `bcars-portal-sx1` |
| WS6.5 FCC verification | Domain done / **API stubbed** | `bcars-portal-5eg` |
| WS6.6 Honorary grants | **Partial** — create+revoke only | No Update/Expire. `bcars-portal-5eg` |
| WS6.7 Notes | Domain done / **API stubbed** | Web UI: create only. `bcars-portal-sx1` |
| WS6.8 Search + timeline | **Partial** — name search only, no timeline | `bcars-portal-sx1`, `bcars-portal-5eg` |
| WS6.9 Export | **Not started** | No domain service. `bcars-portal-5eg` |

**Gate G5 (Officer ops ready):** Domain services cover ~70% of the surface. API layer is 0% wired.

---

## WS7 — Administrative UI

| Task | Status | Notes |
|------|--------|-------|
| WS7.1 Layout + sign-in/out | Done | Full CSS system, HTMX, cookie auth |
| WS7.1 Recovery/invitation pages | **Not started** | `bcars-portal-hvv` |
| WS7.2 Dashboard | Done | Live stats, recent audit, quick actions |
| WS7.3 Member search + detail | Done | HTMX search, create/edit/deactivate/approve/notes |
| WS7.3 Remaining member UI | **Partial** | Missing: sharing prefs, FCC, honorary, timeline, contact edit. `bcars-portal-52g` |
| WS7.4 Import review UI | **Partial** | GET pages work; POST upload/commit/discard missing. `bcars-portal-ypg` |
| WS7.5 Error templates | **Not started** | All errors are plain-text http.Error(). `bcars-portal-149` |

**Gate G6 (UI ready):** Core pages functional. Import actions, error pages, and secondary features remain.

---

## WS8 — Operations & Recovery

| Task | Status | Notes |
|------|--------|-------|
| WS8.1 Health/readiness | **Partial** | /healthz works; /readyz is stub (always 200). `bcars-portal-a9h` |
| WS8.2 Backup + restore | **Not started** | portalctl prints "not yet implemented". `bcars-portal-k4f` |
| WS8.3 Log retention & redaction | **Not started** | `bcars-portal-xvk` |
| WS8.4 Deployment package | **Not started** | `bcars-portal-bzy` |
| WS8.5 Officer handoff checklist | **Not started** | `bcars-portal-8kz` |

**Gate G7 (Ops ready): Not started.**

---

## Recommended Work Order for Agents

The following order respects dependencies and maximizes unblocking:

### Wave 1 — API wiring (no new domain code needed)
These can run in parallel since they touch different handler files:
1. `bcars-portal-909` — Wire session API (WS4.3)
2. `bcars-portal-8hm` — Wire import API (WS5.4-5.7)
3. `bcars-portal-sx1` — Wire member ops API (WS6.1-6.4, 6.7-6.8)

### Wave 2 — Dependent API + UI wiring
4. `bcars-portal-34s` — Wire recovery/invitation API (blocked by 909)
5. `bcars-portal-ypg` — Wire import UI POST routes (blocked by 8hm)

### Wave 3 — Domain gaps + remaining APIs
6. `bcars-portal-5eg` — Missing domain methods + remaining WS6 APIs

### Wave 4 — UI completion
7. `bcars-portal-hvv` — Recovery/invitation UI pages
8. `bcars-portal-52g` — Remaining member UI pages (blocked by 5eg)
9. `bcars-portal-149` — Error templates

### Wave 5 — Operations
10. `bcars-portal-a9h` — Health/readiness (WS8.1)
11. `bcars-portal-k4f` — Backup + restore (WS8.2)
12. `bcars-portal-xvk` — Log redaction (WS8.3)
13. `bcars-portal-bzy` — Deployment package (blocked by a9h)
14. `bcars-portal-8kz` — Handoff checklist (blocked by k4f)

### Wave 6 — Foundation gaps
15. `bcars-portal-05w` — CI pipeline (WS1.6)
16. `bcars-portal-h6h` — Synthetic fixtures (WS1.7)

---

## Open Issues Summary

Run `bd ready` for current unblocked work, or `bd list --status=open` for everything.
