#!/usr/bin/env bash
#
# collector.sh — scenario "server unreachable for the collector" of the
# outage suite (WP-587, OPS-06, F-296). Sourced by run.sh after lib.sh and
# load.sh.
#
# An enrolled collector container (COLLECTOR_IMAGE, built from
# collector/Dockerfile) runs the simulated fleet (RETICORA_SIMULATE, SIM-02:
# 30 devices per discovery pass, as frontend/e2e-real/discovery-override.spec.ts)
# on the network of the Compose stack. It enrolls unattended with a one-time
# code of the scenarios' organization (RETICORA_ENROLL_TOKEN, COL-04) and
# delivers over the mTLS NATS edge (nats-edge); a pass runs every
# RETICORA_DISCOVERY_INTERVAL (5 min, the minimum). The scenario:
#
#   1. calibration: a collector without network spools one pass; its size
#      sets the spool limit (RETICORA_SPOOL_MAX_BYTES) to
#      COLLECTOR_SPOOL_RECORDS records plus 5 % (default 30, one pass: the
#      spool takes the pass of the outage and is saturated, so the collector
#      reports backpressure and pauses discovery);
#   2. the first pass is delivered and ingested before the outage;
#   3. COLLECTOR_OUTAGE_LEAD seconds before the next pass is due, server and
#      nats-edge are stopped (docker compose stop) for at least
#      COLLECTOR_OUTAGE_SECONDS (60 s) and until the next pass ran: the
#      spool counter of the collector (messages in its spool directory) must
#      rise;
#   4. after the restart the first heartbeat the server accepts must report
#      backpressure (server log collector.spool.degraded of the collector;
#      the collector flushes its spool only after an accepted heartbeat,
#      RETICORA_SPOOL_FLUSH_INTERVAL is longer than the scenario), the
#      spool must be delivered and the ingest queue empty;
#   5. accounting: every record the collector sent (the records of its
#      passes, collector log "discovery completed") is ingested (rows of the
#      collector in ingest_record, REC-01), and the loss counter is 0: no
#      dropped message or sample in the heartbeats (the server's
#      reticora_collector_spool_dropped_total) and no loss logged by the
#      collector (collector.spool.data_lost).
#
# Negative self-test COLLECTOR_SPOOL_RECORDS=1: the spool takes one record,
# a pass does not fit, the collector counts it as lost; the scenario must
# FAIL with the records lost and the loss counter above 0 (outage.yml).

COLLECTOR_IMAGE="${COLLECTOR_IMAGE:-reticora-collector:outage}"
COLLECTOR_OUTAGE_SECONDS="${COLLECTOR_OUTAGE_SECONDS:-60}"
# RETICORA_DISCOVERY_INTERVAL of the collector in seconds; the collector
# raises shorter values to 5 min.
COLLECTOR_INTERVAL_SECONDS="${COLLECTOR_INTERVAL_SECONDS:-300}"
# Seconds before the next pass is due at which server and nats-edge stop,
# and the time allowed for that pass after the stop (the collector checks
# every minute what is due).
COLLECTOR_OUTAGE_LEAD="${COLLECTOR_OUTAGE_LEAD:-30}"
COLLECTOR_PASS_TIMEOUT="${COLLECTOR_PASS_TIMEOUT:-$((COLLECTOR_OUTAGE_LEAD + 120))}"
# Spool limit in records of the simulated fleet (one pass = 30 records);
# the margin covers the few bytes a pass differs from the calibration.
COLLECTOR_SPOOL_RECORDS="${COLLECTOR_SPOOL_RECORDS:-30}"
COLLECTOR_SPOOL_MARGIN_PERCENT=105
# Seconds to wait for the enrollment and the first pass, and for the
# calibration.
COLLECTOR_START_TIMEOUT="${COLLECTOR_START_TIMEOUT:-120}"
# The data directory of the collector container (credentials, spool), a
# tmpfs the unprivileged collector user can write.
COLLECTOR_DATA=/var/lib/reticora-collector

# collector_docker: docker for the collector containers (stubbed by
# tests/outage/config.test.sh).
collector_docker() {
    docker "$@"
}

# collector_jq <log file> <jq filter>: the filter over the JSON lines of a
# collector or server log; other lines are skipped.
collector_jq() {
    jq -R -r "fromjson? // empty | $2" "$1" 2>/dev/null
}

# collector_refresh: the log of the scenario's collector to $COLLECTOR_LOG.
collector_refresh() {
    collector_docker logs "$COLLECTOR_CONTAINER" > "$COLLECTOR_LOG" 2>&1 || true
}

# collector_passes / collector_sent: the discovery passes of the collector
# and the records they produced (uploaded, spooled or lost), from the log.
collector_passes() {
    collector_jq "$COLLECTOR_LOG" 'select(.msg == "discovery completed") | 1' | wc -l
}

collector_sent() {
    collector_jq "$COLLECTOR_LOG" 'select(.msg == "discovery completed") | .total_records // 0' |
        awk '{ s += $1 } END { print s + 0 }'
}

# collector_logged_loss: the messages the collector logged as lost
# (collector.spool.data_lost: spool full, size or age limit, records or
# batches the server refused, unreadable entries).
collector_logged_loss() {
    collector_jq "$COLLECTOR_LOG" 'select(.event == "collector.spool.data_lost") | .messages // 1' |
        awk '{ s += $1 } END { print s + 0 }'
}

collector_has_passes() { # <n>
    collector_refresh
    [ "$(collector_passes)" -ge "$1" ]
}

# collector_spool_messages: the spool counter, the messages in the spool
# directory of the container ("?" when it cannot be read).
collector_spool_messages() {
    local n
    n="$(collector_docker exec "$COLLECTOR_CONTAINER" sh -c "find $COLLECTOR_DATA/spool -maxdepth 1 -name '*.json' 2>/dev/null | wc -l" 2>/dev/null |
        tr -d ' \n')"
    case "$n" in
        '' | *[!0-9]*) printf '?' ;;
        *) printf '%s' "$n" ;;
    esac
}

# collector_settled: the spool is empty and the ingest queue too, so every
# delivered record is processed.
collector_settled() {
    [ "$(collector_spool_messages)" = 0 ] && ingest_queue_empty
}

# collector_ingested <collector id>: the records of the collector the
# ingest processed (REC-01 keeps every record_id with its collector).
collector_ingested() {
    psql_exec -c "SELECT count(*) FROM ingest_record WHERE organization_id = '$OUTAGE_ORG_ID' AND collector_id = '$1'" 2>/dev/null
}

# collector_heartbeat_since <collector id> <timestamp>: the server accepted
# a heartbeat of the collector at or after the timestamp.
collector_heartbeat_since() {
    [ "$(psql_exec -c "SELECT count(*) FROM collector WHERE id = '$1' AND last_heartbeat_at >= '$2'" 2>/dev/null)" = 1 ]
}

# collector_reports <since> <collector id> <file>: the spool reports of the
# collector's heartbeats the server logged (collector.spool.degraded:
# losses or backpressure), oldest first, as JSON lines.
collector_reports() {
    compose logs --no-log-prefix --since "$1" server 2>/dev/null |
        jq -R -c --arg id "$2" 'fromjson? // empty | select(.event == "collector.spool.degraded" and .collector_id == $id)
            | {spool_messages, backpressure, dropped_messages, dropped_samples}' > "$3" 2>/dev/null || true
}

# collector_network: the Compose network of the server.
collector_network() {
    # shellcheck disable=SC2016 # a Go template of docker inspect
    collector_docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}' \
        "$(compose ps -q server)" 2>/dev/null | awk '{ print $1 }'
}

# collector_calibrate: a collector without network spools its first pass;
# sets CAL_BYTES (size of the spooled pass) and CAL_RECORDS (its records),
# or COLLECTOR_ERROR.
collector_calibrate() {
    local name="$COLLECTOR_CONTAINER-calibration" log="$TMPDIR_OUTAGE/collector-calibration.log"
    CAL_BYTES="" CAL_RECORDS=""
    if ! collector_docker run -d --name "$name" --network none --tmpfs "$COLLECTOR_DATA:rw,mode=1777" \
        -e RETICORA_ENVIRONMENT=test -e RETICORA_SIMULATE=true -e RETICORA_METRICS_INTERVAL=0 \
        -e RETICORA_SERVER_URL=http://127.0.0.1:9 -e RETICORA_ORGANIZATION_ID=calibration -e RETICORA_COLLECTOR_ID=calibration \
        -e "RETICORA_CREDENTIALS_PATH=$COLLECTOR_DATA/credentials.json" -e "RETICORA_SPOOL_DIR=$COLLECTOR_DATA/spool" \
        "$COLLECTOR_IMAGE" >/dev/null 2>&1; then
        COLLECTOR_ERROR="calibration container did not start"
        return 1
    fi
    wait_for "$COLLECTOR_START_TIMEOUT" "the calibration pass" collector_calibrated "$name" "$log"
    local rc=$?
    collector_docker rm -f "$name" >/dev/null 2>&1 || true
    [ "$rc" -eq 0 ] || COLLECTOR_ERROR="no spooled pass in the calibration log: $(oneline "$(tail -n 3 "$log" 2>/dev/null)")"
    return "$rc"
}

# collector_calibrated <container> <log file>: the calibration container
# spooled its first pass; sets CAL_BYTES and CAL_RECORDS.
collector_calibrated() {
    collector_docker logs "$1" > "$2" 2>&1 || true
    CAL_BYTES="$(collector_jq "$2" 'select(.msg == "spool stats" and .spool_messages == 1) | .spool_bytes' | head -n 1)"
    CAL_RECORDS="$(collector_jq "$2" 'select(.msg == "discovery completed") | .total_records' | head -n 1)"
    [ -n "$CAL_BYTES" ] && [ -n "$CAL_RECORDS" ] && [ "$CAL_RECORDS" -gt 0 ]
}

# collector_start <network> <enrollment code> <spool limit in bytes>: the
# scenario's collector. The code reaches docker through the environment,
# not the command line.
collector_start() {
    (
        export RETICORA_ENROLL_TOKEN="$2"
        collector_docker run -d --name "$COLLECTOR_CONTAINER" --hostname "$COLLECTOR_CONTAINER" --network "$1" \
            --tmpfs "$COLLECTOR_DATA:rw,mode=1777" \
            -e RETICORA_ENVIRONMENT=test -e RETICORA_SIMULATE=true -e RETICORA_ENROLL_TOKEN \
            -e RETICORA_CLOUD_URL=http://server:8080 -e RETICORA_NATS_URL=tls://nats-edge:4222 \
            -e "RETICORA_CREDENTIALS_PATH=$COLLECTOR_DATA/credentials.json" -e "RETICORA_SPOOL_DIR=$COLLECTOR_DATA/spool" \
            -e "RETICORA_SPOOL_MAX_BYTES=$3" -e "RETICORA_DISCOVERY_INTERVAL=${COLLECTOR_INTERVAL_SECONDS}s" \
            -e RETICORA_HEARTBEAT_INTERVAL=5s -e RETICORA_SPOOL_FLUSH_INTERVAL=24h -e RETICORA_METRICS_INTERVAL=0 \
            "$COLLECTOR_IMAGE" >/dev/null
    )
}

collector_cleanup() {
    collector_docker rm -f "$COLLECTOR_CONTAINER" >/dev/null 2>&1 || true
}

collector_outage() {
    local name="collector outage" failures=() network status code limit id started first_pass wait_s
    local sent_before ingested_before reports_before passes_before spool_before spool_during outage_start down
    local remaining recovered passes sent ingested reported logged heartbeat_report accounting=()
    COLLECTOR_CONTAINER="outage-collector-$(date -u +%Y%m%d%H%M%S)"
    COLLECTOR_LOG="$TMPDIR_OUTAGE/collector.log"
    COLLECTOR_REPORTS="$TMPDIR_OUTAGE/collector-reports.jsonl"
    : > "$COLLECTOR_LOG"
    if ! ready; then
        report FAIL "$name" "stack not ready before the outage: $(oneline "$(readyz)")"
        return
    fi
    if ! collector_docker image inspect "$COLLECTOR_IMAGE" >/dev/null 2>&1; then
        report FAIL "$name" "collector image $COLLECTOR_IMAGE not found (docker build -t $COLLECTOR_IMAGE -f collector/Dockerfile .)"
        return
    fi
    network="$(collector_network)"
    if [ -z "$network" ]; then
        report FAIL "$name" "no Compose network of the server found"
        return
    fi
    if ! collector_calibrate; then
        report FAIL "$name" "$COLLECTOR_ERROR"
        return
    fi
    limit=$(((CAL_BYTES * COLLECTOR_SPOOL_RECORDS * COLLECTOR_SPOOL_MARGIN_PERCENT + CAL_RECORDS * 100 - 1) / (CAL_RECORDS * 100)))
    detail collector "- Collector container \`$COLLECTOR_CONTAINER\` ($COLLECTOR_IMAGE, simulated fleet, enrolled with a one-time code, edge tls://nats-edge:4222), discovery every ${COLLECTOR_INTERVAL_SECONDS}s"
    detail collector "- Spool limit: $COLLECTOR_SPOOL_RECORDS record(s) = $limit bytes (calibration: one spooled pass of $CAL_RECORDS records = $CAL_BYTES bytes, plus 5 %)"

    status="$(api POST /collectors/enrollment-codes '{"label":"outage-collector","ttl_minutes":30}')"
    code="$(jq -r '.code // empty' "$API_OUT" 2>/dev/null)"
    if [ "$status" != 201 ] || [ -z "$code" ]; then
        report FAIL "$name" "enrollment code: $status $(oneline "$(head -c 200 "$API_OUT" 2>/dev/null)")"
        return
    fi
    started="$(utc_now)"
    if ! collector_start "$network" "$code" "$limit"; then
        report FAIL "$name" "collector container $COLLECTOR_CONTAINER did not start"
        collector_cleanup
        return
    fi

    # Before the outage: the first pass is delivered and ingested.
    if ! wait_for "$COLLECTOR_START_TIMEOUT" "the enrollment and the first pass of $COLLECTOR_CONTAINER" collector_has_passes 1; then
        report FAIL "$name" "no discovery pass within ${COLLECTOR_START_TIMEOUT}s: $(oneline "$(tail -n 3 "$COLLECTOR_LOG")")"
        collector_cleanup
        return
    fi
    first_pass=$SECONDS
    id="$(psql_exec -c "SELECT id FROM collector WHERE organization_id = '$OUTAGE_ORG_ID' AND name = '$COLLECTOR_CONTAINER'" 2>/dev/null)"
    if [ -z "$id" ]; then
        report FAIL "$name" "collector $COLLECTOR_CONTAINER not enrolled in the organization"
        collector_cleanup
        return
    fi
    wait_for "$RECOVERY_TIMEOUT" "the first pass delivered (spool and ingest queue empty)" collector_settled || true
    collector_refresh
    sent_before="$(collector_sent)"
    ingested_before="$(collector_ingested "$id")"
    [ "$ingested_before" = "$sent_before" ] ||
        failures+=("before the outage: ${ingested_before:-?} of $sent_before records ingested, spool $(collector_spool_messages)")
    detail collector "- Before the outage: $(collector_passes) pass(es), $sent_before records sent, ${ingested_before:-?} ingested"

    # The outage: server and nats-edge stop shortly before the next pass.
    wait_s=$((first_pass + COLLECTOR_INTERVAL_SECONDS - COLLECTOR_OUTAGE_LEAD - SECONDS))
    [ "$wait_s" -gt 0 ] && sleep "$wait_s"
    collector_reports "$started" "$id" "$COLLECTOR_REPORTS"
    reports_before="$(wc -l < "$COLLECTOR_REPORTS")"
    collector_refresh
    passes_before="$(collector_passes)"
    spool_before="$(collector_spool_messages)"
    compose stop server nats-edge >/dev/null 2>&1
    outage_start=$SECONDS
    ready && failures+=("server still ready after stopping it: $(oneline "$(readyz)")")
    if wait_for "$COLLECTOR_PASS_TIMEOUT" "a discovery pass during the outage" collector_has_passes $((passes_before + 1)); then
        spool_during="$(collector_spool_messages)"
    else
        spool_during="$(collector_spool_messages)"
        failures+=("no discovery pass during the outage within ${COLLECTOR_PASS_TIMEOUT}s")
    fi
    case "$spool_before$spool_during" in
        *'?'*) failures+=("spool counter not readable ($spool_before → $spool_during)") ;;
        *) [ "$spool_during" -gt "$spool_before" ] || failures+=("spool did not rise during the outage ($spool_before → $spool_during)") ;;
    esac
    remaining=$((COLLECTOR_OUTAGE_SECONDS - (SECONDS - outage_start)))
    [ "$remaining" -gt 0 ] && sleep "$remaining"
    down=$((SECONDS - outage_start))
    detail collector "- Outage: server and nats-edge stopped for ${down}s; spool counter $spool_before → $spool_during message(s); $(($(collector_passes) - passes_before)) pass(es) during the outage"

    compose start server >/dev/null 2>&1
    compose start nats-edge >/dev/null 2>&1
    recovered="$(utc_now)"
    wait_for "$RECOVERY_TIMEOUT" "readiness after restarting server and nats-edge" ready ||
        failures+=("no recovery within ${RECOVERY_TIMEOUT}s: $(oneline "$(readyz)")")
    wait_for "$RECOVERY_TIMEOUT" "an accepted heartbeat of the collector" collector_heartbeat_since "$id" "$recovered" ||
        failures+=("no heartbeat of the collector accepted after the restart")
    wait_for "$RECOVERY_TIMEOUT" "the spool delivered and the ingest queue empty" collector_settled ||
        failures+=("spool not delivered after the restart: $(collector_spool_messages) message(s), ingest queue $(ingest_queue_depth)")

    # The heartbeat after the restart and the accounting.
    collector_reports "$started" "$id" "$COLLECTOR_REPORTS"
    heartbeat_report="$(tail -n +"$((reports_before + 1))" "$COLLECTOR_REPORTS" | jq -c 'select(.backpressure == true)' 2>/dev/null | head -n 1)"
    if [ -n "$heartbeat_report" ]; then
        detail collector "- Heartbeat after the restart: spool report \`$heartbeat_report\`"
    else
        heartbeat_report="$(tail -n +"$((reports_before + 1))" "$COLLECTOR_REPORTS" | paste -sd ' ' -)"
        detail collector "- Spool reports of the heartbeats after the restart: ${heartbeat_report:-none}"
        failures+=("no heartbeat after the restart reported backpressure")
    fi
    collector_refresh
    passes="$(collector_passes)"
    sent="$(collector_sent)"
    ingested="$(collector_ingested "$id")"
    reported="$(jq -s 'map((.dropped_messages // 0) + (.dropped_samples // 0)) | add // 0' "$COLLECTOR_REPORTS" 2>/dev/null)"
    logged="$(collector_logged_loss)"
    detail collector "- After the recovery: $sent records sent in $passes passes, ${ingested:-?} ingested; loss counter: ${reported:-?} reported by the heartbeats, $logged logged by the collector"
    # The accounting leads the details, so the protocol keeps it in full.
    if [ "${ingested:-?}" != "$sent" ]; then
        if [ -n "$ingested" ] && [ "$ingested" -lt "$sent" ]; then
            accounting+=("records: sent $sent in $passes passes != ingested $ingested (lost $((sent - ingested)))")
        else
            accounting+=("records: sent $sent in $passes passes != ingested ${ingested:-?}")
        fi
    fi
    if [ "${reported:-?}" != 0 ] || [ "$logged" != 0 ]; then
        accounting+=("loss counter: ${reported:-?} reported by the heartbeats, $logged logged by the collector")
    fi
    failures=("${accounting[@]}" "${failures[@]}")

    if [ "${#failures[@]}" -eq 0 ]; then
        report PASS "$name" "server and nats-edge down ${down}s; spool $spool_before → $spool_during, backpressure reported by the heartbeat after the restart; $sent records sent in $passes passes = $ingested ingested; loss counter 0"
    else
        report FAIL "$name" "$(oneline "$(joined "${failures[@]}")")"
        tail -n 40 "$COLLECTOR_LOG" >&2 || true
    fi
    collector_cleanup
}
