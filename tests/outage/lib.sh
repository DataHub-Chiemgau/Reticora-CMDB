#!/usr/bin/env bash
#
# lib.sh — helpers of the outage suite (WP-104, OPS-06). Sourced by run.sh.

COMPOSE_DIR="${COMPOSE_DIR:-deploy/docker-compose}"
ENV_FILE="${ENV_FILE:-.env}"
OUTAGE_REPORT="${OUTAGE_REPORT:-outage-report.md}"
# Seconds a dependency outage lasts and the time allowed for recovery.
OUTAGE_SECONDS="${OUTAGE_SECONDS:-30}"
RECOVERY_TIMEOUT="${RECOVERY_TIMEOUT:-180}"

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

report() { # <status> <scenario> <details>
    case "$1" in
        PASS) PASSED=$((PASSED + 1)) ;;
        FAIL) FAILED=$((FAILED + 1)) ;;
        N/A) NOT_APPLICABLE=$((NOT_APPLICABLE + 1)) ;;
    esac
    printf '%-4s %s — %s\n' "$1" "$2" "$3"
    printf '| %s | %s | %s |\n' "$2" "$1" "$3" >> "$OUTAGE_REPORT"
}
