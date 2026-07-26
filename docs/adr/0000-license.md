# ADR-0000: License selection

- Status: Proposed
- Date: 2026-07-26

## Context

The portal is a private BCARS repository containing club-specific business
rules and, at runtime, member data. We still need to decide the source
license because officers rotate and future volunteers may need to fork or
publish scrubbed excerpts.

## Options

1. All-rights-reserved (no license): safest for a private club project, but
   makes it hard for a departing volunteer to keep a working copy for
   reference.
2. AGPLv3: strong copyleft; would apply if the portal were ever hosted for
   another club.
3. MIT/BSD: permissive; simplest for future volunteers.

## Decision

Deferred. The repository remains all-rights-reserved by default until an
officer decision is recorded here. No third-party contributions are accepted
until the license is set.

## Consequences

- README states "License: TBD".
- No `LICENSE` file is committed until this ADR is Accepted.
- Do not copy code into or out of the repository until this is resolved.
