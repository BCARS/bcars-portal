# ADR-0005: Capability-based authorization

- Status: Accepted
- Date: 2026-07-26

## Context

The plan calls for capability-based authorization with default-deny, an
explicit capability catalog, and no hard-coded title checks in application
code. The same policy layer serves the HTTP API and future AI tool handlers.

## Decision

- Capabilities are strings with a stable, versioned catalog
  (`internal/authz/catalog.go`) and matching database rows seeded via
  migration.
- Roles bundle capabilities. Roles may map to club offices or technical
  positions.
- A `Policy.Authorize(ctx, principal, action, resource)` call is the only
  authorization primitive. It denies by default and always logs an
  `authz.denied` audit event on denial with a structured `reason_code`.
- Every Huma operation registers its required capability, resource-authz
  rule, and audit action. A startup check refuses to boot if any operation
  lacks metadata.

## Rejected alternatives

- **String role checks scattered in handlers**: what the archive did;
  brittle and untestable.
- **RBAC-only (no direct grants)**: too rigid for occasional exceptions
  like temporary treasurer coverage.

## Consequences

- Adding a capability requires a migration and a catalog entry — this is
  intentional friction.
- The AI tool layer never accepts a trusted principal from the model.
