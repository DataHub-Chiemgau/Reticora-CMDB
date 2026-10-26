#!/usr/bin/env bash
#
# install-cloud-helpers.test.sh — unit tests for the validation helpers in
# install-cloud.sh (Epic A of the hardening plan). Runs without bats or any
# other dependency; each test exercises one function sourced from the
# installer with the side-effecting `main` call stripped.
#
# Usage: ./tests/install-cloud-helpers.test.sh
set -uo pipefail

cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

# Load install-cloud.sh without executing main: the script ends with
# `main "$@"`; dropping the last line removes that call.
HARNESS="$TMP_ROOT/harness.sh"
head -n -1 install-cloud.sh > "$HARNESS"
cp install-preflight.sh "$TMP_ROOT/"

PASS=0
FAIL=0

ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

# Source the installer with no positional arguments — its top-level argument
# parser rejects anything it does not know (e.g. the test function names).
load_installer() {
    set --
    # shellcheck disable=SC1090
    . "$HARNESS"
    NON_INTERACTIVE=1
}

run_test() {
    local name="$1"; shift
    if ( set -e; load_installer; "$@" ) >"$TMP_ROOT/out" 2>&1; then
        ok "$name"
    else
        fail "$name"
        sed 's/^/     /' "$TMP_ROOT/out" | tail -n 5
    fi
}

expect_fail() {
    local name="$1"; shift
    if ( set -e; load_installer; "$@" ) >"$TMP_ROOT/out" 2>&1; then
        fail "$name (expected failure, got success)"
    else
        ok "$name"
    fi
}

# ─── validate_realm_json ──────────────────────────────────────────────────────

test_realm_valid() {
    local f="$TMP_ROOT/realm-valid.json"
    printf '{"realm": "reticora", "enabled": true}\n' > "$f"
    validate_realm_json "$f"
}

test_realm_leftover_placeholder() {
    local f="$TMP_ROOT/realm-placeholder.json"
    printf '{"realm": "reticora", "secret": "RETICORA_OIDC_CLIENT_SECRET_PLACEHOLDER"}\n' > "$f"
    validate_realm_json "$f"
}

test_realm_invalid_json() {
    local f="$TMP_ROOT/realm-broken.json"
    printf '{"realm": "reticora",\n' > "$f"
    validate_realm_json "$f"
}

test_realm_wrong_realm_name() {
    local f="$TMP_ROOT/realm-wrong.json"
    printf '{"realm": "master", "enabled": true}\n' > "$f"
    validate_realm_json "$f"
}

run_test   "validate_realm_json accepts a valid realm file"                 test_realm_valid
expect_fail "validate_realm_json rejects leftover placeholders"             test_realm_leftover_placeholder
expect_fail "validate_realm_json rejects invalid JSON"                      test_realm_invalid_json
expect_fail "validate_realm_json rejects a file without the reticora realm" test_realm_wrong_realm_name

# ─── validate_tls_material ────────────────────────────────────────────────────

make_cert() {
    # make_cert <dir> <domain>
    local dir="$1" domain="$2"
    mkdir -p "$dir"
    openssl req -x509 -newkey rsa:2048 -nodes \
        -keyout "$dir/privkey.pem" \
        -out "$dir/fullchain.pem" \
        -days 30 -subj "/CN=$domain" \
        -addext "subjectAltName=DNS:$domain" 2>/dev/null
}

test_tls_valid() {
    RETICORA_TLS_DOMAIN="cmdb.example.com"
    local live="$TMP_ROOT/tls-valid/live/$RETICORA_TLS_DOMAIN"
    make_cert "$live" "$RETICORA_TLS_DOMAIN"
    tls_live_dir() { printf '%s' "$live"; }
    validate_tls_material "$RETICORA_TLS_DOMAIN"
}

test_tls_key_mismatch() {
    RETICORA_TLS_DOMAIN="cmdb2.example.com"
    local live="$TMP_ROOT/tls-mismatch/live/$RETICORA_TLS_DOMAIN"
    make_cert "$live" "$RETICORA_TLS_DOMAIN"
    # Overwrite the key with a fresh, non-matching one.
    openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$live/privkey.pem" 2>/dev/null
    tls_live_dir() { printf '%s' "$live"; }
    validate_tls_material "$RETICORA_TLS_DOMAIN"
}

test_tls_unparseable() {
    RETICORA_TLS_DOMAIN="cmdb3.example.com"
    local live="$TMP_ROOT/tls-broken/live/$RETICORA_TLS_DOMAIN"
    mkdir -p "$live"
    printf 'not a certificate\n' > "$live/fullchain.pem"
    openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$live/privkey.pem" 2>/dev/null
    tls_live_dir() { printf '%s' "$live"; }
    validate_tls_material "$RETICORA_TLS_DOMAIN"
}

run_test   "validate_tls_material accepts a consistent cert/key pair" test_tls_valid
expect_fail "validate_tls_material rejects a mismatched key"          test_tls_key_mismatch
expect_fail "validate_tls_material rejects an unparseable cert"       test_tls_unparseable

# ─── TLS detection, ACME path and template (WP-094, INS-03) ──────────────────

# make_ca_chain <dir> <domain> — a private CA (ca.pem) and a leaf certificate
# for <domain> signed by an intermediate, written as fullchain.pem (leaf +
# intermediate) and privkey.pem like certbot does.
make_ca_chain() {
    local dir="$1" domain="$2"
    mkdir -p "$dir"
    openssl req -x509 -newkey rsa:2048 -nodes -keyout "$dir/ca.key" -out "$dir/ca.pem" -days 30 \
        -subj "/CN=Test Root" -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
    openssl req -newkey rsa:2048 -nodes -keyout "$dir/int.key" -out "$dir/int.csr" -subj "/CN=Test Intermediate" 2>/dev/null
    printf 'basicConstraints=critical,CA:TRUE\nkeyUsage=critical,keyCertSign,cRLSign\n' > "$dir/int.ext"
    openssl x509 -req -in "$dir/int.csr" -CA "$dir/ca.pem" -CAkey "$dir/ca.key" -CAcreateserial -days 30 \
        -extfile "$dir/int.ext" -out "$dir/int.pem" 2>/dev/null
    openssl req -newkey rsa:2048 -nodes -keyout "$dir/privkey.pem" -out "$dir/leaf.csr" -subj "/CN=$domain" 2>/dev/null
    printf 'subjectAltName=DNS:%s\n' "$domain" > "$dir/leaf.ext"
    openssl x509 -req -in "$dir/leaf.csr" -CA "$dir/int.pem" -CAkey "$dir/int.key" -CAcreateserial -days 30 \
        -extfile "$dir/leaf.ext" -out "$dir/leaf.pem" 2>/dev/null
    cat "$dir/leaf.pem" "$dir/int.pem" > "$dir/fullchain.pem"
}

# The bootstrap placeholder is self-signed: ACME must run. (The former
# subject=/issuer= comparison never matched and skipped ACME.)
test_tls_placeholder_detected() {
    RETICORA_TLS_DOMAIN="placeholder.example.com"
    local live="$TMP_ROOT/tls-placeholder/live/$RETICORA_TLS_DOMAIN"
    make_cert "$live" "$RETICORA_TLS_DOMAIN"
    tls_live_dir() { printf '%s' "$live"; }
    tls_cert_is_selfsigned
}

test_tls_missing_counts_as_placeholder() {
    RETICORA_TLS_DOMAIN="missing.example.com"
    tls_live_dir() { printf '%s' "$TMP_ROOT/tls-none"; }
    tls_cert_is_selfsigned
}

# A certificate whose chain verifies is a real one: no new request.
test_tls_trusted_chain_detected() {
    RETICORA_TLS_DOMAIN="real.example.com"
    local live="$TMP_ROOT/tls-real/live/$RETICORA_TLS_DOMAIN"
    make_ca_chain "$live" "$RETICORA_TLS_DOMAIN"
    tls_live_dir() { printf '%s' "$live"; }
    RETICORA_TLS_CA_BUNDLE="$live/ca.pem"
    ! tls_cert_is_selfsigned && validate_tls_material "$RETICORA_TLS_DOMAIN" 1
}

# Without its intermediate the chain does not verify: rejected after issuance.
test_tls_incomplete_chain_rejected() {
    RETICORA_TLS_DOMAIN="chain.example.com"
    local live="$TMP_ROOT/tls-chain/live/$RETICORA_TLS_DOMAIN"
    make_ca_chain "$live" "$RETICORA_TLS_DOMAIN"
    cp "$live/leaf.pem" "$live/fullchain.pem"
    tls_live_dir() { printf '%s' "$live"; }
    RETICORA_TLS_CA_BUNDLE="$live/ca.pem"
    validate_tls_material "$RETICORA_TLS_DOMAIN" 1
}

# The ACME request runs certbot itself, not the renewal-loop entrypoint, with
# the webroot, the lineage name and the deploy hook that the certbot service
# mounts (WP-419). Only the call is checked: certbot's storage, the deploy
# hook and the nginx check are covered by tests/install-cloud-acme.test.sh.
test_tls_acme_uses_certbot_entrypoint() {
    unset RETICORA_ACME_SERVER RETICORA_ACME_CA_BUNDLE
    RETICORA_TLS_DOMAIN="acme.example.com"
    RETICORA_CERT_EMAIL="ops@example.com"
    COMPOSE_DIR="$TMP_ROOT/tls-acme"
    local calls="$TMP_ROOT/tls-acme-calls" want
    compose_cmd() { printf '[%s]' "$@" >> "$calls"; printf '\n' >> "$calls"; return 1; }
    issue_tls_certificate
    want="$(certbot_call_without_acme_settings acme.example.com)"
    [ "$(head -n 1 "$calls")" = "$want" ] || { printf 'got:  %s\nwant: %s\n' "$(head -n 1 "$calls")" "$want"; return 1; }
    # The certbot service mounts the hook there and renews with it as well
    # (against the ACME server of the issuance, WP-420).
    # shellcheck disable=SC2016 # literal text of docker-compose.yml
    grep -qx -- '      - ./certbot-deploy-hook.sh:/usr/local/bin/reticora-certbot-deploy-hook.sh:ro' \
        "$REPO_ROOT/deploy/docker-compose/docker-compose.yml" &&
        grep -q -- 'certbot renew --webroot -w /var/www/certbot --quiet --deploy-hook "sh /usr/local/bin/reticora-certbot-deploy-hook.sh" --server "$$RETICORA_ACME_SERVER";' \
            "$REPO_ROOT/deploy/docker-compose/docker-compose.yml" &&
        [ -f "$REPO_ROOT/deploy/docker-compose/certbot-deploy-hook.sh" ]
}

# certbot_call_without_acme_settings <domain> — install-cloud.sh's certbot call
# before WP-420 (compose_cmd argv, e-mail ops@example.com); without
# RETICORA_ACME_SERVER and RETICORA_ACME_CA_BUNDLE it stays exactly this.
certbot_call_without_acme_settings() {
    printf '%s' '[run][--rm][--no-deps][--entrypoint][certbot][certbot][certonly][--webroot][-w][/var/www/certbot]'
    printf '[-d][%s][--cert-name][%s][--non-interactive][--agree-tos]' "$1" "$1"
    printf '%s' '[--deploy-hook][sh /usr/local/bin/reticora-certbot-deploy-hook.sh][--email][ops@example.com]'
}

# A served real certificate is not requested again.
test_tls_acme_skipped_with_real_certificate() {
    RETICORA_TLS_DOMAIN="kept.example.com"
    local live="$TMP_ROOT/tls-kept/live/$RETICORA_TLS_DOMAIN"
    make_ca_chain "$live" "$RETICORA_TLS_DOMAIN"
    tls_live_dir() { printf '%s' "$live"; }
    RETICORA_TLS_CA_BUNDLE="$live/ca.pem"
    compose_cmd() { return 1; }
    issue_tls_certificate
}

test_tls_template_selection() {
    RETICORA_TLS_DOMAIN=""
    [ "$(tls_template_source)" = "./nginx-no-tls.conf.template" ] || return 1
    RETICORA_TLS_DOMAIN="cmdb.example.com"
    [ "$(tls_template_source)" = "./nginx-tls.conf.template" ] || return 1
    # Both templates exist next to the compose file, and the no-TLS one
    # declares no server block.
    [ -f "$REPO_ROOT/deploy/docker-compose/nginx-tls.conf.template" ] &&
        [ -f "$REPO_ROOT/deploy/docker-compose/nginx-no-tls.conf.template" ] &&
        ! grep -q 'server' <(grep -v '^#' "$REPO_ROOT/deploy/docker-compose/nginx-no-tls.conf.template") &&
        grep -q 'RETICORA_NGINX_TLS_TEMPLATE' "$REPO_ROOT/deploy/docker-compose/docker-compose.yml"
}

run_test   "tls: the self-signed placeholder is detected"                 test_tls_placeholder_detected
run_test   "tls: a missing certificate counts as placeholder"              test_tls_missing_counts_as_placeholder
run_test   "tls: a verifying chain is a real certificate"                  test_tls_trusted_chain_detected
expect_fail "tls: an incomplete chain is rejected after issuance"          test_tls_incomplete_chain_rejected
run_test   "tls: ACME runs certbot with the webroot and the deploy hook"  test_tls_acme_uses_certbot_entrypoint
run_test   "tls: no new request while a real certificate is served"        test_tls_acme_skipped_with_real_certificate
run_test   "tls: the TLS template is mounted only with a domain"           test_tls_template_selection

# ─── OIDC issuer from the host and the container (WP-095, INS-02) ───────────

ISSUER="http://192.0.2.10:8180/realms/reticora"

# curl stub: serves a discovery document announcing $STUB_ANNOUNCED, or
# fails when it is empty.
stub_discovery() {
    STUB_ANNOUNCED="$1"
    RETICORA_ISSUER_RETRY_DELAY=0
    curl() {
        [ -n "$STUB_ANNOUNCED" ] || return 7
        printf '{\n  "issuer": "%s",\n  "jwks_uri": "x"\n}\n' "$STUB_ANNOUNCED"
    }
}

test_issuer_host_matches() {
    RETICORA_OIDC_ISSUER_URL="$ISSUER/"
    RETICORA_TLS_DOMAIN=""
    stub_discovery "$ISSUER"
    check_issuer_from_host
}

test_issuer_host_other_issuer() {
    RETICORA_OIDC_ISSUER_URL="$ISSUER"
    RETICORA_TLS_DOMAIN=""
    stub_discovery "http://localhost:8180/realms/reticora"
    local out
    out="$( (check_issuer_from_host) 2>&1)" && return 1
    printf '%s' "$out" | grep -q "announces itself as 'http://localhost:8180/realms/reticora'" &&
        printf '%s' "$out" | grep -q "docs/troubleshooting-installation.md"
}

test_issuer_host_unreachable() {
    RETICORA_OIDC_ISSUER_URL="$ISSUER"
    RETICORA_TLS_DOMAIN=""
    stub_discovery ""
    local out
    out="$( (check_issuer_from_host) 2>&1)" && return 1
    printf '%s' "$out" | grep -q "not reachable from this host" &&
        printf '%s' "$out" | grep -q "docs/troubleshooting-installation.md"
}

test_issuer_container_check_runs_server() {
    RETICORA_OIDC_ISSUER_URL="$ISSUER"
    local calls="$TMP_ROOT/issuer-compose-calls"
    compose_cmd() { printf '%s\n' "$*" >> "$calls"; }
    check_issuer_from_container
    grep -qx -- "run --rm --no-deps --entrypoint server server -check-oidc-issuer" "$calls"
}

test_issuer_container_unreachable() {
    RETICORA_OIDC_ISSUER_URL="$ISSUER"
    compose_cmd() { echo "OIDC issuer check failed: dial tcp 192.0.2.10:8180: connect: connection refused"; return 1; }
    local out
    out="$( (check_issuer_from_container) 2>&1)" && return 1
    printf '%s' "$out" | grep -q "connection refused" &&
        printf '%s' "$out" | grep -q "backend container cannot reach the OIDC issuer" &&
        printf '%s' "$out" | grep -q "docs/troubleshooting-installation.md"
}

test_issuer_loopback_refused() {
    RETICORA_OIDC_ISSUER_URL="http://localhost:8180/realms/reticora"
    RETICORA_HOST_IP="192.0.2.10"
    local out
    out="$( (validate_keycloak_bootstrap) 2>&1)" && return 1
    printf '%s' "$out" | grep -q "backend container cannot reach" &&
        printf '%s' "$out" | grep -q "http://192.0.2.10:8180/realms/reticora"
}

test_host_primary_ip() {
    RETICORA_HOST_IP="198.51.100.7"
    [ "$(host_primary_ip)" = "198.51.100.7" ] || return 1
    issuer_is_loopback "http://127.0.0.1:8180/realms/reticora" &&
        issuer_is_loopback "https://localhost/realms/reticora" &&
        ! issuer_is_loopback "$ISSUER"
}

test_issuer_troubleshooting_documented() {
    grep -q "OIDC issuer check failed" "$REPO_ROOT/docs/troubleshooting-installation.md"
}

run_test   "issuer: discovery from the host announces the configured issuer" test_issuer_host_matches
run_test   "issuer: another announced issuer is refused with a hint"         test_issuer_host_other_issuer
run_test   "issuer: an unreachable issuer is refused with a hint"            test_issuer_host_unreachable
run_test   "issuer: the backend container checks the issuer itself"          test_issuer_container_check_runs_server
run_test   "issuer: unreachable from the container is refused with a hint"   test_issuer_container_unreachable
run_test   "issuer: a localhost issuer is refused, the host IP suggested"    test_issuer_loopback_refused
run_test   "issuer: host IP and loopback detection"                          test_host_primary_ip
run_test   "issuer: the troubleshooting guide covers the check"              test_issuer_troubleshooting_documented

# ─── issuer checks after the frontend start (WP-385, INS-02, F-035) ──────────

# stub_issuer_stack <case> [after-frontend|never] — start_stack without side
# effects. The issuer https://<domain>/realms/reticora is served by the
# frontend's /realms/ proxy: the curl stub answers it, and the backend
# container reaches it, only after `up -d server frontend` (never with
# "never"); Keycloak's host port answers always.
# shellcheck disable=SC2317,SC2034 # stubs and variables used by the installer
stub_issuer_stack() {
    STACK_CALLS="$TMP_ROOT/$1-calls"
    FRONTEND_UP="$TMP_ROOT/$1-frontend-up"
    ISSUER_SERVED="${2:-after-frontend}"
    : > "$STACK_CALLS"
    rm -f "$FRONTEND_UP"
    RETICORA_KEYCLOAK_PORT=8180
    RETICORA_ISSUER_RETRY_DELAY=0
    RETICORA_TLS_DOMAIN="cmdb.example.test"
    RETICORA_OIDC_ISSUER_URL="https://cmdb.example.test/realms/reticora"
    tls_live_dir() { printf '%s/no-tls-live' "$TMP_ROOT"; }
    issuer_served() { [ "$ISSUER_SERVED" = "after-frontend" ] && [ -e "$FRONTEND_UP" ]; }
    compose_cmd() {
        printf 'compose %s\n' "$*" >> "$STACK_CALLS"
        case "$*" in
            "up -d server frontend") touch "$FRONTEND_UP" ;;
            "run --rm --no-deps --entrypoint server server -check-oidc-issuer")
                issuer_served || { echo "OIDC issuer check failed: dial tcp: connection refused"; return 1; } ;;
        esac
        return 0
    }
    curl() {
        local url="${*: -1}" issuer="${RETICORA_OIDC_ISSUER_URL%/}"
        case "$url" in
            "http://localhost:8180/"*) ;;
            "$issuer"/*) issuer_served || return 7 ;;
            *) return 7 ;;
        esac
        printf 'curl %s\n' "$url" >> "$STACK_CALLS"
        printf '{"issuer": "%s"}\n' "$issuer"
    }
    wait_for_service() { printf 'wait %s\n' "$1" >> "$STACK_CALLS"; }
    sync_db_password() { :; }
    update_timescaledb() { :; }
    run_migrations() { :; }
    seed_default_organization() { :; }
    ensure_runtime_login() { :; }
    create_initial_admin() { printf 'create_initial_admin\n' >> "$STACK_CALLS"; }
    show_service_logs() { :; }
}

# line_in_calls <fixed line> — number of its first occurrence, empty if none.
line_in_calls() { grep -n -x -F -- "$1" "$STACK_CALLS" | head -n 1 | cut -d: -f1; }

# A domain issuer that only the started frontend serves: start_stack
# completes, both checks pass after the frontend became healthy.
test_issuer_domain_served_by_frontend() {
    stub_issuer_stack frontend-issuer
    start_stack
    local frontend host container
    frontend="$(line_in_calls 'wait frontend')"
    host="$(line_in_calls "curl $RETICORA_OIDC_ISSUER_URL/.well-known/openid-configuration")"
    container="$(line_in_calls 'compose run --rm --no-deps --entrypoint server server -check-oidc-issuer')"
    [ -n "$frontend" ] && [ -n "$host" ] && [ -n "$container" ] || return 1
    [ "$frontend" -lt "$host" ] && [ "$host" -lt "$container" ]
}

# Order of the whole run (main): Keycloak health and realm before
# create_initial_admin, which stays before the server start; the strict
# checks after `up -d server frontend` and both health waits (after a reload
# of a regenerated bootstrap certificate) and before print_summary. The realm
# validation itself never asks the configured issuer.
# shellcheck disable=SC2317,SC2034 # stubs and variables used by main
test_issuer_checks_order() {
    stub_issuer_stack order
    RETICORA_PUBLIC_BASE_URL="https://cmdb.example.test"
    RETICORA_OIDC_REDIRECT_URL="https://cmdb.example.test/auth/callback"
    RETICORA_FRONTEND_PORT=80
    RETICORA_SERVER_PORT=8080
    RETICORA_IMAGE_REGISTRY=""
    RETICORA_IMAGE_TAG=""
    # The order of the issuer checks is the same in both modes; the source
    # build needs no release (E-69: the default selects and verifies one,
    # tests/install-signature.test.sh).
    BUILD_FROM_SOURCE=1
    preflight() { :; }
    collect_config() { :; }
    write_env() { :; }
    ensure_session_key() { :; }
    configure_realm() { :; }
    bootstrap_tls_cert() { TLS_BOOTSTRAP_REGENERATED=1; }
    provide_images() { :; }
    # Would write the journald drop-in of the host (NFR-06,
    # tests/install-log-retention.test.sh).
    configure_log_retention() { :; }
    issue_tls_certificate() { :; }
    verify_stack() { :; }
    gc_images() { :; }
    print_summary() { printf 'print_summary\n' >> "$STACK_CALLS"; }
    main
    local issuer_probe="curl $RETICORA_OIDC_ISSUER_URL/.well-known/openid-configuration"
    local container_probe="compose run --rm --no-deps --entrypoint server server -check-oidc-issuer"
    local keycloak realm admin up server frontend reload host container summary
    keycloak="$(line_in_calls 'wait keycloak')"
    realm="$(line_in_calls 'curl http://localhost:8180/realms/reticora/.well-known/openid-configuration')"
    admin="$(line_in_calls 'create_initial_admin')"
    up="$(line_in_calls 'compose up -d server frontend')"
    server="$(line_in_calls 'wait server')"
    frontend="$(line_in_calls 'wait frontend')"
    reload="$(line_in_calls 'compose exec -T frontend nginx -s reload')"
    host="$(line_in_calls "$issuer_probe")"
    container="$(line_in_calls "$container_probe")"
    summary="$(line_in_calls 'print_summary')"
    local n prev=0
    for n in "$keycloak" "$realm" "$admin" "$up" "$server" "$frontend" "$reload" "$host" "$container" "$summary"; do
        if [ -z "$n" ] || [ "$n" -le "$prev" ]; then sed 's/^/  /' "$STACK_CALLS"; return 1; fi
        prev="$n"
    done
    [ "$(grep -c -x -F -- "$issuer_probe" "$STACK_CALLS")" -eq 1 ] &&
        [ "$(grep -c -x -F -- "$container_probe" "$STACK_CALLS")" -eq 1 ]
}

# The checks run after create_initial_admin: a failure in the run that just
# created the administrator shows its temporary password with the error (a
# re-run would not show it again), otherwise no password.
# shellcheck disable=SC2317 # the stubs are called by the installer functions
test_issuer_failure_keeps_initial_login() {
    stub_issuer_stack failed-check never
    RETICORA_INITIAL_ADMIN_EMAIL="ops@example.test"
    create_initial_admin() { INITIAL_ADMIN_PASSWORD="Tmp-only-shown-once-1Aa-"; }
    local out rc
    out="$( (start_stack) 2>&1)"; rc=$?
    [ "$rc" -eq 1 ] || return 1
    printf '%s' "$out" | grep -q "not reachable from this host" &&
        printf '%s' "$out" | grep -q "docs/troubleshooting-installation.md" &&
        printf '%s' "$out" | grep -q -F "Initial login:     ops@example.test / Tmp-only-shown-once-1Aa-" || return 1
    create_initial_admin() { INITIAL_ADMIN_PASSWORD=""; }
    out="$( (start_stack) 2>&1)"; rc=$?
    [ "$rc" -eq 1 ] && ! printf '%s' "$out" | grep -q "Initial login:"
}

run_test   "issuer: a domain issuer served by the frontend is checked after its start" test_issuer_domain_served_by_frontend
run_test   "issuer: strict checks after server and frontend, before the summary"       test_issuer_checks_order
run_test   "issuer: a failed check shows the temporary password of this run"           test_issuer_failure_keeps_initial_login

# ─── re-run: .env merge and password probes (WP-096, INS-05) ─────────────────

set_env_values() {
    local v
    for v in KEYCLOAK_ADMIN KEYCLOAK_ADMIN_PASSWORD KEYCLOAK_DB_PASSWORD RETICORA_CERT_EMAIL RETICORA_DB_PASSWORD \
        RETICORA_FRONTEND_PORT RETICORA_GC_KEEP_IMAGES RETICORA_IMAGE_REGISTRY RETICORA_IMAGE_TAG \
        RETICORA_INITIAL_ADMIN_EMAIL RETICORA_KEYCLOAK_PORT RETICORA_MASTER_KEY RETICORA_OIDC_CLIENT_ID \
        RETICORA_OIDC_CLIENT_SECRET RETICORA_OIDC_ISSUER_URL RETICORA_OIDC_REDIRECT_URL RETICORA_PUBLIC_BASE_URL \
        RETICORA_S3_ACCESS_KEY RETICORA_S3_SECRET_KEY RETICORA_SERVER_PORT RETICORA_BACKUP_CIPHER_PASS \
        RETICORA_DB_APP_PASSWORD; do
        printf -v "$v" '%s' "new-$v"
    done
    RETICORA_TLS_DOMAIN=""
}

# A re-run keeps settings the installer does not write itself, replaces the
# ones it writes and does not duplicate the kept ones on the next run.
test_env_merge_keeps_extra_settings() {
    ENV_FILE="$TMP_ROOT/merge.env"
    printf '# old header\nRETICORA_DB_PASSWORD=old\nRETICORA_HTTP_BIND=127.0.0.1\nRETICORA_EXTRA="a=b"\n\n# comment\n' > "$ENV_FILE"
    set_env_values
    write_env
    write_env
    grep -qx 'RETICORA_HTTP_BIND=127.0.0.1' "$ENV_FILE" &&
        grep -qx 'RETICORA_EXTRA="a=b"' "$ENV_FILE" &&
        grep -qx 'RETICORA_DB_PASSWORD=new-RETICORA_DB_PASSWORD' "$ENV_FILE" &&
        [ "$(grep -c '^RETICORA_DB_PASSWORD=' "$ENV_FILE")" -eq 1 ] &&
        [ "$(grep -c '^RETICORA_HTTP_BIND=' "$ENV_FILE")" -eq 1 ] &&
        [ "$(grep -c 'kept from the previous .env' "$ENV_FILE")" -eq 1 ] &&
        [ "$(stat -c %a "$ENV_FILE")" = "600" ]
}

test_env_first_run_has_no_extras() {
    ENV_FILE="$TMP_ROOT/fresh.env"
    rm -f "$ENV_FILE"
    set_env_values
    write_env
    ! grep -q 'kept from the previous .env' "$ENV_FILE"
}

# The probe connects over TCP to the service address: the postgres images
# trust the local socket and 127.0.0.1 regardless of the password.
test_password_probe_uses_tcp() {
    local calls="$TMP_ROOT/probe-calls"
    compose_cmd() { printf '%s\n' "$*" >> "$calls"; }
    db_password_matches keycloak-db keycloak keycloak secret
    grep -qx -- "exec -T -e PGPASSWORD=secret -e PGCONNECT_TIMEOUT=5 keycloak-db psql -h keycloak-db -U keycloak -d keycloak -tAc SELECT 1" "$calls"
}

test_password_sync_covers_both_databases() {
    local calls="$TMP_ROOT/sync-calls"
    RETICORA_DB_PASSWORD="cmdb-pw"
    KEYCLOAK_DB_PASSWORD="kc-pw"
    db_password_matches() { printf '%s\n' "$*" >> "$calls"; }
    sync_db_password
    grep -qx "postgres reticora reticora cmdb-pw" "$calls" && grep -qx "keycloak-db keycloak keycloak kc-pw" "$calls"
}

# A mismatching Keycloak DB password is reset on the keycloakdb volume with
# the Keycloak image and verified over TCP afterwards.
test_password_sync_resets_keycloak_db() {
    local calls="$TMP_ROOT/reset-calls" probes="$TMP_ROOT/reset-probes"
    : > "$probes"
    db_password_matches() { printf 'x\n' >> "$probes"; [ "$(wc -l < "$probes")" -gt 1 ]; }
    compose_cmd() { printf 'compose %s\n' "$*" >> "$calls"; }
    wait_for_service() { return 0; }
    db_volume_name() { printf 'proj_%s' "$2"; }
    db_image() { printf 'image-of-%s' "$1"; }
    fake_docker() {
        printf 'docker %s\n' "$*" >> "$calls"
        if [ "$1" = "exec" ]; then cat >> "$calls"; fi
        return 0
    }
    DOCKER=fake_docker
    sync_role_password keycloak-db keycloak keycloak kc-pw keycloakdb
    grep -q -- "compose rm -sf keycloak-db" "$calls" &&
        grep -q -- "-v proj_keycloakdb:/var/lib/postgresql/data" "$calls" &&
        grep -q -- "image-of-keycloak-db" "$calls" &&
        grep -q -- 'ALTER ROLE "keycloak" WITH PASSWORD' "$calls" &&
        ! grep -q -- "kc-pw" <(grep '^docker run\|ALTER' "$calls" | grep -v 'POSTGRES_PASSWORD') &&
        grep -q -- "compose up -d keycloak-db" "$calls" &&
        [ "$(wc -l < "$probes")" -eq 2 ]
}

run_test   "re-run: .env keeps extra settings such as RETICORA_HTTP_BIND"   test_env_merge_keeps_extra_settings
run_test   "re-run: a fresh .env has no kept section"                       test_env_first_run_has_no_extras
run_test   "re-run: the password probe connects over TCP"                   test_password_probe_uses_tcp
run_test   "re-run: CMDB and Keycloak database passwords are both probed"   test_password_sync_covers_both_databases
run_test   "re-run: a stale Keycloak DB password is reset and re-verified"  test_password_sync_resets_keycloak_db

# ─── runtime database login without SUPERUSER/BYPASSRLS (WP-348, TEN-03) ────

APP_PW="0123456789abcdef0123456789abcdef"

# The role script creates the login only when it is missing, then forces the
# attributes, strips every membership except reticora_app and sets the
# password through a psql variable.
test_runtime_login_sql() {
    local sql
    sql="$(runtime_login_sql "$APP_PW")"
    printf '%s\n' "$sql" > "$TMP_ROOT/runtime-login.sql"
    [ "$(printf '%s\n' "$sql" | head -n 1)" = "\\set pw '$APP_PW'" ] || return 1
    grep -qxF "SELECT 'CREATE ROLE reticora_runtime' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'reticora_runtime') \\gexec" "$TMP_ROOT/runtime-login.sql" || return 1
    grep -qxF "ALTER ROLE reticora_runtime WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION INHERIT PASSWORD :'pw';" "$TMP_ROOT/runtime-login.sql" || return 1
    grep -qF "SELECT format('REVOKE %I FROM reticora_runtime GRANTED BY %I', r.rolname, pg_get_userbyid(m.grantor))" "$TMP_ROOT/runtime-login.sql" || return 1
    grep -qF "WHERE m.member = 'reticora_runtime'::regrole AND r.rolname <> 'reticora_app' \\gexec" "$TMP_ROOT/runtime-login.sql" || return 1
    grep -qxF "GRANT reticora_app TO reticora_runtime;" "$TMP_ROOT/runtime-login.sql" || return 1
    # Idempotent: no unconditional CREATE, nothing dropped, no other grant,
    # and the password appears only in the \set line.
    if grep -qiE '^[[:space:]]*CREATE ROLE|DROP |WITH ADMIN|WITH GRANT' "$TMP_ROOT/runtime-login.sql"; then return 1; fi
    [ "$(grep -c '^GRANT' "$TMP_ROOT/runtime-login.sql")" -eq 1 ] || return 1
    [ "$(grep -c "$APP_PW" "$TMP_ROOT/runtime-login.sql")" -eq 1 ] || return 1
    # Same input, same script: a re-run repeats exactly the same repair.
    [ "$sql" = "$(runtime_login_sql "$APP_PW")" ] || return 1
}

# ensure_runtime_login feeds the script on stdin to psql as the bootstrap
# superuser in one transaction, never with the password on a command line,
# then proves the password over TCP via sync_role_password.
# shellcheck disable=SC2317 # the stubs are called by the installer functions
test_runtime_login_ensure() {
    local calls="$TMP_ROOT/runtime-calls" stdin="$TMP_ROOT/runtime-stdin"
    : > "$calls"; : > "$stdin"
    RETICORA_DB_APP_PASSWORD="$APP_PW"
    compose_cmd() { printf 'compose %s\n' "$*" >> "$calls"; cat >> "$stdin"; }
    sync_role_password() { printf 'sync %s\n' "$*" >> "$calls"; }
    ensure_runtime_login
    ensure_runtime_login
    [ "$(grep -c '^compose exec -T postgres psql -U reticora -d reticora -v ON_ERROR_STOP=1 -q --single-transaction$' "$calls")" -eq 2 ] || return 1
    [ "$(grep -c "^sync postgres reticora_runtime reticora $APP_PW pgdata$" "$calls")" -eq 2 ] || return 1
    ! grep -q "^compose.*$APP_PW" "$calls" || return 1
    [ "$(grep -c "^\\\\set pw '$APP_PW'$" "$stdin")" -eq 2 ] || return 1
    ! grep -qE 'down|volume|rm ' "$calls"
}

# shellcheck disable=SC2317 # the stubs are called by the installer functions
test_runtime_login_failure_aborts() {
    RETICORA_DB_APP_PASSWORD="$APP_PW"
    compose_cmd() { cat > /dev/null; return 3; }
    sync_role_password() { return 0; }
    ensure_runtime_login
}

# The login is created after the migrations (they create reticora_app) and
# before the server starts; the bootstrap superuser keeps the migrations.
# shellcheck disable=SC2317 # the stubs are called by the installer functions
test_runtime_login_order() {
    local calls="$TMP_ROOT/order-calls"
    : > "$calls"
    compose_cmd() { printf 'compose %s\n' "$*" >> "$calls"; }
    wait_for_service() { return 0; }
    sync_db_password() { printf 'sync_db_password\n' >> "$calls"; }
    update_timescaledb() { :; }
    run_migrations() { printf 'run_migrations\n' >> "$calls"; }
    seed_default_organization() { printf 'seed\n' >> "$calls"; }
    ensure_runtime_login() { printf 'ensure_runtime_login\n' >> "$calls"; }
    validate_keycloak_bootstrap() { :; }
    create_initial_admin() { :; }
    check_issuer_from_host() { :; }
    check_issuer_from_container() { :; }
    RETICORA_TLS_DOMAIN=""
    start_stack
    local mig login server
    mig="$(grep -n '^run_migrations$' "$calls" | cut -d: -f1)"
    login="$(grep -n '^ensure_runtime_login$' "$calls" | cut -d: -f1)"
    server="$(grep -n '^compose up -d server frontend$' "$calls" | cut -d: -f1)"
    [ -n "$mig" ] && [ -n "$login" ] && [ -n "$server" ] && [ "$mig" -lt "$login" ] && [ "$login" -lt "$server" ] || return 1
    # shellcheck disable=SC2016 # literal compose/shell variable reference
    grep -q 'postgres://reticora:${RETICORA_DB_PASSWORD}@localhost:5432/reticora' "$REPO_ROOT/install-cloud.sh"
}

# .env of an installation from before the runtime login (all settings of the
# previous write_env, no RETICORA_DB_APP_PASSWORD).
write_legacy_env() {
    ENV_FILE="$TMP_ROOT/$1.env"
    cat > "$ENV_FILE" <<'ENV'
RETICORA_DB_PASSWORD=legacy-db-password
RETICORA_S3_ACCESS_KEY=reticora
RETICORA_S3_SECRET_KEY=legacy-s3-secret
KEYCLOAK_ADMIN=admin
KEYCLOAK_ADMIN_PASSWORD=legacy-kc-password
KEYCLOAK_DB_PASSWORD=legacy-kc-db-password
RETICORA_INITIAL_ADMIN_EMAIL=ops@acme.example
RETICORA_OIDC_CLIENT_SECRET=legacy-client-secret
RETICORA_MASTER_KEY=bGVnYWN5LW1hc3Rlci1rZXktMzItYnl0ZXMtbG9uZyE=
RETICORA_BACKUP_CIPHER_PASS=legacy-cipher
RETICORA_PUBLIC_BASE_URL=http://192.0.2.10:3000
RETICORA_OIDC_ISSUER_URL=http://192.0.2.10:8180/realms/reticora
RETICORA_OIDC_CLIENT_ID=reticora-app
RETICORA_OIDC_REDIRECT_URL=http://192.0.2.10:3000/auth/callback
RETICORA_TLS_DOMAIN=
RETICORA_FRONTEND_PORT=3000
RETICORA_SERVER_PORT=8080
RETICORA_KEYCLOAK_PORT=8180
RETICORA_IMAGE_REGISTRY=
RETICORA_IMAGE_TAG=latest
RETICORA_HTTP_BIND=127.0.0.1
ENV
}

# Upgrade: a re-run on a legacy .env generates the runtime password once,
# keeps every other value (in particular the superuser password and thus the
# existing data volume) and keeps the generated password on later runs.
test_runtime_login_upgrade() {
    write_legacy_env upgrade
    collect_config >/dev/null
    write_env
    local pw
    pw="$(env_get RETICORA_DB_APP_PASSWORD)"
    [[ "$pw" =~ ^[0-9a-f]{48}$ ]] || return 1
    [ "$(grep -c '^RETICORA_DB_APP_PASSWORD=' "$ENV_FILE")" -eq 1 ] || return 1
    grep -qx 'RETICORA_DB_PASSWORD=legacy-db-password' "$ENV_FILE" &&
        grep -qx 'RETICORA_BACKUP_CIPHER_PASS=legacy-cipher' "$ENV_FILE" &&
        grep -qx 'RETICORA_MASTER_KEY=bGVnYWN5LW1hc3Rlci1rZXktMzItYnl0ZXMtbG9uZyE=' "$ENV_FILE" &&
        grep -qx 'RETICORA_HTTP_BIND=127.0.0.1' "$ENV_FILE" || return 1
    collect_config >/dev/null
    write_env
    [ "$(env_get RETICORA_DB_APP_PASSWORD)" = "$pw" ] || return 1
    [ "$RETICORA_DB_PASSWORD" = "legacy-db-password" ] || return 1
    runtime_login_sql "$RETICORA_DB_APP_PASSWORD" | grep -qxF "\\set pw '$pw'"
}

# Rotation: a changed value in .env is applied by the next run.
# shellcheck disable=SC2317 # the stubs are called by the installer functions
test_runtime_login_rotation() {
    write_legacy_env rotation
    printf 'RETICORA_DB_APP_PASSWORD=%s\n' "$APP_PW" >> "$ENV_FILE"
    collect_config >/dev/null
    [ "$RETICORA_DB_APP_PASSWORD" = "$APP_PW" ] || return 1
    sed -i "s/^RETICORA_DB_APP_PASSWORD=.*/RETICORA_DB_APP_PASSWORD=rotated-${APP_PW}/" "$ENV_FILE"
    collect_config >/dev/null
    [ "$RETICORA_DB_APP_PASSWORD" = "rotated-$APP_PW" ] || return 1
    local calls="$TMP_ROOT/rotation-calls" stdin="$TMP_ROOT/rotation-stdin"
    compose_cmd() { cat > "$stdin"; }
    sync_role_password() { printf '%s\n' "$*" > "$calls"; }
    ensure_runtime_login
    grep -qxF "\\set pw 'rotated-$APP_PW'" "$stdin" && grep -qx "postgres reticora_runtime reticora rotated-$APP_PW pgdata" "$calls"
}

# The password is part of the database URL: characters that would break it
# or the psql literal are refused instead of being written to the server.
test_runtime_login_rejects_unsafe_password() {
    write_legacy_env unsafe
    printf "RETICORA_DB_APP_PASSWORD=abc'def@ghi/jklmnop\n" >> "$ENV_FILE"
    collect_config >/dev/null
}

test_runtime_password_validation() {
    valid_app_password "$APP_PW" && valid_app_password "Ab-._~0123456789" &&
        ! valid_app_password "short" && ! valid_app_password "0123456789abcdef'" &&
        ! valid_app_password "0123456789abcdef@x" && ! valid_app_password "0123456789 abcdef"
}

# Compose: the server logs in as reticora_runtime with its own password and
# an application_name for pg_stat_activity; the superuser serves only the
# database itself and the backups.
test_runtime_login_compose() {
    local compose="$REPO_ROOT/deploy/docker-compose/docker-compose.yml" url
    url="$(grep -E '^[[:space:]]+RETICORA_DATABASE_URL:' "$compose")"
    [ "$(printf '%s\n' "$url" | wc -l)" -eq 1 ] || return 1
    # shellcheck disable=SC2016 # literal compose/shell variable reference
    printf '%s' "$url" | grep -qF '"postgres://reticora_runtime:${RETICORA_DB_APP_PASSWORD:?' || return 1
    printf '%s' "$url" | grep -qF '@postgres:5432/reticora?sslmode=disable&application_name=reticora-server"' || return 1
    ! printf '%s' "$url" | grep -q 'RETICORA_DB_PASSWORD' || return 1
    grep -qx '      POSTGRES_USER: reticora' "$compose" && grep -qx '      PGUSER: reticora' "$compose"
}

# Kubernetes: the base takes the server's DSN from the runtime-login secret,
# never from the migration one; the overlays inherit it (staging patches only
# what differs, WP-422) or, where they set it, use the same secret.
test_runtime_login_k8s() {
    local f ref
    ref="$(grep -A5 -- '- name: RETICORA_DATABASE_URL' "$REPO_ROOT/deploy/k8s/base/deployment.yaml")"
    printf '%s' "$ref" | grep -q 'name: reticora-db-runtime' && printf '%s' "$ref" | grep -q 'key: database-url' || return 1
    for f in "$REPO_ROOT"/deploy/k8s/overlays/*/kustomization.yaml; do
        ref="$(grep -A5 -- '- name: RETICORA_DATABASE_URL' "$f" || true)"
        [ -z "$ref" ] && continue
        printf '%s' "$ref" | grep -q 'name: reticora-db-runtime' && printf '%s' "$ref" | grep -q 'key: database-url' || return 1
    done
    if grep -rhE '^[^#]*name: reticora-db-migrate' "$REPO_ROOT/deploy/k8s"; then return 1; fi
    grep -q 'reticora-db-runtime' "$REPO_ROOT/deploy/README.md" && grep -q 'reticora-db-migrate' "$REPO_ROOT/deploy/README.md" &&
        grep -q 'NOSUPERUSER NOBYPASSRLS' "$REPO_ROOT/deploy/README.md"
}

run_test    "runtime login: SQL creates once, forces attributes and memberships" test_runtime_login_sql
run_test    "runtime login: applied as superuser via stdin, verified over TCP"   test_runtime_login_ensure
expect_fail "runtime login: a failing role script aborts the installation"      test_runtime_login_failure_aborts
run_test    "runtime login: created after the migrations, before the server"     test_runtime_login_order
run_test    "runtime login: upgrade generates the password, keeps all data"      test_runtime_login_upgrade
run_test    "runtime login: a changed password in .env is rotated"               test_runtime_login_rotation
expect_fail "runtime login: an unsafe password in .env is refused"               test_runtime_login_rejects_unsafe_password
run_test    "runtime login: password validation"                                 test_runtime_password_validation
run_test    "runtime login: the compose server uses reticora_runtime"            test_runtime_login_compose
run_test    "runtime login: k8s server DSN comes from the runtime secret"        test_runtime_login_k8s

# ─── image source on a (re-)run (WP-388, E-69 Option 1) ──────────────────────
# The signed release images are the default, also for the .env of an earlier
# source build; a mirror comes from RETICORA_IMAGE_REGISTRY or .env, the
# release from --release (otherwise select_release in main). collect_config
# itself never asks GitHub; --build-from-source writes neither registry nor
# release.

# shellcheck disable=SC2317 # the stubs are called by the installer functions
test_image_source_default() {
    write_legacy_env image-default
    curl() { echo "collect_config must not call curl" >&2; return 1; }
    collect_config >/dev/null
    [ "$RETICORA_IMAGE_REGISTRY" = "ghcr.io/datahub-chiemgau" ] && [ -z "$RETICORA_IMAGE_TAG" ] || return 1
    write_env
    grep -qx 'RETICORA_IMAGE_REGISTRY=ghcr.io/datahub-chiemgau' "$ENV_FILE" && grep -qx 'RETICORA_SERVER_IMAGE=' "$ENV_FILE"
}

# shellcheck disable=SC2034 # read by the installer functions
test_image_source_mirror() {
    write_legacy_env image-mirror
    sed -i 's|^RETICORA_IMAGE_REGISTRY=.*|RETICORA_IMAGE_REGISTRY=registry.example.test/reticora|' "$ENV_FILE"
    collect_config >/dev/null
    [ "$RETICORA_IMAGE_REGISTRY" = "registry.example.test/reticora" ] || return 1
    IMAGE_REGISTRY_OVERRIDE="mirror.example.test/reticora"
    collect_config >/dev/null
    [ "$RETICORA_IMAGE_REGISTRY" = "mirror.example.test/reticora" ]
}

# shellcheck disable=SC2317,SC2034 # stubs and variables used by the installer
test_image_source_release() {
    write_legacy_env image-release
    RELEASE="v2.0.1"
    curl() { return 1; }
    collect_config >/dev/null
    [ "$RETICORA_IMAGE_TAG" = "v2.0.1" ] || return 1
    select_release
    [ "$RETICORA_IMAGE_TAG" = "v2.0.1" ]
}

# shellcheck disable=SC2034 # read by the installer functions
test_image_source_build_from_source() {
    write_legacy_env image-bfs
    BUILD_FROM_SOURCE=1
    collect_config >/dev/null
    select_release
    write_env
    grep -qx 'RETICORA_IMAGE_REGISTRY=' "$ENV_FILE" && grep -qx 'RETICORA_IMAGE_TAG=' "$ENV_FILE"
}

run_test    "image source: default is the signed release registry, also after a source build" test_image_source_default
run_test    "image source: a mirror from .env or RETICORA_IMAGE_REGISTRY"                  test_image_source_mirror
run_test    "image source: --release names the release without asking GitHub"              test_image_source_release
run_test    "image source: --build-from-source writes no registry and no release"          test_image_source_build_from_source

# ─── ACME server and CA bundle (WP-420) ──────────────────────────────────────
# RETICORA_ACME_SERVER and RETICORA_ACME_CA_BUNDLE (environment, otherwise
# .env) reach the certbot request (--server), .env and the certbot service
# (REQUESTS_CA_BUNDLE, the mounted bundle, --server of the renewal loop).
# Without them the request is unchanged and the service renews against Let's
# Encrypt production with the image's CAs. The real run against Pebble is
# tests/install-acme-pebble.test.sh.

# acme_env <name> — legacy .env with a TLS domain and the e-mail of the call.
acme_env() {
    write_legacy_env "$1"
    sed -i 's/^RETICORA_TLS_DOMAIN=.*/RETICORA_TLS_DOMAIN=acme.example.com/' "$ENV_FILE"
    printf 'RETICORA_CERT_EMAIL=ops@example.com\n' >> "$ENV_FILE"
}

# acme_bundle <name> — a PEM CA certificate file.
acme_bundle() {
    local f="$TMP_ROOT/$1-acme-ca.pem"
    openssl req -x509 -newkey rsa:2048 -nodes -keyout "$TMP_ROOT/$1-acme-ca.key" -out "$f" -days 2 \
        -subj "/CN=ACME endpoint test CA" 2>/dev/null
    printf '%s' "$f"
}

# certbot_service <env file> — the certbot service of docker-compose.yml as
# docker compose renders it with <env file> (JSON). rc 2 when docker compose
# or jq is missing; the compose file text is checked in any case.
certbot_service() {
    command -v jq >/dev/null && docker compose version >/dev/null 2>&1 || return 2
    env -u RETICORA_ACME_SERVER -u RETICORA_ACME_CA_BUNDLE docker compose --env-file "$1" \
        -f "$REPO_ROOT/deploy/docker-compose/docker-compose.yml" config --format json certbot | jq -c '.services.certbot'
}

# compose_certbot_text — the certbot service's ACME lines in docker-compose.yml.
# shellcheck disable=SC2016 # literal text of docker-compose.yml
compose_certbot_text() {
    local f="$REPO_ROOT/deploy/docker-compose/docker-compose.yml"
    grep -qxF '      RETICORA_ACME_SERVER: "${RETICORA_ACME_SERVER:-https://acme-v02.api.letsencrypt.org/directory}"' "$f" &&
        grep -qxF '      REQUESTS_CA_BUNDLE: "${RETICORA_ACME_CA_BUNDLE:+/etc/reticora/acme-ca.pem}"' "$f" &&
        grep -qxF '      - ${RETICORA_ACME_CA_BUNDLE:-/dev/null}:/etc/reticora/acme-ca.pem:ro' "$f" &&
        grep -qF -- '--deploy-hook "sh /usr/local/bin/reticora-certbot-deploy-hook.sh" --server "$$RETICORA_ACME_SERVER";' "$f"
}

# certbot_service_has <json> <server> <bundle source> <REQUESTS_CA_BUNDLE>
certbot_service_has() {
    jq -e --arg server "$2" --arg source "$3" --arg requests "$4" '
        .environment.RETICORA_ACME_SERVER == $server
        and (.environment.REQUESTS_CA_BUNDLE // "") == $requests
        and ([.volumes[] | select(.target == "/etc/reticora/acme-ca.pem" and .source == $source and .read_only == true)] | length == 1)
        and (.command[1] | test("--server \"[$]+RETICORA_ACME_SERVER\";"))' <<< "$1" >/dev/null \
        || { echo "certbot service: $1"; return 1; }
}

# shellcheck disable=SC2034,SC2317 # variables and stubs used by the installer functions
test_acme_settings_absent_unchanged() {
    unset RETICORA_ACME_SERVER RETICORA_ACME_CA_BUNDLE
    ACME_SERVER_OVERRIDE=""
    ACME_CA_BUNDLE_OVERRIDE=""
    acme_env acme-absent
    collect_config >/dev/null
    write_env
    [ -z "$RETICORA_ACME_SERVER" ] && [ -z "$RETICORA_ACME_CA_BUNDLE" ] || return 1
    grep -qx 'RETICORA_ACME_SERVER=' "$ENV_FILE" && grep -qx 'RETICORA_ACME_CA_BUNDLE=' "$ENV_FILE" || return 1
    COMPOSE_DIR="$TMP_ROOT/acme-absent"
    local calls="$TMP_ROOT/acme-absent-calls" want out
    compose_cmd() { printf '[%s]' "$@" >> "$calls"; printf '\n' >> "$calls"; return 1; }
    out="$(issue_tls_certificate 2>&1)"
    want="$(certbot_call_without_acme_settings acme.example.com)"
    [ "$(head -n 1 "$calls")" = "$want" ] || { printf 'got:  %s\nwant: %s\n' "$(head -n 1 "$calls")" "$want"; return 1; }
    ! grep -q 'ACME server' <<< "$out" || return 1
    compose_certbot_text || { echo "docker-compose.yml: certbot service without the ACME settings"; return 1; }
    local svc rc=0
    svc="$(certbot_service "$ENV_FILE")" || rc=$?
    [ "$rc" -ne 2 ] || { echo "docker compose/jq missing: rendering not checked"; return 0; }
    [ "$rc" -eq 0 ] || return 1
    certbot_service_has "$svc" "https://acme-v02.api.letsencrypt.org/directory" /dev/null ""
}

# shellcheck disable=SC2317 # the stub is called by the installer functions
test_acme_settings_reach_certbot() {
    local bundle server="https://acme.test.example:14000/dir"
    bundle="$(acme_bundle acme-set)"
    # As in the environment of the installer run.
    RETICORA_ACME_SERVER="$server" RETICORA_ACME_CA_BUNDLE="$bundle" load_installer
    acme_env acme-set
    collect_config >/dev/null
    write_env
    [ "$RETICORA_ACME_SERVER" = "$server" ] && [ "$RETICORA_ACME_CA_BUNDLE" = "$bundle" ] || return 1
    grep -qx "RETICORA_ACME_SERVER=$server" "$ENV_FILE" && grep -qx "RETICORA_ACME_CA_BUNDLE=$bundle" "$ENV_FILE" || return 1
    COMPOSE_DIR="$TMP_ROOT/acme-set"
    local calls="$TMP_ROOT/acme-set-calls" want out
    compose_cmd() { printf '[%s]' "$@" >> "$calls"; printf '\n' >> "$calls"; return 1; }
    out="$(issue_tls_certificate 2>&1)"
    want="$(certbot_call_without_acme_settings acme.example.com)[--server][$server]"
    [ "$(head -n 1 "$calls")" = "$want" ] || { printf 'got:  %s\nwant: %s\n' "$(head -n 1 "$calls")" "$want"; return 1; }
    grep -qF "(ACME server: $server)" <<< "$out" || return 1
    compose_certbot_text || return 1
    local svc rc=0
    svc="$(certbot_service "$ENV_FILE")" || rc=$?
    [ "$rc" -ne 2 ] || { echo "docker compose/jq missing: rendering not checked"; return 0; }
    [ "$rc" -eq 0 ] || return 1
    certbot_service_has "$svc" "$server" "$bundle" /etc/reticora/acme-ca.pem
}

# A re-run keeps the settings of .env; the environment overrides them.
# shellcheck disable=SC2034 # read by the installer functions
test_acme_settings_from_env_file() {
    local bundle
    bundle="$(acme_bundle acme-kept)"
    acme_env acme-kept
    printf 'RETICORA_ACME_SERVER=https://acme-staging-v02.api.letsencrypt.org/directory\nRETICORA_ACME_CA_BUNDLE=%s\n' "$bundle" >> "$ENV_FILE"
    ACME_SERVER_OVERRIDE=""
    ACME_CA_BUNDLE_OVERRIDE=""
    collect_config >/dev/null
    [ "$RETICORA_ACME_SERVER" = "https://acme-staging-v02.api.letsencrypt.org/directory" ] && [ "$RETICORA_ACME_CA_BUNDLE" = "$bundle" ] || return 1
    ACME_SERVER_OVERRIDE="https://10.1.0.5:14000/dir"
    collect_config >/dev/null
    [ "$RETICORA_ACME_SERVER" = "https://10.1.0.5:14000/dir" ] && [ "$RETICORA_ACME_CA_BUNDLE" = "$bundle" ]
}

# Values that would break .env, the volume or certbot's command line are
# refused before anything is written.
# shellcheck disable=SC2034 # read by the installer functions
test_acme_settings_validated() {
    local bundle v out
    bundle="$(acme_bundle acme-valid)"
    for v in "" "https://acme-v02.api.letsencrypt.org/directory" "https://10.1.0.5:14000/dir" "https://ca.internal:9000/acme/acme/directory"; do
        valid_optional_acme_server "$v" || { echo "refused: '$v'"; return 1; }
    done
    for v in "http://acme.example/dir" "acme.example/dir" "https://acme.example/dir x" "https://acme.example/\$HOME" \
        "https://acme.example/'dir'" "https://" $'https://acme.example/dir\nRETICORA_X=1'; do
        ! valid_optional_acme_server "$v" || { echo "accepted: '$v'"; return 1; }
    done
    if ! valid_optional_ca_bundle "" || ! valid_optional_ca_bundle "$bundle"; then
        echo "refused: $bundle"
        return 1
    fi
    printf 'no certificate\n' > "$TMP_ROOT/acme-valid-not-pem.pem"
    cp "$bundle" "$TMP_ROOT/acme:valid.pem"
    for v in "$TMP_ROOT/missing.pem" "$TMP_ROOT/acme-valid-not-pem.pem" "$TMP_ROOT/acme:valid.pem" "$TMP_ROOT" /dev/null; do
        ! valid_optional_ca_bundle "$v" || { echo "accepted: '$v'"; return 1; }
    done
    # A relative path, even of an existing PEM file (compose would resolve it
    # against deploy/docker-compose/).
    (cd "$TMP_ROOT" && ! valid_optional_ca_bundle "${bundle##*/}") || { echo "accepted the relative path ${bundle##*/}"; return 1; }
    acme_env acme-invalid
    ACME_SERVER_OVERRIDE="http://acme.example/dir"
    if out="$(collect_config 2>&1)"; then echo "collect_config accepted http://"; return 1; fi
    grep -q "RETICORA_ACME_SERVER ('http://acme.example/dir') must be the https:// directory URL" <<< "$out" || { printf '%s\n' "$out" | tail -n 2; return 1; }
    ACME_SERVER_OVERRIDE=""
    ACME_CA_BUNDLE_OVERRIDE="$TMP_ROOT/missing.pem"
    if out="$(collect_config 2>&1)"; then echo "collect_config accepted a missing bundle"; return 1; fi
    grep -q "RETICORA_ACME_CA_BUNDLE ('$TMP_ROOT/missing.pem') must be the absolute path of a readable PEM certificate file" <<< "$out" \
        || { printf '%s\n' "$out" | tail -n 2; return 1; }
    grep -q "Staging and test CAs" "$REPO_ROOT/docs/troubleshooting-installation.md"
}

run_test   "acme: without RETICORA_ACME_SERVER/_CA_BUNDLE the certbot call is unchanged" test_acme_settings_absent_unchanged
run_test   "acme: ACME server and CA bundle reach certonly, .env and the certbot service" test_acme_settings_reach_certbot
run_test   "acme: settings of .env are kept, the environment overrides them"        test_acme_settings_from_env_file
run_test   "acme: invalid ACME server or CA bundle is refused"                       test_acme_settings_validated

# ─── collector NATS endpoint on a re-run (WP-381) ─────────────────────────────

# nats-edge reads pki/edge.conf only at start: every run recreates it after
# the server is up (and has written the current edge.conf), so a re-run on an
# existing installation applies the new configuration (WP-381, COL-04).
# shellcheck disable=SC2317 # the stubs are called by the installer functions
test_nats_edge_recreated_after_server() {
    local calls="$TMP_ROOT/edge-calls"
    : > "$calls"
    compose_cmd() { printf 'compose %s\n' "$*" >> "$calls"; }
    wait_for_service() { printf 'wait %s\n' "$1" >> "$calls"; }
    sync_db_password() { :; }
    update_timescaledb() { :; }
    run_migrations() { :; }
    seed_default_organization() { :; }
    ensure_runtime_login() { :; }
    validate_keycloak_bootstrap() { :; }
    create_initial_admin() { :; }
    check_issuer_from_host() { :; }
    check_issuer_from_container() { :; }
    RETICORA_TLS_DOMAIN=""
    start_stack
    local server edge
    server="$(grep -n '^wait server$' "$calls" | cut -d: -f1)"
    edge="$(grep -n '^compose up -d --force-recreate nats-edge$' "$calls" | cut -d: -f1)"
    [ -n "$server" ] && [ -n "$edge" ] && [ "$server" -lt "$edge" ] || return 1
    [ "$(grep -c 'nats-edge' "$calls")" -eq 2 ] || return 1
    grep -qx 'wait nats-edge' "$calls"
}

run_test    "re-run: nats-edge is recreated after the server (new edge.conf)"  test_nats_edge_recreated_after_server

# ─── realm files and initial administrator (WP-044, AUT-09) ───────────────────

realm_py() {
    # realm_py <python expression over r (prod) and d (dev)>
    python3 -c "import json,sys; r=json.load(open('deploy/keycloak/realm-reticora.json')); d=json.load(open('deploy/keycloak/realm-reticora-dev.json')); sys.exit(0 if ($1) else 1)"
}

test_prod_realm_has_no_users() {
    realm_py "r.get('users') == []" || return 1
}

test_prod_path_has_no_known_password() {
    if grep -rqE 'admin123|Admin-Dev-2024|Demo-Dev-2024|Acme-Dev-2024' deploy/keycloak/realm-reticora.json install-cloud.sh install.sh deploy/docker-compose; then return 1; fi
}

test_prod_realm_policies() {
    realm_py "r['bruteForceProtected'] and r['failureFactor'] <= 5 and 'length(12)' in r['passwordPolicy'] and r['browserFlow'] == 'browser-mfa'" || return 1
    # org_admin and operator require the second factor.
    realm_py "all('mfa_required' in x.get('composites', {}).get('realm', []) for x in r['roles']['realm'] if x['name'] in ('org_admin', 'operator'))" || return 1
    realm_py "r['authenticatorConfig'][0]['config']['condUserRole'] == 'mfa_required'" || return 1
    # organization_id is a declared, admin-only user profile attribute.
    realm_py "any(a['name'] == 'organization_id' and a['permissions']['edit'] == ['admin'] for a in json.loads(r['components']['org.keycloak.userprofile.UserProfileProvider'][0]['config']['kc.user.profile.config'][0])['attributes'])" || return 1
}

test_dev_realm_differs_only_by_demo_users() {
    realm_py "{k: v for k, v in d.items() if k not in ('users', 'displayName')} == {k: v for k, v in r.items() if k not in ('users', 'displayName')}" || return 1
    # The demo user of the development stack (no org_admin) and the
    # administrators of the seeded organizations demo and acme (SIM-01,
    # WP-433; tests/audit/demo-realm-users.test.sh checks them).
    realm_py "[u['username'] for u in d['users']] == ['admin@reticora.local', 'admin@demo.local', 'admin@acme.local'] and 'org_admin' not in d['users'][0]['realmRoles']" || return 1
}

test_dev_compose_mounts_dev_realm_only() {
    grep -q 'deploy/keycloak/realm-reticora-dev.json:/opt/keycloak/data/import/' docker-compose.yml || return 1
    if grep -qE 'deploy/keycloak:/opt/keycloak/data/import' docker-compose.yml; then return 1; fi
    if grep -q 'realm-reticora-dev' deploy/docker-compose/docker-compose.yml deploy/docker-compose/docker-compose.override.yml install-cloud.sh; then return 1; fi
}

test_initial_password_random() {
    local a b
    a="$(generate_initial_password)"
    b="$(generate_initial_password)"
    [ "${#a}" -ge 24 ] && [ "$a" != "$b" ] || return 1
    printf '%s' "$a" | grep -q '[A-Z]' && printf '%s' "$a" | grep -q '[a-z]' \
        && printf '%s' "$a" | grep -q '[0-9]' && printf '%s' "$a" | grep -q '[^A-Za-z0-9]'
}

# fake_realm <kcadm args…> — the realm of the fake admin CLI (WP-428): a realm
# in the format of Keycloak's partial export in KC_STATE/realm.json, which the
# calls of apply_realm_settings change and its partial export reads back
# (client secrets masked). Flows keep their executions with ids, requirement
# and authenticator config like Keycloak: new executions are DISABLED and
# listed in creation order; a realm update replaces every field it sends,
# the attributes as a whole. Every write is recorded in KCADM_WRITES with
# what it changes (names, never a value of the client). KC_FAIL_REALM=1
# makes the realm update fail. Returns 3 for a call it does not handle.
# shellcheck disable=SC2317,SC2016 # called by the compose_cmd stub of fake_kcadm; jq programs
fake_realm() {
    local verb="$1" path="$2" realm="$KC_STATE/realm.json" body="$KC_STATE/realm-body" a prev="" flow=""
    local provider="" name="" rname="" gpath="" rolename=""
    shift 2
    for a in "$@"; do
        case "$prev" in
            -s) case "$a" in provider=*) provider="${a#provider=}" ;; name=*) name="${a#name=}" ;; esac ;;
            --rname) rname="$a" ;;
            --gpath) gpath="${a#/}" ;;
            --rolename) rolename="$a" ;;
        esac
        prev="$a"
    done
    case "$path" in
        authentication/flows/*/*) flow="${path#authentication/flows/}"; flow="${flow%%/*}"; flow="$(printf '%b' "${flow//%/\\x}")" ;;
    esac
    # realm_set <jq program> [jq args…] — changes the realm.
    realm_set() { jq "$@" "$realm" > "$realm.new" && mv "$realm.new" "$realm"; }
    # flow_of <execution id> — the alias of the flow that holds the execution.
    flow_of() { jq -r --arg id "$1" '[.authenticationFlows[] | select(any(.authenticationExecutions[]; .id == $id)) | .alias] | first' "$realm"; }
    next_id() { a=$(( $(cat "$KC_STATE/seq" 2>/dev/null || echo 0) + 1 )); echo "$a" > "$KC_STATE/seq"; printf '%s-%s' "$1" "$a"; }
    case "$verb $path" in
        "create partial-export") jq '(.clients[]?.secret) |= "**********"' "$realm" ;;
        "get users/profile")
            jq '.components["org.keycloak.userprofile.UserProfileProvider"][0].config["kc.user.profile.config"][0] | fromjson' "$realm" ;;
        "update users/profile")
            cat > "$body"
            printf 'update users/profile %s\n' "$(jq -r --slurpfile r "$realm" '[.attributes[].name]
                - [$r[0].components["org.keycloak.userprofile.UserProfileProvider"][0].config["kc.user.profile.config"][0]
                   | fromjson | .attributes[].name] | join(",")' "$body")" >> "$KCADM_WRITES"
            realm_set --slurpfile b "$body" \
                '.components["org.keycloak.userprofile.UserProfileProvider"][0].config["kc.user.profile.config"] = [$b[0] | tojson]' ;;
        "create roles")
            cat > "$body"
            printf 'create role %s\n' "$(jq -r .name "$body")" >> "$KCADM_WRITES"
            realm_set --slurpfile b "$body" '.roles.realm += [$b[0] + {composite: false}]' ;;
        "add-roles -r")
            if [ -n "$rname" ]; then
                printf 'add-roles role %s %s\n' "$rname" "$rolename" >> "$KCADM_WRITES"
                realm_set --arg r "$rname" --arg c "$rolename" \
                    '(.roles.realm[] | select(.name == $r)) |= (.composite = true | .composites.realm = ((.composites.realm // []) + [$c]))'
            elif [ -n "$gpath" ]; then
                printf 'add-roles group %s %s\n' "$gpath" "$rolename" >> "$KCADM_WRITES"
                realm_set --arg g "$gpath" --arg c "$rolename" '(.groups[] | select(.name == $g)) |= (.realmRoles = ((.realmRoles // []) + [$c]))'
            else
                return 3
            fi ;;
        "create groups")
            printf 'create group %s\n' "$name" >> "$KCADM_WRITES"
            realm_set --arg n "$name" '.groups += [{id: ("group-" + $n), name: $n, path: ("/" + $n), realmRoles: []}]' ;;
        "create client-scopes")
            cat > "$body"
            printf 'create client-scope %s\n' "$(jq -r .name "$body")" >> "$KCADM_WRITES"
            realm_set --slurpfile b "$body" '.clientScopes += [$b[0] + {id: ("scope-" + $b[0].name)}]'
            printf 'scope-%s\n' "$(jq -r .name "$body")" ;;
        "update clients/"*"/default-client-scopes/"*)
            a="${path#clients/}"
            printf 'default-client-scope %s\n' "$(jq -r --arg c "${a%%/*}" --arg s "${path##*/}" \
                '"\(.clients[] | select(.id == $c) | .clientId) \(.clientScopes[] | select(.id == $s) | .name)"' "$realm")" >> "$KCADM_WRITES"
            realm_set --arg c "${a%%/*}" --arg s "${path##*/}" \
                '([.clientScopes[] | select(.id == $s) | .name] | first) as $n | (.clients[] | select(.id == $c) | .defaultClientScopes) += [$n]' ;;
        "create authentication/flows")
            cat > "$body"
            printf 'create flow %s\n' "$(jq -r .alias "$body")" >> "$KCADM_WRITES"
            realm_set --slurpfile b "$body" '.authenticationFlows += [$b[0] + {id: ("flow-" + $b[0].alias), authenticationExecutions: []}]' ;;
        "create authentication/flows/"*"/executions/execution")
            printf 'create execution %s %s\n' "$flow" "$provider" >> "$KCADM_WRITES"
            realm_set --arg f "$flow" --arg p "$provider" --arg id "$(next_id exec)" '(.authenticationFlows[] | select(.alias == $f)
                | .authenticationExecutions) |= . + [{id: $id, authenticator: $p, authenticatorFlow: false, requirement: "DISABLED", priority: length}]' ;;
        "create authentication/flows/"*"/executions/flow")
            cat > "$body"
            printf 'create sub-flow %s %s\n' "$flow" "$(jq -r .alias "$body")" >> "$KCADM_WRITES"
            realm_set --arg f "$flow" --slurpfile b "$body" --arg id "$(next_id exec)" '(.authenticationFlows[] | select(.alias == $f)
                | .authenticationExecutions) |= . + [{id: $id, authenticatorFlow: true, flowAlias: $b[0].alias, requirement: "DISABLED", priority: length}]
                | .authenticationFlows += [{id: ("flow-" + $b[0].alias), alias: $b[0].alias, description: $b[0].description,
                    providerId: $b[0].type, topLevel: false, builtIn: false, authenticationExecutions: []}]' ;;
        "get authentication/flows/"*"/executions")
            jq --arg f "$flow" 'def rows($r; $a; $level):
                    [$r.authenticationFlows[] | select(.alias == $a) | .authenticationExecutions[]] | sort_by(.priority) | to_entries[]
                    | .key as $i | .value as $e
                    | {id: $e.id, requirement: $e.requirement, level: $level, index: $i, providerId: $e.authenticator,
                       authenticationFlow: $e.authenticatorFlow, displayName: ($e.flowAlias // $e.authenticator),
                       authenticationConfig: ([$r.authenticatorConfig[] | select(.alias == $e.authenticatorConfig) | .id] | first)},
                      (if $e.authenticatorFlow then rows($r; $e.flowAlias; $level + 1) else empty end);
                [rows(.; $f; 0)]' "$realm" ;;
        "update authentication/flows/"*"/executions")
            cat > "$body"
            a="$(jq -r .id "$body")"
            printf 'requirement %s %s %s\n' "$(flow_of "$a")" "$(jq -r --arg id "$a" \
                '[.authenticationFlows[].authenticationExecutions[] | select(.id == $id) | .authenticator // .flowAlias] | first' "$realm")" \
                "$(jq -r .requirement "$body")" >> "$KCADM_WRITES"
            realm_set --slurpfile b "$body" '(.authenticationFlows[].authenticationExecutions[] | select(.id == $b[0].id) | .requirement) = $b[0].requirement' ;;
        "create authentication/executions/"*"/config")
            cat > "$body"
            a="${path#authentication/executions/}"
            printf 'create config %s\n' "$(jq -r .alias "$body")" >> "$KCADM_WRITES"
            realm_set --slurpfile b "$body" --arg e "${a%/config}" --arg id "$(next_id config)" '.authenticatorConfig += [$b[0] + {id: $id}]
                | (.authenticationFlows[].authenticationExecutions[] | select(.id == $e) | .authenticatorConfig) = $b[0].alias' ;;
        "delete authentication/flows/"*)
            printf 'delete flow %s\n' "$(jq -r --arg id "${path##*/}" '.authenticationFlows[] | select(.id == $id) | .alias' "$realm")" >> "$KCADM_WRITES"
            realm_set --arg id "${path##*/}" 'def subs($r; $a): [$r.authenticationFlows[] | select(.alias == $a) | .authenticationExecutions[]
                    | select(.authenticatorFlow) | .flowAlias][] | ., subs($r; .);
                . as $r | ([.authenticationFlows[] | select(.id == $id) | .alias] | first) as $top | [$top, subs($r; $top)] as $gone
                | .authenticationFlows |= map(select(.alias as $a | $gone | index([$a]) == null))' ;;
        "delete authentication/config/"*)
            printf 'delete config %s\n' "$(jq -r --arg id "${path##*/}" '.authenticatorConfig[] | select(.id == $id) | .alias' "$realm")" >> "$KCADM_WRITES"
            realm_set --arg id "${path##*/}" '.authenticatorConfig |= map(select(.id != $id))' ;;
        "get realms/reticora")
            jq 'del(.roles, .groups, .clients, .clientScopes, .authenticationFlows, .authenticatorConfig, .components, .users)' "$realm" ;;
        "update realms/reticora")
            cat > "$body"
            [ -z "${KC_FAIL_REALM:-}" ] || return 1
            printf 'update realm %s\n' "$(jq -r --slurpfile r "$realm" '[(to_entries[] | select(.key != "attributes" and .value != $r[0][.key]) | .key),
                ((.attributes // {}) | to_entries[] | select(.value != ($r[0].attributes // {})[.key]) | "attributes.\(.key)")] | sort | join(",")' "$body")" \
                >> "$KCADM_WRITES"
            realm_set --slurpfile b "$body" '. + $b[0]' ;;
        *) return 3 ;;
    esac
}

# A fake Keycloak admin CLI: records the calls and knows the users listed in
# KC_USERS (username=id). The state a rotation changes lives in files under
# KC_STATE (kcadm also runs in pipelines, i.e. subshells): the password of
# the master administrator and the client reticora-app, initially as in the
# realm configure_realm rendered. Logins are checked against that password
# and recorded in KC_STATE/logins (ok/refused); the log masks the --password
# value. Every write is recorded in KCADM_WRITES with the fields it sets,
# never with values. KC_FAIL_UPDATE=1 makes client updates fail.
# shellcheck disable=SC2317 # the stubs are called by the installer functions
fake_kcadm() {
    KCADM_LOG="$TMP_ROOT/kcadm.log"
    KC_STATE="$TMP_ROOT/kc-state"
    KCADM_WRITES="$KC_STATE/writes"
    : > "$KCADM_LOG"
    rm -rf "$KC_STATE"
    mkdir -p "$KC_STATE"
    : > "$KCADM_WRITES"
    compose_cmd() {
        shift 4 # exec -T keycloak /opt/keycloak/bin/kcadm.sh
        local a pw="" prev="" logged=()
        for a in "$@"; do
            if [ "$prev" = --password ]; then pw="$a"; logged+=('***'); else logged+=("$a"); fi
            prev="$a"
        done
        printf '%s\n' "${logged[*]}" >> "$KCADM_LOG"
        local rc=0
        fake_realm "$@" || rc=$?
        [ "$rc" -eq 3 ] || return "$rc"
        case "$1 $2" in
            "config credentials")
                if [ "$pw" = "$(cat "$KC_STATE/admin-password")" ]; then
                    printf 'ok\n' >> "$KC_STATE/logins"
                    return 0
                fi
                printf 'refused\n' >> "$KC_STATE/logins"
                return 1 ;;
            "get clients") printf 'client-uuid\n'; return 0 ;;
            "get clients/client-uuid") cat "$KC_STATE/client.json"; return 0 ;;
            "update clients/client-uuid")
                cat > "$KC_STATE/body"
                [ -z "${KC_FAIL_UPDATE:-}" ] || return 1
                printf 'clients %s\n' "$(jq -r 'keys | join(",")' "$KC_STATE/body")" >> "$KCADM_WRITES"
                jq -s '.[0] * .[1]' "$KC_STATE/client.json" "$KC_STATE/body" > "$KC_STATE/client.new" &&
                    mv "$KC_STATE/client.new" "$KC_STATE/client.json"
                return ;;
            "update users/"*"/reset-password")
                cat > "$KC_STATE/body"
                a="${2#users/}"
                printf 'reset-password %s\n' "${a%/reset-password}" >> "$KCADM_WRITES"
                jq -r .value "$KC_STATE/body" > "$KC_STATE/admin-password"
                return ;;
        esac
        case "$2" in
            users)
                if [ "$1" = get ]; then
                    for a in "$@"; do
                        case "$a" in username=*)
                            printf '%s' "$KC_USERS" | tr ' ' '\n' | sed -n "s/^${a#username=}=//p" ;;
                        esac
                    done
                fi
                if [ "$1" = create ]; then KC_USERS="$KC_USERS $RETICORA_INITIAL_ADMIN_EMAIL=new-id"; fi ;;
            groups) [ "$1" = get ] && printf 'group-id\n' ;;
        esac
        return 0
    }
    # shellcheck disable=SC2034 # read by create_initial_admin
    KEYCLOAK_ADMIN="admin"
    # shellcheck disable=SC2034
    KEYCLOAK_ADMIN_PASSWORD="secret"
    RETICORA_INITIAL_ADMIN_EMAIL="ops@acme.example"
    # Keycloak as the previous run left it: the configured values applied.
    # shellcheck disable=SC2034 # read by configure_realm
    SCRIPT_DIR="$REPO_ROOT"
    COMPOSE_DIR="$KC_STATE/compose"
    ENV_FILE="$COMPOSE_DIR/.env"
    RETICORA_OIDC_ISSUER_URL="http://192.0.2.10:8180/realms/reticora"
    RETICORA_OIDC_CLIENT_SECRET="client-secret-0"
    RETICORA_OIDC_REDIRECT_URL="http://192.0.2.10:3000/auth/callback"
    kc_applied
}

# kc_applied — the fake Keycloak holds the current admin password and the
# client of the realm rendered from the current values (in another order:
# Keycloak stores the lists as sets), and the realm as a fresh import of the
# rendered realm (kc_imported_realm).
kc_applied() {
    configure_realm >/dev/null
    printf '%s\n' "$KEYCLOAK_ADMIN_PASSWORD" > "$KC_STATE/admin-password"
    jq '.clients[] | select(.clientId == "reticora-app")
        | {id: "client-uuid", clientId, secret, redirectUris: (.redirectUris | reverse), webOrigins: (.webOrigins | reverse), serviceAccountsEnabled}' \
        "$COMPOSE_DIR/.generated/realm-reticora.json" > "$KC_STATE/client.json"
    kc_imported_realm "$COMPOSE_DIR/.generated/realm-reticora.json" > "$KC_STATE/realm.json"
}

# kc_imported_realm <realm file> — the partial export of a realm Keycloak
# imported from the file: ids for the client, groups, flows, executions and
# authenticator configs, the client scopes Keycloak creates in every realm,
# its built-in browser flow and an attribute of its own (cibaInterval).
kc_imported_realm() {
    jq '.clients |= map(. + {id: "client-uuid"})
        | .groups |= map(. + {id: ("group-" + .name), realmRoles: (.realmRoles // [])})
        | .clientScopes = (["profile", "email", "acr", "roles", "web-origins"] | map({id: ("scope-" + .), name: .}))
        | .authenticationFlows = ([{alias: "browser", providerId: "basic-flow", topLevel: true, builtIn: true,
              authenticationExecutions: [{authenticator: "auth-cookie", authenticatorFlow: false, requirement: "ALTERNATIVE", priority: 10}]}]
            + (.authenticationFlows // []))
        | .authenticationFlows |= map(.alias as $a | . + {id: ("flow-" + $a)}
            | .authenticationExecutions |= (to_entries | map(.value + {id: "exec-\($a)-\(.key)"})))
        | .authenticatorConfig = ((.authenticatorConfig // []) | map(. + {id: ("config-" + .alias)}))
        | .attributes = ((.attributes // {}) + {cibaInterval: "5"})
        | .browserFlow = (.browserFlow // "browser")' "$1"
}

# kc_legacy_realm — the fake realm becomes the realm of the release before
# WP-044 (tests/keycloak/realm-reticora-legacy.json) as Keycloak 24 imports
# it: no password policy, brute-force detection off with Keycloak's defaults,
# browser flow "browser", no organization_id in the user profile.
kc_legacy_realm() {
    local current="$KC_STATE/realm.json"
    sed -e "s/RETICORA_OIDC_CLIENT_SECRET_PLACEHOLDER/$RETICORA_OIDC_CLIENT_SECRET/g" \
        -e "s|RETICORA_OIDC_REDIRECT_URL_PLACEHOLDER|$RETICORA_OIDC_REDIRECT_URL|g" \
        -e "s|RETICORA_OIDC_REDIRECT_ORIGIN_PLACEHOLDER|http://192.0.2.10:3000|g" \
        -e "s|RETICORA_OIDC_ISSUER_ORIGIN_PLACEHOLDER|http://192.0.2.10:8180|g" \
        "$REPO_ROOT/tests/keycloak/realm-reticora-legacy.json" > "$KC_STATE/legacy.json"
    kc_imported_realm "$KC_STATE/legacy.json" | jq --slurpfile cur "$current" 'del(.users)
        | . + {bruteForceProtected: false, permanentLockout: false, failureFactor: 30, waitIncrementSeconds: 60,
               maxFailureWaitSeconds: 900, minimumQuickLoginWaitSeconds: 60, quickLoginCheckMilliSeconds: 1000,
               maxDeltaTimeSeconds: 43200}
        | .components = ($cur[0].components | .["org.keycloak.userprofile.UserProfileProvider"][0].config["kc.user.profile.config"]
            |= map(fromjson | .attributes |= map(select(.name != "organization_id")) | tojson))' > "$KC_STATE/realm.new"
    mv "$KC_STATE/realm.new" "$current"
}

# kc_realm_edit <jq program> [jq args…] — changes the fake realm (operator
# settings, a customized flow) before a run.
kc_realm_edit() {
    jq "$@" "$KC_STATE/realm.json" > "$KC_STATE/realm.new" && mv "$KC_STATE/realm.new" "$KC_STATE/realm.json"
}

# kc_log_writes — the kcadm calls of KCADM_LOG that write; the partial export
# of apply_realm_settings is a POST that only reads.
kc_log_writes() { grep -E '^(update|create|delete|set-password|add-roles) ' "$KCADM_LOG" | grep -v '^create partial-export '; }

test_initial_admin_created_with_temporary_password() {
    fake_kcadm
    KC_USERS="admin@reticora.local=legacy-id"
    create_initial_admin || return 1
    [ -n "$INITIAL_ADMIN_PASSWORD" ] || return 1
    grep -q '^delete users/legacy-id -r reticora' "$KCADM_LOG" || return 1
    grep -q 'create users -r reticora .*username=ops@acme.example' "$KCADM_LOG" || return 1
    grep -q 'attributes.organization_id=\["00000000-0000-0000-0000-000000000001"\]' "$KCADM_LOG" || return 1
    grep -q 'requiredActions=\["UPDATE_PASSWORD","CONFIGURE_TOTP"\]' "$KCADM_LOG" || return 1
    grep -q "set-password -r reticora --username ops@acme.example --new-password $INITIAL_ADMIN_PASSWORD --temporary" "$KCADM_LOG" || return 1
    grep -q 'add-roles -r reticora --uusername ops@acme.example --rolename org_admin' "$KCADM_LOG" || return 1
    grep -q 'update users/new-id/groups/group-id' "$KCADM_LOG" || return 1
    initial_login_line | grep -q "ops@acme.example / $INITIAL_ADMIN_PASSWORD" || return 1
}

test_initial_admin_not_recreated() {
    fake_kcadm
    KC_USERS="ops@acme.example=existing-id"
    create_initial_admin || return 1
    [ -z "$INITIAL_ADMIN_PASSWORD" ] || return 1
    if kc_log_writes | grep -qE '^(create|set-password|delete)'; then return 1; fi
    initial_login_line | grep -q 'password not shown again' || return 1
}

test_initial_admin_rejects_demo_account() {
    valid_initial_admin_email "admin@reticora.local" || return 1
}

run_test    "production realm contains no user"                          test_prod_realm_has_no_users
run_test    "no known password in the production path"                   test_prod_path_has_no_known_password
run_test    "realm sets password, lockout and MFA policy"                 test_prod_realm_policies
run_test    "dev realm differs from production only by the demo users"    test_dev_realm_differs_only_by_demo_users
run_test    "dev compose imports the dev realm, production never"         test_dev_compose_mounts_dev_realm_only
run_test    "initial password is random and meets the policy"             test_initial_password_random
run_test    "initial administrator gets a temporary password once"        test_initial_admin_created_with_temporary_password
run_test    "an existing initial administrator is not recreated"          test_initial_admin_not_recreated
expect_fail "the demo account is rejected as initial administrator"       test_initial_admin_rejects_demo_account

# ─── rotation of Keycloak values on a re-run (WP-386, INS-05, F-125) ─────────

OLD_ADMIN_PW="old-admin-pw-0123"
NEW_ADMIN_PW="new-admin-pw-4567"
OLD_CLIENT_SECRET="old-client-secret-89ab"
NEW_CLIENT_SECRET="new-client-secret-cdef"

# kc_env <file> — the Keycloak settings of .env as the installer writes them.
kc_env() {
    printf 'KEYCLOAK_ADMIN=%s\nKEYCLOAK_ADMIN_PASSWORD=%s\nRETICORA_OIDC_CLIENT_SECRET=%s\nRETICORA_OIDC_ISSUER_URL=%s\nRETICORA_OIDC_REDIRECT_URL=%s\n' \
        "$KEYCLOAK_ADMIN" "$KEYCLOAK_ADMIN_PASSWORD" "$RETICORA_OIDC_CLIENT_SECRET" "$RETICORA_OIDC_ISSUER_URL" \
        "$RETICORA_OIDC_REDIRECT_URL" > "$1"
    chmod 600 "$1"
}

# kc_rerun <admin password> <client secret> <redirect URL> — an installation
# whose previous run applied the old values (Keycloak, .env.previous), and a
# re-run with the given values in .env (rendered like main does). The initial
# administrator exists already.
# shellcheck disable=SC2034 # read by the installer functions
kc_rerun() {
    fake_kcadm
    KC_USERS="admin=admin-id ops@acme.example=existing-id"
    KEYCLOAK_ADMIN_PASSWORD="$OLD_ADMIN_PW"
    RETICORA_OIDC_CLIENT_SECRET="$OLD_CLIENT_SECRET"
    RETICORA_OIDC_REDIRECT_URL="http://192.0.2.10:3000/auth/callback"
    kc_applied
    kc_env "$ENV_FILE.previous"
    KEYCLOAK_ADMIN_PASSWORD="$1"
    RETICORA_OIDC_CLIENT_SECRET="$2"
    RETICORA_OIDC_REDIRECT_URL="$3"
    kc_env "$ENV_FILE"
    configure_realm >/dev/null
}

# no_secret_in <file> — none of the passwords and secrets appears in it.
no_secret_in() {
    ! grep -q -F -e "$OLD_ADMIN_PW" -e "$NEW_ADMIN_PW" -e "$OLD_CLIENT_SECRET" -e "$NEW_CLIENT_SECRET" "$1"
}

# Admin password, client secret and redirect URL (new origin) rotated in .env:
# login with the previous password, which sets the new one, then exactly one
# update per rotated value (secret, redirectUris, webOrigins); every rotation
# is logged without its value; .env becomes the new .env.previous.
test_rotation_applies_each_value_once() {
    kc_rerun "$NEW_ADMIN_PW" "$NEW_CLIENT_SECRET" "https://cmdb-new.example.test/auth/callback"
    local out="$TMP_ROOT/rotation-out"
    create_initial_admin > "$out" 2>&1 || { cat "$out"; return 1; }
    [ "$(sort "$KCADM_WRITES")" = "$(printf 'clients redirectUris\nclients secret\nclients webOrigins\nreset-password admin-id')" ] || { cat "$KCADM_WRITES"; return 1; }
    [ "$(cat "$KC_STATE/admin-password")" = "$NEW_ADMIN_PW" ] &&
        [ "$(jq -r .secret "$KC_STATE/client.json")" = "$NEW_CLIENT_SECRET" ] &&
        jq -e '.redirectUris | index("https://cmdb-new.example.test/auth/callback")' "$KC_STATE/client.json" >/dev/null &&
        jq -e '.webOrigins | index("https://cmdb-new.example.test")' "$KC_STATE/client.json" >/dev/null &&
        ! jq -e '.redirectUris | index("http://192.0.2.10:3000/auth/callback")' "$KC_STATE/client.json" >/dev/null &&
        jq -e '.serviceAccountsEnabled == false' "$KC_STATE/client.json" >/dev/null || return 1
    # Login with the previous password, the reset, a login with the new one;
    # all before the client is read and the initial administrator looked up.
    local login1 reset login2 client admin
    login1="$(grep -n '^config credentials' "$KCADM_LOG" | sed -n 1p | cut -d: -f1)"
    reset="$(grep -n '^update users/admin-id/reset-password -r master -f -$' "$KCADM_LOG" | cut -d: -f1)"
    login2="$(grep -n '^config credentials' "$KCADM_LOG" | sed -n 2p | cut -d: -f1)"
    client="$(grep -n '^get clients -r reticora -q clientId=reticora-app' "$KCADM_LOG" | cut -d: -f1)"
    admin="$(grep -n '^get users -r reticora -q username=ops@acme.example' "$KCADM_LOG" | cut -d: -f1)"
    [ -n "$login1" ] && [ -n "$reset" ] && [ -n "$login2" ] && [ -n "$client" ] && [ -n "$admin" ] &&
        [ "$login1" -lt "$reset" ] && [ "$reset" -lt "$login2" ] && [ "$login2" -lt "$client" ] && [ "$client" -lt "$admin" ] || return 1
    [ "$(grep -c '^config credentials --server http://localhost:8080 --realm master --user admin --password \*\*\*$' "$KCADM_LOG")" -eq 2 ] &&
        [ "$(tr '\n' ' ' < "$KC_STATE/logins")" = "ok ok " ] || return 1
    # Detected and logged by name, never by value; no secret on a kcadm
    # command line except the login password.
    grep -q -F "Changed since the last run: KEYCLOAK_ADMIN_PASSWORD RETICORA_OIDC_CLIENT_SECRET RETICORA_OIDC_REDIRECT_URL — applying to Keycloak" "$out" &&
        [ "$(grep -c 'Keycloak rotation:' "$out")" -eq 4 ] &&
        grep -q -F "Keycloak rotation: password of the administrator admin set to KEYCLOAK_ADMIN_PASSWORD" "$out" &&
        grep -q -F "Keycloak rotation: client reticora-app secret set to RETICORA_OIDC_CLIENT_SECRET" "$out" &&
        grep -q -F "Keycloak rotation: client reticora-app redirectUris set to https://cmdb-new.example.test/auth/callback, https://cmdb-new.example.test/*" "$out" &&
        no_secret_in "$out" && no_secret_in "$KCADM_LOG" || return 1
    # .env.previous now holds the applied values (mode 0600).
    cmp -s "$ENV_FILE" "$ENV_FILE.previous" && [ "$(stat -c %a "$ENV_FILE.previous")" = "600" ]
}

# Nothing rotated: one login, no write, no rotation logged.
test_rotation_unchanged_writes_nothing() {
    kc_rerun "$OLD_ADMIN_PW" "$OLD_CLIENT_SECRET" "http://192.0.2.10:3000/auth/callback"
    local out="$TMP_ROOT/unchanged-out"
    create_initial_admin > "$out" 2>&1 || { cat "$out"; return 1; }
    [ ! -s "$KCADM_WRITES" ] || { cat "$KCADM_WRITES"; return 1; }
    [ "$(tr '\n' ' ' < "$KC_STATE/logins")" = "ok " ] &&
        [ -z "$(kc_log_writes)" ] &&
        ! grep -q -e 'Keycloak rotation:' -e 'Changed since the last run' "$out" &&
        grep -q 'client secret, redirect URIs and web origins match the configuration' "$out"
}

# Only one value rotated: exactly its update. A new callback path on the same
# origin changes the redirect URIs, not the web origins.
test_rotation_single_values() {
    kc_rerun "$OLD_ADMIN_PW" "$NEW_CLIENT_SECRET" "http://192.0.2.10:3000/auth/callback"
    create_initial_admin >/dev/null 2>&1 || return 1
    [ "$(cat "$KCADM_WRITES")" = "clients secret" ] || { cat "$KCADM_WRITES"; return 1; }
    kc_rerun "$OLD_ADMIN_PW" "$OLD_CLIENT_SECRET" "http://192.0.2.10:3000/oidc/callback"
    create_initial_admin >/dev/null 2>&1 || return 1
    [ "$(cat "$KCADM_WRITES")" = "clients redirectUris" ] || { cat "$KCADM_WRITES"; return 1; }
    kc_rerun "$NEW_ADMIN_PW" "$OLD_CLIENT_SECRET" "http://192.0.2.10:3000/auth/callback"
    create_initial_admin >/dev/null 2>&1 || return 1
    [ "$(cat "$KCADM_WRITES")" = "reset-password admin-id" ] || { cat "$KCADM_WRITES"; return 1; }
}

# The previous password no longer works (an interrupted run already set the
# new one): the login with the configured password succeeds, nothing is reset.
test_rotation_already_applied() {
    kc_rerun "$NEW_ADMIN_PW" "$OLD_CLIENT_SECRET" "http://192.0.2.10:3000/auth/callback"
    printf '%s\n' "$NEW_ADMIN_PW" > "$KC_STATE/admin-password"
    create_initial_admin >/dev/null 2>&1 || return 1
    [ ! -s "$KCADM_WRITES" ] && [ "$(tr '\n' ' ' < "$KC_STATE/logins")" = "refused ok " ]
}

# Neither password is accepted (e.g. changed in the Keycloak console): rc 1
# with the hint, no secret in the output, .env.previous unchanged.
test_rotation_login_refused() {
    kc_rerun "$NEW_ADMIN_PW" "$NEW_CLIENT_SECRET" "http://192.0.2.10:3000/auth/callback"
    printf 'changed-in-the-console\n' > "$KC_STATE/admin-password"
    cp "$ENV_FILE.previous" "$TMP_ROOT/previous-before"
    local out="$TMP_ROOT/refused-out" rc
    (create_initial_admin) > "$out" 2>&1; rc=$?
    [ "$rc" -eq 1 ] &&
        grep -q -F "Could not log in to the Keycloak admin API as admin with KEYCLOAK_ADMIN_PASSWORD or the previous password of $ENV_FILE.previous" "$out" &&
        grep -q "docs/troubleshooting-installation.md, 'Keycloak admin login or rotation failed'" "$out" &&
        no_secret_in "$out" && [ ! -s "$KCADM_WRITES" ] && [ "$(tr '\n' ' ' < "$KC_STATE/logins")" = "refused refused " ] &&
        cmp -s "$ENV_FILE.previous" "$TMP_ROOT/previous-before"
}

# A failing client update aborts with the hint and keeps .env.previous.
test_rotation_update_failure_aborts() {
    kc_rerun "$OLD_ADMIN_PW" "$NEW_CLIENT_SECRET" "http://192.0.2.10:3000/auth/callback"
    cp "$ENV_FILE.previous" "$TMP_ROOT/previous-before"
    KC_FAIL_UPDATE=1
    local out="$TMP_ROOT/update-fail-out" rc
    (create_initial_admin) > "$out" 2>&1; rc=$?
    [ "$rc" -eq 1 ] &&
        grep -q -F "Could not apply secret of the rendered realm to the Keycloak client reticora-app" "$out" &&
        grep -q "Keycloak admin login or rotation failed" "$out" && no_secret_in "$out" &&
        cmp -s "$ENV_FILE.previous" "$TMP_ROOT/previous-before"
}

# write_env keeps the existing .env as .env.previous (0600) before the merge;
# a .env.previous that is still there (values not applied yet) is kept; a
# first installation has none.
test_rotation_env_previous_backup() {
    ENV_FILE="$TMP_ROOT/previous.env"
    rm -f "$ENV_FILE" "$ENV_FILE.previous"
    set_env_values
    write_env
    [ ! -e "$ENV_FILE.previous" ] || return 1
    printf 'KEYCLOAK_ADMIN_PASSWORD=edited-by-hand\nRETICORA_HTTP_BIND=127.0.0.1\n' > "$ENV_FILE"
    chmod 644 "$ENV_FILE"
    cp "$ENV_FILE" "$TMP_ROOT/previous-expected"
    write_env
    cmp -s "$ENV_FILE.previous" "$TMP_ROOT/previous-expected" && [ "$(stat -c %a "$ENV_FILE.previous")" = "600" ] || return 1
    write_env
    cmp -s "$ENV_FILE.previous" "$TMP_ROOT/previous-expected" &&
        grep -qx 'KEYCLOAK_ADMIN_PASSWORD=new-KEYCLOAK_ADMIN_PASSWORD' "$ENV_FILE" &&
        grep -qx 'RETICORA_HTTP_BIND=127.0.0.1' "$ENV_FILE" &&
        ! grep -q 'kept from the previous' "$ENV_FILE.previous" &&
        git -C "$REPO_ROOT" check-ignore -q deploy/docker-compose/.env.previous
}

test_rotation_documented() {
    grep -q "Keycloak admin login or rotation failed" "$REPO_ROOT/docs/troubleshooting-installation.md" &&
        grep -q '\.env\.previous' "$REPO_ROOT/docs/troubleshooting-installation.md"
}

run_test    "rotation: rotated admin password, secret and redirect URL, one update each" test_rotation_applies_each_value_once
run_test    "rotation: unchanged values cause no write"                                  test_rotation_unchanged_writes_nothing
run_test    "rotation: a single rotated value causes exactly its update"                 test_rotation_single_values
run_test    "rotation: an already applied admin password is not reset again"             test_rotation_already_applied
run_test    "rotation: a refused admin login aborts with a hint"                         test_rotation_login_refused
run_test    "rotation: a failing client update aborts with a hint"                       test_rotation_update_failure_aborts
run_test    "rotation: .env is kept as .env.previous (0600) until applied"               test_rotation_env_previous_backup
run_test    "rotation: the troubleshooting guide covers the rotation"                    test_rotation_documented

# ─── realm settings on every run (WP-428, AUT-09, SEC-07, F-061) ─────────────
# `start --import-realm` skips the existing realm on an upgrade; the installer
# applies the settings of the realm file with apply_realm_settings (run by
# create_initial_admin). tests/keycloak-realm-upgrade.test.sh runs the same
# against Keycloak 24.

REALM_FILE_REL=".generated/realm-reticora.json"

# kc_legacy_rerun — a re-run of the installer on an installation whose realm
# the release before WP-044 imported: the client already has the configured
# values, the demo administrator of that release exists.
kc_legacy_rerun() {
    fake_kcadm
    KC_USERS="admin@reticora.local=legacy-id"
    kc_legacy_realm
}

# realm_writes_of <kind…> — the KCADM_WRITES lines of these kinds, sorted.
realm_writes_of() {
    local kinds
    kinds="$(printf '%s|' "$@")"
    grep -E "^(${kinds%|}) " "$KCADM_WRITES" | LC_ALL=C sort
}

# realm_like_file — the fake realm has every setting of the rendered realm
# file: the plan of the installer finds nothing to change, browser-mfa is
# bound.
realm_like_file() {
    jq '(.clients[]?.secret) |= "**********"' "$KC_STATE/realm.json" | kc_realm_plan "$COMPOSE_DIR/$REALM_FILE_REL" \
        | jq -e '.roles == [] and .composites == [] and .groups == [] and .group_roles == [] and .scopes == []
            and .flow.state == "same" and .flow.binding == "browser-mfa" and .realm == {} and .attributes == {}' >/dev/null
}

# file_count <jq filter> — counts over the rendered realm file.
file_count() { jq "$1" "$COMPOSE_DIR/$REALM_FILE_REL"; }

# Re-run against the legacy realm: every missing setting is applied with
# exactly one kcadm call (the flow with one call per flow, execution,
# requirement and authenticator config of the realm file), the user profile
# before the administrator is created, every change logged, no secret.
test_realm_upgrade_applies_once() {
    kc_legacy_rerun
    local out="$TMP_ROOT/realm-upgrade-out" profile admin subflows executions requirements configs
    create_initial_admin > "$out" 2>&1 || { cat "$out"; return 1; }
    subflows="$(file_count '[.authenticationFlows[].authenticationExecutions[] | select(.authenticatorFlow)] | length')"
    executions="$(file_count '[.authenticationFlows[].authenticationExecutions[] | select(.authenticatorFlow | not)] | length')"
    requirements="$(file_count '[.authenticationFlows[].authenticationExecutions[] | select(.requirement != "DISABLED")] | length')"
    configs="$(file_count '.authenticatorConfig | length')"
    [ "$(realm_writes_of 'update users/profile' 'create role' 'add-roles' 'create group' 'default-client-scope' 'update realm')" = \
        "$(printf '%s\n' 'add-roles group operators operator' 'add-roles group reticora-admin mfa_required' \
            'add-roles role operator mfa_required' 'add-roles role org_admin mfa_required' 'create group operators' \
            'create role mfa_required' 'create role operator' 'default-client-scope reticora-app acr' \
            'update realm attributes.acr.loa.map,browserFlow,bruteForceProtected,failureFactor,passwordPolicy' \
            'update users/profile organization_id')" ] || { cat "$KCADM_WRITES"; return 1; }
    # The flow: one call per flow, execution, requirement and config of the
    # realm file, none twice, nothing else.
    if ! { [ -z "$(sort "$KCADM_WRITES" | uniq -d)" ] &&
        [ "$(grep -c '^create flow ' "$KCADM_WRITES")" -eq 1 ] &&
        [ "$(grep -c '^create sub-flow ' "$KCADM_WRITES")" -eq "$subflows" ] &&
        [ "$(grep -c '^create execution ' "$KCADM_WRITES")" -eq "$executions" ] &&
        [ "$(grep -c '^requirement ' "$KCADM_WRITES")" -eq "$requirements" ] &&
        [ "$(grep -c '^create config ' "$KCADM_WRITES")" -eq "$configs" ] &&
        [ "$(wc -l < "$KCADM_WRITES")" -eq $((10 + 1 + subflows + executions + requirements + configs)) ]; }; then
        sort "$KCADM_WRITES"
        return 1
    fi
    realm_like_file || return 1
    # The whole realm representation went back: Keycloak's own attributes stay.
    jq -e '.attributes.cibaInterval == "5" and .passwordPolicy == "length(12) and upperCase(1) and lowerCase(1) and digits(1) and specialChars(1) and notUsername(undefined) and notEmail(undefined) and passwordHistory(5)"' \
        "$KC_STATE/realm.json" >/dev/null || return 1
    # organization_id is declared before the administrator is created with it.
    profile="$(grep -n '^update users/profile' "$KCADM_LOG" | cut -d: -f1)"
    admin="$(grep -n '^create users -r reticora' "$KCADM_LOG" | cut -d: -f1)"
    [ -n "$profile" ] && [ -n "$admin" ] && [ "$profile" -lt "$admin" ] || return 1
    grep -q -F 'Keycloak realm: user profile attribute organization_id declared (admin only)' "$out" &&
        grep -q -F 'Keycloak realm: role mfa_required created' "$out" &&
        grep -q -F 'Keycloak realm: role org_admin contains mfa_required' "$out" &&
        grep -q -F 'Keycloak realm: group operators carries the realm role operator' "$out" &&
        grep -q -F 'Keycloak realm: client reticora-app has the default client scope acr' "$out" &&
        grep -q -F 'Keycloak realm: flow browser-mfa created (OTP for mfa_required, acr levels 1 and 2)' "$out" &&
        grep -q -F 'Keycloak realm: password policy length(12) added' "$out" &&
        grep -q -F 'Keycloak realm: brute-force detection enabled' "$out" &&
        grep -q -F 'Keycloak realm: failureFactor 30 lowered to 5' "$out" &&
        grep -q -F 'Keycloak realm: attribute acr.loa.map set to {"1":1,"2":2}' "$out" &&
        grep -q -F 'Keycloak realm: browser flow browser replaced by browser-mfa' "$out" &&
        grep -q -F 'Initial administrator ops@acme.example created' "$out" &&
        ! grep -q 'client-secret-0' "$out" && ! grep -q 'client-secret-0' "$KCADM_LOG"
}

# A second re-run changes nothing: two reads, no write, reported unchanged.
test_realm_second_rerun_unchanged() {
    kc_legacy_rerun
    create_initial_admin >/dev/null 2>&1 || return 1
    # The users after the first run: the demo administrator removed, the
    # initial administrator created.
    KC_USERS="ops@acme.example=new-id"
    : > "$KCADM_WRITES"
    : > "$KCADM_LOG"
    local out="$TMP_ROOT/realm-second-out"
    create_initial_admin > "$out" 2>&1 || { cat "$out"; return 1; }
    if [ -s "$KCADM_WRITES" ] || [ -n "$(kc_log_writes)" ]; then cat "$KCADM_WRITES"; return 1; fi
    [ "$(grep -cE '^(create partial-export|get users/profile) ' "$KCADM_LOG")" -eq 2 ] &&
        grep -q -F 'Keycloak realm settings match the bundled realm (policies, MFA flow, roles, groups, acr scope)' "$out" &&
        ! grep -q -F 'Keycloak realm: kept' "$out"
}

# Stricter values of the operator stay (longer passwords, longer history, an
# extra policy, fewer failures until the lockout), weaker ones are raised;
# both are logged.
test_realm_stricter_values_kept() {
    kc_legacy_rerun
    kc_realm_edit '. + {passwordPolicy: "length(16) and passwordHistory(10) and forceExpiredPasswordChange(365) and digits(0)",
        failureFactor: 3, bruteForceProtected: true, maxFailureWaitSeconds: 300}'
    local out="$TMP_ROOT/realm-strict-out"
    create_initial_admin > "$out" 2>&1 || { cat "$out"; return 1; }
    jq -e '.passwordPolicy == "length(16) and passwordHistory(10) and forceExpiredPasswordChange(365) and digits(1) and upperCase(1) and lowerCase(1) and specialChars(1) and notUsername(undefined) and notEmail(undefined)"
        and .failureFactor == 3 and .bruteForceProtected == true and .maxFailureWaitSeconds == 900' "$KC_STATE/realm.json" >/dev/null \
        || { jq -c '{passwordPolicy, failureFactor, maxFailureWaitSeconds}' "$KC_STATE/realm.json"; return 1; }
    [ "$(realm_writes_of 'update realm')" = 'update realm attributes.acr.loa.map,browserFlow,maxFailureWaitSeconds,passwordPolicy' ] || { cat "$KCADM_WRITES"; return 1; }
    grep -q -F 'Keycloak realm: kept password policy length(16) (stricter than length(12))' "$out" &&
        grep -q -F 'Keycloak realm: kept password policy passwordHistory(10) (stricter than passwordHistory(5))' "$out" &&
        grep -q -F 'Keycloak realm: kept failureFactor 3 (stricter than 5)' "$out" &&
        grep -q -F 'Keycloak realm: password policy digits(0) raised to digits(1)' "$out" &&
        grep -q -F 'Keycloak realm: maxFailureWaitSeconds 300 raised to 900' "$out" &&
        ! grep -q -F 'Keycloak realm: brute-force detection enabled' "$out"
}

# A realm without the client scope acr gets it (as Keycloak creates it) and
# the client gets it as default scope.
test_realm_acr_scope_created() {
    kc_legacy_rerun
    kc_realm_edit '.clientScopes |= map(select(.name != "acr"))'
    create_initial_admin >/dev/null 2>&1 || return 1
    [ "$(realm_writes_of 'create client-scope' 'default-client-scope')" = "$(printf '%s\n' 'create client-scope acr' 'default-client-scope reticora-app acr')" ] &&
        jq -e '.clientScopes[] | select(.name == "acr") | .protocolMappers[0].protocolMapper == "oidc-acr-mapper"' "$KC_STATE/realm.json" >/dev/null &&
        realm_like_file
}

# A bound browser-mfa that differs from the realm file (e.g. an added
# WebAuthn step) is the operator's: kept with a warning, nothing written.
test_realm_custom_bound_flow_kept() {
    fake_kcadm
    KC_USERS="ops@acme.example=existing-id"
    kc_realm_edit '(.authenticationFlows[] | select(.alias == "browser-mfa forms") | .authenticationExecutions)
        += [{id: "exec-webauthn", authenticator: "webauthn-authenticator", authenticatorFlow: false, requirement: "REQUIRED", priority: 40}]'
    local out="$TMP_ROOT/realm-custom-flow-out"
    create_initial_admin > "$out" 2>&1 || { cat "$out"; return 1; }
    [ ! -s "$KCADM_WRITES" ] &&
        grep -q -F 'Keycloak realm: the bound browser flow browser-mfa differs from the bundled realm (customized or from an earlier release) and is kept' "$out" &&
        jq -e '[.authenticationFlows[].authenticationExecutions[] | select(.id == "exec-webauthn")] | length == 1' "$KC_STATE/realm.json" >/dev/null &&
        ! grep -q -F 'match the bundled realm' "$out"
}

# An unbound browser-mfa that differs (e.g. an interrupted earlier run) is
# rebuilt from the realm file, with its authenticator configs, and bound.
test_realm_unbound_flow_rebuilt() {
    fake_kcadm
    KC_USERS="ops@acme.example=existing-id"
    kc_realm_edit '.browserFlow = "browser"
        | (.authenticationFlows[] | select(.alias == "browser-mfa level 2") | .authenticationExecutions) |= map(select(.authenticator != "allow-access-authenticator"))'
    local out="$TMP_ROOT/realm-rebuild-out"
    create_initial_admin > "$out" 2>&1 || { cat "$out"; return 1; }
    [ "$(realm_writes_of 'delete flow' 'delete config' 'create flow' 'update realm')" = \
        "$(printf '%s\n' 'create flow browser-mfa' 'delete config loa-1' 'delete config loa-2' 'delete config mfa-required-role' \
            'delete config no-mfa-required-role' 'delete flow browser-mfa' 'update realm browserFlow')" ] || { cat "$KCADM_WRITES"; return 1; }
    jq -e '(.authenticatorConfig | length) == 4' "$KC_STATE/realm.json" >/dev/null &&
        grep -q -F 'Keycloak realm: unbound flow browser-mfa that differed from the bundled realm removed' "$out" &&
        grep -q -F 'Keycloak realm: browser flow browser replaced by browser-mfa' "$out" && realm_like_file
}

# A browser flow of the operator other than Keycloak's built-in "browser"
# stays bound (warning); browser-mfa exists for it to switch to.
test_realm_operator_browser_flow_kept() {
    fake_kcadm
    KC_USERS="ops@acme.example=existing-id"
    kc_realm_edit '.browserFlow = "corporate-sso" | .authenticationFlows += [{id: "flow-corporate-sso", alias: "corporate-sso",
        providerId: "basic-flow", topLevel: true, builtIn: false, authenticationExecutions: []}]'
    local out="$TMP_ROOT/realm-operator-flow-out"
    create_initial_admin > "$out" 2>&1 || { cat "$out"; return 1; }
    [ ! -s "$KCADM_WRITES" ] && jq -e '.browserFlow == "corporate-sso"' "$KC_STATE/realm.json" >/dev/null &&
        grep -q -F 'Keycloak realm: the browser flow corporate-sso is bound instead of browser-mfa and kept' "$out"
}

# A failing realm update aborts the run with the hint of the README.
test_realm_update_failure_aborts() {
    kc_legacy_rerun
    KC_FAIL_REALM=1
    local out="$TMP_ROOT/realm-fail-out" rc
    (create_initial_admin) > "$out" 2>&1; rc=$?
    [ "$rc" -eq 1 ] &&
        grep -q -F "Could not update the password policy, lockout and browser flow of the realm (see deploy/keycloak/README.md, 'Upgrade')" "$out" &&
        ! grep -q 'Initial administrator ops@acme.example created' "$out"
}

test_realm_upgrade_documented() {
    grep -q '^## Upgrade' "$REPO_ROOT/deploy/keycloak/README.md" &&
        grep -q 'apply_realm_settings' "$REPO_ROOT/deploy/keycloak/README.md" &&
        grep -q 'Import skipped' "$REPO_ROOT/deploy/keycloak/README.md"
}

run_test    "realm upgrade: a re-run on the legacy realm applies each missing setting once" test_realm_upgrade_applies_once
run_test    "realm upgrade: a second re-run changes nothing"                                test_realm_second_rerun_unchanged
run_test    "realm upgrade: stricter operator values stay, weaker ones are raised"         test_realm_stricter_values_kept
run_test    "realm upgrade: a missing client scope acr is created"                         test_realm_acr_scope_created
run_test    "realm upgrade: a customized bound browser-mfa is kept with a warning"         test_realm_custom_bound_flow_kept
run_test    "realm upgrade: an unbound browser-mfa that differs is rebuilt and bound"      test_realm_unbound_flow_rebuilt
run_test    "realm upgrade: an operator's browser flow stays bound with a warning"         test_realm_operator_browser_flow_kept
run_test    "realm upgrade: a failing realm update aborts with a hint"                     test_realm_update_failure_aborts
run_test    "realm upgrade: deploy/keycloak/README.md describes the upgrade"               test_realm_upgrade_documented

# ─── summary ──────────────────────────────────────────────────────────────────
echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
