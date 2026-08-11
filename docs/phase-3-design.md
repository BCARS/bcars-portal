# Phase 3 Design: Reviewed Member Requests and Optional Access

Status: planned under epic `bcars-portal-4ux`.

This document is the standalone design authority for Phase 3. It distills the
member-directory and correction-workflow decisions that were previously present
only in local design material, but no implementation task may depend on
`scratch/`, a parent checkout, another repository, real member data, or a live
external service.

## Outcome

BCARS officers have one review queue for corrections received by telephone,
email, mail, a meeting, an authenticated member, or the blind public form.
Members may optionally use a provisioned password account to see records an
officer explicitly associated with that identity, view a safe dues summary, and
suggest changes. A short-lived recovery link lets a newly provisioned member set
their first password or replace a forgotten one; it is not a separate sign-in
method. Active approved Full members may browse and print a private directory
whose contact values are filtered by the server. Associate members may use their
own optional access but may not browse the directory.

Member and public input never changes canonical records directly. An officer
reviews each proposed item, and only an approved, supported item is applied
through the existing domain service for that field.

## Explicit exclusions

- direct member edits to canonical people, contacts, memberships, coverage, or
  payment records;
- payment disputes, online dues, bank/card data, and member-visible payment
  detail beyond the safe Phase 2 dues-standing summary;
- automatic account claiming based on a matching contact email;
- automatic delegated access from a spouse, household, parent, child, or other
  informational relationship;
- directory access for Associate members, pending applicants, or inactive,
  resigned, deceased, or rejected memberships;
- live SMTP activation, FCC verification, Groups.io integration, real-data
  import, or deployment;
- full interactive visual redesign and polish;
- member applications, elections, files, AI tools, and portal calendar editing.

Production SMTP remains `bcars-portal-8ou`; FCC and Groups.io work remains in
the existing deferred interactive beads. The functional UI may make reasonable
accessible MVP decisions until `bcars-portal-6pz` begins.

## Product and security decisions

1. A contact method is not login authority. An officer explicitly provisions a
   member user and grants it access to named person records.
2. Member accounts are optional and use the existing email-and-password sign-in,
   recovery, and session flow. Provisioning does not choose a password for the
   member. The member uses the enumeration-safe recovery flow to set an initial
   password and later recover it.

   Corrected 2026-08-10 by the repository owner: this does NOT mean an officer
   account and a member account are different things. An individual is a
   member; some members are also officers, and an officer role only adds
   permissions to one identity. Provisioning therefore adds the member role to
   an existing identity rather than demanding a second mailbox
   (`bcars-portal-4ux.4`). The owner then removed passwordless member sign-in in
   `bcars-portal-4ux.15` and ADR-0012, so there is no authentication-method
   downscoping or special rule for officer-members: everyone authenticates the
   same identity with a password.
3. One normalized user email may have access to multiple records, which supports
   a shared household mailbox. The user chooses among only those records after
   sign-in. No unique constraint is added to member contact email values.
4. `users.person_id` remains compatible with existing officer identities. The
   Phase 3 migration creates the authoritative many-to-many access mapping and
   deterministically carries existing links forward; the migration ADR records
   the exact compatibility rule.
5. An informational family relationship and a record-access grant are separate
   facts. Neither creates the other. Explicit access permits viewing the safe
   own-profile model and submitting a reviewed suggestion, never direct editing
   or approval.
6. Every intake channel uses the same request and item model. Source affects
   provenance and triage, not which canonical validation rules apply.
7. Requests use typed, allowlisted items. There is no arbitrary JSON field path,
   generic SQL update, or prose-to-record mutation.
8. Review is per item. Approved, rejected, and needs-verification decisions can
   coexist in one request; the request resolves only when every item is terminal.
9. Supported approvals call the same person, contact, or preference service used
   by officer operations. Preference approvals append events with
   `source=member_request`; they do not rewrite preference history.
10. Sensitive approvals require the checked-in sensitivity policy, a verification
    note, the repository confirmation control, and a reviewer other than the
    requester. Ordinary officer-entered contact corrections may be entered and
    approved by the same officer when policy permits it.
11. The public form is blind. It stores supplied target hints for later officer
    triage, performs no member lookup for the caller, sends no email, and returns
    the same success shape whether a matching person exists or not.
12. Directory eligibility is based on an active approved Full membership, not
    dues standing. Honorary status changes dues, not the underlying Full or
    Associate rights.
13. The directory is a separate safe read model. The broad administrative
    `member.read` and `dues.read` capabilities are never granted to ordinary
    member users.
14. Screen and print directory views consume the same already-filtered domain
    result. A template cannot recover a hidden value because it never receives
    one.
15. Missing and withheld directory contact values both become one
    `not_shared` display state. The UI renders “Not shared” and does not disclose
    whether a hidden contact exists.

## Identity and access model

`users` continues to represent an authenticated identity. The Phase 3 schema
adds revocable access grants from a user to a person. A grant records access kind,
reason, granting officer, grant time, and revocation provenance. It is not
created by joining on `contact_methods.value_norm`.

The ordinary member role receives only capabilities for:

- reading the caller's explicitly granted safe profile;
- submitting and tracking reviewed requests for a granted record;
- attempting the member-directory operation, whose resource policy separately
  verifies an active approved Full membership.

Officers receive explicit capabilities for request intake, review, account/access
provisioning, and relationship maintenance. Existing administrative member and
treasury capabilities remain unchanged.

Authorization loads active access grants and current membership state on every
request. Revoking a grant therefore takes effect within an existing session.
Possessing the member role without an active grant provides no record access.

## Member password setup, sign-in, and recovery

Phase 3 reuses the existing password and recovery implementation. Officer
provisioning creates or reuses one active `users` row and adds the member role,
but never chooses, reads, clears, or replaces a password. A new account with no
password cannot sign in until its user requests the same enumeration-safe
recovery flow already available to officers and sets a password.

Recovery remains rate-limited per source and normalized target. Below the limit
it returns the same response for known, unknown, inactive, and unprovisioned
addresses; only an active provisioned account receives mail. At the limit, the
same 429 contract applies to every address. The recovery token is random,
short-lived, single-use, stored only as a hash, and authorizes setting a new
password. Successful recovery may establish the normal configured session, as
the existing flow does, but there is no reusable `member_sign_in` purpose or
email-link-only session path. Subsequent sign-in uses email plus password.

Members and officers receive the same server-side session and configured cookie
used by both HTML and `/api/v1`; authorization comes from current roles,
capabilities, and active record grants. Revoked users, expired or replayed
recovery links, and accounts with no active record grant fail safely.

Local development, tests, and smoke use the fake or filelog mail sender. This
phase proves the complete flow without production credentials; live relay
delivery is activated interactively by `bcars-portal-8ou`.

The following existing hardening work was completed before the member shell:

- `bcars-portal-6q6.3`: one session cookie for API and HTML;
- `bcars-portal-fmc.21`: the HTML path supplies the same client-address hash;
- `bcars-portal-fmc.20`: one reusable, enumeration-safe attempt limiter.

## Change-request model

A request records its source, optional authenticated requester, optional target
person, supplied requester/contact snapshot, stated relationship, status, and
submission/triage timestamps. A blind request may begin without a canonical
target; an officer links it later without changing what the submitter supplied.

Each item records a typed operation, proposed value, optional target resource and
base version, sensitivity class, review status, reviewer, decision time, reason
or verification note, and the resource/version produced by an approval.

Initial structured operations are:

- change display name or call sign;
- add, update, archive, or choose a primary email, phone, or postal contact;
- change a contact method's directory visibility;
- change the person's ACS/ARES sharing preference;
- add or correct an informational family relationship.

Suggestions about membership lifecycle/type, FCC verification, dues coverage,
payments, honorary status, or an unsupported field remain visible to officers as
an `other` item. They cannot be approved through a generic mutation path; an
officer uses the existing specialized workflow if action is warranted.

### Request state

```text
draft -> submitted -> in_review -> resolved
                    \-> withdrawn

item: pending -> approved | rejected | needs_verification
                    ^                 |
                    +------review-----+
```

Only the authenticated member who submitted a request may withdraw it, and only
before any item has been applied. Officer-entered and public requests are not
withdrawn through the member API.

### Apply semantics

Approval is an idempotent transaction. It checks the current target version,
records the decision, calls an explicit domain adapter, and records the resulting
resource/version. A stale target returns the standard conflict and applies
nothing. Retrying the same approval returns its recorded outcome; the same item
cannot be applied twice. One item's failure cannot silently mark another item as
approved.

`bcars-portal-6q6.1` must settle and enforce the generic confirmation contract
before review operations rely on it. The sensitivity matrix is a committed,
tested artifact, not template logic.

## Member-safe own profile

For each explicitly granted person record, the member model may include:

- display name and call sign;
- the person's own active contact methods;
- current directory visibility for those contacts;
- current ACS/ARES sharing preference, including “not recorded” when appropriate;
- base membership type and safe lifecycle wording;
- current Phase 2 dues standing and paid-through date when present;
- the caller's submitted request history and item decisions.

It excludes payment amounts, methods, references, receipts, batches, coverage
history, officer and treasurer notes, FCC verification notes, other users,
roles/grants, import details, and administrative audit history. Requests against
an ungranted record return 404 so the endpoint does not become an existence
oracle.

## Member directory

The caller must have an active explicit access grant to a person whose current
membership is approved, active, and Full. Associate accounts may still use the
own-profile and request workflow but cannot list or print the directory.

Directory rows include current approved active Full and Associate memberships:

- name and call sign are included when present;
- email and phone values are included only when the latest effective
  `contact_method_visibility_event` permits `full_members`;
- a missing visibility event follows the Phase 1 domain default: current Full
  target -> `full_members`, Associate or unknown target -> hidden;
- imported `import_default` events retain their recorded result and provenance
  until superseded by an officer-approved request;
- postal data is not part of the Phase 3 table or print contract;
- absent and hidden email/phone both serialize as `not_shared`, without a value.

Search covers name and call sign. Sort is stable by name or call sign with a
deterministic identifier tie-breaker. Pagination and print use the same filtered
query/service; the print adapter may request the full filtered result within a
documented club-sized maximum.

## Blind public intake

The public surface accepts bounded name/call-sign hints, optional requester
contact, stated relationship, and a plain-language correction. It does not offer
autocomplete or return match candidates. It creates an unresolved request and
returns generic receipt copy with no member or request identifier useful for
probing.

Abuse controls are local and deterministic: per-source and normalized-target
limits, payload limits, CSRF for the HTML form, safe log redaction, and a simple
honeypot/timing check that requires no external CAPTCHA provider. Intake sends no
mail and never changes canonical data.

## Informational relationships

Relationships use a small checked-in vocabulary such as spouse/partner,
parent/guardian, child/dependent, and household/other. They record direction,
actor, time, version, archival state, and optional restricted context. They do
not appear in the member directory.

A relationship can help an officer understand why one person is suggesting a
change for another. It does not grant profile visibility, request authority, or
approval rights. If BCARS chooses to let a helper submit requests for a related
record, an officer creates a separate revocable access grant.

## API contract

Exact paths are finalized in OpenAPI without changing these resource boundaries.

| Operation family | Capability | Contract purpose |
| --- | --- | --- |
| officer request create/list/detail/triage | `change_request.manage` | Capture every channel and resolve blind target hints. |
| per-item review/apply | `change_request.review` | Decide and apply supported items with concurrency, idempotency, and sensitivity policy. |
| member access grant/revoke | `member_access.manage` | Explicitly associate a provisioned user with person records. |
| relationship CRUD | `relationship.manage` | Maintain informational links independently of access. |
| password sign-in and recovery | public | Reuse the existing enumeration-safe recovery/password/session operations for members and officers. |
| own records/profile/request history | `profile.self.read` | Return only explicitly granted safe data. |
| own request submit/withdraw | `change_request.submit.self` | Suggest changes without canonical mutation. |
| directory list/print feed | `directory.read` plus resource policy | Full-member-only, consent-filtered directory. |
| blind correction submit | public | Store an unresolved suggestion without lookup or disclosure. |

All operations are registered through the generic HTTP layer. Sensitive reads,
intake, triage, review, access changes, relationship changes, recovery use,
directory reads/print, and authorization denials are audited without copying
private field values into log messages.

## UI boundary

The officer MVP provides a request queue, channel-aware request entry, unlinked
public triage, per-item current-versus-proposed review, verification notes,
access grant/revoke, and relationship maintenance.

The member MVP provides password setup/recovery, password sign-in,
granted-record selection, safe profile and dues standing, request
submission/status/withdrawal, preference suggestions, directory navigation for
eligible Full members, and sign-out.

The directory is a plain sortable table with name, call sign, email, and phone.
Printing is a primary action. Hidden and absent contact cells say “Not shared,”
and the print view identifies the Bedford County Amateur Radio Society. A link
from the directory takes the caller to the reviewed correction flow for their own
record.

The UI remains a server-rendered adapter. It does not introduce a JavaScript or
CSS framework, UI-only mutations, or authorization rules present only in a
template.

## Concurrency, retries, and privacy

- Request creation, recovery-link consumption, item approval, access
  grant/revoke, and relationship writes are safe under retries.
- Mutable triage and relationship resources use ETags and the existing stale
  write contract.
- Canonical apply checks the target version captured or refreshed at review.
- Tokens are never stored or logged in raw form.
- Public and member error responses do not reveal person, contact, or account
  existence.
- Directory filtering occurs before DTO/template construction.
- Synthetic fixtures use Bedford County context but no real member information.

## Testing and completion

Every implementation PR runs the repository's seven gates:

```text
make build
make test
make lint
make migration-updown
make sqlc-diff
make openapi-diff
make smoke
```

Phase 3 closes only after `bcars-portal-4ux.13` proves the assembled real
binaries can provision synthetic Full and Associate access, use recovery through
fake/filelog mail to set a member password, sign out and sign back in with that
password, preserve canonical data before review, apply a mixed decision exactly
once, protect stale and self-sensitive review, show only a safe dues summary,
enforce directory eligibility and field filtering, revoke access immediately,
and keep relationships separate from authorization.

Live SMTP delivery and other external systems are activation evidence, not a
dependency of that reproducible code-completion gate.
