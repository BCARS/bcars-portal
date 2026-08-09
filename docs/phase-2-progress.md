# Phase 2 Progress and Completion Audit

Status: complete, audited against merged `main`.

This document records what Phase 2 delivered and, more importantly, how each
claim was verified. Bead closure is not evidence of working software: Phase 1
reached a state where every bead was closed and all gates were green while the
production API could not resolve a signed-in principal. Everything below was
re-checked against merged `main`, and the treasury flow was driven through the
shipped binaries rather than a reconstructed router.

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
| `pma.12` | This audit and the treasury smoke test | — |

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
| Worksheet order can seed the batch grid | §7: ordinals, linked batch, zero invented entries |
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
