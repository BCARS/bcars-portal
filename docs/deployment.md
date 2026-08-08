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
| `--addr` | `PORTAL_ADDR` | `:8080` | Listen address |
| `--log-level` | `PORTAL_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |

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
- Session cookies are HttpOnly + SameSite=Lax
- PII is redacted in structured logs (see `docs/log-retention.md`)
