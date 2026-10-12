#!/usr/bin/env bash
#
# run.sh — outage and backpressure suite (WP-104, OPS-06). Runs against an
# installed Compose stack (./install.sh --cloud --non-interactive) and
# simulates the outage of every dependency:
#
#   postgres, nats, redis, minio (S3)  stopped for OUTAGE_SECONDS: /readyz
#       reports exactly that dependency unavailable (503, backpressure:
#       load balancers stop routing), the server process keeps running
#       without restarts, readiness recovers after the restart, and data
#       written before the outage is still there (no silent loss);
#   server unreachable for the collector, webhook target down, spool
#       limits: the component tests of the collector spool, the edge
#       buffer and the webhook retry/dead-letter path;
#   smtp: reported as not applicable while no SMTP delivery exists
#       (TEC-13); it becomes a scenario with the notification channel.
#
# Writes the protocol to OUTAGE_REPORT (default outage-report.md) and exits
# non-zero when a scenario fails.
#
# Usage: ./tests/outage/run.sh [scenario …]   (default: all)
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
# shellcheck source=tests/outage/lib.sh
. tests/outage/lib.sh

ALL_SCENARIOS="postgres nats redis minio component smtp"
SCENARIOS="${*:-$ALL_SCENARIOS}"

{
    printf '# Outage suite protocol (OPS-06)\n\n'
    printf -- '- Date: %s\n- Commit: %s\n- Outage: %ss, recovery timeout: %ss\n\n' \
        "$(date -u '+%Y-%m-%d %H:%M:%S UTC')" "$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" \
        "$OUTAGE_SECONDS" "$RECOVERY_TIMEOUT"
    printf '| Scenario | Result | Details |\n|---|---|---|\n'
} > "$OUTAGE_REPORT"

# dependency_outage <compose service> <readiness check name>
dependency_outage() {
    local service="$1" check="$2" marker restarts details
    if ! ready; then
        report FAIL "$service outage" "stack not ready before the outage: $(readyz)"
        return
    fi
    marker="outage-$service-$(date +%s)"
    if ! psql_exec -c "CREATE TABLE IF NOT EXISTS outage_marker (id text PRIMARY KEY, at timestamptz NOT NULL DEFAULT now())" \
        -c "INSERT INTO outage_marker (id) VALUES ('$marker')" >/dev/null; then
        report FAIL "$service outage" "could not write the marker row"
        return
    fi
    restarts="$(server_restarts)"

    compose stop "$service" >/dev/null 2>&1
    local failures=()
    wait_for 60 "/readyz to report $check unavailable" check_unavailable "$check" ||
        failures+=("readiness did not report $check unavailable: $(readyz)")
    sleep "$OUTAGE_SECONDS"
    server_alive || failures+=("server process stopped during the outage")
    check_unavailable "$check" || failures+=("readiness changed during the outage: $(readyz)")

    compose start "$service" >/dev/null 2>&1
    wait_for "$RECOVERY_TIMEOUT" "readiness after restarting $service" ready ||
        failures+=("no recovery within ${RECOVERY_TIMEOUT}s: $(readyz)")
    [ "$(server_restarts)" = "$restarts" ] || failures+=("server restarted ($restarts → $(server_restarts))")
    [ "$(psql_exec -c "SELECT count(*) FROM outage_marker WHERE id = '$marker'" 2>/dev/null)" = "1" ] ||
        failures+=("marker row written before the outage is missing")

    if [ "${#failures[@]}" -eq 0 ]; then
        details="readiness 503 with $check unavailable, server kept running, recovered, data intact"
        report PASS "$service outage" "$details"
    else
        report FAIL "$service outage" "$(IFS='; '; printf '%s' "${failures[*]}")"
        compose logs --tail 30 server >&2 || true
    fi
}

# Component scenarios: collector with the server unreachable (spool,
# backpressure, loss reporting), webhook target down (retries, dead
# letters).
component_scenarios() {
    local name pkg run test missing log
    while read -r name pkg run; do
        log="$TMPDIR_OUTAGE/$name.log"
        if ! (cd "${pkg%%/*}" && go test -count=1 -v -run "^($run)\$" "./${pkg#*/}") > "$log" 2>&1; then
            report FAIL "$name" "go test -run '$run' ./$pkg failed: $(tail -n 3 "$log" | tr '\n' ' ')"
            continue
        fi
        # Every named test must have run: a renamed test fails the scenario
        # instead of passing with "no tests to run".
        missing=""
        for test in ${run//|/ }; do
            grep -q -- "--- PASS: $test " "$log" || missing="$missing $test"
        done
        if [ -n "$missing" ]; then
            report FAIL "$name" "tests did not run:$missing"
        else
            report PASS "$name" "go test -run '$run' ./$pkg"
        fi
    done <<'LIST'
collector-server-unreachable collector/collectorcmd TestUploadRecords_FailureSpoolsAndFlushDelivers|TestHeartbeatReportsSpoolBackpressureAndLoss
collector-spool-limits edgecore/buffer TestDiskBufferSizeLimitAppliesBackpressure|TestDiskBufferMaxAgeKeepsMessagesFor24Hours|TestPersistenceAcrossBufferInstances
webhook-target-down backend/internal/webhook TestDispatcherPersistsAndRetriesFailedDelivery|TestDispatcherMarksDeliveryFailedAfterMaxAttempts
LIST
}

TMPDIR_OUTAGE="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_OUTAGE"' EXIT

for scenario in $SCENARIOS; do
    case "$scenario" in
        postgres) dependency_outage postgres database ;;
        nats) dependency_outage nats nats ;;
        redis) dependency_outage redis redis ;;
        minio) dependency_outage minio object_storage ;;
        component) component_scenarios ;;
        smtp) report N/A "smtp outage" "no SMTP delivery exists yet (TEC-13); added with the notification channel" ;;
        *) report FAIL "$scenario" "unknown scenario" ;;
    esac
done

# The marker table is test data only.
psql_exec -c "DROP TABLE IF EXISTS outage_marker" >/dev/null 2>&1 || true

printf '\n**Result:** %d passed, %d failed, %d not applicable\n' "$PASSED" "$FAILED" "$NOT_APPLICABLE" >> "$OUTAGE_REPORT"
printf '\n%d passed, %d failed, %d not applicable — protocol: %s\n' "$PASSED" "$FAILED" "$NOT_APPLICABLE" "$OUTAGE_REPORT"
[ "$FAILED" -eq 0 ]
