#!/usr/bin/env bash
# shellcheck disable=SC2016 # the bash -c checks expand their arguments
#
# backup-config.test.sh — checks the backup chain (WP-100: OPS-04, NFR-05,
# NFR-06; E-29 pgBackRest): WAL archiving and the daily base backup in
# Compose and Kubernetes, 35 days retention, encryption, bucket versioning
# and replication, and reticora-backup.sh against a stubbed pgbackrest and
# curl: health, verify-repo (WP-443, F-129) and the scheduler under dash and
# busybox sh (F-290).
#
# Docker is used for busybox sh when no busybox binary is on PATH (the
# TimescaleDB base of deploy/docker-compose/postgres/Dockerfile, the shell
# the script runs with; BACKUP_TEST_SH_IMAGE overrides it) and for the
# verify-repo case against MinIO (the pinned image of docker-compose.yml,
# TLS on a random local port); without Docker that case is skipped.
#
# Usage: ./tests/backup-config.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

TMP_ROOT="$(mktemp -d)"
MINIO_CT=""
cleanup() {
    [ -z "$MINIO_CT" ] || docker rm -f "$MINIO_CT" >/dev/null 2>&1
    rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

PASS=0
FAIL=0
SKIP=0
check() { # <name> <command …>
    local name="$1"; shift
    if "$@" >/dev/null 2>&1; then
        PASS=$((PASS + 1)); printf 'ok   %s\n' "$name"
    else
        FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$name"
    fi
}
skip() { SKIP=$((SKIP + 1)); printf 'skip %s\n' "$1"; }
has() { grep -qF -- "$2" <<<"$1"; } # <text> <fixed string>

docker_ok() { docker info >/dev/null 2>&1; }

COMPOSE=deploy/docker-compose/docker-compose.yml
OVERRIDE=deploy/docker-compose/docker-compose.override.yml
PGDIR=deploy/docker-compose/postgres
CONF=$PGDIR/pgbackrest.conf
K8S=deploy/k8s/base/components/backup

# The postgres service of the compose file (or the given one) as one block.
service_block() {
    awk -v svc="$1" '$0 == "  " svc ":" {f=1; print; next} f && /^  [^ #]/ {exit} f {print}' "${2:-$COMPOSE}"
}

# ─── Compose: WAL archiving and base backups ─────────────────────────────────
pg="$(service_block postgres)"
backup="$(service_block backup)"
# The image with pgBackRest: built from ./postgres by the source-build
# override, or the signed reticora-postgres of the release (same Dockerfile,
# RETICORA_POSTGRES_IMAGE, WP-387); the backup service runs the same image.
check "postgres is built with pgBackRest" \
    bash -c 'grep -q "context: ./postgres" <<<"$1" && grep -qF "image: \${RETICORA_POSTGRES_IMAGE:-reticora-postgres:latest}" <<<"$2" \
        && grep -qF "image: \${RETICORA_POSTGRES_IMAGE:-reticora-postgres:latest}" <<<"$3" \
        && grep -q "dockerfile: deploy/docker-compose/postgres/Dockerfile" .github/workflows/release.yml' \
    _ "$(service_block postgres "$OVERRIDE")" "$pg" "$backup"
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
# The runtime stage (after the last FROM) installs curl for verify-repo.
runtime_has_curl() {
    awk '/^FROM /{s=""} {s=s $0 "\n"} END{printf "%s", s}' "$PGDIR/Dockerfile" \
        | grep -qE '^RUN apk add --no-cache .*[[:space:]]curl([[:space:]]|$)'
}
check "the image contains curl for verify-repo" runtime_has_curl

# ─── Kubernetes component ────────────────────────────────────────────────────
check "k8s: daily full backup CronJob" \
    bash -c 'grep -q "schedule: \"0 2 \* \* \*\"" "$1" && grep -q -- "--type=full" "$1"' _ "$K8S/pgbackrest-cronjob.yaml"
check "k8s: the CronJob checks the bucket versioning before the backup" \
    grep -qxF '                - reticora-backup.sh verify-repo && exec pgbackrest --stanza=reticora --type=full backup' "$K8S/pgbackrest-cronjob.yaml"
check "k8s: S3 repository, 35 days, encrypted" \
    bash -c 'grep -q "repo1-type=s3" "$1" && grep -q "repo1-retention-full=35" "$1" && grep -q "repo1-cipher-type=aes-256-cbc" "$1"' _ "$K8S/pgbackrest-configmap.yaml"
check "k8s: the component includes the pgBackRest resources" \
    bash -c 'grep -q pgbackrest-cronjob.yaml "$1" && grep -q pgbackrest-configmap.yaml "$1"' _ "$K8S/kustomization.yaml"

# ─── Terraform, object storage, installer, docs ──────────────────────────────
TF=deploy/terraform
check "terraform: retention of at least 35 days is enforced" grep -q 'var.backup_retention_days >= 35' $TF/variables.tf
# NFR-05 (WP-443, F-129): a validation fails the plan; a check block only
# warned. terraform test in the CI job terraform runs the plans.
tf_replica_enforced() {
    local block
    block="$(awk '/^variable "backup_replica_region" [{]/,/^[}]/' "$TF/variables.tf")"
    has "$block" 'condition     = var.environment != "production" || (var.backup_replica_region != null && var.backup_replica_region != local.data_locations.backups)' \
        && has "$block" '(NFR-05)' \
        && ! grep -q '^check ' "$TF"/*.tf
}
tf_run_block() { awk -v r="run \"$1\" {" '$0 == r {f=1} f {print} f && /^}/ {exit}' "$TF/tests/backup.tftest.hcl"; }
tf_replica_tested() {
    local run
    for run in terraform_replica_missing terraform_replica_same; do
        has "$(tf_run_block "$run")" 'expect_failures = [var.backup_replica_region]' || return 1
    done
    has "$(tf_run_block terraform_replica_eu)" 'backup_replica_region = "eu-west-1"' \
        && ! has "$(tf_run_block terraform_replica_eu)" expect_failures
}
tf_job_runs_test() {
    awk '/^  terraform:$/{f=1; next} f && /^  [^ #]/{exit} f' .github/workflows/ci.yml | grep -qE '^ +run: terraform test( |$)'
}
check "terraform: production requires an EU replica region" tf_replica_enforced
check "terraform: tests/backup.tftest.hcl plans missing, same and distinct replica" tf_replica_tested
check "terraform: the CI job terraform runs terraform test" tf_job_runs_test
check "server enables bucket versioning" grep -q 'store.EnableVersioning' backend/cmd/server/main.go
check "installer generates and keeps the cipher passphrase" \
    bash -c 'grep -q "env_get RETICORA_BACKUP_CIPHER_PASS" install-cloud.sh && grep -q "^RETICORA_BACKUP_CIPHER_PASS=\$RETICORA_BACKUP_CIPHER_PASS" install-cloud.sh'
check "installer starts the backup service" grep -q 'compose_cmd up -d backup' install-cloud.sh
check "docs describe the chain, 35 days and the restore" \
    bash -c 'grep -q "pgBackRest" docs/backup-dr.md && grep -q "35 days" docs/backup-dr.md && grep -q -- "--type=time" docs/backup-dr.md'
check "docs describe the replica enforcement, verify-repo and the backup hour" \
    bash -c 'grep -q "backup.tftest.hcl" docs/backup-dr.md && grep -q "reticora-backup.sh verify-repo" docs/backup-dr.md \
        && grep -q "s3:GetBucketVersioning" docs/backup-dr.md && grep -q "an hour 0..23" docs/backup-dr.md'

# ─── reticora-backup.sh against a stubbed pgbackrest ─────────────────────────
mkdir -p "$TMP_ROOT/bin"
cat > "$TMP_ROOT/bin/pgbackrest" <<'STUB'
#!/bin/sh
case "$*" in
    *info*) [ -n "${STUB_STOP:-}" ] && printf '[{"backup":[{"timestamp":{"start":1,"stop":%s}}]}]\n' "$STUB_STOP" || printf '[{"backup":[]}]\n' ;;
esac
STUB
chmod +x "$TMP_ROOT/bin/pgbackrest"
not() { ! "$@"; }
# Without an S3 repository (repo1 of the Compose configuration is local).
health() { PATH="$TMP_ROOT/bin:$PATH" STUB_STOP="$1" PGBACKREST_CONFIG="$CONF" sh "$PGDIR/reticora-backup.sh" health; }
now="$(date +%s)"
check "health: a backup of 1 h is healthy" health $((now - 3600))
check "health: a backup of 30 h is unhealthy" not health $((now - 30 * 3600))
check "health: no backup is unhealthy" not health ""

# ─── verify-repo against a stubbed curl (NFR-05, WP-443) ─────────────────────
# The stub records its arguments and the configuration curl reads from stdin
# and answers like S3 GET ?versioning: <Status>STUB_VERSIONING</Status>
# (empty: never enabled), or with STUB_HTTP an S3 error that carries the
# access key, the string to sign and the signature.
cat > "$TMP_ROOT/bin/curl" <<'STUB'
#!/bin/sh
printf '%s\n' "$@" > "$STUB_DIR/curl.args"
cat > "$STUB_DIR/curl.config"
xmlns='xmlns="http://s3.amazonaws.com/doc/2006-03-01/"'
if [ -n "${STUB_HTTP:-}" ]; then
    printf '<?xml version="1.0" encoding="UTF-8"?>\n<Error><Code>SignatureDoesNotMatch</Code><Message>The request signature we calculated does not match the signature you provided.</Message><AWSAccessKeyId>%s</AWSAccessKeyId><StringToSign>AWS4-HMAC-SHA256 stub-string-to-sign</StringToSign><SignatureProvided>stub-signature-5f2b</SignatureProvided></Error>\n%s' "$STUB_KEY" "$STUB_HTTP"
elif [ -n "${STUB_VERSIONING:-}" ]; then
    printf '<?xml version="1.0" encoding="UTF-8"?>\n<VersioningConfiguration %s><Status>%s</Status></VersioningConfiguration>\n200' "$xmlns" "$STUB_VERSIONING"
else
    printf '<?xml version="1.0" encoding="UTF-8"?>\n<VersioningConfiguration %s/>\n200' "$xmlns"
fi
STUB
chmod +x "$TMP_ROOT/bin/curl"

S3_KEY=AKIARETICORATESTKEY01
# A quote and a backslash: the curl configuration must escape them.
S3_SECRET='wJalrXUtnFEMI/K7MDENG+bPx"Rfi\CYEXAMPLEKEY'
ALL_OUT="$TMP_ROOT/verify-output"
: > "$ALL_OUT"
# backup_sh <env assignment …> <command>: reticora-backup.sh <command> with
# the stubs, output kept for the credential check at the end.
backup_sh() {
    local out rc
    out="$(env PATH="$TMP_ROOT/sched:$TMP_ROOT/bin:$PATH" STUB_DIR="$TMP_ROOT" STUB_KEY="$S3_KEY" "$@" 2>&1)"; rc=$?
    printf '%s\n' "$out" | tee -a "$ALL_OUT"
    return "$rc"
}
# compose_s3 <env assignment …> <command>: like the Compose services, through
# reticora-pgbackrest-env.sh with RETICORA_BACKUP_S3_* (repo2).
compose_s3() {
    local cmd="${*: -1}"
    backup_sh PGBACKREST_CONFIG="$CONF" RETICORA_BACKUP_CIPHER_PASS=test-cipher \
        RETICORA_BACKUP_S3_BUCKET=reticora-backup RETICORA_BACKUP_S3_ENDPOINT=s3.eu-central-1.amazonaws.com \
        RETICORA_BACKUP_S3_REGION=eu-central-1 RETICORA_BACKUP_S3_KEY="$S3_KEY" RETICORA_BACKUP_S3_KEY_SECRET="$S3_SECRET" \
        "${@:1:$#-1}" sh "$PGDIR/reticora-pgbackrest-env.sh" sh "$PGDIR/reticora-backup.sh" "$cmd"
}
curl_args() { cat "${1:-$TMP_ROOT}/curl.args" 2>/dev/null; }
# signed_get <url> <region> [<stub dir>]: curl signed the GET with SigV4
# and got the credentials only on stdin (escaped), never as an argument.
signed_get() {
    local args config
    args="$(curl_args "${3:-}")"; config="$(cat "${3:-$TMP_ROOT}/curl.config")"
    grep -qx -- "$1" <<<"$args" \
        && grep -A1 -x -- --aws-sigv4 <<<"$args" | grep -qx "aws:amz:$2:s3" \
        && grep -qx -- '--config' <<<"$args" && grep -qx -- '-' <<<"$args" \
        && ! has "$args" "$S3_KEY" && ! has "$args" "wJalr" \
        && [ "$config" = 'user = "AKIARETICORATESTKEY01:wJalrXUtnFEMI/K7MDENG+bPx\"Rfi\\CYEXAMPLEKEY"' ]
}

out="$(compose_s3 STUB_VERSIONING=Enabled verify-repo)"; rc=$?
check "verify-repo: Compose bucket with versioning Enabled passes" \
    bash -c '[ "$1" -eq 0 ] && grep -qx "repo2: versioning of bucket reticora-backup is Enabled" <<<"$2"' _ "$rc" "$out"
check "verify-repo: signed GET ?versioning (SigV4), credentials only on stdin" \
    signed_get 'https://reticora-backup.s3.eu-central-1.amazonaws.com/?versioning=' eu-central-1
out="$(compose_s3 STUB_VERSIONING=Suspended verify-repo)"; rc=$?
check "verify-repo: versioning Suspended fails with a message" \
    bash -c '[ "$1" -ne 0 ] && grep -q "repo2: versioning of bucket reticora-backup is Suspended, it must be Enabled (NFR-05)" <<<"$2"' _ "$rc" "$out"
out="$(compose_s3 verify-repo)"; rc=$?
check "verify-repo: versioning never enabled fails with a message" \
    bash -c '[ "$1" -ne 0 ] && grep -q "is not configured, it must be Enabled" <<<"$2"' _ "$rc" "$out"
out="$(compose_s3 STUB_HTTP=403 verify-repo)"; rc=$?
check "verify-repo: an S3 error fails and names only its code" \
    bash -c '[ "$1" -ne 0 ] && grep -q "failed: HTTP 403 SignatureDoesNotMatch$" <<<"$2" && ! grep -qE "stub-(signature|string)" <<<"$2"' _ "$rc" "$out"
rm -f "$TMP_ROOT/curl.args"
out="$(backup_sh PGBACKREST_CONFIG="$CONF" sh "$PGDIR/reticora-backup.sh" verify-repo)"; rc=$?
check "verify-repo: without an S3 repository the check is skipped" \
    bash -c '[ "$1" -eq 0 ] && grep -q "no S3 repository configured, bucket versioning not checked" <<<"$2" && [ ! -e "$3/curl.args" ]' _ "$rc" "$out" "$TMP_ROOT"
out="$(backup_sh PGBACKREST_CONFIG="$CONF" RETICORA_BACKUP_S3_BUCKET=reticora-backup sh "$PGDIR/reticora-backup.sh" verify-repo)"; rc=$?
check "verify-repo: RETICORA_BACKUP_S3_BUCKET without reticora-pgbackrest-env.sh fails" \
    bash -c '[ "$1" -ne 0 ] && grep -q "run through reticora-pgbackrest-env.sh" <<<"$2"' _ "$rc" "$out"

# Kubernetes: repo1-type=s3 from the ConfigMap, the keys from the Secret.
sed -n '/pgbackrest.conf: |/,$p' "$K8S/pgbackrest-configmap.yaml" | sed '1d; s/^    //' > "$TMP_ROOT/k8s.conf"
k8s_s3() {
    backup_sh PGBACKREST_CONFIG="$TMP_ROOT/k8s.conf" PGBACKREST_REPO1_S3_KEY="$S3_KEY" PGBACKREST_REPO1_S3_KEY_SECRET="$S3_SECRET" \
        "$@" sh "$PGDIR/reticora-backup.sh" verify-repo
}
out="$(k8s_s3 STUB_VERSIONING=Enabled)"; rc=$?
check "verify-repo: k8s repo1-type=s3 with versioning Enabled passes" \
    bash -c '[ "$1" -eq 0 ] && grep -qx "repo1: versioning of bucket reticora-backup is Enabled" <<<"$2"' _ "$rc" "$out"
check "verify-repo: k8s signs for the bucket and region of the ConfigMap" \
    signed_get 'https://reticora-backup.s3.eu-central-1.amazonaws.com/?versioning=' eu-central-1
out="$(k8s_s3 STUB_VERSIONING=Suspended)"; rc=$?
check "verify-repo: k8s versioning Suspended fails" bash -c '[ "$1" -ne 0 ] && grep -q "is Suspended" <<<"$2"' _ "$rc" "$out"
out="$(backup_sh PGBACKREST_CONFIG="$TMP_ROOT/k8s.conf" sh "$PGDIR/reticora-backup.sh" verify-repo)"; rc=$?
check "verify-repo: missing S3 keys fail" bash -c '[ "$1" -ne 0 ] && grep -q "s3-key and s3-key-secret are required" <<<"$2"' _ "$rc" "$out"
rm -f "$TMP_ROOT/curl.args"
out="$(k8s_s3 STUB_VERSIONING=Enabled PGBACKREST_REPO1_S3_KEY_SECRET="$(printf 'first-part\nsecond-part-of-the-secret')")"; rc=$?
check "verify-repo: a secret with a line break fails without calling curl" \
    bash -c '[ "$1" -ne 0 ] && grep -q "credentials of bucket reticora-backup contain a line break" <<<"$2" && ! grep -q "part-of-the-secret" <<<"$2" && [ ! -e "$3/curl.args" ]' _ "$rc" "$out" "$TMP_ROOT"
# Path style, port and CA file of a self-hosted object store.
out="$(backup_sh PGBACKREST_CONFIG=/dev/null PGBACKREST_REPO1_TYPE=s3 PGBACKREST_REPO1_S3_BUCKET=reticora-backup \
    PGBACKREST_REPO1_S3_ENDPOINT=objects.example.eu PGBACKREST_REPO1_STORAGE_PORT=9000 PGBACKREST_REPO1_S3_REGION=eu-west-3 \
    PGBACKREST_REPO1_S3_URI_STYLE=path PGBACKREST_REPO1_STORAGE_CA_FILE=/etc/ssl/reticora-ca.crt \
    PGBACKREST_REPO1_S3_KEY="$S3_KEY" PGBACKREST_REPO1_S3_KEY_SECRET="$S3_SECRET" STUB_VERSIONING=Enabled \
    sh "$PGDIR/reticora-backup.sh" verify-repo)"; rc=$?
check "verify-repo: path style with port and CA file" \
    bash -c '[ "$1" -eq 0 ] && grep -A1 -x -- --cacert <<<"$2" | grep -qx /etc/ssl/reticora-ca.crt' _ "$rc" "$(curl_args)"
check "verify-repo: path style URL" signed_get 'https://objects.example.eu:9000/reticora-backup?versioning=' eu-west-3

# Health and start follow the versioning.
check "health: versioning Suspended is unhealthy despite a fresh backup" \
    not compose_s3 STUB_VERSIONING=Suspended STUB_STOP=$((now - 3600)) health
check "health: versioning Enabled and a fresh backup is healthy" \
    compose_s3 STUB_VERSIONING=Enabled STUB_STOP=$((now - 3600)) health

# ─── Start and scheduler, also under dash and busybox sh (F-290) ─────────────
# reticora-backup.sh run against stubs: pg_isready answers at once, the
# stubbed sleep ends the scheduler loop after its first wait.
mkdir -p "$TMP_ROOT/sched" "$TMP_ROOT/out"
printf '#!/bin/sh\necho "stub: pg_isready"\n' > "$TMP_ROOT/sched/pg_isready"
printf '#!/bin/sh\necho "stub: sleep $*"\nexit 1\n' > "$TMP_ROOT/sched/sleep"
chmod +x "$TMP_ROOT/sched/pg_isready" "$TMP_ROOT/sched/sleep"
# Readable for the container user, out/ also writable (curl stub).
chmod 0755 "$TMP_ROOT"
chmod 0777 "$TMP_ROOT/out"

out="$(compose_s3 STUB_VERSIONING=Suspended STUB_STOP=1 run)"; rc=$?
check "run: versioning Suspended stops the start before the first backup" \
    bash -c '[ "$1" -ne 0 ] && grep -q "is Suspended" <<<"$2" && grep -q "bucket versioning check FAILED, not starting" <<<"$2" && ! grep -q "stub: pg_isready" <<<"$2"' _ "$rc" "$out"

SH_IMAGE="${BACKUP_TEST_SH_IMAGE:-$(sed -n 's/^ARG TIMESCALE_IMAGE=//p' "$PGDIR/Dockerfile")}"
BUSYBOX="$(command -v busybox || true)"
# A busybox built to prefer its applets (Ubuntu's busybox-static, present on
# the GitHub runners) runs its own sleep instead of the stub, and the
# scheduler then really waits for the backup hour: such a binary is not
# used, the image's busybox sh is.
if [ -n "$BUSYBOX" ] && [ "$(PATH="$TMP_ROOT/sched:$PATH" "$BUSYBOX" sh -c 'command -v sleep' 2>/dev/null)" != "$TMP_ROOT/sched/sleep" ]; then
    BUSYBOX=""
fi
# in_shell <shell> <command> [<env assignment> …]: reticora-backup.sh
# <command> under dash or busybox sh (busybox binary, else the image) with
# the stubs; @T@ and @S@ in the assignments stand for the stub directory and
# deploy/docker-compose/postgres. Prints the output. A run that does not
# end within 120 s fails instead of holding the job.
in_shell() {
    local shell="$1" cmd="$2" a
    local -a envs=() sh=(dash)
    shift 2
    if [ "$shell" = busybox ] && [ -z "$BUSYBOX" ]; then
        for a in "$@"; do a="${a//@T@//t}"; envs+=(-e "${a//@S@//s}"); done
        timeout 120 docker run --rm --entrypoint /bin/sh -v "$PWD/$PGDIR:/s:ro" -v "$TMP_ROOT:/t:ro" -v "$TMP_ROOT/out:/t/out" \
            -e PATH=/t/sched:/t/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin -e STUB_DIR=/t/out \
            "${envs[@]}" "$SH_IMAGE" /s/reticora-backup.sh "$cmd" 2>&1
    else
        for a in "$@"; do a="${a//@T@/$TMP_ROOT}"; envs+=("${a//@S@/$PGDIR}"); done
        [ "$shell" = dash ] || sh=("$BUSYBOX" sh)
        env PATH="$TMP_ROOT/sched:$TMP_ROOT/bin:$PATH" STUB_DIR="$TMP_ROOT/out" "${envs[@]}" \
            timeout 120 "${sh[@]}" "$PGDIR/reticora-backup.sh" "$cmd" 2>&1
    fi
}
# waits_until <output> <hour>: the scheduler announced the wait until
# <hour>:00 UTC: 0 < secs <= 86400 and at most 30 s off the expected value.
waits_until() {
    local secs exp off
    secs="$(sed -n 's/.*reticora-backup: next backup in \([0-9][0-9]*\)s$/\1/p' <<<"$1")"
    [ -n "$secs" ] && [ "$secs" -gt 0 ] && [ "$secs" -le 86400 ] || return 1
    exp=$(( (10#$2 * 3600 - $(date -u +%s) % 86400 + 86400) % 86400 ))
    [ "$exp" -gt 0 ] || exp=86400
    off=$(( (secs - exp + 86400 + 43200) % 86400 - 43200 ))
    [ "${off#-}" -le 30 ]
}
shell_cases() { # <shell>
    local hour out rc
    for hour in 0 08 23; do
        out="$(in_shell "$1" run STUB_STOP=1 PGBACKREST_CONFIG=@S@/pgbackrest.conf RETICORA_BACKUP_HOUR="$hour")"
        check "scheduler ($1): RETICORA_BACKUP_HOUR=$hour waits until $((10#$hour)):00 UTC ($(sed -n 's/.*next backup in \([0-9]*s\)$/\1/p' <<<"$out"))" \
            waits_until "$out" "$hour"
    done
    for hour in 24 -1 abc; do
        out="$(in_shell "$1" run STUB_STOP=1 PGBACKREST_CONFIG=@S@/pgbackrest.conf RETICORA_BACKUP_HOUR="$hour")"; rc=$?
        check "scheduler ($1): RETICORA_BACKUP_HOUR=$hour stops the start with a message" \
            bash -c '[ "$1" -ne 0 ] && grep -q "RETICORA_BACKUP_HOUR must be an hour 0..23 (UTC), not '\''$3'\''" <<<"$2" && ! grep -qE "stub: pg_isready|next backup" <<<"$2"' _ "$rc" "$out" "$hour"
    done
    # verify-repo: the Kubernetes configuration file, and the environment.
    rm -f "$TMP_ROOT/out/curl.args"
    out="$(in_shell "$1" verify-repo PGBACKREST_CONFIG=@T@/k8s.conf PGBACKREST_REPO1_S3_KEY="$S3_KEY" \
        PGBACKREST_REPO1_S3_KEY_SECRET="$S3_SECRET" STUB_VERSIONING=Enabled STUB_KEY="$S3_KEY")"; rc=$?
    printf '%s\n' "$out" >> "$ALL_OUT"
    check "verify-repo ($1): k8s repo1-type=s3 with versioning Enabled passes" \
        bash -c '[ "$1" -eq 0 ] && grep -qx "repo1: versioning of bucket reticora-backup is Enabled" <<<"$2"' _ "$rc" "$out"
    check "verify-repo ($1): signed GET ?versioning, credentials only on stdin" \
        signed_get 'https://reticora-backup.s3.eu-central-1.amazonaws.com/?versioning=' eu-central-1 "$TMP_ROOT/out"
    out="$(in_shell "$1" verify-repo PGBACKREST_CONFIG=@S@/pgbackrest.conf PGBACKREST_REPO2_TYPE=s3 \
        PGBACKREST_REPO2_S3_BUCKET=reticora-backup PGBACKREST_REPO2_S3_ENDPOINT=s3.eu-central-1.amazonaws.com \
        PGBACKREST_REPO2_S3_REGION=eu-central-1 PGBACKREST_REPO2_S3_KEY="$S3_KEY" PGBACKREST_REPO2_S3_KEY_SECRET="$S3_SECRET" \
        STUB_VERSIONING=Suspended STUB_KEY="$S3_KEY")"; rc=$?
    printf '%s\n' "$out" >> "$ALL_OUT"
    check "verify-repo ($1): repo2 from the environment with versioning Suspended fails" \
        bash -c '[ "$1" -ne 0 ] && grep -qx "repo2: versioning of bucket reticora-backup is Suspended, it must be Enabled (NFR-05)" <<<"$2"' _ "$rc" "$out"
}
if command -v dash >/dev/null 2>&1; then
    shell_cases dash
else
    check "dash: dash available" false
fi
if [ -n "$BUSYBOX" ]; then
    shell_cases busybox
elif ! docker_ok; then
    check "busybox: busybox or Docker available" false
elif ! docker image inspect "$SH_IMAGE" >/dev/null 2>&1 && ! docker pull -q "$SH_IMAGE" >/dev/null 2>&1; then
    check "busybox: image $SH_IMAGE available" false
else
    shell_cases busybox
fi

# ─── verify-repo against MinIO (Docker) ──────────────────────────────────────
# Buckets with versioning enabled, never enabled and suspended on the pinned
# MinIO of the stack, TLS with a throwaway certificate on a random port.
MINIO_IMAGE="${BACKUP_TEST_MINIO_IMAGE:-$(sed -n 's/^ *image: \(pgsty\/minio:[^ ]*\)$/\1/p' "$COMPOSE" | head -n 1)}"
minio_up() {
    local i
    mkdir -p "$TMP_ROOT/minio/CAs" && chmod 0755 "$TMP_ROOT/minio" "$TMP_ROOT/minio/CAs" || return 1
    openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
        -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
        -keyout "$TMP_ROOT/minio/private.key" -out "$TMP_ROOT/minio/public.crt" >/dev/null 2>&1 || return 1
    chmod 0644 "$TMP_ROOT/minio/private.key" # throwaway key, read by the container user
    MINIO_USER=reticora-test
    MINIO_PASS="$(openssl rand -hex 20)"
    MINIO_CT="reticora-backup-test-minio-$$"
    docker run -d --name "$MINIO_CT" -p 127.0.0.1::9000 \
        -e MINIO_ROOT_USER="$MINIO_USER" -e MINIO_ROOT_PASSWORD="$MINIO_PASS" \
        -v "$TMP_ROOT/minio:/certs:ro" "$MINIO_IMAGE" server /data --certs-dir /certs >/dev/null || return 1
    MINIO_PORT="$(docker port "$MINIO_CT" 9000/tcp | sed -n 's/^127\.0\.0\.1:\([0-9][0-9]*\)$/\1/p' | head -n 1)"
    [ -n "$MINIO_PORT" ] || return 1
    for i in $(seq 1 60); do
        curl -fsS --cacert "$TMP_ROOT/minio/public.crt" "https://127.0.0.1:$MINIO_PORT/minio/health/live" >/dev/null 2>&1 && break
        [ "$i" -lt 60 ] || return 1
        sleep 1
    done
    docker exec "$MINIO_CT" mc --insecure alias set t https://127.0.0.1:9000 "$MINIO_USER" "$MINIO_PASS" >/dev/null \
        && docker exec "$MINIO_CT" mc --insecure mb t/versioned t/unversioned t/suspended >/dev/null \
        && docker exec "$MINIO_CT" mc --insecure version enable t/versioned >/dev/null \
        && docker exec "$MINIO_CT" mc --insecure version enable t/suspended >/dev/null \
        && docker exec "$MINIO_CT" mc --insecure version suspend t/suspended >/dev/null
}
# The real curl: PATH without the stubs.
minio_verify() { # <bucket> [<secret>]
    backup_sh PGBACKREST_CONFIG=/dev/null PGBACKREST_REPO1_TYPE=s3 PGBACKREST_REPO1_S3_BUCKET="$1" \
        PGBACKREST_REPO1_S3_ENDPOINT=127.0.0.1 PGBACKREST_REPO1_STORAGE_PORT="$MINIO_PORT" \
        PGBACKREST_REPO1_S3_REGION=us-east-1 PGBACKREST_REPO1_S3_URI_STYLE=path \
        PGBACKREST_REPO1_STORAGE_CA_FILE="$TMP_ROOT/minio/public.crt" \
        PGBACKREST_REPO1_S3_KEY="$MINIO_USER" PGBACKREST_REPO1_S3_KEY_SECRET="${2:-$MINIO_PASS}" \
        PATH="$PATH" sh "$PGDIR/reticora-backup.sh" verify-repo
}
if ! docker_ok; then
    skip "minio: verify-repo against MinIO needs Docker (runs in CI, job installer-script-tests)"
elif ! minio_up; then
    check "minio: MinIO $MINIO_IMAGE with TLS and three buckets is up" false
    docker logs "$MINIO_CT" 2>&1 | tail -n 5 | sed 's/^/     /'
else
    out="$(minio_verify versioned)"; rc=$?
    check "minio: versioned bucket passes (rc $rc)" bash -c '[ "$1" -eq 0 ] && grep -qx "repo1: versioning of bucket versioned is Enabled" <<<"$2"' _ "$rc" "$out"
    out="$(minio_verify unversioned)"; rc=$?
    check "minio: unversioned bucket fails (rc $rc)" bash -c '[ "$1" -ne 0 ] && grep -q "versioning of bucket unversioned is not configured" <<<"$2"' _ "$rc" "$out"
    out="$(minio_verify suspended)"; rc=$?
    check "minio: suspended bucket fails (rc $rc)" bash -c '[ "$1" -ne 0 ] && grep -q "versioning of bucket suspended is Suspended" <<<"$2"' _ "$rc" "$out"
    out="$(minio_verify versioned "wrong-$MINIO_PASS")"; rc=$?
    check "minio: a wrong secret fails with the S3 error code (rc $rc)" \
        bash -c '[ "$1" -ne 0 ] && grep -q "failed: HTTP 403 SignatureDoesNotMatch$" <<<"$2"' _ "$rc" "$out"
    out="$(minio_verify missing-bucket)"; rc=$?
    check "minio: a missing bucket fails (rc $rc)" bash -c '[ "$1" -ne 0 ] && grep -q "failed: HTTP 404 NoSuchBucket$" <<<"$2"' _ "$rc" "$out"
fi

# Nothing verify-repo printed contains a key, a secret or a signature.
no_credentials() {
    ! grep -qF -e "$S3_KEY" -e "wJalr" -e "stub-signature" -e "stub-string-to-sign" "$ALL_OUT" \
        && { [ -z "${MINIO_PASS:-}" ] || ! grep -qF -e "$MINIO_PASS" -e "$MINIO_USER" "$ALL_OUT"; } \
        && ! grep -qiE "signature=|credential=|x-amz-security-token" "$ALL_OUT"
}
check "verify-repo never prints a key, a secret or a signature" no_credentials

printf '\n%d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
