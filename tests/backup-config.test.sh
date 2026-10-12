#!/usr/bin/env bash
# shellcheck disable=SC2016 # the bash -c checks expand their arguments
#
# backup-config.test.sh — checks the backup chain (WP-100: OPS-04, NFR-05,
# NFR-06; E-29 pgBackRest) without Docker: WAL archiving and the daily base
# backup in Compose and Kubernetes, 35 days retention, encryption, bucket
# versioning and replication, and the scheduler/health logic of
# reticora-backup.sh against a stubbed pgbackrest.
#
# Usage: ./tests/backup-config.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
check() { # <name> <command …>
    local name="$1"; shift
    if "$@" >/dev/null 2>&1; then
        PASS=$((PASS + 1)); printf 'ok   %s\n' "$name"
    else
        FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$name"
    fi
}

COMPOSE=deploy/docker-compose/docker-compose.yml
PGDIR=deploy/docker-compose/postgres
CONF=$PGDIR/pgbackrest.conf
K8S=deploy/k8s/base/components/backup

# The postgres service of the compose file as one block.
service_block() {
    awk -v svc="$1" '$0 == "  " svc ":" {f=1; print; next} f && /^  [^ #]/ {exit} f {print}' "$COMPOSE"
}

# ─── Compose: WAL archiving and base backups ─────────────────────────────────
pg="$(service_block postgres)"
backup="$(service_block backup)"
check "postgres is built with pgBackRest" grep -q 'context: ./postgres' <<<"$pg"
check "postgres archives WAL (archive_mode=on)" grep -q 'archive_mode=on' <<<"$pg"
check "archive_command is pgbackrest archive-push" grep -q 'archive_command=pgbackrest --stanza=reticora archive-push %p' <<<"$pg"
timeout="$(sed -n 's/.*archive_timeout=\([0-9]*\).*/\1/p' <<<"$pg")"
check "archive_timeout keeps the RPO below 15 min (${timeout:-unset}s)" test "${timeout:-99999}" -le 900
check "postgres and backup share the repository volume" \
    bash -c 'grep -q "pgbackrest:/var/lib/pgbackrest" <<<"$1" && grep -q "pgbackrest:/var/lib/pgbackrest" <<<"$2"' _ "$pg" "$backup"
check "backup reads the data directory read-only" grep -q 'pgdata:/var/lib/postgresql/data:ro' <<<"$backup"
check "backup runs the scheduler and reports its health" \
    bash -c 'grep -q "reticora-backup.sh" <<<"$1" && grep -q "\"health\"" <<<"$1"' _ "$backup"
check "the cipher passphrase is required" grep -q 'RETICORA_BACKUP_CIPHER_PASS:?' <<<"$pg"
check "volumes pgbackrest and pgsocket are declared" \
    bash -c 'grep -qx "  pgbackrest:" "$1" && grep -qx "  pgsocket:" "$1"' _ "$COMPOSE"

# ─── pgBackRest configuration: retention and encryption ──────────────────────
check "repo1 keeps backups 35 days by time (NFR-06)" \
    bash -c 'grep -qx "repo1-retention-full-type=time" "$1" && grep -qx "repo1-retention-full=35" "$1"' _ "$CONF"
check "repo1 is encrypted" grep -qx 'repo1-cipher-type=aes-256-cbc' "$CONF"
check "the offsite repo2 keeps 35 days and is encrypted" \
    bash -c 'grep -q "REPO2_RETENTION_FULL=35" "$1" && grep -q "REPO2_CIPHER_TYPE=aes-256-cbc" "$1"' _ "$PGDIR/reticora-pgbackrest-env.sh"
check "the image builds pgBackRest" grep -q 'meson setup' "$PGDIR/Dockerfile"

# ─── Kubernetes component ────────────────────────────────────────────────────
check "k8s: daily full backup CronJob" \
    bash -c 'grep -q "schedule: \"0 2 \* \* \*\"" "$1" && grep -q -- "--type=full" "$1"' _ "$K8S/pgbackrest-cronjob.yaml"
check "k8s: S3 repository, 35 days, encrypted" \
    bash -c 'grep -q "repo1-type=s3" "$1" && grep -q "repo1-retention-full=35" "$1" && grep -q "repo1-cipher-type=aes-256-cbc" "$1"' _ "$K8S/pgbackrest-configmap.yaml"
check "k8s: the component includes the pgBackRest resources" \
    bash -c 'grep -q pgbackrest-cronjob.yaml "$1" && grep -q pgbackrest-configmap.yaml "$1"' _ "$K8S/kustomization.yaml"

# ─── Terraform, object storage, installer, docs ──────────────────────────────
check "terraform: retention of at least 35 days is enforced" grep -q 'var.backup_retention_days >= 35' deploy/terraform/variables.tf
check "terraform: production requires an EU replica region" grep -q 'check "backup_replication"' deploy/terraform/main.tf
check "server enables bucket versioning" grep -q 'store.EnableVersioning' backend/cmd/server/main.go
check "installer generates and keeps the cipher passphrase" \
    bash -c 'grep -q "env_get RETICORA_BACKUP_CIPHER_PASS" install-cloud.sh && grep -q "^RETICORA_BACKUP_CIPHER_PASS=\$RETICORA_BACKUP_CIPHER_PASS" install-cloud.sh'
check "installer starts the backup service" grep -q 'compose_cmd up -d backup' install-cloud.sh
check "docs describe the chain, 35 days and the restore" \
    bash -c 'grep -q "pgBackRest" docs/backup-dr.md && grep -q "35 days" docs/backup-dr.md && grep -q -- "--type=time" docs/backup-dr.md'

# ─── reticora-backup.sh against a stubbed pgbackrest ─────────────────────────
mkdir -p "$TMP_ROOT/bin"
cat > "$TMP_ROOT/bin/pgbackrest" <<'STUB'
#!/bin/sh
case "$*" in
    *info*) [ -n "${STUB_STOP:-}" ] && printf '[{"backup":[{"timestamp":{"start":1,"stop":%s}}]}]\n' "$STUB_STOP" || printf '[{"backup":[]}]\n' ;;
esac
STUB
chmod +x "$TMP_ROOT/bin/pgbackrest"
health() { PATH="$TMP_ROOT/bin:$PATH" STUB_STOP="$1" sh "$PGDIR/reticora-backup.sh" health; }
now="$(date +%s)"
check "health: a backup of 1 h is healthy" health $((now - 3600))
check "health: a backup of 30 h is unhealthy" bash -c '! "$@"' _ health $((now - 30 * 3600))
check "health: no backup is unhealthy" bash -c '! "$@"' _ health ""
secs="$(RETICORA_BACKUP_HOUR=2 sh -c 'eval "$(sed -n "/^seconds_until_hour()/,/^}/p" "$1")"; HOUR=$RETICORA_BACKUP_HOUR; seconds_until_hour' _ "$PGDIR/reticora-backup.sh")"
check "scheduler waits at most a day (${secs}s)" test "$secs" -gt 0 -a "$secs" -le 86400

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
