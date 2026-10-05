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
