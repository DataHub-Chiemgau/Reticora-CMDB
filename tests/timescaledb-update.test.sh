#!/usr/bin/env bash
#
# timescaledb-update.test.sh — update of the TimescaleDB extension of an
# existing database (WP-604, INS-05, OPS-01): scripts/ops/timescaledb-update.sh
# and its call in start_stack of install-cloud.sh.
#
# Stub part (always): psql and docker are stubs on a PATH of their own; the
# psql stub keeps per database the extension version, the default version of
# the library and the state of the event trigger reticora_restrict_app_ddl,
# accepts only calls with -X and exactly one -c, and answers
# ALTER EXTENSION timescaledb UPDATE while the trigger is enabled with the
# FATAL of the real server ("already loaded with a different version").
# Cases: order DISABLE → UPDATE → ENABLE in a session each, postgres and
# template1 with UPDATE only; a second run changes nothing; no action at the
# current version; a failed update enables the trigger again (trap) and
# aborts with the message; a trigger an aborted update left disabled is
# enabled; an extension newer than the library aborts; while a login of the
# application roles stays connected the trigger is not disabled; start_stack
# stops the server and runs the update through
# `docker compose exec -T postgres psql` between sync_db_password and
# run_migrations, a failed update aborts it before the migrations. The
# installer is sourced without main; docker, psql, sudo, curl and systemctl
# are stubs, everything is written below a temporary directory.
#
# PostgreSQL part (with TSDB_UPDATE_TEST_DATABASE_URL, the superuser URL of a
# server with the TimescaleDB image of the repository): a database of its own
# with CREATE EXTENSION timescaledb VERSION '$TSDB_UPDATE_TEST_FROM_VERSION'
# (default 2.17.2), all migrations; a plain update fails there with the
# FATAL; with a session of a reticora_app login open the script refuses;
# then the script: the extension is at default_version, the event trigger is
# enabled and refuses DDL of reticora_app again, a second run changes
# nothing, migrate up has nothing left and
# TestSchemaBaselineMatchesMigratedSchema passes on that database. The
# temporary database and login are dropped at the end. The script also
# visits postgres and template1 of that server; at the image's version it
# leaves them unchanged. Needs psql, openssl, migrate ($MIGRATE) and go ($GO).
#
# Usage: ./tests/timescaledb-update.test.sh
#   TSDB_UPDATE_TEST_DATABASE_URL=postgres://reticora:…@localhost:5432/postgres?sslmode=disable \
#       ./tests/timescaledb-update.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
REPO_ROOT="$(pwd)"
SCRIPT="$REPO_ROOT/scripts/ops/timescaledb-update.sh"

TMP_ROOT="$(mktemp -d)"
PG_CLEANUP=""
cleanup() {
    [ -z "$PG_CLEANUP" ] || eval "$PG_CLEANUP"
    rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | tail -n 12 | sed 's/^/     /'; return 0; }

# ─── Stubs ────────────────────────────────────────────────────────────────────
BIN="$TMP_ROOT/bin"
mkdir -p "$BIN"

# psql: one statement per call; state in $TSDB_STUB_STATE/<database>/{ext,trigger},
# the library's default version in $TSDB_STUB_STATE/default, the maintenance
# databases in $TSDB_STUB_STATE/maintenance. Every call is logged as
# "psql <database> <statement>" in $TSDB_STUB_EVENTS.
cat > "$BIN/psql" <<'STUB'
#!/usr/bin/env bash
set -u
db="" sql="" x=0 commands=0
while [ "$#" -gt 0 ]; do
    case "$1" in
        -X) x=1 ;;
        -d) db="$2"; shift ;;
        -c) sql="$2"; commands=$((commands + 1)); shift ;;
        -U|-h|-p|-v) shift ;;
    esac
    shift
done
printf 'psql %s %s\n' "$db" "$sql" >> "$TSDB_STUB_EVENTS"
[ "$x" -eq 1 ] || { echo "psql stub: called without -X" >&2; exit 3; }
[ "$commands" -eq 1 ] || { echo "psql stub: want exactly one -c per session" >&2; exit 3; }
dir="$TSDB_STUB_STATE/$db"
[ -d "$dir" ] || { echo "psql: error: database \"$db\" does not exist" >&2; exit 2; }
get() { cat "$dir/$1" 2>/dev/null || true; }
case "$sql" in
    *"FROM pg_extension e JOIN pg_available_extensions a"*)
        [ -z "$(get ext)" ] || printf '%s %s\n' "$(get ext)" "$(cat "$TSDB_STUB_STATE/default")" ;;
    *"FROM pg_event_trigger WHERE evtname = 'reticora_restrict_app_ddl'"*) get trigger ;;
    *"FROM pg_database WHERE datname IN ('postgres', 'template1') AND datallowconn"*) cat "$TSDB_STUB_STATE/maintenance" ;;
    # One line of <database>/sessions per query: the connected application
    # logins at that moment.
    *"FROM pg_stat_activity a JOIN pg_roles r"*"'reticora_app', 'reticora_owner'"*)
        if [ -s "$dir/sessions" ]; then
            head -n 1 "$dir/sessions"
            sed -i 1d "$dir/sessions"
        fi ;;
    "ALTER EVENT TRIGGER reticora_restrict_app_ddl DISABLE")
        [ -f "$dir/trigger" ] || { echo 'ERROR:  event trigger "reticora_restrict_app_ddl" does not exist' >&2; exit 1; }
        echo D > "$dir/trigger" ;;
    "ALTER EVENT TRIGGER reticora_restrict_app_ddl ENABLE")
        [ -f "$dir/trigger" ] || { echo 'ERROR:  event trigger "reticora_restrict_app_ddl" does not exist' >&2; exit 1; }
        [ ! -e "$TSDB_STUB_STATE/fail-enable" ] || { echo 'ERROR:  stub: enable refused' >&2; exit 1; }
        echo O > "$dir/trigger" ;;
    "ALTER EXTENSION timescaledb UPDATE")
        case "$(get trigger)" in
            O|A)
                echo 'FATAL:  "timescaledb" already loaded with a different version' >&2
                echo 'server closed the connection unexpectedly' >&2
                exit 2 ;;
        esac
        [ ! -e "$TSDB_STUB_STATE/fail-update" ] || { echo 'ERROR:  stub: update failed' >&2; exit 1; }
        cat "$TSDB_STUB_STATE/default" > "$dir/ext" ;;
    *) echo "psql stub: unexpected statement: $sql" >&2; exit 3 ;;
esac
STUB

# docker: logs "docker <args>"; `… exec -T postgres psql <args>` runs the psql
# stub with <args>, everything else succeeds without effect.
cat > "$BIN/docker" <<'STUB'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >> "$TSDB_STUB_EVENTS"
args=("$@")
for ((i = 0; i + 3 < ${#args[@]}; i++)); do
    if [ "${args[i]}" = exec ] && [ "${args[i + 1]}" = -T ] && [ "${args[i + 2]}" = postgres ] && [ "${args[i + 3]}" = psql ]; then
        exec "$(dirname "$0")/psql" "${args[@]:i+4}"
    fi
done
exit 0
STUB

# Anything else the installer could reach is refused and logged.
for tool in sudo curl systemctl service apt-get; do
    cat > "$BIN/$tool" <<STUB
#!/usr/bin/env bash
printf '$tool %s\\n' "\$*" >> "\$TSDB_STUB_EVENTS"
exit 1
STUB
done
chmod +x "$BIN"/*

# stub_db <database> <extension version|""> [trigger state] — one database
# of the stub; without a trigger state the database has no event trigger.
stub_db() {
    mkdir -p "$TSDB_STUB_STATE/$1"
    [ -z "$2" ] || printf '%s\n' "$2" > "$TSDB_STUB_STATE/$1/ext"
    [ -z "${3:-}" ] || printf '%s\n' "$3" > "$TSDB_STUB_STATE/$1/trigger"
}

# new_case <name> <default version> — a fresh stub state; reticora, postgres
# and template1 follow with stub_db.
new_case() {
    CASE_DIR="$TMP_ROOT/case-$1"
    export TSDB_STUB_STATE="$CASE_DIR/state" TSDB_STUB_EVENTS="$CASE_DIR/events"
    mkdir -p "$TSDB_STUB_STATE"
    : > "$TSDB_STUB_EVENTS"
    printf '%s\n' "$2" > "$TSDB_STUB_STATE/default"
    printf 'postgres\ntemplate1\n' > "$TSDB_STUB_STATE/maintenance"
}

state_of() { cat "$TSDB_STUB_STATE/$1/$2" 2>/dev/null || true; }
# alters — the changing statements of the run, "<database> <statement>".
alters() { sed -n 's/^psql \([^ ]*\) \(ALTER .*\)$/\1 \2/p' "$TSDB_STUB_EVENTS"; }
# run_update [args] — the script against the psql stub; output in $OUT, rc in $RC.
run_update() {
    OUT="$(PATH="$BIN:$PATH" bash "$SCRIPT" "$@" 2>&1)"
    RC=$?
}
report() { printf 'rc=%s\n%s\nstatements:\n%s' "$RC" "$OUT" "$(sed 's/^/  /' "$TSDB_STUB_EVENTS")"; }

DISABLE="ALTER EVENT TRIGGER reticora_restrict_app_ddl DISABLE"
ENABLE="ALTER EVENT TRIGGER reticora_restrict_app_ddl ENABLE"
UPDATE="ALTER EXTENSION timescaledb UPDATE"

# ─── The stub models the defect ───────────────────────────────────────────────
new_case model 2.31.0
stub_db reticora 2.17.2 O
if ! out="$("$BIN/psql" -X -d reticora -c "$UPDATE" 2>&1)" \
    && grep -q 'already loaded with a different version' <<<"$out" && [ "$(state_of reticora ext)" = 2.17.2 ]; then
    ok "model: ALTER EXTENSION timescaledb UPDATE with the event trigger enabled fails with the FATAL of the server"
else
    fail "model: ALTER EXTENSION timescaledb UPDATE with the event trigger enabled fails with the FATAL of the server" "$out"
fi

# ─── Update from an older version ─────────────────────────────────────────────
new_case update 2.31.0
stub_db reticora 2.17.2 O
stub_db postgres 2.17.2
stub_db template1 2.17.2
run_update -- "$BIN/psql" -U reticora
want="reticora $DISABLE
reticora $UPDATE
reticora $ENABLE
postgres $UPDATE
template1 $UPDATE"
if [ "$RC" -eq 0 ] && [ "$(alters)" = "$want" ]; then
    ok "update: DISABLE, UPDATE, ENABLE in reticora, then UPDATE only in postgres and template1 (rc 0)"
else
    fail "update: DISABLE, UPDATE, ENABLE in reticora, then UPDATE only in postgres and template1 (rc 0)" "$(report)"
fi
# The stub refuses a call without -X or with more than one statement, so a
# run with rc 0 had each statement in a psql -X session of its own.
if [ "$RC" -eq 0 ] && ! grep -q -v '^psql ' "$TSDB_STUB_EVENTS"; then
    ok "update: every statement in a psql -X session of its own"
else
    fail "update: every statement in a psql -X session of its own" "$(report)"
fi
if [ "$(state_of reticora ext)" = 2.31.0 ] && [ "$(state_of postgres ext)" = 2.31.0 ] \
    && [ "$(state_of template1 ext)" = 2.31.0 ] && [ "$(state_of reticora trigger)" = O ]; then
    ok "update: all three databases at default_version 2.31.0, event trigger enabled"
else
    fail "update: all three databases at default_version 2.31.0, event trigger enabled" "$(report)"
fi

: > "$TSDB_STUB_EVENTS"
run_update -- "$BIN/psql" -U reticora
if [ "$RC" -eq 0 ] && [ -z "$(alters)" ] && [ "$(grep -c 'timescaledb 2.31.0 is current' <<<"$OUT")" -eq 3 ]; then
    ok "idempotent: a second run changes nothing (no ALTER, rc 0)"
else
    fail "idempotent: a second run changes nothing (no ALTER, rc 0)" "$(report)"
fi

# ─── Current version ──────────────────────────────────────────────────────────
new_case current 2.31.0
stub_db reticora 2.31.0 O
stub_db postgres 2.31.0
stub_db template1 2.31.0
run_update -- "$BIN/psql" -U reticora
if [ "$RC" -eq 0 ] && [ -z "$(alters)" ] && [ "$(state_of reticora trigger)" = O ]; then
    ok "current: no action at the current version (no ALTER, rc 0)"
else
    fail "current: no action at the current version (no ALTER, rc 0)" "$(report)"
fi

# Without a command the script runs psql from PATH (connection from PG*).
new_case default-psql 2.31.0
stub_db reticora 2.31.0 O
: > "$TSDB_STUB_STATE/maintenance"
run_update
if [ "$RC" -eq 0 ] && grep -q '^psql reticora SELECT' "$TSDB_STUB_EVENTS" && [ -z "$(alters)" ]; then
    ok "current: without a command the script runs psql from PATH"
else
    fail "current: without a command the script runs psql from PATH" "$(report)"
fi

# ─── Failures ─────────────────────────────────────────────────────────────────
new_case failed-update 2.31.0
stub_db reticora 2.17.2 O
stub_db postgres 2.17.2
stub_db template1 2.17.2
touch "$TSDB_STUB_STATE/fail-update"
run_update -- "$BIN/psql" -U reticora
want="reticora $DISABLE
reticora $UPDATE
reticora $ENABLE"
if [ "$RC" -eq 1 ] && [ "$(alters)" = "$want" ] && [ "$(state_of reticora trigger)" = O ] \
    && [ "$(state_of reticora ext)" = 2.17.2 ] \
    && grep -q "error: reticora: ALTER EXTENSION timescaledb UPDATE failed; timescaledb stays at 2.17.2" <<<"$OUT" \
    && grep -q "TimescaleDB extension update failed" <<<"$OUT" \
    && grep -q "event trigger reticora_restrict_app_ddl enabled again" <<<"$OUT"; then
    ok "failure: a failed update enables the event trigger again (trap) and aborts with the message (rc 1)"
else
    fail "failure: a failed update enables the event trigger again (trap) and aborts with the message (rc 1)" "$(report)"
fi
if [ "$(state_of postgres ext)" = 2.17.2 ] && [ "$(state_of template1 ext)" = 2.17.2 ]; then
    ok "failure: the run stops at the failed database"
else
    fail "failure: the run stops at the failed database" "$(report)"
fi

new_case failed-enable 2.31.0
stub_db reticora 2.17.2 O
touch "$TSDB_STUB_STATE/fail-update" "$TSDB_STUB_STATE/fail-enable"
run_update -- "$BIN/psql" -U reticora
if [ "$RC" -eq 1 ] && [ "$(alters | grep -c "^reticora $ENABLE\$")" -eq 1 ] \
    && grep -q 'the event trigger reticora_restrict_app_ddl could not be enabled again' <<<"$OUT" \
    && grep -q 'ALTER EVENT TRIGGER reticora_restrict_app_ddl ENABLE"' <<<"$OUT"; then
    ok "failure: when enabling fails as well, the run names the manual ENABLE (rc 1)"
else
    fail "failure: when enabling fails as well, the run names the manual ENABLE (rc 1)" "$(report)"
fi

new_case connected 2.31.0
stub_db reticora 2.17.2 O
printf 'reticora_runtime\nreticora_runtime\nreticora_runtime\n' > "$TSDB_STUB_STATE/reticora/sessions"
TSDB_UPDATE_SESSION_WAIT=2 run_update -- "$BIN/psql" -U reticora
if [ "$RC" -eq 1 ] && [ -z "$(alters)" ] && [ "$(state_of reticora trigger)" = O ] && [ "$(state_of reticora ext)" = 2.17.2 ] \
    && [ "$(grep -c 'FROM pg_stat_activity' "$TSDB_STUB_EVENTS")" -eq 2 ] \
    && grep -q 'error: reticora: the application is still connected (reticora_runtime); stop the Reticora server before the update' <<<"$OUT"; then
    ok "server running: the trigger is not disabled while an application login is connected (rc 1, no ALTER)"
else
    fail "server running: the trigger is not disabled while an application login is connected (rc 1, no ALTER)" "$(report)"
fi

new_case disconnecting 2.31.0
stub_db reticora 2.17.2 O
: > "$TSDB_STUB_STATE/maintenance"
printf 'reticora_runtime\n' > "$TSDB_STUB_STATE/reticora/sessions"
TSDB_UPDATE_SESSION_WAIT=3 run_update -- "$BIN/psql" -U reticora
if [ "$RC" -eq 0 ] && [ "$(alters)" = "reticora $DISABLE
reticora $UPDATE
reticora $ENABLE" ] && [ "$(state_of reticora ext)" = 2.31.0 ]; then
    ok "server running: a just-stopped server gets time to disconnect, then the update runs"
else
    fail "server running: a just-stopped server gets time to disconnect, then the update runs" "$(report)"
fi

new_case aborted-earlier 2.31.0
stub_db reticora 2.31.0 D
stub_db postgres 2.31.0
stub_db template1 2.31.0
run_update -- "$BIN/psql" -U reticora
if [ "$RC" -eq 0 ] && [ "$(alters)" = "reticora $ENABLE" ] && [ "$(state_of reticora trigger)" = O ]; then
    ok "repair: a trigger an aborted update left disabled is enabled (one ENABLE, rc 0)"
else
    fail "repair: a trigger an aborted update left disabled is enabled (one ENABLE, rc 0)" "$(report)"
fi

new_case newer 2.31.0
stub_db reticora 2.99.0 O
run_update -- "$BIN/psql" -U reticora
if [ "$RC" -eq 1 ] && [ -z "$(alters)" ] && grep -q 'timescaledb 2.99.0 is newer than the installed library (2.31.0)' <<<"$OUT"; then
    ok "newer: an extension newer than the library aborts without a change (rc 1)"
else
    fail "newer: an extension newer than the library aborts without a change (rc 1)" "$(report)"
fi

# Before the first migration there is no event trigger, or no extension.
new_case no-trigger 2.31.0
stub_db reticora 2.17.2
stub_db postgres ""
stub_db template1 2.31.0
run_update -- "$BIN/psql" -U reticora
if [ "$RC" -eq 0 ] && [ "$(alters)" = "reticora $UPDATE" ] && [ "$(state_of reticora ext)" = 2.31.0 ] \
    && grep -q 'postgres: timescaledb is not installed' <<<"$OUT"; then
    ok "fresh: without the event trigger only UPDATE, a database without the extension is skipped"
else
    fail "fresh: without the event trigger only UPDATE, a database without the extension is skipped" "$(report)"
fi

# ─── install-cloud.sh start_stack ─────────────────────────────────────────────
HARNESS="$TMP_ROOT/harness.sh"
head -n -1 install-cloud.sh > "$HARNESS"
cp install-preflight.sh "$TMP_ROOT/"

# installer_start_stack — start_stack of the installer (main stripped) with
# the stubs: compose_cmd is the installer's own and runs the docker stub;
# every step around the update is a stub that logs "step <name>".
# shellcheck disable=SC2034,SC2317 # settings and stubs used by the installer
installer_start_stack() {
    (
        set --
        # shellcheck disable=SC1090
        . "$HARNESS"
        PATH="$BIN:$PATH"
        NON_INTERACTIVE=1
        SCRIPT_DIR="$REPO_ROOT"
        COMPOSE_DIR="$CASE_DIR/compose"
        COMPOSE_FILE="$COMPOSE_DIR/docker-compose.yml"
        OVERRIDE_FILE="$COMPOSE_DIR/docker-compose.override.yml"
        ENV_FILE="$COMPOSE_DIR/.env"
        COMPOSE=(docker compose)
        DOCKER=docker
        RETICORA_TLS_DOMAIN=""
        step() { printf 'step %s\n' "$1" >> "$TSDB_STUB_EVENTS"; }
        for f in wait_for_service sync_db_password run_migrations seed_default_organization ensure_runtime_login \
            validate_keycloak_bootstrap create_initial_admin reload_frontend_after_bootstrap check_issuer_from_host \
            check_issuer_from_container show_service_logs; do
            eval "$f() { step $f; }"
        done
        start_stack
    ) > "$CASE_DIR/out" 2>&1
}

line_of() { grep -n -x -F -- "$1" "$TSDB_STUB_EVENTS" | head -n 1 | cut -d: -f1; }
first_line_of() { grep -n -E -- "$1" "$TSDB_STUB_EVENTS" | head -n 1 | cut -d: -f1; }
last_line_of() { grep -n -E -- "$1" "$TSDB_STUB_EVENTS" | tail -n 1 | cut -d: -f1; }
installer_report() { printf '%s\nevents:\n%s' "$(tail -n 12 "$CASE_DIR/out")" "$(sed 's/^/  /' "$TSDB_STUB_EVENTS")"; }

new_case installer 2.31.0
stub_db reticora 2.17.2 O
stub_db postgres 2.17.2
stub_db template1 2.17.2
mkdir -p "$CASE_DIR/compose"
if installer_start_stack; then rc=0; else rc=$?; fi
sync="$(line_of 'step sync_db_password')"
stop="$(line_of "docker compose --env-file $CASE_DIR/compose/.env -f $CASE_DIR/compose/docker-compose.yml stop server")"
first="$(first_line_of '^psql reticora ')"
last="$(last_line_of '^psql ')"
migrate="$(line_of 'step run_migrations')"
if [ "$rc" -eq 0 ] && [ -n "$sync" ] && [ -n "$stop" ] && [ -n "$first" ] && [ -n "$migrate" ] \
    && [ "$sync" -lt "$stop" ] && [ "$stop" -lt "$first" ] && [ "$last" -lt "$migrate" ]; then
    ok "installer: start_stack stops the server and updates TimescaleDB between sync_db_password and run_migrations"
else
    fail "installer: start_stack stops the server and updates TimescaleDB between sync_db_password and run_migrations" "$(installer_report)"
fi
via="docker compose --env-file $CASE_DIR/compose/.env -f $CASE_DIR/compose/docker-compose.yml exec -T postgres psql -U reticora -X "
if [ "$(grep -c '^psql ' "$TSDB_STUB_EVENTS")" -eq "$(grep -c -F -- "$via" "$TSDB_STUB_EVENTS")" ] \
    && [ "$(alters | head -n 3)" = "reticora $DISABLE
reticora $UPDATE
reticora $ENABLE" ] && [ "$(state_of reticora ext)" = 2.31.0 ] && [ "$(state_of reticora trigger)" = O ]; then
    ok "installer: the update runs as reticora through docker compose exec -T postgres psql, trigger enabled afterwards"
else
    fail "installer: the update runs as reticora through docker compose exec -T postgres psql, trigger enabled afterwards" "$(installer_report)"
fi
if ! grep -qE '^(sudo|curl|systemctl|service|apt-get) ' "$TSDB_STUB_EVENTS"; then
    ok "installer: no call outside the stubs"
else
    fail "installer: no call outside the stubs" "$(installer_report)"
fi

new_case installer-failure 2.31.0
stub_db reticora 2.17.2 O
touch "$TSDB_STUB_STATE/fail-update"
mkdir -p "$CASE_DIR/compose"
if installer_start_stack; then rc=0; else rc=$?; fi
if [ "$rc" -eq 1 ] && [ -z "$(line_of 'step run_migrations')" ] && [ "$(state_of reticora trigger)" = O ] \
    && grep -q "The TimescaleDB extension update failed — see docs/troubleshooting-installation.md, 'TimescaleDB extension update failed'" "$CASE_DIR/out"; then
    ok "installer: a failed update aborts the installation before the migrations, trigger enabled (rc 1)"
else
    fail "installer: a failed update aborts the installation before the migrations, trigger enabled (rc 1)" "$(installer_report)"
fi

# ─── PostgreSQL: update from an older extension version ───────────────────────
pg_part() {
    local url="$TSDB_UPDATE_TEST_DATABASE_URL" from="${TSDB_UPDATE_TEST_FROM_VERSION:-2.17.2}"
    local migrate_bin="${MIGRATE:-migrate}" go_bin="${GO:-go}"
    local re='^postgres(ql)?://([^:@/]+)(:([^@/]*))?@([^:/?]+)(:([0-9]+))?/([^?]*)(\?(.*))?$'
    if ! [[ "$url" =~ $re ]]; then
        fail "pg: TSDB_UPDATE_TEST_DATABASE_URL is postgres://user:password@host[:port]/database[?sslmode=…]"
        return
    fi
    local user="${BASH_REMATCH[2]}" host="${BASH_REMATCH[5]}" port="${BASH_REMATCH[7]:-5432}" query="${BASH_REMATCH[10]}"
    export PGPASSWORD="${BASH_REMATCH[4]}"
    PGSSLMODE="$(sed -n 's/.*\(^\|&\)sslmode=\([^&]*\).*/\2/p' <<<"$query")"
    export PGSSLMODE="${PGSSLMODE:-prefer}"
    local db="tsdb_update_$$"
    local pg=(psql -h "$host" -p "$port" -U "$user")
    local db_url="postgres://$user:$PGPASSWORD@$host:$port/$db?${query:-sslmode=$PGSSLMODE}"
    sql() { "${pg[@]}" -X -q -A -t -v ON_ERROR_STOP=1 -d "$1" -c "$2"; }
    for tool in psql "$migrate_bin" "$go_bin"; do
        command -v "$tool" >/dev/null || { fail "pg: $tool is required for the PostgreSQL part"; return; }
    done

    local default available out
    default="$(sql postgres "SELECT default_version FROM pg_available_extensions WHERE name = 'timescaledb'")"
    available="$(sql postgres "SELECT count(*) FROM pg_available_extension_versions WHERE name = 'timescaledb' AND version = '$from'")"
    if [ -z "$default" ] || [ "$available" != 1 ] || [ "$from" = "$default" ]; then
        fail "pg: the server offers timescaledb $from below its default version (default: ${default:-none})"
        return
    fi
    PG_CLEANUP="$(printf '%q ' "${pg[@]}") -X -q -d postgres -c 'DROP DATABASE IF EXISTS $db WITH (FORCE)' >/dev/null 2>&1"
    if out="$(sql postgres "CREATE DATABASE $db TEMPLATE template0" 2>&1 &&
        sql "$db" "CREATE EXTENSION timescaledb VERSION '$from'" 2>&1 &&
        "$migrate_bin" -path "$REPO_ROOT/backend/migrations" -database "$db_url" up 2>&1)" \
        && [ "$(sql "$db" "SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'")" = "$from" ]; then
        ok "pg: a database with timescaledb $from, migrated to the newest migration"
    else
        fail "pg: a database with timescaledb $from, migrated to the newest migration" "$out"
        return
    fi

    out="$(sql "$db" "ALTER EXTENSION timescaledb UPDATE" 2>&1)"
    if [ "$(sql "$db" "SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'")" = "$from" ] \
        && grep -q 'already loaded with a different version' <<<"$out"; then
        ok "pg: without the script ALTER EXTENSION timescaledb UPDATE fails (event trigger loads $from)"
    else
        fail "pg: without the script ALTER EXTENSION timescaledb UPDATE fails (event trigger loads $from)" "$out"
    fi

    # A login of reticora_app holds a session, as a running server would.
    local app="${db}_app" app_pw holder rc
    app_pw="$(openssl rand -hex 16)"
    PG_CLEANUP="$PG_CLEANUP; $(printf '%q ' "${pg[@]}") -X -q -d postgres -c 'DROP ROLE IF EXISTS $app' >/dev/null 2>&1"
    sql postgres "CREATE ROLE $app LOGIN PASSWORD '$app_pw' IN ROLE reticora_app" >/dev/null
    PGPASSWORD="$app_pw" psql -h "$host" -p "$port" -U "$app" -d "$db" -X -q -c 'SELECT pg_sleep(60)' >/dev/null 2>&1 &
    holder=$!
    for _ in $(seq 1 50); do
        [ "$(sql "$db" "SELECT count(*) FROM pg_stat_activity WHERE usename = '$app'")" = 1 ] && break
        sleep 0.1
    done
    out="$(TSDB_UPDATE_SESSION_WAIT=2 bash "$SCRIPT" --database "$db" -- "${pg[@]}" 2>&1)"
    rc=$?
    if [ "$rc" -eq 1 ] && grep -q "the application is still connected ($app)" <<<"$out" \
        && [ "$(sql "$db" "SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'")" = "$from" ] \
        && [ "$(sql "$db" "SELECT evtenabled FROM pg_event_trigger WHERE evtname = 'reticora_restrict_app_ddl'")" = O ]; then
        ok "pg: while a login of reticora_app is connected the script refuses, trigger enabled, $from kept"
    else
        fail "pg: while a login of reticora_app is connected the script refuses, trigger enabled, $from kept" "rc=$rc
$out"
    fi
    sql postgres "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = '$app'" >/dev/null
    wait "$holder" 2>/dev/null

    out="$(bash "$SCRIPT" --database "$db" -- "${pg[@]}" 2>&1)"
    rc=$?
    if [ "$rc" -eq 0 ] && [ "$(sql "$db" "SELECT extversion = '$default' FROM pg_extension WHERE extname = 'timescaledb'")" = t ]; then
        ok "pg: the script raises timescaledb $from to default_version $default"
    else
        fail "pg: the script raises timescaledb $from to default_version $default" "rc=$rc
$out"
    fi
    out="$(sql "$db" "SET ROLE reticora_app; CREATE TABLE wp604_ddl_probe (id int)" 2>&1)"
    if [ "$(sql "$db" "SELECT evtenabled FROM pg_event_trigger WHERE evtname = 'reticora_restrict_app_ddl'")" = O ] \
        && grep -q 'role reticora_app may only run index DDL, not CREATE TABLE' <<<"$out"; then
        ok "pg: the event trigger is enabled and refuses DDL of reticora_app again"
    else
        fail "pg: the event trigger is enabled and refuses DDL of reticora_app again" "$out"
    fi

    out="$(bash "$SCRIPT" --database "$db" -- "${pg[@]}" 2>&1)"
    rc=$?
    if [ "$rc" -eq 0 ] && grep -q "$db: timescaledb $default is current" <<<"$out" && ! grep -qE 'updating|enabl' <<<"$out"; then
        ok "pg: a second run changes nothing"
    else
        fail "pg: a second run changes nothing" "rc=$rc
$out"
    fi

    if out="$("$migrate_bin" -path "$REPO_ROOT/backend/migrations" -database "$db_url" up 2>&1)" && grep -q 'no change' <<<"$out" \
        && out="$(cd "$REPO_ROOT/backend" && TEST_DATABASE_URL="$db_url" "$go_bin" test -count=1 \
            -run '^TestSchemaBaselineMatchesMigratedSchema$' ./internal/database/ 2>&1)"; then
        ok "pg: after the update the migrations are complete and TestSchemaBaselineMatchesMigratedSchema passes"
    else
        fail "pg: after the update the migrations are complete and TestSchemaBaselineMatchesMigratedSchema passes" "$out"
    fi
}

if [ -n "${TSDB_UPDATE_TEST_DATABASE_URL:-}" ]; then
    pg_part
else
    echo "skip PostgreSQL part: TSDB_UPDATE_TEST_DATABASE_URL is not set"
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
