# Portal screens

Every primary screen of the portal, walked in order by
[`scripts/ui-walkthrough.sh`](../../scripts/ui-walkthrough.sh) against a seeded
development portal. They are committed so a screen can be looked at — and a
visual change reviewed in a pull request — without booting a portal and signing
in as three different roles.

**Everything shown here is synthetic.** The members, call signs, addresses and
`@demo.local` email addresses come from
[`cmd/portalctl/seeddemo_members.go`](../../cmd/portalctl/seeddemo_members.go),
which exists only behind the `demoseed` build tag and is absent from shipped
binaries. No real member appears in any of these images.

## Refreshing them

```bash
make run-demo                 # in one terminal
./scripts/ui-walkthrough.sh   # in another
git diff --stat docs/screenshots/
```

The walk overwrites every file, so `git diff --stat` afterwards is the list of
screens a change actually altered.

**Generate these only from `make run-demo`.** An image cannot be removed from
git history, and no gate in this repository can read one — `grep` skips
binaries, so a screenshot taken against a database of real members would commit
their names, addresses and telephone numbers permanently with nothing to catch
it. This rule is held by the person running the script and the person reviewing
the pull request; there is no automated check.

Screenshot diffing is deliberately **not** wired into CI. It fails on font
rendering and scrollbar width, and the usual response to a flaky visual gate is
to stop trusting it.

## The walk

Numbered in walk order. The role prefix is the account the screen was captured
as; each role runs in its own browser session, so no role's cookie leaks into
another's screens.

The member files a correction and a note before the officer screens are taken,
even though the member screens come last. An empty queue photographs as an
empty queue, and the two screens carrying the correction workflow have nothing
to show without one of each.

### Officer / administrator

| | Screen | File |
|---|---|---|
| 01 | Dashboard — club-wide counts and recent audit events | [`01-admin-dashboard.png`](01-admin-dashboard.png) |
| 02 | Member list with search | [`02-admin-members.png`](02-admin-members.png) |
| 03 | Member detail — contact methods, memberships, notes | [`03-admin-member-detail.png`](03-admin-member-detail.png) |
| 04 | Correction requests, opening on what is still outstanding | [`04-admin-requests.png`](04-admin-requests.png) |
| 05 | Reviewing an edit — each change beside the value on the record, editable, tick to include, one apply | [`05-admin-request-review.png`](05-admin-request-review.png) |
| 06 | Reviewing a note — nothing to apply, so an officer acts on the record and marks it done | [`06-admin-note-review.png`](06-admin-note-review.png) |
| 07 | Groups.io import — upload and staging | [`07-admin-imports.png`](07-admin-imports.png) |
| 08 | Dashboard at the larger text size | [`08-admin-dashboard-large.png`](08-admin-dashboard-large.png) |

### Treasurer

| | Screen | File |
|---|---|---|
| 09 | Treasury home — dues standing as of a date | [`09-treasurer-treasury-home.png`](09-treasurer-treasury-home.png) |
| 10 | Full dues standing | [`10-treasurer-dues-standing.png`](10-treasurer-dues-standing.png) |
| 11 | Payment batches | [`11-treasurer-batches.png`](11-treasurer-batches.png) |
| 12 | Single payment entry | [`12-treasurer-payment-entry.png`](12-treasurer-payment-entry.png) |
| 13 | Renewal worksheet options | [`13-treasurer-worksheet-opts.png`](13-treasurer-worksheet-opts.png) |
| 14 | Printable renewal worksheet | [`14-treasurer-worksheet-sheet.png`](14-treasurer-worksheet-sheet.png) |
| 15 | Payment batch entry — defaults, grid and totals | [`15-treasurer-batch-entry.png`](15-treasurer-batch-entry.png) |

### Member

| | Screen | File |
|---|---|---|
| 16 | Member landing — your own records | [`16-member-landing.png`](16-member-landing.png) |
| 17 | Member directory | [`17-member-directory.png`](17-member-directory.png) |
| 18 | Directory print view | [`18-member-directory-print.png`](18-member-directory-print.png) |
| 19 | Text size preference | [`19-member-text-size.png`](19-member-text-size.png) |
| 20 | Correcting your details — the record as a form, every field holding its own value | [`20-member-edit.png`](20-member-edit.png) |
| 21 | Telling the officers about another member — a note, proposing nothing structured | [`21-member-note.png`](21-member-note.png) |

### Signed out

| | Screen | File |
|---|---|---|
| 22 | Not found, as a signed-out visitor sees it — no navigation, one way back | [`22-public-not-found.png`](22-public-not-found.png) |

Screens 05, 06, 20 and 21 are the shape ADR-0014 describes: a member edits the
record they can see, reports anything else as a note, and an officer amends and
applies in one pass rather than answering yes or no a field at a time.

These show the functional MVP layouts. Visual design and polish are the subject
of the `6pz` design beads, so treat them as evidence of what each screen *does*
rather than of how it should finally look.
