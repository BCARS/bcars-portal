# ADR-0001: Use SQLite for Phase 1

- Status: Accepted
- Date: 2026-07-26

## Context

Phase 1 runs a single persistent application instance for a small club
(currently ~62 canonical members). Operations are almost entirely
officer-driven and low-frequency. The webmaster owns backups.

## Decision

Use SQLite via `modernc.org/sqlite` (pure Go — no cgo build friction). Run
in WAL mode with foreign keys enforced. Persist the database on the host's
durable storage.

## Rejected alternatives

- **PostgreSQL now**: adds a separate process to operate for no functional
  gain at this scale.
- **CGo SQLite (`mattn/go-sqlite3`)**: forces a C toolchain on every
  developer and CI runner for no benefit here.

## Consequences

- Backups use `sqlite3 .backup` (WS8.2), which is WAL-safe.
- Migrations use Goose SQL files. See ADR-0008 for the import staging model.
- Revisit if hosting requires multiple instances, if we need pg-only features,
  or if concurrent write load rises materially. The `internal/db` package
  hides the driver behind a small interface to make this change less painful,
  but a real move would still require rewriting SQL.
