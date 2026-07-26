# ADR-0007: Coverage/payment split (Phase 2 shape recorded now)

- Status: Accepted
- Date: 2026-07-26

## Context

`PLANNING.md` separates money received (payments) from paid-through decisions
(coverage events) and forbids inventing a cash payment for lifetime honorary
grants. Payments and coverage are Phase 2 work, but Phase 1 imports the
legacy `Current Until` date and we do not want to invent a coverage_events
row before that table exists nor to lose the imported value.

## Decision

- Phase 1 lands `memberships.legacy_current_until DATE NULL` and a
  companion `legacy_current_until_note TEXT` populated by the importer.
- Phase 2's first migration:
  1. creates `dues_rates`, `coverage_events`, `payments`,
     `payment_batches`;
  2. inserts one `coverage_events` row per non-null
     `legacy_current_until`, with `reason='legacy_import'` and a link back
     to the originating import run;
  3. drops the `legacy_current_until*` columns.
- Officer UI and API in Phase 1 show the legacy field labelled clearly as
  "imported, pending Phase 2 backfill".
- Honorary grants ship in Phase 1 in their full shape so the two known
  lifetime records land as first-class grants, not as fabricated dates.

## Rejected alternatives

- **Keep imported paid-through only in `staged_import_rows`**: officers
  reviewing a member post-import cannot see when they last paid without
  loading the source import, which is a bad daily experience.
- **Ship `coverage_events` in Phase 1**: pulls Phase 2 scope forward for a
  single legacy value.

## Consequences

- Phase 1 code that reads `legacy_current_until` must never derive a "dues
  standing" from it — that's Phase 2's job.
- Phase 2's first migration is fully specified here.
