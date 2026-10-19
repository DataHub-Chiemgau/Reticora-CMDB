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

# The ACME request runs certbot itself, not the renewal-loop entrypoint.
test_tls_acme_uses_certbot_entrypoint() {
    RETICORA_TLS_DOMAIN="acme.example.com"
    RETICORA_CERT_EMAIL="ops@example.com"
    local live="$TMP_ROOT/tls-acme/live/$RETICORA_TLS_DOMAIN" calls="$TMP_ROOT/compose-calls"
    make_ca_chain "$TMP_ROOT/tls-acme-issued" "$RETICORA_TLS_DOMAIN"
    make_cert "$live" "$RETICORA_TLS_DOMAIN"
    tls_live_dir() { printf '%s' "$live"; }
    compose_cmd() {
        printf '%s\n' "$*" >> "$calls"
        # certbot "issues" the real chain.
        if [ "$1" = "run" ]; then cp "$TMP_ROOT/tls-acme-issued/fullchain.pem" "$TMP_ROOT/tls-acme-issued/privkey.pem" "$live/"; fi
        return 0
    }
    RETICORA_TLS_CA_BUNDLE="$TMP_ROOT/tls-acme-issued/ca.pem"
    issue_tls_certificate
    grep -q -- "run --rm --no-deps --entrypoint certbot certbot certonly --webroot -w /var/www/certbot -d acme.example.com" "$calls" \
        && grep -q -- "--email ops@example.com" "$calls" && grep -q -- "exec -T frontend nginx -s reload" "$calls"
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
run_test   "tls: ACME runs certbot with the webroot and reloads nginx"     test_tls_acme_uses_certbot_entrypoint
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
    run_migrations() { printf 'run_migrations\n' >> "$calls"; }
    seed_default_organization() { printf 'seed\n' >> "$calls"; }
    ensure_runtime_login() { printf 'ensure_runtime_login\n' >> "$calls"; }
    validate_keycloak_bootstrap() { :; }
    create_initial_admin() { :; }
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

# Kubernetes: base and staging (which replaces the env list) take the
# server's DSN from the runtime-login secret, never from the migration one.
test_runtime_login_k8s() {
    local f ref
    for f in deploy/k8s/base/deployment.yaml deploy/k8s/overlays/staging/kustomization.yaml; do
        ref="$(grep -A5 -- '- name: RETICORA_DATABASE_URL' "$REPO_ROOT/$f")"
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

# ─── realm files and initial administrator (WP-044, AUT-09) ───────────────────

realm_py() {
    # realm_py <python expression over r (prod) and d (dev)>
    python3 -c "import json,sys; r=json.load(open('deploy/keycloak/realm-reticora.json')); d=json.load(open('deploy/keycloak/realm-reticora-dev.json')); sys.exit(0 if ($1) else 1)"
}

test_prod_realm_has_no_users() {
    realm_py "r.get('users') == []" || return 1
}

test_prod_path_has_no_known_password() {
    if grep -rqE 'admin123|Admin-Dev-2024' deploy/keycloak/realm-reticora.json install-cloud.sh install.sh deploy/docker-compose; then return 1; fi
}

test_prod_realm_policies() {
    realm_py "r['bruteForceProtected'] and r['failureFactor'] <= 5 and 'length(12)' in r['passwordPolicy'] and r['browserFlow'] == 'browser-mfa'" || return 1
    # org_admin and operator require the second factor.
    realm_py "all('mfa_required' in x.get('composites', {}).get('realm', []) for x in r['roles']['realm'] if x['name'] in ('org_admin', 'operator'))" || return 1
    realm_py "r['authenticatorConfig'][0]['config']['condUserRole'] == 'mfa_required'" || return 1
    # organization_id is a declared, admin-only user profile attribute.
    realm_py "any(a['name'] == 'organization_id' and a['permissions']['edit'] == ['admin'] for a in json.loads(r['components']['org.keycloak.userprofile.UserProfileProvider'][0]['config']['kc.user.profile.config'][0])['attributes'])" || return 1
}

test_dev_realm_differs_only_by_demo_user() {
    realm_py "{k: v for k, v in d.items() if k not in ('users', 'displayName')} == {k: v for k, v in r.items() if k not in ('users', 'displayName')}" || return 1
    realm_py "[u['username'] for u in d['users']] == ['admin@reticora.local'] and 'org_admin' not in d['users'][0]['realmRoles']" || return 1
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

# A fake Keycloak admin CLI: records the calls and knows the users listed in
# KC_USERS (username=id).
fake_kcadm() {
    KCADM_LOG="$TMP_ROOT/kcadm.log"
    : > "$KCADM_LOG"
    compose_cmd() {
        shift 4 # exec -T keycloak /opt/keycloak/bin/kcadm.sh
        printf '%s\n' "$*" >> "$KCADM_LOG"
        local a
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
}

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
    if grep -qE '^(create|set-password|delete)' "$KCADM_LOG"; then return 1; fi
    initial_login_line | grep -q 'password not shown again' || return 1
}

test_initial_admin_rejects_demo_account() {
    valid_initial_admin_email "admin@reticora.local" || return 1
}

run_test    "production realm contains no user"                          test_prod_realm_has_no_users
run_test    "no known password in the production path"                   test_prod_path_has_no_known_password
run_test    "realm sets password, lockout and MFA policy"                 test_prod_realm_policies
run_test    "dev realm differs from production only by the demo user"     test_dev_realm_differs_only_by_demo_user
run_test    "dev compose imports the dev realm, production never"         test_dev_compose_mounts_dev_realm_only
run_test    "initial password is random and meets the policy"             test_initial_password_random
run_test    "initial administrator gets a temporary password once"        test_initial_admin_created_with_temporary_password
run_test    "an existing initial administrator is not recreated"          test_initial_admin_not_recreated
expect_fail "the demo account is rejected as initial administrator"       test_initial_admin_rejects_demo_account

# ─── summary ──────────────────────────────────────────────────────────────────
echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
