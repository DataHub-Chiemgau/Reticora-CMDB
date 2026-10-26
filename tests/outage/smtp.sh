#!/usr/bin/env bash
#
# smtp.sh — SMTP outage scenario of the outage suite (WP-449, OPS-06,
# F-135). Sourced by run.sh after lib.sh and load.sh; run directly it
# prepares the stack:
#
#   ./tests/outage/smtp.sh overlay deploy/docker-compose/docker-compose.override.yml
#
# merges Mailpit (MAILPIT_IMAGE, API on 127.0.0.1:8025) and the central SMTP
# server of the installation (RETICORA_SMTP_HOST=mailpit, port 1025, no TLS)
# into the override that install-cloud.sh passes to compose in its source
# build (outage.yml runs it before the installation; only in that checkout,
# the comments of the file are not kept). The server then depends on
# Mailpit, and /readyz has the check smtp from the installation on.
#
# The scenario: Mailpit is stopped; /readyz reports smtp unavailable; the
# load of load.sh runs; an invitation (a notification of the e-mail channel
# central_smtp to the invitee) is issued. Its delivery must be pending with
# attempts >= 1 and a last_error while /readyz still reports smtp
# unavailable. After Mailpit is started again the delivery must be sent
# within SMTP_SENT_TIMEOUT (120 s) and Mailpit must hold exactly one
# message to the invitee; then the accounting of load.sh.

MAILPIT_IMAGE="${MAILPIT_IMAGE:-axllent/mailpit:v1.21}"
SMTP_SENT_TIMEOUT="${SMTP_SENT_TIMEOUT:-120}"
# Seconds to wait for the failed attempt (outbox, notifier cycle of 5 s).
SMTP_ATTEMPT_TIMEOUT="${SMTP_ATTEMPT_TIMEOUT:-60}"
# Seconds Mailpit stays down after the failed attempt was seen. The notifier
# retries 2 minutes after the first failure (notify.Notifier.Backoff), so
# the retry falls within SMTP_SENT_TIMEOUT of the restart only when the
# restart is not right after the failure.
SMTP_HOLD_AFTER_FAILURE="${SMTP_HOLD_AFTER_FAILURE:-25}"
# Name of the e-mail channel of the scenarios' organization.
SMTP_CHANNEL_NAME="${SMTP_CHANNEL_NAME:-outage-central-smtp}"

# smtp_overlay <override file>: see the header. Needs python3 with PyYAML.
smtp_overlay() {
    python3 - "$1" "$MAILPIT_IMAGE" <<'PY'
import sys

import yaml

path, image = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as f:
    doc = yaml.safe_load(f) or {}
services = doc.setdefault("services", {})
services["mailpit"] = {
    "image": image,
    "restart": "unless-stopped",
    "ports": ["127.0.0.1:8025:8025"],
}
server = services.setdefault("server", {})
env = server.setdefault("environment", {})
deps = server.setdefault("depends_on", {})
if not isinstance(env, dict) or not isinstance(deps, dict):
    sys.exit(f"{path}: server.environment and server.depends_on must be maps")
env.update({
    "RETICORA_SMTP_HOST": "mailpit",
    "RETICORA_SMTP_PORT": "1025",
    "RETICORA_SMTP_TLS": "none",
    "RETICORA_SMTP_FROM": "Reticora Outage <noreply@reticora-outage.example>",
})
deps["mailpit"] = {"condition": "service_started"}
with open(path, "w", encoding="utf-8") as f:
    yaml.safe_dump(doc, f, sort_keys=False)
PY
}

# smtp_channel: the e-mail channel (central_smtp) of the scenarios'
# organization, created once; sets SMTP_CHANNEL_ID.
smtp_channel() {
    local status
    status="$(api GET /notification-channels)"
    [ "$status" = 200 ] || return 1
    SMTP_CHANNEL_ID="$(jq -r --arg n "$SMTP_CHANNEL_NAME" '[.data[]? | select(.name == $n) | .id][0] // empty' "$API_OUT")"
    [ -n "$SMTP_CHANNEL_ID" ] && return 0
    status="$(api POST /notification-channels "$(jq -cn --arg n "$SMTP_CHANNEL_NAME" '{name: $n, provider: "central_smtp", enabled: true}')")"
    [ "$status" = 201 ] || return 1
    SMTP_CHANNEL_ID="$(jq -r '.id // empty' "$API_OUT")"
    [ -n "$SMTP_CHANNEL_ID" ]
}

# smtp_delivery <email>: the newest delivery of the channel to the address
# as JSON (id, status, attempts, last_error, next_attempt_at, sent_at),
# empty without one.
smtp_delivery() {
    [ "$(api GET '/notification-deliveries?limit=200')" = 200 ] || return 0
    jq -c --arg c "$SMTP_CHANNEL_ID" --arg e "$1" \
        '[.data[]? | select(.channel_id == $c and ((.recipient.email // "") | ascii_downcase) == ($e | ascii_downcase))][0]
         // empty | {id, status, attempts, last_error, next_attempt_at, sent_at}' "$API_OUT"
}

# smtp_failed_attempt <email>: the delivery is pending after at least one
# failed attempt with its error.
smtp_failed_attempt() {
    SMTP_DELIVERY="$(smtp_delivery "$1")"
    [ -n "$SMTP_DELIVERY" ] && printf '%s' "$SMTP_DELIVERY" |
        jq -e '.status == "pending" and .attempts >= 1 and ((.last_error // "") != "")' >/dev/null
}

smtp_sent() {
    SMTP_DELIVERY="$(smtp_delivery "$1")"
    [ -n "$SMTP_DELIVERY" ] && printf '%s' "$SMTP_DELIVERY" | jq -e '.status == "sent"' >/dev/null
}

# delivery_text: SMTP_DELIVERY as evidence for the protocol.
delivery_text() {
    local d="${SMTP_DELIVERY:-}"
    [ -n "$d" ] || d='{}'
    printf '%s' "$d" | jq -j '"id \(.id // "-"), status \(.status // "-"), attempts \(.attempts // "-"), last_error \"\(.last_error // "")\", next attempt \(.next_attempt_at // "-"), sent_at \(.sent_at // "-")"' 2>/dev/null |
        tr '|\n\r' '/  '
}

# mailpit_count <email> prints the messages to the address in Mailpit and
# the messages of the mailbox ("<to address> <mailbox>"), "? ?" without an
# answer.
mailpit_count() {
    local query out
    query="$(jq -rn --arg q "to:\"$1\"" '$q | @uri')"
    out="$(mailpit_get "/api/v1/search?query=$query" |
        jq -r '"\(.messages_count // (.messages | length)) \(.total // "?")"' 2>/dev/null)"
    printf '%s' "${out:-? ?}"
}

smtp_outage() {
    local name="smtp outage" restarts failures=() started remaining hold=0 email role invite recovered sent_after counts r
    if ! ready; then
        report FAIL "$name" "stack not ready before the outage: $(oneline "$(readyz)")"
        return
    fi
    r="$(readyz)"
    case "$r" in
        *'"smtp":"ok"'*) ;;
        *) report FAIL "$name" "/readyz has no smtp check: the server has no RETICORA_SMTP_HOST (smtp.sh overlay before the installation): $(oneline "$r")"
           return ;;
    esac
    if ! smtp_channel; then
        report FAIL "$name" "e-mail channel $SMTP_CHANNEL_NAME (central_smtp) not available: $(oneline "$(head -c 200 "$API_OUT" 2>/dev/null)")"
        return
    fi
    role="$(psql_exec -c "SELECT id FROM role WHERE organization_id = '$OUTAGE_ORG_ID' AND name = 'viewer'")"
    restarts="$(server_restarts)"

    compose stop mailpit >/dev/null 2>&1
    started=$SECONDS
    wait_for 60 "/readyz to report smtp unavailable" check_unavailable smtp ||
        failures+=("readiness did not report smtp unavailable: $(oneline "$(readyz)")")
    load_send smtp
    email="outage-$(date -u +%Y%m%d%H%M%S)-$RANDOM@reticora-outage.example"
    invite="$(api POST /invitations "$(jq -cn --arg e "$email" --arg r "$role" '{email: $e, role_id: $r, scope_type: "org"}')")"
    [ "$invite" = 201 ] || failures+=("invitation to $email: $invite $(oneline "$(head -c 200 "$API_OUT")")")
    detail smtp "- E-mail channel \`$SMTP_CHANNEL_NAME\` ($SMTP_CHANNEL_ID, central_smtp); invitation to \`$email\` issued during the outage: HTTP $invite"
    if wait_for "$SMTP_ATTEMPT_TIMEOUT" "a failed delivery attempt to $email" smtp_failed_attempt "$email"; then
        hold=$((SECONDS - started + SMTP_HOLD_AFTER_FAILURE))
        detail smtp "- During the outage: delivery $(delivery_text)"
    else
        failures+=("no pending delivery with a failed attempt and last_error: $(delivery_text)")
    fi
    r="$(readyz)"
    if check_unavailable smtp; then
        detail smtp "- /readyz during the outage: \`$(oneline "$r")\`"
    else
        failures+=("readiness changed during the outage: $(oneline "$r")")
    fi
    # The outage lasts OUTAGE_SECONDS at least, and SMTP_HOLD_AFTER_FAILURE
    # after the failed attempt.
    [ "$hold" -gt "$OUTAGE_SECONDS" ] || hold="$OUTAGE_SECONDS"
    remaining=$((hold - (SECONDS - started)))
    [ "$remaining" -gt 0 ] && sleep "$remaining"
    server_alive || failures+=("server process stopped during the outage")
    detail smtp "- Mailpit down for $((SECONDS - started))s"

    compose start mailpit >/dev/null 2>&1
    recovered="$(utc_now)"
    started=$SECONDS
    if wait_for "$SMTP_SENT_TIMEOUT" "the delivery to $email" smtp_sent "$email"; then
        sent_after=$((SECONDS - started))
        detail smtp "- After Mailpit was started again: sent after ${sent_after}s (limit ${SMTP_SENT_TIMEOUT}s), delivery $(delivery_text)"
    else
        failures+=("delivery not sent within ${SMTP_SENT_TIMEOUT}s: $(delivery_text)")
    fi
    counts="$(mailpit_count "$email")"
    detail smtp "- Mailpit: ${counts% *} message(s) to \`$email\`, ${counts#* } in the mailbox"
    [ "${counts% *}" = 1 ] || failures+=("Mailpit holds ${counts% *} messages to $email, want exactly 1")
    wait_for "$RECOVERY_TIMEOUT" "readiness after restarting mailpit" ready ||
        failures+=("no recovery within ${RECOVERY_TIMEOUT}s: $(oneline "$(readyz)")")
    load_verify smtp "$recovered" || failures+=("${LOAD_PROBLEMS[@]}")
    [ "$(server_restarts)" = "$restarts" ] || failures+=("server restarted ($restarts → $(server_restarts))")
    detail recovery "| smtp | $LOAD_RECOVERY | $restarts → $(server_restarts) |"

    if [ "${#failures[@]}" -eq 0 ]; then
        report PASS "$name" "readiness 503 with smtp unavailable; delivery pending (attempts >= 1, last_error) during the outage, sent ${sent_after}s after the restart, 1 mail to the invitee; load: $LOAD_SUMMARY; $LOAD_RECOVERY"
    else
        report FAIL "$name" "$(oneline "$(joined "${failures[@]}")")"
        compose logs --tail 30 server >&2 || true
    fi
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    set -euo pipefail
    case "${1:-}" in
        overlay) smtp_overlay "${2:?usage: smtp.sh overlay <docker-compose.override.yml>}" ;;
        *) echo "usage: smtp.sh overlay <docker-compose.override.yml> (the scenario runs from run.sh)" >&2; exit 2 ;;
    esac
fi
