#!/usr/bin/env bash
#
# restore-pitr.sh — restore proof for the pgBackRest chain (WP-101, WP-445:
# NFR-05, OPS-04). Starts postgres and backup from deploy/docker-compose in a
# throwaway project, writes a workload after the first base backup and
# simulates two disasters:
#
#   1. Logical error (point in time): the operator archives the current WAL
#      (pgbackrest check) and restores to a target time between two
#      workload phases. Expected: every row before the target, none after.
#   2. Loss of the data volume during continuous writes (no chance to
#      archive): restore the latest archived state. Measured: data loss
#      (RPO) = last committed row before the disaster minus last restored
#      row. The writes run RESTORE_WRITE_SECONDS (default 1050 s), longer
#      than the RPO limit of NFR-05: a chain that archives too rarely loses
#      more than 15 min of them and fails (F-130).
#
# Both restores measure the restore duration (RTO): wipe of the data
# directory, restore, WAL replay and promotion.
#
# Fails when the point in time restore is not exact, archiving does not work
# after the restore, the RTO exceeds RETICORA_RTO_LIMIT_SECONDS (14400 = 4 h)
# or the RPO exceeds one of two limits:
#   - the archive cycle: archive_timeout of docker-compose.yml plus
#     RETICORA_RPO_MARGIN_SECONDS (300 + 60 s), since the WAL is archived at
#     the latest every archive_timeout;
#   - RETICORA_RPO_LIMIT_SECONDS (900 = 15 min, NFR-05).
# The protocol (RESTORE_REPORT, default restore-pitr-report.md) has one line
# "RPO … PASS|FAIL" with both limits.
#
# RESTORE_ARCHIVE_TIMEOUT runs the database with another archive_timeout;
# the limit stays that of docker-compose.yml. The negative self-test (job
# rpo-negative of restore-test.yml) runs with 1200 s and must fail: the
# default write window ends before that archive_timeout, so nothing of it
# is archived.
# RESTORE_SEED_OBJECTS=<n> applies the migrations (migrate from PATH or
# RESTORE_MIGRATE_BIN) and seeds the demo organization with n CIs
# (RESTORE_SEED_BIN, built from backend/cmd/seed) before the first base
# backup: the RTO of a representative database (F-292).
# RETICORA_POSTGRES_IMAGE selects a prebuilt image; otherwise the image is
# built from deploy/docker-compose/postgres.
#
# Sourced (tests/restore-pitr-config.test.sh), it only defines its settings
# and functions.
#
# Usage: ./tests/restore-pitr.sh

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$ROOT/deploy/docker-compose/docker-compose.yml"

RPO_LIMIT="${RETICORA_RPO_LIMIT_SECONDS:-900}"
RPO_MARGIN="${RETICORA_RPO_MARGIN_SECONDS:-60}"
RTO_LIMIT="${RETICORA_RTO_LIMIT_SECONDS:-14400}"
REPORT="${RESTORE_REPORT:-$ROOT/restore-pitr-report.md}"
ROWS_A="${RESTORE_ROWS:-50000}"
WRITE_SECONDS="${RESTORE_WRITE_SECONDS:-1050}"
TEST_ARCHIVE_TIMEOUT="${RESTORE_ARCHIVE_TIMEOUT:-}"
SEED_OBJECTS="${RESTORE_SEED_OBJECTS:-0}"
SEED_BIN="${RESTORE_SEED_BIN:-}"
MIGRATE_BIN="${RESTORE_MIGRATE_BIN:-migrate}"
PROJECT="reticora-pitr-$$"
FAILED=0

log() { printf '%s %s\n' "$(date -u '+%H:%M:%S')" "$*"; }

# pg_command: the command of the postgres service in COMPOSE_FILE, one
# argument per line.
pg_command() {
    awk '$0 == "  postgres:" { s = 1; next }
         s && /^  [^ #]/ { exit }
         s && /^    command:/ { c = 1; next }
         c && /^      - / { sub(/^      - /, ""); print; next }
         c { exit }' "$COMPOSE_FILE"
}

# archive_timeout_of: archive_timeout of the postgres command in seconds;
# empty when it is not set there in seconds.
archive_timeout_of() {
    pg_command | sed -n 's/^archive_timeout=\([0-9][0-9]*\)$/\1/p'
}

# write_override <file> <archive_timeout or ""> <host port 0|1>: the Compose
# override of the throwaway project. No host port, so that the database
# cannot collide with a running stack, unless the seed needs one (loopback,
# random port). With an archive_timeout, the postgres command of
# docker-compose.yml with that value (the arguments as written there, so
# Compose interpolates them alike; only quoted for YAML).
write_override() {
    local arg sep=""
    {
        printf 'services:\n  postgres:\n'
        if [ "$3" = 1 ]; then
            printf '    ports: !override ["127.0.0.1::5432"]\n'
        else
            printf '    ports: !reset []\n'
        fi
        if [ -n "$2" ]; then
            printf '    command: ['
            while IFS= read -r arg; do
                case "$arg" in archive_timeout=*) arg="archive_timeout=$2" ;; esac
                arg="${arg//\\/\\\\}"
                arg="${arg//\"/\\\"}"
                printf '%s"%s"' "$sep" "$arg"
                sep=", "
            done < <(pg_command)
            printf ']\n'
        fi
    } > "$1"
}

# docker-compose.override.yml carries the build: of the postgres image.
compose() {
    docker compose -p "$PROJECT" --env-file "$ENV_FILE" \
        -f "$COMPOSE_FILE" \
        -f "$ROOT/deploy/docker-compose/docker-compose.override.yml" -f "$OVERRIDE" "$@"
}
# shellcheck disable=SC2317,SC2329 # invoked by the EXIT trap
cleanup() {
    compose down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$WORK"
}

sql() { compose exec -T postgres psql -U reticora -d reticora -v ON_ERROR_STOP=1 -tAq -c "$1"; }

wait_ready() { # waits until PostgreSQL accepts writes (recovery finished)
    local deadline=$((SECONDS + RTO_LIMIT))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if [ "$(sql 'SELECT pg_is_in_recovery()' 2>/dev/null || true)" = "f" ]; then return 0; fi
        sleep 1
    done
    return 1
}

wait_healthy() { # <service>
    local cid status deadline=$((SECONDS + 600))
    while [ "$SECONDS" -lt "$deadline" ]; do
        cid="$(compose ps -q "$1")"
        status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || true)"
        [ "$status" = "healthy" ] && return 0
        sleep 2
    done
    compose logs --tail 40 "$1" >&2
    return 1
}

# restore <kill|stop> <pgbackrest restore options …> — stops PostgreSQL
# (kill: crash, stop: orderly) and restores; prints the duration in seconds
# (data volume wiped, restore, replay, promotion).
restore() {
    local how="$1"; shift
    compose stop backup >/dev/null 2>&1
    compose "$how" postgres >/dev/null 2>&1
    local start=$SECONDS
    compose run --rm --no-deps --user postgres --entrypoint reticora-pgbackrest-env.sh postgres \
        sh -c 'find /var/lib/postgresql/data -mindepth 1 -delete && pgbackrest --stanza=reticora "$@" restore' _ "$@" >&2
    compose start postgres >/dev/null 2>&1
    wait_ready || { compose logs --tail 40 postgres >&2; return 1; }
    printf '%s' $((SECONDS - start))
}

result() { # <check> <measured> <limit> <ok 0|1>
    local verdict=PASS
    [ "$4" -eq 0 ] || { verdict=FAIL; FAILED=1; }
    printf '| %s | %s | %s | %s |\n' "$1" "$2" "$3" "$verdict" >> "$REPORT"
    log "$verdict $1: $2 (limit $3)"
}

# rpo_result <rpo s> <lost rows> <committed rows> <archive_timeout s>: the
# report line "RPO … PASS|FAIL" against both limits, the archive cycle
# (archive_timeout + RPO_MARGIN) and NFR-05 (RPO_LIMIT). An RPO that is not
# a measured number of seconds fails.
rpo_result() {
    local rpo="$1" window=$(($4 + RPO_MARGIN)) above="" measured
    if [[ "$rpo" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
        if awk -v r="$rpo" -v l="$window" 'BEGIN { exit !(r > l) }'; then above="${window}s"; fi
        if awk -v r="$rpo" -v l="$RPO_LIMIT" 'BEGIN { exit !(r > l) }'; then above="${above:+$above and }${RPO_LIMIT}s"; fi
        measured="${rpo}s${above:+, above $above} ($2 of $3 rows lost)"
    else
        measured="not measured ('$rpo')"
        above=unmeasured
    fi
    result "RPO: data loss after losing the data volume (latest restore)" "$measured" \
        "${window}s (archive_timeout ${4}s + ${RPO_MARGIN}s), ${RPO_LIMIT}s (NFR-05)" "$([ -z "$above" ] && echo 0 || echo 1)"
}

main() {
    cd "$ROOT" || exit 1
    local archive_timeout running v url port size target rto_pitr phase_a later deadline last_commit committed rto restored_last restored rpo data

    archive_timeout="$(archive_timeout_of)"
    if [ -z "$archive_timeout" ]; then
        echo "restore-pitr.sh: no archive_timeout=<seconds> in the postgres command of $COMPOSE_FILE" >&2
        exit 2
    fi
    for v in "$RPO_LIMIT" "$RPO_MARGIN" "$RTO_LIMIT" "$ROWS_A" "$WRITE_SECONDS" "$SEED_OBJECTS" ${TEST_ARCHIVE_TIMEOUT:+"$TEST_ARCHIVE_TIMEOUT"}; do
        [[ "$v" =~ ^[0-9]+$ ]] || { echo "restore-pitr.sh: '$v' is not a whole number" >&2; exit 2; }
    done
    if [ "$SEED_OBJECTS" -gt 0 ]; then
        [ -x "$SEED_BIN" ] || { echo "restore-pitr.sh: RESTORE_SEED_OBJECTS needs RESTORE_SEED_BIN (go build ./cmd/seed in backend/)" >&2; exit 2; }
        command -v "$MIGRATE_BIN" >/dev/null || { echo "restore-pitr.sh: RESTORE_SEED_OBJECTS needs golang-migrate (migrate in PATH or RESTORE_MIGRATE_BIN)" >&2; exit 2; }
    fi

    WORK="$(mktemp -d)"
    ENV_FILE="$WORK/pitr.env"
    OVERRIDE="$WORK/override.yml"
    DB_PASSWORD="$(openssl rand -hex 12)"
    # The daily backup stays 12 h away: its WAL switch would archive the
    # write window early and hide the archive cadence the RPO check measures.
    cat > "$ENV_FILE" <<ENV
RETICORA_DB_PASSWORD=$DB_PASSWORD
RETICORA_DB_APP_PASSWORD=unused
RETICORA_BACKUP_CIPHER_PASS=$(openssl rand -hex 24)
RETICORA_BACKUP_HOUR=$(((10#$(date -u +%H) + 12) % 24))
RETICORA_S3_ACCESS_KEY=unused
RETICORA_S3_SECRET_KEY=unused
RETICORA_MASTER_KEY=unused
KEYCLOAK_DB_PASSWORD=unused
KEYCLOAK_ADMIN_PASSWORD=unused
ENV
    [ -n "${RETICORA_POSTGRES_IMAGE:-}" ] && printf 'RETICORA_POSTGRES_IMAGE=%s\n' "$RETICORA_POSTGRES_IMAGE" >> "$ENV_FILE"
    write_override "$OVERRIDE" "$TEST_ARCHIVE_TIMEOUT" "$([ "$SEED_OBJECTS" -gt 0 ] && echo 1 || echo 0)"
    trap cleanup EXIT

    data="pitr_probe workload only"
    [ "$SEED_OBJECTS" -eq 0 ] || data="demo organization with $SEED_OBJECTS CIs (seed -objects $SEED_OBJECTS) and the pitr_probe workload"
    {
        printf '# PITR restore proof (NFR-05, OPS-04)\n\n'
        printf -- '- Date: %s\n- Commit: %s\n' "$(date -u '+%Y-%m-%d %H:%M:%S UTC')" "$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
        printf -- '- Limits: RPO %ss (archive_timeout %ss of docker-compose.yml + %ss) and %ss (NFR-05), RTO %ss\n' \
            "$((archive_timeout + RPO_MARGIN))" "$archive_timeout" "$RPO_MARGIN" "$RPO_LIMIT" "$RTO_LIMIT"
        printf -- '- Database under test: archive_timeout %ss%s, write window %ss, data: %s\n\n' "${TEST_ARCHIVE_TIMEOUT:-$archive_timeout}" \
            "${TEST_ARCHIVE_TIMEOUT:+ (RESTORE_ARCHIVE_TIMEOUT, negative self-test)}" "$WRITE_SECONDS" "$data"
        printf '| Check | Measured | Limit | Result |\n|---|---|---|---|\n'
    } > "$REPORT"

    log "starting postgres and backup ($PROJECT)"
    if [ -z "${RETICORA_POSTGRES_IMAGE:-}" ]; then compose build postgres >&2; fi
    compose up -d --no-build postgres >&2
    # Over TCP: the temporary server of the first initialisation listens on
    # the socket only.
    until compose exec -T postgres pg_isready -q -h 127.0.0.1 -U reticora >/dev/null 2>&1; do sleep 1; done
    running="$(sql "SELECT setting FROM pg_settings WHERE name = 'archive_timeout'")"
    if [ "$running" != "${TEST_ARCHIVE_TIMEOUT:-$archive_timeout}" ]; then
        log "the database runs with archive_timeout ${running}s, expected ${TEST_ARCHIVE_TIMEOUT:-$archive_timeout}s"
        exit 2
    fi
    if [ "$SEED_OBJECTS" -gt 0 ]; then
        port="$(compose port postgres 5432)"
        url="postgres://reticora@127.0.0.1:${port##*:}/reticora?sslmode=disable"
        log "migrations and seed -objects $SEED_OBJECTS"
        PGPASSWORD="$DB_PASSWORD" "$MIGRATE_BIN" -path "$ROOT/backend/migrations" -database "$url" up >&2
        PGPASSWORD="$DB_PASSWORD" "$SEED_BIN" -database-url "$url" -objects "$SEED_OBJECTS" >&2
    fi
    sql "CREATE TABLE pitr_probe (id bigserial PRIMARY KEY, phase text NOT NULL,
         at timestamptz NOT NULL DEFAULT clock_timestamp(), payload text)"
    sql "INSERT INTO pitr_probe (phase) VALUES ('base')"
    size="$(sql "SELECT round(pg_database_size(current_database()) / 1048576.0)")"
    compose up -d --no-build backup >&2
    wait_healthy backup
    log "first base backup taken (database ${size} MB)"

    # Scenario 1: logical error. Workload after the base backup — only the WAL
    # archive can bring it back.
    sql "INSERT INTO pitr_probe (phase, payload) SELECT 'A', md5(g::text) FROM generate_series(1, $ROWS_A) g"
    target="$(sql "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US+00')")"
    sleep 2
    sql "INSERT INTO pitr_probe (phase, payload) SELECT 'B', md5(g::text) FROM generate_series(1, 1000) g"
    log "phases A and B committed, target time $target"
    # The operator archives the current WAL before stopping the database.
    compose exec -T backup reticora-pgbackrest-env.sh pgbackrest --stanza=reticora check >&2
    rto_pitr="$(restore stop --type=time "--target=$target" --target-action=promote)"
    phase_a="$(sql "SELECT count(*) FROM pitr_probe WHERE phase = 'A'")"
    later="$(sql "SELECT count(*) FROM pitr_probe WHERE phase = 'B'")"
    result "PITR to $target: rows before the target" "$phase_a" "$ROWS_A" "$([ "$phase_a" -eq "$ROWS_A" ] && echo 0 || echo 1)"
    result "PITR to $target: rows after the target" "$later" "0" "$([ "$later" -eq 0 ] && echo 0 || echo 1)"
    result "RTO: point in time restore" "${rto_pitr}s" "${RTO_LIMIT}s" "$([ "$rto_pitr" -le "$RTO_LIMIT" ] && echo 0 || echo 1)"

    # Scenario 2: the data volume is lost during continuous writes; restore
    # the latest archived state (following the new timeline).
    compose start backup >/dev/null 2>&1
    log "writing for ${WRITE_SECONDS}s"
    deadline=$((SECONDS + WRITE_SECONDS))
    while [ "$SECONDS" -lt "$deadline" ]; do
        sql "INSERT INTO pitr_probe (phase, payload) SELECT 'C', md5(g::text) FROM generate_series(1, 50) g"
        sleep 1
    done
    last_commit="$(sql "SELECT extract(epoch FROM max(at)) FROM pitr_probe")"
    committed="$(sql "SELECT count(*) FROM pitr_probe")"
    rto="$(restore kill --type=default)"
    restored_last="$(sql "SELECT extract(epoch FROM max(at)) FROM pitr_probe")"
    restored="$(sql "SELECT count(*) FROM pitr_probe")"
    rpo="$(awk -v a="$last_commit" -v b="$restored_last" 'BEGIN { printf "%.1f", a - b }')"
    rpo_result "$rpo" "$((committed - restored))" "$committed" "$archive_timeout"
    result "RTO: restore of the latest state" "${rto}s" "${RTO_LIMIT}s" "$([ "$rto" -le "$RTO_LIMIT" ] && echo 0 || echo 1)"

    # After the restore the chain continues: archiving works on the new timeline.
    compose start backup >/dev/null 2>&1
    if compose exec -T backup reticora-pgbackrest-env.sh pgbackrest --stanza=reticora check >&2; then
        result "WAL archiving after the restore" "pgbackrest check ok" "ok" 0
    else
        result "WAL archiving after the restore" "pgbackrest check failed" "ok" 1
    fi

    {
        printf '\n- Database size at the first base backup: %s MB\n' "$size"
        if [ "$FAILED" -eq 0 ]; then
            printf '\n**Result:** passed\n'
        else
            printf '\n**Result:** FAILED\n'
        fi
    } >> "$REPORT"
    log "protocol: $REPORT"
    exit "$FAILED"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    set -euo pipefail
    main "$@"
fi
