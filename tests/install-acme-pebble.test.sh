#!/usr/bin/env bash
#
# install-acme-pebble.test.sh — the TLS-domain path of install-cloud.sh
# against a real ACME server (WP-420; INS-02, TEC-09; F-033, F-034, F-035).
#
# Pebble with challtestsrv (tests/fixtures/pebble/) issues the certificate,
# certbot/certbot:v2.11.0 (the certbot service of docker-compose.yml)
# requests and renews it, and install-cloud.sh runs for real
# (--non-interactive --build-from-source) with RETICORA_TLS_DOMAIN (in .env),
# RETICORA_ACME_SERVER, RETICORA_ACME_CA_BUNDLE and RETICORA_TLS_CA_BUNDLE:
#
#   (a) first installation with the bootstrap placeholder: live/<domain>/
#       holds the Pebble chain, which verifies against the Pebble root, and
#       validate_tls_material <domain> 1 passes;
#   (b) second installer run: rc 0, Pebble's order counter unchanged, no
#       <domain>-0001 lineage;
#   (c) after the issuance and after 'certbot renew --force-renewal' (the
#       renewal command of the certbot service): the frontend (uid 101)
#       reads live/<domain>/privkey.pem, 'nginx -t' passes in it, 'docker
#       compose restart frontend' becomes healthy and serves the chain of
#       live/<domain>/;
#   (d) the log of every installer run holds both successful issuer checks,
#       'OIDC issuer is reachable from the backend container' included
#       (F-035 in the TLS-domain path);
#   (e) failed challenge (no A record in challtestsrv): the placeholder
#       stays, the installer warns and ends with rc 0.
#
# Name resolution: Pebble resolves the domain only through challtestsrv (A
# record of the host IP, removed for (e)). The host and the containers (the
# issuer checks, the served chain) need it as well: by default it is
# cmdb.<host IP with dashes>.sslip.io, a public wildcard DNS name for the
# host IP; RETICORA_PEBBLE_DOMAIN names another one that resolves to the host
# IP. The host's curl trusts the Pebble root through CURL_CA_BUNDLE (system
# CAs plus that root), like an operator whose private CA is in the host's
# trust store; RETICORA_TLS_CA_BUNDLE is the same root for the installer's
# openssl checks, RETICORA_ACME_CA_BUNDLE the CA of Pebble's own HTTPS
# endpoints, which this test issues for the host IP.
#
# Runs the installer: it builds and starts the stack in deploy/docker-compose
# with ports 80 and 443 and removes it, volumes included, at the end. Meant
# for CI (install-smoke.yml, job tls-domain-pebble); it refuses to run when
# deploy/docker-compose/.env exists, i.e. when this checkout holds an
# installation.
#
# Usage: ./tests/install-acme-pebble.test.sh [log directory]
#   The log directory receives the installer logs (install-*.log) and the
#   logs of Pebble, challtestsrv and the stack.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1
REPO_ROOT="$(pwd)"
COMPOSE_DIR="$REPO_ROOT/deploy/docker-compose"
ENV_FILE="$COMPOSE_DIR/.env"
LE_DIR="$COMPOSE_DIR/letsencrypt"
PEBBLE_COMPOSE="$REPO_ROOT/tests/fixtures/pebble/docker-compose.pebble.yml"
CERTBOT_IMAGE="certbot/certbot:v2.11.0"
SYSTEM_CA_BUNDLE="/etc/ssl/certs/ca-certificates.crt"

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

# check <name> <command…> — one case: ok, or FAIL with the end of its output.
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

# ─── environment ─────────────────────────────────────────────────────────────

host_ip() {
    ip -4 route get 1.1.1.1 2>/dev/null | awk '{ for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit } }'
}

# setup_names — HOST_IP, DOMAIN, ACME_SERVER and the work files.
setup_names() {
    HOST_IP="$(host_ip)"
    [ -n "$HOST_IP" ] || { echo "No IPv4 address of this host (ip -4 route get)." >&2; return 1; }
    DOMAIN="${RETICORA_PEBBLE_DOMAIN:-cmdb.${HOST_IP//./-}.sslip.io}"
    ACME_SERVER="https://$HOST_IP:14000/dir"
    PEBBLE_TLS_DIR="$WORK/pebble-tls"
    ACME_CA="$PEBBLE_TLS_DIR/ca.pem"
    PEBBLE_ROOT="$WORK/pebble-root.pem"
    CURL_BUNDLE="$WORK/curl-ca-bundle.pem"
}

# The installation, as install-cloud.sh's compose_cmd addresses it.
stack() {
    local files=(-f "$COMPOSE_DIR/docker-compose.yml")
    [ -f "$COMPOSE_DIR/docker-compose.override.yml" ] && files+=(-f "$COMPOSE_DIR/docker-compose.override.yml")
    docker compose --env-file "$ENV_FILE" "${files[@]}" "$@"
}

# The ACME side (tests/fixtures/pebble/).
pebble() { PEBBLE_TLS_DIR="$PEBBLE_TLS_DIR" docker compose -f "$PEBBLE_COMPOSE" "$@"; }

# challtest <endpoint> <json> — challtestsrv management API.
challtest() { curl -fsS -X POST -d "$2" "http://127.0.0.1:8055/$1"; }

# make_pebble_tls — a CA (RETICORA_ACME_CA_BUNDLE) and the certificate of
# Pebble's HTTPS endpoints for the host IP, which the certbot container uses.
make_pebble_tls() {
    local d="$PEBBLE_TLS_DIR"
    mkdir -p "$d"
    openssl req -x509 -newkey rsa:2048 -nodes -keyout "$d/ca.key" -out "$d/ca.pem" -days 2 \
        -subj "/CN=Reticora Pebble endpoint test CA" \
        -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
        -addext "subjectKeyIdentifier=hash" 2>/dev/null || return 1
    openssl req -newkey rsa:2048 -nodes -keyout "$d/key.pem" -out "$d/cert.csr" -subj "/CN=pebble" 2>/dev/null || return 1
    printf '%s\n' "subjectAltName=IP:$HOST_IP,IP:127.0.0.1,DNS:localhost" "basicConstraints=critical,CA:FALSE" \
        "keyUsage=critical,digitalSignature,keyEncipherment" "extendedKeyUsage=serverAuth" \
        "subjectKeyIdentifier=hash" "authorityKeyIdentifier=keyid,issuer" > "$d/cert.ext"
    openssl x509 -req -in "$d/cert.csr" -CA "$d/ca.pem" -CAkey "$d/ca.key" -CAcreateserial -days 2 \
        -extfile "$d/cert.ext" -out "$d/cert.pem" 2>/dev/null || return 1
    # Pebble runs as root in its container and reads the key through the bind mount.
    chmod 0755 "$d"
}

# start_pebble — starts Pebble and challtestsrv, fetches the root of the
# chains Pebble issues (new on every start) and builds the curl bundle.
start_pebble() {
    pebble up -d || return 1
    local _
    for _ in $(seq 1 30); do
        if curl -fsS --cacert "$ACME_CA" -o "$PEBBLE_ROOT" https://127.0.0.1:15000/roots/0 2>/dev/null &&
            curl -fsS --cacert "$ACME_CA" -o /dev/null "$ACME_SERVER" 2>/dev/null &&
            curl -fsS -o /dev/null -X POST -d '{"host":"probe.invalid"}' http://127.0.0.1:8055/clear-a 2>/dev/null; then
            openssl x509 -in "$PEBBLE_ROOT" -noout || return 1
            cat "$SYSTEM_CA_BUNDLE" "$PEBBLE_ROOT" > "$CURL_BUNDLE"
            return 0
        fi
        sleep 1
    done
    echo "Pebble or challtestsrv did not answer within 30 s:" >&2
    curl -sS --cacert "$ACME_CA" -o /dev/null https://127.0.0.1:15000/roots/0
    curl -sS --cacert "$ACME_CA" -o /dev/null "$ACME_SERVER"
    curl -sS -o /dev/null -X POST -d '{"host":"probe.invalid"}' http://127.0.0.1:8055/clear-a
    pebble logs --no-color >&2
    return 1
}

# pebble_orders — Pebble's order counter ("There are now N orders in the db").
pebble_orders() {
    local n
    n="$(pebble logs --no-color pebble 2>/dev/null | sed -n 's/.*There are now \([0-9][0-9]*\) orders in the db.*/\1/p' | tail -n 1)"
    printf '%s' "${n:-0}"
}

# pebble_count <text> — Pebble log lines containing <text>.
pebble_count() { pebble logs --no-color pebble 2>/dev/null | grep -cF -- "$1"; }

# resolves_to_host — the domain resolves to the host IP on the host and in a
# container; precondition of the issuer checks and the served chain.
resolves_to_host() {
    local got
    got="$(getent ahostsv4 "$DOMAIN" | awk 'NR == 1 { print $1 }')"
    [ "$got" = "$HOST_IP" ] || { echo "$DOMAIN resolves to '${got:-nothing}' on the host, not to $HOST_IP."; return 1; }
    got="$(docker run --rm --entrypoint python3 "$CERTBOT_IMAGE" -c 'import socket, sys; print(socket.gethostbyname(sys.argv[1]))' "$DOMAIN" 2>&1)"
    [ "$got" = "$HOST_IP" ] || { echo "$DOMAIN resolves to '$got' in a container, not to $HOST_IP."; return 1; }
}

# run_installer <name> — install-cloud.sh with the Pebble settings; output in
# $LOG_DIR/install-<name>.log (without colour codes), returns its exit code.
run_installer() {
    local log="$LOG_DIR/install-$1.log" rc
    echo "── installer run '$1' (log: $log)"
    RETICORA_ACME_SERVER="$ACME_SERVER" RETICORA_ACME_CA_BUNDLE="$ACME_CA" \
        RETICORA_TLS_CA_BUNDLE="$PEBBLE_ROOT" CURL_CA_BUNDLE="$CURL_BUNDLE" \
        ./install-cloud.sh --non-interactive --build-from-source </dev/null 2>&1 | tee "$log"
    rc="${PIPESTATUS[0]}"
    sed -i 's/\x1b\[[0-9;]*m//g' "$log"
    echo "── installer run '$1': rc $rc"
    return "$rc"
}

# ─── checks ──────────────────────────────────────────────────────────────────

live() { printf '%s/live/%s/%s' "$LE_DIR" "$DOMAIN" "$1"; }

fingerprint() { openssl x509 -noout -fingerprint -sha256 "$@" 2>/dev/null; }

# chain_is_pebble — live/<domain>/fullchain.pem verifies against the Pebble
# root for the domain and is not self-signed.
chain_is_pebble() {
    local chain; chain="$(live fullchain.pem)"
    [ -f "$chain" ] || { echo "$chain missing"; return 1; }
    openssl verify -CAfile "$PEBBLE_ROOT" -untrusted "$chain" -verify_hostname "$DOMAIN" "$chain" \
        || { echo "live/$DOMAIN/fullchain.pem does not verify against the Pebble root (issuer: $(openssl x509 -in "$chain" -noout -issuer))"; return 1; }
    ! openssl verify -CAfile "$chain" "$chain" >/dev/null 2>&1 || { echo "live/$DOMAIN/fullchain.pem is self-signed"; return 1; }
}

# validate_tls_material — the installer's own check on this installation.
validate_tls_material_passes() {
    local harness="$WORK/harness"
    mkdir -p "$harness"
    head -n -1 install-cloud.sh > "$harness/install-cloud.sh"
    cp install-preflight.sh "$harness/"
    # The installer is sourced without arguments (its parser rejects unknown ones).
    # shellcheck disable=SC2016 # expanded by the inner shell
    bash -c 't_harness="$1" t_compose="$2" t_domain="$3" t_bundle="$4"; set --; . "$t_harness"
        COMPOSE_DIR="$t_compose"; RETICORA_TLS_DOMAIN="$t_domain"; RETICORA_TLS_CA_BUNDLE="$t_bundle"
        validate_tls_material "$t_domain" 1' \
        _ "$harness/install-cloud.sh" "$COMPOSE_DIR" "$DOMAIN" "$PEBBLE_ROOT"
}

no_second_lineage() {
    local p
    for p in "live/$DOMAIN-0001" "archive/$DOMAIN-0001" "renewal/$DOMAIN-0001.conf"; do
        [ ! -e "$LE_DIR/$p" ] || { echo "second lineage: $p"; return 1; }
    done
}

no_placeholder_backup() {
    [ ! -e "$LE_DIR/reticora-placeholder/$DOMAIN" ] || { echo "placeholder backup left: $LE_DIR/reticora-placeholder/$DOMAIN"; return 1; }
}

# log_has <run> <text…> — every text occurs in the log of the run.
log_has() {
    local log="$LOG_DIR/install-$1.log" text
    shift
    for text in "$@"; do
        grep -qF -- "$text" "$log" || { echo "$(basename "$log"): no line '$text'"; return 1; }
    done
}

# issuer_checks_logged <run> — (d): both strict issuer checks passed.
issuer_checks_logged() {
    log_has "$1" "OIDC issuer https://$DOMAIN/realms/reticora is reachable from this host" \
        "OIDC issuer is reachable from the backend container"
}

issued_in_first_run() {
    log_has first "(ACME server: $ACME_SERVER)" "Let's Encrypt certificate issued for $DOMAIN" \
        "HTTPS is now active at https://$DOMAIN" "TLS material for $DOMAIN is consistent" ", chain verifies)" || return 1
    chain_is_pebble || return 1
    no_second_lineage || return 1
    no_placeholder_backup || return 1
    local renewal="$LE_DIR/renewal/$DOMAIN.conf"
    grep -qx "server = $ACME_SERVER" "$renewal" || { echo "renewal/$DOMAIN.conf without 'server = $ACME_SERVER'"; return 1; }
    grep -qx "renew_hook = sh /usr/local/bin/reticora-certbot-deploy-hook.sh" "$renewal" || { echo "renewal/$DOMAIN.conf without the deploy hook"; return 1; }
    [ "$(pebble_orders)" = "1" ] || { echo "Pebble orders after the first installation: $(pebble_orders), expected 1"; return 1; }
}

# frontend_reads_key — (c): nginx's user in the frontend reads the key, and
# it belongs to the served certificate. The key goes through a pipe only.
frontend_reads_key() {
    local uid key_pub cert_pub
    uid="$(stack exec -T frontend id -u)" || return 1
    [ "$uid" = "101" ] || { echo "frontend runs as uid $uid, not 101"; return 1; }
    key_pub="$(stack exec -T frontend cat "/etc/letsencrypt/live/$DOMAIN/privkey.pem" | openssl pkey -pubout | openssl sha256)" \
        || { echo "the frontend (uid 101) cannot read /etc/letsencrypt/live/$DOMAIN/privkey.pem"; return 1; }
    cert_pub="$(openssl x509 -in "$(live cert.pem)" -noout -pubkey | openssl sha256)"
    [ "$key_pub" = "$cert_pub" ] || { echo "the key read in the frontend does not belong to live/$DOMAIN/cert.pem"; return 1; }
}

nginx_t_as_101() {
    # shellcheck disable=SC2016 # expanded in the frontend container
    stack exec -T frontend sh -c 'echo "uid $(id -u)"; nginx -t'
}

wait_healthy() {
    local id status="" _
    id="$(stack ps -q "$1")"
    [ -n "$id" ] || { echo "$1 has no container"; return 1; }
    for _ in $(seq 1 60); do
        status="$(docker inspect -f '{{.State.Health.Status}}' "$id" 2>/dev/null)"
        [ "$status" = "healthy" ] && return 0
        sleep 2
    done
    echo "$1 is '$status' after 120 s"
    stack logs --no-color --tail 20 "$1"
    return 1
}

# restart_serves_live_chain — (c): the restarted frontend becomes healthy and
# serves the certificate of live/<domain>/ with a chain to the Pebble root.
restart_serves_live_chain() {
    stack restart frontend || return 1
    wait_healthy frontend || return 1
    local out served want
    out="$(openssl s_client -connect 127.0.0.1:443 -servername "$DOMAIN" -verify_hostname "$DOMAIN" \
        -CAfile "$PEBBLE_ROOT" -verify_return_error </dev/null 2>&1)" \
        || { printf '%s\n' "$out" | tail -n 8; echo "the served chain does not verify against the Pebble root"; return 1; }
    grep -q 'Verify return code: 0 (ok)' <<< "$out" || { printf '%s\n' "$out" | tail -n 8; return 1; }
    served="$(fingerprint <<< "$out")"
    want="$(fingerprint -in "$(live cert.pem)")"
    if [ -z "$served" ] || [ "$served" != "$want" ]; then
        echo "served: ${served:-nothing}, live/$DOMAIN/cert.pem: $want"
        return 1
    fi
}

after_issuance_checks() {
    check "pebble: $1: the frontend (uid 101) reads live/<domain>/privkey.pem" frontend_reads_key
    check "pebble: $1: nginx -t passes as uid 101"                            nginx_t_as_101
    check "pebble: $1: restarted frontend is healthy and serves the chain"     restart_serves_live_chain
}

# renew_with_service_command — (c): the renewal command of the certbot
# service (its loop in docker-compose.yml, with the service's environment)
# with --force-renewal.
renew_with_service_command() {
    local id loop cmd before orders key
    id="$(stack ps -q certbot)"
    [ -n "$id" ] || { echo "the certbot service is not running"; return 1; }
    loop="$(docker inspect -f '{{index .Config.Cmd 1}}' "$id")" || return 1
    cmd="${loop#*do }"
    cmd="${cmd%%; sleep*}"
    case "$cmd" in
        "certbot renew "*"--server "*) ;;
        *) echo "unexpected renewal loop: $loop"; return 1 ;;
    esac
    before="$(fingerprint -in "$(live cert.pem)")"
    orders="$(pebble_orders)"
    echo "renewal: $cmd --force-renewal"
    stack exec -T certbot sh -c "$cmd --force-renewal" || { echo "certbot renew failed"; return 1; }
    [ "$(fingerprint -in "$(live cert.pem)")" != "$before" ] || { echo "live/$DOMAIN/cert.pem was not renewed"; return 1; }
    [ "$(pebble_orders)" = "$((orders + 1))" ] || { echo "Pebble orders: $(pebble_orders), expected $((orders + 1))"; return 1; }
    chain_is_pebble || return 1
    no_second_lineage || return 1
    key="$(readlink -f "$(live privkey.pem)")"
    case "$key" in */archive/"$DOMAIN"/privkey2.pem) ;; *) echo "renewed key: $key"; return 1 ;; esac
    [ "$(stat -c '%u:%g %a' "$key")" = "0:101 640" ] || { echo "renewed key: $(stat -c '%u:%g %a' "$key"), expected 0:101 640 (deploy hook)"; return 1; }
}

rerun_places_no_order() {
    local orders="$1" rc="$2"
    [ "$rc" -eq 0 ] || { echo "second run: rc $rc"; return 1; }
    [ "$(pebble_orders)" = "$orders" ] || { echo "Pebble orders: $(pebble_orders) after the second run, $orders before"; return 1; }
    no_second_lineage || return 1
    chain_is_pebble || return 1
    log_has second "Let's Encrypt certificate for $DOMAIN already present" || return 1
    ! grep -qF "Requesting a Let's Encrypt certificate" "$LOG_DIR/install-second.log" || { echo "the second run requested a certificate"; return 1; }
}

# wipe_tls_state — removes letsencrypt/ (certbot's files belong to root) with
# the stack stopped, so the next run starts with a new placeholder and a new
# ACME account.
wipe_tls_state() {
    [ -d "$LE_DIR" ] || return 0
    docker run --rm -v "$LE_DIR:/le" --entrypoint sh "$CERTBOT_IMAGE" -c 'rm -rf /le/* /le/.[!.]*'
}

failed_challenge_keeps_placeholder() {
    local rc="$1" invalid_before="$2" chain p
    chain="$(live fullchain.pem)"
    [ "$rc" -eq 0 ] || { echo "installer rc $rc"; return 1; }
    log_has failed-challenge "Let's Encrypt certificate issuance failed" "self-signed bootstrap certificate (issuance failed" || return 1
    [ "$(pebble_count "set INVALID by completed challenge")" -gt "$invalid_before" ] \
        || { echo "Pebble did not reject a challenge (no 'set INVALID by completed challenge')"; return 1; }
    openssl verify -CAfile "$chain" "$chain" >/dev/null 2>&1 || { echo "live/$DOMAIN/fullchain.pem is not the self-signed placeholder"; return 1; }
    ! openssl verify -CAfile "$PEBBLE_ROOT" -untrusted "$chain" "$chain" >/dev/null 2>&1 || { echo "live/$DOMAIN/fullchain.pem chains to Pebble"; return 1; }
    no_placeholder_backup || return 1
    for p in "archive/$DOMAIN" "renewal/$DOMAIN.conf"; do
        [ ! -e "$LE_DIR/$p" ] || { echo "left behind by the failed request: $p"; return 1; }
    done
}

# ─── cleanup ─────────────────────────────────────────────────────────────────

collect_logs() {
    pebble logs --no-color > "$LOG_DIR/pebble.log" 2>&1 || true
    if [ -f "$ENV_FILE" ]; then
        stack ps -a > "$LOG_DIR/stack-ps.log" 2>&1 || true
        stack logs --no-color --tail 300 frontend certbot server keycloak > "$LOG_DIR/stack.log" 2>&1 || true
    fi
}

cleanup() {
    collect_logs
    if [ -f "$ENV_FILE" ]; then
        stack down -v --remove-orphans >/dev/null 2>&1 || true
    fi
    pebble down -v --remove-orphans >/dev/null 2>&1 || true
    wipe_tls_state >/dev/null 2>&1 || true
    rmdir "$LE_DIR" 2>/dev/null || true
    # .env did not exist before this test (see main).
    rm -f "$ENV_FILE" "$ENV_FILE.previous"
    rm -rf "$WORK"
}

# ─── main ────────────────────────────────────────────────────────────────────

main() {
    LOG_DIR="${1:-}"
    if [ -e "$ENV_FILE" ]; then
        echo "$ENV_FILE exists: this checkout holds an installation. The test installs and removes its own stack (volumes included); run it in a clean checkout (CI)." >&2
        exit 2
    fi
    local tool
    for tool in docker openssl curl ip getent awk; do
        command -v "$tool" >/dev/null || { echo "$tool is required." >&2; exit 2; }
    done
    docker compose version >/dev/null || { echo "docker compose is required." >&2; exit 2; }
    [ -f "$SYSTEM_CA_BUNDLE" ] || { echo "$SYSTEM_CA_BUNDLE (system CA bundle) is required." >&2; exit 2; }

    [ -n "$LOG_DIR" ] || LOG_DIR="$(mktemp -d)"
    mkdir -p "$LOG_DIR" || exit 2
    LOG_DIR="$(cd "$LOG_DIR" && pwd)"
    WORK="$(mktemp -d)"
    chmod 0755 "$WORK"
    trap cleanup EXIT
    setup_names || exit 1
    printf 'host IP:     %s\ndomain:      %s\nACME server: %s\nlogs:        %s\n' "$HOST_IP" "$DOMAIN" "$ACME_SERVER" "$LOG_DIR" | tee "$LOG_DIR/setup.txt"

    docker pull -q "$CERTBOT_IMAGE" >/dev/null || { echo "Cannot pull $CERTBOT_IMAGE." >&2; exit 1; }
    if ! resolves_to_host; then
        fail "pebble: $DOMAIN resolves to the host IP on the host and in containers (set RETICORA_PEBBLE_DOMAIN)"
        exit 1
    fi
    make_pebble_tls || { echo "Cannot create the TLS files of Pebble." >&2; exit 1; }
    start_pebble || { fail "pebble: Pebble and challtestsrv start"; exit 1; }
    challtest add-a "{\"host\":\"$DOMAIN\",\"addresses\":[\"$HOST_IP\"]}" >/dev/null || { fail "pebble: A record in challtestsrv"; exit 1; }

    # (a), (d): first installation; .env only names the domain.
    mkdir -p "$COMPOSE_DIR"
    printf 'RETICORA_PUBLIC_BASE_URL=https://%s\nRETICORA_TLS_DOMAIN=%s\n' "$DOMAIN" "$DOMAIN" > "$ENV_FILE"
    chmod 0600 "$ENV_FILE"
    local rc=0
    run_installer first || rc=$?
    if [ "$rc" -ne 0 ]; then
        fail "pebble: first installation rc 0 (rc $rc, see $LOG_DIR/install-first.log)"
        exit 1
    fi
    ok "pebble: first installation rc 0"
    check "pebble: first installation replaces the placeholder with the Pebble chain (one order)" issued_in_first_run
    check "pebble: validate_tls_material <domain> 1 passes on the issued chain" validate_tls_material_passes
    check "pebble: first installation logs both issuer checks"                  issuer_checks_logged first

    # (c) after the issuance.
    after_issuance_checks "after the issuance"

    # (b), (d): second installer run.
    local orders
    orders="$(pebble_orders)"
    rc=0
    run_installer second || rc=$?
    check "pebble: second run rc 0 without a new order or a -0001 lineage" rerun_places_no_order "$orders" "$rc"
    check "pebble: second run logs both issuer checks"                       issuer_checks_logged second

    # (c) after the renewal.
    check "pebble: certbot renew --force-renewal (renewal command of the certbot service)" renew_with_service_command
    after_issuance_checks "after the renewal"

    # (e), (d): the challenge fails (no A record in challtestsrv).
    stack down >/dev/null 2>&1 || true
    wipe_tls_state || { fail "pebble: TLS state removed for the failed challenge"; exit 1; }
    challtest clear-a "{\"host\":\"$DOMAIN\"}" >/dev/null || { fail "pebble: A record removed from challtestsrv"; exit 1; }
    local invalid
    invalid="$(pebble_count "set INVALID by completed challenge")"
    rc=0
    run_installer failed-challenge || rc=$?
    check "pebble: failed challenge keeps the placeholder, installer rc 0 with the warning" failed_challenge_keeps_placeholder "$rc" "$invalid"
    check "pebble: run with the failed challenge logs both issuer checks"                  issuer_checks_logged failed-challenge

    echo
    printf '%d passed, %d failed\n' "$PASS" "$FAIL"
    [ "$FAIL" -eq 0 ]
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    main "$@"
fi
