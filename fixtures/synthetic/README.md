# Synthetic Groups.io fixtures

Every record here is fabricated. There is **no real BCARS member data** in
this directory. The `check-no-secrets.sh` script allows email-like strings
under `fixtures/synthetic/` on the assumption that this rule holds.

## Files

- `groupsio_contact.json` — Groups.io "table" export envelope that mirrors
  the real column layout (see `docs/phase-1-design.md` §7). Includes the
  variety of cases the importer must handle.
- `groupsio_contact.csv` — the same rows in the CSV export shape used by
  the human cross-check path.

## Case coverage

The row set intentionally exercises every branch of the importer state
machine (WS5):

| Case | Rows | Notes |
| --- | --- | --- |
| Clean Full member | 8 | Valid call sign, Class, email, phone. |
| Clean Associate | 4 | No call sign; Associate type. |
| Lifetime honorary (known) | 2 | `Current Until = 12/31/2055`. External ids `900001` and `900002` are declared as the "known lifetime rows" in the importer constant. |
| Unexpected lifetime-like date | 1 | `12/31/2055` on a row whose external id is not in the known list → `requires_manual`. |
| Unknown paid-through | 1 | `01/01/0001` → normalized to null with no manual flag. |
| Ambiguous email | 2 | Two persons share the same email; email-based match must NOT auto-resolve. |
| Honorary with unspecified type | 1 | `Membership Type=Honorary` but the base type is not implied → officer must pick Full/Associate. |
| Invalid phone | 1 | Non-numeric characters that don't normalize. |
| Case-fold membership type | 1 | Lowercase `full` should normalize to `Full`. |

The counts here are the fixture's promise; if a case is added or removed,
update this table and the WS5 tests that assert row totals.

## Fabricated identifiers

- All personal names use the pattern `Testname<N>` or common western
  first names paired with placeholder surnames.
- All email addresses use the domain `example.invalid` (RFC 6761 reserved
  for testing).
- All phone numbers use the `555` exchange in the North American plan.
- All call signs use the reserved `KA9XXX`-style prefix that would not
  match a real amateur licensee. If a chosen call sign happens to belong
  to a real licensee, replace it — this is a defect in the fixture.
- Postal addresses use `123 Fake St` style with real state abbreviations.
- Groups.io external row ids in the 900000+ range are far above the real
  data's `max_row_id` and are chosen to avoid collision.

If you catch a fixture value that could be mistaken for a real person's
data, delete it and open an issue; do not commit the replacement until it
has been reviewed.
