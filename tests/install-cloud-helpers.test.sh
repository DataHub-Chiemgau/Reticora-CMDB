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

# ─── URL / domain validators and error hints ──────────────────────────────────

test_valid_url_https_host() { valid_url "https://dev.reticora.cloud"; }
test_valid_url_http_localhost_port() { valid_url "http://localhost:3000"; }
test_valid_url_rejects_bare_host() { valid_url "dev.reticora.cloud"; }

test_explain_url_error_suggests_scheme() {
    local suggestion=""
    explain_url_error "dev.reticora.cloud" suggestion 2>/dev/null
    [ "$suggestion" = "https://dev.reticora.cloud" ] && valid_url "$suggestion"
}

test_valid_domain_simple() { valid_domain "dev.reticora.cloud"; }
test_valid_domain_rejects_url() { valid_domain "https://dev.reticora.cloud"; }
test_valid_domain_rejects_path() { valid_domain "dev.reticora.cloud/auth"; }
test_valid_domain_rejects_port() { valid_domain "dev.reticora.cloud:443"; }
test_valid_domain_rejects_single_label() { valid_domain "localhost"; }

test_valid_optional_domain_accepts_empty() { valid_optional_domain ""; }
test_valid_optional_domain_rejects_url() { valid_optional_domain "https://dev.reticora.cloud"; }

test_explain_domain_error_strips_scheme() {
    local suggestion=""
    explain_domain_error "https://dev.reticora.cloud" suggestion 2>/dev/null
    [ "$suggestion" = "dev.reticora.cloud" ] && valid_domain "$suggestion"
}

test_explain_domain_error_strips_port_and_path() {
    local suggestion=""
    explain_domain_error "dev.reticora.cloud:8443/auth" suggestion 2>/dev/null
    [ "$suggestion" = "dev.reticora.cloud" ] && valid_domain "$suggestion"
}

test_explain_domain_error_leaves_empty_alone() {
    # An empty answer is valid (TLS disabled) and must never be "corrected".
    local suggestion=""
    explain_domain_error "" suggestion 2>/dev/null
    [ -z "$suggestion" ]
}

test_explain_domain_error_no_fixable_suggestion() {
    # Nothing salvageable: the hint must not echo the rejected value back,
    # otherwise ask_validated would offer it as the new default and loop.
    local suggestion=""
    explain_domain_error "not a domain" suggestion 2>/dev/null
    [ -z "$suggestion" ]
}

run_test   "valid_url accepts an https URL"                            test_valid_url_https_host
run_test   "valid_url accepts http://localhost with port"              test_valid_url_http_localhost_port
expect_fail "valid_url rejects a bare hostname"                        test_valid_url_rejects_bare_host
run_test   "explain_url_error suggests adding the https:// scheme"     test_explain_url_error_suggests_scheme
run_test   "valid_domain accepts a plain domain"                       test_valid_domain_simple
expect_fail "valid_domain rejects a URL with scheme"                   test_valid_domain_rejects_url
expect_fail "valid_domain rejects a domain with a path"                test_valid_domain_rejects_path
expect_fail "valid_domain rejects a domain with a port"                test_valid_domain_rejects_port
expect_fail "valid_domain rejects a single-label host"                 test_valid_domain_rejects_single_label
run_test   "valid_optional_domain accepts empty input"                 test_valid_optional_domain_accepts_empty
expect_fail "valid_optional_domain rejects a URL"                      test_valid_optional_domain_rejects_url
run_test   "explain_domain_error strips a scheme"                      test_explain_domain_error_strips_scheme
run_test   "explain_domain_error strips port and path"                 test_explain_domain_error_strips_port_and_path
run_test   "explain_domain_error leaves empty input alone"             test_explain_domain_error_leaves_empty_alone
run_test   "explain_domain_error does not recycle unfixable input"     test_explain_domain_error_no_fixable_suggestion

# ─── summary ──────────────────────────────────────────────────────────────────
echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
