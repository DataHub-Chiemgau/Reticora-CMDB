#!/bin/sh
# reticora-backup.sh — scheduler of the backup container (WP-100, OPS-04).
#
#   reticora-backup.sh run      create the stanza, take a full backup when
#                               none exists, then one full backup per day at
#                               RETICORA_BACKUP_HOUR (UTC, default 2); each
#                               backup expires those older than 35 days
#   reticora-backup.sh health   healthy while the newest backup is younger
#                               than RETICORA_BACKUP_MAX_AGE_HOURS (26)
#
# WAL is archived continuously by the postgres container (archive_command,
# archive_timeout 300 s), so the restore point objective stays below 15
# minutes between the daily base backups (NFR-05).
set -eu

STANZA="${PGBACKREST_STANZA:-reticora}"
HOUR="${RETICORA_BACKUP_HOUR:-2}"
MAX_AGE_HOURS="${RETICORA_BACKUP_MAX_AGE_HOURS:-26}"

log() { printf '%s reticora-backup: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"; }

# last_backup_stop prints the stop time (epoch) of the newest backup.
last_backup_stop() {
    pgbackrest --stanza="$STANZA" --output=json info 2>/dev/null \
        | tr ',' '\n' | sed -n 's/.*"stop":\([0-9][0-9]*\).*/\1/p' | sort -n | tail -n 1
}

backup() {
    log "starting full backup"
    if pgbackrest --stanza="$STANZA" --type=full backup; then
        log "backup finished"
        return 0
    fi
    log "backup FAILED"
    return 1
}

seconds_until_hour() {
    h="$(date -u +%H)"; m="$(date -u +%M)"; s="$(date -u +%S)"
    secs=$(( (HOUR - ${h#0}) * 3600 - ${m#0} * 60 - ${s#0} ))
    [ "$secs" -gt 0 ] || secs=$((secs + 86400))
    printf '%s' "$secs"
}

case "${1:-run}" in
    health)
        stop="$(last_backup_stop)"
        [ -n "$stop" ] || { echo "no backup yet"; exit 1; }
        age=$(( $(date +%s) - stop ))
        if [ "$age" -gt $((MAX_AGE_HOURS * 3600)) ]; then
            echo "newest backup is $((age / 3600)) h old"
            exit 1
        fi
        echo "newest backup is $((age / 60)) min old"
        ;;
    run)
        until pg_isready -q -h /var/run/postgresql -U "${PGUSER:-reticora}"; do sleep 2; done
        pgbackrest --stanza="$STANZA" stanza-create
        pgbackrest --stanza="$STANZA" check
        [ -n "$(last_backup_stop)" ] || backup || true
        while :; do
            wait="$(seconds_until_hour)"
            log "next backup in ${wait}s"
            sleep "$wait"
            # A failed backup is retried after an hour; the health check
            # turns unhealthy once the newest backup is too old.
            until backup; do sleep 3600; done
        done
        ;;
    *)
        echo "usage: reticora-backup.sh run|health" >&2
        exit 2
        ;;
esac
