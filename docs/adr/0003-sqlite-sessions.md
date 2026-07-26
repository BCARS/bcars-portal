# ADR-0003: Server-side sessions in SQLite

- Status: Accepted
- Date: 2026-07-26

## Context

Phase 1 uses password-authenticated officer sessions and (later) short-lived
email links. We need immediate server-side revocation on password change,
role change, or officer removal.

## Decision

Store sessions in a `sessions` table in the same SQLite database. The client
cookie carries only an opaque random session id (32 bytes hex), HttpOnly,
Secure, SameSite=Lax. All session state is server-side.

## Rejected alternatives

- **Signed cookie carrying claims (JWT-style)**: cannot be revoked
  immediately without a denylist that is effectively a session table anyway.
- **Redis / separate session store**: adds an operational component with no
  benefit at our scale.

## Consequences

- Session rotation on privilege change is a simple `UPDATE`/`INSERT` +
  cookie reissue.
- "Recent auth" for high-impact operations is a timestamp check on the
  session row, not on the cookie.
- Session table grows; a background job (or startup sweep) expires stale
  rows.
