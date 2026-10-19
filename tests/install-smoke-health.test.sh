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
# $STUB_PS_RC), "compose … exec" (the psql query of the server's database
# sessions) records its arguments in $STUB_EXEC_LOG and prints $STUB_SESSIONS
# (or fails with $STUB_EXEC_RC).
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
        exec)
            printf '%s\n' "$*" >> "$STUB_EXEC_LOG"
            [ "${STUB_EXEC_RC:-0}" -eq 0 ] || { echo 'psql: error: connection to server failed' >&2; exit "$STUB_EXEC_RC"; }
            printf '%s' "$STUB_SESSIONS"; exit 0 ;;
    esac
done
exit 2
STUBEOF
chmod +x "$STUB"

row() { printf '{"Service":"%s","Name":"rc-%s-1","State":"%s","Health":"%s"}' "$1" "$1" "$2" "$3"; }

expect() {
    local name="$1" want="$2"
    local rc=0
    DOCKER="$STUB" DB_SESSION_RETRIES=2 DB_SESSION_DELAY=0 "$SCRIPT" "$TMP_ROOT/compose" .env >"$TMP_ROOT/out" 2>&1 || rc=$?
    if { [ "$want" = pass ] && [ "$rc" -eq 0 ]; } || { [ "$want" = fail ] && [ "$rc" -ne 0 ]; }; then
        PASS=$((PASS + 1)); printf 'ok   %s\n' "$name"
    else
        FAIL=$((FAIL + 1)); printf 'FAIL %s (exit %d)\n' "$name" "$rc"; sed 's/^/     /' "$TMP_ROOT/out"
    fi
}

reset() {
    export STUB_SERVICES=$'postgres\nserver\ncertbot' STUB_CONFIG_RC=0 STUB_PS_RC=0 STUB_EXEC_RC=0
    export STUB_SESSIONS=$'reticora_runtime|f|f\n' STUB_EXEC_LOG="$TMP_ROOT/exec.log"
    : > "$STUB_EXEC_LOG"
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

reset
STUB_PS="$(row postgres running healthy)
$(row server running healthy)"
expect "certbot is not expected without RETICORA_TLS_DOMAIN" pass

reset
STUB_PS="$(row postgres running healthy)
$(row server running healthy)"
echo "RETICORA_TLS_DOMAIN=cmdb.example.com" > "$TMP_ROOT/compose/.env"
expect "certbot is expected with RETICORA_TLS_DOMAIN" fail
: > "$TMP_ROOT/compose/.env"

# ─── server database login (TEN-03, WP-348) ──────────────────────────────────

check() {
    local name="$1"; shift
    if "$@"; then
        PASS=$((PASS + 1)); printf 'ok   %s\n' "$name"
    else
        FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$name"; sed 's/^/     /' "$STUB_EXEC_LOG"
    fi
}

reset
expect "server sessions as reticora_runtime without SUPERUSER/BYPASSRLS pass" pass
check "the session query selects the server's sessions by application_name" \
    grep -q "exec -T postgres psql -U reticora -d reticora -XAtc SELECT DISTINCT a.usename, r.rolsuper, r.rolbypassrls FROM pg_stat_activity a JOIN pg_roles r ON r.oid = a.usesysid WHERE a.application_name = 'reticora-server'" "$STUB_EXEC_LOG"

reset
STUB_SESSIONS=$'reticora|t|t\n'
expect "server logged in as the bootstrap superuser fails" fail

reset
STUB_SESSIONS=$'reticora|t|t\nreticora_runtime|f|f\n'
expect "a superuser session next to the runtime login fails" fail

reset
STUB_SESSIONS=$'reticora_runtime|f|t\n'
expect "a runtime login with BYPASSRLS fails" fail

reset
STUB_SESSIONS=$'reticora_runtime|t|f\n'
expect "a runtime login with SUPERUSER fails" fail

reset
STUB_SESSIONS=$'other_login|f|f\n'
expect "another login than reticora_runtime fails" fail

reset
STUB_SESSIONS=""
expect "no server session in pg_stat_activity fails" fail
check "a missing session is retried" test "$(grep -c pg_stat_activity "$STUB_EXEC_LOG")" -eq 2

reset
STUB_EXEC_RC=1
expect "psql error fails" fail

reset
STUB_SERVICES=$'collector\nnats'
STUB_PS="$(row collector running healthy)
$(row nats running healthy)"
STUB_SESSIONS=""
expect "a stack without postgres and server skips the login check" pass
check "no session query without postgres and server" test ! -s "$STUB_EXEC_LOG"

echo
echo "passed: $PASS, failed: $FAIL"
[ "$FAIL" -eq 0 ]
