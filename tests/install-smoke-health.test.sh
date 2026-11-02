#!/usr/bin/env bash
#
# install-smoke-health.test.sh — tests for tests/install-smoke-health.sh with a
# stubbed docker binary (WP-092) and, for scaled services and one-off
# containers, against a real compose project (WP-440, F-288): a service
# "web" ($SMOKE_HEALTH_TEST_IMAGE, default redis:7-alpine, the image of the
# stack's redis service) with --scale web=2, removed again at the end.
# Without Docker the real cases are skipped; with CI=true they fail then.
#
# Usage: ./tests/install-smoke-health.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
SCRIPT="$(pwd)/tests/install-smoke-health.sh"

TMP_ROOT="$(mktemp -d)"
DOCKER_PROJECT=""
cleanup() {
    [ -z "$DOCKER_PROJECT" ] || docker_cleanup
    rm -rf "$TMP_ROOT"
}
trap cleanup EXIT
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
# row_n <service> <container> <state> <health> <labels JSON> — a row with the
# labels docker compose ps prints ("k=v,…") or a label object.
row_n() {
    printf '{"Service":"%s","Name":"%s","State":"%s","Health":"%s","Labels":%s}' "$1" "$2" "$3" "$4" "$5"
}

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

# ─── several containers per service (F-288) ──────────────────────────────────

SERVICE_LABELS='"com.docker.compose.oneoff=False,com.docker.compose.project=rc,com.docker.compose.service=server"'
ONEOFF_LABELS='"com.docker.compose.oneoff=True,com.docker.compose.project=rc,com.docker.compose.service=server"'

# assert <name> <command…> — like check, with the command's own output.
assert() {
    local name="$1" out
    shift
    if out="$("$@" 2>&1)"; then
        PASS=$((PASS + 1)); printf 'ok   %s\n' "$name"
    else
        FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$name"; printf '%s\n' "$out" | sed 's/^/     /'
    fi
}

# output_has <regex> — a line of the last run's output matches.
output_has() { grep -Eq "$1" "$TMP_ROOT/out" || { echo "no line /$1/ in:"; cat "$TMP_ROOT/out"; return 1; }; }

reset
STUB_PS="$(row postgres running healthy)
$(row_n server rc-server-1 running healthy "$SERVICE_LABELS")
$(row_n server rc-server-2 running healthy "$SERVICE_LABELS")
$(row certbot running "")"
expect "every replica of a scaled service running and healthy passes" pass

reset
STUB_PS="$(row postgres running healthy)
$(row_n server rc-server-1 running healthy "$SERVICE_LABELS")
$(row_n server rc-server-2 exited "" "$SERVICE_LABELS")
$(row certbot running "")"
expect "an exited replica after a running one fails" fail

reset
STUB_PS="$(row postgres running healthy)
$(row_n server rc-server-1 running healthy "$SERVICE_LABELS")
$(row_n server rc-server-2 running unhealthy "$SERVICE_LABELS")
$(row certbot running "")"
expect "an unhealthy replica after a healthy one fails" fail

reset
STUB_PS="$(row postgres running healthy)
$(row_n server rc-server-1 running healthy "$SERVICE_LABELS")
$(row_n server rc-server-run-0123abcd exited "" "$ONEOFF_LABELS")
$(row certbot running "")"
expect "an exited one-off container next to the running service passes" pass

reset
STUB_PS="$(row postgres running healthy)
$(row_n server rc-server-run-0123abcd running healthy "$ONEOFF_LABELS")
$(row certbot running "")"
expect "a service with only a one-off container counts as missing" fail
assert "the service without a container is reported as missing" output_has '^server +missing'

reset
STUB_PS="$(row postgres running healthy)
$(row_n server rc-server-1 running healthy '{"com.docker.compose.oneoff":"False"}')
$(row_n server rc-server-run-0123abcd exited "" '{"com.docker.compose.oneoff":"True"}')
$(row certbot running "")"
expect "a one-off label object is recognised as well" pass

# ─── real compose project: scaled service, one-off container (F-288) ─────────

docker_available() { docker info >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; }

dc() { docker compose --env-file "$DOCKER_DIR/.env" -f "$DOCKER_DIR/docker-compose.yml" "$@"; }

docker_cleanup() {
    dc down -v --remove-orphans --timeout 1 >/dev/null 2>&1 || true
    docker ps -aq --filter "label=com.docker.compose.project=$DOCKER_PROJECT" | xargs -r docker rm -f >/dev/null 2>&1 || true
}

# expect_real <name> pass|fail — the script with the real docker binary.
expect_real() {
    local name="$1" want="$2" rc=0
    "$SCRIPT" "$DOCKER_DIR" .env >"$TMP_ROOT/out" 2>&1 || rc=$?
    if { [ "$want" = pass ] && [ "$rc" -eq 0 ]; } || { [ "$want" = fail ] && [ "$rc" -eq 1 ]; }; then
        PASS=$((PASS + 1)); printf 'ok   %s\n' "$name"
    else
        FAIL=$((FAIL + 1)); printf 'FAIL %s (exit %d)\n' "$name" "$rc"; sed 's/^/     /' "$TMP_ROOT/out"
    fi
}

# oneoff_exited — compose ps lists an exited one-off container of web, so the
# one-off case does not pass merely because compose ps hides it.
oneoff_exited() {
    local ps
    ps="$(dc ps --all --format json)" || return 1
    jq -se 'if length == 1 and (.[0] | type) == "array" then .[0] else . end
        | map(select(.Service == "web" and .State == "exited"
            and ((.Labels // "") | test("(^|,)com\\.docker\\.compose\\.oneoff=True(,|$)"))))
        | length == 1' <<<"$ps" >/dev/null \
        || { echo "no exited one-off container of web in docker compose ps:"; printf '%s\n' "$ps"; return 1; }
}

if docker_available; then
    DOCKER_PROJECT="reticora-smoke-health-$$"
    DOCKER_DIR="$TMP_ROOT/real-compose"
    mkdir -p "$DOCKER_DIR"
    printf 'name: %s\nservices:\n  web:\n    image: %s\n' "$DOCKER_PROJECT" "${SMOKE_HEALTH_TEST_IMAGE:-redis:7-alpine}" > "$DOCKER_DIR/docker-compose.yml"
    : > "$DOCKER_DIR/.env"
    if dc up -d --scale web=2 --wait >"$TMP_ROOT/up.log" 2>&1; then
        expect_real "docker: scale web=2, both replicas running" pass
        docker stop -t 1 "$DOCKER_PROJECT-web-2" >/dev/null
        expect_real "docker: scale web=2 with one replica stopped" fail
        assert "docker: the stopped replica is reported" output_has "^web +exited +- +$DOCKER_PROJECT-web-2\$"
        docker start "$DOCKER_PROJECT-web-2" >/dev/null
        dc up -d --scale web=2 --wait >>"$TMP_ROOT/up.log" 2>&1
        dc run -T --no-deps web redis-server --version >/dev/null 2>&1
        assert "docker: an exited one-off container of web is listed" oneoff_exited
        expect_real "docker: exited one-off container next to the running service" pass
    else
        FAIL=$((FAIL + 1)); printf 'FAIL docker: compose project with web=2 starts\n'; sed 's/^/     /' "$TMP_ROOT/up.log"
    fi
elif [ "${CI:-}" = true ]; then
    FAIL=$((FAIL + 1)); printf 'FAIL docker: Docker with compose is required for the real cases (CI=true)\n'
else
    printf 'skip docker: no Docker with compose; the real cases (scaled service, one-off container) did not run\n'
fi

echo
echo "passed: $PASS, failed: $FAIL"
[ "$FAIL" -eq 0 ]
