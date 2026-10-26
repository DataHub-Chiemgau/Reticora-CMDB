#!/usr/bin/env bash
#
# run.sh — outage and backpressure suite (WP-104, WP-449, OPS-06). Runs
# against an installed Compose stack (./install.sh --cloud --non-interactive
# --build-from-source, as in outage.yml, with Mailpit as SMTP server:
# tests/outage/smtp.sh overlay) and simulates the outage of every
# dependency:
#
#   postgres, nats, redis, minio (S3)  stopped for OUTAGE_SECONDS: /readyz
#       reports exactly that dependency unavailable (503, backpressure:
#       load balancers stop routing), the server process keeps running
#       without restarts, readiness recovers after the restart, and a
#       marker row written before the outage is still there. During the
#       outage the load of load.sh (API writes and ingest records) is
#       answered with 2xx or a visible 429/503, and after the recovery
#       accepted == persisted, the ingest queue is empty and the event
#       outbox published (no silent data loss, counted in the database);
#   smtp: Mailpit stopped, an e-mail notification stays pending with its
#       error and is sent once Mailpit is back, plus the same load
#       (smtp.sh);
#   server unreachable for the collector, webhook target down, spool
#       limits: the component tests of the collector spool, the edge
#       buffer and the webhook retry/dead-letter path.
#
# Writes the protocol to OUTAGE_REPORT (default outage-report.md) with every
# count and exits non-zero when a scenario fails. OUTAGE_SELFTEST_DROP=1
# drops one ingest record of every load on purpose (load.sh): the run must
# then FAIL with that record lost.
#
# Usage: ./tests/outage/run.sh [scenario …]   (default: all)
# Sourced, it only defines the scenarios (tests/outage/config.test.sh).

OUTAGE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tests/outage/lib.sh
. "$OUTAGE_DIR/lib.sh"
# shellcheck source=tests/outage/load.sh
. "$OUTAGE_DIR/load.sh"
# shellcheck source=tests/outage/smtp.sh
. "$OUTAGE_DIR/smtp.sh"

ALL_SCENARIOS="postgres nats redis minio component smtp"

# dependency_outage <compose service> <readiness check name>
dependency_outage() {
    local service="$1" check="$2" marker restarts details started remaining recovered
    local failures=()
    if ! ready; then
        report FAIL "$service outage" "stack not ready before the outage: $(oneline "$(readyz)")"
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
    started=$SECONDS
    wait_for 60 "/readyz to report $check unavailable" check_unavailable "$check" ||
        failures+=("readiness did not report $check unavailable: $(oneline "$(readyz)")")
    # The load runs while the dependency is down; the outage lasts until it
    # is sent, at least OUTAGE_SECONDS.
    load_send "$service"
    remaining=$((OUTAGE_SECONDS - (SECONDS - started)))
    [ "$remaining" -gt 0 ] && sleep "$remaining"
    server_alive || failures+=("server process stopped during the outage")
    check_unavailable "$check" || failures+=("readiness changed during the outage: $(oneline "$(readyz)")")

    compose start "$service" >/dev/null 2>&1
    recovered="$(utc_now)"
    wait_for "$RECOVERY_TIMEOUT" "readiness after restarting $service" ready ||
        failures+=("no recovery within ${RECOVERY_TIMEOUT}s: $(oneline "$(readyz)")")
    load_verify "$service" "$recovered" || failures+=("${LOAD_PROBLEMS[@]}")
    [ "$(server_restarts)" = "$restarts" ] || failures+=("server restarted ($restarts → $(server_restarts))")
    [ "$(psql_exec -c "SELECT count(*) FROM outage_marker WHERE id = '$marker'" 2>/dev/null)" = "1" ] ||
        failures+=("marker row written before the outage is missing")
    detail recovery "| $service | $LOAD_RECOVERY | $restarts → $(server_restarts) |"

    if [ "${#failures[@]}" -eq 0 ]; then
        details="readiness 503 with $check unavailable, server kept running, recovered, data intact; load: $LOAD_SUMMARY; $LOAD_RECOVERY"
        report PASS "$service outage" "$details"
    else
        report FAIL "$service outage" "$(oneline "$(joined "${failures[@]}")")"
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

# load_scenario <scenario>: the load scenarios share the organization of
# load.sh, set up before the first of them.
LOAD_READY=0
load_scenario() {
    if [ "$LOAD_READY" = 0 ]; then
        if load_setup "$TMPDIR_OUTAGE/load"; then
            LOAD_READY=1
        else
            LOAD_READY=-1
        fi
    fi
    if [ "$LOAD_READY" != 1 ]; then
        report FAIL "$1 outage" "no organization for the load: $(oneline "${LOAD_SETUP_ERROR:-setup failed}")"
        return 1
    fi
}

# write_protocol_tail: result line and the evidence sections.
write_protocol_tail() {
    printf '\n**Result:** %d passed, %d failed, %d not applicable\n' "$PASSED" "$FAILED" "$NOT_APPLICABLE"
    if [ -s "$OUTAGE_DETAILS.load" ]; then
        printf '\n## Load during the outage: accepted == persisted\n\n'
        printf 'Answers per kind of the load; persisted counts the CIs of the run in the database (serial number prefix of the run), lost = accepted but not persisted, late = refused but persisted.\n\n'
        printf '| Scenario | Kind | Sent | Accepted (2xx) | Refused 429 | Refused 503 | Other | Persisted | Lost | Late |\n|---|---|---|---|---|---|---|---|---|---|\n'
        cat "$OUTAGE_DETAILS.load"
    fi
    if [ -s "$OUTAGE_DETAILS.recovery" ]; then
        printf '\n## After the recovery\n\n| Scenario | Ingest queue / unpublished outbox entries up to the recovery | Server restarts (before → after) |\n|---|---|---|\n'
        cat "$OUTAGE_DETAILS.recovery"
    fi
    if [ -s "$OUTAGE_DETAILS.smtp" ]; then
        printf '\n## SMTP outage: delivery status\n\n'
        cat "$OUTAGE_DETAILS.smtp"
    fi
}

outage_main() {
    local scenarios="${*:-$ALL_SCENARIOS}" scenario
    cd "$OUTAGE_DIR/../.." || return 1
    TMPDIR_OUTAGE="$(mktemp -d)"
    # shellcheck disable=SC2064 # the directory is known now
    trap "rm -rf '$TMPDIR_OUTAGE'" EXIT
    OUTAGE_DETAILS="$TMPDIR_OUTAGE/details"
    {
        printf '# Outage suite protocol (OPS-06)\n\n'
        printf -- '- Date: %s\n- Commit: %s\n- Outage: %ss at least, recovery timeout: %ss\n' \
            "$(date -u '+%Y-%m-%d %H:%M:%S UTC')" "$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" \
            "$OUTAGE_SECONDS" "$RECOVERY_TIMEOUT"
        printf -- '- Load during every outage: %s API writes (POST /api/v1/cis) and %s ingest records (POST /api/v1/ingest/bulk, one per request)\n' \
            "$LOAD_API_WRITES" "$LOAD_INGEST_RECORDS"
        if [ "${OUTAGE_SELFTEST_DROP:-0}" = 1 ]; then
            printf -- '- **Negative self-test OUTAGE_SELFTEST_DROP=1:** one ingest record of every load is dropped on purpose; the run must FAIL\n'
        fi
        printf '\n| Scenario | Result | Details |\n|---|---|---|\n'
    } > "$OUTAGE_REPORT"

    for scenario in $scenarios; do
        case "$scenario" in
            postgres) load_scenario postgres && dependency_outage postgres database ;;
            nats) load_scenario nats && dependency_outage nats nats ;;
            redis) load_scenario redis && dependency_outage redis redis ;;
            minio) load_scenario minio && dependency_outage minio object_storage ;;
            component) component_scenarios ;;
            smtp) load_scenario smtp && smtp_outage ;;
            *) report FAIL "$scenario" "unknown scenario" ;;
        esac
    done

    # The marker table is test data only.
    psql_exec -c "DROP TABLE IF EXISTS outage_marker" >/dev/null 2>&1 || true

    write_protocol_tail >> "$OUTAGE_REPORT"
    printf '\n%d passed, %d failed, %d not applicable — protocol: %s\n' "$PASSED" "$FAILED" "$NOT_APPLICABLE" "$OUTAGE_REPORT"
    [ "$FAILED" -eq 0 ]
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    set -uo pipefail
    outage_main "$@"
fi
