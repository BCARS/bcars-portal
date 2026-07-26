# BCARS portal Makefile.
# Every target should be safe to run on a clean checkout with only Go installed.

GO           ?= go
BIN_DIR      ?= bin
PKGS         := ./...

# Version stamp. Overridable: `make build VERSION=v1.2.3`.
# Defaults to the nearest git tag (e.g. "v1.2.3"); when the repo has no tag
# yet we fall back to "dev". Revision SHA and dirty state come from
# runtime/debug at run time, so we intentionally do NOT include them here.
VERSION      ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
VERSION_PKG  := github.com/bcars/bcars-portal/internal/version
LDFLAGS      := -s -w -X $(VERSION_PKG).version=$(VERSION)
GOLANGCI     := $(shell go env GOPATH)/bin/golangci-lint
STATICCHECK  := $(shell go env GOPATH)/bin/staticcheck
SQLC         := $(shell go env GOPATH)/bin/sqlc

.PHONY: all build test lint fmt vet staticcheck golangci sqlc sqlc-diff openapi openapi-diff migrate run tools clean check-secrets install-hooks

all: build

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/portal ./cmd/portal
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/portalctl ./cmd/portalctl

test:
	$(GO) test -race -count=1 $(PKGS)

fmt:
	$(GO) fmt $(PKGS)

vet:
	$(GO) vet $(PKGS)

staticcheck:
	@if [ ! -x "$(STATICCHECK)" ]; then \
		echo "installing staticcheck"; \
		$(GO) install honnef.co/go/tools/cmd/staticcheck@latest; \
	fi
	$(STATICCHECK) $(PKGS)

golangci:
	@if [ ! -x "$(GOLANGCI)" ]; then \
		echo "installing golangci-lint"; \
		$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
	fi
	$(GOLANGCI) run

check-secrets:
	./scripts/check-no-secrets.sh

lint: fmt vet staticcheck golangci check-secrets

sqlc:
	@if [ ! -x "$(SQLC)" ]; then \
		echo "installing sqlc"; \
		$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@latest; \
	fi
	$(SQLC) generate

sqlc-diff: sqlc
	@git diff --exit-code -- internal/db/sqlc \
		|| (echo "sqlc drift detected; run 'make sqlc' and commit"; exit 1)

openapi: build
	./$(BIN_DIR)/portal -dump-openapi docs/openapi.json -dump-catalog docs/capability-catalog.json
	@echo "wrote docs/openapi.json and docs/capability-catalog.json"

openapi-diff: openapi
	@git diff --exit-code -- docs/openapi.json docs/capability-catalog.json 2>/dev/null \
		|| (echo "openapi/capability catalog drift; run 'make openapi' and commit"; exit 1)

migrate:
	@echo "migrations not yet implemented (WS3.1)"

run: build
	./$(BIN_DIR)/portal

tools:
	$(GO) install honnef.co/go/tools/cmd/staticcheck@latest
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@latest

clean:
	rm -rf $(BIN_DIR) dist coverage.out coverage.html

install-hooks:
	@if [ ! -d .git ]; then echo "not a git repo"; exit 1; fi
	install -m 0755 hooks/pre-push .git/hooks/pre-push
	@echo "installed .git/hooks/pre-push"
