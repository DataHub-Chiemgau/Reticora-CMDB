#!/usr/bin/env bash
#
# keycloak-realm-upgrade.test.sh — Keycloak 24 integration test: an upgrade
# brings the realm of an earlier release to the settings of the current realm
# export (WP-428, AUT-09, SEC-07, F-061). Needs Docker (the keycloak and
# keycloak-db images of deploy/docker-compose/docker-compose.yml) and jq.
#
# Runs keycloak-db and keycloak of deploy/docker-compose/docker-compose.yml in
# a throwaway compose project (Keycloak on 127.0.0.1) and the Keycloak steps
# of install-cloud.sh like tests/install-reinstall.test.sh:
#   1. Keycloak imports the realm of the release before WP-044
#      (tests/keycloak/realm-reticora-legacy.json: no password, lockout or MFA
#      policy, group reticora-admin without roles, demo administrator
#      admin@reticora.local);
#   2. upgrade without the installer: Keycloak is recreated with
#      `start --import-realm` and the current realm export. Confirms F-061:
#      Keycloak skips the existing realm, password policy, browser flow and
#      groups stay those of step 1 (the observed state is printed);
#   3. upgrade with the installer (configure_realm, keycloak up,
#      validate_keycloak_bootstrap, create_initial_admin with
#      apply_realm_settings): password policy, lockout, browser flow
#      browser-mfa, roles, groups with their realm roles, the default scope acr
#      and the declared organization_id are set; the initial administrator has
#      a temporary password (UPDATE_PASSWORD, CONFIGURE_TOTP), org_admin,
#      reticora-admin and its organization; admin@reticora.local is removed,
#      there is no admin@demo.local;
#   4. re-run without a change: no kcadm write;
#   5. stricter values of the operator (password length 16, failureFactor 3)
#      survive a re-run, a weakened one (brute-force detection off) is
#      raised again;
#   6. a fresh installation of the current realm (new keycloak-db) has the
#      same settings as the upgraded realm of step 4, and the installer
#      changes nothing in it.
#
# Usage: ./tests/keycloak-realm-upgrade.test.sh
#   RETICORA_UPGRADE_KC_PORT  host port of the test Keycloak (default: a free
#                             random high port)
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
REPO_ROOT="$(pwd)"

if ! docker info >/dev/null 2>&1 || ! command -v jq >/dev/null; then
    echo "FAIL: Docker and jq are required"
    exit 1
fi

TMP_ROOT="$(mktemp -d)"
PROJECT="reticora-kcupgrade-$$"
KC_PORT="${RETICORA_UPGRADE_KC_PORT:-$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')}"
T_COMPOSE_DIR="$TMP_ROOT/compose"
T_ENV="$T_COMPOSE_DIR/.env"
KCADM_CALLS="$TMP_ROOT/kcadm-calls"
ADMIN_EMAIL="admin@reticora.example"
DEFAULT_ORG="00000000-0000-0000-0000-000000000001"
mkdir -p "$T_COMPOSE_DIR"
# A copy, so that the realm file lands in the throwaway .generated/.
cp deploy/docker-compose/docker-compose.yml "$T_COMPOSE_DIR/"
# A source tree whose realm template is the legacy realm (step 1).
LEGACY_TREE="$TMP_ROOT/legacy"
mkdir -p "$LEGACY_TREE/deploy/keycloak"
cp tests/keycloak/realm-reticora-legacy.json "$LEGACY_TREE/deploy/keycloak/realm-reticora.json"

compose() { docker compose -p "$PROJECT" --env-file "$T_ENV" -f "$T_COMPOSE_DIR/docker-compose.yml" "$@"; }

cleanup() {
    [ ! -f "$T_ENV" ] || compose down -v --remove-orphans >/dev/null 2>&1
    rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

HARNESS="$TMP_ROOT/harness.sh"
head -n -1 install-cloud.sh > "$HARNESS"
cp install-preflight.sh "$TMP_ROOT/"

PASS=0
FAIL=0
ok()    { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail()  { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }
check() { local name="$1"; shift; if "$@"; then ok "$name"; else fail "$name"; fi; }

# show_log <log> — the end of an installer log without the line that shows
# the temporary password of the initial administrator.
show_log() { grep -v 'Initial login:' "$1" | tail -n 40; }

# installer_run <mode> <log> — the Keycloak steps of the installer in a fresh
# shell with install-cloud.sh loaded (main stripped):
#   legacy  — renders the legacy realm and starts keycloak-db and keycloak;
#   import  — renders the current realm and recreates keycloak (an upgrade
#             without the installer's Keycloak steps);
#   install — the Keycloak steps of start_stack up to create_initial_admin.
# Every kcadm call is recorded in KCADM_CALLS by its first two arguments
# (never a secret). The test Keycloak stays on 127.0.0.1.
# shellcheck disable=SC2034,SC2317 # settings and the wrappers used by the installer
installer_run() {
    local mode="$1"
    (
        set --
        # shellcheck disable=SC1090
        . "$HARNESS"
        NON_INTERACTIVE=1
        SCRIPT_DIR="$REPO_ROOT"
        COMPOSE_DIR="$T_COMPOSE_DIR"
        COMPOSE_FILE="$COMPOSE_DIR/docker-compose.yml"
        OVERRIDE_FILE="$COMPOSE_DIR/docker-compose.override.yml"
        ENV_FILE="$T_ENV"
        COMPOSE=(docker compose -p "$PROJECT")
        select_direct_ports() { RETICORA_PUBLISH_DIRECT_PORTS=false; RETICORA_DIRECT_PORTS_BIND=127.0.0.1; }
        eval "installer_$(declare -f kcadm)"
        kcadm() { printf '%s %s\n' "$1" "$2" >> "$KCADM_CALLS"; installer_kcadm "$@"; }
        collect_config
        write_env
        case "$mode" in
            legacy)
                SCRIPT_DIR="$LEGACY_TREE" configure_realm
                compose_cmd up -d keycloak-db
                wait_for_service keycloak-db || die "keycloak-db did not become healthy."
                compose_cmd up -d keycloak ;;
            import)
                configure_realm
                compose_cmd up -d --force-recreate keycloak ;;
            install)
                configure_realm
                compose_cmd up -d keycloak-db
                wait_for_service keycloak-db || die "keycloak-db did not become healthy."
                compose_cmd up -d keycloak ;;
        esac
        wait_for_service keycloak || { show_service_logs keycloak; die "Keycloak did not become healthy."; }
        validate_keycloak_bootstrap
        if [ "$mode" = install ]; then
            create_initial_admin
            initial_login_line
            echo
        fi
    ) > "$2" 2>&1
}

# Admin CLI of the test itself (own config file inside the container).
kc() { compose exec -T keycloak /opt/keycloak/bin/kcadm.sh "$@" --config /tmp/kcupgrade-test.config; }
kc_login() {
    kc config credentials --server http://localhost:8080 --realm master --user admin \
        --password "$(sed -n 's/^KEYCLOAK_ADMIN_PASSWORD=//p' "$T_ENV" | tail -n 1)" >/dev/null 2>&1
}
# export_realm <file> — the realm as the installer reads it (partial export).
export_realm() {
    kc create partial-export -r reticora -q exportGroupsAndRoles=true -q exportClients=true -o > "$1" 2>/dev/null
}
kc_writes() { grep -E '^(create|update|delete|set-password|add-roles|remove-roles) ' "$KCADM_CALLS" | grep -vc '^create partial-export$'; }

# The settings of the realm the upgrade must bring (AUT-09, SEC-07): what a
# fresh import of the current realm file has. Flows compare by what Keycloak
# runs: requirement, authenticator or sub-flow, authenticator config.
# shellcheck disable=SC2016 # a jq program
SETTINGS='
    def tree($r; $alias):
        ([$r.authenticationFlows[] | select(.alias == $alias) | .authenticationExecutions] | first // [])
        | sort_by(.priority)
        | map(. as $e | {requirement: $e.requirement,
            step: (if $e.authenticatorFlow then {flow: tree($r; $e.flowAlias)} else $e.authenticator end),
            config: (if $e.authenticatorConfig then
                [$r.authenticatorConfig[] | select(.alias == $e.authenticatorConfig) | .config] | first else null end)});
    . as $r | {
        passwordPolicy, bruteForceProtected, permanentLockout, failureFactor, waitIncrementSeconds,
        maxFailureWaitSeconds, minimumQuickLoginWaitSeconds, quickLoginCheckMilliSeconds, maxDeltaTimeSeconds,
        browserFlow, acr_loa_map: .attributes["acr.loa.map"],
        roles: [.roles.realm[] | select(.name == "mfa_required" or .name == "org_admin" or .name == "operator")
            | {name, composites: ((.composites.realm // []) | sort)}] | sort_by(.name),
        groups: [.groups[] | {name, realmRoles: ((.realmRoles // []) | sort)}] | sort_by(.name),
        default_scopes: [.clients[] | select(.clientId == "reticora-app") | .defaultClientScopes | sort] | first,
        flow: tree($r; "browser-mfa"),
        organization_id: ([.components["org.keycloak.userprofile.UserProfileProvider"][0].config["kc.user.profile.config"][0]
            | strings | fromjson | .attributes[] | select(.name == "organization_id")] | first)
    }'
settings() { jq -S "$SETTINGS" "$1"; }

# ─── 1. Keycloak imports the realm of the release before WP-044 ──────────────
cat > "$T_ENV" <<ENV
RETICORA_PUBLIC_BASE_URL=http://192.0.2.10:3000
RETICORA_OIDC_ISSUER_URL=http://192.0.2.10:${KC_PORT}/realms/reticora
RETICORA_OIDC_REDIRECT_URL=http://192.0.2.10:3000/auth/callback
RETICORA_KEYCLOAK_PORT=${KC_PORT}
RETICORA_INITIAL_ADMIN_EMAIL=${ADMIN_EMAIL}
ENV
LOG="$TMP_ROOT/legacy.log"
: > "$KCADM_CALLS"
if ! installer_run legacy "$LOG"; then
    fail "Keycloak imports the legacy realm"
    show_log "$LOG"
    printf '%d passed, %d failed\n' "$PASS" "$FAIL"
    exit 1
fi
ok "Keycloak imports the legacy realm"
kc_login
export_realm "$TMP_ROOT/legacy.json"
legacy_state() {
    jq -e '(.passwordPolicy // "") == "" and .bruteForceProtected == false and .browserFlow == "browser"
        and ([.groups[] | {name, realmRoles: (.realmRoles // [])}] == [{name: "reticora-admin", realmRoles: []}])
        and ([.roles.realm[].name] | index(["mfa_required"]) == null)' "$TMP_ROOT/legacy.json" >/dev/null
}
check "legacy realm: no password policy, no lockout, built-in browser flow, reticora-admin without roles" legacy_state

# ─── 2. upgrade without the installer: --import-realm with the current export ─
LOG="$TMP_ROOT/import.log"
if installer_run import "$LOG"; then ok "Keycloak recreated with the current realm export"; else fail "Keycloak recreated with the current realm export"; show_log "$LOG"; fi
kc_login
export_realm "$TMP_ROOT/import.json"
# grep reads the whole log: with pipefail, grep -q would end the pipe at the
# first match and fail the check through SIGPIPE of compose logs.
import_skipped() { compose logs keycloak 2>&1 | grep -F "Realm 'reticora' already exists. Import skipped" >/dev/null; }
unchanged_by_import() {
    [ -n "$(settings "$TMP_ROOT/legacy.json")" ] && [ "$(settings "$TMP_ROOT/legacy.json")" = "$(settings "$TMP_ROOT/import.json")" ]
}
check "F-061: Keycloak skips the existing realm (\"Realm 'reticora' already exists. Import skipped\")" import_skipped
check "F-061: password policy, browser flow and groups unchanged by --import-realm" unchanged_by_import
jq -r '"F-061 observed after --import-realm with the current export: passwordPolicy=\(.passwordPolicy // "<none>"), bruteForceProtected=\(.bruteForceProtected), failureFactor=\(.failureFactor), browserFlow=\(.browserFlow), groups=\([.groups[] | "\(.name)\(.realmRoles // [])"] | join(",")), acr.loa.map=\(.attributes["acr.loa.map"] // "<none>")"' \
    "$TMP_ROOT/import.json"

# ─── 3. upgrade with the installer ───────────────────────────────────────────
LOG="$TMP_ROOT/install.log"
: > "$KCADM_CALLS"
if installer_run install "$LOG"; then ok "installer run on the legacy realm rc 0"; else fail "installer run on the legacy realm rc 0"; show_log "$LOG"; fi
kc_login
export_realm "$TMP_ROOT/upgraded.json"
upgraded() {
    jq -e --slurpfile want "$T_COMPOSE_DIR/.generated/realm-reticora.json" '
        .passwordPolicy == $want[0].passwordPolicy and .bruteForceProtected == true and .failureFactor == 5
        and .browserFlow == "browser-mfa" and .attributes["acr.loa.map"] == $want[0].attributes["acr.loa.map"]' \
        "$TMP_ROOT/upgraded.json" >/dev/null
}
group_roles() {
    jq -e '[.groups[] | {name, realmRoles}] | sort_by(.name) == [{name: "operators", realmRoles: ["operator"]}, {name: "reticora-admin", realmRoles: ["mfa_required"]}]' \
        "$TMP_ROOT/upgraded.json" >/dev/null &&
        jq -e '[.roles.realm[] | select(.name == "org_admin" or .name == "operator") | .composites.realm] == [["mfa_required"], ["mfa_required"]]' \
            "$TMP_ROOT/upgraded.json" >/dev/null
}
acr_scope() {
    jq -e '.clients[] | select(.clientId == "reticora-app") | .defaultClientScopes | index("acr")' "$TMP_ROOT/upgraded.json" >/dev/null
}
same_flow_as_bundled() {
    [ "$(settings "$T_COMPOSE_DIR/.generated/realm-reticora.json" | jq -S .flow)" = "$(settings "$TMP_ROOT/upgraded.json" | jq -S .flow)" ] &&
        [ "$(settings "$TMP_ROOT/upgraded.json" | jq '.flow | length')" -eq 3 ]
}
changes_logged() {
    grep -q 'Keycloak realm: password policy length(12) added' "$LOG" &&
        grep -q 'Keycloak realm: brute-force detection enabled' "$LOG" &&
        grep -q 'Keycloak realm: failureFactor 30 lowered to 5' "$LOG" &&
        grep -q 'Keycloak realm: flow browser-mfa created' "$LOG" &&
        grep -q 'Keycloak realm: browser flow browser replaced by browser-mfa' "$LOG" &&
        grep -q 'Keycloak realm: group operators created' "$LOG" &&
        grep -q 'Keycloak realm: group reticora-admin carries the realm role mfa_required' "$LOG" &&
        grep -q 'Keycloak realm: client reticora-app has the default client scope acr' "$LOG" &&
        grep -q 'Keycloak realm: user profile attribute organization_id declared' "$LOG" &&
        ! grep -q "$(sed -n 's/^RETICORA_OIDC_CLIENT_SECRET=//p' "$T_ENV")" "$LOG"
}
check "upgrade: password policy and brute-force detection of the realm export, browser flow browser-mfa, acr.loa.map" upgraded
check "upgrade: browser-mfa runs the executions of the realm export (OTP for mfa_required, acr levels)" same_flow_as_bundled
check "upgrade: groups reticora-admin (mfa_required) and operators (operator); org_admin and operator contain mfa_required" group_roles
check "upgrade: reticora-app has the default client scope acr" acr_scope
check "upgrade: every change is logged, no secret in the log" changes_logged

# The initial administrator.
ADMIN_ID="$(kc get users -r reticora -q username="$ADMIN_EMAIL" -q exact=true --fields id --format csv --noquotes 2>/dev/null | head -n 1)"
admin_account() {
    [ -n "$ADMIN_ID" ] &&
        kc get "users/$ADMIN_ID" -r reticora 2>/dev/null | jq -e --arg org "$DEFAULT_ORG" '
            (.requiredActions | sort) == ["CONFIGURE_TOTP", "UPDATE_PASSWORD"] and .attributes.organization_id == [$org]
            and .enabled == true' >/dev/null
}
admin_roles() {
    kc get "users/$ADMIN_ID/role-mappings/realm" -r reticora 2>/dev/null | jq -e 'map(.name) | index("org_admin")' >/dev/null &&
        kc get "users/$ADMIN_ID/role-mappings/realm/composite" -r reticora 2>/dev/null | jq -e 'map(.name) | index("mfa_required")' >/dev/null &&
        kc get "users/$ADMIN_ID/groups" -r reticora 2>/dev/null | jq -e 'map(.name) == ["reticora-admin"]' >/dev/null
}
# The temporary password works, but Keycloak first demands the required
# actions (password change, OTP setup): a direct grant answers "Account is
# not fully set up".
admin_password_temporary() {
    local password secret body
    password="$(sed -n "s|^  Initial login: *$ADMIN_EMAIL / ||p" "$LOG" | head -n 1)"
    secret="$(sed -n 's/^RETICORA_OIDC_CLIENT_SECRET=//p' "$T_ENV" | tail -n 1)"
    [ -n "$password" ] && kc get "users/$ADMIN_ID/credentials" -r reticora 2>/dev/null | jq -e 'map(.type) == ["password"]' >/dev/null || return 1
    body="$(curl -s --max-time 10 -d grant_type=password -d client_id=reticora-app --data-urlencode "client_secret=$secret" \
        --data-urlencode "username=$ADMIN_EMAIL" --data-urlencode "password=$password" \
        "http://127.0.0.1:${KC_PORT}/realms/reticora/protocol/openid-connect/token")"
    printf '%s' "$body" | jq -e '.error == "invalid_grant" and .error_description == "Account is not fully set up"' >/dev/null
}
no_demo_admins() {
    local user
    for user in admin@demo.local admin@reticora.local; do
        [ -z "$(kc get users -r reticora -q username="$user" -q exact=true --fields id --format csv --noquotes 2>/dev/null)" ] || return 1
    done
    grep -q 'Removed the demo administrator admin@reticora.local' "$LOG"
}
check "initial administrator: UPDATE_PASSWORD and CONFIGURE_TOTP, organization_id of the default organization" admin_account
check "initial administrator: realm role org_admin (contains mfa_required), group reticora-admin" admin_roles
check "initial administrator: temporary password, login only after the required actions" admin_password_temporary
check "no admin@demo.local; the demo administrator admin@reticora.local of the legacy realm is removed" no_demo_admins

# ─── 4. re-run without a change ──────────────────────────────────────────────
LOG="$TMP_ROOT/rerun.log"
: > "$KCADM_CALLS"
if installer_run install "$LOG"; then ok "re-run rc 0"; else fail "re-run rc 0"; show_log "$LOG"; fi
check "re-run: no kcadm write call" [ "$(kc_writes)" -eq 0 ]
check "re-run: realm settings reported unchanged" grep -q 'Keycloak realm settings match the bundled realm' "$LOG"
kc_login
export_realm "$TMP_ROOT/rerun.json"

# ─── 5. stricter values of the operator are kept ─────────────────────────────
STRICT_POLICY="length(16) and upperCase(1) and lowerCase(1) and digits(1) and specialChars(1) and notUsername(undefined) and notEmail(undefined) and passwordHistory(5)"
kc get realms/reticora 2>/dev/null \
    | jq -c --arg p "$STRICT_POLICY" '. * {passwordPolicy: $p, failureFactor: 3, bruteForceProtected: false}' \
    | kc update realms/reticora -f - >/dev/null 2>&1
LOG="$TMP_ROOT/strict.log"
: > "$KCADM_CALLS"
if installer_run install "$LOG"; then ok "run with operator values rc 0"; else fail "run with operator values rc 0"; show_log "$LOG"; fi
kc_login
stricter_kept() {
    kc get realms/reticora 2>/dev/null | jq -e --arg p "$STRICT_POLICY" \
        '.passwordPolicy == $p and .failureFactor == 3 and .bruteForceProtected == true' >/dev/null &&
        grep -q 'Keycloak realm: kept password policy length(16) (stricter than length(12))' "$LOG" &&
        grep -q 'Keycloak realm: kept failureFactor 3 (stricter than 5)' "$LOG" &&
        grep -q 'Keycloak realm: brute-force detection enabled' "$LOG" &&
        [ "$(grep -c '^update realms/reticora$' "$KCADM_CALLS")" -eq 1 ] && [ "$(kc_writes)" -eq 1 ]
}
check "operator values: password length 16 and failureFactor 3 kept, brute-force detection enabled again (one realm update)" stricter_kept

# ─── 6. a fresh installation has the same settings ───────────────────────────
compose down -v --remove-orphans >/dev/null 2>&1
LOG="$TMP_ROOT/fresh.log"
: > "$KCADM_CALLS"
if installer_run install "$LOG"; then ok "fresh installation rc 0"; else fail "fresh installation rc 0"; show_log "$LOG"; fi
kc_login
export_realm "$TMP_ROOT/fresh.json"
same_as_fresh() {
    [ "$(settings "$TMP_ROOT/fresh.json")" = "$(settings "$TMP_ROOT/rerun.json")" ] || {
        diff <(settings "$TMP_ROOT/fresh.json") <(settings "$TMP_ROOT/rerun.json") | head -n 20
        return 1
    }
}
check "the upgraded realm has the settings of a fresh installation (policies, flow, roles, groups, acr, organization_id)" same_as_fresh
check "fresh installation: apply_realm_settings changes nothing in the imported realm" \
    grep -q 'Keycloak realm settings match the bundled realm' "$LOG"

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
