#!/usr/bin/env bash
#
# install-cloud-issuer-order.test.sh — Phase-1 acceptance audit, group T5,
# DEFECT test (fails until the installer is fixed; not wired into CI).
#
# WP-095 (INS-02): "Installer: Issuer-Prüfung extern und containerseitig".
#
# Finding encoded here:
#   P1 "OIDC issuer check runs before the frontend (issuer proxy) is started,
#       so domain/TLS installations abort": check_issuer_from_host and
#       check_issuer_from_container are called from validate_keycloak_bootstrap,
#       which start_stack runs before `compose up -d server frontend`. With a
#       public domain the issuer https://<domain>/realms/reticora (the default
#       of install-cloud.sh) is served by the frontend nginx (/realms/ proxy to
#       Keycloak), so it cannot answer yet: start_stack dies with "The OIDC
#       issuer … is not reachable from this host", on every re-run as well.
# Test gap closed: critical "No test of the issuer-check order relative to the
#   frontend start".
#
# Model (as in scratch probe verify-B12/issuer/order.sh): compose_cmd records
# every call and starts the "frontend" on `up -d server frontend`; the stubbed
# curl answers https://<domain>/* only once the frontend runs, the Keycloak
# host port http://localhost:8180/* always; the backend container's
# `server -check-oidc-issuer` reaches a domain issuer only through the
# frontend as well. All other side effects are stubbed.
#
# Cases:
#   - domain issuer: start_stack completes (rc 0) and both issuer checks run
#     after the frontend start                                    (fails today)
#   - domain issuer never reachable: rc 1 with the troubleshooting hint
#                                                                 (control)
#   - IP issuer http://<ip>:8180/realms/reticora: still checked from host and
#     container, rc 0; unreachable IP issuer: rc 1                (control)
#
# Retest:
#   ./tests/audit/install-cloud-issuer-order.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.."

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

HARNESS="$TMP_ROOT/harness.sh"
head -n -1 install-cloud.sh > "$HARNESS"
cp install-preflight.sh "$TMP_ROOT/"

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

load_installer() {
    set --
    # shellcheck disable=SC1090
    . "$HARNESS"
    NON_INTERACTIVE=1
    COMPOSE=(docker compose)
}

run_test() {
    local name="$1"; shift
    if ( set -e; load_installer; "$@" ) >"$TMP_ROOT/out" 2>&1; then
        ok "$name"
    else
        fail "$name"
        sed 's/\x1b\[[0-9;]*m//g; s/^/     /' "$TMP_ROOT/out" | tail -n 14
    fi
}

# stub_stack <case> <issuer> <reachable: always|after-frontend|never>
stub_stack() {
    CASE_DIR="$TMP_ROOT/$1"
    mkdir -p "$CASE_DIR"
    CALLS="$CASE_DIR/calls"
    : > "$CALLS"
    ISSUER_MODE="$3"
    RETICORA_OIDC_ISSUER_URL="$2"
    RETICORA_ISSUER_RETRY_DELAY=0
    RETICORA_KEYCLOAK_PORT=8180
    FRONTEND_MARK="$CASE_DIR/frontend-up"
    rm -f "$FRONTEND_MARK"

    issuer_served() {
        case "$ISSUER_MODE" in
            always) return 0 ;;
            after-frontend) [ -e "$FRONTEND_MARK" ] ;;
            *) return 1 ;;
        esac
    }
    compose_cmd() {
        printf 'compose %s\n' "$*" >> "$CALLS"
        case "$*" in
            *"up -d server frontend"*) touch "$FRONTEND_MARK" ;;
            *"--entrypoint server server -check-oidc-issuer"*)
                if issuer_served; then
                    echo "check-oidc-issuer ok" >> "$CALLS"
                    return 0
                fi
                echo "check-oidc-issuer: issuer $RETICORA_OIDC_ISSUER_URL unreachable" >&2
                return 1
                ;;
        esac
        return 0
    }
    curl() {
        local url="${*: -1}" issuer="${RETICORA_OIDC_ISSUER_URL%/}"
        case "$url" in
            "http://localhost:${RETICORA_KEYCLOAK_PORT}/"*)
                # Keycloak's host port answers as soon as Keycloak runs.
                printf '{"issuer":"%s"}' "$issuer"
                return 0
                ;;
            "$issuer"/*)
                if issuer_served; then
                    echo "host-issuer-check ok $url" >> "$CALLS"
                    printf '{"issuer":"%s"}' "$issuer"
                    return 0
                fi
                echo "curl: (7) Failed to connect" >&2
                return 7
                ;;
        esac
        return 7
    }
    wait_for_service() { return 0; }
    sync_db_password() { :; }
    run_migrations() { :; }
    seed_default_organization() { :; }
    create_initial_admin() { echo "create_initial_admin" >> "$CALLS"; }
    show_service_logs() { :; }
    tls_cert_is_selfsigned() { return 1; }
}

line_of() { grep -n -F -- "$1" "$CALLS" | head -n 1 | cut -d: -f1; }

# P1: a public-domain installation must complete; the issuer is checked
# (from the host and from the backend container) once the frontend that
# serves it runs.
test_domain_issuer_checked_after_frontend_start() {
    RETICORA_PUBLIC_BASE_URL=https://cmdb.example.test
    RETICORA_TLS_DOMAIN=cmdb.example.test
    stub_stack domain https://cmdb.example.test/realms/reticora after-frontend
    if ! ( start_stack ); then
        echo "start_stack aborted; compose calls so far:"
        sed 's/^/  /' "$CALLS"
        return 1
    fi
    local up host cont
    up="$(line_of 'compose up -d server frontend')"
    host="$(line_of 'host-issuer-check ok')"
    cont="$(line_of 'check-oidc-issuer ok')"
    [ -n "$up" ] || { echo "frontend never started"; return 1; }
    [ -n "$host" ] && [ "$host" -gt "$up" ] || { echo "host issuer check missing or before the frontend start (up=$up host=${host:-none})"; return 1; }
    [ -n "$cont" ] && [ "$cont" -gt "$up" ] || { echo "container issuer check missing or before the frontend start (up=$up container=${cont:-none})"; return 1; }
}

# Control: an issuer that never answers still aborts the installation with
# the troubleshooting hint.
test_unreachable_domain_issuer_aborts() {
    RETICORA_PUBLIC_BASE_URL=https://cmdb.example.test
    RETICORA_TLS_DOMAIN=cmdb.example.test
    stub_stack unreachable https://cmdb.example.test/realms/reticora never
    local out rc
    out="$( ( start_stack ) 2>&1 )"; rc=$?
    [ "$rc" -ne 0 ] || { echo "start_stack succeeded with an unreachable issuer"; return 1; }
    printf '%s' "$out" | grep -q "OIDC issuer check failed" || { echo "troubleshooting hint missing"; return 1; }
}

# Control: an IP issuer on the Keycloak port is still checked from host and
# container, and an unreachable one aborts.
test_ip_issuer_still_checked() {
    RETICORA_PUBLIC_BASE_URL=http://10.20.30.40
    RETICORA_TLS_DOMAIN=""
    stub_stack ip http://10.20.30.40:8180/realms/reticora always
    ( start_stack ) || { echo "start_stack failed for a reachable IP issuer"; return 1; }
    [ -n "$(line_of 'host-issuer-check ok')" ] || { echo "host issuer check not executed"; return 1; }
    [ -n "$(line_of 'check-oidc-issuer ok')" ] || { echo "container issuer check not executed"; return 1; }
    stub_stack ip-down http://10.20.30.40:8180/realms/reticora never
    if ( start_stack ) >/dev/null 2>&1; then
        echo "start_stack succeeded with an unreachable IP issuer"
        return 1
    fi
}

run_test "issuer: domain issuer is checked after the frontend start (start_stack rc 0)" test_domain_issuer_checked_after_frontend_start
run_test "issuer: an unreachable domain issuer aborts with the hint (control)"           test_unreachable_domain_issuer_aborts
run_test "issuer: an IP issuer is still checked; unreachable aborts (control)"           test_ip_issuer_still_checked

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
