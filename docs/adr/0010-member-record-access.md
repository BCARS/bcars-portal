# ADR-0010: Member record access is an explicit grant, not an inferred link

- Status: Accepted; authentication and correction context amended 2026-08-10
- Date: 2026-08-09

## Context

Phase 3 (`docs/phase-3-design.md`) gives members an optional provisioned
account. Members and officers now use the same password and recovery flow under
ADR-0012; that authentication choice is independent of the question the rest of
the phase depends on: given an authenticated user, which person records may they
see?

Three answers were already available in the schema, and all three are wrong:

- **`users.person_id`**: a nullable one-to-one link added in `0001_init.sql` for
  officer identities. It cannot express a shared household mailbox, and it is
  not revocable — clearing it destroys the identity link rather than recording
  that access ended.
- **`contact_methods.value_norm`**: matching a sign-in address against a member's
  recorded email would let anyone who learns a member's address, or who is
  simply added to a shared address, claim that record. A contact method is a
  way to reach someone, not proof of who they are.
- **A family relationship**: Phase 3 also introduces informational relationships
  so an officer can see why one person is reporting a change for another.
  Treating a spouse or parent link as authority would grant access to records
  the club never decided to share.

BCARS is 20 to 30 members. Households genuinely share one email address, and
some members will never want an account at all. Whatever we choose has to
survive a shared mailbox without inventing separate identities for people who
do not want them.

## Decision

1. **`member_access_grants` is the sole authority.** A row maps one user to one
   person, with access kind, reason, granting officer, grant time, and
   revocation provenance. Authorization asks this table and nothing else.
   `ListActiveAccessGrantsForUser` and `CountActiveAccessGrant` are the only
   queries that answer the access question, and neither joins `users`,
   `contact_methods`, or `person_relationships`.
2. **One user may hold several active grants.** That is how a shared household
   mailbox reaches more than one record; after sign-in the user chooses among
   exactly the granted records. No unique constraint is added to member contact
   email values, because sharing an address is normal and not a data error.
3. **`users.person_id` stays, and stops being an authority.** It remains the
   officer identity link that existing code reads for display and profile
   purposes. It is not consulted for access. Migration `0009` carries every
   non-null link forward as exactly one `self` grant, attributed to no officer
   because none acted, and a guard table aborts the migration if any linked user
   does not end with exactly one active grant. The column is deliberately not
   dropped: dropping it would break existing officer identity reads for a
   benefit the grant table already provides.
4. **Access is loaded per request, not per session.** Revoking a grant therefore
   takes effect inside a session that is already open, without a sign-out.
5. **Revocation is append-only in spirit.** A revoked grant keeps its row and
   its provenance. Re-granting later inserts a new row rather than clearing
   `revoked_at`, so "who could see this record, and when" stays answerable. A
   partial unique index permits at most one *active* grant per pair.
6. **Relationships confer nothing.** `person_relationships` carries no user
   reference other than actor provenance and no foreign key into
   `member_access_grants`. An authenticated member needs no relationship or
   access grant merely to submit an officer-reviewed correction suggestion about
   another person. A separate revocable grant is required only to read that
   person's safe profile.
7. **The member role is a member role.** `0004_seed_roles.sql` classified
   `member` as kind `officer` when no member-facing surface existed. Migration
   `0009` reclassifies it as `member`; Phase 3 grants `profile.self.read`,
   `change_request.submit.member`, and `directory.read` — never `member.read` or
   `dues.read`, which are the broad administrative reads. The initial
   `change_request.submit.self` seed is renamed by `bcars-portal-4ux.6` because
   ADR-0013 deliberately permits suggestions about another person.

## Rejected alternatives

- **Widen `users.person_id` into a join table and drop the column**: the same
  destination with a larger blast radius. Every existing officer-identity read
  would have to change in the same migration that introduces the member
  boundary, and a mistake in either half would be hard to attribute.
- **Auto-claim a record when the sign-in address matches a contact method**:
  the friendliest onboarding and the worst failure mode. A shared or recycled
  address silently grants access to someone else's record, and the club never
  makes a decision it could later audit.
- **Derive access from relationships with an officer opt-out**: default-allow
  in a system whose authorization model is default-deny. It also conflates two
  facts the club states separately: who is related, and who may see what.
- **Add passwordless member sign-in links**: this was the original Phase 3 plan,
  but it made authentication method affect authorization as soon as one identity
  held both member and officer roles. The owner chose one simpler password and
  recovery model for everyone rather than session downscoping, refusing links
  for officer-members, or treating a routine email link as an officer login.

## Consequences

- A member with no grant sees nothing, and that is the correct default. Officer
  provisioning (`bcars-portal-4ux.4`) is a required step, not a convenience.
- Provisioning does not choose a password. A new member uses the existing
  recovery flow to set one, then signs in through the same password/session
  path as an officer. This changes no access-grant rule in this ADR.
- Correction submission and record reading deliberately ask different
  authorization questions. ADR-0013 permits any authenticated member to submit
  a reviewed suggestion about another person; this table remains the sole
  authority for whether the requester may read that person's profile.
- Anything that needs "which records may this user see" must call the access
  queries. A future join back to `users.person_id` would reintroduce exactly the
  authority this ADR removes; `TestOnlyAnActiveGrantConfersAccess` fails if it
  does, because it revokes the grant while leaving `users.person_id` in place.
- Directory eligibility is a separate question from access. Holding
  `directory.read` only lets a caller attempt the listing; the resource policy
  in `bcars-portal-4ux.7` checks for an active approved Full membership, which
  is how an Associate holds the capability and is still refused.
- The backfill is re-runnable. `users.person_id` is never modified on the way
  up, so a down/up round trip reproduces the same grants without duplicating
  them.
