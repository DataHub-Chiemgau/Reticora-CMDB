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
# A negative case changes the object, deletes the server pods and waits for
# the state of the new pod. Afterwards the fixtures are applied again and the
# new pod must be Ready again ("restore after <case>"), so every case starts
# from the positive state. When the positive case fails, the negative cases
# are skipped: they mean nothing without a working baseline.
#
# Usage:
#   tests/k8s/kind-smoke.sh setup        apply tests/k8s/fixtures, create the
#                                        session key, wait for the
#                                        dependencies, migrate the database
#                                        and create the runtime login
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
POLL_INTERVAL="${POLL_INTERVAL:-5}"
ARTIFACT_DIR="${ARTIFACT_DIR:-}"
CASES=(ready master_key_missing master_key_empty issuer_empty issuer_key_missing)

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

# await TIMEOUT WANT [ARG] repeats observe until it decides or TIMEOUT
# seconds have passed; the last observation is left in LAST.
await() {
    local timeout=$1 deadline rc
    shift
    deadline=$((SECONDS + timeout))
    while :; do
        LAST=$(observe "$@")
        rc=$?
        [ "$rc" -eq 2 ] || return "$rc"
        if [ "$SECONDS" -ge "$deadline" ]; then
            LAST="$LAST (no decision within ${timeout}s)"
            return 1
        fi
        sleep "$POLL_INTERVAL"
    done
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
    if case_ready; then
        for c in "$@"; do
            [ "$c" = ready ] || run_case "$c"
        done
    else
        note "negative cases skipped: the positive case failed"
    fi
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
