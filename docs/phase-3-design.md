# Phase 3 Design: Reviewed Member Requests and Optional Access

Status: shipped under epic `bcars-portal-4ux`; the correction workflow was
reshaped by [ADR-0014](adr/0014-corrections-are-edits-and-notes.md) under epic
`bcars-portal-ssz`.

ADR-0014 wins where this document and it disagree. It narrows
[ADR-0013](adr/0013-authenticated-member-corrections.md) rather than replacing
it: submission still requires authentication, there is still no public form, a
member may still report about a person they cannot see with no grant and no
recorded relationship, and members still cannot write canonical data. What
changed is the SHAPE of a correction, described under "Corrections after
ADR-0014" below.

This document is the standalone design authority for Phase 3. It distills the
member-directory and correction-workflow decisions that were previously present
only in local design material, but no implementation task may depend on
`scratch/`, a parent checkout, another repository, real member data, or a live
external service.

## Outcome

BCARS officers have one review queue for corrections received by telephone,
email, mail, a meeting, or an authenticated member.
Members may optionally use a provisioned password account to see records an
officer explicitly associated with that identity, view a safe dues summary, and
suggest changes. A short-lived recovery link lets a newly provisioned member set
their first password or replace a forgotten one; it is not a separate sign-in
method. Active approved Full members may browse and print a private directory
whose contact values are filtered by the server. Associate members may use their
own optional access but may not browse the directory. Any authenticated member,
including an Associate, may report a correction about another person, as a
note, without receiving profile access to that person.

Member input never changes canonical records directly. An officer reviews each
proposed item, and only an approved, supported item is applied through the
existing domain service for that field.

## Explicit exclusions

- direct member edits to canonical people, contacts, memberships, coverage, or
  payment records;
- payment disputes, online dues, bank/card data, and member-visible payment
  detail beyond the safe Phase 2 dues-standing summary;
- automatic account claiming based on a matching contact email;
- automatic delegated access from a spouse, household, parent, child, or other
  informational relationship;
- unauthenticated or anonymous correction submission through the portal;
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
   own-profile model, never direct editing or approval. It is not required to
   report a correction about another person.
6. Every supported officer or authenticated-member intake channel uses the same
   request and item model. Source affects provenance and triage, not which
   canonical validation rules apply.
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
11. Any authenticated member, including an Associate, may report a correction
    about another person without an access grant to that record. Since ADR-0014
    such a report is a NOTE: it carries no structured item, because nothing
    could apply one against a record the sender may not see. Submission accepts
    bounded name/call-sign hints for officer triage and returns no lookup
    result, current value, or new access. Anonymous callers cannot submit.
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
- submitting and tracking the caller's reviewed requests about themselves or
  another person, without gaining read access to the target;
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
submission/triage timestamps, and — once an officer has finished with it — who
resolved it and optionally what they did. An authenticated cross-member report
may begin with only bounded name/call-sign hints; an officer links it later
without changing what the submitter supplied. Since ADR-0014 that link is a
filing aid rather than a precondition for applying anything, because such a
report carries no item to apply. Member submission never performs a target
lookup for the caller.

Each item records a typed operation, proposed value, optional target resource and
base version, sensitivity class, review status, reviewer, decision time, reason
or verification note, and the resource/version produced by an approval.

Initial structured operations are:

- change display name or call sign;
- add, update, archive, or choose a primary email, phone, or postal contact;
- change a contact method's directory visibility;
- change the person's ACS/ARES sharing preference;
- add or correct an informational family relationship.

Changes proposed to membership lifecycle/type, FCC verification, dues coverage,
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
before any item has been applied. Officer-entered requests are not withdrawn
through the member API.

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

## Corrections after ADR-0014

A correction takes one of three shapes, decided by what the sender can already
see.

**An officer editing a record edits it.** No proposal and no queue. The review
queue exists for people who cannot write to the record, not as ceremony around
people who can.

**A member who can see a record gets an edit form for it** — their own record,
or one an officer granted them. The form mirrors the record: name, call sign,
and the current value of each contact detail, each editable, plus a note box.
Submitting creates ONE request carrying one item per field the member actually
changed; unchanged fields propose nothing. Existing values only — adding or
removing a contact detail is described in the note box and an officer does it,
because a form that creates and archives rows needs a review screen that can
apply creations and archives, and correcting a wrong digit does not need that.

Each item carries the version its sender was looking at, so an approval weeks
later is refused as a conflict rather than quietly undoing an officer's more
recent edit.

**Everything else is a note.** A member reporting something about a record they
cannot see writes what they know and that is the whole submission: no items, no
proposed values, and nothing to resolve a target for. An officer reads it and
edits the record, which is what they would do with the same sentence heard at a
meeting. The API refuses a structured item on such a submission rather than
converting it silently, so a client that believes it proposed a change is told
it did not.

This replaced a form that asked which single field was wrong and produced an
item naming no record. Nothing could ever apply one: linking the REQUEST did not
give the ITEM a target, so an officer who linked it was told to link it
(`bcars-portal-3la`). Items now exist only where the submitter could already
read the record, so an item always arrives with its target known.

### Review

Review is one form over the whole request, not a decision per field. Each
proposed change appears beside the value currently on the record, editable, with
a tick to include it; one action applies everything ticked. An officer who can
see that a member mistyped one character corrects it and approves, instead of
rejecting and asking the member to send the whole thing again.

What the member proposed is never rewritten. The applied value and the reviewing
officer are recorded next to it, so a member reading their own correction
afterwards sees both what they asked for and what was done, and those may
differ.

Unticking a change leaves it PENDING rather than rejecting it: a member is owed
a reason for a refusal and an empty checkbox is not one. Declining is a separate
action carrying one reason for everything still open. Each change applies in its
own transaction and partial success is reported, so a stale target on the third
change does not undo the first two.

Per-field sensitivity policy is unchanged in intent: a sensitive change still
requires a verification note, still cannot be approved by the member who
requested it, and a replayed apply still returns the recorded outcome rather
than applying twice.

### Closing a request

A request resolves on its own once every item is terminal. A note has no items,
so nothing empties its queue slot: an officer marks it done explicitly, and that
records who did it and, optionally, one line about what they did. The queue
opens on what is still outstanding; finished work is behind a filter rather than
mixed into the pile.

Marking done is refused while a proposed change is still pending, because
closing it would tell the member their correction was dealt with when no officer
ever decided it.

## Authenticated cross-member reports

A provisioned member may report a correction about another person without an
active access grant to that person's profile. This is submission authority, not
delegated management: the caller receives no profile data, current contact
values, match candidates, or directory access from the request.

Full members may start from a person visible in the private directory.
Associates cannot browse that directory and use the same form, describing the
person in their own words. The response shows the requester only what they
submitted and a generic review status; the officer's conclusion about who it
concerned is never echoed back, nor is the value an officer applied to a record
the caller may not see. Anonymous callers use an offline channel through an
officer; there is no public correction form or API.

Such a report carries no structured item (see "Corrections after ADR-0014"),
so an officer resolves it by editing the record and marking the note done rather
than by triaging an item onto a target.

## Informational relationships

Relationships use a small checked-in vocabulary such as spouse/partner,
parent/guardian, child/dependent, and household/other. They record direction,
actor, time, version, archival state, and optional restricted context. They do
not appear in the member directory.

A relationship can help an officer understand why one person is suggesting a
change for another. It does not grant profile visibility or approval rights, and
it is not required for request submission. Any authenticated member already may
report a correction about another person; a separate access grant is needed
only to read that person's safe profile, and an edit form is offered only for a
record the caller may already read.

## API contract

Exact paths are finalized in OpenAPI without changing these resource boundaries.

| Operation family | Capability | Contract purpose |
| --- | --- | --- |
| officer request create/list/detail/triage | `change_request.manage` | Capture every channel and link a request to the person an officer decides it concerns. Linking is provenance for the request; it does not give an item a target, and an item that names no record is a note (see "Corrections after ADR-0014"). |
| per-item review/apply | `change_request.review` | Decide and apply supported items with concurrency, idempotency, and sensitivity policy. A reviewer may supply the value to apply when it differs from the one proposed; the proposal is not overwritten. |
| mark a request done | `change_request.review` | Close a request an officer has finished with, recording who and optionally what they did. Refused while an appliable item is still pending. |
| member access grant/revoke | `member_access.manage` | Explicitly associate a provisioned user with person records. |
| relationship CRUD | `relationship.manage` | Maintain informational links independently of access. |
| password sign-in and recovery | public | Reuse the existing enumeration-safe recovery/password/session operations for members and officers. |
| own records/profile/request history | `profile.self.read` | Return only explicitly granted safe data. |
| member request submit/withdraw | `change_request.submit.member` | An authenticated member may propose changes to a record they may see, or send a note about anyone else, without canonical mutation or target read access. A submission about somebody else carries only `other` items. |
| directory list/print feed | `directory.read` plus resource policy | Full-member-only, consent-filtered directory. |

All operations are registered through the generic HTTP layer. Sensitive reads,
intake, triage, review, access changes, relationship changes, recovery use,
directory reads/print, and authorization denials are audited without copying
private field values into log messages.

## UI boundary

The officer MVP provides a request queue that opens on outstanding work,
channel-aware request entry, linking a request to the person it concerns, one
review form showing each proposed change beside the record's current value with
a per-field tick and a single apply, marking a note done, verification notes,
access grant/revoke, and relationship maintenance.

The member MVP provides password setup/recovery, password sign-in,
granted-record selection, safe profile and dues standing, request
submission/status/withdrawal, proposed preference changes, directory navigation for
eligible Full members, a correction-about-someone-else entry point for Full and
Associate members, and sign-out.

The directory is a plain sortable table with name, call sign, email, and phone.
Printing is a primary action. Hidden and absent contact cells say “Not shared,”
and the print view identifies the Bedford County Amateur Radio Society. A link
from the directory may start a note about that listed person. Associates send
the same note without gaining directory access, describing the person in their
own words.

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
- Member submission responses do not reveal target person, contact, or account
  existence; anonymous submission is not registered.
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
allow an Associate to send a note about a person they cannot see while
rejecting anonymous submission, and keep relationships separate from
authorization.

Live SMTP delivery and other external systems are activation evidence, not a
dependency of that reproducible code-completion gate.
