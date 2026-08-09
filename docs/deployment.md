# Local Deployment Guide

## Quick Start

```bash
# 1. Build
make build

# 2. Initialize database and create admin
./bin/portalctl bootstrap-admin --email admin@yourclub.org --db data/portal.db

# 3. Run
./bin/portal --db data/portal.db --addr :8080
```

## Prerequisites

- Go 1.22+ (for building from source)
- SQLite 3.35+ (bundled via go-sqlite3)
- No external services required

## Build

```bash
make build              # Builds bin/portal and bin/portalctl
make test               # Run all tests
make lint               # Run linter
```

## Database Setup

```bash
# Create and migrate database
./bin/portalctl bootstrap-admin \
  --email admin@yourclub.org \
  --db data/portal.db \
  --base-url https://portal.yourclub.org

# (Optional) Seed demo users for testing
./bin/portalctl seed-demo --db data/portal.db
```

The `bootstrap-admin` command creates the database, runs migrations, and
prints an invitation URL for the first administrator account.

## Running

### Direct

```bash
./bin/portal \
  --db data/portal.db \
  --addr :8080 \
  --log-level info
```

### systemd

Create `/etc/systemd/system/bcars-portal.service`:

```ini
[Unit]
Description=BCARS Members Portal
After=network.target

[Service]
Type=simple
User=portal
Group=portal
WorkingDirectory=/opt/bcars-portal
ExecStart=/opt/bcars-portal/bin/portal --db /opt/bcars-portal/data/portal.db --addr :8080
Restart=on-failure
RestartSec=5

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/opt/bcars-portal/data
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now bcars-portal
sudo systemctl status bcars-portal
```

### Docker

```dockerfile
FROM golang:1.22-bookworm AS builder
WORKDIR /src
COPY . .
RUN make build

FROM debian:bookworm-slim
RUN groupadd -r portal && useradd -r -g portal portal
COPY --from=builder /src/bin/portal /usr/local/bin/
COPY --from=builder /src/bin/portalctl /usr/local/bin/
COPY --from=builder /src/internal/web/templates /opt/bcars-portal/templates
COPY --from=builder /src/internal/web/static /opt/bcars-portal/static
RUN mkdir -p /data && chown portal:portal /data
USER portal
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["portal", "--db", "/data/portal.db", "--addr", ":8080"]
```

```bash
docker build -t bcars-portal .
docker run -d -p 8080:8080 -v portal-data:/data bcars-portal
```

## Backup & Restore

```bash
# Backup (WAL-safe, with SHA-256 manifest)
./bin/portalctl backup --db data/portal.db --to backups/

# Restore (never overwrites live DB)
./bin/portalctl restore --from backups/portal-backup-*.db --into restored/
```

See the backup manifest JSON for schema version, file size, and SHA-256 checksum.

## Configuration

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--db` | `PORTAL_DB` | `portal.db` | Path to SQLite database |
| — | `PORTAL_PASSWORD_PEPPER` | *(none)* | **Required.** Secret mixed into every password hash. Minimum 16 bytes. |
| `--allow-empty-pepper` | — | `false` | Development only. Start without a pepper. |
| `--allow-insecure-cookies` | — | `false` | Development only. Issue session cookies without `Secure`. |
| `--addr` | `PORTAL_ADDR` | `:8080` | Listen address |
| `--log-level` | `PORTAL_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |
| `--base-url` | — | `http://localhost:8080` | Public base URL used to build recovery/invitation links |
| `--mail-transport` | — | `filelog` | Outbound mail transport: `filelog` (JSON files, dev) or `smtp` |
| `--mail-dir` | — | `mail-outbox` | Directory for the `filelog` transport (created if missing) |
| `--smtp-host` | — | — | SMTP relay host (required for `--mail-transport=smtp`) |
| `--smtp-port` | — | `587` | SMTP relay port |
| `--smtp-user` | — | — | SMTP username; empty means no authentication |
| `--smtp-from` | — | — | From address (required for `--mail-transport=smtp`) |
| — | `PORTAL_SMTP_PASSWORD` | — | SMTP password; env-only so it never appears in process listings |

Production deployments should set `--base-url` to the externally reachable URL
and use `--mail-transport=smtp`; the default `filelog` transport writes messages
to disk instead of delivering them.

## Health Checks

- `GET /healthz` — liveness (always 200)
- `GET /readyz` — readiness (checks DB, migrations, pragmas)

## File Permissions

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
./bin/portal -db data/portal.db -allow-insecure-cookies
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
