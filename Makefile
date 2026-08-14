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

# Seeded demo portal (bcars-portal-bql). DEVELOPMENT ONLY.
#
# The pepper is defined here, once, and handed to both the seeding step and the
# server. That is the whole point of the target: the pepper must be identical in
# both places or seeding produces accounts whose passwords can never verify,
# which is the failure bcars-portal-fmc.14 fixed and which a hand-typed sequence
# reintroduces every time. It is a fixed development string on purpose and is
# published here; nothing that uses it is reachable from a shipped binary.
DEMO_DIR     ?= $(CURDIR)/demo-data
DEMO_DB      ?= $(DEMO_DIR)/demo.db
DEMO_MAIL    ?= $(DEMO_DIR)/mail-outbox
DEMO_ADDR    ?= :8080
DEMO_PEPPER  ?= bcars-local-demo-pepper-not-for-any-real-data

.PHONY: all build build-demo test test-demoseed smoke lint fmt vet staticcheck golangci sqlc sqlc-diff openapi openapi-diff migrate run run-demo seed-demo demo-reset tools clean check-secrets check-ci-paths install-hooks migration-updown

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
	./scripts/check-no-secrets.sh --self-test
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

# git diff says nothing about an untracked file, so a newly generated query
# file that was never git-added used to pass this gate both locally and in CI
# (bcars-portal-6q6.7). git status reports modified and untracked alike.
sqlc-diff: sqlc
	@if [ -n "$$(git status --porcelain -- internal/db/sqlc)" ]; then \
		git status --short -- internal/db/sqlc; \
		echo "sqlc drift detected; run 'make sqlc' and commit the result"; \
		exit 1; \
	fi

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

# --- Seeded demo portal. DEVELOPMENT ONLY. ---
#
# `make run-demo` takes a clean checkout to a portal with members in it. Without
# it, standing one up means building two binaries, migrating a throwaway
# database, exporting a pepper, seeding with that pepper, and starting the
# server with the same pepper again -- five steps, none of them written down,
# with one value that has to match in three places.
#
# `make run` is deliberately NOT this: it uses a default build with no
# seed-demo, an empty pepper, and a database with no members, so every figure on
# every screen reads 0.

# The demo portalctl is built to its own name. Writing a demoseed binary to
# bin/portalctl would leave a binary containing seed-demo and its published
# passwords sitting where every other target expects the shipped one.
build-demo: build
	$(GO) build -trimpath -tags demoseed -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/portalctl-demo ./cmd/portalctl

seed-demo: build-demo
	@mkdir -p $(DEMO_DIR)
	$(BIN_DIR)/portal --migrate-only --db $(DEMO_DB)
	@PORTAL_PASSWORD_PEPPER=$(DEMO_PEPPER) $(BIN_DIR)/portalctl-demo seed-demo --db $(DEMO_DB)

# Re-running is safe: seeding is idempotent and will not duplicate members.
# Secure cookies are dropped because sign-in over plaintext http://localhost
# needs it, exactly as `run` already does. Nothing else is relaxed -- unlike
# `run`, this target supplies a real pepper rather than allowing an empty one.
run-demo: seed-demo
	@echo
	@echo "  Seeded demo portal on http://localhost$(DEMO_ADDR)"
	@echo "  DEVELOPMENT ONLY. Sign-in details are printed by the seeding step above."
	@echo "  Database $(DEMO_DB) -- delete it or run 'make demo-reset' to start over."
	@echo
	@PORTAL_PASSWORD_PEPPER=$(DEMO_PEPPER) $(BIN_DIR)/portal \
		--allow-insecure-cookies --db $(DEMO_DB) --mail-dir $(DEMO_MAIL) --addr $(DEMO_ADDR)

# Throw the demo database away. Scoped to the demo directory so it cannot be
# pointed at a real one by overriding a variable.
demo-reset:
	rm -rf $(DEMO_DIR)
	@echo "demo data removed; 'make run-demo' will build a fresh one"

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
