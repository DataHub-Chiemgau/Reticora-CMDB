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
case "${1:-}" in
    get)
        case "${2:-}" in
            pods)
                [ "${STUB_GET_RC:-0}" -eq 0 ] || { echo "The connection to the server was refused" >&2; exit 1; }
                case "$*" in
                    *jsonpath*) echo reticora-server-5f7c9-x1 ;;
                    *"-o json"*) pod_json "$(behaviour)" ;;
                    *) echo "NAME READY STATUS" ;;
                esac ;;
            secret)
                case "$3" in
                    kind-smoke-postgres) jq -n --arg v "$STUB_ADMIN_PW" '{data:{password:($v | @base64)}}' ;;
                    reticora-db-runtime) jq -n --arg v "$STUB_RUNTIME_DSN" '{data:{"database-url":($v | @base64)}}' ;;
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
        b=$(behaviour)
        case $b in exit:*) rest=${b#exit:}; printf '%s\n' "${rest#*:}" ;; esac ;;
    port-forward)
        echo "Forwarding from 127.0.0.1:18080 -> 8080"
        echo "Forwarding from [::1]:18080 -> 8080"
        exec sleep 30 ;;
    config)
        [ -n "$STUB_CONTEXT" ] || { echo "error: current-context is not set" >&2; exit 1; }
        echo "$STUB_CONTEXT" ;;
    rollout) exit "${STUB_ROLLOUT_RC:-0}" ;;
    exec) cat >"$STUB_DIR/exec.sql"; exit "${STUB_EXEC_RC:-0}" ;;
    create) printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: reticora-session-key\n' ;;
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

cat >"$STUB_BIN/openssl" <<'STUBEOF'
#!/usr/bin/env bash
while [ $# -gt 0 ]; do
    [ "$1" = -out ] && { echo "stub key" >"$2"; exit 0; }
    shift
done
exit 1
STUBEOF
chmod +x "$STUB_BIN"/*

# ─── Harness ────────────────────────────────────────────────────────────────

# The runtime DSN and the admin password come from the real fixtures, so the
# setup test also checks that the script reads them as they are committed.
FIXTURE_DSN=$(sed -n 's/^ *database-url: //p' tests/k8s/fixtures/reticora-secrets.yaml)
FIXTURE_ADMIN_PW=$(sed -n 's/^ *password: //p' tests/k8s/fixtures/postgres.yaml)

reset() {
    unset STUB_ok STUB_master_key_missing STUB_master_key_empty STUB_issuer_empty STUB_issuer_key_missing \
        STUB_GET_RC STUB_RESTORE_BROKEN STUB_ROLLOUT_RC STUB_EXEC_RC STUB_MIGRATE_RC
    export STUB_DIR="$TMP_ROOT/stub" STUB_ROOT="$ROOT"
    rm -rf "$STUB_DIR" "$TMP_ROOT/artifacts"
    mkdir -p "$STUB_DIR"
    echo ok >"$STUB_DIR/state"
    export STUB_READYZ_CODE=200
    export STUB_READYZ_BODY='{"status":"ready","checks":{"database":"ok","nats":"ok","redis":"ok"},"details":{"ingest":{"lanes":1}}}'
    export STUB_RUNTIME_DSN="$FIXTURE_DSN" STUB_ADMIN_PW="$FIXTURE_ADMIN_PW"
    export STUB_CONTEXT=kind-reticora-smoke
}

# smoke ARGS… runs the script against the stubs; output in $OUT, code in $RC.
smoke() {
    RC=0
    OUT=$(KUBECTL="$STUB_BIN/kubectl" KUSTOMIZE="$STUB_BIN/kustomize" CURL="$STUB_BIN/curl" \
        MIGRATE="$STUB_BIN/migrate" OPENSSL="$STUB_BIN/openssl" \
        READY_TIMEOUT=2 STATE_TIMEOUT=2 DEPS_TIMEOUT=2 POLL_INTERVAL=0.1 \
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
rc_is 0 && has '^kind smoke: 9 passed, 0 failed$'
check "correct cluster: exit 0, 9 passed (5 cases, 4 restores)"
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
# Order of the cluster changes: overlay, then per case patch, pod deletion,
# fixtures, pod deletion.
order=$(grep -oE '^-n reticora-staging (patch (secret|configmap) [a-z-]+|delete pod)|^apply' "$STUB_DIR/calls" | tr '\n' '|')
want='apply|'
for obj in 'secret reticora-secrets' 'secret reticora-secrets' 'configmap reticora-config' 'configmap reticora-config'; do
    want="${want}-n reticora-staging patch $obj|-n reticora-staging delete pod|apply|-n reticora-staging delete pod|"
done
[ "$order" = "$want" ]
check "cluster changes in order: overlay, then patch, restart, restore per case"
[ "$(grep -c '^# rendered' "$STUB_DIR/applied")" -eq 5 ] &&
    [ "$(head -n 1 "$STUB_DIR/applied")" = "# rendered deploy/k8s/overlays/staging" ] &&
    [ "$(grep -c '^# rendered tests/k8s/fixtures$' "$STUB_DIR/applied")" -eq 4 ]
check "the overlay is applied first, the fixtures restore every case"
called 'port-forward svc/reticora-server :80' && grep -q 'http://127.0.0.1:18080/readyz' "$STUB_DIR/curl"
check "/readyz is requested through the port-forward of svc/reticora-server"
! has '5f7c9-old'
check "pods being deleted are ignored"
missing=""
for f in ready master_key_missing master_key_empty issuer_empty issuer_key_missing; do
    [ -s "$TMP_ROOT/artifacts/$f.txt" ] || missing="$missing $f"
done
[ -z "$missing" ]
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

# ─── Run: positive case ─────────────────────────────────────────────────────

reset
export STUB_ok='exit:1:{"msg":"failed to connect to database"}'
smoke run
fails_with '^FAIL ready: the pod reticora-server is not Ready within 2s: .*CrashLoopBackOff' &&
    has 'negative cases skipped' && ! has '^(ok|FAIL) +(master_key|issuer|restore)' &&
    has '^kind smoke: 0 passed, 1 failed$'
check "pod never Ready: positive case fails, negative cases skipped"

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
rc_is 0 && has '^ok   issuer_empty' && ! has '^(ok|FAIL) +master_key' && has '^kind smoke: 3 passed, 0 failed$'
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
