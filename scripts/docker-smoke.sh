#!/usr/bin/env bash
# Exercise the container image the way a deployment does.
#
# `docker build` only proves the image assembles. This starts it the documented
# way — secrets from the environment, migrations before readiness, a mounted
# database directory — and asserts the running container reaches ready, serves
# its own front-end assets, and honours the environment variables the image
# documents. Those are the claims docs/deployment.md makes; nothing else checks
# them (bcars-portal-fmc.8).
#
# Nothing here assumes the Docker daemon shares a filesystem or a network
# namespace with this script. On a GitHub-hosted runner it does — the job runs
# on the VM that owns the daemon — but on a containerized runner (Forgejo
# Actions, act) the daemon is elsewhere, and the two assumptions that follow
# from sharing both fail silently rather than loudly:
#
#   * A bind mount resolves its source on the *daemon's* filesystem. A `mktemp
#     -d` path from this script does not exist there, so the daemon creates an
#     empty root-owned directory, the container's nonroot user cannot write the
#     database, and a `[ -f "$dir/portal.db" ]` check reads a directory nothing
#     ever wrote to. A named volume is managed by the daemon and inherits the
#     image's ownership of /data, so it works in both places.
#
#   * A published port lands in the *daemon's* network namespace. `127.0.0.1`
#     from this script is not that namespace, so every readiness poll times out
#     while the container is up and healthy. Instead the container and the
#     process doing the polling share a user-defined network and address each
#     other by container name, which is true wherever the daemon lives.
#
# Usage: scripts/docker-smoke.sh <image[:tag]>
set -euo pipefail

IMAGE="${1:?usage: docker-smoke.sh <image[:tag]>}"
DOCKER="${DOCKER:-docker}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# A non-default port, so a pass proves PORTAL_ADDR was read rather than that the
# image happened to listen where the test looked.
CONTAINER_PORT=9443
NAME="bcars-portal-smoke-$$"
NET="bcars-portal-smoke-net-$$"
DATA_VOL="bcars-portal-smoke-data-$$"

# The runtime image is distroless: no shell, no curl. The probing has to happen
# from somewhere else, and the Dockerfile's own build stage is the one image
# this repository already guarantees is pullable — reusing it keeps the smoke
# test from adding a registry dependency, and from carrying a second image
# version to bump.
HELPER="$(awk '$1 == "FROM" && $NF == "build" { print $2 }' "$ROOT/Dockerfile")"
[ -n "$HELPER" ] || { echo "docker-smoke: no build stage found in Dockerfile" >&2; exit 1; }

# A throwaway pepper for a throwaway database. Never reuse this anywhere.
PEPPER="docker-smoke-pepper-not-for-any-real-data"

cleanup() {
    status=$?
    if [ "$status" -ne 0 ]; then
        echo "--- container logs ---" >&2
        "$DOCKER" logs "$NAME" 2>&1 | tail -40 >&2 || true
    fi
    "$DOCKER" rm -f "$NAME" >/dev/null 2>&1 || true
    "$DOCKER" volume rm -f "$DATA_VOL" >/dev/null 2>&1 || true
    "$DOCKER" network rm "$NET" >/dev/null 2>&1 || true
    exit "$status"
}
trap cleanup EXIT

fail() { echo "docker-smoke: $*" >&2; exit 1; }

# The image must not carry a secret. A pepper baked into a layer is readable by
# anyone who can pull the image, and would defeat the reason it exists.
if "$DOCKER" run --rm --entrypoint /usr/local/bin/portal "$IMAGE" -version >/dev/null 2>&1; then
    :
else
    fail "the image cannot run 'portal -version'"
fi

env_output="$("$DOCKER" image inspect "$IMAGE" --format '{{range .Config.Env}}{{println .}}{{end}}')"
if grep -Eqi 'PEPPER|PASSWORD|PASSPHRASE|SECRET' <<<"$env_output"; then
    fail "the image bakes a secret into its environment:
$env_output"
fi

"$DOCKER" network create "$NET" >/dev/null
"$DOCKER" volume create "$DATA_VOL" >/dev/null

"$DOCKER" run -d --name "$NAME" --network "$NET" \
    -v "${DATA_VOL}:/data" \
    -e PORTAL_PASSWORD_PEPPER="$PEPPER" \
    -e PORTAL_ADDR=":${CONTAINER_PORT}" \
    -e PORTAL_MIGRATE=true \
    -e PORTAL_LOG_LEVEL=debug \
    "$IMAGE" >/dev/null

# Front-end assets are compiled in, so the image needs no asset directory and
# the container needs no network (bcars-portal-chp). The URLs are derived from
# the vendored files rather than written here, so a version bump does not leave
# this check quietly asserting a path nobody serves. The list is built out here,
# where the source tree is, and passed to the probe, which cannot see it.
assets=()
for path in "$ROOT"/internal/web/static/*.js; do
    assets+=("/static/$(basename "$path")")
done
[ "${#assets[@]}" -gt 0 ] || fail "no vendored front-end assets found to check"

# One container runs every HTTP assertion, on the network the portal is on and
# addressing it by container name. Everything inside this heredoc executes over
# there; it is quoted, so nothing in it is expanded by this shell.
"$DOCKER" run --rm -i --network "$NET" "$HELPER" bash -s -- \
    "http://${NAME}:${CONTAINER_PORT}" "${assets[@]}" <<'PROBE'
set -euo pipefail

base="$1"; shift

fail() { echo "docker-smoke: $*" >&2; exit 1; }

# Readiness, not liveness: /readyz is 503 until migrations have run, so a pass
# here is what proves PORTAL_MIGRATE was honoured on a fresh volume.
ready=""
for _ in $(seq 1 60); do
    if [ "$(curl -s -o /dev/null -w '%{http_code}' "${base}/readyz" || true)" = "200" ]; then
        ready=yes
        break
    fi
    sleep 1
done
[ -n "$ready" ] || fail "the container never became ready on ${base}/readyz"

# The sign-in page renders for an anonymous caller.
login_html="$(curl -sS "${base}/login")"
grep -q '<form' <<<"$login_html" || fail "the sign-in page did not render"

for asset in "$@"; do
    code="$(curl -s -o /dev/null -w '%{http_code}' "${base}${asset}")"
    [ "$code" = "200" ] || fail "the container does not serve its own asset ${asset} (HTTP ${code})"
done

# The mount point itself must not enumerate what the image contains.
code="$(curl -s -o /dev/null -w '%{http_code}' "${base}/static/")"
[ "$code" != "200" ] || fail "the container serves a directory listing at /static/"
PROBE

# The database landed on the mounted volume, which is what makes the container
# restartable without losing the club's data. The runtime image has no shell to
# check with, so the helper looks at the same volume.
"$DOCKER" run --rm -v "${DATA_VOL}:/data" "$HELPER" \
    test -f /data/portal.db || fail "no database was created on the mounted volume"

# portalctl ships in the same image, so bootstrap and backup are runnable
# against a deployed instance.
"$DOCKER" run --rm -v "${DATA_VOL}:/data" --entrypoint /usr/local/bin/portalctl "$IMAGE" \
    --help >/dev/null 2>&1 || fail "portalctl is not runnable from the image"

echo "docker-smoke: ok (${IMAGE})"
