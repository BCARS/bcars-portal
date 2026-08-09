# Phase 2 Design: Treasurer Workflow

Status: proposed for implementation

This document is the standalone design authority for Phase 2. It incorporates
the API-relevant decisions from the local treasurer mockups, but no implementation
task may depend on `scratch/` or another checkout being present.

## Outcome

An authorized treasurer can record cash and check payments singly or in a saved
batch, post a batch atomically, correct a posted payment without erasing history,
adjust dues paid-through independently of money received, find members by derived
dues standing, export treasury records, and print a renewal worksheet.

The API and domain services are authoritative. The Phase 2 UI is a thin,
server-rendered adapter over the same operations and authorization rules.

## Explicit exclusions

- online payment processing, bank/card credentials, and accounting-system sync;
- emailed receipts (stable printable receipt identifiers are included);
- member sign-in, self-service, and the member directory (Phase 3);
- Groups.io and FCC integrations;
- donations or other non-dues income unless separately approved;
- production packaging and hardening tracked by `bcars-portal-6q6`;
- the later full interactive visual-design pass in `bcars-portal-6pz`.

## Product decisions carried forward

1. The club dues year ends December 31, and suggestions default to that date.
   The server does **not** reject an off-cycle paid-through date (owner decision,
   2026-08-09). Recording what actually happened outranks enforcing the club
   convention, and historical imported dates are preserved unchanged. If the
   year-end is ever enforced it becomes per-club configuration — anniversary
   date versus end of year — which is deliberately out of scope for Phase 2.
2. Money received and coverage granted are separate facts. The server never
   derives `paid_through` from `amount_cents` or note text.
3. Suggestions are optional display data. A client submits the chosen
   `paid_through` explicitly, and a typed value is never silently replaced.
4. Batches are first-class draft objects. Draft entries do not create payments,
   coverage events, or a changed dues standing.
5. Posting is one atomic transition. Either every valid entry becomes an
   immutable payment plus its explicit coverage event, or none does.
6. Open entries may be edited. Posted payments may not. A correction appends a
   reversal and replacement, requires a reason, and preserves the original.
7. Coverage corrections append a new adjustment event. If a payment correction
   changes only money fields, the existing coverage decision remains effective.
8. UI copy uses plain language: “Dues paid through,” “Dues waived,” “Fix this,”
   and “What happened to this batch.” Ledger terminology stays in code and docs.
9. The chosen batch UI is a spreadsheet-style grid with persisted defaults,
   server-calculated totals, Enter-to-add progressive enhancement, and a single
   posting action.
10. The printed renewal worksheet is a supported workflow, not decoration. Its
    generation time, row order, and filters are durable so a later batch can use
    the same order and show which rows have since been entered.

## Domain model

All money uses integer cents. Dates use ISO `YYYY-MM-DD`; timestamps use UTC.
Posted financial and coverage history is append-only.

| Resource | Purpose and important fields |
| --- | --- |
| `dues_rates` | One amount per effective calendar year: `year`, `amount_cents`, actor/time, `version`. It informs suggestions only. |
| `payment_batches` | Draft/posting boundary: label, `open|posted|abandoned`, persisted default amount and paid-through, optional worksheet source, opener/poster/timestamps, `version`. |
| `payment_batch_entries` | Mutable rows belonging to an open batch: membership, sequence, amount, method, reference, received date/officer, intended paid-through, treasurer note, `version`. |
| `payments` | Immutable posted ledger rows. Includes signed amount, method/reference, received and entering officers, receipt code, batch, entry kind, and correction linkage. |
| `payment_corrections` | Immutable grouping of original, reversal, and replacement payments with required reason, actor, and timestamp. |
| `coverage_events` | Append-only paid-through decisions: membership, paid-through, reason kind, optional payment/import source, decision actor/time, and optional superseded event. |
| `dues_worksheet_runs` | Saved worksheet request: generated time, as-of date, filters, sort, selected columns, and creator. |
| `dues_worksheet_rows` | Stable row order and the minimum snapshot needed to reproduce what was printed and relate later batch entries to it. |
| `idempotency_records` | Actor/operation/key, request hash, resulting resource, and creation time for exactly-once retryable writes. |

Detailed schema belongs in the migration bead. Constraints must enforce valid
states, foreign keys, non-zero posted ledger amounts, terminal batch states, and
one correction chain without making ordinary queries reconstruct mutable history.

### Batch state machine

```text
open ──post atomically──> posted
  └────abandon──────────> abandoned
```

Only `open` batches accept entry or default changes. `posted` and `abandoned`
are terminal. “Save and finish later” requires no transition because every open
batch mutation is already persisted.

Every entry mutation increments both the entry version and batch version.
Posting requires the current batch ETag, preventing a stale browser from posting
while another officer is editing a row.

### Posting transaction

For each entry, posting creates:

1. one positive immutable payment with a stable receipt code; and
2. one coverage event containing the treasurer-selected paid-through date.

The transaction then marks the batch posted with actor and timestamp. Totals are
always calculated by the server. Posting is idempotent: retrying the same request
returns the original result and cannot duplicate payments or coverage.

The single-payment operation calls this same application primitive with a
server-created one-row batch. It is a convenience contract, not a second ledger
implementation.

### Corrections

A correction request supplies replacement money fields, an explicit
paid-through value, the current payment ETag, and a required plain-language
reason. In one transaction the service:

1. appends a negative reversal of the currently effective payment;
2. appends the positive replacement;
3. records the correction linkage, actor, timestamp, and reason;
4. appends a superseding coverage adjustment only when paid-through changed;
5. returns the new net batch totals and member standing.

There is no PATCH or DELETE for posted payments. Repeated corrections target the
currently effective replacement while retaining the entire chain.

## Derived dues standing

Standing is calculated as of an explicit date so tests, worksheets, and reports
are deterministic.

| Status | Meaning |
| --- | --- |
| `honorary_waived` | An honorary grant is active as of the requested date; the response also states fixed-term or lifetime. |
| `current` | The latest effective coverage event is paid through on or after the as-of date. |
| `expiring` | Current, but paid-through falls within the requested warning window. This is a query classification, not stored state. |
| `expired` | The latest effective coverage event predates the as-of date. |
| `unknown` | No effective coverage event exists. |

The response also carries underlying Full/Associate membership rights. Honorary
waiver changes dues standing, never base membership rights. Payment amount,
method, reference, and treasurer notes are absent from this safe summary.

## API contract

Paths are proposed under `/api/v1`; the OpenAPI bead may refine names without
changing the resource boundaries or state transitions.

| Operation | Capability | Contract purpose |
| --- | --- | --- |
| `GET /dues-rates` | `dues.read` | List effective-year rates. |
| `PUT /dues-rates/{year}` | `dues.rate.manage` | Create or revise a rate with `If-Match`. |
| `GET /memberships/{id}/dues-standing` | `dues.read` | Safe derived summary as of a supplied/default date. |
| `GET /dues-standing` | `dues.read` | Paginated current/expiring/expired/waived/unknown views with search and stable sort. |
| `GET /dues-suggestions` | `dues.read` | Return non-binding calendar/rate-based choices and explanations. |
| `GET /memberships/{id}/coverage-events` | `coverage.read` | List the append-only coverage history. |
| `POST /memberships/{id}/coverage-events` | `coverage.adjust` | Add an independent paid-through adjustment with required reason. |
| `GET, POST /payment-batches` | `payment.read`, `payment.batch.manage` | List and open batches. |
| `GET, PATCH /payment-batches/{id}` | `payment.read`, `payment.batch.manage` | Read a batch or change open metadata/defaults. |
| `POST /payment-batches/{id}/abandon` | `payment.batch.manage` | Terminal audited abandonment of an open batch. |
| entry CRUD beneath `/payment-batches/{id}/entries` | `payment.batch.manage` | Add/edit/remove versioned draft rows and return server totals. |
| `POST /payment-batches/{id}/post` | `payment.post` | Idempotent, ETag-guarded atomic posting. |
| `POST /payments` | `payment.post` | Record and post one payment through the shared posting primitive. |
| `GET /payments/{id}` and member payment history | `payment.read` | Treasury-only money, references, receipts, and correction chain. |
| `POST /payments/{id}/corrections` | `payment.correct` | Append reversal/replacement and any changed coverage decision. |
| treasury and batch CSV exports | `payment.export` | Deterministic, formula-safe CSV with applied filters in metadata. |
| worksheet run create/list/detail | `dues.worksheet.manage` | Persist an as-of snapshot, filters, columns, and stable row order. |

Member autocomplete reuses canonical member search and returns only identifiers,
name/call sign, and safe dues standing. It never exposes payment details.

## Authorization

- Treasurer receives every Phase 2 treasury capability.
- Administrator retains its catalog-wide administrative grant.
- President, vice-president, and secretary receive `dues.read` so they can see
  standing, but no detailed payment, export, batch, correction, or treasurer-note
  capability by default.
- Other roles receive no Phase 2 capability unless explicitly decided.
- Every operation is registered through `httpapi.Register`; handlers do not add
  hand-written capability checks.
- Server responses, not templates, enforce the distinction between safe standing
  and restricted payment detail.

The exact seed matrix is tested as a committed golden artifact. Payment writes,
posting, abandonment, corrections, coverage adjustments, rate changes, exports,
and worksheet generation declare audit actions. The generic audit middleware
remains the only audit emitter for HTTP operations.

## Concurrency, retries, and validation

- Open batches and entries use ETags and the existing 412 stale-write contract.
- Create-entry, post, single-payment, correction, and worksheet-generation writes
  accept idempotency keys where a browser/tool retry could duplicate state.
- Duplicate idempotency keys with different bodies return the existing conflict
  contract rather than replaying different work.
- Idempotency keys are scoped by actor and operation. The database, not process
  memory, owns replay detection so retries remain safe after restart.
- A paid-through date must be a real ISO `YYYY-MM-DD` date. It need not be
  December 31: an off-cycle or historical date is accepted as written, and no
  imported value is rewritten. See product decision 1.
- Amount and paid-through are validated independently. No rule asserts that a
  particular amount buys a particular date.
- Cash, check, and `other` are accepted methods; Phase 2 adds no online processor.
- CSV cells are escaped against spreadsheet formula injection.

## Migration and legacy backfill

The first Phase 2 migration implements ADR-0007:

1. Create the ledger, coverage, batch, and dues-rate tables plus capability seeds.
2. Insert one `coverage_events` row for every non-null
   `memberships.legacy_current_until`, with reason `legacy_import` and its source
   import link when available.
3. Preserve the legacy note as restricted context or a linked treasurer note.
4. Drop `legacy_current_until` and `legacy_current_until_note` only after verified
   row-count and value-preservation checks.

The Phase 1 importer currently normalizes `Current Until` but does not persist it
to the legacy membership columns, and it recognizes the known 2055 lifetime rows
without creating honorary grants. This is a verified cutover gap, not a migration
assumption. After the schema lands, the import commit path must write ordinary
dates directly to source-linked coverage events and lifetime decisions to honorary
grants for both new and matched members. The legacy-column backfill still protects
databases populated by any earlier/manual path.

Worksheet tables may land in a later migration so the ledger foundation and API
work do not collide unnecessarily.

## Worksheet and batch-grid constraints

- Worksheet filters: everyone who owes, all active memberships, or members from a
  prior sheet with no posted payment since that sheet.
- Sort: last name, call sign, or longest overdue. The chosen order is stored.
- Optional contact columns are server-authorized and snapshot with a clear “Good
  as of” timestamp. Blank guest rows are presentation-only.
- Creating a batch from a worksheet preserves worksheet order. A later print can
  mark a row entered by finding a posted payment after the prior run.
- The grid persists batch defaults for amount and paid-through, but each submitted
  row carries explicit values.
- Enter-to-add and live totals are progressive enhancement. Every mutation and
  total remains available with ordinary form submissions.

## Testing strategy

- Migration tests verify exact legacy backfill, no duplicate backfill, foreign
  keys, and up/down/up behavior.
- Import tests prove ordinary Current Until, sentinel dates, known and unknown
  2055 values, and honorary base-type decisions reach canonical coverage/grant
  records atomically after the schema cutover.
- Domain tests use explicit as-of dates and cover every standing state, honorary
  precedence, independent coverage adjustment, and non-binding suggestions.
- Posting tests prove draft isolation, all-or-nothing behavior, server totals,
  idempotent retry, and stale-post rejection.
- Correction tests prove original preservation, net totals, unchanged coverage,
  changed coverage, chains, and required reasons.
- Authorization tests prove non-treasurers cannot retrieve payment details even
  when they can read the member and safe standing.
- Export tests cover filters, stable order, cents/date formatting, redaction, and
  spreadsheet-formula safety.
- The production smoke test drives the shipped binaries through draft, post,
  standing change, correction, and a denied non-treasurer payment read.

## Owner decisions still visible

The implementation plan uses these safe defaults unless the owner changes them:

- CSV is the Phase 2 books export; specialized accounting formats are deferred.
- Receipts are printable/stable identifiers; automatic email is deferred.
- Proration is always treasurer judgment; `$10` is UI hint text, never a rule.
- Donations remain outside the dues ledger until their accounting semantics are
  designed.
- Phase 2 provides a member-safe summary DTO but not member authentication or a
  member-facing route.
- The owner confirmed BCARS is the Bedford County Amateur Radio Society. Current
  synthetic locality data uses Bedford, PA 15522; Phase 2 APIs must not hard-code
  organization branding or locality.

## Required gate set

Every Phase 2 implementation PR runs all seven repository gates:

```bash
make build
make test
make lint
make migration-updown
make sqlc-diff
make openapi-diff
make smoke
```
