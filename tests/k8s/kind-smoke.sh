#!/usr/bin/env bash
#
# kind-smoke.sh — runtime smoke of the Kubernetes manifests on a cluster
# (OPS-03, WP-441). The job k8s-manifests of ci.yml only renders them; this
# script observes the staging overlay on a cluster (kind in the workflow
# k8s-smoke) against the test dependencies of tests/k8s/fixtures.
#
# Cases of `run`, in this order:
#   ready               the staging overlay is applied, the pod
#                       reticora-server is Ready within READY_TIMEOUT (5 min)
#                       and /readyz answers 200 over kubectl port-forward
#                       with database, redis and nats ok
#   master_key_missing  key master-key removed from the secret
#                       reticora-secrets: CreateContainerConfigError naming
#                       the key
#   master_key_empty    master-key empty: the container ends with exit 1 and
#                       the log 'requires RETICORA_MASTER_KEY'
#   issuer_empty        oidc-issuer-url empty in the ConfigMap
#                       reticora-config: exit 1 with 'requires
#                       RETICORA_OIDC_ISSUER_URL' (config.loader.str treats
#                       an empty value as unset)
#   issuer_key_missing  key oidc-issuer-url removed from reticora-config:
#                       CreateContainerConfigError naming the key
#   wal_archive         WAL archiving of the database (OPS-04, NFR-05,
#                       WP-444): tests/k8s/fixtures/overlay-wal (base plus
#                       the components postgres and backup, MinIO as the
#                       repository) is applied and reticora-db becomes Ready;
#                       'pgbackrest --stanza=reticora check' in the database
#                       ends with rc 0; pg_stat_archiver.archived_count rises
#                       within WAL_TIMEOUT (120 s) after SELECT
#                       pg_switch_wal() and last_archived_wal reaches the
#                       switched segment; a job created from the CronJob
#                       reticora-pgbackrest-full completes and adds a full
#                       backup (pgbackrest info); the same job with a wrong
#                       cipher pass at the repository host (only in the job,
#                       the secret stays) fails with rc != 0 because it
#                       cannot decrypt backup.info ('unable to load info
#                       file', FormatError or CryptoError)
# A negative case changes the object, deletes the server pods and waits for
# the state of the new pod. Afterwards the fixtures are applied again and the
# new pod must be Ready again ("restore after <case>"), so every case starts
# from the positive state. When the positive case fails, the negative cases
# are skipped: they mean nothing without a working baseline. wal_archive does
# not depend on the server and runs anyway.
#
# Usage:
#   tests/k8s/kind-smoke.sh setup        apply tests/k8s/fixtures, create the
#                                        session key, the test CA with the
#                                        certificates and the secrets of
#                                        wal_archive, wait for the
#                                        dependencies, migrate the database,
#                                        create the runtime login and the
#                                        versioned bucket in MinIO
#   tests/k8s/kind-smoke.sh run [CASE…]  the cases above (default all; ready
#                                        always runs first)
#   tests/k8s/kind-smoke.sh cases        list the cases
# setup and run refuse every kubectl context but a kind cluster (kind-*).
# Exit code: 0 all passed, 1 a case or the setup failed, 2 usage or a
# context other than kind.
#
# Environment:
#   KUBECTL, KUSTOMIZE, MIGRATE, OPENSSL, CURL  binaries (the self-test
#                       tests/k8s/kind-smoke.test.sh uses stubs)
#   READY_TIMEOUT       seconds until the pod is Ready (default 300)
#   STATE_TIMEOUT       seconds until a negative case shows its state
#                       (default 180)
#   DEPS_TIMEOUT        seconds for each dependency of setup (default 300)
#   WAL_TIMEOUT         seconds until archived_count rises (default 120)
#   JOB_TIMEOUT         seconds for a backup job of wal_archive (default 600)
#   POLL_INTERVAL       seconds between two observations (default 5)
#   ARTIFACT_DIR        when set, the pods, describe and logs of every case
#                       are written there (artifact of the workflow)
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
KUBECTL="${KUBECTL:-kubectl}"
KUSTOMIZE="${KUSTOMIZE:-kustomize}"
MIGRATE="${MIGRATE:-migrate}"
OPENSSL="${OPENSSL:-openssl}"
CURL="${CURL:-curl}"
FIXTURES="$ROOT/tests/k8s/fixtures"
OVERLAY="$ROOT/deploy/k8s/overlays/staging"
NAMESPACE=reticora-staging
SELECTOR='app.kubernetes.io/name=reticora-cmdb,app.kubernetes.io/component=server'
READY_TIMEOUT="${READY_TIMEOUT:-300}"
STATE_TIMEOUT="${STATE_TIMEOUT:-180}"
DEPS_TIMEOUT="${DEPS_TIMEOUT:-300}"
WAL_TIMEOUT="${WAL_TIMEOUT:-120}"
JOB_TIMEOUT="${JOB_TIMEOUT:-600}"
POLL_INTERVAL="${POLL_INTERVAL:-5}"
ARTIFACT_DIR="${ARTIFACT_DIR:-}"
CASES=(ready master_key_missing master_key_empty issuer_empty issuer_key_missing wal_archive)
WAL_OVERLAY="$ROOT/tests/k8s/fixtures/overlay-wal"
DB_POD=reticora-db-0
WRONG_CIPHER_PASS=kind-smoke-wrong-cipher-pass

PASSED=0
FAILED=0
LAST=""
PF_PID=""
PF_PORT=""
PF_LOG=""

ok() { PASSED=$((PASSED + 1)); printf 'ok   %s\n' "$*"; }
fail() { FAILED=$((FAILED + 1)); printf 'FAIL %s\n' "$*"; }
note() { printf '     %s\n' "$*"; }

k() { "$KUBECTL" -n "$NAMESPACE" "$@"; }

# require_kind_context refuses every kubectl context but a kind cluster
# (kind-<name>): setup applies test-only secrets to the namespace of the
# staging overlay, and the cases remove and empty keys of reticora-secrets
# and reticora-config there.
require_kind_context() {
    local context
    context=$("$KUBECTL" config current-context 2>/dev/null) || context=""
    case $context in
        kind-?*) echo "kubectl context $context" ;;
        *)
            echo "refusing kubectl context '$context': kind-smoke.sh writes test-only secrets to $NAMESPACE and changes them; it runs against a kind cluster (context kind-<name>) only" >&2
            exit 2
            ;;
    esac
}

# apply_dir DIR renders DIR with the pinned kustomize (the version of the job
# k8s-manifests, not the one built into kubectl) and applies it.
apply_dir() {
    local rendered
    rendered=$("$KUSTOMIZE" build "$1") || { echo "kustomize build $1 failed"; return 1; }
    "$KUBECTL" apply -f - <<<"$rendered"
}

# port_forward TARGET PORT starts kubectl port-forward to a free local port
# in the background and sets PF_PORT; port_forward_stop ends it.
port_forward() {
    PF_LOG=$(mktemp)
    k port-forward "$1" ":$2" >"$PF_LOG" 2>&1 &
    PF_PID=$!
    local i
    for ((i = 0; i < 150; i++)); do
        PF_PORT=$(sed -nE 's/^Forwarding from 127\.0\.0\.1:([0-9]+) .*/\1/p' "$PF_LOG" | head -n 1)
        [ -n "$PF_PORT" ] && return 0
        kill -0 "$PF_PID" 2>/dev/null || break
        sleep 0.2
    done
    echo "kubectl port-forward $1 did not start: $(tr '\n' ' ' <"$PF_LOG")"
    port_forward_stop
    return 1
}

port_forward_stop() {
    if [ -n "$PF_PID" ]; then
        kill "$PF_PID" 2>/dev/null
        wait "$PF_PID" 2>/dev/null
    fi
    [ -n "$PF_LOG" ] && rm -f "$PF_LOG"
    PF_PID="" PF_LOG=""
}
trap port_forward_stop EXIT

# ─── Observation of the server pods ─────────────────────────────────────────

# pod_lines prints one line per server pod that is not being deleted, tab
# separated: name, Ready condition, "running" or "-", waiting reason, waiting
# message, exit code of the current or last terminated container ("-" when
# it never terminated). No field is empty, so read splits them reliably.
pod_lines() {
    local json err
    err=$(mktemp)
    if ! json=$(k get pods -l "$SELECTOR" -o json 2>"$err"); then
        echo "kubectl get pods: $(tr '\n' ' ' <"$err")" >&2
        rm -f "$err"
        return 1
    fi
    rm -f "$err"
    jq -r '.items[]? | select(.metadata.deletionTimestamp == null)
        | (.status.containerStatuses // [] | map(select(.name == "server")) | first // {}) as $c
        | [.metadata.name,
           ([.status.conditions[]? | select(.type == "Ready") | .status][0] // "False"),
           (if $c.state.running then "running" else "-" end),
           ($c.state.waiting.reason // "-"),
           (($c.state.waiting.message // "-") | gsub("[\t\n]"; " ")),
           (($c.state.terminated.exitCode // $c.lastState.terminated.exitCode) // "-" | tostring)]
        | @tsv' <<<"$json"
}

# pod_logs NAME prints the logs of the last terminated and of the current
# container of the server.
pod_logs() {
    k logs "$1" -c server --previous --tail=50 2>/dev/null
    k logs "$1" -c server --tail=50 2>/dev/null
}

# observe WANT [ARG] looks at the server pods once and prints what it saw.
#   ready               every server pod (at least one) is Ready
#   config-error KEY    a pod waits in CreateContainerConfigError naming KEY
#   exit1 MESSAGE       a container ended with exit code 1 and logged MESSAGE
# Returns 0 when WANT holds, 1 when it can no longer hold (a negative case
# whose container started or became Ready), 2 to keep waiting.
observe() {
    local want=$1 arg=${2:-}
    local lines name ready running reason message code logs state seen="" hit="" bad="" all_ready=1
    lines=$(pod_lines 2>&1) || { echo "server pods cannot be listed: $lines"; return 2; }
    [ -n "$lines" ] || { echo "no server pod"; return 2; }
    while IFS=$'\t' read -r name ready running reason message code; do
        if [ "$ready" = True ]; then
            state=Ready
        elif [ "$reason" != - ]; then
            state="$reason: $message"
        elif [ "$running" = running ]; then
            state="running, not Ready"
        else
            state=pending
        fi
        [ "$code" != - ] && state="$state (exit code $code)"
        seen="${seen:+$seen; }$name $state"
        [ "$ready" = True ] || all_ready=0
        case $want in
            config-error)
                if [ "$ready" = True ] || [ "$running" = running ] || [ "$code" != - ]; then
                    bad="$name started although $arg is missing: $state"
                elif [ "$reason" = CreateContainerConfigError ] && [[ $message == *"$arg"* ]]; then
                    hit="$name CreateContainerConfigError: $message"
                fi
                ;;
            exit1)
                if [ "$ready" = True ]; then
                    bad="$name became Ready instead of ending with exit 1"
                elif [ "$code" = 1 ]; then
                    logs=$(pod_logs "$name")
                    if grep -qF -- "$arg" <<<"$logs"; then
                        hit="$name exit code 1, log: $(grep -F -- "$arg" <<<"$logs" | head -n 1)"
                    else
                        seen="$seen; log lacks '$arg': $(tail -n 3 <<<"$logs" | tr '\n' ' ')"
                    fi
                fi
                ;;
        esac
    done <<<"$lines"
    if [ -n "$bad" ]; then echo "$bad"; return 1; fi
    if [ -n "$hit" ]; then echo "$hit"; return 0; fi
    if [ "$want" = ready ] && [ "$all_ready" -eq 1 ]; then echo "$seen"; return 0; fi
    echo "$seen"
    return 2
}

# poll TIMEOUT FUNCTION [ARG…] repeats FUNCTION, which returns like observe
# (0 holds, 1 can no longer hold, 2 keep waiting), until it decides or
# TIMEOUT seconds have passed; its last output is left in LAST.
poll() {
    local timeout=$1 deadline rc
    shift
    deadline=$((SECONDS + timeout))
    while :; do
        LAST=$("$@")
        rc=$?
        [ "$rc" -eq 2 ] || return "$rc"
        if [ "$SECONDS" -ge "$deadline" ]; then
            LAST="$LAST (no decision within ${timeout}s)"
            return 1
        fi
        sleep "$POLL_INTERVAL"
    done
}

# await TIMEOUT WANT [ARG] repeats observe until it decides or TIMEOUT
# seconds have passed; the last observation is left in LAST.
await() {
    local timeout=$1
    shift
    poll "$timeout" observe "$@"
}

# restart_server deletes the server pods; the ReplicaSet creates new ones,
# which resolve the secret and ConfigMap references anew.
restart_server() {
    k delete pod -l "$SELECTOR" --wait=true --timeout=120s >/dev/null
}

# snapshot NAME writes the state of the server pods to ARTIFACT_DIR.
snapshot() {
    [ -n "$ARTIFACT_DIR" ] || return 0
    mkdir -p "$ARTIFACT_DIR" || return 0
    local name
    {
        echo "== kubectl get pods"
        k get pods -l "$SELECTOR" -o wide
        echo "== kubectl describe pods"
        k describe pods -l "$SELECTOR"
        for name in $(k get pods -l "$SELECTOR" -o jsonpath='{.items[*].metadata.name}'); do
            echo "== logs $name"
            k logs "$name" -c server --tail=100
            echo "== previous logs $name"
            k logs "$name" -c server --previous --tail=100
        done
    } >"$ARTIFACT_DIR/$1.txt" 2>&1
}

# ─── Cases ──────────────────────────────────────────────────────────────────

# readyz checks /readyz of the Service over kubectl port-forward.
readyz() {
    local body code
    port_forward svc/reticora-server 80 || return 1
    body=$(mktemp)
    code=$("$CURL" -sS -o "$body" -w '%{http_code}' --max-time 10 "http://127.0.0.1:$PF_PORT/readyz" 2>&1)
    LAST="/readyz $code $(tr -d '\n' <"$body")"
    port_forward_stop
    if [ "$code" != 200 ] ||
        ! jq -e '.status == "ready" and .checks.database == "ok" and .checks.redis == "ok" and .checks.nats == "ok"' "$body" >/dev/null 2>&1; then
        rm -f "$body"
        return 1
    fi
    rm -f "$body"
}

case_ready() {
    local start=$SECONDS out
    if ! out=$(apply_dir "$OVERLAY" 2>&1); then
        fail "ready: applying $OVERLAY failed: $out"
        return 1
    fi
    if ! await "$READY_TIMEOUT" ready; then
        fail "ready: the pod reticora-server is not Ready within ${READY_TIMEOUT}s: $LAST"
        snapshot ready
        return 1
    fi
    local pods=$LAST elapsed=$((SECONDS - start))
    if ! readyz; then
        fail "ready: $pods after ${elapsed}s, but $LAST (want 200 with database, redis and nats ok)"
        snapshot ready
        return 1
    fi
    ok "ready: $pods after ${elapsed}s; $LAST"
    snapshot ready
}

# negative NAME DESCRIPTION WANT ARG KUBECTL-ARGS… changes an object with
# kubectl (the arguments after ARG), restarts the server and awaits WANT.
negative() {
    local name=$1 description=$2 want=$3 arg=$4 out
    shift 4
    if ! out=$(k "$@" 2>&1); then
        fail "$name: $description: kubectl $* failed: $out"
        return 1
    fi
    if ! out=$(restart_server 2>&1); then
        fail "$name: $description: deleting the server pods failed: $out"
        return 1
    fi
    if await "$STATE_TIMEOUT" "$want" "$arg"; then
        ok "$name: $description -> $LAST"
    else
        fail "$name: $description: want $want '$arg', got: $LAST"
    fi
    snapshot "$name"
}

# restore applies the fixtures again (secret and ConfigMap with their test
# values) and awaits the positive state.
restore() {
    local name=$1 out start=$SECONDS
    if ! out=$(apply_dir "$FIXTURES" 2>&1); then
        fail "restore after $name: applying the fixtures failed: $out"
        return 1
    fi
    if ! out=$(restart_server 2>&1); then
        fail "restore after $name: deleting the server pods failed: $out"
        return 1
    fi
    if await "$READY_TIMEOUT" ready; then
        ok "restore after $name: $LAST after $((SECONDS - start))s"
    else
        fail "restore after $name: not Ready within ${READY_TIMEOUT}s: $LAST"
        snapshot "restore-$name"
    fi
}

# ─── Case wal_archive: WAL archiving and backups of reticora-db (WP-444) ────

# db CONTAINER COMMAND… runs COMMAND in a container of the database pod.
db() {
    local container=$1
    shift
    k exec "$DB_POD" -c "$container" -- "$@"
}

# sql QUERY prints the rows of QUERY, as the bootstrap superuser over the
# socket.
sql() { db postgres psql -U reticora -d reticora -v ON_ERROR_STOP=1 -qAt -c "$1"; }

# wal_check runs 'pgbackrest check' in the database: it switches the WAL and
# waits until the segment is in the repository. Until the container
# pgbackrest has created the stanza it fails, so it is repeated (2).
wal_check() {
    local out rc=0
    out=$(db postgres pgbackrest --stanza=reticora check 2>&1) || rc=$?
    echo "rc $rc: $(tail -n 2 <<<"$out" | tr '\n' ' ')"
    [ "$rc" -eq 0 ] || return 2
}

# archiver SEGMENT BEFORE prints pg_stat_archiver; it holds once
# archived_count exceeds BEFORE and last_archived_wal has reached SEGMENT.
archiver() {
    local row count last failed reached
    row=$(sql "SELECT archived_count, coalesce(last_archived_wal, '-'), failed_count, coalesce(last_archived_wal COLLATE \"C\" >= '$1', false) FROM pg_stat_archiver" 2>&1) ||
        { echo "pg_stat_archiver unreadable: $row"; return 2; }
    IFS='|' read -r count last failed reached <<<"$row"
    echo "archived_count $count, last_archived_wal $last, failed_count $failed"
    [[ $count =~ ^[0-9]+$ ]] && [ "$count" -gt "$2" ] && [ "$reached" = t ] || return 2
}

# job_state NAME prints the conditions of the job NAME and the exit codes of
# its pods ("rc 0", "rc 1,0"); 0 complete, 1 failed, 2 still running.
job_state() {
    local json conditions codes
    json=$(k get job "$1" -o json 2>&1) || { echo "kubectl get job $1: $json"; return 2; }
    conditions=$(jq -r '[.status.conditions[]? | select(.status == "True") | .type] | join(",")' <<<"$json")
    codes=$(k get pods -l "batch.kubernetes.io/job-name=$1" -o json 2>/dev/null |
        jq -r '[.items[]?.status.containerStatuses[]? | select(.name == "pgbackrest") | .state.terminated.exitCode // empty] | map(tostring) | join(",")')
    echo "job $1 ${conditions:-running}, rc ${codes:--}"
    case ",$conditions," in
        *,Complete,*) return 0 ;;
        *,Failed,*) return 1 ;;
    esac
    return 2
}

# job_log NAME prints the log of the job's container, printable ASCII only
# (a wrongly decrypted info file shows up in the error).
job_log() { k logs "job/$1" -c pgbackrest --tail=30 2>&1 | LC_ALL=C tr -cd '\11\12\40-\176'; }

# full_backups prints the number of full backups of the stanza and their
# labels, read in the database pod (pgbackrest info).
full_backups() {
    local json
    json=$(db pgbackrest pgbackrest --stanza=reticora --output=json info 2>&1) ||
        { echo "pgbackrest info failed: $(tail -n 2 <<<"$json" | tr '\n' ' ')"; return 1; }
    jq -er '[.[0].backup[]? | select(.type == "full") | .label] | "\(length) \(join(","))"' <<<"$json" 2>/dev/null ||
        { echo "pgbackrest info printed no stanza: $(head -c 200 <<<"$json")"; return 1; }
}

# wrong_cipher_job NAME creates the job of the CronJob with a wrong cipher
# pass at the repository host: PGBACKREST_REPO1_CIPHER_PASS becomes a literal
# value in this job only (the secret, and so the database, keep the right
# one), and no retry.
wrong_cipher_job() {
    local json
    json=$(k create job "$1" --from=cronjob/reticora-pgbackrest-full --dry-run=client -o json) || return 1
    json=$(jq --arg pass "$WRONG_CIPHER_PASS" '.spec.backoffLimit = 0
        | .spec.template.spec.containers |= map(if .name == "pgbackrest" then .env |= map(
            if .name == "PGBACKREST_REPO1_CIPHER_PASS" then {name, value: $pass} else . end) else . end)' <<<"$json") || return 1
    jq -e --arg pass "$WRONG_CIPHER_PASS" '[.spec.template.spec.containers[] | select(.name == "pgbackrest")
        | .env[]? | select(.name == "PGBACKREST_REPO1_CIPHER_PASS" and .value == $pass)] | length == 1' <<<"$json" >/dev/null ||
        { echo "the CronJob passes no PGBACKREST_REPO1_CIPHER_PASS to the container pgbackrest"; return 1; }
    k create -f - <<<"$json"
}

# wal_snapshot writes the state of reticora-db and of the backup jobs to
# ARTIFACT_DIR.
wal_snapshot() {
    [ -n "$ARTIFACT_DIR" ] || return 0
    mkdir -p "$ARTIFACT_DIR" || return 0
    local c j
    {
        echo "== kubectl get statefulsets,pods,jobs,pvc"
        k get statefulsets,pods,jobs,pvc -o wide
        echo "== kubectl describe pod $DB_POD"
        k describe pod "$DB_POD"
        echo "== kubectl describe pods of the backup jobs"
        k describe pods -l 'batch.kubernetes.io/job-name in (kind-smoke-full,kind-smoke-wrong-cipher)'
        for c in postgres pgbackrest; do
            echo "== logs $DB_POD -c $c"
            k logs "$DB_POD" -c "$c" --tail=200
        done
        for j in kind-smoke-full kind-smoke-wrong-cipher; do
            echo "== logs job/$j"
            k logs "job/$j" -c pgbackrest --tail=200
        done
    } >"$ARTIFACT_DIR/wal_archive.txt" 2>&1
}

case_wal_archive() {
    local out rc before seg start full_before full_after state codes logs
    if ! out=$(apply_dir "$WAL_OVERLAY" 2>&1); then
        fail "wal_archive: applying ${WAL_OVERLAY#"$ROOT"/} failed: $out"
        return 1
    fi
    if ! out=$(k rollout status statefulset/reticora-db --timeout="${READY_TIMEOUT}s" 2>&1); then
        fail "wal_archive: reticora-db is not Ready within ${READY_TIMEOUT}s: $(tail -n 1 <<<"$out")"
        wal_snapshot
        return 1
    fi
    note "wal_archive: ${WAL_OVERLAY#"$ROOT"/} applied, $DB_POD Ready"

    if poll "$STATE_TIMEOUT" wal_check; then
        ok "wal_archive: pgbackrest --stanza=reticora check in $DB_POD $LAST"
    else
        fail "wal_archive: pgbackrest --stanza=reticora check in $DB_POD, want rc 0: $LAST"
    fi

    # A write first: without new WAL since the last switch pg_switch_wal()
    # completes no segment.
    if ! before=$(sql "SELECT archived_count FROM pg_stat_archiver" 2>&1) || ! [[ $before =~ ^[0-9]+$ ]]; then
        fail "wal_archive: archived_count after pg_switch_wal(): pg_stat_archiver unreadable: $before"
    elif ! out=$(sql "CREATE TABLE IF NOT EXISTS kind_smoke_wal (at timestamptz); INSERT INTO kind_smoke_wal VALUES (now())" 2>&1) ||
        ! seg=$(sql "SELECT pg_walfile_name(pg_switch_wal())" 2>&1) || ! [[ $seg =~ ^[0-9A-F]{24}$ ]]; then
        fail "wal_archive: archived_count after pg_switch_wal(): the switch failed: $out ${seg:-}"
    else
        start=$SECONDS
        if poll "$WAL_TIMEOUT" archiver "$seg" "$before"; then
            ok "wal_archive: archived_count $before -> ${LAST#archived_count } within $((SECONDS - start))s after pg_switch_wal() (segment $seg)"
        else
            fail "wal_archive: archived_count $before does not rise to segment $seg within ${WAL_TIMEOUT}s after pg_switch_wal(): $LAST"
        fi
    fi

    # The full backup: a job of the CronJob as it is, as an operator starts
    # it by hand.
    full_before=$(full_backups) || full_before="0 ($full_before)"
    k delete job kind-smoke-full --ignore-not-found --wait=true >/dev/null 2>&1
    if ! out=$(k create job kind-smoke-full --from=cronjob/reticora-pgbackrest-full 2>&1); then
        fail "wal_archive: full backup: kubectl create job --from=cronjob/reticora-pgbackrest-full failed: $out"
    else
        poll "$JOB_TIMEOUT" job_state kind-smoke-full
        rc=$?
        state=$LAST
        codes=${state##*, rc }
        if [ "$rc" -eq 0 ] && [[ ,$codes, == *,0,* ]] && full_after=$(full_backups) && [ "${full_after%% *}" -gt "${full_before%% *}" ]; then
            ok "wal_archive: full backup by a job of the CronJob reticora-pgbackrest-full: $state; full backups ${full_before%% *} -> $full_after"
        else
            fail "wal_archive: full backup by a job of the CronJob reticora-pgbackrest-full: $state; full backups before ${full_before%% *}, after ${full_after:-?}; log: $(job_log kind-smoke-full | tail -n 3 | tr '\n' ' ')"
        fi
    fi

    # Negative case: the same job with a wrong cipher pass must not back up.
    k delete job kind-smoke-wrong-cipher --ignore-not-found --wait=true >/dev/null 2>&1
    if ! out=$(wrong_cipher_job kind-smoke-wrong-cipher 2>&1); then
        fail "wal_archive: wrong cipher pass at the repository host: creating the job failed: $out"
    else
        poll "$JOB_TIMEOUT" job_state kind-smoke-wrong-cipher
        rc=$?
        state=$LAST
        codes=${state##*, rc }
        logs=$(job_log kind-smoke-wrong-cipher)
        if [ "$rc" -eq 0 ]; then
            fail "wal_archive: wrong cipher pass at the repository host: the backup succeeded: $state"
        # Failed, or no decision: then codes ends with the timeout note and
        # does not match.
        elif [[ $codes =~ ^[0-9]+(,[0-9]+)*$ ]] && [[ ,$codes, != *,0,* ]] && grep -q "unable to load info file '[^']*/backup.info'" <<<"$logs" &&
            grep -qE '(Crypto|Format)Error' <<<"$logs"; then
            ok "wal_archive: wrong cipher pass at the repository host: $state; backup.info not decrypted: $(grep -m 1 -oE '(Crypto|Format)Error: [^:]*' <<<"$logs")"
        else
            fail "wal_archive: wrong cipher pass at the repository host, want rc != 0 and backup.info not decrypted: $state; log: $(tail -n 4 <<<"$logs" | tr '\n' ' ')"
        fi
    fi
    wal_snapshot
}

run_case() {
    case $1 in
        master_key_missing)
            negative "$1" "master-key removed from the secret reticora-secrets" config-error master-key \
                patch secret reticora-secrets --type=json -p '[{"op":"remove","path":"/data/master-key"}]'
            ;;
        master_key_empty)
            negative "$1" "master-key empty in the secret reticora-secrets" exit1 'requires RETICORA_MASTER_KEY' \
                patch secret reticora-secrets --type=merge -p '{"data":{"master-key":""}}'
            ;;
        issuer_empty)
            negative "$1" "oidc-issuer-url empty in the ConfigMap reticora-config" exit1 'requires RETICORA_OIDC_ISSUER_URL' \
                patch configmap reticora-config --type=merge -p '{"data":{"oidc-issuer-url":""}}'
            ;;
        issuer_key_missing)
            negative "$1" "oidc-issuer-url removed from the ConfigMap reticora-config" config-error oidc-issuer-url \
                patch configmap reticora-config --type=json -p '[{"op":"remove","path":"/data/oidc-issuer-url"}]'
            ;;
    esac
    restore "$1"
}

run_cases() {
    local c known known_case
    for c in "$@"; do
        known=0
        for known_case in "${CASES[@]}"; do [ "$c" = "$known_case" ] && known=1; done
        [ "$known" -eq 1 ] || { echo "unknown case $c (cases: ${CASES[*]})" >&2; exit 2; }
    done
    [ "$#" -gt 0 ] || set -- "${CASES[@]}"
    echo "kind smoke: namespace $NAMESPACE, overlay ${OVERLAY#"$ROOT"/}, cases $*"
    local ready=1
    case_ready || { ready=0; note "negative cases skipped: the positive case failed"; }
    for c in "$@"; do
        case $c in
            ready) ;;
            wal_archive) case_wal_archive ;;
            *) [ "$ready" -eq 0 ] || run_case "$c" ;;
        esac
    done
    echo "kind smoke: $PASSED passed, $FAILED failed"
    [ "$FAILED" -eq 0 ]
}

# ─── Setup ──────────────────────────────────────────────────────────────────

# secret_value NAME KEY prints a value of a secret in the namespace.
secret_value() {
    local json
    json=$(k get secret "$1" -o json) || return 1
    jq -er --arg key "$2" '.data[$key] // empty | @base64d' <<<"$json"
}

# apply_secret NAME ARGS… creates or replaces the secret NAME from the
# kubectl create secret generic ARGS.
apply_secret() {
    local name=$1 out
    shift
    out=$(k create secret generic "$name" "$@" --dry-run=client -o yaml) || { echo "kubectl create secret $name failed"; return 1; }
    k apply -f - <<<"$out"
}

# leaf_cert DIR NAME CN USAGE [SAN] creates DIR/NAME.key and DIR/NAME.crt,
# signed by the test CA in DIR.
leaf_cert() {
    local dir=$1 name=$2 cn=$3 usage=$4 san=${5:-}
    "$OPENSSL" req -new -newkey rsa:2048 -nodes -keyout "$dir/$name.key" -out "$dir/$name.csr" -subj "/CN=$cn" 2>/dev/null || return 1
    { echo "extendedKeyUsage=$usage"; [ -z "$san" ] || echo "subjectAltName=$san"; } >"$dir/$name.ext"
    "$OPENSSL" x509 -req -in "$dir/$name.csr" -CA "$dir/ca.crt" -CAkey "$dir/ca.key" -CAcreateserial -days 2 \
        -extfile "$dir/$name.ext" -out "$dir/$name.crt" 2>/dev/null
}

# wal_secrets DIR creates the material of the case wal_archive, generated
# per run: a test CA with the certificates of MinIO (the repository speaks S3
# over TLS only), of the pgBackRest TLS server reticora-db and of its client
# (common name pgbackrest-client, tls-server-auth of the component
# postgres); the secret kind-smoke-minio-tls; the secret reticora-pgbackrest
# that the components postgres and backup expect, with the MinIO keys of
# kind-smoke-minio as S3 key and a random cipher pass.
wal_secrets() {
    local dir=$1
    if ! "$OPENSSL" req -x509 -newkey rsa:2048 -nodes -keyout "$dir/ca.key" -out "$dir/ca.crt" -days 2 \
        -subj "/CN=kind-smoke test CA" 2>/dev/null ||
        ! leaf_cert "$dir" minio minio serverAuth "DNS:minio,DNS:minio.$NAMESPACE.svc,DNS:localhost,IP:127.0.0.1" ||
        ! leaf_cert "$dir" server reticora-db serverAuth "DNS:reticora-db,DNS:reticora-db.$NAMESPACE.svc" ||
        ! leaf_cert "$dir" client pgbackrest-client clientAuth; then
        echo "openssl: creating the test certificates failed"
        return 1
    fi
    if ! "$OPENSSL" rand -hex 32 | tr -d '\n' >"$dir/cipher-pass" || [ ! -s "$dir/cipher-pass" ]; then
        echo "openssl rand failed"
        return 1
    fi
    if ! secret_value kind-smoke-minio root-user | tr -d '\n' >"$dir/s3-key" ||
        ! secret_value kind-smoke-minio root-password | tr -d '\n' >"$dir/s3-key-secret"; then
        echo "secret kind-smoke-minio unreadable"
        return 1
    fi
    apply_secret kind-smoke-minio-tls --from-file=tls.crt="$dir/minio.crt" --from-file=tls.key="$dir/minio.key" \
        --from-file=ca.crt="$dir/ca.crt" || return 1
    apply_secret reticora-pgbackrest --from-file=cipher-pass="$dir/cipher-pass" \
        --from-file=s3-key="$dir/s3-key" --from-file=s3-key-secret="$dir/s3-key-secret" \
        --from-file=ca.crt="$dir/ca.crt" --from-file=tls.crt="$dir/client.crt" --from-file=tls.key="$dir/client.key" \
        --from-file=server.crt="$dir/server.crt" --from-file=server.key="$dir/server.key"
}

setup() {
    local tmp out dep admin_pw dsn userinfo login login_pw
    tmp=$(mktemp -d)
    # shellcheck disable=SC2064 # expand tmp now
    trap "port_forward_stop; rm -rf '$tmp'" EXIT
    echo "== apply ${FIXTURES#"$ROOT"/}"
    apply_dir "$FIXTURES" || return 1
    echo "== session key reticora-session-key (RSA, generated for this run)"
    "$OPENSSL" genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$tmp/private.pem" 2>/dev/null ||
        { echo "openssl genpkey failed"; return 1; }
    out=$(k create secret generic reticora-session-key --from-file=private.pem="$tmp/private.pem" \
        --dry-run=client -o yaml) || { echo "kubectl create secret failed"; return 1; }
    k apply -f - <<<"$out" || return 1
    # Before the wait: MinIO starts only with its certificate.
    echo "== test CA, certificates, secrets kind-smoke-minio-tls and reticora-pgbackrest (wal_archive)"
    wal_secrets "$tmp" || return 1
    for dep in postgres redis nats minio; do
        echo "== wait for $dep"
        k rollout status "deployment/$dep" --timeout="${DEPS_TIMEOUT}s" || return 1
    done
    echo "== migrate the database (bootstrap superuser, as the operator's migration run)"
    admin_pw=$(secret_value kind-smoke-postgres password) || { echo "secret kind-smoke-postgres unreadable"; return 1; }
    port_forward svc/postgres 5432 || return 1
    "$MIGRATE" -path "$ROOT/backend/migrations" \
        -database "postgres://reticora:${admin_pw}@127.0.0.1:${PF_PORT}/reticora?sslmode=disable" up ||
        { echo "migrate failed"; port_forward_stop; return 1; }
    port_forward_stop
    echo "== runtime login of reticora-db-runtime (NOSUPERUSER NOBYPASSRLS, member of reticora_app)"
    dsn=$(secret_value reticora-db-runtime database-url) || { echo "secret reticora-db-runtime unreadable"; return 1; }
    # postgres://<login>:<password>@host/db: the fixture's values need no
    # URL decoding.
    userinfo=${dsn#*://}
    case $userinfo in
        *:*@*) userinfo=${userinfo%%@*} ;;
        *) userinfo="" ;;
    esac
    login=${userinfo%%:*}
    login_pw=${userinfo#*:}
    if [ -z "$login" ] || [ -z "$login_pw" ]; then
        echo "reticora-db-runtime has no user:password"
        return 1
    fi
    k exec -i deployment/postgres -- psql -U reticora -d reticora -v ON_ERROR_STOP=1 \
        -v login="$login" -v pw="$login_pw" <<'SQL' || { echo "creating the runtime login failed"; return 1; }
SELECT format('CREATE ROLE %I LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD %L',
              :'login', :'pw')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'login') \gexec
GRANT reticora_app TO :"login";
SQL
    # pgBackRest creates no bucket, and the backup job (verify-repo) wants
    # versioning. mc runs in the MinIO pod with its credentials and trusts the
    # test CA.
    echo "== bucket reticora-backup with versioning in MinIO (repository of wal_archive)"
    k exec -i deployment/minio -- sh -s <<'SH' || { echo "creating the bucket reticora-backup failed"; return 1; }
set -e
mkdir -p "$MC_CONFIG_DIR/certs/CAs"
cp /certs/CAs/ca.crt "$MC_CONFIG_DIR/certs/CAs/kind-smoke-ca.crt"
export MC_HOST_kind="https://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@localhost:9000"
mc mb --ignore-existing kind/reticora-backup
mc version enable kind/reticora-backup
mc version info kind/reticora-backup
SH
    echo "setup done"
}

usage() {
    sed -n '/^# Usage:/,/^# Exit code/p' "$0" | sed 's/^# \{0,1\}//'
}

command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }
case "${1:-}" in
    setup) require_kind_context && setup ;;
    run) shift; require_kind_context && run_cases "$@" ;;
    cases) printf '%s\n' "${CASES[@]}" ;;
    *) usage >&2; exit 2 ;;
esac
