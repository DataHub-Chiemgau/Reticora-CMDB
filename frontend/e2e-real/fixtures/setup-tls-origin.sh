#!/usr/bin/env bash
#
# setup-tls-origin.sh — installs Reticora on a TLS origin for the WebKit
# projects of the real-stack suite (WP-646, F-375; job e2e-real of
# install-smoke.yml, projects webkit and mobile-safari).
#
# WebKit does not store the Secure refresh cookie reticora_refresh over
# http://localhost, so these projects run against https://<domain>, a name
# that is not localhost: the installer runs for real with RETICORA_TLS_DOMAIN
# and the certificate of a test CA, Pebble with challtestsrv as in the job
# tls-domain-pebble. The Pebble helpers come from tests/install-acme-pebble.
# test.sh (sourced; its main does not run): domain cmdb.<host IP with
# dashes>.sslip.io (or RETICORA_PEBBLE_DOMAIN), the CA of Pebble's
# endpoints, the installer call with RETICORA_ACME_SERVER and the CA bundles,
# the checks of the issued and served chain.
#
#   1. starts Pebble and challtestsrv, writes .env with the domain and runs
#      install-cloud.sh --non-interactive --build-from-source once; the
#      installation must issue the Pebble certificate, serve it, and log
#      both issuer checks;
#   2. hands the root of the served chain to <out-dir>/pebble-root.pem and
#      removes Pebble (the installation, its .env, letsencrypt/ and the CA
#      files under <out-dir>/work stay);
#   3. trusts that root on the runner: the system trust store
#      (/usr/local/share/ca-certificates, update-ca-certificates), which the
#      WebKit build of Playwright reads through
#      /etc/ssl/certs/ca-certificates.crt, curl and the other tools of the
#      job, and NODE_EXTRA_CA_CERTS for the Node processes of Playwright
#      (request contexts). Certificate errors are not ignored
#      (ignoreHTTPSErrors stays off);
#   4. checks that curl reaches https://<domain> with the system trust store
#      alone and writes E2E_CA_CERT and NODE_EXTRA_CA_CERTS to $GITHUB_ENV
#      (else prints export lines).
#
# setup-onboarding.sh and setup-real-stack.sh then address the installation
# over https://<domain> (BASE_URL, issuer). tls_origin_check is the check of
# the job before the suite: BASE_URL starts with https:// and does not name
# localhost.
#
# Runs the installer and changes the system trust store (sudo): meant for CI
# only. It refuses to run when deploy/docker-compose/.env exists. Functions
# are tested with stubs in tests/e2e-real-tls-origin.test.sh (sourced).
#
# Usage: frontend/e2e-real/fixtures/setup-tls-origin.sh <out-dir>
#        frontend/e2e-real/fixtures/setup-tls-origin.sh --check-base-url <url>
set -uo pipefail

TLS_ORIGIN_HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TLS_ORIGIN_REPO="$(cd "$TLS_ORIGIN_HERE/../../.." && pwd)"
# Name of the root in the system trust store.
TLS_ORIGIN_CA_NAME="${TLS_ORIGIN_CA_NAME:-reticora-e2e-tls-origin}"
TLS_ORIGIN_CA_DIR="${TLS_ORIGIN_CA_DIR:-/usr/local/share/ca-certificates}"
TLS_ORIGIN_SYSTEM_BUNDLE="${TLS_ORIGIN_SYSTEM_BUNDLE:-/etc/ssl/certs/ca-certificates.crt}"
TLS_ORIGIN_SUDO="${TLS_ORIGIN_SUDO-sudo}"

# tls_origin_check <url> — the URL is a TLS origin https://<host>[:port]
# whose host is not localhost or a loopback address.
tls_origin_check() {
    local url="$1" rest host
    case "$url" in
        https://?*) ;;
        *) echo "FAIL: BASE_URL '$url' does not start with https:// (the WebKit projects need a TLS origin, F-375)"; return 1 ;;
    esac
    rest="${url#https://}"
    host="${rest%%/*}"
    case "$host" in
        \[*\]*) host="${host%%]*}]" ;;
        *) host="${host%%:*}" ;;
    esac
    host="$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')"
    host="${host%.}"
    case "$host" in
        '' | localhost | *.localhost | 127.* | 0.0.0.0 | '[::1]' | '[::]' | '[0:0:0:0:0:0:0:1]')
            echo "FAIL: BASE_URL '$url' names localhost ($host); the WebKit projects need a TLS origin that is not localhost (F-375)"
            return 1 ;;
    esac
}

# tls_origin_trust <pem> — adds the root to the system trust store and checks
# that the system bundle holds it afterwards.
tls_origin_trust() {
    local pem="$1" target fp
    fp="$(openssl x509 -in "$pem" -noout -fingerprint -sha256 2>/dev/null)" \
        || { echo "FAIL: $pem is not a PEM certificate"; return 1; }
    openssl x509 -in "$pem" -noout -text 2>/dev/null | grep -q 'CA:TRUE' \
        || { echo "FAIL: $pem is not a CA certificate"; return 1; }
    target="$TLS_ORIGIN_CA_DIR/$TLS_ORIGIN_CA_NAME.crt"
    $TLS_ORIGIN_SUDO install -D -m 0644 "$pem" "$target" || { echo "FAIL: cannot write $target"; return 1; }
    $TLS_ORIGIN_SUDO update-ca-certificates >/dev/null || { echo "FAIL: update-ca-certificates failed"; return 1; }
    tls_origin_bundle_has "$TLS_ORIGIN_SYSTEM_BUNDLE" "$fp" \
        || { echo "FAIL: $TLS_ORIGIN_SYSTEM_BUNDLE does not hold the root ($fp) after update-ca-certificates"; return 1; }
    echo "OK: root $fp in $TLS_ORIGIN_SYSTEM_BUNDLE"
}

# tls_origin_bundle_has <bundle> <fingerprint line> — one certificate of the
# PEM bundle has the SHA-256 fingerprint.
tls_origin_bundle_has() {
    local bundle="$1" want="$2" cert="" line
    [ -f "$bundle" ] || return 1
    while IFS= read -r line; do
        case "$line" in
            '-----BEGIN CERTIFICATE-----') cert="$line"$'\n' ;;
            '-----END CERTIFICATE-----')
                cert+="$line"$'\n'
                [ "$(printf '%s' "$cert" | openssl x509 -noout -fingerprint -sha256 2>/dev/null)" = "$want" ] && return 0
                cert="" ;;
            *) [ -z "$cert" ] || cert+="$line"$'\n' ;;
        esac
    done <"$bundle"
    return 1
}

# tls_origin_export <name=value…> — to $GITHUB_ENV when set, else export lines.
tls_origin_export() {
    local v
    for v in "$@"; do
        if [ -n "${GITHUB_ENV:-}" ]; then echo "$v" >>"$GITHUB_ENV"; else echo "export $v"; fi
    done
}

# tls_origin_fail <message> — the logs of Pebble and the stack, then exit 1.
# The installation stays for the log step of the job.
tls_origin_fail() {
    echo "FAIL: $1"
    collect_logs
    pebble down -v --remove-orphans >/dev/null 2>&1 || true
    exit 1
}

tls_origin_main() {
    local out="${1:?usage: setup-tls-origin.sh <out-dir> | --check-base-url <url>}" rc=0 tool
    # The Pebble helpers (tests/install-acme-pebble.test.sh); sourcing
    # changes into the repository root.
    # shellcheck source=tests/install-acme-pebble.test.sh
    . "$TLS_ORIGIN_REPO/tests/install-acme-pebble.test.sh"
    if [ -e "$ENV_FILE" ]; then
        echo "FAIL: $ENV_FILE exists: this checkout already holds an installation." >&2
        exit 2
    fi
    for tool in docker openssl curl ip getent awk; do
        command -v "$tool" >/dev/null || { echo "FAIL: $tool is required." >&2; exit 2; }
    done
    mkdir -p "$out" || exit 2
    out="$(cd "$out" && pwd)"
    LOG_DIR="$out/logs"
    WORK="$out/work"
    mkdir -p "$LOG_DIR" "$WORK" && chmod 0755 "$WORK" || exit 2
    setup_names || exit 1
    printf 'host IP:     %s\ndomain:      %s\nACME server: %s\nlogs:        %s\n' "$HOST_IP" "$DOMAIN" "$ACME_SERVER" "$LOG_DIR" | tee "$LOG_DIR/setup.txt"

    docker pull -q "$CERTBOT_IMAGE" >/dev/null || tls_origin_fail "cannot pull $CERTBOT_IMAGE"
    resolves_to_host || tls_origin_fail "$DOMAIN does not resolve to the host IP (set RETICORA_PEBBLE_DOMAIN)"
    make_pebble_tls || tls_origin_fail "cannot create the TLS files of Pebble"
    start_pebble || tls_origin_fail "Pebble and challtestsrv did not start"
    challtest add-a "{\"host\":\"$DOMAIN\",\"addresses\":[\"$HOST_IP\"]}" >/dev/null || tls_origin_fail "A record in challtestsrv"

    echo "== Installation on https://$DOMAIN"
    mkdir -p "$COMPOSE_DIR"
    printf 'RETICORA_PUBLIC_BASE_URL=https://%s\nRETICORA_TLS_DOMAIN=%s\n' "$DOMAIN" "$DOMAIN" >"$ENV_FILE"
    chmod 0600 "$ENV_FILE"
    run_installer tls-origin || rc=$?
    [ "$rc" -eq 0 ] || tls_origin_fail "installer rc $rc (see $LOG_DIR/install-tls-origin.log)"
    log_has tls-origin "Let's Encrypt certificate issued for $DOMAIN" "HTTPS is now active at https://$DOMAIN" \
        || tls_origin_fail "the installer did not issue the Pebble certificate"
    issuer_checks_logged tls-origin || tls_origin_fail "the installer did not log both issuer checks"
    chain_is_pebble || tls_origin_fail "live/$DOMAIN/ does not hold the Pebble chain"
    wait_healthy frontend || tls_origin_fail "the frontend is not healthy"
    serves_live_chain || tls_origin_fail "the frontend does not serve the Pebble chain"

    echo "== Test CA in the trust store of the runner"
    cp "$PEBBLE_ROOT" "$out/pebble-root.pem" || tls_origin_fail "cannot copy the Pebble root"
    collect_logs
    pebble down -v --remove-orphans >/dev/null 2>&1 || true
    tls_origin_trust "$out/pebble-root.pem" || exit 1
    curl -fsS --cacert "$TLS_ORIGIN_SYSTEM_BUNDLE" -o /dev/null "https://$DOMAIN/api/v1/auth/config" \
        || { echo "FAIL: https://$DOMAIN is not trusted with the system trust store $TLS_ORIGIN_SYSTEM_BUNDLE"; exit 1; }
    tls_origin_check "https://$DOMAIN" || exit 1

    tls_origin_export "E2E_CA_CERT=$out/pebble-root.pem" "NODE_EXTRA_CA_CERTS=$out/pebble-root.pem"
    echo "OK: installation on the TLS origin https://$DOMAIN, test CA trusted"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    if [ "${1:-}" = "--check-base-url" ]; then
        tls_origin_check "${2:-}" || exit 1
        echo "OK: BASE_URL ${2} is a TLS origin that is not localhost"
        exit 0
    fi
    tls_origin_main "$@"
fi
