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

## Repository Execution Profile

This repository explicitly opts into the **team-maintainer** workflow for a
claimed Beads task. An agent may create a feature branch, commit scoped changes,
push the branch, open a pull request, wait for all required CI checks, fix CI
failures, and squash-merge the PR into `main` once every required check passes.
Delete the merged branch. A current user instruction not to commit, push, or
merge still overrides this profile.

The portal must work from a standalone clone. Do not depend on files, data, or
source code in a parent directory or sibling repository. Checked-in synthetic
fixtures are the only member-like data allowed in tests. Real exports,
databases, credentials, uploads, backups, and logs with member data are supplied
out of band, remain under ignored paths such as `data/`, and are never committed.

For Phase 2 treasury work, read `docs/phase-2-design.md` and
`docs/phase-2-plan.md` before claiming a story. The ignored `scratch/` mockups
are not a dependency or acceptance authority.


## Build & Test

```bash
make build        # Build ./cmd/portal and ./cmd/portalctl binaries
make test         # go test -race -count=1 ./...
make lint         # fmt + vet + staticcheck + golangci-lint + secrets scan
make migration-updown  # migration up/down/up round-trip
make sqlc         # Regenerate SQL query code from sqlc.yaml
make sqlc-diff    # Regenerate and fail on committed sqlc drift
make openapi-diff # Regenerate and fail on OpenAPI/catalog drift
make smoke        # Exercise the shipped binaries outside the source tree
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
- `internal/httpapi/` — Huma API handlers and capability-enforced registration
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
1. On a fresh clone run `bd bootstrap --yes`; otherwise run `bd dolt pull`.
2. Run `bd prime`, `bd ready`, and `bd show <id>`; choose only ready work.
3. Claim with `bd update <id> --claim`, then publish with `bd dolt push`.
4. Create `codex/<bead-id>-short-topic`; keep one bead per PR.
5. Implement only the bead acceptance criteria. Create follow-up beads for
   discovered work. Do not start deferred external/interactive tasks without
   the repository owner.
6. Run all seven repository gates before opening the PR: `make build`, `make
   test`, `make lint`, `make migration-updown`, `make sqlc-diff`, `make
   openapi-diff`, and `make smoke`. Generic `bd preflight` output does not
   replace these repository-specific gates.
7. Review the diff and secret/PII scan, commit, push, and open a PR.
8. Wait for every required CI check, fix failures, and never merge red CI.
9. Squash-merge, delete the branch, update local `main`, close the bead with the
   PR in the reason, then run `bd dolt pull` and `bd dolt push`.

Functional MVP UI work has latitude to choose reasonable accessible layouts and
copy. Full visual design and polish are intentionally deferred to the dedicated
interactive UI-design bead.

### Key patterns

- **API handlers** in `internal/httpapi/ops_*.go` — production dependencies are
  wired; remaining `ErrNotImplemented()` returns are nil-dependency guards
- **Domain services** in `internal/domain/` — these are the real implementations, well-tested
- **Web handlers** in `internal/web/handler.go` — server-rendered administrative UI
- All API operations must be registered via `httpapi.Register` (not raw `huma.Register`) to enforce capability checks
