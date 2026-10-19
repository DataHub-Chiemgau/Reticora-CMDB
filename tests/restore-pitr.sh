#!/usr/bin/env bash
#
# restore-pitr.sh — restore proof for the pgBackRest chain (WP-101: NFR-05,
# OPS-04). Starts postgres and backup from deploy/docker-compose in a
# throwaway project, writes a workload after the first base backup and
# simulates two disasters:
#
#   1. Logical error (point in time): the operator archives the current WAL
#      (pgbackrest check) and restores to a target time between two
#      workload phases. Expected: every row before the target, none after.
#   2. Loss of the data volume during continuous writes (no chance to
#      archive): restore the latest archived state. Measured: data loss
#      (RPO) = last committed row before the disaster minus last restored
#      row. The writes run RESTORE_WRITE_SECONDS (default 420 s, longer than
#      archive_timeout = 300 s), so the measurement covers a full archive
#      cycle.
#
# Both restores measure the restore duration (RTO): wipe of the data
# directory, restore, WAL replay and promotion.
#
# Fails when the RPO exceeds RETICORA_RPO_LIMIT_SECONDS (900 = 15 min),
# the RTO exceeds RETICORA_RTO_LIMIT_SECONDS (14400 = 4 h) or the point in
# time restore is not exact. Writes the protocol to RESTORE_REPORT
# (default restore-pitr-report.md).
#
# RETICORA_POSTGRES_IMAGE selects a prebuilt image; otherwise the image is
# built from deploy/docker-compose/postgres.
#
# Usage: ./tests/restore-pitr.sh
set -euo pipefail

cd "$(dirname "$0")/.." || exit 1
ROOT="$(pwd)"

RPO_LIMIT="${RETICORA_RPO_LIMIT_SECONDS:-900}"
RTO_LIMIT="${RETICORA_RTO_LIMIT_SECONDS:-14400}"
REPORT="${RESTORE_REPORT:-$ROOT/restore-pitr-report.md}"
ROWS_A="${RESTORE_ROWS:-50000}"
WRITE_SECONDS="${RESTORE_WRITE_SECONDS:-420}"
PROJECT="reticora-pitr-$$"

WORK="$(mktemp -d)"
ENV_FILE="$WORK/pitr.env"
OVERRIDE="$WORK/override.yml"
cat > "$ENV_FILE" <<ENV
RETICORA_DB_PASSWORD=$(openssl rand -hex 12)
RETICORA_DB_APP_PASSWORD=unused
RETICORA_BACKUP_CIPHER_PASS=$(openssl rand -hex 24)
RETICORA_S3_ACCESS_KEY=unused
RETICORA_S3_SECRET_KEY=unused
RETICORA_MASTER_KEY=unused
KEYCLOAK_DB_PASSWORD=unused
KEYCLOAK_ADMIN_PASSWORD=unused
ENV
[ -n "${RETICORA_POSTGRES_IMAGE:-}" ] && printf 'RETICORA_POSTGRES_IMAGE=%s\n' "$RETICORA_POSTGRES_IMAGE" >> "$ENV_FILE"
# No host port: the throwaway database must not collide with a running stack.
cat > "$OVERRIDE" <<'YML'
services:
  postgres:
    ports: !reset []
YML

compose() {
    docker compose -p "$PROJECT" --env-file "$ENV_FILE" \
        -f "$ROOT/deploy/docker-compose/docker-compose.yml" -f "$OVERRIDE" "$@"
}
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
    compose down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$WORK"
}
trap cleanup EXIT

log() { printf '%s %s\n' "$(date -u '+%H:%M:%S')" "$*"; }
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

{
    printf '# PITR restore proof (NFR-05, OPS-04)\n\n'
    printf -- '- Date: %s\n- Commit: %s\n- Limits: RPO %ss, RTO %ss\n\n' \
        "$(date -u '+%Y-%m-%d %H:%M:%S UTC')" "$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" "$RPO_LIMIT" "$RTO_LIMIT"
    printf '| Check | Measured | Limit | Result |\n|---|---|---|---|\n'
} > "$REPORT"
FAILED=0
result() { # <check> <measured> <limit> <ok 0|1>
    local verdict=PASS
    [ "$4" -eq 0 ] || { verdict=FAIL; FAILED=1; }
    printf '| %s | %s | %s | %s |\n' "$1" "$2" "$3" "$verdict" >> "$REPORT"
    log "$verdict $1: $2 (limit $3)"
}

log "starting postgres and backup ($PROJECT)"
if [ -z "${RETICORA_POSTGRES_IMAGE:-}" ]; then compose build postgres >&2; fi
compose up -d --no-build postgres >&2
until sql 'SELECT 1' >/dev/null 2>&1; do sleep 1; done
sql "CREATE TABLE pitr_probe (id bigserial PRIMARY KEY, phase text NOT NULL,
     at timestamptz NOT NULL DEFAULT clock_timestamp(), payload text)"
sql "INSERT INTO pitr_probe (phase) VALUES ('base')"
compose up -d --no-build backup >&2
wait_healthy backup
log "first base backup taken"

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
result "RPO: data loss after losing the data volume (latest restore)" "${rpo}s ($((committed - restored)) of $committed rows)" "${RPO_LIMIT}s" \
    "$(awk -v r="$rpo" -v l="$RPO_LIMIT" 'BEGIN { print (r <= l) ? 0 : 1 }')"
result "RTO: restore of the latest state" "${rto}s" "${RTO_LIMIT}s" "$([ "$rto" -le "$RTO_LIMIT" ] && echo 0 || echo 1)"

# After the restore the chain continues: archiving works on the new timeline.
compose start backup >/dev/null 2>&1
if compose exec -T backup reticora-pgbackrest-env.sh pgbackrest --stanza=reticora check >&2; then
    result "WAL archiving after the restore" "pgbackrest check ok" "ok" 0
else
    result "WAL archiving after the restore" "pgbackrest check failed" "ok" 1
fi

if [ "$FAILED" -eq 0 ]; then
    printf '\n**Result:** passed\n' >> "$REPORT"
else
    printf '\n**Result:** FAILED\n' >> "$REPORT"
fi
log "protocol: $REPORT"
exit "$FAILED"
