#!/usr/bin/env bash
#
# kind-smoke.test.sh — tests for tests/k8s/kind-smoke.sh with a stubbed
# kubectl that simulates the cluster (WP-441). Runs without a cluster; the
# workflow k8s-smoke runs it before it creates the kind cluster.
#
# The stub keeps the state of the secret reticora-secrets and the ConfigMap
# reticora-config in a file: the patches of the negative cases change it,
# applying the fixtures restores it. `get pods -o json` answers with the pod
# of the current state, as a correct cluster with the real server shows it,
# unless a scenario overrides that state (STUB_<state>):
#   ready                 pod Ready
#   config-error:<msg>    container waiting in CreateContainerConfigError
#   exit:<code>:<log>     container ended with <code>, <log> in its logs
#   pending               no container status yet
#
# For the case wal_archive (WP-444) the stub plays the database pod
# reticora-db-0 and the backup jobs, as a correct cluster answers, unless a
# scenario says otherwise:
#   STUB_DB_ROLLOUT_RC    rollout status of statefulset/reticora-db
#   STUB_CHECK_RC         rc of 'pgbackrest check'
#   STUB_ARCHIVE          ok: after a write and pg_switch_wal() the archiver
#                         reaches the switched segment; stuck: nothing is
#                         archived; other: the count rises, but with an older
#                         segment; nocount: the segment, but the same count
#   STUB_FULL_JOB         complete|failed|running, job of the CronJob as is
#   STUB_FULL_LISTED      0: the completed job adds no full backup to info;
#                         always: a full backup appears whatever the job does
#   STUB_WRONG_JOB        complete|failed|running, job with a literal cipher
#                         pass; STUB_WRONG_LOG and STUB_WRONG_RC its log and
#                         exit code (default: FormatError on backup.info, as
#                         pgBackRest reports a wrong pass)
#   STUB_NO_CIPHER_ENV    1: the CronJob passes no PGBACKREST_REPO1_CIPHER_PASS
# Usage: ./tests/k8s/kind-smoke.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
ROOT="$(pwd)"
SCRIPT="$ROOT/tests/k8s/kind-smoke.sh"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
STUB_BIN="$TMP_ROOT/bin"
mkdir -p "$STUB_BIN"

PASS=0
FAIL=0

# ─── Stubs ──────────────────────────────────────────────────────────────────

cat >"$STUB_BIN/kubectl" <<'STUBEOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_DIR/calls"
[ "${1:-}" = -n ] && shift 2
state=$(cat "$STUB_DIR/state")
behaviour() {
    local var="STUB_$state"
    if [ -n "${!var:-}" ]; then echo "${!var}"; return; fi
    case $state in
        ok) echo ready ;;
        master_key_missing) echo "config-error:couldn't find key master-key in Secret reticora-staging/reticora-secrets" ;;
        master_key_empty) echo 'exit:1:{"level":"ERROR","msg":"invalid configuration","error":"RETICORA_ENVIRONMENT=staging requires RETICORA_MASTER_KEY"}' ;;
        issuer_empty) echo 'exit:1:{"level":"ERROR","msg":"invalid configuration","error":"RETICORA_ENVIRONMENT=staging requires RETICORA_OIDC_ISSUER_URL"}' ;;
        issuer_key_missing) echo "config-error:couldn't find key oidc-issuer-url in ConfigMap reticora-staging/reticora-config" ;;
    esac
}
pod_json() {
    local b=$1 container conditions='[{"type":"Ready","status":"False"}]'
    case $b in
        ready)
            conditions='[{"type":"Ready","status":"True"}]'
            container='{"name":"server","ready":true,"restartCount":0,"state":{"running":{"startedAt":"2026-10-26T00:00:00Z"}}}' ;;
        config-error:*)
            container=$(jq -cn --arg m "${b#config-error:}" \
                '{name:"server",ready:false,restartCount:0,state:{waiting:{reason:"CreateContainerConfigError",message:$m}}}') ;;
        exit:*)
            local rest=${b#exit:}
            container=$(jq -cn --argjson c "${rest%%:*}" \
                '{name:"server",ready:false,restartCount:3,state:{waiting:{reason:"CrashLoopBackOff",message:"back-off 40s restarting failed container"}},lastState:{terminated:{exitCode:$c,reason:"Error"}}}') ;;
        *) container='' ;;
    esac
    jq -n --argjson cond "$conditions" --arg c "$container" '{items:[
        {metadata:{name:"reticora-server-5f7c9-x1"},status:{phase:"Running",conditions:$cond,
         containerStatuses:(if $c == "" then [] else [$c | fromjson] end)}},
        {metadata:{name:"reticora-server-5f7c9-old",deletionTimestamp:"2026-10-26T00:00:00Z"},
         status:{conditions:[{type:"Ready",status:"True"}],containerStatuses:[]}}]}'
}
# ── wal_archive: database pod reticora-db-0 and backup jobs ──
counter() { cat "$STUB_DIR/$1" 2>/dev/null || echo "$2"; }
# The job the CronJob template yields, as kubectl create job --from prints it.
cronjob_job() {
    jq -n --arg name "$1" --arg nocipher "${STUB_NO_CIPHER_ENV:-0}" '
        def ref($k): {valueFrom: {secretKeyRef: {name: "reticora-pgbackrest", key: $k}}};
        {apiVersion: "batch/v1", kind: "Job", metadata: {name: $name},
         spec: {backoffLimit: 2, template: {spec: {restartPolicy: "Never", containers: [{name: "pgbackrest",
           image: "reticora-cmdb/postgres:kind-smoke",
           env: ((if $nocipher == "1" then [] else [{name: "PGBACKREST_REPO1_CIPHER_PASS"} + ref("cipher-pass")] end)
                 + [{name: "PGBACKREST_REPO1_S3_KEY"} + ref("s3-key"),
                    {name: "PGBACKREST_REPO1_S3_ENDPOINT", value: "minio"}])}]}}}}'
}
job_status() {
    case $(counter "job-$1" none) in
        complete) jq -n '{status: {succeeded: 1, conditions: [{type: "SuccessCriteriaMet", status: "True"}, {type: "Complete", status: "True"}]}}' ;;
        failed) jq -n '{status: {failed: 1, conditions: [{type: "FailureTarget", status: "True"}, {type: "Failed", status: "True"}]}}' ;;
        running) jq -n '{status: {active: 1}}' ;;
        *) echo "Error from server (NotFound): jobs.batch \"$1\" not found" >&2; return 1 ;;
    esac
}
job_pods() {
    local code
    case $(counter "job-$1" none) in
        complete) code=0 ;;
        failed) code=$(counter "rc-$1" 1) ;;
        *) jq -n '{items: [{status: {containerStatuses: [{name: "pgbackrest", state: {running: {}}}]}}]}'; return ;;
    esac
    jq -n --argjson c "$code" '{items: [{status: {containerStatuses: [{name: "pgbackrest", state: {terminated: {exitCode: $c}}}]}}]}'
}
# start_job NAME KIND records the job and its outcome.
start_job() {
    local outcome
    if [ "$2" = wrong ]; then
        outcome=${STUB_WRONG_JOB:-failed}
        echo "${STUB_WRONG_RC:-29}" >"$STUB_DIR/rc-$1"
        # pgBackRest parses the decrypted backup.info while it streams, so a
        # wrong pass usually ends in a FormatError with the garbage in it.
        garbage=$'\xb4\x1f\xe9x\x087\xc3'
        printf '%s\n' "${STUB_WRONG_LOG:-ERROR: [029]: unable to load info file '/reticora/backup/reticora/backup.info' or '/reticora/backup/reticora/backup.info.copy':
       FormatError: key/value found outside of section at line 1: $garbage
       FormatError: key/value found outside of section at line 1: $garbage
       HINT: backup.info cannot be opened and is required to perform a backup.
       HINT: has a stanza-create been performed?}" >"$STUB_DIR/log-$1"
    else
        outcome=${STUB_FULL_JOB:-complete}
        echo 1 >"$STUB_DIR/rc-$1"
        printf 'INFO: backup command end: %s\n' "$outcome" >"$STUB_DIR/log-$1"
        if { [ "$outcome" = complete ] && [ "${STUB_FULL_LISTED:-1}" = 1 ]; } || [ "${STUB_FULL_LISTED:-1}" = always ]; then
            echo $(($(counter fulls 0) + 1)) >"$STUB_DIR/fulls"
        fi
    fi
    echo "$outcome" >"$STUB_DIR/job-$1"
    echo "job.batch/$1 created"
}
db_exec() {
    local container=$1 sql="${*: -1}"
    shift 2
    case "$container $*" in
        "postgres pgbackrest --stanza=reticora check")
            if [ "${STUB_CHECK_RC:-0}" -eq 0 ]; then
                echo "P00   INFO: check command end: completed successfully"
            else
                echo "ERROR: [082]: WAL segment 000000010000000000000003 was not archived before the 60000ms timeout"
            fi
            return "${STUB_CHECK_RC:-0}" ;;
        "pgbackrest pgbackrest --stanza=reticora --output=json info")
            jq -n --argjson n "$(counter fulls 0)" '[{name: "reticora", status: {code: (if $n == 0 then 2 else 0 end)},
                backup: [range($n) | {label: "20261026-0100\(.)F", type: "full"}]}]' ;;
        "postgres psql "*)
            case $sql in
                "SELECT archived_count FROM pg_stat_archiver") echo 2 ;;
                *"INSERT INTO kind_smoke_wal"*) touch "$STUB_DIR/wrote" ;;
                *"pg_switch_wal()"*)
                    [ -e "$STUB_DIR/wrote" ] && touch "$STUB_DIR/switched"
                    echo 000000010000000000000004 ;;
                *"FROM pg_stat_archiver"*)
                    if [ ! -e "$STUB_DIR/switched" ] || [ "${STUB_ARCHIVE:-ok}" = stuck ]; then
                        echo "2|000000010000000000000003|0|f"
                    elif [ "${STUB_ARCHIVE:-ok}" = other ]; then
                        echo "3|000000010000000000000003|0|f"
                    elif [ "${STUB_ARCHIVE:-ok}" = nocount ]; then
                        echo "2|000000010000000000000004|0|t"
                    else
                        echo "3|000000010000000000000004|0|t"
                    fi ;;
                *) echo "unexpected SQL $sql" >&2; return 1 ;;
            esac ;;
        *) echo "unexpected exec in reticora-db-0: $container $*" >&2; return 1 ;;
    esac
}
case "${1:-}" in
    get)
        case "${2:-}" in
            pods)
                [ "${STUB_GET_RC:-0}" -eq 0 ] || { echo "The connection to the server was refused" >&2; exit 1; }
                case "$*" in
                    *job-name=*) job="$*"; job=${job##*job-name=}; job_pods "${job%% *}" ;;
                    *jsonpath*) echo reticora-server-5f7c9-x1 ;;
                    *"-o json"*) pod_json "$(behaviour)" ;;
                    *) echo "NAME READY STATUS" ;;
                esac ;;
            job) job_status "$3" || exit 1 ;;
            secret)
                case "$3" in
                    kind-smoke-postgres) jq -n --arg v "$STUB_ADMIN_PW" '{data:{password:($v | @base64)}}' ;;
                    reticora-db-runtime) jq -n --arg v "$STUB_RUNTIME_DSN" '{data:{"database-url":($v | @base64)}}' ;;
                    kind-smoke-minio) jq -n --arg u "$STUB_MINIO_USER" --arg p "$STUB_MINIO_PASSWORD" \
                        '{data:{"root-user":($u | @base64),"root-password":($p | @base64)}}' ;;
                    *) echo "Error from server (NotFound): secrets \"$3\" not found" >&2; exit 1 ;;
                esac ;;
        esac ;;
    patch)
        case "$*" in
            *'secret reticora-secrets'*'"remove"'*'/data/master-key'*) echo master_key_missing >"$STUB_DIR/state" ;;
            *'secret reticora-secrets'*'"master-key":""'*) echo master_key_empty >"$STUB_DIR/state" ;;
            *'configmap reticora-config'*'"oidc-issuer-url":""'*) echo issuer_empty >"$STUB_DIR/state" ;;
            *'configmap reticora-config'*'"remove"'*'/data/oidc-issuer-url'*) echo issuer_key_missing >"$STUB_DIR/state" ;;
            *) echo "unexpected patch $*" >&2; exit 1 ;;
        esac
        echo "patched" ;;
    apply)
        cat >>"$STUB_DIR/applied"
        [ "${STUB_RESTORE_BROKEN:-0}" = 1 ] || echo ok >"$STUB_DIR/state"
        echo "configured" ;;
    logs)
        case "${2:-}" in
            job/*) cat "$STUB_DIR/log-${2#job/}" 2>/dev/null ;;
            reticora-db-0) echo "stub log of reticora-db-0" ;;
            *)
                b=$(behaviour)
                case $b in exit:*) rest=${b#exit:}; printf '%s\n' "${rest#*:}" ;; esac ;;
        esac ;;
    port-forward)
        echo "Forwarding from 127.0.0.1:18080 -> 8080"
        echo "Forwarding from [::1]:18080 -> 8080"
        exec sleep 30 ;;
    config)
        [ -n "$STUB_CONTEXT" ] || { echo "error: current-context is not set" >&2; exit 1; }
        echo "$STUB_CONTEXT" ;;
    rollout)
        case "$*" in
            *statefulset/reticora-db*)
                [ "${STUB_DB_ROLLOUT_RC:-0}" -eq 0 ] || echo 'error: timed out waiting for the condition'
                exit "${STUB_DB_ROLLOUT_RC:-0}" ;;
        esac
        exit "${STUB_ROLLOUT_RC:-0}" ;;
    exec)
        case "${2:-} ${3:-}" in
            "-i deployment/postgres") cat >"$STUB_DIR/exec.sql"; exit "${STUB_EXEC_RC:-0}" ;;
            "-i deployment/minio") cat >"$STUB_DIR/minio.sh"; exit "${STUB_BUCKET_RC:-0}" ;;
            "reticora-db-0 -c") db_exec "${@:4}"; exit ;;
        esac
        echo "unexpected kubectl $*" >&2; exit 1 ;;
    create)
        case "${2:-}" in
            secret)
                printf '%s\n' "$*" >>"$STUB_DIR/secrets"
                printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n' "$4" ;;
            job)
                case "$*" in
                    *--dry-run=client*) cronjob_job "$3" ;;
                    *--from=cronjob/reticora-pgbackrest-full) start_job "$3" full ;;
                    *) echo "unexpected kubectl $*" >&2; exit 1 ;;
                esac ;;
            -f)
                json=$(cat)
                printf '%s\n' "$json" >"$STUB_DIR/created-$(jq -r .metadata.name <<<"$json").json"
                if jq -e '.spec.template.spec.containers[].env[] | select(.name == "PGBACKREST_REPO1_CIPHER_PASS") | has("value")' <<<"$json" >/dev/null; then
                    start_job "$(jq -r .metadata.name <<<"$json")" wrong
                else
                    start_job "$(jq -r .metadata.name <<<"$json")" full
                fi ;;
            *) echo "unexpected kubectl $*" >&2; exit 1 ;;
        esac ;;
    delete | describe) ;;
    *) echo "unexpected kubectl $*" >&2; exit 1 ;;
esac
STUBEOF

# kustomize build DIR prints a marker of DIR; the apply stub records it.
cat >"$STUB_BIN/kustomize" <<'STUBEOF'
#!/usr/bin/env bash
[ "$1" = build ] || exit 2
printf '# rendered %s\n' "${2#"$STUB_ROOT"/}"
STUBEOF

# curl -o FILE … URL writes STUB_READYZ_BODY to FILE and prints the code.
cat >"$STUB_BIN/curl" <<'STUBEOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_DIR/curl"
while [ $# -gt 0 ]; do
    [ "$1" = -o ] && { printf '%s' "$STUB_READYZ_BODY" >"$2"; shift; }
    shift
done
printf '%s' "$STUB_READYZ_CODE"
STUBEOF

cat >"$STUB_BIN/migrate" <<'STUBEOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_DIR/migrate"
exit "${STUB_MIGRATE_RC:-0}"
STUBEOF

# openssl writes a marker into every -out and -keyout file, prints random
# bytes for rand and records each call, an -extfile with its content.
cat >"$STUB_BIN/openssl" <<'STUBEOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_DIR/openssl"
[ "${1:-}" = "${STUB_OPENSSL_FAIL:-none}" ] && exit 1
[ "${1:-}" = rand ] && { echo 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef; exit 0; }
written=0
while [ $# -gt 0 ]; do
    case $1 in
        -out | -keyout) echo "stub $1" >"$2"; written=1; shift ;;
        -extfile) sed 's/^/     extfile: /' "$2" >>"$STUB_DIR/openssl"; shift ;;
    esac
    shift
done
[ "$written" -eq 1 ]
STUBEOF
chmod +x "$STUB_BIN"/*

# ─── Harness ────────────────────────────────────────────────────────────────

# The runtime DSN and the admin password come from the real fixtures, so the
# setup test also checks that the script reads them as they are committed.
FIXTURE_DSN=$(sed -n 's/^ *database-url: //p' tests/k8s/fixtures/reticora-secrets.yaml)
FIXTURE_ADMIN_PW=$(sed -n 's/^ *password: //p' tests/k8s/fixtures/postgres.yaml)
FIXTURE_MINIO_USER=$(sed -n 's/^ *root-user: //p' tests/k8s/fixtures/minio.yaml)
FIXTURE_MINIO_PASSWORD=$(sed -n 's/^ *root-password: //p' tests/k8s/fixtures/minio.yaml)

reset() {
    unset STUB_ok STUB_master_key_missing STUB_master_key_empty STUB_issuer_empty STUB_issuer_key_missing \
        STUB_GET_RC STUB_RESTORE_BROKEN STUB_ROLLOUT_RC STUB_EXEC_RC STUB_MIGRATE_RC \
        STUB_DB_ROLLOUT_RC STUB_CHECK_RC STUB_ARCHIVE STUB_FULL_JOB STUB_FULL_LISTED STUB_WRONG_JOB \
        STUB_WRONG_LOG STUB_WRONG_RC STUB_NO_CIPHER_ENV STUB_BUCKET_RC STUB_OPENSSL_FAIL
    export STUB_DIR="$TMP_ROOT/stub" STUB_ROOT="$ROOT"
    rm -rf "$STUB_DIR" "$TMP_ROOT/artifacts"
    mkdir -p "$STUB_DIR"
    echo ok >"$STUB_DIR/state"
    export STUB_READYZ_CODE=200
    export STUB_READYZ_BODY='{"status":"ready","checks":{"database":"ok","nats":"ok","redis":"ok"},"details":{"ingest":{"lanes":1}}}'
    export STUB_RUNTIME_DSN="$FIXTURE_DSN" STUB_ADMIN_PW="$FIXTURE_ADMIN_PW"
    export STUB_MINIO_USER="$FIXTURE_MINIO_USER" STUB_MINIO_PASSWORD="$FIXTURE_MINIO_PASSWORD"
    export STUB_CONTEXT=kind-reticora-smoke
}

# smoke ARGS… runs the script against the stubs; output in $OUT, code in $RC.
smoke() {
    RC=0
    OUT=$(KUBECTL="$STUB_BIN/kubectl" KUSTOMIZE="$STUB_BIN/kustomize" CURL="$STUB_BIN/curl" \
        MIGRATE="$STUB_BIN/migrate" OPENSSL="$STUB_BIN/openssl" \
        READY_TIMEOUT=2 STATE_TIMEOUT=2 DEPS_TIMEOUT=2 WAL_TIMEOUT=2 JOB_TIMEOUT=2 POLL_INTERVAL=0.1 \
        ARTIFACT_DIR="$TMP_ROOT/artifacts" "$SCRIPT" "$@" 2>&1) || RC=$?
}

# check NAME [COMMAND…] counts a test: it passes when COMMAND succeeds or,
# without COMMAND, when the command before the call succeeded. On failure
# the output of the last run is shown.
check() {
    local status=$? name=$1
    shift
    if [ "$#" -gt 0 ]; then
        "$@"
        status=$?
    fi
    if [ "$status" -eq 0 ]; then
        PASS=$((PASS + 1))
        printf 'ok   %s\n' "$name"
    else
        FAIL=$((FAIL + 1))
        printf 'FAIL %s (exit %s)\n' "$name" "${RC:-?}"
        printf '%s\n' "$OUT" | sed 's/^/     /'
    fi
}

has() { grep -qE -- "$1" <<<"$OUT"; }
rc_is() { [ "$RC" -eq "$1" ]; }
fails_with() { [ "$RC" -eq 1 ] && has "$1"; }
called() { grep -qE -- "$1" "$STUB_DIR/calls"; }
# line_of PATTERN prints the number of the first call matching PATTERN.
line_of() { grep -nE -- "$1" "$STUB_DIR/calls" | head -n 1 | cut -d: -f1; }

# ─── Run: correct cluster ───────────────────────────────────────────────────

reset
smoke run
rc_is 0 && has '^kind smoke: 13 passed, 0 failed$'
check "correct cluster: exit 0, 13 passed (5 cases, 4 restores, 4 checks of wal_archive)"
check "the protocol names the kind context" has '^kubectl context kind-reticora-smoke$'
check "ready: pod Ready and /readyz 200 with database, redis, nats ok" \
    has '^ok   ready: reticora-server-5f7c9-x1 Ready after [0-9]+s; /readyz 200 \{"status":"ready"'
check "master_key_missing: CreateContainerConfigError naming master-key" \
    has "^ok   master_key_missing: master-key removed from the secret reticora-secrets -> reticora-server-5f7c9-x1 CreateContainerConfigError: couldn't find key master-key"
check "master_key_empty: exit code 1 with 'requires RETICORA_MASTER_KEY'" \
    has '^ok   master_key_empty: .* exit code 1, log: .*staging requires RETICORA_MASTER_KEY'
check "issuer_empty: exit code 1 with 'requires RETICORA_OIDC_ISSUER_URL'" \
    has '^ok   issuer_empty: .* exit code 1, log: .*staging requires RETICORA_OIDC_ISSUER_URL'
check "issuer_key_missing: CreateContainerConfigError naming oidc-issuer-url" \
    has "^ok   issuer_key_missing: .*CreateContainerConfigError: couldn't find key oidc-issuer-url"
[ "$(grep -c '^ok   restore after .*Ready after' <<<"$OUT")" -eq 4 ]
check "every negative case is followed by a restore to Ready"
check "wal_archive: pgbackrest check rc 0 in reticora-db-0" \
    has '^ok   wal_archive: pgbackrest --stanza=reticora check in reticora-db-0 rc 0: .*check command end: completed successfully'
check "wal_archive: archived_count rises to the switched segment after pg_switch_wal()" \
    has '^ok   wal_archive: archived_count 2 -> 3, last_archived_wal 000000010000000000000004, failed_count 0 within [0-9]+s after pg_switch_wal\(\) \(segment 000000010000000000000004\)'
check "wal_archive: the job of the CronJob completes with rc 0 and adds a full backup" \
    has '^ok   wal_archive: full backup by a job of the CronJob reticora-pgbackrest-full: job kind-smoke-full SuccessCriteriaMet,Complete, rc 0; full backups 0 -> 1 20261026-01000F'
check "wal_archive: the job with the wrong cipher pass fails with rc != 0, backup.info not decrypted" \
    has '^ok   wal_archive: wrong cipher pass at the repository host: job kind-smoke-wrong-cipher FailureTarget,Failed, rc 29; backup.info not decrypted: FormatError: key/value found outside of section at line 1$'
# Order of the cluster changes: overlay, then per case patch, pod deletion,
# fixtures, pod deletion; last the overlay of wal_archive.
order=$(grep -oE '^-n reticora-staging (patch (secret|configmap) [a-z-]+|delete pod)|^apply' "$STUB_DIR/calls" | tr '\n' '|')
want='apply|'
for obj in 'secret reticora-secrets' 'secret reticora-secrets' 'configmap reticora-config' 'configmap reticora-config'; do
    want="${want}-n reticora-staging patch $obj|-n reticora-staging delete pod|apply|-n reticora-staging delete pod|"
done
want="${want}apply|"
[ "$order" = "$want" ]
check "cluster changes in order: overlay, then patch, restart, restore per case, then the overlay of wal_archive"
[ "$(grep -c '^# rendered' "$STUB_DIR/applied")" -eq 6 ] &&
    [ "$(head -n 1 "$STUB_DIR/applied")" = "# rendered deploy/k8s/overlays/staging" ] &&
    [ "$(grep -c '^# rendered tests/k8s/fixtures$' "$STUB_DIR/applied")" -eq 4 ] &&
    [ "$(grep '^# rendered' "$STUB_DIR/applied" | tail -n 1)" = "# rendered tests/k8s/fixtures/overlay-wal" ]
check "the overlay is applied first, the fixtures restore every case, wal_archive applies overlay-wal"
# wal_archive: the database is Ready before the checks; check before the
# write, the write before the switch, the switch before the full backup,
# the full backup before the job with the wrong cipher pass.
seq_wal=""
for p in 'rollout status statefulset/reticora-db --timeout=2s' 'exec reticora-db-0 -c postgres -- pgbackrest --stanza=reticora check' \
    'INSERT INTO kind_smoke_wal' 'pg_switch_wal\(\)' 'create job kind-smoke-full --from=cronjob/reticora-pgbackrest-full$' \
    'create job kind-smoke-wrong-cipher --from=cronjob/reticora-pgbackrest-full --dry-run=client -o json' 'create -f -'; do
    seq_wal="$seq_wal $(line_of "$p")"
done
# shellcheck disable=SC2086 # one number per word
[ "$(wc -w <<<"$seq_wal")" -eq 7 ] && [ "$(printf '%s\n' $seq_wal | sort -n | tr '\n' ' ')" = "$(printf '%s\n' $seq_wal | tr '\n' ' ')" ]
check "wal_archive: Ready, check, write, pg_switch_wal(), full backup, then the wrong cipher pass"
wrong="$STUB_DIR/created-kind-smoke-wrong-cipher.json"
[ -s "$wrong" ] && jq -e '.spec.backoffLimit == 0
    and ([.spec.template.spec.containers[0].env[] | select(.name == "PGBACKREST_REPO1_CIPHER_PASS")] == [{name: "PGBACKREST_REPO1_CIPHER_PASS", value: "kind-smoke-wrong-cipher-pass"}])
    and ([.spec.template.spec.containers[0].env[] | select(.name == "PGBACKREST_REPO1_S3_KEY") | .valueFrom.secretKeyRef.key] == ["s3-key"])' "$wrong" >/dev/null &&
    ! called 'patch secret reticora-pgbackrest' && ! called 'reticora-pgbackrest.*--from-literal'
check "wal_archive: the wrong cipher pass is a literal in that job only (no retry, S3 key and secret unchanged)"
called 'delete job kind-smoke-full --ignore-not-found' && called 'delete job kind-smoke-wrong-cipher --ignore-not-found' &&
    called 'get pods -l batch.kubernetes.io/job-name=kind-smoke-full -o json' &&
    called 'get pods -l batch.kubernetes.io/job-name=kind-smoke-wrong-cipher -o json'
check "wal_archive: jobs of an earlier run are deleted first, exit codes from the pods of each job"
called 'port-forward svc/reticora-server :80' && grep -q 'http://127.0.0.1:18080/readyz' "$STUB_DIR/curl"
check "/readyz is requested through the port-forward of svc/reticora-server"
! has '5f7c9-old'
check "pods being deleted are ignored"
missing=""
for f in ready master_key_missing master_key_empty issuer_empty issuer_key_missing wal_archive; do
    [ -s "$TMP_ROOT/artifacts/$f.txt" ] || missing="$missing $f"
done
[ -z "$missing" ] && grep -q '^== logs job/kind-smoke-wrong-cipher' "$TMP_ROOT/artifacts/wal_archive.txt"
check "protocol of every case in ARTIFACT_DIR"

# ─── Run: broken clusters must fail ─────────────────────────────────────────

reset
export STUB_master_key_missing=ready
smoke run
check "master-key reference optional (pod Ready without the key) fails" \
    fails_with '^FAIL master_key_missing: .*want config-error .master-key., got: reticora-server-5f7c9-x1 started although master-key is missing: Ready'

reset
export STUB_master_key_missing="config-error:couldn't find key oidc-client-secret in Secret reticora-staging/reticora-secrets"
smoke run
check "CreateContainerConfigError naming another key fails" \
    fails_with "^FAIL master_key_missing: .*CreateContainerConfigError: couldn't find key oidc-client-secret.*no decision within 2s"

reset
export STUB_master_key_missing='exit:1:{"msg":"invalid configuration","error":"RETICORA_ENVIRONMENT=staging requires RETICORA_MASTER_KEY"}'
smoke run
check "a started container instead of CreateContainerConfigError fails" \
    fails_with '^FAIL master_key_missing: .*started although master-key is missing'

reset
export STUB_master_key_empty=ready
smoke run
check "empty master key accepted (pod Ready) fails" \
    fails_with '^FAIL master_key_empty: .*became Ready instead of ending with exit 1'

reset
export STUB_master_key_empty='exit:1:{"msg":"failed to init envelope encryptor","error":"crypto: master key is required"}'
smoke run
check "empty master key: exit 1 without 'requires RETICORA_MASTER_KEY' fails" \
    fails_with "^FAIL master_key_empty: .*log lacks 'requires RETICORA_MASTER_KEY'"

reset
export STUB_master_key_empty='exit:2:{"msg":"invalid configuration","error":"RETICORA_ENVIRONMENT=staging requires RETICORA_MASTER_KEY"}'
smoke run
check "empty master key: exit code 2 instead of 1 fails" \
    fails_with '^FAIL master_key_empty: .*\(exit code 2\).*no decision within 2s'

reset
export STUB_master_key_empty="config-error:couldn't find key master-key in Secret reticora-staging/reticora-secrets"
smoke run
check "empty master key: CreateContainerConfigError instead of exit 1 fails" \
    fails_with '^FAIL master_key_empty: .*want exit1 .*CreateContainerConfigError'

reset
export STUB_issuer_empty=ready
smoke run
check "empty issuer accepted (pod Ready) fails" \
    fails_with '^FAIL issuer_empty: .*became Ready instead of ending with exit 1'

reset
export STUB_issuer_empty='exit:1:{"msg":"invalid configuration","error":"RETICORA_ENVIRONMENT=staging requires RETICORA_MASTER_KEY"}'
smoke run
check "empty issuer: exit 1 for another key fails" \
    fails_with "^FAIL issuer_empty: .*log lacks 'requires RETICORA_OIDC_ISSUER_URL'"

reset
export STUB_issuer_key_missing='exit:1:{"msg":"invalid configuration","error":"RETICORA_ENVIRONMENT=staging requires RETICORA_OIDC_ISSUER_URL"}'
smoke run
check "issuer key removed: container starts (optional reference) fails" \
    fails_with '^FAIL issuer_key_missing: .*started although oidc-issuer-url is missing'

reset
export STUB_issuer_key_missing=pending
smoke run
check "issuer key removed: no state within the timeout fails" \
    fails_with '^FAIL issuer_key_missing: .*pending \(no decision within 2s\)'

reset
export STUB_RESTORE_BROKEN=1
smoke run
check "restore that leaves the pod broken fails" \
    fails_with '^FAIL restore after master_key_missing: not Ready within 2s: .*CreateContainerConfigError'

# ─── Run wal_archive: broken WAL archiving and backups must fail ────────────

reset
export STUB_DB_ROLLOUT_RC=1
smoke run ready wal_archive
fails_with '^FAIL wal_archive: reticora-db is not Ready within 2s: error: timed out waiting for the condition' &&
    ! called 'exec reticora-db-0' && ! called 'create job' && has '^kind smoke: 1 passed, 1 failed$' &&
    [ -s "$TMP_ROOT/artifacts/wal_archive.txt" ]
check "wal_archive: reticora-db not Ready fails, nothing else runs, protocol written"

reset
export STUB_CHECK_RC=82
smoke run ready wal_archive
check "wal_archive: pgbackrest check rc != 0 fails" \
    fails_with '^FAIL wal_archive: pgbackrest --stanza=reticora check in reticora-db-0, want rc 0: rc 82: ERROR: \[082\]: WAL segment .* was not archived.*\(no decision within 2s\)'

reset
export STUB_ARCHIVE=stuck
smoke run ready wal_archive
check "wal_archive: archived_count that does not rise fails" \
    fails_with '^FAIL wal_archive: archived_count 2 does not rise to segment 000000010000000000000004 within 2s after pg_switch_wal\(\): archived_count 2, .*no decision within 2s'

reset
export STUB_ARCHIVE=other
smoke run ready wal_archive
check "wal_archive: a rising count without the switched segment fails" \
    fails_with '^FAIL wal_archive: archived_count 2 does not rise to segment 000000010000000000000004 .*archived_count 3, last_archived_wal 000000010000000000000003'

reset
export STUB_ARCHIVE=nocount
smoke run ready wal_archive
check "wal_archive: the switched segment archived without a rising count fails" \
    fails_with '^FAIL wal_archive: archived_count 2 does not rise .*archived_count 2, last_archived_wal 000000010000000000000004'

reset
export STUB_FULL_JOB=failed
smoke run ready wal_archive
check "wal_archive: a failed job of the CronJob fails" \
    fails_with '^FAIL wal_archive: full backup by a job of the CronJob reticora-pgbackrest-full: job kind-smoke-full FailureTarget,Failed, rc 1; full backups before 0'

reset
export STUB_FULL_JOB=failed STUB_FULL_LISTED=always
smoke run ready wal_archive
check "wal_archive: a failed job of the CronJob fails although a new full backup appears" \
    fails_with '^FAIL wal_archive: full backup by a job of the CronJob reticora-pgbackrest-full: job kind-smoke-full FailureTarget,Failed, rc 1;'

reset
export STUB_FULL_JOB=running
smoke run ready wal_archive
check "wal_archive: a job of the CronJob that does not finish fails" \
    fails_with '^FAIL wal_archive: full backup .*job kind-smoke-full running, rc - \(no decision within 2s\)'

reset
export STUB_FULL_LISTED=0
smoke run ready wal_archive
check "wal_archive: a completed job without a new full backup in pgbackrest info fails" \
    fails_with '^FAIL wal_archive: full backup .*Complete, rc 0; full backups before 0, after 0 ; log: '

reset
export STUB_WRONG_JOB=complete
smoke run ready wal_archive
check "wal_archive: a backup that succeeds with the wrong cipher pass fails" \
    fails_with '^FAIL wal_archive: wrong cipher pass at the repository host: the backup succeeded: job kind-smoke-wrong-cipher SuccessCriteriaMet,Complete, rc 0'

reset
export STUB_WRONG_RC=95 STUB_WRONG_LOG="ERROR: [095]: unable to load info file '/reticora/backup/reticora/backup.info' or '/reticora/backup/reticora/backup.info.copy':
       CryptoError: unable to flush: [101077092] bad decrypt
       HINT: is or was the repo encrypted?"
smoke run ready wal_archive
check "wal_archive: the wrong-cipher job failing with CryptoError on backup.info passes" \
    has '^ok   wal_archive: wrong cipher pass at the repository host: .*rc 95; backup.info not decrypted: CryptoError: unable to flush$'

reset
export STUB_WRONG_LOG='ERROR: [049]: unable to connect to reticora-db:8432: Connection refused'
smoke run ready wal_archive
check "wal_archive: the wrong-cipher job failing for another reason fails" \
    fails_with '^FAIL wal_archive: wrong cipher pass at the repository host, want rc != 0 and backup.info not decrypted: .*rc 29; log: ERROR: \[049\]: unable to connect'

reset
export STUB_WRONG_LOG="ERROR: [055]: unable to load info file '/reticora/backup/reticora/backup.info' or '/reticora/backup/reticora/backup.info.copy':
       FileMissingError: unable to open missing file '/reticora/backup/reticora/backup.info' for read"
smoke run ready wal_archive
check "wal_archive: the wrong-cipher job failing on a missing backup.info fails" \
    fails_with '^FAIL wal_archive: wrong cipher pass .*want rc != 0 and backup.info not decrypted: .*FileMissingError'

reset
export STUB_WRONG_LOG="ERROR: [029]: unable to load info file '/reticora/archive/reticora/archive.info' or '/reticora/archive/reticora/archive.info.copy':
       FormatError: invalid format in section 'db'"
smoke run ready wal_archive
check "wal_archive: the wrong-cipher job failing on another info file fails" \
    fails_with '^FAIL wal_archive: wrong cipher pass .*want rc != 0 and backup.info not decrypted: .*archive.info'

reset
export STUB_WRONG_RC=0
smoke run ready wal_archive
check "wal_archive: a failed wrong-cipher job whose pod ended with rc 0 fails" \
    fails_with '^FAIL wal_archive: wrong cipher pass .*want rc != 0 and backup.info not decrypted: job kind-smoke-wrong-cipher FailureTarget,Failed, rc 0;'

reset
export STUB_WRONG_JOB=running
smoke run ready wal_archive
check "wal_archive: a wrong-cipher job without a decision fails" \
    fails_with '^FAIL wal_archive: wrong cipher pass .*want rc != 0 and backup.info not decrypted: job kind-smoke-wrong-cipher running, rc - \(no decision within 2s\)'

reset
export STUB_NO_CIPHER_ENV=1
smoke run ready wal_archive
fails_with '^FAIL wal_archive: wrong cipher pass at the repository host: creating the job failed: the CronJob passes no PGBACKREST_REPO1_CIPHER_PASS' &&
    ! called 'create -f -'
check "wal_archive: a CronJob without the cipher pass reference fails (no job created)"

reset
smoke run wal_archive
rc_is 0 && has '^ok   ready' && [ "$(grep -c '^ok   wal_archive' <<<"$OUT")" -eq 4 ] && ! has '^(ok|FAIL) +(master_key|issuer|restore)' &&
    has '^kind smoke: 5 passed, 0 failed$'
check "selected case wal_archive: ready first, then its four checks"

# ─── Run: positive case ─────────────────────────────────────────────────────

reset
export STUB_ok='exit:1:{"msg":"failed to connect to database"}'
smoke run
fails_with '^FAIL ready: the pod reticora-server is not Ready within 2s: .*CrashLoopBackOff' &&
    has 'negative cases skipped' && ! has '^(ok|FAIL) +(master_key|issuer|restore)' &&
    [ "$(grep -c '^ok   wal_archive' <<<"$OUT")" -eq 4 ] && has '^kind smoke: 4 passed, 1 failed$'
check "pod never Ready: positive case fails, negative cases skipped, wal_archive runs anyway"

reset
export STUB_READYZ_CODE=503
export STUB_READYZ_BODY='{"status":"not_ready","checks":{"database":"ok","nats":"unavailable","redis":"ok"}}'
smoke run
check "Ready but /readyz 503 fails" \
    fails_with '^FAIL ready: .*/readyz 503 \{"status":"not_ready".*want 200'

reset
export STUB_READYZ_BODY='{"status":"ready","checks":{"database":"ok","redis":"ok"}}'
smoke run
check "/readyz 200 without the nats check fails" \
    fails_with '^FAIL ready: .*/readyz 200 .*want 200 with database, redis and nats ok'

reset
export STUB_GET_RC=1
smoke run
check "unreachable API server fails" \
    fails_with '^FAIL ready: .*server pods cannot be listed: kubectl get pods: The connection to the server was refused'

reset
smoke run ready issuer_empty
rc_is 0 && has '^ok   issuer_empty' && ! has '^(ok|FAIL) +(master_key|wal_archive)' && has '^kind smoke: 3 passed, 0 failed$'
check "selected cases: ready and issuer_empty only"

reset
smoke run no_such_case
rc_is 2 && has 'unknown case no_such_case'
check "unknown case: exit 2"

reset
smoke
rc_is 2 && has 'Usage:'
check "no mode: usage, exit 2"

# Only a kind cluster: a real staging cluster would get the test-only master
# key and lose its keys.
for context in prod-cluster kind- ""; do
    for mode in run setup; do
        reset
        export STUB_CONTEXT="$context"
        smoke "$mode"
        rc_is 2 && has "refusing kubectl context '$context'" && [ "$(grep -cv '^config current-context$' "$STUB_DIR/calls")" -eq 0 ]
        check "$mode refuses the context '${context}' before touching the cluster"
    done
done

# ─── Setup ──────────────────────────────────────────────────────────────────

reset
smoke setup
rc_is 0 && has '^setup done$'
check "setup: exit 0"
grep -qx '# rendered tests/k8s/fixtures' "$STUB_DIR/applied" &&
    called 'create secret generic reticora-session-key --from-file=private.pem=.*/private.pem --dry-run=client -o yaml' &&
    grep -q 'name: reticora-session-key' "$STUB_DIR/applied"
check "setup: fixtures applied, session key secret created and applied"
missing=""
for d in postgres redis nats minio; do
    called "rollout status deployment/$d --timeout=2s" || missing="$missing $d"
done
[ -z "$missing" ]
check "setup: waits for postgres, redis, nats and minio"
grep -qxF -- "-path $ROOT/backend/migrations -database postgres://reticora:$FIXTURE_ADMIN_PW@127.0.0.1:18080/reticora?sslmode=disable up" "$STUB_DIR/migrate" &&
    called 'port-forward svc/postgres :5432'
check "setup: migrates backend/migrations with the bootstrap superuser of the fixture over the port-forward"
called 'exec -i deployment/postgres -- psql -U reticora -d reticora -v ON_ERROR_STOP=1 -v login=reticora_runtime -v pw=kind-smoke-runtime' &&
    grep -q 'LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD %L' "$STUB_DIR/exec.sql" &&
    grep -qx 'GRANT reticora_app TO :"login";' "$STUB_DIR/exec.sql"
check "setup: runtime login of reticora-db-runtime, NOSUPERUSER NOBYPASSRLS, member of reticora_app"
migrate_at=$(line_of 'port-forward svc/postgres')
login_at=$(line_of 'exec -i deployment/postgres')
[ -n "$migrate_at" ] && [ -n "$login_at" ] && [ "$migrate_at" -lt "$login_at" ]
check "setup: the migration runs before the runtime login"
# wal_archive: test CA and certificates, generated per run.
grep -q '^req -x509 -newkey rsa:2048 -nodes -keyout .*/ca.key -out .*/ca.crt -days 2 -subj /CN=kind-smoke test CA$' "$STUB_DIR/openssl" &&
    grep -q '^req -new .* -subj /CN=pgbackrest-client$' "$STUB_DIR/openssl" &&
    grep -q '^req -new .* -subj /CN=reticora-db$' "$STUB_DIR/openssl" &&
    grep -qx '     extfile: subjectAltName=DNS:reticora-db,DNS:reticora-db.reticora-staging.svc' "$STUB_DIR/openssl" &&
    grep -qx '     extfile: subjectAltName=DNS:minio,DNS:minio.reticora-staging.svc,DNS:localhost,IP:127.0.0.1' "$STUB_DIR/openssl" &&
    [ "$(grep -c '^     extfile: extendedKeyUsage=serverAuth$' "$STUB_DIR/openssl")" -eq 2 ] &&
    [ "$(grep -c '^     extfile: extendedKeyUsage=clientAuth$' "$STUB_DIR/openssl")" -eq 1 ] &&
    [ "$(grep -c '^x509 -req .* -CA .*/ca.crt -CAkey .*/ca.key ' "$STUB_DIR/openssl")" -eq 3 ] &&
    grep -qx 'rand -hex 32' "$STUB_DIR/openssl"
check "setup: test CA, MinIO and reticora-db server certificates, client certificate pgbackrest-client, random cipher pass"
grep -q '^create secret generic kind-smoke-minio-tls --from-file=tls.crt=.*/minio.crt --from-file=tls.key=.*/minio.key --from-file=ca.crt=.*/ca.crt --dry-run=client -o yaml$' "$STUB_DIR/secrets" &&
    keys=$(grep 'create secret generic reticora-pgbackrest ' "$STUB_DIR/secrets" | grep -oE -- '--from-file=[a-z0-9.-]+=[^ ]+' | sed -E 's/^--from-file=//; s/=.*\//=/' | tr '\n' ' ') &&
    [ "$keys" = "cipher-pass=cipher-pass s3-key=s3-key s3-key-secret=s3-key-secret ca.crt=ca.crt tls.crt=client.crt tls.key=client.key server.crt=server.crt server.key=server.key " ] &&
    ! grep -q -- '--from-literal' "$STUB_DIR/secrets" &&
    grep -q 'name: kind-smoke-minio-tls' "$STUB_DIR/applied" && grep -q 'name: reticora-pgbackrest' "$STUB_DIR/applied"
check "setup: secrets kind-smoke-minio-tls and reticora-pgbackrest (all keys of the components, from files) applied"
tls_at=$(line_of 'create secret generic kind-smoke-minio-tls')
minio_at=$(line_of 'rollout status deployment/minio')
bucket_at=$(line_of 'exec -i deployment/minio -- sh -s')
[ -n "$tls_at" ] && [ -n "$minio_at" ] && [ -n "$bucket_at" ] && [ "$tls_at" -lt "$minio_at" ] && [ "$minio_at" -lt "$bucket_at" ]
check "setup: MinIO's certificate before waiting for MinIO, the bucket after"
# shellcheck disable=SC2016 # the script's own variables, literally
grep -qx 'export MC_HOST_kind="https://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@localhost:9000"' "$STUB_DIR/minio.sh" &&
    grep -qx 'cp /certs/CAs/ca.crt "$MC_CONFIG_DIR/certs/CAs/kind-smoke-ca.crt"' "$STUB_DIR/minio.sh" &&
    grep -qx 'mc mb --ignore-existing kind/reticora-backup' "$STUB_DIR/minio.sh" &&
    grep -qx 'mc version enable kind/reticora-backup' "$STUB_DIR/minio.sh" && grep -qx 'set -e' "$STUB_DIR/minio.sh"
check "setup: bucket reticora-backup with versioning, mc over TLS with the test CA and the pod's credentials"

reset
export STUB_BUCKET_RC=1
smoke setup
[ "$RC" -ne 0 ] && has '^creating the bucket reticora-backup failed$' && ! has 'setup done'
check "setup: failing bucket creation fails"

for step in req x509 rand; do
    reset
    export STUB_OPENSSL_FAIL=$step
    smoke setup
    [ "$RC" -ne 0 ] && has '^openssl' && ! called 'rollout status' && ! grep -q 'reticora-pgbackrest' "$STUB_DIR/secrets" 2>/dev/null
    check "setup: failing openssl $step fails before the wait, no secret reticora-pgbackrest"
done

reset
export STUB_MIGRATE_RC=1
smoke setup
[ "$RC" -ne 0 ] && has '^migrate failed$' && ! has 'setup done'
check "setup: failing migration fails"

reset
export STUB_ROLLOUT_RC=1
smoke setup
[ "$RC" -ne 0 ] && ! has 'migrate the database'
check "setup: dependency not ready fails"

for dsn in 'postgres://postgres:5432/reticora' 'postgres://reticora_runtime@postgres:5432/reticora'; do
    reset
    export STUB_RUNTIME_DSN="$dsn"
    smoke setup
    [ "$RC" -ne 0 ] && has 'reticora-db-runtime has no user:password' && [ ! -e "$STUB_DIR/exec.sql" ]
    check "setup: runtime DSN $dsn without user:password fails"
done

reset
export STUB_EXEC_RC=3
smoke setup
[ "$RC" -ne 0 ] && has 'creating the runtime login failed'
check "setup: failing runtime login fails"

echo
echo "kind-smoke.test.sh: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
