# ADR-0011: One generic confirmation control; recent-auth removed

- Status: Accepted
- Date: 2026-08-09

## Context

Every operation registered through `httpapi.Register` declared a
`ConfirmationLevel` of `none`, `recent-auth`, or `explicit-confirm`. Nothing
read the field. It was recorded in the operation metadata and then ignored at
request time, so an operation marked `explicit-confirm` was reachable by an
ordinary request exactly like one marked `none`. (The bead reporting this said
the level reached the capability catalog; it did not — the catalog published
capabilities and their operation IDs only, which is why the inert control was
invisible from outside the code.)

Two operations had worked around this by hand: batch posting and payment
correction each required a `"confirm": true` body field their own handlers
checked. Batch abandonment declared `explicit-confirm` and enforced nothing at
all. That split was the real problem — the metadata looked like a guarantee,
was not one, and each handler was free to invent its own convention or forget
to.

`RequiredCapability`, in the same struct, is enforced generically by the authz
middleware, and `Register` panics when it is missing. That is the pattern this
decision matches.

Phase 3 forced the question: `bcars-portal-4ux.3` needs consequential approvals
to be confirmed, and would otherwise have added a third handler-specific
convention.

## Decision

1. **`ConfirmationLevel` is enforced generically** by `AuthzMiddleware`, beside
   the capability check. The declaration in `Register` IS the enforcement.
2. **Intent is stated with the `X-Confirm` header.** Not every consequential
   operation has a body — `POST .../abandon` carries almost nothing — so a body
   field cannot express this uniformly. A header also keeps a control flag out
   of the domain payload, so a request body stays a description of what to
   write.
3. **Only unambiguous affirmatives count**: `true`, `yes`, `1`, case- and
   space-insensitive. A header present but set to `false` is the caller stating
   the opposite, and treating mere presence as consent would make the control
   satisfiable by accident.
4. **A refusal is `428 Precondition Required`**, deliberately distinct from the
   `412` an `If-Match` mismatch produces, so "you did not confirm" and "someone
   else changed the row" are not the same signal.
5. **Confirmation is checked after the capability**, so an unauthorized caller
   is told they lack the capability and learns nothing about which operations
   are consequential.
6. **The refusal is audited** as a denial with reason code
   `missing_confirmation`, so repeated unconfirmed attempts are visible.
7. **`Register` panics on an unknown level**, exactly as it does for a missing
   capability. A level the middleware cannot enforce is worse than no level,
   because the operation metadata reads as a guarantee and is not one.
8. **The hand-rolled `"confirm"` body fields are removed** from batch posting,
   single-payment creation, and correction. Their handlers now pass
   `ConfirmedFrom(ctx)` into the domain rather than a literal `true`: the
   middleware has already refused unconfirmed requests, so it is always true in
   practice, but wiring the real value means the domain guard still refuses if
   the middleware is ever removed or reordered. A literal `true` would turn that
   backstop into decoration.
9. **`recent-auth` is removed.** It was declared on three operations (invite
   user, grant role, revoke role), specified in `docs/phase-1-design.md` as
   requiring a password re-entry within five minutes via a
   `POST /sessions/current/reauth` endpoint, and implemented nowhere. Those
   three operations now declare `explicit-confirm`, which is enforced. Genuine
   step-up re-authentication is tracked as separate work.

## What this control is not

It is not proof that a human confirmed anything. A header is a client assertion
of intent, exactly as the `"confirm": true` body field it replaces was. What it
buys is that a consequential operation cannot be reached by a request that did
not deliberately opt in, and that the opt-in is uniform rather than reinvented
per handler.

Anything that needs actual proof of human presence needs step-up
re-authentication, and this deliberately does not claim to be that.

## Rejected alternatives

- **Delete `ConfirmationLevel` entirely** and leave confirmation to each
  handler. Honest about the status quo, and the bead allowed it. Rejected
  because Phase 3 needs the control, and three handlers inventing three
  conventions is what produced this defect.
- **Implement `recent-auth` now.** It is the right control for privilege
  escalation, but it needs a schema column, a re-authentication endpoint, and
  session plumbing that nothing currently exercises. By the repository's own
  test — does deferring make the eventual fix harder, or only later? — it is
  only later: adding it invalidates no stored data. Leaving it declared and
  inert was the one option not available.
- **Keep confirmation purely in the domain**, as `batches.ErrConfirmationRequired`
  already does. The domain guard stays, and is still what the admin UI's
  checkbox satisfies. But a domain error cannot be declared in operation
  metadata, so the catalog could not describe which operations are
  consequential. The catalog now publishes `confirm_operation_ids` for exactly
  that reason: before this change it named no confirmation control at all, so
  an operator could not see which operations were guarded.

## Consequences

- Every API client calling an `explicit-confirm` operation must send
  `X-Confirm: true`. This is a breaking change to three request bodies, which
  no longer accept a `confirm` property; the OpenAPI document records it.
- The three operations that declared `recent-auth` are now *more* protected
  than before, not less: they previously had no enforcement of any kind.
- The admin UI is unaffected. Its routes do not pass through this middleware,
  and its posting checkbox still satisfies the domain guard.
- New operations must declare a level. There is no default, and an unknown or
  absent value stops the process at startup.
