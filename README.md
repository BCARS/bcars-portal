# BCARS Members Portal

Private membership portal for the Butler County Amateur Radio Society.

**This repository contains no real member data.** All fixtures under
`fixtures/synthetic/` are fabricated for testing. Real Groups.io exports,
databases, uploaded files, extracted document text, API credentials, backups,
and AI transcripts are never committed. See `.gitignore` and
`scripts/check-no-secrets.sh`.

## Status

Phase 1 (Administrative Membership MVP) is complete. Beads is the source of
truth for deferred and remaining work; `docs/phase-1-progress.md` is the
human-readable completion record. See:

- `PLANNING.md` — product plan and decisions.
- `docs/phase-1-design.md` — technical design.
- `docs/phase-1-plan.md` — task-level implementation plan.
- `docs/adr/` — architecture decision records.

## Prerequisites

- Go 1.26.0 (auto-downloaded via the `toolchain` directive if you have any
  recent Go installed and `GOTOOLCHAIN=auto`, which is the default).
- `make`, `git`.
- `bd` (Beads) for development task hydration and coordination.

Optional developer tools:

- `sqlc` (installed on demand via `make sqlc`).
- `staticcheck`, `golangci-lint` (installed on demand via `make lint`).

## Getting started

```sh
git clone git@github.com:BCARS/bcars-portal.git
cd bcars-portal
bd bootstrap --yes          # hydrate the Beads database from refs/dolt/data
bd prime
bd ready
make install-hooks          # ONE-TIME: blocks direct pushes to main
make build
make test
make lint
make migration-updown       # verify migration round-trip
make sqlc-diff
make openapi-diff
make smoke                  # exercise the shipped binaries outside the repo
./bin/portal --version
./bin/portalctl --help
```

After bootstrap, run `bd dolt pull` before selecting later work so the local
task database includes claims, closures, and newly discovered dependencies from
other agents.

The repository is standalone: no parent or sibling checkout is required. Real
Groups.io exports are copied separately into the ignored `data/` directory when
the owner is ready to run the supervised import. Never commit real exports,
SQLite databases, uploaded files, backups, extracted text, credentials, or logs
containing member data.

## Development workflow

`main` is protected by a local pre-push hook (installed by `make
install-hooks`). All work goes through a pull request:

```sh
git switch -c codex/<bead-id>-short-topic
# ... edits ...
make build && make test && make lint && make migration-updown && make sqlc-diff && make openapi-diff && make smoke
git push -u origin HEAD
gh pr create --fill
gh run watch                 # wait for CI green
gh pr merge --squash --delete-branch
git switch main && git pull --ff-only
```

Repository agents may complete this workflow autonomously for a claimed Beads
task: create a branch, commit, push, open a PR, wait for every required CI check,
fix failures, and squash-merge once CI is green. The task is closed and its Beads
state pushed only after the merge. External or interactive tasks remain deferred
until the owner explicitly supplies access and participates.

Emergency bypass (initial repo push, hotfix): `PORTAL_ALLOW_PUSH_MAIN=1 git
push`. Every use of the bypass should be justifiable in the commit message.

## Running the server

For local development, `make run` builds and starts the portal on
`http://localhost:8080`, migrates `bcars.db`, allows an empty password pepper,
and issues non-Secure cookies. Those relaxations are intentionally local-only;
see `docs/deployment.md` before configuring a production process.

## Bootstrapping the first administrator

A fresh install requires a one-time bootstrap:

```sh
./bin/portalctl bootstrap-admin --email you@bcars.org --db bcars.db
```

There is no default password. The command prints a single-use invitation URL
to be opened in a browser.

## Backups

Backups are owned by the appointed webmaster. See the tested procedure in
`docs/runbooks/backup-restore.md`.

## License

TBD — decision recorded in `docs/adr/0000-license.md`.
