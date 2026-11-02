#!/usr/bin/env bash
#
# install-smoke-health.sh — strict health check of the compose stack after an
# installation (INS-01, TEC-09, WP-092).
#
# Fails when
#   - docker compose cannot list the services or containers (compose error),
#   - the container list is not valid JSON,
#   - an expected service has no container, or one of its containers is not
#     running (every container of a scaled service counts, F-288),
#   - a container with a health check is not "healthy",
#   - the server's database sessions (pg_stat_activity, application_name
#     reticora-server) are missing or do not all log in as reticora_runtime
#     with rolsuper = false and rolbypassrls = false (TEN-03, WP-348). The
#     check runs when the stack has both a postgres and a server service.
# The expected services are the ones `docker compose config --services`
# reports for the active profiles, so a service added to the stack is checked
# without changing this script. One-off containers (`docker compose run`,
# label com.docker.compose.oneoff=True) are not part of a service and are
# ignored, whatever their state. The certbot renewal service is only started
# by install-cloud.sh when RETICORA_TLS_DOMAIN is set; without a domain it is
# not expected.
#
# Usage: tests/install-smoke-health.sh <compose-dir> [env-file]
#   DOCKER                  docker binary (default: docker; tests use a stub)
#   COMPOSE_FILES           extra "-f file" arguments, space separated
#                           (default: docker-compose.yml plus the override file
#                           when it exists, as install-cloud.sh does)
#   DB_SESSION_RETRIES      attempts to find the server's sessions (default 6)
#   DB_SESSION_DELAY        seconds between the attempts (default 5)
set -uo pipefail

compose_dir="${1:?usage: install-smoke-health.sh <compose-dir> [env-file]}"
env_file="${2:-.env}"
DOCKER="${DOCKER:-docker}"

cd "$compose_dir" || { echo "FAIL: compose directory $compose_dir not found"; exit 1; }

if [ -z "${COMPOSE_FILES:-}" ]; then
    COMPOSE_FILES="-f docker-compose.yml"
    [ -f docker-compose.override.yml ] && COMPOSE_FILES="$COMPOSE_FILES -f docker-compose.override.yml"
fi
# shellcheck disable=SC2206 # intentional word splitting of the -f list
compose=("$DOCKER" compose --env-file "$env_file" $COMPOSE_FILES)

if ! services=$("${compose[@]}" config --services 2>&1); then
    echo "FAIL: docker compose config failed:"
    echo "$services"
    exit 1
fi
services=$(printf '%s\n' "$services" | sed '/^[[:space:]]*$/d' | sort -u)
tls_domain=$(sed -n 's/^RETICORA_TLS_DOMAIN=//p' "$env_file" 2>/dev/null | tail -n 1 | tr -d "\"' ")
if [ -z "$tls_domain" ]; then
    services=$(printf '%s\n' "$services" | grep -vx certbot || true)
fi
if [ -z "$services" ]; then
    echo "FAIL: docker compose config lists no services"
    exit 1
fi

if ! raw=$("${compose[@]}" ps --all --format json 2>&1); then
    echo "FAIL: docker compose ps failed:"
    echo "$raw"
    exit 1
fi
# Compose prints one JSON object per line (v2.21+) or one JSON array (older).
if ! containers=$(printf '%s\n' "$raw" | jq -cs 'if length == 1 and (.[0] | type) == "array" then .[0] else . end' 2>&1); then
    echo "FAIL: docker compose ps returned invalid JSON:"
    echo "$raw"
    exit 1
fi

# Labels: one "key=value,…" string (docker compose ps), an object elsewhere.
# shellcheck disable=SC2016 # jq program
service_containers='def oneoff: (.Labels // "") as $l
    | if ($l | type) == "object" then $l["com.docker.compose.oneoff"] == "True"
      else ($l | tostring | test("(^|,)com\\.docker\\.compose\\.oneoff=True(,|$)")) end;
    .[] | select(.Service == $s and (oneoff | not))'

failed=0
printf '%-14s %-10s %-10s %s\n' SERVICE STATE HEALTH CONTAINER
while IFS= read -r service; do
    # Every container of the service, a stopped replica of a scaled one
    # included (F-288).
    rows=$(jq -c --arg s "$service" "$service_containers" <<<"$containers")
    if [ -z "$rows" ]; then
        printf '%-14s %-10s %-10s %s\n' "$service" missing - -
        failed=1
        continue
    fi
    while IFS= read -r row; do
        state=$(jq -r '.State // ""' <<<"$row")
        health=$(jq -r '.Health // ""' <<<"$row")
        name=$(jq -r '.Name // ""' <<<"$row")
        printf '%-14s %-10s %-10s %s\n' "$service" "${state:--}" "${health:--}" "$name"
        if [ "$state" != "running" ]; then
            failed=1
        elif [ -n "$health" ] && [ "$health" != "healthy" ]; then
            failed=1
        fi
    done <<<"$rows"
done <<<"$services"

if [ "$failed" -ne 0 ]; then
    echo "FAIL: not every expected service is running and healthy"
    exit 1
fi

# The server must not log in as the bootstrap superuser: SET ROLE
# reticora_app is undone by RESET ROLE (F-057). usename in pg_stat_activity is
# the session user, i.e. the login, not the role switched into.
if grep -qx postgres <<<"$services" && grep -qx server <<<"$services"; then
    query="SELECT DISTINCT a.usename, r.rolsuper, r.rolbypassrls FROM pg_stat_activity a JOIN pg_roles r ON r.oid = a.usesysid WHERE a.application_name = 'reticora-server' ORDER BY 1"
    sessions=""
    for attempt in $(seq 1 "${DB_SESSION_RETRIES:-6}"); do
        if ! sessions=$("${compose[@]}" exec -T postgres psql -U reticora -d reticora -XAtc "$query" 2>&1); then
            echo "FAIL: cannot query pg_stat_activity:"
            echo "$sessions"
            exit 1
        fi
        sessions=$(printf '%s\n' "$sessions" | sed '/^[[:space:]]*$/d')
        [ -n "$sessions" ] && break
        [ "$attempt" -lt "${DB_SESSION_RETRIES:-6}" ] && sleep "${DB_SESSION_DELAY:-5}"
    done
    if [ -z "$sessions" ]; then
        echo "FAIL: no database session of the server (application_name reticora-server) in pg_stat_activity"
        exit 1
    fi
    printf 'server database sessions (login|rolsuper|rolbypassrls): %s\n' "$(tr '\n' ' ' <<<"$sessions")"
    if [ "$sessions" != "reticora_runtime|f|f" ]; then
        echo "FAIL: the server must log in only as reticora_runtime without SUPERUSER and BYPASSRLS (TEN-03)"
        exit 1
    fi
fi
echo "OK: $(wc -l <<<"$services" | tr -d ' ') services running and healthy"
