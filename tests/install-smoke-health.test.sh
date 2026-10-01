#!/usr/bin/env bash
#
# install-smoke-health.test.sh — tests for tests/install-smoke-health.sh with a
# stubbed docker binary (WP-092). Runs without Docker.
#
# Usage: ./tests/install-smoke-health.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
SCRIPT="$(pwd)/tests/install-smoke-health.sh"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
mkdir -p "$TMP_ROOT/compose"
touch "$TMP_ROOT/compose/docker-compose.yml" "$TMP_ROOT/compose/.env"

PASS=0
FAIL=0

# Stub docker: "compose … config --services" prints $STUB_SERVICES (or fails
# with $STUB_CONFIG_RC), "compose … ps" prints $STUB_PS (or fails with
# $STUB_PS_RC).
STUB="$TMP_ROOT/docker"
cat > "$STUB" <<'STUBEOF'
#!/usr/bin/env bash
for arg in "$@"; do
    case "$arg" in
        config)
            [ "${STUB_CONFIG_RC:-0}" -eq 0 ] || { echo "service \"x\" refers to undefined volume" >&2; exit "$STUB_CONFIG_RC"; }
            printf '%s\n' "$STUB_SERVICES"; exit 0 ;;
        ps)
            [ "${STUB_PS_RC:-0}" -eq 0 ] || { echo "Cannot connect to the Docker daemon" >&2; exit "$STUB_PS_RC"; }
            printf '%s\n' "$STUB_PS"; exit 0 ;;
    esac
done
exit 2
STUBEOF
chmod +x "$STUB"

row() { printf '{"Service":"%s","Name":"rc-%s-1","State":"%s","Health":"%s"}' "$1" "$1" "$2" "$3"; }

expect() {
    local name="$1" want="$2"
    local rc=0
    DOCKER="$STUB" "$SCRIPT" "$TMP_ROOT/compose" .env >"$TMP_ROOT/out" 2>&1 || rc=$?
    if { [ "$want" = pass ] && [ "$rc" -eq 0 ]; } || { [ "$want" = fail ] && [ "$rc" -ne 0 ]; }; then
        PASS=$((PASS + 1)); printf 'ok   %s\n' "$name"
    else
        FAIL=$((FAIL + 1)); printf 'FAIL %s (exit %d)\n' "$name" "$rc"; sed 's/^/     /' "$TMP_ROOT/out"
    fi
}

reset() {
    export STUB_SERVICES=$'postgres\nserver\ncertbot' STUB_CONFIG_RC=0 STUB_PS_RC=0
    STUB_PS="$(row postgres running healthy)
$(row server running healthy)
$(row certbot running "")"
    export STUB_PS
}

reset
expect "all services running and healthy (JSON lines)" pass

reset
STUB_PS="[$(row postgres running healthy),$(row server running healthy),$(row certbot running "")]"
expect "all services running and healthy (JSON array)" pass

reset
STUB_PS="$(row postgres running healthy)
$(row server running unhealthy)
$(row certbot running "")"
expect "unhealthy container fails" fail

reset
STUB_PS="$(row postgres running healthy)
$(row server running starting)
$(row certbot running "")"
expect "container still starting fails" fail

reset
STUB_PS="$(row postgres running healthy)
$(row server exited "")
$(row certbot running "")"
expect "exited container fails" fail

reset
STUB_PS="$(row postgres running healthy)
$(row certbot running "")"
expect "missing container of an expected service fails" fail

reset
STUB_PS_RC=1
expect "compose ps error fails" fail

reset
STUB_CONFIG_RC=1
expect "compose config error fails" fail

reset
STUB_PS='{"Service":"postgres", not json'
expect "invalid JSON fails" fail

reset
STUB_PS=""
expect "empty container list fails" fail

echo
echo "passed: $PASS, failed: $FAIL"
[ "$FAIL" -eq 0 ]
