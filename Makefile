# BCARS portal Makefile.
# Every target should be safe to run on a clean checkout with only Go installed.

GO           ?= go
BIN_DIR      ?= $(CURDIR)/bin
PKGS         := ./...
GOBIN				?= $(BIN_DIR)
GOOSE        := $(BIN_DIR)/goose

# Ensure the toolchain matches go.mod so fmt/lint use the correct Go version.
export GOTOOLCHAIN := go1.26.0

# Version stamp. Overridable: `make build VERSION=v1.2.3`.
# Defaults to the nearest git tag (e.g. "v1.2.3"); when the repo has no tag
# yet we fall back to "dev". Revision SHA and dirty state come from
# runtime/debug at run time, so we intentionally do NOT include them here.
VERSION      ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
VERSION_PKG  := github.com/bcars/bcars-portal/internal/version
LDFLAGS      := -s -w -X $(VERSION_PKG).version=$(VERSION)
GOLANGCI     := $(BIN_DIR)/golangci-lint
STATICCHECK  := $(BIN_DIR)/staticcheck
SQLC         := $(BIN_DIR)/sqlc
RUN_DB       ?= bcars.db
RUN_MAIL_DIR ?= mail-outbox

.PHONY: all build test test-demoseed smoke lint fmt vet staticcheck golangci sqlc sqlc-diff openapi openapi-diff migrate run tools clean check-secrets check-ci-paths install-hooks migration-updown

all: build

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/portal ./cmd/portal
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/portalctl ./cmd/portalctl

test: test-demoseed
	$(GO) test -race -count=1 $(PKGS)

# seed-demo lives behind the `demoseed` build tag so it is absent from shipped
# binaries; its tests only compile with the tag, so they need their own run.
test-demoseed:
	$(GO) test -race -count=1 -tags demoseed ./cmd/portalctl/

# Production-assembly smoke test: builds both binaries, migrates a temporary
# database, runs portalctl bootstrap-admin, starts the server, and drives the
# real HTTP surface. This is the gate that catches assembly defects the
# package tests structurally cannot — every other test builds its own router.
smoke:
	$(GO) test -count=1 -v ./internal/smoke/

fmt:
	$(GO) fmt $(PKGS)

vet:
	$(GO) vet $(PKGS)

staticcheck:
	@if [ ! -x "$(STATICCHECK)" ]; then \
		echo "installing staticcheck"; \
		GOBIN=$(GOBIN) $(GO) install honnef.co/go/tools/cmd/staticcheck@latest; \
	fi
	$(STATICCHECK) $(PKGS)

golangci:
	@if [ ! -x "$(GOLANGCI)" ]; then \
		echo "installing golangci-lint"; \
		GOBIN=$(GOBIN) $(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
	fi
	$(GOLANGCI) run

check-secrets:
	./scripts/check-no-secrets.sh
	./scripts/check-version-conflicts.sh

check-ci-paths:
	./scripts/ci-code-changed.sh --self-test

lint: fmt vet staticcheck golangci check-secrets check-ci-paths

sqlc:
	@if [ ! -x "$(SQLC)" ]; then \
		echo "installing sqlc"; \
		GOBIN=$(GOBIN) $(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1; \
	fi
	$(SQLC) generate

sqlc-diff: sqlc
	@git diff --exit-code -- internal/db/sqlc \
		|| (echo "sqlc drift detected; run 'make sqlc' and commit"; exit 1)

openapi: build
	$(BIN_DIR)/portal -dump-openapi docs/openapi.json -dump-catalog docs/capability-catalog.json
	@echo "wrote docs/openapi.json and docs/capability-catalog.json"

openapi-diff: openapi
	@git diff -I '"version":' --exit-code -- docs/openapi.json docs/capability-catalog.json 2>/dev/null \
		|| (echo "openapi/capability catalog drift; run 'make openapi' and commit"; exit 1)

migrate: build
	$(BIN_DIR)/portal --migrate-only --db bcars.db

run: build
	# Development only: production must supply a pepper and use Secure cookies.
	$(BIN_DIR)/portal --allow-empty-pepper --allow-insecure-cookies --migrate --db $(RUN_DB) --mail-dir $(RUN_MAIL_DIR)

# Migration up/down/up round-trip. Used in CI to verify every migration
# has a matching Down that leaves the schema consistent for a second Up.
migration-updown:
	@if [ ! -x "$(GOOSE)" ]; then \
		echo "installing goose"; \
		GOBIN=$(GOBIN) $(GO) install github.com/pressly/goose/v3/cmd/goose@v3.27.3; \
	fi
	@TMPDB=$$(mktemp /tmp/bcars-migrate-XXXXXX.db) && \
	rm -f "$$TMPDB" && \
	$(GOOSE) -dir internal/db/migrations sqlite "$$TMPDB" up && \
	$(GOOSE) -dir internal/db/migrations sqlite "$$TMPDB" down && \
	$(GOOSE) -dir internal/db/migrations sqlite "$$TMPDB" up && \
	rm -f "$$TMPDB" && \
	echo "migration-updown: ok"

tools:
	GOBIN=$(GOBIN) $(GO) install honnef.co/go/tools/cmd/staticcheck@v0.7.0
	GOBIN=$(GOBIN) $(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8
	GOBIN=$(GOBIN) $(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1

clean:
	rm -rf $(BIN_DIR) dist coverage.out coverage.html

install-hooks:
	@if [ ! -d .git ]; then echo "not a git repo"; exit 1; fi
	install -m 0755 hooks/pre-push .git/hooks/pre-push
	@echo "installed .git/hooks/pre-push"
