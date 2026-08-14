# Development lessons learned

Ways this repository has produced a confident wrong answer, and what each one
cost to find. Every entry names the bead it came from, so this reads as history
rather than as advice.

The entries share a shape. Something reported success. The success was real —
the test passed, the gate was green, the page rendered — and the thing it was
taken to prove was false. None of these were caught by being careful. They were
caught by someone breaking the check on purpose and watching what it said.

Two of these are also stored as `bd` memories, `audit-the-property-not-the-pieces`
and `closure-is-not-evidence`, for agents that recall rather than read.

---

## 1. A check that passes tells you nothing until you have watched it fail

Write the assertion, then break the behaviour it covers and confirm the test
goes red. If it stays green, the assertion was measuring something else.

Four instances in a single day of work on `6pz.1`, `xdx` and `414`:

- **The assertion matched the stylesheet, not the page.** `6pz.1` added a
  per-user text size stamped onto `<html>`. The test asserted the response body
  contained `data-text-size="large"` — which it always did, because every page
  ships a `:root[data-text-size="large"]` rule in its CSS. The test passed
  against a wrapper hard-coded to the base size. It now asserts on the opening
  `<html>` element.
- **The assertion matched the navigation bar.** `414`'s walkthrough checked each
  page contained an expected string, reading the whole `<body>`. Every page
  carries the same nav, so `"Imports"` was true on every screen in the portal,
  including an error page. A deliberately wrong path passed. Assertions now read
  `main#main`.
- **The check could never have matched.** The same script verified the text size
  with `get html html`, which returns *innerHTML* — it excludes the element's own
  attributes, so `data-text-size` was never in the string being searched.
- **The fixture drifted into the wrong state.** `xdx` seeds a member meant to
  read as expired. The club-year offset was expressed in days, so `-1` moved back
  one day, stayed in the same calendar year, and 31 December was still in the
  future. The member rendered as comfortably current and the dashboard reported
  one overdue member instead of two. Nothing errored.

The last one generalises past tests: **a fixture whose rows drift into the wrong
bucket is worse than no fixture**, because it makes a screen look reviewed when
the state it was meant to demonstrate was never on the page.

## 2. Assert the property through the surface the user touches

An audit that tests the pieces and reports the property reaches a confident
wrong answer.

Phase 2's completion smoke asserted that worksheet rows were ordered, and
separately that the linked batch existed. Both passed, and the audit concluded
"worksheet order seeds the batch grid". Nothing read the order *from* the batch,
and the batch surface ignored `worksheet_run_id` entirely, so the claim was false
while every assertion was true (`9zm.1`).

When a criterion names a user-visible property, assert it through the surface the
user touches, then confirm the test fails when the behaviour is removed —
disabling the consumer must turn it red.

## 3. Package tests do not exercise the assembly

A pepper bug survived every package test because those tests build the web
handler directly. Only signing in through the shipped binary's login form
exposed it (`pma.12`). The same shape produced `fmc.21`, where the admin UI
passed an empty client-IP hash and every UI-initiated recovery stored NULL, and
`fmc.4`, where recovery and invitation templates were embedded but never
registered, so every one of those pages failed at runtime with no build-time
signal.

`make smoke` exists for this. It runs the shipped binaries. A change to wiring,
configuration, embedding or registration is not covered until smoke covers it.

## 4. Gates that reason about git state cannot see new files

`check-no-secrets.sh` scanned `git ls-files`, so a brand-new file was invisible
until staged. An author could write a file, run all seven gates, see green, and
fail the secrets job on CI — which is exactly how `4ux.3` shipped an example
email address in a new domain file. `sqlc-diff` had the same blind spot from the
other side: `git diff` reports nothing at all for an untracked file, so a newly
generated query file that was never added passed the drift gate both locally and
in CI (`6q6.7`).

Both now scan what git *would* add, via `git ls-files --others --exclude-standard`
and `git status --porcelain`. The `--exclude-standard` flag is load-bearing: it
honours `.gitignore`, so `data/` and local databases holding real member records
stay unread. Removing it to "be thorough" would make the secrets script open the
very files it exists to keep out of git.

## 5. A guard that names the wrong cause is worse than one that stays quiet

`check-no-secrets.sh --self-test` plants a violation and fails if the scan does
not reject it, then plants an ignored file and fails if the scan did read it. It
met its first real violation hours after being written — email literals in a new
script — and reported *"an ignored private data file was read"*, sending the
reader to `.gitignore` when the problem was somewhere else entirely (`414`).

It now establishes the tree is clean before planting anything, and reports
`SELF-TEST INCONCLUSIVE` with the actual offending file.

## 6. `|| true` converts a failure into a silently wrong artefact

`414`'s walkthrough used `|| true` on the steps that set up state — toggling a
preference, submitting a form. A selector matched two buttons, the click was
refused, and the walk carried on. The printed worksheet was never created, and
the "large text" dashboard would have been captured at base size and written to a
file named `06-admin-dashboard-large.png`.

Nobody reviewing a folder of screenshots questions one that is present. A wrong
screenshot is worse than a missing one because it looks like evidence. Steps that
change state must be checked exactly like the assertions that follow them.

## 7. A value that must match in several places belongs in one place

Seeding demo accounts and running the server must use the same password pepper,
or the seeded passwords can never verify. `fmc.14` was mainly about the pepper
being nil in the production assembly, and part of its fix was making `seed-demo`
and the assembly test fixture agree on one value. The five-step manual sequence
for standing up a demo portal reintroduced the disagreement every time someone
typed it — twice in one session, by hand. `make run-demo` now defines
`DEMO_PEPPER` once and hands it to both steps (`bql`).

The general form: if correctness depends on two written values being identical,
they should not be two written values.

## 8. Closure is not evidence

On 2026-08-08 `phase-1-progress.md` read COMPLETE because every bead beneath it
was closed. Bead status records that someone decided the work was done. It does
not record that the software works. Reconcile a progress claim against the
running system, not against the tracker.

## 9. Tools fail quietly in both directions

A selector that matches **nothing** and a selector that matches **too many** both
stop a step from happening; whether either is reported depends on the tool.
`agent-browser` refuses an ambiguous selector loudly and returns empty for a
missing one. During `414` both happened, and only the first was noticed, because
the second was swallowed by `|| true`.

Related: reading a page immediately after navigating races the load. One screen
failed its assertion and passed on a second look. Reads are now retried, bounded
— a slow page is tolerated, a wrong page still fails.

Also: `pgrep -f` and `pkill -f` match full command lines, *including the shell
running them*. A wait-loop polling `pgrep -f "go test"` matched itself and spun
forever; a `pkill -f` pattern matched its own wrapper and killed the command it
was part of. Match on process name, or on a PID.

## 10. Staged is not committed

After a rebase and stash pop, the new files for `xdx` were staged and the
one-line change that *called* them was not. `git commit` took only what was
staged, and the pushed commit contained a 468-line seeder that nothing invoked.

Every test still passed, because they call the seeding function directly. Only
reading `git show HEAD:` caught it. Check what the commit contains, not what the
working tree does.

## 11. Prefix routes swallow unknown paths

`GET /admin/` is a `net/http` ServeMux prefix pattern, so `/admin/nonexistent`
returns 200 and renders the dashboard rather than a not-found page (`i4a`). No
unauthorised content is served — the dashboard gates on capability and redirects
a member to their own landing — but a mistyped or renamed route gives no signal,
and a broken link looks like it works.

---

## The short version

- Break the check and watch it fail, or you do not have a check.
- Assert through the surface, not the pieces behind it.
- Run the shipped binary before believing the wiring.
- A green gate proves the gate ran, not that it looked at your change.
- Prefer a missing artefact to a wrong one.
