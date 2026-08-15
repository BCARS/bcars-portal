#!/usr/bin/env bash
# Walk the primary screens of a seeded development portal and write one
# screenshot per screen (bcars-portal-414).
#
# WHY
#
# Reviewing a UI change otherwise means booting a portal by hand, signing in as
# each of three roles, and clicking through every screen. That cost is paid
# again after every change and grows with each screen added, so in practice
# nobody re-walks twelve screens to check a token edit -- they check the two
# they were working on, and regressions surface late.
#
# This produces evidence a person looks at. It is deliberately NOT wired into
# CI: screenshot diffing fails on font rendering and scrollbar width, and the
# usual response is to stop trusting it.
#
# USAGE
#
#   make run-demo            # in one terminal
#   ./scripts/ui-walkthrough.sh
#
# Screenshots land in docs/screenshots/, numbered in walk order, and are
# COMMITTED (bcars-portal-jeo). Re-running overwrites them, so `git diff --stat`
# after a walk shows which screens a change altered. See that directory's
# README.md for the index.
#
# SAFETY — read this before running it anywhere but a demo portal.
#
# The output of this script is committed to the repository, and an image cannot
# be un-committed from history. GENERATE SCREENSHOTS ONLY FROM `make run-demo`.
# A walk against a database holding real members would write their names,
# addresses and telephone numbers into git permanently, as image files that no
# secrets gate can read — grep skips binaries, so nothing downstream will catch
# it. There is no automated check here; this rule is held by the person running
# the script and the person reviewing the pull request.
#
# Two things keep the script itself off real data. It refuses any host but
# localhost, and it authenticates only as demo accounts, which exist solely in a
# seeded development database. Neither helps if a real database is served on
# localhost with demo credentials, which is why the rule above is the one that
# matters.

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
OUT_DIR="${OUT_DIR:-docs/screenshots}"
# Every browser call is wrapped in this. A hung call otherwise stalls the whole
# walk with no output, which is how a two-minute pause got mistaken for a slow
# page during the 6pz session.
STEP_TIMEOUT="${STEP_TIMEOUT:-25}"
# Demo addresses are assembled rather than written out, the same way
# cmd/portalctl/seeddemo_members.go does it, so this file needs no secrets-gate
# allowlist entry. An allowlist entry is a permanent hole in that gate.
DEMO_DOMAIN="demo.local"

VIEWPORT_W="${VIEWPORT_W:-1280}"
VIEWPORT_H="${VIEWPORT_H:-900}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

step=0
failures=0

die() { echo "ui-walkthrough: $*" >&2; exit 1; }

command -v agent-browser >/dev/null 2>&1 \
  || die "agent-browser not found; install it or adapt this script to Playwright"

case "$BASE_URL" in
  http://localhost:*|http://127.0.0.1:*) ;;
  *) die "refusing to run against $BASE_URL; this walks a seeded development portal only" ;;
esac

curl -sf -o /dev/null --max-time 5 "$BASE_URL/login" \
  || die "no portal at $BASE_URL — start one with 'make run-demo'"

mkdir -p "$OUT_DIR"
# These are tracked files. Clearing them first means a screen that stops being
# captured shows up as a deletion in `git status` rather than leaving a stale
# image behind that still looks current. A failed walk is recoverable with
# `git checkout -- "$OUT_DIR"`.
rm -f "$OUT_DIR"/*.png

# ab runs one browser command in the named session, under a timeout.
ab() {
  local session="$1"; shift
  timeout "$STEP_TIMEOUT" agent-browser --session "bcars-$session" "$@"
}

# act runs a step that must succeed for a later screenshot to mean anything --
# toggling a preference, submitting a form. It reports and counts a failure
# rather than continuing silently.
#
# The alternative, `|| true`, is how this script's first run captured nothing
# for the printed worksheet and would happily have captured the base-size
# dashboard twice and labelled one of them "large": the selector
# button[type=submit] matched two buttons, the click was refused, and the walk
# carried on. A step that changes state has to be checked exactly like a
# navigation does.
act() {
  local session="$1" what="$2"; shift 2
  if ! ab "$session" "$@" >/dev/null 2>&1; then
    echo "  !! $session: $what failed — screens depending on it may be wrong" >&2
    failures=$((failures + 1))
    return 1
  fi
  return 0
}

# sign_in authenticates a role in its own browser session. Sessions are
# isolated, so the three roles are walked without signing out between them and
# without one role's cookie leaking into another's screenshots.
sign_in() {
  local session="$1" email="$2" password="$3"

  ab "$session" set viewport "$VIEWPORT_W" "$VIEWPORT_H" >/dev/null 2>&1 || true
  ab "$session" open "$BASE_URL/login" >/dev/null 2>&1 \
    || die "$session: could not open the sign-in page"
  ab "$session" fill 'input[name=email]' "$email" >/dev/null 2>&1 \
    || die "$session: no email field on the sign-in page"
  ab "$session" fill 'input[type=password]' "$password" >/dev/null 2>&1 \
    || die "$session: no password field on the sign-in page"
  ab "$session" click 'form[action="/login"] button[type=submit]' >/dev/null 2>&1 \
    || die "$session: could not submit the sign-in form"

  local url
  url="$(ab "$session" get url 2>/dev/null | tail -1 || true)"
  case "$url" in
    *"/login"*) die "$session: sign-in as $email failed — is this database seeded? ('make seed-demo')" ;;
  esac
}

# shot navigates, proves it landed on the right page, then screenshots.
#
# The proof is the point. A selector that matches nothing fails quietly and the
# walk carries on against whatever was on screen before, producing a confident
# screenshot of the wrong page. Asserting an expected string first means a
# broken step is reported by name instead of silently mis-captured.
shot() {
  local session="$1" slug="$2" path="$3" expect="$4"
  step=$((step + 1))
  local n; n="$(printf '%02d' "$step")"
  local file="$OUT_DIR/${n}-${session}-${slug}.png"

  if ! ab "$session" open "$BASE_URL$path" >/dev/null 2>&1; then
    echo "  !! $n $session/$slug — navigation to $path timed out or failed" >&2
    failures=$((failures + 1)); return
  fi

  # Read the main content, NOT the whole body. Every page carries the same
  # navigation, so a body-wide match makes an expectation like "Imports" true on
  # every screen in the portal -- including an error page. That is how a
  # deliberately broken path passed this check while the guard looked sound.
  # Read with a bounded retry. Querying immediately after a navigation can come
  # back empty because the page has not settled, which showed up as one screen
  # failing its assertion and passing on a second look. Retrying tolerates a
  # slow load without masking a wrong page: an expectation that is genuinely
  # absent still fails, just a beat later.
  local content=""
  local attempt
  for attempt in 1 2 3; do
    content="$(ab "$session" get text 'main#main' 2>/dev/null || true)"
    [ -z "$content" ] && content="$(ab "$session" get text body 2>/dev/null || true)"
    grep -qF -- "$expect" <<<"$content" && break
    [ "$attempt" -lt 3 ] && sleep 1
  done
  if ! grep -qF -- "$expect" <<<"$content"; then
    echo "  !! $n $session/$slug — $path did not contain \"$expect\"; not captured" >&2
    failures=$((failures + 1)); return
  fi

  if ! ab "$session" screenshot "$ROOT/$file" >/dev/null 2>&1; then
    echo "  !! $n $session/$slug — screenshot failed" >&2
    failures=$((failures + 1)); return
  fi
  echo "  $n $session/$slug"
}

echo "Walking $BASE_URL — screenshots to $OUT_DIR/"
echo

# --- Officer ---------------------------------------------------------------
sign_in admin "admin@$DEMO_DOMAIN" admin
shot admin dashboard          "/admin/"                  "Active Memberships"
shot admin members            "/admin/members"           "Whitfield"
shot admin member-detail      "/admin/members/1"         "Whitfield"
shot admin requests           "/admin/requests"          "Correction requests"
shot admin imports            "/admin/imports"           "Upload Import File"

# The text size preference changes every page, so the screen it most needs to
# be seen on is an ordinary one, at both sizes.
set_text_size() {
  local size="$1"
  act admin "open text size preference" open "$BASE_URL/preferences/text-size" || return 1
  act admin "select $size text" click "input[value=\"$size\"]" || return 1
  act admin "save $size text" click 'form[action="/preferences/text-size"] button[type=submit]' || return 1
  # Prove it took, by asking the browser what the root element actually says.
  # `get html html` returns innerHTML, which excludes the element's own
  # attributes -- it looks like it should work and silently never matches.
  local actual="" attempt
  for attempt in 1 2 3; do
    actual="$(ab admin eval "document.documentElement.getAttribute('data-text-size')" 2>/dev/null | tail -1 || true)"
    [[ "$actual" == *"$size"* ]] && break
    [ "$attempt" -lt 3 ] && sleep 1
  done
  if [[ "$actual" != *"$size"* ]]; then
    echo "  !! admin: text size did not change to $size; skipping that screenshot" >&2
    failures=$((failures + 1)); return 1
  fi
}

if set_text_size large; then
  shot admin dashboard-large  "/admin/"                  "Active Memberships"
fi
set_text_size base || true

# --- Treasurer -------------------------------------------------------------
sign_in treasurer "treasurer@$DEMO_DOMAIN" treasurer
shot treasurer treasury-home  "/admin/treasury"                    "Dues standing as of"
shot treasurer dues-standing  "/admin/treasury/standing"           "Dues standing"
shot treasurer batches        "/admin/treasury/batches"            "Payment batches"
shot treasurer payment-entry  "/admin/treasury/memberships/1/payment" "aid through"
shot treasurer worksheet-opts "/admin/treasury/worksheets"         "Renewal worksheets"

# The printed sheet needs a run to exist. Create one through the form rather
# than reaching into the database, so the walk also exercises the creation path.
# The submit selector is scoped to the form on purpose: this page also carries
# a search form, and an unscoped button[type=submit] matches both.
if act treasurer "open worksheet options" open "$BASE_URL/admin/treasury/worksheets" \
  && act treasurer "name the sheet" fill 'input[name=label]' 'Walkthrough sheet' \
  && act treasurer "generate the sheet" click 'form[action="/admin/treasury/worksheets"] button[type=submit]'
then
  sheet_url="$(ab treasurer get url 2>/dev/null | tail -1 || true)"
else
  sheet_url=""
fi
case "$sheet_url" in
  *"/worksheets/"*)
    shot treasurer worksheet-sheet "${sheet_url#"$BASE_URL"}" "Dues renewal worksheet" ;;
  *)
    echo "  !! could not create a worksheet run; printed sheet not captured" >&2
    failures=$((failures + 1)) ;;
esac

# The payment grid needs an open batch, and the grid only shows its entry
# controls once a member has been found — so the walk opens a batch and searches
# in it, which is what a treasurer does at a meeting. Without this the screen
# carrying the amount suggestion, the labelled date fields, the defaults block
# and the totals beside the attestation was never captured at all
# (bcars-portal-yec).
if act treasurer "open the batches page" open "$BASE_URL/admin/treasury/batches" \
  && act treasurer "name the batch" fill 'input[name=label]' 'Walkthrough meeting' \
  && act treasurer "open the batch" click 'form[action="/admin/treasury/batches"] button[type=submit]'
then
  batch_url="$(ab treasurer get url 2>/dev/null | tail -1 || true)"
else
  batch_url=""
fi
case "$batch_url" in
  *"/batches/"*)
    shot treasurer batch-entry "${batch_url#"$BASE_URL"}?member=e" "Add a row" ;;
  *)
    echo "  !! could not open a payment batch; the entry grid was not captured" >&2
    failures=$((failures + 1)) ;;
esac

# --- Member ----------------------------------------------------------------
sign_in member "joe@$DEMO_DOMAIN" joe
shot member landing           "/member/"                 "Your records"
shot member directory         "/member/directory"        "Zeller"
shot member directory-print   "/member/directory/print"  "Zeller"
shot member text-size         "/preferences/text-size"   "Text size"

# The screen a lost visitor sees, captured signed OUT. The session name is new
# on purpose: the walk's other sessions hold a cookie, and the public not-found
# page is precisely the one that must carry no navigation to screens the caller
# is not signed in to (bcars-portal-i4a).
shot public not-found         "/no-such-page"            "Not Found"

echo
if [ "$failures" -gt 0 ]; then
  echo "ui-walkthrough: $step screens attempted, $failures failed" >&2
  exit 1
fi
echo "ui-walkthrough: $step screens captured in $OUT_DIR/"
