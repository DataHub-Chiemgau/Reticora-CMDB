#!/usr/bin/env bash
#
# compose-port-policy.test.sh — published ports of the shipped compose stack
# (COL-08, PRI-07; WP-146, WP-431, F-103). Runs in the CI job
# installer-script-tests (.github/workflows/install-smoke.yml).
#
# docs/betrieb/firewall-matrix.md: from outside, the platform is reached on
# 80 and 443 (frontend) and 4222 (nats-edge) only. The REST API (8080) and
# Keycloak with its admin console (8180) speak plain HTTP and are bound to
# 127.0.0.1. An installation without a TLS domain (IP address) publishes both
# on all interfaces only through the opt-in RETICORA_PUBLISH_DIRECT_PORTS=true
# that install-cloud.sh sets there, with a warning; its web UI is the
# frontend's plain-HTTP port 3000. With a TLS domain that port is 80 and 3000
# is not published on all interfaces.
#
# The .env of each case is the one install-cloud.sh writes: collect_config
# (non-interactive, on a previous .env) and write_env of the installer,
# sourced without its main call. Cases:
#   tls installation          domain: only 80/443/4222 on all interfaces,
#                             8080/8180 on 127.0.0.1, 3000 not public
#   tls after ip installation the previous .env of an IP installation (opt-in,
#                             port 3000, issuer on 8180) gets a domain: the
#                             same, and the installer warns about the issuer
#   ip installation           no domain: 3000/443/4222 and, through the
#                             opt-in, 8080/8180 on 0.0.0.0; the installer
#                             warns
#   ip without opt-in         the same .env without the installer's bind
#                             address (the compose default, e.g. an .env from
#                             before WP-431): 8080/8180 on 127.0.0.1
#   control                   a further port on all interfaces is reported
#
# Needs docker compose (only `docker compose config`, no daemon), jq and
# openssl (secrets of collect_config).
# Retest: ./tests/audit/compose-port-policy.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
ROOT="$(pwd)"
COMPOSE="$ROOT/deploy/docker-compose/docker-compose.yml"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

if ! docker compose version >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1 || ! command -v openssl >/dev/null 2>&1; then
    echo "FAIL docker compose, jq and openssl are required"
    exit 1
fi

# The installer without its main call (last line), next to the preflight
# helpers it sources.
mkdir -p "$TMP/harness"
head -n -1 install-cloud.sh > "$TMP/harness/install-cloud.sh"
cp install-preflight.sh "$TMP/harness/"

# clean <command…> — the command without the RETICORA_* and KEYCLOAK_*
# variables of the caller's environment (they override .env values).
clean() {
    local unset=() v
    for v in $(compgen -e); do
        case "$v" in RETICORA_*|KEYCLOAK_*) unset+=(-u "$v") ;; esac
    done
    env "${unset[@]}" "$@"
}

# installer_env <case> <previous .env lines…> — the .env install-cloud.sh
# writes on a run (non-interactive) over that previous .env:
# $TMP/<case>/.env, the installer's output in $TMP/<case>/installer.log.
installer_env() {
    local dir="$TMP/$1"
    shift
    mkdir -p "$dir"
    printf '%s\n' "$@" > "$dir/.env"
    # shellcheck disable=SC2016 # expanded by the inner shell
    clean bash -c 't_harness="$1" t_env="$2"; set --; . "$t_harness"
        NON_INTERACTIVE=1; ENV_FILE="$t_env"; collect_config && write_env' \
        _ "$TMP/harness/install-cloud.sh" "$dir/.env" > "$dir/installer.log" 2>&1
}

# ports <case> [compose file] — every published port of the stack with the
# case's .env as "<service> <host ip> <published> <target>" in
# $TMP/<case>/ports.txt; an empty host IP means all interfaces (0.0.0.0).
ports() {
    local dir="$TMP/$1" files=(-f "$COMPOSE")
    [ -n "${2:-}" ] && files+=(-f "$2")
    if ! clean docker compose --env-file "$dir/.env" "${files[@]}" config --format json > "$dir/config.json" 2> "$dir/config.err"; then
        fail "$1: docker compose config: $(grep -v level=warning "$dir/config.err" | head -3)"
        return 1
    fi
    jq -r '.services | to_entries[] | .key as $s | (.value.ports // [])[] |
        "\($s) \(if (.host_ip // "") == "" then "0.0.0.0" else .host_ip end) \(.published) \(.target)"' \
        "$dir/config.json" > "$dir/ports.txt"
}

# policy <case> <public> <loopback> — exactly the ports <public> are
# published on all interfaces, each port of <loopback> on 127.0.0.1 only.
policy() {
    local case="$1" public=" $2 " file="$TMP/$1/ports.txt" service host published target p
    while read -r service host published target; do
        case "$host" in
            127.0.0.1|::1|localhost) continue ;;
        esac
        if [[ "$public" == *" $published "* ]]; then
            ok "$case: $service publishes $published -> $target on all interfaces"
        else
            fail "$case: COL-08 violated, $service publishes $published -> $target on $host (all interfaces), allowed are only $2"
        fi
    done < "$file"
    for p in $2; do
        [ -n "$(awk -v p="$p" '$3 == p && $2 != "127.0.0.1"' "$file")" ] \
            || fail "$case: $p is not published on all interfaces (expected: $2)"
    done
    for p in $3; do
        service="$(awk -v p="$p" '$3 == p && $2 == "127.0.0.1" { print $1; exit }' "$file")"
        if [ -n "$service" ]; then
            ok "$case: $service publishes $p on 127.0.0.1 only"
        else
            fail "$case: $p is not published on 127.0.0.1"
        fi
    done
}

# env_is <case> <KEY=value…> — the .env of the case holds these lines.
env_is() {
    local case="$1" line
    shift
    for line in "$@"; do
        if grep -qx -- "$line" "$TMP/$case/.env"; then
            ok "$case: .env has $line"
        else
            fail "$case: .env has $(grep -E "^${line%%=*}=" "$TMP/$case/.env" || echo "no ${line%%=*}"), expected $line"
        fi
    done
}

# log_has <case> <yes|no> <text> <description> — the installer's output of the
# case contains the text (yes) or not (no).
log_has() {
    local case="$1" want="$2" text="$3" what="$4" got=no
    grep -qF -- "$text" "$TMP/$case/installer.log" && got=yes
    if [ "$got" = "$want" ]; then
        ok "$case: $what"
    else
        fail "$case: $what (installer output: $(grep -F -e '!' -e 'Error' "$TMP/$case/installer.log" | head -3))"
    fi
}

# not_public_3000 <case> — the frontend's plain-HTTP port of a TLS
# installation is 80; 3000 is not published on all interfaces.
not_public_3000() {
    if [ -n "$(awk '$3 == "3000" && $2 != "127.0.0.1"' "$TMP/$1/ports.txt")" ]; then
        fail "$1: COL-08 violated, 3000 is published on all interfaces with a TLS domain"
    else
        ok "$1: 3000 is not published on all interfaces"
    fi
}

warning="published on all interfaces without TLS (RETICORA_PUBLISH_DIRECT_PORTS=true"

# ─── TLS installation ────────────────────────────────────────────────────────
if installer_env "tls installation" \
    "RETICORA_PUBLIC_BASE_URL=https://cmdb.example.com" "RETICORA_TLS_DOMAIN=cmdb.example.com" &&
    ports "tls installation"; then
    policy "tls installation" "80 443 4222" "8080 8180"
    not_public_3000 "tls installation"
    env_is "tls installation" RETICORA_FRONTEND_PORT=80 RETICORA_PUBLISH_DIRECT_PORTS=false RETICORA_DIRECT_PORTS_BIND=127.0.0.1
    log_has "tls installation" no "$warning" "the installer does not announce the direct ports"
else
    fail "tls installation: install-cloud.sh collect_config/write_env ($(tail -n 3 "$TMP/tls installation/installer.log"))"
fi

# ─── TLS installation on the .env of an IP installation ──────────────────────
if installer_env "tls after ip installation" \
    "RETICORA_PUBLIC_BASE_URL=https://cmdb.example.com" "RETICORA_TLS_DOMAIN=cmdb.example.com" \
    "RETICORA_OIDC_ISSUER_URL=http://192.0.2.10:8180/realms/reticora" "RETICORA_FRONTEND_PORT=3000" \
    "RETICORA_PUBLISH_DIRECT_PORTS=true" "RETICORA_DIRECT_PORTS_BIND=0.0.0.0" &&
    ports "tls after ip installation"; then
    policy "tls after ip installation" "80 443 4222" "8080 8180"
    not_public_3000 "tls after ip installation"
    env_is "tls after ip installation" RETICORA_FRONTEND_PORT=80 RETICORA_PUBLISH_DIRECT_PORTS=false RETICORA_DIRECT_PORTS_BIND=127.0.0.1
    log_has "tls after ip installation" yes "names the Keycloak port 8180, which is bound to 127.0.0.1" \
        "the installer warns that the issuer on port 8180 is unreachable with a TLS domain"
else
    fail "tls after ip installation: install-cloud.sh collect_config/write_env ($(tail -n 3 "$TMP/tls after ip installation/installer.log"))"
fi

# ─── IP installation (opt-in) ────────────────────────────────────────────────
if installer_env "ip installation" "RETICORA_PUBLIC_BASE_URL=http://192.0.2.10:3000" &&
    ports "ip installation"; then
    policy "ip installation" "3000 443 4222 8080 8180" ""
    env_is "ip installation" RETICORA_FRONTEND_PORT=3000 RETICORA_PUBLISH_DIRECT_PORTS=true RETICORA_DIRECT_PORTS_BIND=0.0.0.0 \
        RETICORA_OIDC_ISSUER_URL=http://192.0.2.10:8180/realms/reticora
    log_has "ip installation" yes "$warning" \
        "the installer warns that REST API and Keycloak are published on all interfaces without TLS"

    # ─── the same installation without the installer's bind address ──────────
    mkdir -p "$TMP/ip without opt-in"
    grep -vE '^RETICORA_(PUBLISH_DIRECT_PORTS|DIRECT_PORTS_BIND)=' "$TMP/ip installation/.env" > "$TMP/ip without opt-in/.env"
    if ports "ip without opt-in"; then
        policy "ip without opt-in" "3000 443 4222" "8080 8180"
    fi
else
    fail "ip installation: install-cloud.sh collect_config/write_env ($(tail -n 3 "$TMP/ip installation/installer.log"))"
fi

# ─── control: the policy reports a further public port ───────────────────────
cat > "$TMP/public-postgres.yml" <<'YAML'
services:
  postgres:
    ports:
      - "5432:5432"
YAML
if [ -f "$TMP/tls installation/.env" ]; then
    mkdir -p "$TMP/control"
    cp "$TMP/tls installation/.env" "$TMP/control/.env"
    if ports control "$TMP/public-postgres.yml"; then
        report="$(policy control "80 443 4222" "8080 8180")"
        if grep -q '^FAIL control: COL-08 violated, postgres publishes 5432 -> 5432' <<< "$report"; then
            ok "control: postgres 5432 on all interfaces is reported as a COL-08 violation"
        else
            fail "control: postgres 5432 on all interfaces is not reported"
        fi
    fi
fi

echo
echo "compose-port-policy: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
