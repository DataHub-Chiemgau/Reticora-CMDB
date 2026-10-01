#!/usr/bin/env bash
#
# install-smoke-health.sh — strict health check of the compose stack after an
# installation (INS-01, TEC-09, WP-092).
#
# Fails when
#   - docker compose cannot list the services or containers (compose error),
#   - the container list is not valid JSON,
#   - an expected service has no container, or its container is not running,
#   - a container with a health check is not "healthy".
# The expected services are the ones `docker compose config --services`
# reports for the active profiles, so a service added to the stack is checked
# without changing this script. The certbot renewal service is only started
# by install-cloud.sh when RETICORA_TLS_DOMAIN is set; without a domain it is
# not expected.
#
# Usage: tests/install-smoke-health.sh <compose-dir> [env-file]
#   DOCKER                  docker binary (default: docker; tests use a stub)
#   COMPOSE_FILES           extra "-f file" arguments, space separated
#                           (default: docker-compose.yml plus the override file
#                           when it exists, as install-cloud.sh does)
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

failed=0
printf '%-14s %-10s %-10s %s\n' SERVICE STATE HEALTH CONTAINER
while IFS= read -r service; do
    row=$(jq -c --arg s "$service" '[.[] | select(.Service == $s)] | first // empty' <<<"$containers")
    if [ -z "$row" ]; then
        printf '%-14s %-10s %-10s %s\n' "$service" missing - -
        failed=1
        continue
    fi
    state=$(jq -r '.State // ""' <<<"$row")
    health=$(jq -r '.Health // ""' <<<"$row")
    name=$(jq -r '.Name // ""' <<<"$row")
    printf '%-14s %-10s %-10s %s\n' "$service" "${state:--}" "${health:--}" "$name"
    if [ "$state" != "running" ]; then
        failed=1
    elif [ -n "$health" ] && [ "$health" != "healthy" ]; then
        failed=1
    fi
done <<<"$services"

if [ "$failed" -ne 0 ]; then
    echo "FAIL: not every expected service is running and healthy"
    exit 1
fi
echo "OK: $(wc -l <<<"$services" | tr -d ' ') services running and healthy"
