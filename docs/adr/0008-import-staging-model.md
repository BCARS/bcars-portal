# ADR-0008: Import staging model

- Status: Accepted
- Date: 2026-07-26

## Context

The Groups.io export is the primary initial data source. It contains
sentinel dates, free-text membership types, and rows that require officer
judgement (Honorary type, lifetime dates, ambiguous email matches). We
cannot let raw import writes reach the canonical member tables.

## Decision

Every import passes through three tables:

- `import_runs` — metadata, source hash, status, idempotency key.
- `staged_import_rows` — one row per source row: raw JSON, normalized JSON,
  proposed action, match candidate, validation errors, `requires_manual`
  flag with a `manual_reason` code.
- `reconciliation_decisions` — append-only officer decisions per staged row.

Commit is a single SQL transaction that refuses to run if any row still
needs a manual decision. It writes canonical rows, plants the default
sharing-preference events from ADR-0006, emits per-row and summary audit
events, and stores its result under an idempotency key so retries return
the original response.

State machine:

```
uploaded -> validated -> previewed -> committed
                       \_ discarded
                       \_ failed
```

## Rejected alternatives

- **Direct upsert on canonical tables with a diff report**: hides mistakes;
  cannot roll back a bad interpretation of a sentinel date.
- **Store only staged rows, no committed audit trail**: loses the
  provenance of every canonical value.

## Consequences

- Slower than a naive upsert, but the whole point of Phase 1 is to
  establish canonical data with judgement, not speed.
- Retained staged rows are private data; purge is an explicit webmaster
  action, audited.
