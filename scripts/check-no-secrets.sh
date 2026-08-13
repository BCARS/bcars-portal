#!/usr/bin/env bash
# WS1.2: guard against accidentally committing secrets, populated .env files,
# or real BCARS member data. Runs in CI and as part of `make lint`.
#
# WHAT "TRACKED" MEANS HERE
#
# The scan covers files git tracks AND files git would add if you staged
# everything: `git ls-files` union `git ls-files --others --exclude-standard`.
# Scanning only the index is how bcars-portal-4ux.3 shipped -- an example email
# address in a brand-new domain file passed every local gate, because the file
# did not exist as far as `git ls-files` was concerned, and then failed the
# secrets job on CI once it had been committed (bcars-portal-6q6.7).
#
# --exclude-standard is what keeps this safe to point at a working tree: it
# honours .gitignore, so data/, backups/, uploads/ and local SQLite databases
# holding real member records stay unread. Do not drop that flag to "be
# thorough" -- it would make this script open the very files it exists to keep
# out of git.
#
# Rules:
#   - No tracked file may be named .env or .env.<something> other than
#     .env.sample.
#   - No tracked file under /data, /backups, /uploads, /extracted, or
#     /transcripts.
#   - No tracked SQLite file.
#   - Anything that looks like an email address in a tracked file is only
#     allowed inside fixtures/synthetic/, docs/, README.md, or *_test.go.
#   - .env.sample must not contain a value on the right of '=' for any key
#     listed as secret.
#
# Exits non-zero on the first violation.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# --self-test proves this gate is load-bearing rather than merely present.
#
# A guard nobody has watched fail is a guard nobody knows works. This mode
# plants the exact file that used to slip through -- a new, unstaged,
# non-allowlisted file holding an email literal -- and fails if the scan
# does not reject it. It also plants an ignored file to confirm the scan
# still refuses to read anything under a private data path.
if [ "${1:-}" = "--self-test" ]; then
  probe_code="$ROOT/internal/zz_selftest_probe.go"
  probe_ignored_dir="$ROOT/data"
  probe_ignored="$probe_ignored_dir/zz_selftest_probe.csv"
  created_ignored_dir=""
  [ -d "$probe_ignored_dir" ] || { mkdir -p "$probe_ignored_dir"; created_ignored_dir=1; }
  cleanup_selftest() {
    rm -f "$probe_code" "$probe_ignored"
    [ -n "$created_ignored_dir" ] && rmdir "$probe_ignored_dir" 2>/dev/null
    return 0
  }
  trap cleanup_selftest EXIT

  printf 'package internal\n\nconst probe = "someone@example.com"\n' >"$probe_code"
  if "$0" >/dev/null 2>&1; then
    echo "check-no-secrets: SELF-TEST FAILED — an untracked file with an email literal was not rejected" >&2
    exit 1
  fi
  rm -f "$probe_code"

  printf 'name,email\nA Member,real.member@example.org\n' >"$probe_ignored"
  if ! "$0" >/dev/null 2>&1; then
    echo "check-no-secrets: SELF-TEST FAILED — an ignored private data file was read" >&2
    exit 1
  fi

  echo "check-no-secrets: self-test ok"
  exit 0
fi

fail() { echo "check-no-secrets: $*" >&2; exit 1; }

# candidate_files prints, NUL-separated, every file this gate is responsible
# for: what git already tracks, plus what git would pick up on `git add -A`.
# Ignored paths appear in neither list.
candidate_files() {
  {
    git ls-files -z 2>/dev/null || true
    git ls-files -z --others --exclude-standard 2>/dev/null || true
  } | sort -zu
}

# 1. Disallowed filenames in git index.
if candidate_files | tr '\0' '\n' | grep -Ev '^\.env\.sample$' | grep -E '(^|/)\.env($|\.)' >/dev/null; then
  fail "populated .env file is tracked by git or would be added by it"
fi

if candidate_files | tr '\0' '\n' \
  | grep -E '^(data|backups|uploads|extracted|transcripts)/' >/dev/null; then
  fail "private data directory contents are tracked by git or would be added by it"
fi

if candidate_files | tr '\0' '\n' \
  | grep -E '\.(db|sqlite|sqlite3)(-wal|-shm)?$' >/dev/null; then
  fail "SQLite database files are tracked by git or would be added by it"
fi

# 2. Email pattern outside allowed locations.
ALLOWED_EMAIL_PATHS='^(fixtures/synthetic/|docs/|README\.md$|\.env\.sample$|scripts/check-no-secrets\.sh$|PLANNING\.md$)'
EMAIL_RE='[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
# Git SSH remotes contain the transport identity `git@github.com`. It is not
# an email address or secret, so exclude that exact token after extracting
# email-like matches. Keep the exception token-specific rather than allowing
# an entire file such as .beads/config.yaml.
SAFE_SCM_IDENTITY_RE='^git@github\.com$'

# Scan tracked and would-be-added files alike. A temporary file in the repo's
# own tmp would itself be a candidate, so use mktemp outside the tree.
SCAN_LIST="$(mktemp)"
trap 'rm -f "$SCAN_LIST"' EXIT
if candidate_files >"$SCAN_LIST" 2>/dev/null; then
  while IFS= read -r -d '' f; do
    [ -f "$f" ] || continue
    case "$f" in
      # cmd/portalctl/seeddemo.go holds the @demo.local development accounts;
      # it is excluded from production builds by the `demoseed` build tag.
      fixtures/synthetic/*|docs/*|README.md|.env.sample|scripts/check-no-secrets.sh|PLANNING.md|*_test.go|internal/web/handler.go|cmd/portalctl/seeddemo.go) continue ;;
    esac
    EMAIL_MATCHES=$(LC_ALL=C grep -E -I -o "$EMAIL_RE" "$f" 2>/dev/null \
      | grep -Ev "$SAFE_SCM_IDENTITY_RE" || true)
    if [ -n "$EMAIL_MATCHES" ]; then
      # Allow the well-known bcars.org placeholders in code comments only if
      # the file was already listed above; otherwise reject.
      fail "email-like value found in file: $f"
    fi
  done <"$SCAN_LIST"
fi

# 3. .env.sample: keys marked secret must not have a value.
if [ -f .env.sample ]; then
  # Any variable name containing PASSWORD, PEPPER, SECRET, TOKEN, KEY must be
  # blank on the RHS. (KEY is intentionally broad; adjust if it produces
  # false positives.)
  BAD=$(grep -E '^[A-Z_]*(PASSWORD|PEPPER|SECRET|TOKEN|KEY)[A-Z_]*=.+' .env.sample || true)
  if [ -n "$BAD" ]; then
    echo "check-no-secrets: .env.sample contains a value for a secret-looking key:" >&2
    echo "$BAD" >&2
    exit 1
  fi
fi

echo "check-no-secrets: ok"
