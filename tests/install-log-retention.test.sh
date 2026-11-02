#!/usr/bin/env bash
#
# install-log-retention.test.sh — container logs at most 30 days (NFR-06,
# E-77 Option 1, WP-458, F-150). Runs in the CI job gate
# (.github/workflows/ci.yml) next to tests/audit/log-retention.test.sh.
#
# Installer: configure_log_retention of install-cloud.sh, sourced without its
# main call, with systemctl and docker stubbed (calls recorded) and the
# journald configuration under a directory of the test (JOURNALD_ROOT, as /):
#   journald                   drop-in etc/systemd/journald.conf.d/reticora.conf
#                              with MaxRetentionSec=30day and MaxFileSec=1day,
#                              journald restarted, the host-wide effect named;
#                              no COMPOSE_FILE in .env, compose_cmd without the
#                              json-file rotation
#   journald re-run            drop-in unchanged, no restart
#   no journald / no systemd   COMPOSE_FILE in .env selects
#                              docker-compose.logging-json.yml (with the
#                              source-build override when it is in use),
#                              compose_cmd passes it; no drop-in, no restart
#   stricter / partly stricter / looser / masked operator settings, a stricter
#   value in reticora.conf itself, an earlier drop-in that a stricter operator
#   drop-in makes redundant, a later looser drop-in (warning), the switch from
#   the fallback to journald, an unwritable drop-in (fallback), an operator
#   COMPOSE_FILE without the rotation (stop), write_env on a re-run;
#   timespan_seconds against systemd.time(7).
# Compose (docker compose config, no daemon): valid with and without
# docker-compose.logging-json.yml; without it every service logs to journald,
# with it every service rotates json-file (max-size, max-file); the
# COMPOSE_FILE the installer writes merges the rotation when docker compose
# runs in deploy/docker-compose.
# Negative case of tests/audit/log-retention.test.sh: copies of the compose
# file with one service without logging section, or with json-file without
# rotation, fail it; the unchanged copy passes.
#
# Needs docker compose (only `docker compose config`), jq and python3 with
# PyYAML (tests/audit/log-retention.test.sh).
# Retest: ./tests/install-log-retention.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
ROOT="$(pwd)"
COMPOSE_SRC="$ROOT/deploy/docker-compose"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }
check() {
    local name="$1"
    shift
    if "$@"; then ok "$name"; else fail "$name"; fi
}

if ! docker compose version >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1 ||
    ! python3 -I -c 'import yaml' 2>/dev/null; then
    echo "FAIL docker compose, jq and python3 with PyYAML are required"
    exit 1
fi

# The installer without its main call (last line), next to the preflight
# helpers it sources.
mkdir -p "$TMP/harness"
head -n -1 install-cloud.sh > "$TMP/harness/install-cloud.sh"
cp install-preflight.sh "$TMP/harness/"

# Stubs: systemctl answers is-active after $JOURNALD (active, inactive, or
# no-systemd like a container without systemd as PID 1); every call of
# systemctl and docker is recorded in $STUB_LOG.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/systemctl" <<'EOF'
#!/bin/sh
printf 'systemctl %s\n' "$*" >> "$STUB_LOG"
if [ "$1" = is-active ]; then
    case "$JOURNALD" in
        active) exit 0 ;;
        no-systemd) echo "System has not been booted with systemd as init system (PID 1). Can't operate." >&2; exit 1 ;;
        *) exit 3 ;;
    esac
fi
exit 0
EOF
# docker records its arguments separated by tabs (the case directories
# contain blanks).
cat > "$TMP/bin/docker" <<'EOF'
#!/bin/sh
{ printf 'docker'; for a in "$@"; do printf '\t%s' "$a"; done; printf '\n'; } >> "$STUB_LOG"
exit 0
EOF
chmod +x "$TMP/bin/systemctl" "$TMP/bin/docker"

DROPIN=etc/systemd/journald.conf.d/reticora.conf

# new_case <case> [override] — $TMP/<case>: compose/ with the compose files
# (with override: also the source-build override), an .env, an empty
# journald root root/.
new_case() {
    local dir="$TMP/$1"
    mkdir -p "$dir/compose" "$dir/root"
    cp "$COMPOSE_SRC/docker-compose.yml" "$COMPOSE_SRC/docker-compose.logging-json.yml" "$dir/compose/"
    [ "${2:-}" = override ] && cp "$COMPOSE_SRC/docker-compose.override.yml" "$dir/compose/"
    printf 'RETICORA_DB_PASSWORD=x\n' > "$dir/compose/.env"
}

# conf <case> <path below root> <lines…> — a journald configuration file.
conf() {
    local file="$TMP/$1/root/$2"
    shift 2
    mkdir -p "$(dirname "$file")"
    printf '%s\n' "$@" > "$file"
}

# run_installer <case> <journald state> [extra commands] — configure_log_retention
# of the installer on the case, then compose_cmd config (docker stub). Output
# in <case>/installer.log, calls in <case>/calls.log (both replaced per run),
# exit status in <case>/rc.
run_installer() {
    local dir="$TMP/$1" state="$2" extra="${3:-:}"
    : > "$dir/calls.log"
    # shellcheck disable=SC2016 # expanded by the inner shell
    PATH="$TMP/bin:$PATH" STUB_LOG="$dir/calls.log" JOURNALD="$state" \
        bash -c 't_harness="$1" t_dir="$2" t_extra="$3"; set --; . "$t_harness"
            NON_INTERACTIVE=1; SUDO=""; COMPOSE=(docker compose)
            COMPOSE_DIR="$t_dir/compose"; COMPOSE_FILE="$COMPOSE_DIR/docker-compose.yml"
            OVERRIDE_FILE="$COMPOSE_DIR/docker-compose.override.yml"
            LOGGING_JSON_FILE="$COMPOSE_DIR/docker-compose.logging-json.yml"
            ENV_FILE="$COMPOSE_DIR/.env"; JOURNALD_ROOT="$t_dir/root"
            eval "$t_extra"
            configure_log_retention && compose_cmd config' \
        _ "$TMP/harness/install-cloud.sh" "$dir" "$extra" > "$dir/installer.log" 2>&1
    echo "$?" > "$dir/rc"
}

succeeded()   { [ "$(cat "$TMP/$1/rc")" = 0 ] || { sed 's/^/     /' "$TMP/$1/installer.log" | tail -n 5; return 1; }; }
stopped()     { [ "$(cat "$TMP/$1/rc")" != 0 ]; }
dropin_has()  { grep -qxF -- "$2" "$TMP/$1/root/$DROPIN" 2>/dev/null; }
# dropin_lacks <case> <key> — the drop-in (if any) does not set <key>.
dropin_lacks() { ! grep -q "^$2" "$TMP/$1/root/$DROPIN" 2>/dev/null || { echo "     $(grep "^$2" "$TMP/$1/root/$DROPIN")"; return 1; }; }
no_dropin()   { [ ! -e "$TMP/$1/root/$DROPIN" ]; }
said()        { grep -qF -- "$2" "$TMP/$1/installer.log"; }
restarted()   { grep -qx 'systemctl restart systemd-journald' "$TMP/$1/calls.log"; }
env_line()    { grep -qx -- "$2" "$TMP/$1/compose/.env"; }
no_compose_file() { ! grep -q '^COMPOSE_FILE=' "$TMP/$1/compose/.env"; }
not_in_env()  { ! grep -qF -- "$2" "$TMP/$1/compose/.env"; }
# compose_files <case> — the -f files of compose_cmd config (docker stub).
compose_files() {
    awk -F'\t' '$1 == "docker" && $2 == "compose" { for (i = 3; i < NF; i++) if ($i == "-f") { n = split($(i + 1), p, "/"); printf "%s%s", sep, p[n]; sep = " " } }' \
        "$TMP/$1/calls.log"
}
compose_files_are() { [ "$(compose_files "$1")" = "$2" ] || { echo "     compose_cmd -f: $(compose_files "$1")"; return 1; }; }
not_said()    { ! said "$@"; }
not_restarted() { ! restarted "$1"; }
# effective <case> <key> — journald_setting of the installer on the case.
effective() {
    # shellcheck disable=SC2016 # expanded by the inner shell
    bash -c 't_harness="$1"; t_root="$2"; t_key="$3"; set --; . "$t_harness"; JOURNALD_ROOT="$t_root"; journald_setting "$t_key"' \
        _ "$TMP/harness/install-cloud.sh" "$TMP/$1/root" "$2" 2>/dev/null
}
effective_is() { [ "$(effective "$1" "$2")" = "$3" ] || { echo "     $2 in effect: $(effective "$1" "$2")"; return 1; }; }

# ─── journald running ────────────────────────────────────────────────────────
new_case journald override
run_installer journald active
check "journald: the installer step succeeds"                                  succeeded journald
check "journald: drop-in $DROPIN sets MaxRetentionSec=30day"                    dropin_has journald MaxRetentionSec=30day
check "journald: drop-in sets MaxFileSec=1day (daily journal files)"            dropin_has journald MaxFileSec=1day
check "journald: drop-in has the [Journal] section"                             dropin_has journald '[Journal]'
check "journald: systemd-journald restarted"                                    restarted journald
check "journald: the installer names the host-wide effect"                      said journald 'applies to the whole journal of this host, not only to Reticora'
check "journald: no COMPOSE_FILE in .env"                                       no_compose_file journald
check "journald: compose_cmd without the json-file rotation"                    compose_files_are journald 'docker-compose.yml docker-compose.override.yml'
check "journald: 30 days in effect"                                             effective_is journald MaxRetentionSec 2592000
sum_first="$(sha256sum "$TMP/journald/root/$DROPIN" 2>/dev/null)"
run_installer journald active
check "journald re-run: succeeds"                                               succeeded journald
check "journald re-run: drop-in unchanged"                                      test "$(sha256sum "$TMP/journald/root/$DROPIN" 2>/dev/null)" = "$sum_first"
check "journald re-run: no restart of systemd-journald"                         not_restarted journald
check "journald re-run: no host-wide notice for an unchanged drop-in"           not_said journald 'wrote '

# ─── no systemd-journald ─────────────────────────────────────────────────────
new_case "no journald" override
run_installer "no journald" inactive
check "no journald: the installer step succeeds"                                succeeded "no journald"
check "no journald: COMPOSE_FILE selects the json-file rotation with the source-build override" \
    env_line "no journald" 'COMPOSE_FILE=docker-compose.yml:docker-compose.override.yml:docker-compose.logging-json.yml'
check "no journald: compose_cmd merges the rotation"                            compose_files_are "no journald" 'docker-compose.yml docker-compose.override.yml docker-compose.logging-json.yml'
check "no journald: no drop-in"                                                 no_dropin "no journald"
check "no journald: no restart of systemd-journald"                             not_restarted "no journald"
check "no journald: the installer names the size-only bound"                    said "no journald" 'It bounds the size, not the age'

new_case "no systemd"
run_installer "no systemd" no-systemd
check "no systemd (container, release mode): COMPOSE_FILE selects the rotation without override" \
    env_line "no systemd" 'COMPOSE_FILE=docker-compose.yml:docker-compose.logging-json.yml'
check "no systemd: compose_cmd merges the rotation"                             compose_files_are "no systemd" 'docker-compose.yml docker-compose.logging-json.yml'
check "no systemd: no drop-in"                                                  no_dropin "no systemd"
cp "$TMP/no systemd/compose/.env" "$TMP/env-before"
run_installer "no systemd" no-systemd
check "no systemd re-run: .env unchanged (one COMPOSE_FILE)"                    cmp -s "$TMP/env-before" "$TMP/no systemd/compose/.env"

# ─── operator settings ───────────────────────────────────────────────────────
new_case stricter
conf stricter etc/systemd/journald.conf.d/10-operator.conf '[Journal]' 'MaxRetentionSec=7day' 'MaxFileSec=12h'
run_installer stricter active
check "stricter operator drop-in: no drop-in written"                           no_dropin stricter
check "stricter operator drop-in: no restart"                                   not_restarted stricter
check "stricter operator drop-in: the installer keeps it"                       said stricter 'already sets MaxRetentionSec MaxFileSec at least as strictly'
check "stricter operator drop-in: 7 days stay in effect"                        effective_is stricter MaxRetentionSec 604800
check "stricter operator drop-in: no COMPOSE_FILE"                              no_compose_file stricter

new_case partly
conf partly etc/systemd/journald.conf '[Journal]' '#MaxRetentionSec=' 'MaxRetentionSec=2week'
run_installer partly active
check "stricter MaxRetentionSec in journald.conf: drop-in leaves it out"        dropin_lacks partly MaxRetentionSec
check "stricter MaxRetentionSec in journald.conf: drop-in sets MaxFileSec=1day" dropin_has partly MaxFileSec=1day
check "stricter MaxRetentionSec in journald.conf: 2 weeks stay in effect"       effective_is partly MaxRetentionSec 1209600

new_case looser
conf looser etc/systemd/journald.conf '[Journal]' 'MaxRetentionSec=3month'
conf looser usr/lib/systemd/journald.conf.d/50-vendor.conf '[Journal]' 'MaxFileSec=1month'
run_installer looser active
check "looser operator settings: drop-in sets MaxRetentionSec=30day"            dropin_has looser MaxRetentionSec=30day
check "looser operator settings: drop-in sets MaxFileSec=1day"                  dropin_has looser MaxFileSec=1day
check "looser operator settings: 30 days in effect"                             effective_is looser MaxRetentionSec 2592000
check "looser operator settings: one day per journal file in effect"            effective_is looser MaxFileSec 86400

new_case masked
conf masked usr/lib/systemd/journald.conf.d/10-vendor.conf '[Journal]' 'MaxRetentionSec=7day'
mkdir -p "$TMP/masked/root/etc/systemd/journald.conf.d"
ln -s /dev/null "$TMP/masked/root/etc/systemd/journald.conf.d/10-vendor.conf"
run_installer masked active
check "vendor drop-in masked in /etc: it does not count, drop-in sets 30day"    dropin_has masked MaxRetentionSec=30day

new_case "own stricter"
conf "own stricter" "$DROPIN" '[Journal]' 'MaxRetentionSec=14day'
run_installer "own stricter" active
check "stricter value in reticora.conf: kept, not overwritten with 30day"       dropin_has "own stricter" MaxRetentionSec=14day
check "stricter value in reticora.conf: 14 days in effect"                      effective_is "own stricter" MaxRetentionSec 1209600

new_case "operator later stricter"
run_installer "operator later stricter" active
conf "operator later stricter" etc/systemd/journald.conf.d/10-operator.conf '[Journal]' 'MaxRetentionSec=7day'
run_installer "operator later stricter" active
check "earlier drop-in, stricter operator drop-in added: reticora.conf no longer sets MaxRetentionSec" \
    dropin_lacks "operator later stricter" MaxRetentionSec
check "earlier drop-in, stricter operator drop-in added: 7 days in effect"     effective_is "operator later stricter" MaxRetentionSec 604800
check "earlier drop-in, stricter operator drop-in added: journald restarted"   restarted "operator later stricter"

new_case "later looser"
conf "later looser" etc/systemd/journald.conf.d/zz-operator.conf '[Journal]' 'MaxRetentionSec=90day'
run_installer "later looser" active
check "looser operator drop-in sorting after reticora.conf: drop-in written"   dropin_has "later looser" MaxRetentionSec=30day
check "looser operator drop-in sorting after reticora.conf: the installer warns" \
    said "later looser" 'sorting after reticora.conf sets a longer MaxRetentionSec'

# ─── switches and failures ───────────────────────────────────────────────────
new_case switch override
run_installer switch inactive
run_installer switch active
check "fallback, then journald: COMPOSE_FILE removed"                           no_compose_file switch
check "fallback, then journald: its comment removed"                            not_in_env switch 'json-file rotation'
check "fallback, then journald: drop-in written"                                dropin_has switch MaxRetentionSec=30day
check "fallback, then journald: compose_cmd without the rotation"              compose_files_are switch 'docker-compose.yml docker-compose.override.yml'

new_case unwritable
run_installer unwritable active 'SUDO=false'
check "drop-in not writable: the installer step succeeds"                       succeeded unwritable
check "drop-in not writable: the installer says so"                             said unwritable 'could not write'
check "drop-in not writable: json-file rotation through COMPOSE_FILE"          env_line unwritable 'COMPOSE_FILE=docker-compose.yml:docker-compose.logging-json.yml'
check "drop-in not writable: no restart"                                        not_restarted unwritable

new_case "operator compose file"
printf 'COMPOSE_FILE=docker-compose.yml:my-extra.yml\n' >> "$TMP/operator compose file/compose/.env"
run_installer "operator compose file" inactive
check "operator COMPOSE_FILE without the rotation, no journald: the installer stops" stopped "operator compose file"
check "operator COMPOSE_FILE without the rotation: the installer names it"     said "operator compose file" 'remove COMPOSE_FILE from'
run_installer "operator compose file" active
check "operator COMPOSE_FILE without the rotation, journald: it stays"          env_line "operator compose file" 'COMPOSE_FILE=docker-compose.yml:my-extra.yml'

# write_env on a re-run: the installer's COMPOSE_FILE is not carried over as
# an extra (configure_log_retention sets it again), other settings are.
test_merge_extras() {
    # shellcheck disable=SC2016 # expanded by the inner shell
    bash -c 't_harness="$1" t_dir="$2"; set --; . "$t_harness"
        LOGGING_JSON_FILE=/x/docker-compose.logging-json.yml; ENV_FILE="$t_dir/old.env"
        printf "%s\n" "RETICORA_HTTP_BIND=127.0.0.1" "COMPOSE_FILE=docker-compose.yml:docker-compose.logging-json.yml" > "$ENV_FILE"
        printf "RETICORA_DB_PASSWORD=x\n" > "$t_dir/new.env"
        merge_env_extras "$t_dir/new.env"
        grep -qx RETICORA_HTTP_BIND=127.0.0.1 "$t_dir/new.env" && ! grep -q "^COMPOSE_FILE=" "$t_dir/new.env"' \
        _ "$TMP/harness/install-cloud.sh" "$TMP"
}
check "write_env re-run: the installer's COMPOSE_FILE is not kept as an extra, other settings are" test_merge_extras

# ─── timespan_seconds (systemd.time(7)) ──────────────────────────────────────
timespan_is() {
    local got
    # shellcheck disable=SC2016 # expanded by the inner shell
    got="$(bash -c 't_harness="$1" t_value="$2"; set --; . "$t_harness"; timespan_seconds "$t_value"' \
        _ "$TMP/harness/install-cloud.sh" "$1" 2>/dev/null)" || got="<rejected>"
    [ "$got" = "$2" ] || { echo "     timespan_seconds '$1' = $got, expected $2"; return 1; }
}
for spec in "30day=2592000" "720h=2592000" "2592000=2592000" "1month=2629800" "4w=2419200" "2w 3d=1468800" \
    "12h30min=45000" "1y=31557600" "90 days=7776000" "0=0" "=0" "infinity=0" "1fortnight=<rejected>" "abc=<rejected>"; do
    check "timespan_seconds '${spec%%=*}' = ${spec#*=}" timespan_is "${spec%%=*}" "${spec#*=}"
done

# ─── docker compose config with and without the json-file rotation ───────────
# clean <command…> — without the caller's RETICORA_*, KEYCLOAK_* and COMPOSE_*
# variables (they override .env values).
clean() {
    local unset=() v
    for v in $(compgen -e); do
        case "$v" in RETICORA_*|KEYCLOAK_*|COMPOSE_*) unset+=(-u "$v") ;; esac
    done
    env ${unset[@]+"${unset[@]}"} "$@"
}
cat > "$TMP/stack.env" <<'EOF'
RETICORA_DB_PASSWORD=test-db
RETICORA_DB_APP_PASSWORD=test-app-password-0123456789
RETICORA_BACKUP_CIPHER_PASS=test-cipher
RETICORA_S3_ACCESS_KEY=test-access
RETICORA_S3_SECRET_KEY=test-secret
KEYCLOAK_DB_PASSWORD=test-kcdb
KEYCLOAK_ADMIN_PASSWORD=test-kcadmin
RETICORA_MASTER_KEY=dGVzdA==
EOF

# compose_config <name> <dir> <env file> [-f files…] — docker compose config
# (all profiles) as JSON in $TMP/<name>.json.
compose_config() {
    local name="$1" dir="$2" env="$3"
    shift 3
    if (cd "$dir" && clean docker compose --env-file "$env" "$@" --profile opensearch config --format json) \
        > "$TMP/$name.json" 2> "$TMP/$name.err"; then
        ok "docker compose config $name is valid"
    else
        fail "docker compose config $name: $(grep -v level=warning "$TMP/$name.err" | head -3)"
        return 1
    fi
}
# drivers <name> — "<service> <driver> <max-size> <max-file>" per service.
drivers() {
    jq -r '.services | to_entries[] | "\(.key) \(.value.logging.driver // "-") \(.value.logging.options["max-size"] // "-") \(.value.logging.options["max-file"] // "-")"' \
        "$TMP/$1.json"
}
services_of() { jq -r '.services | keys[]' "$TMP/$1.json" | paste -sd' ' -; }

# all_journald <name> — every service logs to journald without options.
all_journald() {
    local bad
    bad="$(drivers "$1" | awk '$2 != "journald" || $3 != "-" || $4 != "-"')"
    [ -z "$bad" ] || { echo "     not journald: $bad"; return 1; }
}
# all_rotating <name> — every service rotates json-file with max-size and max-file.
all_rotating() {
    local bad
    bad="$(drivers "$1" | awk '$2 != "json-file" || $3 == "-" || $4 == "-"')"
    [ -z "$bad" ] || { echo "     not rotating: $bad"; return 1; }
}

if compose_config "without the rotation" "$COMPOSE_SRC" "$TMP/stack.env" -f docker-compose.yml; then
    all_services="$(services_of "without the rotation")"
    check "without the rotation: every service logs to journald ($all_services)" all_journald "without the rotation"
    check "without the rotation: 12 services incl. opensearch" test "$(wc -w <<<"$all_services")" -eq 12
fi
if compose_config "with the rotation" "$COMPOSE_SRC" "$TMP/stack.env" -f docker-compose.yml -f docker-compose.logging-json.yml; then
    check "with the rotation: every service rotates json-file (max-size, max-file)" all_rotating "with the rotation"
    check "with the rotation: the same services as without it" test "$(services_of "with the rotation")" = "${all_services:-}"
fi
# The .env the installer wrote, read by docker compose in deploy/docker-compose
# (without -f, as in docs and the install-smoke workflow).
cat "$TMP/stack.env" > "$TMP/fallback.env"
grep '^COMPOSE_FILE=' "$TMP/no journald/compose/.env" >> "$TMP/fallback.env"
if compose_config "from the fallback .env" "$COMPOSE_SRC" "$TMP/fallback.env"; then
    check "from the fallback .env: COMPOSE_FILE merges the rotation for every service" all_rotating "from the fallback .env"
fi
cat "$TMP/stack.env" > "$TMP/journald.env"
if compose_config "from the journald .env" "$COMPOSE_SRC" "$TMP/journald.env"; then
    check "from the journald .env: every service logs to journald" all_journald "from the journald .env"
fi

# ─── negative case of tests/audit/log-retention.test.sh ──────────────────────
# retention_copy <name> <awk program> — tests/audit/log-retention.test.sh and
# the compose file, changed by the awk program, in $TMP/lr-<name>; runs the
# copy: output in $TMP/lr-<name>.out, exit status in $TMP/lr-<name>.rc.
retention_copy() {
    local dir="$TMP/lr-$1"
    mkdir -p "$dir/tests/audit" "$dir/deploy/docker-compose"
    cp tests/audit/log-retention.test.sh "$dir/tests/audit/"
    awk "$2" "$COMPOSE_SRC/docker-compose.yml" > "$dir/deploy/docker-compose/docker-compose.yml"
    bash "$dir/tests/audit/log-retention.test.sh" > "$TMP/lr-$1.out" 2>&1
    echo "$?" > "$TMP/lr-$1.rc"
}
# A service block starts with "  <name>:" under services.
# shellcheck disable=SC2016 # awk program
service_awk='/^  [a-z-]+:$/ { svc = substr($1, 1, length($1) - 1) }'
retention_copy unchanged '{ print }'
check "log-retention.test.sh on an unchanged copy: 3 passed, 0 failed" \
    grep -qx '3 passed, 0 failed' "$TMP/lr-unchanged.out"
# shellcheck disable=SC2016 # awk program
retention_copy redis-unbounded "$service_awk"' svc == "redis" && $0 == "    logging: *logging" { next } { print }'
check "copy with redis without logging section: one line less than the original" \
    test "$(grep -c 'logging: \*logging' "$TMP/lr-redis-unbounded/deploy/docker-compose/docker-compose.yml")" -eq 11
check "copy with redis without logging section: log-retention.test.sh fails" \
    test "$(cat "$TMP/lr-redis-unbounded.rc")" != 0
check "copy with redis without logging section: it names redis" \
    grep -qE '^FAIL NFR-06 logs 30 days: .*\bredis\b' "$TMP/lr-redis-unbounded.out"
# shellcheck disable=SC2016 # awk program
retention_copy server-json "$service_awk"' svc == "server" && $0 == "    logging: *logging" { print "    logging:"; print "      driver: json-file"; next } { print }'
check "copy with server on json-file without rotation: log-retention.test.sh fails" \
    test "$(cat "$TMP/lr-server-json.rc")" != 0
check "copy with server on json-file without rotation: it names server" \
    grep -qE '^FAIL NFR-06 logs 30 days: .*\bserver\b' "$TMP/lr-server-json.out"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
