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
# Usage: scripts/docker-smoke.sh <image[:tag]>
set -euo pipefail

IMAGE="${1:?usage: docker-smoke.sh <image[:tag]>}"
DOCKER="${DOCKER:-docker}"

# A non-default port, so a pass proves PORTAL_ADDR was read rather than that the
# image happened to listen where the test looked.
CONTAINER_PORT=9443
NAME="bcars-portal-smoke-$$"
DATA_DIR="$(mktemp -d)"

# A throwaway pepper for a throwaway database. Never reuse this anywhere.
PEPPER="docker-smoke-pepper-not-for-any-real-data"

cleanup() {
    status=$?
    if [ "$status" -ne 0 ]; then
        echo "--- container logs ---" >&2
        "$DOCKER" logs "$NAME" 2>&1 | tail -40 >&2 || true
    fi
    "$DOCKER" rm -f "$NAME" >/dev/null 2>&1 || true
    rm -rf "$DATA_DIR"
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

# The data directory is bind-mounted as the host user so the container's nonroot
# user can write to it.
chmod 0777 "$DATA_DIR"

"$DOCKER" run -d --name "$NAME" \
    -p "127.0.0.1:${CONTAINER_PORT}:${CONTAINER_PORT}" \
    -v "$DATA_DIR:/data" \
    -e PORTAL_PASSWORD_PEPPER="$PEPPER" \
    -e PORTAL_ADDR=":${CONTAINER_PORT}" \
    -e PORTAL_MIGRATE=true \
    -e PORTAL_LOG_LEVEL=debug \
    "$IMAGE" >/dev/null

base="http://127.0.0.1:${CONTAINER_PORT}"

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

# Front-end assets are compiled in, so the image needs no asset directory and
# the container needs no network (bcars-portal-chp). The URL is derived from the
# vendored file rather than written here, so a version bump does not leave this
# check quietly asserting a path nobody serves.
for path in internal/web/static/*.js; do
    asset="/static/$(basename "$path")"
    code="$(curl -s -o /dev/null -w '%{http_code}' "${base}${asset}")"
    [ "$code" = "200" ] || fail "the container does not serve its own asset ${asset} (HTTP ${code})"
done

# The mount point itself must not enumerate what the image contains.
code="$(curl -s -o /dev/null -w '%{http_code}' "${base}/static/")"
[ "$code" != "200" ] || fail "the container serves a directory listing at /static/"

# The database landed on the mounted volume, which is what makes the container
# restartable without losing the club's data.
[ -f "${DATA_DIR}/portal.db" ] || fail "no database was created on the mounted volume"

# portalctl ships in the same image, so bootstrap and backup are runnable
# against a deployed instance.
"$DOCKER" run --rm -v "$DATA_DIR:/data" --entrypoint /usr/local/bin/portalctl "$IMAGE" \
    --help >/dev/null 2>&1 || fail "portalctl is not runnable from the image"

echo "docker-smoke: ok (${IMAGE})"
