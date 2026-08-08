# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:6cd5cc61 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

This protocol applies when ending a Beads implementation workflow. It is subordinate to explicit user, repository, and orchestrator instructions.

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile**:
   ```bash
   # Conservative/minimal/default: report status and proposed commands; wait for approval.
   git status

   # Team-maintainer opt-in only, unless current instructions forbid it:
   git pull --rebase
   git push
   git status
   ```
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**
- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.
<!-- END BEADS INTEGRATION -->


## Build & Test

```bash
make build        # Build ./cmd/portal and ./cmd/portalctl binaries
make test         # go test -race -count=1 ./...
make lint         # fmt + vet + staticcheck + golangci-lint + secrets scan
make sqlc         # Regenerate SQL query code from sqlc.yaml
make install-hooks  # Install pre-push git hook (blocks direct pushes to main)
```

Run a single test:
```bash
go test -run TestFoo ./internal/...
```

Requires: Go 1.26.0, `make`, `git`. Optional lint tools: `staticcheck`, `golangci-lint`.

## Architecture Overview

API-first Go application targeting SQLite + Goose migrations + sqlc for type-safe queries. Huma v2 for the REST API, server-rendered HTMX admin UI.

### Internal layout

- `cmd/portal` — HTTP server entry point
- `cmd/portalctl` — Admin CLI (bootstrap-admin, backup/restore)
- `internal/authn/` — Password hashing (Argon2id), session store, email link service
- `internal/domain/authz/` — Capability catalog, policy layer (default-deny)
- `internal/domain/members/` — Member operations domain service
- `internal/domain/importd/` — Groups.io import pipeline (parse, normalize, match, stage, commit)
- `internal/httpapi/` — Huma API handlers (registered but mostly stubbed 501s — see phase-1-progress.md)
- `internal/web/` — Server-rendered admin UI (HTMX, templates)
- `internal/db/` — Database layer (Goose migrations + sqlc-generated queries)
- `internal/mail/` — Mail sender interface (filelog + SMTP implementations)
- `internal/obs/` — Observability (structured logging)
- `internal/version/` — Build metadata
- `docs/adr/` — Architecture Decision Records
- `docs/phase-1-plan.md` — Full task breakdown by workstream
- `docs/phase-1-progress.md` — Current implementation status
- `PLANNING.md` — Full product & technical design (~800 lines); read before starting new features

### Key design decisions (from ADRs)

- SQLite with WAL mode for Phase 1 (single-instance simplicity)
- Capability-based authorization (default-deny; not RBAC)
- Argon2id for password hashing
- Immutable preference-history pattern for audit trails
- Import staging model: validate CSV before committing to DB
- Sessions stored in SQLite (HttpOnly + SameSite cookies)

## Agent Workflow

When picking up work:
1. Run `bd ready` to find available tasks
2. Read `docs/phase-1-progress.md` for current status context
3. Claim a task with `bd update <id> --claim`
4. Implement, test (`make test`), lint (`make lint`)
5. Close with `bd close <id>`

### Key patterns

- **API handlers** in `internal/httpapi/ops_*.go` — most return `ErrNotImplemented()` and need wiring to domain services
- **Domain services** in `internal/domain/` — these are the real implementations, well-tested
- **Web handlers** in `internal/web/handler.go` — admin UI, partially wired
- All API operations must be registered via `httpapi.Register` (not raw `huma.Register`) to enforce capability checks
