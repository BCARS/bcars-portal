# Phase 2 Progress and Completion Audit

Status: complete, audited against merged `main`, and reconciled after review.

This document records what Phase 2 delivered and, more importantly, how each
claim was verified. Bead closure is not evidence of working software: Phase 1
reached a state where every bead was closed and all gates were green while the
production API could not resolve a signed-in principal. Everything below was
re-checked against merged `main`, and the treasury flow was driven through the
shipped binaries rather than a reconstructed router.

## Reconciliation findings at a glance

Phase 2 is complete, but its first completion audit was not sufficient on its
own. The reconciliation epic, `bcars-portal-9zm`, separated four independently
testable defects from the final documentation audit and merged each fix through
its own pull request:

- a worksheet-linked batch stored `worksheet_run_id` but the batch surface did
  not consume it, so the saved worksheet order never guided data entry;
- worksheet snapshots selected a lexically maximal active contact instead of
  the contact explicitly marked primary;
- the admin worksheet form silently replaced an invalid `as_of` value with
  today even though the API rejected the same input; and
- single-payment idempotency hashed the ledger entry but omitted the effective
  batch label, allowing a materially changed request to replay as identical.

The review also found that none of the completed treasury pages was reachable
from the normal application navigation (`bcars-portal-6q6.4`). The preceding
completion audit caught a separate production-assembly defect: the admin UI did
not receive the configured password pepper, making every Phase 2 page unusable
in a real peppered deployment. All six problems are fixed on `main`.

Three production-hardening concerns remain deliberately non-blocking and are
tracked under `bcars-portal-6q6`: unenforced generic confirmation metadata
(`6q6.1`), silent loss of an unparseable imported Current Until value (`6q6.2`),
and separate API/admin session cookies (`6q6.3`). External integrations,
deployment packaging, real-data import, and interactive visual polish remain
deferred rather than hidden inside the completed Phase 2 claim.

The central audit lesson is to test the named user-visible property through the
surface that consumes it. Testing ordered worksheet rows and an empty linked
batch separately did not prove that the batch presented those rows in order.
The corrected smoke test reads the members and their order from the shipped
batch page, and its focused regression test was verified to fail when that
consumer behavior was removed.

## Delivered

| Bead | Delivered | PR |
| --- | --- | --- |
| `pma.1` | Ledger schema, ADR-0007 legacy backfill, treasury capabilities | #54 |
| `pma.2` | Derived dues standing, rates, suggestions, coverage adjustments | #55 |
| `pma.3` | Draft payment batches with server totals and draft isolation | #56 |
| `pma.4` | Atomic posting and the single-payment convenience contract | #57 |
| `pma.5` | Immutable posted-payment corrections | #58 |
| `pma.6` | Treasury history, receipts, activity, formula-safe CSV exports | #60 |
| `pma.7` | Durable renewal worksheet snapshots | #61 |
| `pma.8` | Treasurer status pages and the single-payment screen | #62 |
| `pma.9` | Batch grid, posting, posted review, correction dialog | #63 |
| `pma.10` | Printable renewal worksheet | #64 |
| `pma.11` | BCARS county identity reconciliation | #53 |
| `pma.13` | Import dues and lifetime honorary cutover | #59 |
| `pma.12` | This audit and the treasury smoke test | #65 |

## Post-phase reconciliation (`bcars-portal-9zm`)

A review after the epic closed found four defects the original audit missed.
They are recorded here rather than quietly fixed, because the first audit
claimed more than it had proved.

| Bead | Defect | PR |
| --- | --- | --- |
| `9zm.1` | The batch surface ignored `worksheet_run_id`, so a treasurer could not work down the linked sheet; the handoff had no idempotency key | #66 |
| `9zm.2` | `GetPrimaryContact` never read `is_primary`, so a worksheet could print a secondary address | #67 |
| `9zm.3` | The admin form silently replaced an invalid `as_of` with today, while the API rejected it | #68 |
| `9zm.4` | The single-payment fingerprint omitted the label, so a changed label replayed instead of conflicting | #70 |
| `6q6.4` | None of the Phase 2 pages was linked from the header or dashboard | #69 |

### What the first audit got wrong

The worksheet criterion is the instructive one. `TestTreasurySmoke` asserted
the worksheet rows were ordered and, separately, that the linked batch was
empty. Both passed. Neither proved the property the criterion actually claims —
that worksheet order seeds the grid — because nothing read the order *from the
batch*. Testing the pieces and reporting the property is how an audit reaches a
confident wrong answer.

The smoke test now reads member names and their order from the batch page of
the shipped binary, and `TestLinkedBatchPresentsSheetInOrder` was verified to
fail when the consumer ignores the link.

## How each completion criterion was verified

The epic's criteria are proved by `TestTreasurySmoke` in `internal/smoke`,
which builds both binaries, starts the server as a process, and drives it over
HTTP. Section numbers below refer to that test.

| Criterion | Verified by |
| --- | --- |
| A draft batch does not change member standing | §1: two rows added, both members still `unknown` |
| Posting changes every intended standing exactly once | §3: both members `current`, two payments |
| A stale or invalid batch posts nothing | §2: stale ETag 412 and unconfirmed 422, standing unchanged |
| Correcting $400 to $40 preserves the original | §4: chain of three, original still 40000, net 8000 |
| ...without changing paid-through unless requested | §4: coverage event count unchanged at 1 |
| A treasurer can retrieve details and export them | §5: CSV decodes and contains both amounts |
| A non-treasurer reads safe standing but is denied detail | §6: standing 200, four detail paths 403, no leakage |
| Worksheet order can seed the batch grid | §7: the batch reports its run id, and §8 reads the members and their order from the shipped batch page |
| A legible print view | §8: the shipped binary serves the sheet with club identity and print rules |
| No dependency on `scratch/`, a sibling checkout, or real data | Audit below |

## Audit findings

### Fixed in this bead

**The admin UI could not authenticate anyone in a peppered deployment.**
`httpapi.NewRouter` built the admin UI's auth service with a nil pepper while
the API used the configured one, so the two disagreed about every stored
password hash. Production requires a pepper — `-allow-empty-pepper` is
development only — which means the entire admin UI login was broken in any
real deployment, and with it every Phase 2 UI bead.

Package tests could not have caught this: they construct the web handler
directly and pass whatever pepper they choose. Only signing in through the
login form of the shipped binary surfaced it. `Pepper` is now part of
`web.HandlerConfig` and `httpapi.Config`, supplied by the production assembly,
and the smoke test signs in through the real login form so a regression fails
the gate.

### Also fixed during reconciliation

A scripted edit on the `9zm.3` branch corrupted a route pattern, turning
`GET /admin/treasury/worksheets` into a pattern containing a raw tab. It still
registered, but the real path then fell through to the `GET /admin/` dashboard
pattern and its far weaker `session.self.read`, so a page guarded by
`dues.worksheet.manage` became readable by any signed-in member. Nothing failed
to compile; the existing denial test caught it before merge, and merged `main`
was never affected. `TestAdminRoutePatternsAreWellFormed` now rejects control
characters, empty segments, duplicates, and missing capabilities across the
whole route table.

### Tracked, not blocking

- **`bcars-portal-6q6.1`** — `ConfirmationLevel` is declared on every operation
  and enforced nowhere. Posting and correction work around it with an explicit
  `confirm` body field, so nothing is unsafe; the metadata simply does not mean
  what it appears to.
- **`bcars-portal-6q6.2`** — an unparseable `Current Until` passes normalization
  as though it were an ordinary date. The import cutover skips it rather than
  aborting, but the officer is not told the value was dropped.
- **`bcars-portal-6q6.3`** — the API and the admin UI issue different session
  cookies from the same binary. Each surface works alone; a browser touching
  both must sign in twice.

None of these blocks a Phase 2 completion criterion.

### Checks that came back clean

- No unconditional `ErrNotImplemented` remains: all 78 occurrences are
  nil-dependency guards that production wiring satisfies.
- No source, migration, or build file references `scratch/`, `bmp-archive`, a
  parent directory, or the ignored `data/` tree.
- Every treasury capability in the catalog is bound to at least one registered
  operation, except the two Phase 1 treasurer-note capabilities that predate
  this phase.
- `docs/openapi.json` and `docs/capability-catalog.json` regenerate clean.

## Deliberately out of scope

Carried forward unchanged from the design's exclusions: online payment
processing, emailed receipts, member sign-in and self-service, Groups.io and
FCC integrations, donations, production packaging (`bcars-portal-6q6`), and the
full interactive visual-design pass (`bcars-portal-6pz`).

Two further deferrals decided during Phase 2:

- **The December-31 rule is not enforced.** The owner decided on 2026-08-09
  that the server records what actually happened, including off-cycle and
  historical dates. Making the year-end enforceable becomes per-club
  configuration if it is ever wanted.
- **Worksheet print fidelity is asserted, not rendered.** A headless test
  cannot print. The tests assert the print rules are present — letter portrait,
  repeating headers, unsplit rows, 12pt text, no app chrome — and a real sheet
  should go through a printer during the interactive design session.
