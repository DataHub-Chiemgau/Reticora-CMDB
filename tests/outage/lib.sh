#!/usr/bin/env bash
#
# lib.sh — helpers of the outage suite (WP-104, WP-449, OPS-06). Sourced by
# run.sh, load.sh and smtp.sh. Every function that reaches the installed
# stack (compose, psql_exec, server_url, api, nats_jsz, mailpit_get) is a
# plain function, so tests/outage/config.test.sh replaces them with stubs.

COMPOSE_DIR="${COMPOSE_DIR:-deploy/docker-compose}"
ENV_FILE="${ENV_FILE:-.env}"
OUTAGE_REPORT="${OUTAGE_REPORT:-outage-report.md}"
# Seconds a dependency outage lasts at least and the time allowed for
# recovery.
OUTAGE_SECONDS="${OUTAGE_SECONDS:-30}"
RECOVERY_TIMEOUT="${RECOVERY_TIMEOUT:-180}"
# NATS monitoring and the Mailpit API as the stack publishes them on the
# host (deploy/docker-compose/docker-compose.yml, smtp.sh overlay).
NATS_MONITOR_URL="${NATS_MONITOR_URL:-http://127.0.0.1:8222}"
MAILPIT_URL="${MAILPIT_URL:-http://127.0.0.1:8025}"

PASSED=0
FAILED=0
NOT_APPLICABLE=0

compose() {
    (cd "$COMPOSE_DIR" && docker compose --env-file "$ENV_FILE" "$@")
}

env_value() {
    grep -E "^$1=" "$COMPOSE_DIR/$ENV_FILE" | tail -n 1 | cut -d= -f2-
}

server_url() {
    printf 'http://localhost:%s' "$(env_value RETICORA_SERVER_PORT || true)"
}

# readyz prints "<http status> <body>" of /readyz.
readyz() {
    local out
    out="$(curl -sS --max-time 10 -w '\n%{http_code}' "$(server_url)/readyz" 2>/dev/null || printf '\n000')"
    printf '%s %s' "$(printf '%s' "$out" | tail -n 1)" "$(printf '%s' "$out" | head -n -1 | tr -d '\n')"
}

# wait_for <seconds> <description> <command …> polls until the command
# succeeds.
wait_for() {
    local timeout="$1" what="$2"; shift 2
    local deadline=$((SECONDS + timeout))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if "$@"; then return 0; fi
        sleep 2
    done
    printf '   timed out after %ss waiting for %s\n' "$timeout" "$what" >&2
    return 1
}

check_unavailable() { # <check name>
    local r; r="$(readyz)"
    case "$r" in 503\ *"\"$1\":\"unavailable\""*) return 0 ;; esac
    return 1
}

ready() {
    local r; r="$(readyz)"
    case "$r" in 200\ *) return 0 ;; esac
    return 1
}

server_alive() {
    [ "$(docker inspect -f '{{.State.Running}}' "$(compose ps -q server)" 2>/dev/null)" = "true" ]
}

server_restarts() {
    docker inspect -f '{{.RestartCount}}' "$(compose ps -q server)" 2>/dev/null || printf '?'
}

psql_exec() {
    compose exec -T postgres psql -U reticora -d reticora -v ON_ERROR_STOP=1 -tAq "$@"
}

# db_admin_url is the bootstrap login of the installation on the host port
# of the stack; only cmd/seed uses it (load.sh, load_setup).
db_admin_url() {
    local password
    password="$(env_value RETICORA_DB_PASSWORD | tr -d "\"'")"
    printf 'postgres://reticora:%s@127.0.0.1:5432/reticora?sslmode=disable' "$(jq -rn --arg p "$password" '$p | @uri')"
}

# api <method> <path> [json body]: one request of the scenarios' API key
# (OUTAGE_KEY_HEADER, a curl header file, so the key is not on the command
# line). Prints the HTTP status, 000 without an answer; the body goes to
# $API_OUT.
api() {
    local method="$1" path="$2" body="${3:-}"
    local args=(-sS -o "$API_OUT" -w '%{http_code}' --max-time "${API_TIMEOUT:-30}" -X "$method"
        -H "@$OUTAGE_KEY_HEADER" -H 'Content-Type: application/json')
    [ -n "$body" ] && args+=(--data-binary "$body")
    curl "${args[@]}" "$(server_url)/api/v1$path" 2>/dev/null || true
}

# nats_jsz prints the JetStream report of the NATS monitor (streams).
nats_jsz() {
    curl -sS --max-time 10 "$NATS_MONITOR_URL/jsz?accounts=true&streams=true" 2>/dev/null
}

# mailpit_get <path and query>: GET on the Mailpit API.
mailpit_get() {
    curl -sS --max-time 10 "$MAILPIT_URL$1" 2>/dev/null
}

# uuid4 prints a random UUID (record_id of an ingest record).
uuid4() {
    if [ -r /proc/sys/kernel/random/uuid ]; then
        cat /proc/sys/kernel/random/uuid
    else
        python3 -c 'import uuid; print(uuid.uuid4())'
    fi
}

# utc_now prints the current time as an ISO timestamp PostgreSQL and the
# API accept.
utc_now() {
    date -u '+%Y-%m-%dT%H:%M:%SZ'
}

report() { # <status> <scenario> <details>
    case "$1" in
        PASS) PASSED=$((PASSED + 1)) ;;
        FAIL) FAILED=$((FAILED + 1)) ;;
        N/A) NOT_APPLICABLE=$((NOT_APPLICABLE + 1)) ;;
    esac
    printf '%-4s %s — %s\n' "$1" "$2" "$3"
    printf '| %s | %s | %s |\n' "$2" "$1" "$3" >> "$OUTAGE_REPORT"
}

# detail <section> <markdown line>: a line of the evidence that run.sh
# appends to the protocol after the result table, by section: load (the
# counts of every load), recovery (queue, outbox, restarts), smtp (delivery
# status of the SMTP scenario).
detail() {
    printf '%s\n' "$2" >> "${OUTAGE_DETAILS:?OUTAGE_DETAILS is not set}.$1"
}

# joined <item …>: the items separated by "; ".
joined() {
    local out
    out="$(printf '%s; ' "$@")"
    printf '%s' "${out%; }"
}

# oneline <text>: text fit for a table cell of the protocol.
oneline() {
    printf '%s' "$1" | tr '|\n\r' '/  ' | cut -c1-240
}
