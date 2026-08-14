# Local Operation and Deployment Notes

## Quick Start

```bash
# 1. Build and initialize the local database
make build
./bin/portalctl bootstrap-admin --email admin@yourclub.org --db bcars.db

# 2. Start the development server
make run
```

Open the single-use invitation URL printed by `bootstrap-admin`. `make run`
serves `http://localhost:8080` and deliberately relaxes the password-pepper and
Secure-cookie requirements for local development only.

## Prerequisites

- Go 1.26.0 (the Makefile selects this toolchain)
- SQLite is embedded through a pure-Go driver; no separate install is needed
- No external services required

## Build

```bash
make build              # Builds bin/portal and bin/portalctl
make test               # Run all tests
make lint               # Run linter
make migration-updown   # Verify migration up/down/up
make sqlc-diff          # Verify generated query code
make openapi-diff       # Verify OpenAPI and capability catalogs
make smoke              # Exercise the shipped binaries outside the repo
```

## Database Setup

```bash
# Create and migrate database
./bin/portalctl bootstrap-admin \
  --email admin@yourclub.org \
  --db data/portal.db \
  --base-url https://portal.yourclub.org

```

The `bootstrap-admin` command creates the database, runs migrations, and
prints an invitation URL for the first administrator account. Everyone else is
onboarded with `POST /invitations` from the running portal.

### Demo users (development only)

`portalctl seed-demo` creates accounts whose passwords are published in the
source. It is **not** part of a production build: the code lives behind the
`demoseed` build tag, so `make build` produces a binary in which the command
does not exist. Developers who want it build their own:

```bash
go build -tags demoseed -o bin/portalctl-demo ./cmd/portalctl
PORTAL_PASSWORD_PEPPER=<dev pepper> ./bin/portalctl-demo seed-demo --db /tmp/dev.db
```

Even in a tagged build the command refuses to run against a database that
holds any user other than the demo accounts, because seeding overwrites
existing passwords by email. Never point it at a database with real members.

## Running locally

Use the Makefile target for an ordinary local session:

```bash
make run
```

The default database is `bcars.db` and the default file-log mail directory is
`mail-outbox`. Both can be changed without editing the Makefile:

```bash
make run RUN_DB=/tmp/bcars-portal.db RUN_MAIL_DIR=/tmp/bcars-mail
```

`make run` passes `--allow-empty-pepper` and `--allow-insecure-cookies`; never
copy those flags into a production service.

## Production packaging

Two supported shapes: a container image, or the plain binary on a host. Both are
built from this repository; both take every secret from the environment at run
time.

No service unit is shipped. Deployments are expected to be a container platform
or a plain process on a host that may not offer systemd, and a unit file that
nothing here exercises would be documentation pretending to be an artifact. If
you do supervise it with systemd, the invariants below are what the unit has to
preserve: a dedicated non-root user, an `EnvironmentFile` at mode `0600` for the
secrets, and migrations before the health check is expected to pass.

### Container image

```bash
make docker                    # builds bcars-portal:<version>
make docker-smoke              # builds, then starts the image and asserts it works
make docker IMAGE=ghcr.io/bcars/bcars-portal IMAGE_TAG=v1.2.3
```

The image is built by the checked-in [`Dockerfile`](../Dockerfile): a Go build
stage, then a distroless runtime holding only `portal` and `portalctl`. The
binaries are static, the container runs as `nonroot` (uid 65532), and the
front-end assets are compiled into the binary — there is no asset directory to
mount or copy.

`make docker-smoke` runs [`scripts/docker-smoke.sh`](../scripts/docker-smoke.sh),
which starts the image with a mounted data directory, waits for `/readyz`,
confirms the database landed on the volume, confirms the container serves its
own assets, and fails if the image carries a secret in its environment. It runs
in CI on every change to code or packaging.

Run it:

```bash
docker run -d --name portal \
  -p 8080:8080 \
  -v /srv/bcars/data:/data \
  -e PORTAL_PASSWORD_PEPPER="$(cat /run/secrets/portal-pepper)" \
  -e PORTAL_BASE_URL=https://portal.yourclub.org \
  -e PORTAL_MIGRATE=true \
  ghcr.io/bcars/bcars-portal:v1.2.3
```

`PORTAL_DB=/data/portal.db` and `PORTAL_ADDR=:8080` are the image defaults.

**Startup order matters.** `/readyz` returns 503 with `schema version mismatch`
until migrations have run, so a fresh volume needs either `PORTAL_MIGRATE=true`
on the server, or a separate `portal -migrate-only` run before it — an init
container, or a one-shot `docker run` with the same volume. A deployment whose
health check is expected to pass before migrations have run will crash-loop.
Nothing runs migrations implicitly.

### Kubernetes

[`deploy/k8s/portal.example.yaml`](../deploy/k8s/portal.example.yaml) is a
worked example: PVC, Secret placeholders, ConfigMap of the environment
variables, an init container that runs `-migrate-only`, and liveness/readiness
probes on `/healthz` and `/readyz`. It is an example to adapt, not a supported
artifact. Two things in it are not adjustable: `replicas: 1` with the `Recreate`
strategy, and a `ReadWriteOnce` volume. SQLite admits one writer, and a rolling
update would briefly run two pods against one database file.

### Plain binary on a host

```bash
make build
install -m 0755 bin/portal bin/portalctl /opt/bcars-portal/bin/

# Secrets in a file only the service user can read.
install -m 0600 /dev/null /etc/bcars-portal.env
# PORTAL_PASSWORD_PEPPER=...
# PORTAL_SMTP_PASSWORD=...

set -a; . /etc/bcars-portal.env; set +a
/opt/bcars-portal/bin/portal -migrate-only -db /srv/bcars/data/portal.db
/opt/bcars-portal/bin/portal \
  -db /srv/bcars/data/portal.db \
  -base-url https://portal.yourclub.org
```

Put TLS in front of it either way; session cookies carry `Secure`, so a portal
reached over plaintext HTTP accepts a sign-in and then never sees the cookie
again.

## Backup & Restore

```bash
# Backup (WAL-safe, encrypted, with SHA-256 manifest)
export PORTAL_BACKUP_PASSPHRASE='<from the password manager>'
./bin/portalctl backup --db data/portal.db --to backups/

# Restore (never overwrites live DB)
./bin/portalctl restore --from backups/portal-backup-*.db.age --into restored/
```

Follow `docs/runbooks/backup-restore.md` for custody, restore validation, and
drill instructions.

## Configuration

Every non-secret setting can be given as a flag or as the environment variable
bound to it. **Precedence is flag, then environment, then default** — a flag
passed on the command line wins even when its value equals the default, and a
variable that is unset or empty leaves the default in place. An environment
variable the server cannot parse (`PORTAL_SMTP_PORT=eight`) stops startup with a
message naming both the variable and the flag; it is never silently ignored.

The bindings live in `cmd/portal/env.go`, and `portal -help` prints them.
`TestDocumentedEnvironmentVariablesMatchTheCode` fails if this table and that
list disagree — the table described `PORTAL_DB`, `PORTAL_ADDR` and
`PORTAL_LOG_LEVEL` for months while nothing read any of them
(`bcars-portal-fmc.8`).

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--db` | `PORTAL_DB` | `bcars.db` | Path to SQLite database |
| — | `PORTAL_PASSWORD_PEPPER` | *(none)* | **Required.** Secret mixed into every password hash. Minimum 16 bytes. |
| `--allow-empty-pepper` | — | `false` | Development only. Start without a pepper. Deliberately has no env var. |
| `--allow-insecure-cookies` | — | `false` | Development only. Issue session cookies without `Secure`. Deliberately has no env var. |
| `--addr` | `PORTAL_ADDR` | `:8080` | Listen address |
| `--log-level` | `PORTAL_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |
| `--base-url` | `PORTAL_BASE_URL` | `http://localhost:8080` | Public base URL used to build recovery/invitation links |
| `--migrate` | `PORTAL_MIGRATE` | `false` | Apply pending migrations at startup, then serve |
| `--migrate-only` | — | `false` | Apply migrations and exit. Flag-only: it is a one-shot command, not a mode a running service should be configured into. |
| `--mail-transport` | `PORTAL_MAIL_TRANSPORT` | `filelog` | Outbound mail transport: `filelog` (JSON files, dev) or `smtp` |
| `--mail-dir` | `PORTAL_MAIL_DIR` | `mail-outbox` | Directory for the `filelog` transport (created if missing) |
| `--smtp-host` | `PORTAL_SMTP_HOST` | — | SMTP relay host (required for `--mail-transport=smtp`) |
| `--smtp-port` | `PORTAL_SMTP_PORT` | `587` | SMTP relay port |
| `--smtp-user` | `PORTAL_SMTP_USER` | — | SMTP username; empty means no authentication |
| `--smtp-from` | `PORTAL_SMTP_FROM` | — | From address (required for `--mail-transport=smtp`) |
| — | `PORTAL_SMTP_PASSWORD` | — | SMTP password; env-only so it never appears in process listings |
| — | `PORTAL_BACKUP_PASSPHRASE` | — | Passphrase for `portalctl backup`/`restore`; env-only. Minimum 12 characters. |
| `--trusted-proxy-header` | `PORTAL_TRUSTED_PROXY_HEADER` | *(none)* | Header carrying the real client address behind a reverse proxy, e.g. `X-Forwarded-For`. See below. |

The three secrets — `PORTAL_PASSWORD_PEPPER`, `PORTAL_SMTP_PASSWORD` and
`PORTAL_BACKUP_PASSPHRASE` — have no flag at all, and no flag will be added: a
flag is visible in the process table and in shell history. The two
development-only switches have no environment variable, so relaxing production
security takes an explicit command line rather than a copied manifest.

### Client addresses behind a proxy

Sessions and password-recovery links record a keyed hash of the requesting
client's address, which is what per-source abuse limiting and audit correlation
group on. By default the address is the transport peer — the only value a
client cannot choose for itself.

Set `--trusted-proxy-header` **only** when a reverse proxy you control sits in
front of the portal and *overwrites* that header on every inbound request. If
the header is honoured on a directly reachable deployment, any caller can send
`X-Forwarded-For: <anything>` and appear as a different source on every
request, which silently disables the limiting the value exists to feed. The
leftmost entry of the header is used; an unparseable value falls back to the
peer address.

The hash is an HMAC keyed by a subkey derived from `PORTAL_PASSWORD_PEPPER`
rather than by a secret of its own — a bare digest of an IPv4 address is
reversible by enumeration, and a second secret with the same threat model and
the same lifecycle is one more thing to lose. Running with
`--allow-empty-pepper` therefore records no address hash at all; the column is
left empty rather than filled with a reversible or meaningless value.

Production deployments should set `--base-url` to the externally reachable URL
and use `--mail-transport=smtp`; the default `filelog` transport writes messages
to disk instead of delivering them.

## Health Checks

- `GET /healthz` — liveness (always 200)
- `GET /readyz` — readiness (checks DB, migrations, pragmas)

## Illustrative file permissions

```
/opt/bcars-portal/
├── bin/              # 0755 root:root
│   ├── portal
│   └── portalctl
├── data/             # 0750 portal:portal
│   └── portal.db     # 0640 portal:portal
├── backups/          # 0750 portal:portal
└── logs/             # 0750 portal:portal
```

## Security Notes

- Run as a dedicated non-root user
- SQLite database file should be readable only by the portal user
- Use a reverse proxy (nginx/caddy) for TLS termination
- Session cookies are HttpOnly + SameSite=Lax + Secure (see below)
- PII is redacted in structured logs (see `docs/log-retention.md`)


## Session cookie security

Both surfaces — the JSON API and the server-rendered admin UI — issue their
session cookie from one shared configuration, so no set or clear point can
drift from the others. The attributes are `HttpOnly`, `SameSite=Lax`, `Path=/`
and `Secure`.

`Secure` is on by default. Because TLS is terminated at the reverse proxy, the
server cannot tell from the request alone whether the browser is on HTTPS, so
the safe attribute is the default and turning it off is an explicit decision.

`--allow-insecure-cookies` drops `Secure`. It exists for local development,
where the portal is reached over plaintext `http://localhost` and a `Secure`
cookie is never sent back — which presents as being unable to stay signed in
after a successful sign-in. Run the development server as:

```bash
make run
```

Never set this in production: without `Secure` the session cookie is eligible
to travel over plaintext HTTP, where anyone on the path can take the session.

## Password pepper

The pepper is a server-side secret mixed into every password hash before
Argon2id. It is what makes a stolen `bcars.db` insufficient to mount an offline
password-cracking attack: the attacker needs the database *and* the pepper,
which lives only in the process environment.

### Custody

- Supply it as `PORTAL_PASSWORD_PEPPER`, never as a flag — flags are visible in
  the process table and shell history.
- Generate at least 32 bytes from a CSPRNG:
  `openssl rand -base64 32`
- Store it wherever your deployment keeps secrets (systemd `EnvironmentFile`
  with mode `0600`, or the container platform's secret store). It must **not**
  live in the repository, in the image, or in the same backup as the database —
  a backup containing both defeats the purpose.
- Back it up separately and durably. Losing it is equivalent to losing every
  password (see below).

The server refuses to start without one unless `--allow-empty-pepper` is passed,
which exists for local development and is never appropriate in production.

### Rotation, and why it is expensive

An Argon2id hash records no indication of which pepper produced it. There is
therefore no way to distinguish "wrong password" from "right password, wrong
pepper" at verification time. Changing the pepper does not fail loudly on its
own — it makes *every* sign-in return "invalid credentials", for every account,
with a message that reads like ordinary user error.

To make that failure legible, the fingerprint of the pepper in use is recorded
in `app_settings` on first start. If a later start presents a different pepper,
the server **refuses to start** with an explicit message rather than silently
locking everyone out. The fingerprint is an HMAC, so it does not disclose the
pepper to anyone who can read the database.

Consequently:

- **Set the pepper before the first account exists.** While the installation has
  no users, changing it costs nothing.
- **After accounts exist, rotation requires a password reset for every user.**
  There is no re-hash-in-place: the plaintext is not recoverable, so each user
  must set a new password through the recovery flow. Plan it as a coordinated
  event, not a config change.
- **If the pepper is lost, every account must go through recovery.** Recovery
  itself still works: it is email-token based and does not depend on the old
  hash. Restoring service means setting a new pepper, clearing the recorded
  fingerprint, and sending everyone a reset link.

A future phase could carry a pepper version alongside each hash and re-hash on
next successful login, which would make rotation transparent. Phase 1 does not,
because Phase 1 begins with zero accounts and the simpler scheme is easier to
reason about.
