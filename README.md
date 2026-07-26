# BCARS Members Portal

Private membership portal for the Butler County Amateur Radio Society.

**This repository contains no real member data.** All fixtures under
`fixtures/synthetic/` are fabricated for testing. Real Groups.io exports,
databases, uploaded files, extracted document text, API credentials, backups,
and AI transcripts are never committed. See `.gitignore` and
`scripts/check-no-secrets.sh`.

## Status

Phase 1 (Administrative Membership MVP). See:

- `PLANNING.md` — product plan and decisions.
- `docs/phase-1-design.md` — technical design.
- `docs/phase-1-plan.md` — task-level implementation plan.
- `docs/adr/` — architecture decision records.

## Prerequisites

- Go 1.26.0 (auto-downloaded via the `toolchain` directive if you have any
  recent Go installed and `GOTOOLCHAIN=auto`, which is the default).
- `make`, `git`.

Optional developer tools:

- `sqlc` (installed on demand via `make sqlc`).
- `staticcheck`, `golangci-lint` (installed on demand via `make lint`).

## Getting started

```sh
cp .env.sample .env         # then edit; do NOT commit .env
make install-hooks          # ONE-TIME: installs pre-push guard against direct pushes to main
make build                  # builds cmd/portal and cmd/portalctl
make test                   # runs unit tests
make lint                   # gofmt + go vet + staticcheck + golangci-lint + secrets check
./bin/portal --version
./bin/portalctl --help
```

## Development workflow

`main` is protected by a local pre-push hook (installed by `make
install-hooks`). All work goes through a pull request:

```sh
git switch -c ws2/short-topic-name
# ... edits ...
make build && go test -race ./... && ./scripts/check-no-secrets.sh
git push -u origin HEAD
gh pr create --fill
gh run watch                 # wait for CI green
gh pr merge --squash --delete-branch
git switch main && git pull --ff-only
```

Emergency bypass (initial repo push, hotfix): `PORTAL_ALLOW_PUSH_MAIN=1 git
push`. Every use of the bypass should be justifiable in the commit message.

## Running the server

Phase 1 is still under construction. `cmd/portal --help` prints the current
flag set. Actual endpoints are added by later workstreams.

## Bootstrapping the first administrator

Once WS4 lands, a fresh install requires a one-time bootstrap:

```sh
./bin/portalctl bootstrap-admin --email you@bcars.org
```

There is no default password. The command prints a single-use invitation URL
to be opened in a browser.

## Backups

Backups are owned by the appointed webmaster. See
`docs/runbooks/backup-restore.md` (added in WS8).

## License

TBD — decision recorded in `docs/adr/0000-license.md`.
