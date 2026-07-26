#!/usr/bin/env bash
# WS1.2: guard against accidentally committing secrets, populated .env files,
# or real BCARS member data. Runs in CI and as part of `make lint`.
#
# Rules:
#   - No tracked file may be named .env or .env.<something> other than
#     .env.sample.
#   - No tracked file under /data, /backups, /uploads, /extracted, or
#     /transcripts.
#   - No tracked SQLite file.
#   - Anything that looks like an email address in a tracked file is only
#     allowed inside fixtures/synthetic/, docs/, or README.md.
#   - .env.sample must not contain a value on the right of '=' for any key
#     listed as secret.
#
# Exits non-zero on the first violation.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

fail() { echo "check-no-secrets: $*" >&2; exit 1; }

# 1. Disallowed filenames in git index.
if git ls-files -z 2>/dev/null | tr '\0' '\n' | grep -Ev '^\.env\.sample$' | grep -E '(^|/)\.env($|\.)' >/dev/null; then
  fail "populated .env file is tracked by git"
fi

if git ls-files -z 2>/dev/null | tr '\0' '\n' \
  | grep -E '^(data|backups|uploads|extracted|transcripts)/' >/dev/null; then
  fail "private data directory contents are tracked by git"
fi

if git ls-files -z 2>/dev/null | tr '\0' '\n' \
  | grep -E '\.(db|sqlite|sqlite3)(-wal|-shm)?$' >/dev/null; then
  fail "SQLite database files are tracked by git"
fi

# 2. Email pattern outside allowed locations.
ALLOWED_EMAIL_PATHS='^(fixtures/synthetic/|docs/|README\.md$|\.env\.sample$|scripts/check-no-secrets\.sh$|PLANNING\.md$)'
EMAIL_RE='[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'

# Use git grep only over tracked files; ignore files that don't exist yet.
if git ls-files -z >/tmp/.bcars_tracked 2>/dev/null; then
  while IFS= read -r -d '' f; do
    [ -f "$f" ] || continue
    case "$f" in
      fixtures/synthetic/*|docs/*|README.md|.env.sample|scripts/check-no-secrets.sh|PLANNING.md) continue ;;
    esac
    if LC_ALL=C grep -E -I -q "$EMAIL_RE" "$f" 2>/dev/null; then
      # Allow the well-known bcars.org placeholders in code comments only if
      # the file was already listed above; otherwise reject.
      fail "email-like value found in tracked file: $f"
    fi
  done </tmp/.bcars_tracked
  rm -f /tmp/.bcars_tracked
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
