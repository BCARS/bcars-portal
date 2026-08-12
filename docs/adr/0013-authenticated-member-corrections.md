# ADR-0013: Member correction suggestions do not require target access

- Status: Accepted
- Date: 2026-08-10

## Context

BCARS wanted a simple way for one member to report stale contact information
about another person, including a spouse or household member, without building
a delegated editing system. Every proposal is reviewed by an officer before it
changes canonical data, so submission can be broader than direct profile access.

Phase 3 planning accidentally changed two parts of that intent. It restricted
authenticated member submissions to records already granted to the account,
recreating the family-helper permission problem, and added a blind public form
for anyone on the internet. Neither change was requested.

## Decision

1. **Correction submission requires authentication.** The portal has no public
   or anonymous correction form or API. Someone without an account contacts an
   officer by telephone, email, mail, or at a meeting, and the officer records
   the request through the existing officer intake operation.
2. **Any provisioned member may suggest a correction about another person.**
   This includes Associate members. The requester does not need a
   `member_access_grants` row for the target and does not need a recorded family
   relationship.
3. **Submission authority is not read authority.** A cross-member request does
   not return the target's profile, contact methods, current values, match
   result, directory eligibility, or identifier. It creates no access grant and
   gives the requester no management or approval capability.
4. **Target hints may be unresolved.** An Associate who cannot browse the
   directory may provide bounded name or call-sign hints for officer triage. A
   Full member may start from a person already visible in the private directory.
   In both cases the officer confirms the canonical target before applying an
   item.
5. **The requester may track only their own requests.** They may see what they
   submitted, the generic review status, and officer-safe disposition text, and
   may withdraw before any item is applied. They do not gain target data through
   status or error responses.
6. **Every item remains a suggestion.** Submission never invokes canonical
   person, contact, preference, relationship, membership, or treasury mutation.
   The existing per-field officer review and sensitivity policy remain the only
   application path.

## Rejected alternatives

- **Anonymous blind intake**: adds internet abuse controls and probing defenses
  for a use case BCARS did not ask the portal to serve.
- **Require a family relationship**: relationship data is useful context but is
  incomplete, optional, and not authorization.
- **Require delegated target access**: recreates the head-of-household permission
  model that reviewed suggestions were intended to avoid.
- **Allow direct member edits**: removes the officer confirmation that makes the
  broad submission boundary safe.

## Consequences

- `bcars-portal-4ux.6` owns authenticated self-or-other request submission as
  well as requester status and withdrawal. Its profile and dues reads remain
  limited to explicitly granted records.
- `bcars-portal-4ux.9` is superseded; no bot trap, anonymous rate limiter, public
  target-probing contract, or public correction UI is built.
- The `public` source value that migration `0009` wrote into the
  `member_change_requests` CHECK constraint stays in the schema as an INERT
  compatibility value. Removing a CHECK constraint in SQLite means rebuilding
  the table, and this one carries a child item table and four indexes, which is
  more churn than the value is worth. It is neutralised instead:
  `changerequests` refuses it at intake (`SourceLegacyPublic`), no route offers
  it, and a regression test asserts no path can create one. Existing audit
  provenance is not rewritten.
- The member submission capability is named for member submission rather than
  `self`, because the target need not be the requester.
- Full-member directory UI may offer “Suggest a correction” on a listed person.
  Associates receive a hint-based form without directory access.
- The Phase 3 smoke test proves an Associate can submit about an ungranted target
  while an anonymous request is rejected and canonical data remains unchanged.
