# ADR-0004: Argon2id with pepper for officer passwords

- Status: Accepted
- Date: 2026-07-26

## Context

Officer accounts use password authentication. Password recovery is via
short-lived email links. Threat model includes offline attack against a
database backup.

## Decision

- Hash officer passwords with `argon2id` from `golang.org/x/crypto/argon2`.
- Store `time`, `memory`, `threads`, salt length in per-hash metadata so
  parameters can be rotated later.
- Add a per-install pepper (`PORTAL_PASSWORD_PEPPER`, 32 random bytes)
  mixed in via HMAC before hashing. The pepper lives only in the process
  environment, never in the database. A stolen backup alone cannot be
  attacked offline without the pepper.
- Verification is constant-time via `subtle.ConstantTimeCompare`.

## Rejected alternatives

- **bcrypt**: still fine, but argon2id is the current recommendation and
  parameter tuning is more transparent.
- **scrypt**: comparable; no clear reason to prefer over argon2id.

## Consequences

- `PORTAL_PASSWORD_PEPPER` becomes a critical secret; rotation requires
  a rehash on next successful login and is documented in the runbook.
- Login failures use a constant-time path regardless of email existence
  (see WS4.3).
