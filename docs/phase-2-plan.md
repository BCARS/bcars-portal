# Phase 2 Execution Plan

This is the human-readable execution map for the API-first treasurer workflow.
Beads is the durable task and dependency source of truth. Read
[phase-2-design.md](phase-2-design.md) before claiming a Phase 2 task.

## Target outcome

A treasurer can record and reconcile dues without losing the distinction between
money received and coverage granted. Draft batches are safe to edit, posting is
atomic, corrections preserve history, non-treasurers cannot retrieve payment
details, and the thin MVP UI supports the selected batch-entry and paper worksheet
workflows.

## Workstreams

### WS1 - Ledger foundation

One migration establishes dues rates, draft batches and entries, immutable
payments/corrections, append-only coverage events, the ADR-0007 legacy backfill,
and the Phase 2 capability matrix. This is the only schema bottleneck.

### WS2 - API and domain operations

After WS1, two lanes can begin:

- standing, rates, suggestions, and independent coverage adjustments;
- open-batch draft CRUD, persisted defaults, totals, and abandonment.

Atomic posting follows the batch contract. Single payment calls the same posting
primitive. Corrections follow posting. Treasury queries/CSV and worksheet snapshots
can proceed after their required read models exist.

### WS3 - Thin treasurer UI

The UI starts only after each backing API contract is merged:

- treasurer landing/status lists and single-payment form;
- spreadsheet-style batch entry, posting, posted review, and correction;
- renewal worksheet options and print stylesheet.

The MVP follows the checked-in behavioral constraints but retains latitude on
minor layout and copy. Full interactive visual polish remains deferred.

### WS4 - Assembly proof and completion audit

The smoke gate expands to exercise the shipped treasury surface. A final acceptance
audit re-runs the Phase 2 outcome against merged `main`; closed beads alone are not
completion evidence.

## Dependency map

```text
ledger schema/capabilities/backfill
├── standing + rates + suggestions + coverage API
├── import coverage + lifetime honorary cutover
└── draft batch API
    └── atomic posting + single-payment API
        ├── correction API
        └── treasury history + CSV API

standing API + posting API ──> worksheet snapshot API
standing/posting APIs ───────> status + single-payment UI
draft/post/correction APIs ──> batch + correction UI
worksheet API ───────────────> printable worksheet UI
all domain/API/UI work ──────> production smoke + completion audit
```

## Planned beads

The Phase 2 epic owns these independently mergeable stories. IDs below abbreviate
the `bcars-portal-` prefix; live dependencies are in Beads. Claim a ready child,
never the epic itself.

| Bead | Story | Scope | Depends on |
| --- | --- | --- | --- |
| `pma.1` | Ledger schema, backfill, capabilities | Tables, constraints, ADR-0007 conversion, role seeds, sqlc baseline | none |
| `pma.2` | Dues standing, rates, suggestions, coverage API | Safe summary/read models and independent append-only adjustments | `pma.1` |
| `pma.3` | Draft payment-batch API | Open/save/abandon, defaults, entry CRUD, ETags, totals, idempotency | `pma.1` |
| `pma.4` | Atomic posting and single-payment API | All-or-nothing post and one-row convenience operation using one primitive | `pma.2`, `pma.3` |
| `pma.5` | Posted-payment correction API | Reversal/replacement, reason, correction chain, conditional coverage adjustment | `pma.4` |
| `pma.6` | Treasury history and CSV exports | Payment/batch queries, receipts, activity, formula-safe exports | `pma.4`, `pma.5` |
| `pma.7` | Renewal worksheet snapshot API | Durable as-of/filter/order runs and batch-from-sheet linkage | `pma.2`, `pma.4` |
| `pma.8` | Treasurer status and single-payment UI | Safe status lists, suggestions, prior history, one-payment form | `pma.2`, `pma.4`, `pma.11` |
| `pma.9` | Batch and correction UI | Chosen grid, persisted defaults, reconciliation totals, post/review/fix flows | `pma.3`-`pma.6` |
| `pma.10` | Printable renewal worksheet UI | Options, stable ordering, letter print CSS, entered-since markers | `pma.7`, `pma.11` |
| `pma.11` | County identity reconciliation | Owner decision and consistent checked-in copy/fixtures | none; blocks UI acceptance |
| `pma.13` | Import dues/honorary cutover | Persist normalized dates and lifetime decisions after legacy columns disappear | `pma.1` |
| `pma.12` | Production smoke and acceptance audit | Real binaries, authorization boundary, post/correct flow, docs reconciliation | every other Phase 2 story |

## Parallelism and collision rules

- Do not split the first migration across agents.
- After the schema PR merges, standing and draft-batch lanes may run in parallel.
- API tasks that both regenerate `internal/db/sqlc` must rebase sequentially and
  rerun `make sqlc-diff`; a clean Git text merge is not evidence of valid output.
- UI tasks do not invent endpoints. Contract gaps return to the owning API bead.
- External/interactive work remains deferred and never blocks Phase 2.

## Acceptance protocol for every story

Each Bead contains story-specific, observable acceptance criteria. Every PR also:

1. works from a standalone clone with synthetic data only;
2. preserves generic capability enforcement and audit middleware invariants;
3. adds tests that fail when the claimed behavior is removed;
4. commits intentional generated sqlc/OpenAPI/catalog artifacts;
5. runs all seven repository gates;
6. uses a scoped branch and PR, waits for every required CI job, and merges only
   when CI is green;
7. closes and pushes the Bead only after the merge.

## Phase 2 completion criteria

Phase 2 is complete only when all implementation beads are merged and a final audit
demonstrates on assembled binaries that:

- a draft batch does not change member standing;
- posting changes every intended standing exactly once;
- a stale or invalid batch posts nothing;
- correcting `$400` to `$40` preserves the original and produces the correct net
  batch total without changing paid-through unless explicitly requested;
- a treasurer can retrieve details and export them;
- a non-treasurer with member access can read safe standing but receives 403 for
  payment details;
- worksheet order can seed the batch grid and produces a legible print view;
- no task or runtime path depends on `scratch/`, a parent checkout, real member
  data, or an external service.
