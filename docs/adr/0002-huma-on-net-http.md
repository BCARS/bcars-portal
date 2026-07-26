# ADR-0002: Huma v2 on net/http

- Status: Accepted
- Date: 2026-07-26

## Context

Phase 1 is API-first. Every user-visible query or command must have a
versioned, typed contract that both the officer UI and future AI tool
adapters share. We also want an accurate OpenAPI document as a build
artifact, not as documentation drift.

## Decision

Use Huma v2 on top of Go 1.22+'s enhanced `net/http.ServeMux`. No
third-party router (no chi, gin, echo). Operations are registered with typed
input/output structs; the OpenAPI document is generated from the same
metadata.

## Rejected alternatives

- **Hand-written handlers + separately maintained OpenAPI**: guarantees
  drift.
- **grpc-gateway / connect-go**: overkill for a small club portal with only
  browser + curl clients.

## Consequences

- Every Huma operation must also carry portal-specific metadata:
  required capability, audit action, confirmation level,
  ai_tool_eligibility. Enforced by a startup check in `internal/httpapi`
  (WS2.3).
- `docs/openapi.json` and `docs/capability-catalog.json` are checked in and
  diffed in CI.
- Rich error responses use RFC 7807 problem+json.
