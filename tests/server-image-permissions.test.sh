#!/usr/bin/env bash
# Exercise the actual runtime packaging instructions without compiling Go or
# installing packages. Override the base with an already available Alpine image
# for offline testing: RETICORA_TEST_BASE_IMAGE=nats:2.14.10-alpine@sha256:311da47e2f31c26e99c623cd78d486a1e8ab039c49b32652770bb91a4f70512e.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp="$(mktemp -d)"
image="reticora-server-permissions-test-$$"
cleanup() {
    docker image rm "$image" >/dev/null 2>&1 || true
    rm -rf "$tmp"
}
trap cleanup EXIT
mkdir -p "$tmp/api"
cp api/openapi.yaml "$tmp/api/openapi.yaml"
{
    printf 'FROM %s\n' "${RETICORA_TEST_BASE_IMAGE:-alpine:3.24}"
    # Keep COPY, its permissions fix, the runtime USER and the build-time
    # readability assertion exactly as shipped in the backend Dockerfile.
    sed -n '/^# The API contract:/,$p' backend/Dockerfile
} > "$tmp/Dockerfile"
docker build --network=none -t "$image" "$tmp"
docker run --rm --network=none --entrypoint sh "$image" -ec '
    test "$(id -u)" -ne 0
    test -x /etc/reticora
    test ! -w "$RETICORA_OPENAPI_SPEC_PATH"
    cat "$RETICORA_OPENAPI_SPEC_PATH"
' > "$tmp/actual.yaml"
cmp api/openapi.yaml "$tmp/actual.yaml"
printf 'PASS: unprivileged server user can read the complete OpenAPI contract\n'
