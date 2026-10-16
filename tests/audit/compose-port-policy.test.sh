#!/usr/bin/env bash
#
# compose-port-policy.test.sh — Phase-1 acceptance audit, group
# T4-collector-load, DEFECT test (fails until the defect is fixed; not wired
# into CI).
#
# WP-146 (COL-08, docs/betrieb/firewall-matrix.md). Finding P2 "Compose
# publishes the REST API (8080), Keycloak (8180) and the frontend plain port
# (3000) on all interfaces, contradicting the firewall matrix and COL-08":
# the port mappings of server and keycloak carry no host IP
# (docker-compose.yml), so the plain-HTTP API and the Keycloak admin console
# are reachable from the network although the matrix lists only 443/80/4222
# as inbound ports of the platform.
#
# Requirement: in a TLS installation (RETICORA_TLS_DOMAIN set, frontend on
# port 80 as install-cloud.sh configures it) only 80, 443 and 4222 are
# published on all interfaces; every other published port is bound to
# loopback. The 80/443/4222 mappings are the control.
#
# Needs docker compose (only `docker compose config`, no daemon) and jq.
# Retest: ./tests/audit/compose-port-policy.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
ROOT="$(pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

if ! docker compose version >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then
    echo "FAIL docker compose and jq are required"
    exit 1
fi

# The variables compose requires (${VAR:?...}) get dummy values; the TLS
# installation of install-cloud.sh sets the domain and port 80.
COMPOSE="$ROOT/deploy/docker-compose/docker-compose.yml"
{
    grep -oE '\$\{[A-Z0-9_]+:\?' "$COMPOSE" | sort -u | sed -E 's/^\$\{([A-Z0-9_]+):\?$/\1=audit-dummy/'
    echo "RETICORA_TLS_DOMAIN=cmdb.example.com"
    echo "RETICORA_FRONTEND_PORT=80"
} > "$TMP/tls.env"

if ! docker compose --env-file "$TMP/tls.env" -f "$COMPOSE" config --format json > "$TMP/config.json" 2> "$TMP/config.err"; then
    echo "FAIL docker compose config: $(grep -v level=warning "$TMP/config.err" | head -3)"
    exit 1
fi

jq -r '.services | to_entries[] | .key as $s | (.value.ports // [])[] |
    "\($s) \(if (.host_ip // "") == "" then "0.0.0.0" else .host_ip end) \(.published) \(.target)"' \
    "$TMP/config.json" > "$TMP/ports.txt"

allowed=" 80 443 4222 "
seen_allowed=0
while read -r service host published target; do
    case "$host" in
        127.0.0.1|::1|localhost)
            continue ;;
    esac
    if [[ "$allowed" == *" $published "* ]]; then
        seen_allowed=$((seen_allowed + 1))
        ok "$service publishes $published -> $target on all interfaces (firewall matrix)"
    else
        fail "COL-08 violated: $service publishes $published -> $target on $host (all interfaces); the firewall matrix allows only 80/443/4222"
    fi
done < "$TMP/ports.txt"

if [ "$seen_allowed" -lt 3 ]; then
    fail "control: expected the frontend (80, 443) and nats-edge (4222) on all interfaces, found $seen_allowed of them"
fi

echo
echo "compose-port-policy: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
