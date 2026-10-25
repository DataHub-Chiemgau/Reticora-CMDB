#!/usr/bin/env bash
#
# timescaledb-update.sh — raises the TimescaleDB extension of an existing
# Reticora database to the version of the installed library (WP-604, INS-05).
#
# Since E-96 the database image ships the TimescaleDB libraries 2.13.0 to
# 2.31.0; a database keeps the extension version it was created with until
# ALTER EXTENSION timescaledb UPDATE raises it. In the Reticora database the
# event trigger reticora_restrict_app_ddl (migration 000078) runs before that
# statement and loads the old library, and the session aborts with
#   FATAL: "timescaledb" already loaded with a different version
# The script therefore runs, as the bootstrap superuser and each statement in
# a psql -X session of its own:
#   1. reads extversion and default_version of timescaledb;
#   2. when the extension is older: disables the event trigger, runs
#      ALTER EXTENSION timescaledb UPDATE and enables the trigger again — an
#      EXIT trap enables it also when the update fails, which then aborts
#      with a message; it refuses while a login of the application roles is
#      connected (the server must be stopped);
#   3. enables the trigger when an aborted earlier update left it disabled,
#      and checks that it is enabled;
#   4. updates the extension in postgres and template1 (no event trigger).
# A database whose extension is current is not changed, so a second run
# changes nothing. Stop the Reticora server first: while the trigger is
# disabled, the application roles are not restricted to index DDL (CH19).
#
# Usage:
#   scripts/ops/timescaledb-update.sh [--database NAME] [--] [PSQL COMMAND ...]
#
#   --database NAME  the Reticora database (default: reticora)
#   TSDB_UPDATE_SESSION_WAIT  seconds a stopped server gets to close its
#                    database sessions before the update refuses (default 15)
#   PSQL COMMAND     runs psql as the bootstrap superuser, with the connection
#                    but without a database; the script appends -X, its
#                    output options, -d <database> and -c <statement>.
#                    Default: psql (connection from PGHOST, PGPORT, PGUSER,
#                    PGPASSWORD, PGSSLMODE).
#
# Examples:
#   # Docker Compose (install-cloud.sh runs this step itself)
#   scripts/ops/timescaledb-update.sh -- docker compose --env-file deploy/docker-compose/.env \
#       -f deploy/docker-compose/docker-compose.yml exec -T postgres psql -U reticora
#   # External database (Kubernetes), before the rollout
#   PGPASSWORD=… PGSSLMODE=require scripts/ops/timescaledb-update.sh -- psql -h db.example.org -U reticora
set -euo pipefail

EXTENSION="timescaledb"
EVENT_TRIGGER="reticora_restrict_app_ddl"
MAINTENANCE_DATABASES="'postgres', 'template1'"
TROUBLESHOOTING="docs/troubleshooting-installation.md, 'TimescaleDB extension update failed'"
DATABASE="reticora"

usage() {
    sed -n '/^# Usage:/,/^set -euo pipefail/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//'
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --database)
            [ "$#" -ge 2 ] || { echo "--database requires a database name" >&2; exit 2; }
            DATABASE="$2"
            shift 2
            ;;
        --database=*) DATABASE="${1#*=}"; shift ;;
        -h|--help) usage; exit 0 ;;
        --) shift; break ;;
        -*) echo "Unknown option: $1 (see --help)" >&2; exit 2 ;;
        *) break ;;
    esac
done
[ -n "$DATABASE" ] || { echo "--database requires a database name" >&2; exit 2; }
[ "$#" -gt 0 ] || set -- psql
PSQL=("$@")

log() { printf 'timescaledb-update: %s\n' "$*"; }
die() { printf 'timescaledb-update: error: %s\n' "$*" >&2; exit 1; }

# run_sql <database> <statement> — one statement in a session of its own:
# -X skips any psqlrc, so the statement is the first of the session. stdin is
# closed, so a compose exec cannot read the caller's input.
run_sql() {
    "${PSQL[@]}" -X -q -A -t -v ON_ERROR_STOP=1 -d "$1" -c "$2" </dev/null
}

# extension_versions <database> — "<extversion> <default_version>" of the
# extension, empty when it is not installed in <database>.
extension_versions() {
    run_sql "$1" "SELECT e.extversion || ' ' || a.default_version FROM pg_extension e JOIN pg_available_extensions a ON a.name = e.extname WHERE e.extname = '$EXTENSION'"
}

# trigger_state — evtenabled of the event trigger (O enabled, D disabled, R
# replica only, A always), empty when it does not exist (migrations not
# applied yet).
trigger_state() {
    run_sql "$DATABASE" "SELECT evtenabled FROM pg_event_trigger WHERE evtname = '$EVENT_TRIGGER'"
}

# app_sessions — the application logins (members of reticora_app or
# reticora_owner without SUPERUSER) with a session in the Reticora database,
# empty when none is connected.
app_sessions() {
    run_sql "$DATABASE" "SELECT string_agg(DISTINCT r.rolname::text, ', ' ORDER BY r.rolname::text) FROM pg_stat_activity a JOIN pg_roles r ON r.oid = a.usesysid WHERE a.datname = current_database() AND a.pid <> pg_backend_pid() AND NOT r.rolsuper AND EXISTS (SELECT 1 FROM pg_roles g WHERE g.rolname IN ('reticora_app', 'reticora_owner') AND pg_has_role(r.oid, g.oid, 'MEMBER'))"
}

# require_application_stopped <current version> — while the trigger is
# disabled the application roles may run any DDL, so it is only disabled when
# none of them is connected. A server stopped just before gets
# TSDB_UPDATE_SESSION_WAIT seconds (default 15) to close its connections.
require_application_stopped() {
    local sessions attempt wait="${TSDB_UPDATE_SESSION_WAIT:-15}"
    for ((attempt = 1; ; attempt++)); do
        sessions="$(app_sessions)" || die "$DATABASE: cannot list the sessions of the application roles"
        [ -n "$sessions" ] || return 0
        [ "$attempt" -lt "$wait" ] || break
        sleep 1
    done
    die "$DATABASE: the application is still connected ($sessions); stop the Reticora server before the update — $EXTENSION stays at $1"
}

# older <a> <b> — version a is lower than version b.
older() {
    [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n 1)" = "$1" ]
}

# The trigger is disabled only around the update; whatever ends the script
# in between (a failed statement, a signal) enables it again.
TRIGGER_DISABLED=0
enable_trigger_on_exit() {
    local rc=$?
    trap - EXIT
    if [ "$TRIGGER_DISABLED" -eq 1 ]; then
        if run_sql "$DATABASE" "ALTER EVENT TRIGGER $EVENT_TRIGGER ENABLE" >/dev/null && [ "$(trigger_state)" = O ]; then
            log "$DATABASE: event trigger $EVENT_TRIGGER enabled again"
        else
            printf 'timescaledb-update: error: %s: the event trigger %s could not be enabled again; run as the bootstrap superuser: psql -X -d %s -c "ALTER EVENT TRIGGER %s ENABLE"\n' \
                "$DATABASE" "$EVENT_TRIGGER" "$DATABASE" "$EVENT_TRIGGER" >&2
            [ "$rc" -ne 0 ] || rc=1
        fi
    fi
    exit "$rc"
}
trap enable_trigger_on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# enable_trigger — enables the event trigger and checks that it is.
enable_trigger() {
    run_sql "$DATABASE" "ALTER EVENT TRIGGER $EVENT_TRIGGER ENABLE" >/dev/null \
        || die "$DATABASE: ALTER EVENT TRIGGER $EVENT_TRIGGER ENABLE failed"
    [ "$(trigger_state)" = O ] || die "$DATABASE: the event trigger $EVENT_TRIGGER is still not enabled"
    TRIGGER_DISABLED=0
}

# update_extension <database> <with event trigger: 0|1>
update_extension() {
    local db="$1" with_trigger="$2" versions current default trigger=""
    versions="$(extension_versions "$db")" || die "$db: cannot read the version of the $EXTENSION extension"
    if [ -z "$versions" ]; then
        log "$db: $EXTENSION is not installed, nothing to update"
        return 0
    fi
    read -r current default <<<"$versions"
    if [ "$current" = "$default" ]; then
        log "$db: $EXTENSION $current is current"
        return 0
    fi
    older "$current" "$default" \
        || die "$db: $EXTENSION $current is newer than the installed library ($default); this database image cannot serve it — use an image with $EXTENSION $current or newer"

    log "$db: updating $EXTENSION $current -> $default"
    if [ "$with_trigger" -eq 1 ]; then
        trigger="$(trigger_state)" || die "$db: cannot read the state of the event trigger $EVENT_TRIGGER"
    fi
    if [ -n "$trigger" ]; then
        require_application_stopped "$current"
        # Set before the statement: should it take effect and still report a
        # failure, the trap enables the trigger all the same.
        TRIGGER_DISABLED=1
        run_sql "$db" "ALTER EVENT TRIGGER $EVENT_TRIGGER DISABLE" >/dev/null \
            || die "$db: ALTER EVENT TRIGGER $EVENT_TRIGGER DISABLE failed (the bootstrap superuser is required); $EXTENSION stays at $current"
    fi
    run_sql "$db" "ALTER EXTENSION $EXTENSION UPDATE" >/dev/null \
        || die "$db: ALTER EXTENSION $EXTENSION UPDATE failed; $EXTENSION stays at $current ($TROUBLESHOOTING)"
    [ -z "$trigger" ] || enable_trigger
    versions="$(extension_versions "$db")" || die "$db: cannot read the version of the $EXTENSION extension after the update"
    [ "${versions%% *}" = "$default" ] \
        || die "$db: $EXTENSION is at ${versions%% *} after the update, expected $default ($TROUBLESHOOTING)"
    log "$db: $EXTENSION updated to $default"
}

# ensure_trigger_enabled — the event trigger exists only once the migrations
# ran; when it exists it must be enabled (deny-by-default, CH19).
ensure_trigger_enabled() {
    local state
    state="$(trigger_state)" || die "$DATABASE: cannot read the state of the event trigger $EVENT_TRIGGER"
    case "$state" in
        "") log "$DATABASE: event trigger $EVENT_TRIGGER does not exist yet (the migrations create it)" ;;
        O|A) ;;
        *)
            log "$DATABASE: event trigger $EVENT_TRIGGER is not enabled (state $state, an aborted update?); enabling it"
            enable_trigger
            ;;
    esac
}

update_extension "$DATABASE" 1
ensure_trigger_enabled

maintenance="$(run_sql "$DATABASE" "SELECT datname FROM pg_database WHERE datname IN ($MAINTENANCE_DATABASES) AND datallowconn ORDER BY datname")" \
    || die "$DATABASE: cannot list the databases"
for db in $maintenance; do
    [ "$db" = "$DATABASE" ] || update_extension "$db" 0
done
