# BCARS portal container image.
#
# Two stages: a build stage with the Go toolchain, and a runtime stage that
# contains the two binaries and nothing else — no shell, no package manager, no
# source. Both binaries are static (CGO_ENABLED=0); SQLite is a pure-Go driver,
# so the image needs no C library.
#
# Assets are compiled into the binary (internal/web/static), so there is no
# asset directory to copy — an earlier documented Dockerfile copied
# internal/web/static from the source tree, which never existed as a runtime
# path (bcars-portal-fmc.8).
#
# NO SECRET BELONGS IN THIS IMAGE. PORTAL_PASSWORD_PEPPER is required at run
# time, PORTAL_SMTP_PASSWORD is needed for SMTP mail, and PORTAL_BACKUP_PASSPHRASE
# is needed by portalctl backup/restore. All three come from the platform's
# secret store at run time. See docs/deployment.md.

FROM golang:1.26 AS build

WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module
# cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# VERSION is stamped into the binary the same way the Makefile does it.
ARG VERSION=dev
ENV CGO_ENABLED=0
RUN go build -trimpath \
        -ldflags "-s -w -X github.com/bcars/bcars-portal/internal/version.version=${VERSION}" \
        -o /out/portal ./cmd/portal && \
    go build -trimpath \
        -ldflags "-s -w -X github.com/bcars/bcars-portal/internal/version.version=${VERSION}" \
        -o /out/portalctl ./cmd/portalctl

# The database directory is created here because the runtime image has no shell
# to create it with. 65532 is distroless's nonroot user.
RUN mkdir -p /data && chown 65532:65532 /data

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/portal /usr/local/bin/portal
COPY --from=build /out/portalctl /usr/local/bin/portalctl
COPY --from=build --chown=65532:65532 /data /data

# Defaults suit a container: the database lives on a mounted volume, and the
# listen address is explicit. Everything here is overridable at run time, and
# flags still beat these (see cmd/portal/env.go).
ENV PORTAL_DB=/data/portal.db \
    PORTAL_ADDR=:8080

VOLUME ["/data"]
EXPOSE 8080

USER nonroot:nonroot

# No migrations are run implicitly. /readyz reports 503 until the schema matches
# the binary, so the deployment must either run `portal -migrate-only` first
# (an init container) or start the server with PORTAL_MIGRATE=true. Choosing
# silently for the operator is how a fresh volume turns into a crash loop.
ENTRYPOINT ["/usr/local/bin/portal"]
