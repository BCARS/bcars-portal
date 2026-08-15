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

### Officer / administrator

| | Screen | File |
|---|---|---|
| 01 | Dashboard — club-wide counts and recent audit events | [`01-admin-dashboard.png`](01-admin-dashboard.png) |
| 02 | Member list with search | [`02-admin-members.png`](02-admin-members.png) |
| 03 | Member detail — contact methods, memberships, notes | [`03-admin-member-detail.png`](03-admin-member-detail.png) |
| 04 | Correction requests awaiting review | [`04-admin-requests.png`](04-admin-requests.png) |
| 05 | Groups.io import — upload and staging | [`05-admin-imports.png`](05-admin-imports.png) |
| 06 | Dashboard at the larger text size | [`06-admin-dashboard-large.png`](06-admin-dashboard-large.png) |

### Treasurer

| | Screen | File |
|---|---|---|
| 07 | Treasury home — dues standing as of a date | [`07-treasurer-treasury-home.png`](07-treasurer-treasury-home.png) |
| 08 | Full dues standing | [`08-treasurer-dues-standing.png`](08-treasurer-dues-standing.png) |
| 09 | Payment batches | [`09-treasurer-batches.png`](09-treasurer-batches.png) |
| 10 | Single payment entry | [`10-treasurer-payment-entry.png`](10-treasurer-payment-entry.png) |
| 11 | Renewal worksheet options | [`11-treasurer-worksheet-opts.png`](11-treasurer-worksheet-opts.png) |
| 12 | Printable renewal worksheet | [`12-treasurer-worksheet-sheet.png`](12-treasurer-worksheet-sheet.png) |
| 13 | Payment batch entry — defaults, grid and totals | [`13-treasurer-batch-entry.png`](13-treasurer-batch-entry.png) |

### Member

| | Screen | File |
|---|---|---|
| 14 | Member landing — your own records | [`14-member-landing.png`](14-member-landing.png) |
| 15 | Member directory | [`15-member-directory.png`](15-member-directory.png) |
| 16 | Directory print view | [`16-member-directory-print.png`](16-member-directory-print.png) |
| 17 | Text size preference | [`17-member-text-size.png`](17-member-text-size.png) |
| 18 | Suggest a correction — one question, each contact detail a choice | [`18-member-suggest.png`](18-member-suggest.png) |

### Signed out

| | Screen | File |
|---|---|---|
| 19 | Not found, as a signed-out visitor sees it — no navigation, one way back | [`19-public-not-found.png`](19-public-not-found.png) |

These show the functional MVP layouts. Visual design and polish are the subject
of the `6pz` design beads, so treat them as evidence of what each screen *does*
rather than of how it should finally look.
