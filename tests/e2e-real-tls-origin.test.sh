#!/usr/bin/env bash
#
# e2e-real-tls-origin.test.sh — tests for the TLS origin of the WebKit
# projects of the real-stack suite (WP-646, F-375) with stubs; neither the
# installer nor Docker nor sudo run:
#
#   (a) tls_origin_check (frontend/e2e-real/fixtures/setup-tls-origin.sh,
#       also --check-base-url): https://<name> passes, http:// and every
#       localhost/loopback name fail with a message;
#   (b) tls_origin_trust puts a CA root into the trust-store directory, runs
#       update-ca-certificates and finds the root in the system bundle;
#       a bundle without the root after the update, or a leaf certificate,
#       fail;
#   (c) the Pebble helpers setup-tls-origin.sh sources from
#       tests/install-acme-pebble.test.sh exist;
#   (d) frontend/e2e-real/fixtures/setup-real-stack.sh with stubbed docker,
#       curl, psql and sysctl: an installation with
#       RETICORA_TLS_DOMAIN gets the TLS template of the installation with
#       the X-Real-IP passthrough (no $remote_addr line left) and the overlay
#       docker-compose.e2e-real-tls.yml, and is checked over
#       https://<domain>; a loopback installation keeps
#       http://localhost:<port> without that overlay; a server whose links
#       name another origin than the installation fails.
#
# Usage: ./tests/e2e-real-tls-origin.test.sh
# The patterns name nginx variables ($remote_addr, $e2e_client_ip); the stubs
# change PATH only in their subshells.
# shellcheck disable=SC2016,SC2030,SC2031
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
REPO="$(pwd)"
SETUP="$REPO/frontend/e2e-real/fixtures/setup-tls-origin.sh"
REAL_STACK="$REPO/frontend/e2e-real/fixtures/setup-real-stack.sh"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

# check <name> <command…> — the command succeeds.
check() {
    local name="$1" out
    shift
    if out="$("$@" 2>&1)"; then
        ok "$name"
    else
        fail "$name"
        printf '%s\n' "$out" | tail -n 20 | sed 's/^/     /'
    fi
}

# check_fails <name> <expected output> <command…> — the command fails and
# prints the expected text.
check_fails() {
    local name="$1" want="$2" out
    shift 2
    if out="$("$@" 2>&1)"; then
        fail "$name (rc 0)"
        printf '%s\n' "$out" | tail -n 20 | sed 's/^/     /'
    elif ! grep -qF -- "$want" <<<"$out"; then
        fail "$name (no '$want')"
        printf '%s\n' "$out" | tail -n 20 | sed 's/^/     /'
    else
        ok "$name"
    fi
}

# ─── (a) tls_origin_check ────────────────────────────────────────────────────

# shellcheck source=frontend/e2e-real/fixtures/setup-tls-origin.sh
origin_check() { (. "$SETUP" && tls_origin_check "$1"); }

for url in https://cmdb.10-1-0-4.sslip.io https://cmdb.10-1-0-4.sslip.io/ https://cmdb.example.org:8443 \
    https://cmdb.example.org/realms/reticora; do
    check "check: $url is a TLS origin" origin_check "$url"
done
for url in http://cmdb.10-1-0-4.sslip.io http://localhost:3000 '' cmdb.example.org; do
    check_fails "check: '$url' is rejected (not https://)" "does not start with https://" origin_check "$url"
done
for url in https://localhost https://localhost:3000 https://LOCALHOST. https://app.localhost https://127.0.0.1 \
    https://127.1.2.3:8443 'https://[::1]' 'https://[::1]:443' https://0.0.0.0; do
    check_fails "check: '$url' is rejected (localhost)" "names localhost" origin_check "$url"
done
check "check: --check-base-url accepts a TLS origin" "$SETUP" --check-base-url https://cmdb.10-1-0-4.sslip.io
check_fails "check: --check-base-url rejects http://localhost:3000" "does not start with https://" \
    "$SETUP" --check-base-url http://localhost:3000

# ─── (b) tls_origin_trust ────────────────────────────────────────────────────

CERTS="$TMP_ROOT/certs"
mkdir -p "$CERTS"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$CERTS/root.key" -out "$CERTS/root.pem" -days 1 \
    -subj "/CN=WP-646 test root" -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$CERTS/other.key" -out "$CERTS/other.pem" -days 1 \
    -subj "/CN=WP-646 other root" -addext "basicConstraints=critical,CA:TRUE" 2>/dev/null
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$CERTS/leaf.key" -out "$CERTS/leaf.pem" -days 1 \
    -subj "/CN=cmdb.example.org" -addext "basicConstraints=critical,CA:FALSE" 2>/dev/null

# Stub update-ca-certificates: the system bundle is the other root plus every
# .crt of the trust-store directory ($STUB_UPDATE=noop leaves it unchanged).
STUB_BIN="$TMP_ROOT/bin"
mkdir -p "$STUB_BIN"
cat >"$STUB_BIN/update-ca-certificates" <<'STUBEOF'
#!/usr/bin/env bash
[ "${STUB_UPDATE:-}" = noop ] && exit 0
cat "$STUB_BASE_BUNDLE" "$TLS_ORIGIN_CA_DIR"/*.crt >"$TLS_ORIGIN_SYSTEM_BUNDLE"
STUBEOF
chmod +x "$STUB_BIN/update-ca-certificates"

# trust <pem> [noop] — tls_origin_trust in a fresh trust store without sudo.
trust() {
    local store="$TMP_ROOT/store-$RANDOM"
    mkdir -p "$store/dir"
    cp "$CERTS/other.pem" "$store/bundle.crt"
    (
        export PATH="$STUB_BIN:$PATH" STUB_UPDATE="${2:-}" STUB_BASE_BUNDLE="$CERTS/other.pem"
        export TLS_ORIGIN_SUDO="" TLS_ORIGIN_CA_DIR="$store/dir" TLS_ORIGIN_SYSTEM_BUNDLE="$store/bundle.crt"
        # shellcheck source=frontend/e2e-real/fixtures/setup-tls-origin.sh
        . "$SETUP"
        tls_origin_trust "$1" || exit 1
        [ "$(stat -c '%a' "$store/dir/reticora-e2e-tls-origin.crt")" = 644 ] || { echo "mode of the trust-store file"; exit 1; }
        cmp -s "$1" "$store/dir/reticora-e2e-tls-origin.crt" || { echo "the trust-store file is not the root"; exit 1; }
    )
}

check "trust: the root lands in the trust store and in the system bundle" trust "$CERTS/root.pem"
check_fails "trust: a bundle without the root after the update fails" "does not hold the root" \
    trust "$CERTS/root.pem" noop
check_fails "trust: a leaf certificate is not trusted as a CA" "is not a CA certificate" trust "$CERTS/leaf.pem"
check_fails "trust: no certificate" "is not a PEM certificate" trust "$TMP_ROOT/missing.pem"

# ─── (c) Pebble helpers ──────────────────────────────────────────────────────

pebble_helpers() {
    (
        # shellcheck source=tests/install-acme-pebble.test.sh
        . "$REPO/tests/install-acme-pebble.test.sh"
        local f missing=0
        for f in setup_names make_pebble_tls start_pebble challtest pebble run_installer log_has \
            issuer_checks_logged chain_is_pebble wait_healthy serves_live_chain collect_logs resolves_to_host; do
            declare -F "$f" >/dev/null || { echo "missing: $f"; missing=1; }
        done
        for f in ENV_FILE COMPOSE_DIR CERTBOT_IMAGE; do
            [ -n "${!f:-}" ] || { echo "missing: \$$f"; missing=1; }
        done
        exit "$missing"
    )
}
check "pebble: tests/install-acme-pebble.test.sh provides the helpers setup-tls-origin.sh calls" pebble_helpers

# ─── (d) setup-real-stack.sh with stubs ──────────────────────────────────────

# Stubs: docker (compose ps -q → container id, inspect → $STUB_SERVER_ENV,
# everything else logged), curl (logged, succeeds), psql, sysctl.
RS_BIN="$TMP_ROOT/rs-bin"
mkdir -p "$RS_BIN"
cat >"$RS_BIN/docker" <<'STUBEOF'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$STUB_LOG"
case " $* " in
    *" ps -q server "*) echo server-cid ;;
    *" inspect "*) printf '%s\n' "$STUB_SERVER_ENV" ;;
esac
exit 0
STUBEOF
cat >"$RS_BIN/curl" <<'STUBEOF'
#!/usr/bin/env bash
printf 'curl %s\n' "$*" >>"$STUB_LOG"
# The read-only object path answers like the object store without a
# signature (setup-real-stack.sh, F-376).
case "$*" in *"%{http_code}"*"/objects/exports/"*) printf 403 ;; esac
exit 0
STUBEOF
printf '#!/bin/sh\nexit 0\n' >"$RS_BIN/psql"
printf '#!/bin/sh\necho 0\n' >"$RS_BIN/sysctl"
chmod +x "$RS_BIN"/*

# real_stack <name> <.env lines> <server verify URL> — runs setup-real-stack.sh
# against a compose dir with that .env; output in $RS_OUT, log in $RS_LOG.
real_stack() {
    local dir="$TMP_ROOT/rs-$1"
    mkdir -p "$dir/compose" "$dir/tmp"
    printf '%s\n' "$2" >"$dir/compose/.env"
    cp "$REPO/deploy/docker-compose/nginx-tls.conf.template" "$dir/compose/"
    RS_LOG="$dir/calls.log"
    RS_TMP="$dir/tmp"
    : >"$RS_LOG"
    RS_OUT="$(env -u GITHUB_ENV -u GITHUB_ACTIONS PATH="$RS_BIN:$PATH" RUNNER_TEMP="$RS_TMP" STUB_LOG="$RS_LOG" \
        STUB_SERVER_ENV="$(printf 'RETICORA_SIGNUP_CAPTCHA_KEY=k\nRETICORA_KEYCLOAK_ADMIN_CLIENT_ID=c\nRETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET=s\nRETICORA_SIGNUP_VERIFY_URL=%s' "$3")" \
        "$REAL_STACK" "$dir/compose" .env 2>&1)"
}

rs_tls() {
    real_stack tls "$(printf 'RETICORA_TLS_DOMAIN=cmdb.10-1-0-4.sslip.io\nRETICORA_NGINX_TLS_TEMPLATE=./nginx-tls.conf.template\nRETICORA_FRONTEND_PORT=80')" \
        https://cmdb.10-1-0-4.sslip.io/onboarding || { printf '%s\n' "$RS_OUT"; return 1; }
    local tpl="$RS_TMP/nginx-e2e-real-tls.conf.template"
    [ -f "$tpl" ] || { echo "no $tpl"; return 1; }
    [ "$(grep -c 'proxy_set_header X-Real-IP $e2e_client_ip;' "$tpl")" -ge 2 ] || { echo "the TLS template does not pass X-Real-IP through (/api/, /bff/)"; return 1; }
    ! grep -q 'X-Real-IP $remote_addr' "$tpl" || { echo "the TLS template keeps X-Real-IP \$remote_addr"; return 1; }
    diff <(sed 's/X-Real-IP $e2e_client_ip;/X-Real-IP $remote_addr;/' "$tpl") "$REPO/deploy/docker-compose/nginx-tls.conf.template" >/dev/null \
        || { echo "the TLS template differs in more than the X-Real-IP lines"; return 1; }
    grep -q 'docker-compose.e2e-real-tls.yml.*up -d server' "$RS_LOG" || { echo "the server is not started with docker-compose.e2e-real-tls.yml"; cat "$RS_LOG"; return 1; }
    grep -q 'docker-compose.e2e-real-tls.yml.*up -d --force-recreate frontend' "$RS_LOG" || { echo "the frontend is not recreated with docker-compose.e2e-real-tls.yml"; return 1; }
    grep -qF 'curl -fsS https://cmdb.10-1-0-4.sslip.io/api/v1/auth/config' "$RS_LOG" || { echo "the frontend is not checked over https://cmdb.10-1-0-4.sslip.io"; cat "$RS_LOG"; return 1; }
    ! grep -q 'curl .*http://localhost:80/api/v1/auth/config' "$RS_LOG" || { echo "the frontend is checked over plain HTTP"; return 1; }
    grep -q 'OK: real-stack fixtures ready' <<<"$RS_OUT"
}

rs_loopback() {
    real_stack loopback 'RETICORA_FRONTEND_PORT=3000' http://localhost:3000/onboarding || { printf '%s\n' "$RS_OUT"; return 1; }
    ! grep -q 'docker-compose.e2e-real-tls.yml' "$RS_LOG" || { echo "the loopback installation got the TLS overlay"; return 1; }
    [ ! -e "$RS_TMP/nginx-e2e-real-tls.conf.template" ] || { echo "TLS template written for the loopback installation"; return 1; }
    grep -qF 'curl -fsS http://localhost:3000/api/v1/auth/config' "$RS_LOG" || { echo "the frontend is not checked over http://localhost:3000"; cat "$RS_LOG"; return 1; }
    grep -q 'X-Real-IP $e2e_client_ip' "$RS_TMP/nginx-e2e-real.conf" || { echo "nginx.conf without the passthrough"; return 1; }
}

rs_mismatch() {
    local rc=0
    real_stack mismatch 'RETICORA_TLS_DOMAIN=cmdb.10-1-0-4.sslip.io' http://localhost:3000/onboarding || rc=$?
    printf '%s\n' "$RS_OUT"
    return "$rc"
}

check "real stack: TLS installation gets the TLS template with X-Real-IP passthrough and is checked over https://<domain>" rs_tls
check "real stack: loopback installation stays on http://localhost:3000 without the TLS overlay" rs_loopback
check_fails "real stack: a server linking to another origin than the installation fails" \
    "the installation is served at https://cmdb.10-1-0-4.sslip.io" rs_mismatch

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
