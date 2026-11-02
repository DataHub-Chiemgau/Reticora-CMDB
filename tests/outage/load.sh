#!/usr/bin/env bash
#
# load.sh — load during an outage and its accounting (WP-449, OPS-06,
# F-136). Sourced by run.sh after lib.sh.
#
# While a dependency is down (postgres, nats, redis, minio, smtp) the suite
# sends LOAD_API_WRITES API writes (POST /api/v1/cis, one CI each) and
# LOAD_INGEST_RECORDS ingest records (POST /api/v1/ingest/bulk, one
# DeviceRecord per request) with the API key of the scenarios'
# organization. Every answer must be 2xx (accepted) or 429/503 (visibly
# refused). After the dependency is back:
#   - the ingest queue is empty (stream RETICORA_INGEST, a work queue: a
#     message leaves it when the consumer acknowledged it after the commit),
#   - the event outbox holds no unpublished entry that occurred before the
#     recovery (so none from before or during the outage),
#   - accepted == persisted, counted in the database: the CIs whose serial
#     number carries the run's prefix are exactly the accepted writes and
#     records. An accepted one that is missing is lost; one that was
#     refused and is stored anyway is late (the client was told it was not
#     accepted). Timing and log lines decide nothing.
# Every count goes to the protocol (run.sh).
#
# Negative self-test OUTAGE_SELFTEST_DROP=1: the last ingest record of every
# load is dropped on purpose (counted as accepted with 202, never sent), so
# the accounting must report it as lost and the scenario must FAIL.

LOAD_API_WRITES="${LOAD_API_WRITES:-50}"
LOAD_INGEST_RECORDS="${LOAD_INGEST_RECORDS:-50}"
# Slug prefix of the scenarios' organization (cmd/seed -prefix).
OUTAGE_ORG_PREFIX="${OUTAGE_ORG_PREFIX:-outage-}"

# load_setup <dir>: the organization of the scenarios, seeded with cmd/seed
# (plan pro, an administrator with org_admin and an API key of the
# administrator; OUTAGE_SEED_BIN or go run). The seed's webhook subscription
# (an echo URL that does not exist) is switched off, so the load causes no
# failing webhook deliveries. Sets LOAD_DIR, API_OUT, OUTAGE_KEY_HEADER,
# OUTAGE_ORG_ID and LOAD_CI_TYPE_ID.
load_setup() {
    LOAD_DIR="$1"
    mkdir -p "$LOAD_DIR"
    API_OUT="$LOAD_DIR/response.json"
    OUTAGE_KEY_HEADER="$LOAD_DIR/key.header"
    local seed=(go run ./cmd/seed)
    [ -n "${OUTAGE_SEED_BIN:-}" ] && seed=("$OUTAGE_SEED_BIN")
    # The database URL with its password goes through the environment
    # (cmd/seed reads RETICORA_DATABASE_URL), not the command line. It is
    # read before the cd: COMPOSE_DIR is relative to the repository root.
    local db_url
    db_url="$(db_admin_url)"
    if ! (cd backend && RETICORA_DATABASE_URL="$db_url" "${seed[@]}" -prefix "$OUTAGE_ORG_PREFIX" \
        -api-key-file "$LOAD_DIR/key") > "$LOAD_DIR/seed.log" 2>&1; then
        LOAD_SETUP_ERROR="cmd/seed failed: $(tail -n 3 "$LOAD_DIR/seed.log" | tr '\n' ' ')"
        return 1
    fi
    (umask 077 && printf 'X-API-Key: %s\n' "$(head -n 1 "$LOAD_DIR/key")" > "$OUTAGE_KEY_HEADER")
    OUTAGE_ORG_ID="$(psql_exec -c "SELECT id FROM organization WHERE slug = '${OUTAGE_ORG_PREFIX}demo'")"
    LOAD_CI_TYPE_ID="$(psql_exec -c "SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL AND is_active")"
    if [ -z "$OUTAGE_ORG_ID" ] || [ -z "$LOAD_CI_TYPE_ID" ]; then
        # shellcheck disable=SC2034 # reported by run.sh
        LOAD_SETUP_ERROR="organization ${OUTAGE_ORG_PREFIX}demo or CI type server not found"
        return 1
    fi
    psql_exec -c "UPDATE webhook_subscription SET is_active = false WHERE organization_id = '$OUTAGE_ORG_ID'" >/dev/null
}

load_ci_body() { # <serial>
    printf '{"ci_type_id":"%s","name":"%s","serial_number":"%s","status":"active"}' "$LOAD_CI_TYPE_ID" "$1" "$1"
}

# load_ingest_body <serial> <n>: a DeviceRecord of a new server. The serial
# number and the host name are unique; the address alone identifies no
# device (REC-02 needs it with a sysObjectID).
load_ingest_body() {
    local lower
    lower="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
    printf '{"records":[{"record_id":"%s","observed_at":"%s","source":"api","target_ip":"10.249.%d.%d","identity":{"serial_number":"%s","hostname":"%s"},"classification":{"ci_type":"server","manufacturer":"Reticora Outage","model":"OPS-06"},"system":{"name":"%s"}}]}' \
        "$(uuid4)" "$(utc_now)" $(($2 / 250)) $(($2 % 250 + 1)) "$1" "$lower" "$lower"
}

# load_send <scenario>: the load of one outage. One line per request in
# $LOAD_DIR/<scenario>.api and .ingest: serial number and HTTP status (a
# 202 whose body refuses the record is "202-refused").
load_send() {
    local scenario="$1" i serial status
    LOAD_RUN="OUTAGE-$(date -u +%Y%m%d%H%M%S)-$scenario"
    : > "$LOAD_DIR/$scenario.api"
    : > "$LOAD_DIR/$scenario.ingest"
    for i in $(seq 1 "$LOAD_API_WRITES"); do
        serial="$LOAD_RUN-A$i"
        status="$(api POST /cis "$(load_ci_body "$serial")")"
        printf '%s %s\n' "$serial" "$status" >> "$LOAD_DIR/$scenario.api"
    done
    for i in $(seq 1 "$LOAD_INGEST_RECORDS"); do
        serial="$LOAD_RUN-I$i"
        if [ "${OUTAGE_SELFTEST_DROP:-0}" = 1 ] && [ "$i" -eq "$LOAD_INGEST_RECORDS" ]; then
            # Dropped on purpose: counted as accepted, never sent.
            status=202
        else
            status="$(api POST /ingest/bulk "$(load_ingest_body "$serial" "$i")")"
            case "$status" in
                2??) [ "$(jq -r '.rejected_future // 0' "$API_OUT" 2>/dev/null)" = 0 ] || status="$status-refused" ;;
            esac
        fi
        printf '%s %s\n' "$serial" "$status" >> "$LOAD_DIR/$scenario.ingest"
    done
}

# ingest_queue_depth prints the messages in RETICORA_INGEST, "?" when the
# NATS monitor does not answer.
ingest_queue_depth() {
    local n
    n="$(nats_jsz | jq -r '[.account_details[]?.stream_detail[]? | select(.name == "RETICORA_INGEST") | .state.messages] | add // 0' 2>/dev/null)"
    printf '%s' "${n:-?}"
}

ingest_queue_empty() { [ "$(ingest_queue_depth)" = 0 ]; }

# outbox_pending <timestamp> prints the unpublished outbox entries that
# occurred up to the timestamp, "?" without an answer.
outbox_pending() {
    local n
    n="$(psql_exec -c "SELECT count(*) FROM event_outbox WHERE published_at IS NULL AND occurred_at <= '$1'" 2>/dev/null)"
    printf '%s' "${n:-?}"
}

outbox_published() { [ "$(outbox_pending "$1")" = 0 ]; }

# load_persisted <scenario>: the serial numbers of the run's CIs in the
# database, to $LOAD_DIR/<scenario>.persisted.
load_persisted() {
    psql_exec -c "SELECT serial_number FROM ci WHERE organization_id = '$OUTAGE_ORG_ID'
        AND serial_number LIKE '$LOAD_RUN-%' AND deleted_at IS NULL ORDER BY 1" > "$LOAD_DIR/$1.persisted"
}

# sample_of <file>: ": " and up to three of its lines, nothing for an empty
# file.
sample_of() {
    [ -s "$1" ] && printf ': %s' "$(head -n 3 "$1" | paste -sd ' ' -)"
    return 0
}

# load_account <scenario> <kind: api|ingest>: compares the answers of the
# load with the database. Adds a row to the load section and a problem to
# LOAD_PROBLEMS on a mismatch; LOAD_SUMMARY gets a short line for the result
# table.
load_account() {
    local scenario="$1" kind="$2" infix answers dir sent accepted r429 r503 other persisted lost late
    infix=A
    [ "$kind" = ingest ] && infix=I
    answers="$LOAD_DIR/$scenario.$kind"
    dir="$LOAD_DIR/$scenario.$kind.d"
    mkdir -p "$dir"
    awk '$2 ~ /^2[0-9][0-9]$/ { print $1 }' "$answers" | sort > "$dir/accepted"
    grep -E -- "-${infix}[0-9]+\$" "$LOAD_DIR/$scenario.persisted" | sort > "$dir/persisted" || true
    sent="$(wc -l < "$answers")"
    accepted="$(wc -l < "$dir/accepted")"
    r429="$(awk '$2 == "429"' "$answers" | wc -l)"
    r503="$(awk '$2 == "503"' "$answers" | wc -l)"
    other=$((sent - accepted - r429 - r503))
    persisted="$(wc -l < "$dir/persisted")"
    comm -23 "$dir/accepted" "$dir/persisted" > "$dir/lost"
    comm -13 "$dir/accepted" "$dir/persisted" > "$dir/late"
    lost="$(wc -l < "$dir/lost")"
    late="$(wc -l < "$dir/late")"
    detail load "| $scenario | $kind | $sent | $accepted | $r429 | $r503 | $other | $persisted | $lost | $late |"
    LOAD_SUMMARY="$LOAD_SUMMARY${LOAD_SUMMARY:+; }$kind $accepted/$sent accepted = $persisted persisted"
    [ "$r429" -gt 0 ] && LOAD_SUMMARY="$LOAD_SUMMARY, $r429×429"
    [ "$r503" -gt 0 ] && LOAD_SUMMARY="$LOAD_SUMMARY, $r503×503"
    if [ "$other" -gt 0 ]; then
        LOAD_PROBLEMS+=("$kind: $other answers neither 2xx nor 429/503 ($(awk '$2 !~ /^(2[0-9][0-9]|429|503)$/ { print $2 }' "$answers" | sort | uniq -c | awk '{ printf "%s%s×%s", sep, $1, $2; sep=", " }'))")
    fi
    if [ "$lost" -gt 0 ] || [ "$late" -gt 0 ]; then
        LOAD_PROBLEMS+=("$kind: accepted $accepted != persisted $persisted (lost $lost$(sample_of "$dir/lost"), late $late$(sample_of "$dir/late"))")
    fi
}

# load_verify <scenario> <recovered at>: after the dependency is back, waits
# for the ingest queue and the event outbox, then counts. Returns non-zero
# with the reasons in LOAD_PROBLEMS.
load_verify() {
    local scenario="$1" recovered="$2" depth pending
    LOAD_PROBLEMS=()
    LOAD_SUMMARY=""
    wait_for "$RECOVERY_TIMEOUT" "an empty ingest queue" ingest_queue_empty || true
    wait_for "$RECOVERY_TIMEOUT" "the event outbox up to $recovered" outbox_published "$recovered" || true
    depth="$(ingest_queue_depth)"
    pending="$(outbox_pending "$recovered")"
    [ "$depth" = 0 ] || LOAD_PROBLEMS+=("ingest queue not empty: $depth messages")
    [ "$pending" = 0 ] || LOAD_PROBLEMS+=("event outbox: $pending entries up to $recovered unpublished")
    if load_persisted "$scenario"; then
        load_account "$scenario" api
        load_account "$scenario" ingest
    else
        LOAD_PROBLEMS+=("persisted CIs could not be counted")
    fi
    # shellcheck disable=SC2034 # reported by run.sh and smtp.sh
    LOAD_RECOVERY="ingest queue $depth, outbox $pending unpublished"
    [ "${#LOAD_PROBLEMS[@]}" -eq 0 ]
}
