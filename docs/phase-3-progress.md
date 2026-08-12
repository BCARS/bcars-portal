# Phase 3 Progress and Completion Audit

Status: complete, audited against merged `main`, and exercised through the
shipped binaries.

This document records what Phase 3 delivered and how each claim was verified.
Bead closure is not evidence of working software — Phase 1 reached a state where
every bead was closed and all gates were green while the production API could
not resolve a signed-in principal, and Phase 2's first completion audit missed
six defects. So the claims below name the test that holds them, and the ones
that matter most were driven through the built binary rather than a
reconstructed router.

## What Phase 3 is about

One sentence: **a member may suggest a correction to anything, may read only
what an officer explicitly granted them, and only an officer may change
canonical data.**

Four facts stayed deliberately separate throughout, and most of the phase's
testing exists to keep them that way:

| Fact | What it confers |
| --- | --- |
| An access **grant** | reading one person record |
| A **relationship** | nothing at all; it is context for a reviewing officer |
| Submitting a **suggestion** | needs neither of the above, only an authenticated member |
| **Applying** a suggestion | needs an officer capability, and is the only path to canonical change |

The tempting collapse is the second row into the first — "she's his wife, so of
course she can see his record." ADR-0010 refuses it, because a marriage ending
does not file a change request, and a household breaking up is exactly when
derived access is most wrong and least likely to be noticed.

## Two replans, recorded rather than hidden

Phase 3 changed shape twice while in flight. Both are closed beads with their
own pull requests, and the plan documents were rewritten rather than patched:

- **`4ux.15` (#85)** — member and officer authentication were unified. There is
  no separate member login: one identity, one email, one password, and a role
  adds permissions to it. An officer who is also a member holds both roles on
  one account (ADR-0012).
- **`4ux.16` (#86)** — anonymous public correction intake was withdrawn and
  replaced by authenticated member suggestions (ADR-0013). `4ux.9`, the bead
  that would have built the public form, is closed as **superseded** rather
  than quietly dropped.

The withdrawal is enforced, not merely intended. Migration `0013` removed
`'public'` from the `member_change_requests.source` CHECK constraint;
`changerequests` refuses the value; and the smoke test asserts that no public
correction route answers on either transport.

## What shipped

| Bead | PR | Delivered |
| --- | --- | --- |
| `4ux.1` | #74 | Request, relationship, access, and capability foundation (migration 0009) |
| `4ux.2` | #80 | Officer-entered request capture and query API |
| `4ux.3` | #81 | Per-field review and canonical apply API |
| `4ux.4` | #84 | Member account and record-access provisioning API |
| `4ux.5` | #87 | Member password onboarding and shared sign-in |
| `4ux.6` | #88 | Member profile, dues, and authenticated correction API |
| `4ux.7` | #82 | Server-filtered Full-member directory API |
| `4ux.14` | #83 | Directory lists every shared contact; filters by membership type |
| `4ux.8` | #90 | Informational relationships (officer CRUD and archival history) |
| `4ux.11` | #89 | Member profile and correction-request MVP UI |
| `4ux.10` | #91 | Officer request review and access-management MVP UI |
| `4ux.12` | #92 | Member directory and print MVP UI |

## How the acceptance claims were verified

Everything in this section is asserted against the running server started from
a directory containing no source, no `go.mod`, and no configuration
(`internal/smoke/harness_test.go`, `requireOutsideRepo`). That harness predates
Phase 3 and is unchanged; it matters more here than usual because Phase 3 added
twelve templates, and a template read from disk rather than the embedded
filesystem passes every package test and fails only under the smoke harness.
`TestPhase3TemplatesAreEmbedded` states that narrow case so a failure names its
own cause.

`TestPhase3ReviewedCorrectionsSmoke` covers the phase end to end:

- **Officer capture, per-field review, and apply.** An officer records a
  telephoned name change and approves the item; the canonical `persons` row
  changes, and that is the only place in the test where it does.
- **Member onboarding and sign-in.** First password is set through the ordinary
  recovery flow — an officer never chooses somebody else's credential — and the
  member lands on a member surface, not the officer dashboard.
- **Granted profile reads.** The granted record reads; an ungranted one returns
  a response byte-identical to a record that does not exist, so the refusal
  cannot be used to test whether a person is on file.
- **Full-member directory filtering.** An eligible Full member browses and
  prints; an Associate is refused screen, print, and API alike, and receives
  nothing that distinguishes refusal from absence.
- **Suggestions from both member kinds.** A Full member and an Associate each
  suggest a correction about somebody they cannot see. Neither submission
  creates a grant or alters any canonical record.
- **No anonymous intake.** An unauthenticated submission is refused; four
  plausible public correction paths answer 404; and the member suggestion form
  redirects an anonymous browser to sign-in.
- **Officer triage of an unresolved hint.** The officer links a member's
  free-text description to a record, and what the member wrote survives beside
  what the officer concluded.
- **Audit history.** `change_request.triage` and `change_request.item.decide`
  appear as successful audited actions, and the applied item records when it
  was applied.
- **Relationship independence, both directions.** The relationship is recorded
  *before* any grant, so nothing that follows can be explained by ordering. It
  opens no record, and revoking the unrelated grant leaves it untouched —
  because it was never access.
- **Revocation is immediate.** A revoked grant closes the record and withdraws
  directory eligibility on the very next request, inside a session already open.

Package-level tests hold the finer distinctions: self-approval refusal, stale
triage and decision conflicts, verification notes on sensitive approvals,
withheld-versus-absent contact rendering, and escaping of member-supplied text.

## Generated artifacts and the gates

`main` is clean under all seven gates: `make build`, `test`, `lint`,
`migration-updown`, `sqlc-diff`, `openapi-diff`, `smoke`. `docs/openapi.json`
and `docs/capability-catalog.json` are regenerated from the built binary and
committed, so a drifted contract fails CI rather than surviving to a reader.

Every Phase 3 capability is registered against operations:

| Capability | API operations |
| --- | --- |
| `change_request.manage` | 4 |
| `change_request.review` | 1 |
| `change_request.submit.member` | 4 |
| `member_access.manage` | 5 |
| `relationship.manage` | 5 |
| `profile.self.read` | 2 |
| `directory.read` | 1 |

Four capabilities in the catalog still have no API operation:
`integration.config.write`, `system.admin`, `notes.write.treasurer`, and
`notes.read.treasurer`. All four predate Phase 3 and none is a Phase 3
commitment; they are recorded here so the gap is known rather than discovered.

## Deliberately not done

These are open beads, not oversights:

- **`4ux.17`** — relationship change-request items (`relationship.add`,
  `relationship.correct`) are captured but not appliable; their adapters are
  still `AdapterNone`. An officer sees the proposal and uses the ordinary
  workflow.
- **`4ux.18`** — officer triage takes a typed record number rather than
  offering a search-as-you-type picker. Officer-side only: the member
  suggestion form must keep having no lookup, since that is what stops it
  answering "is this person a member".
- **`4ux.19`** — the directory domain service sorts by name or call sign, but
  the `/directory` HTTP operation exposes no sort parameter. The UI sorts; the
  documented API does not.
- **`6pz`** — full interactive visual design and polish, deferred by plan. The
  Phase 3 UI is functional MVP: plain tables, accessible labels, and no
  framework.

External integrations, real-data import, live SMTP, FCC verification, and
deployment packaging remain out of scope for Phase 3 and are tracked elsewhere,
principally under `6q6`.

## Standing constraint

No real member data is in this repository. Every fixture above is synthetic,
and the smoke test builds its own database in a temporary directory it deletes
afterwards.
