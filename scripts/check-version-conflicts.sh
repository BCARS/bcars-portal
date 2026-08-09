#!/usr/bin/env bash
#
# check-version-conflicts.sh — refuse :exec mutations that take a version.
#
# A query declared `:exec` returns only an error. A statement whose WHERE
# clause includes `version = ?` therefore cannot tell the caller whether the
# row matched: a stale version updates nothing and reports success, and the
# caller believes the write happened. Optimistic concurrency that silently
# no-ops is worse than none, because there is no signal to retry.
#
# This shipped six times before anyone noticed (bcars-portal-fmc.15 found the
# first, bcars-portal-fmc.19 the rest), which is why the check exists rather
# than a note in a review guide. The correct shape is `:one` with `RETURNING`,
# mapping sql.ErrNoRows to db.ErrStale.
#
# A genuinely unconditional bulk write may bump `version` WITHOUT taking one as
# a parameter — that is allowed and is not what this looks for.

set -euo pipefail

QUERY_DIR="${1:-internal/db/queries}"
status=0

for file in "$QUERY_DIR"/*.sql; do
    [ -e "$file" ] || continue

    # Walk each `-- name: X :kind` block and flag :exec blocks whose body
    # binds a version parameter.
    awk -v FILE="$file" '
        /^-- name: / {
            if (name != "" && kind == ":exec" && takes_version) {
                printf "%s:%d: %s is :exec but binds a version parameter — a stale version cannot be detected. Use :one with RETURNING.\n", FILE, line, name
                bad++
            }
            name = $3; kind = $4; line = NR; takes_version = 0
            next
        }
        # Match "version = ?" and "version = sqlc.arg(...)" in a WHERE clause.
        /version[[:space:]]*=[[:space:]]*(\?|sqlc\.(arg|narg))/ {
            if (name != "") takes_version = 1
        }
        END {
            if (name != "" && kind == ":exec" && takes_version) {
                printf "%s:%d: %s is :exec but binds a version parameter — a stale version cannot be detected. Use :one with RETURNING.\n", FILE, line, name
                bad++
            }
            exit (bad > 0 ? 1 : 0)
        }
    ' "$file" || status=1
done

if [ "$status" -ne 0 ]; then
    echo ""
    echo "check-version-conflicts: FAILED"
    echo "See internal/domain/members/service.go RevokeHonoraryGrant for the expected shape."
    exit 1
fi

echo "check-version-conflicts: ok"
