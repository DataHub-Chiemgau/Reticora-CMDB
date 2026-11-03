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
# The scenario (E-144): Mailpit is stopped; /readyz reports smtp
# unavailable; the load of load.sh runs. An invitation issued during the
# outage is refused visibly: the invitation mail goes synchronously over the
# central SMTP server (user.InvitationDelivery, never over a channel of the
# organization), so the API answers 503 "the invitation e-mail could not be
# sent" (ErrInvitationMail), no user_invitation row is stored and Mailpit
# never gets a mail to the invitee. Deferral and retry are shown with another
# e-mail notification on the channel central_smtp: an export (POST /exports
# with the API key of the load) completes, and export.completed notifies its
# initiator, the owner of the key (the seeded administrator). That delivery
# must be pending with attempts >= 1 and a last_error while /readyz still
# reports smtp unavailable. After Mailpit is started again it must be sent
# within SMTP_SENT_TIMEOUT (120 s), and Mailpit must hold exactly one message
# to the initiator with the delivery's subject (other mails to the
# administrator, e.g. review digests, have other subjects) and none to the
# invitee; then the accounting of load.sh.

MAILPIT_IMAGE="${MAILPIT_IMAGE:-axllent/mailpit:v1.21}"
SMTP_SENT_TIMEOUT="${SMTP_SENT_TIMEOUT:-120}"
# Seconds to wait for the failed attempt (export worker every 2 s, event
# outbox, notifier cycle of 5 s).
SMTP_ATTEMPT_TIMEOUT="${SMTP_ATTEMPT_TIMEOUT:-90}"
# Seconds Mailpit stays down after the failed attempt was seen. The notifier
# retries 2 minutes after the first failure (notify.Notifier.Backoff), so
# the retry falls within SMTP_SENT_TIMEOUT of the restart only when the
# restart is not right after the failure.
SMTP_HOLD_AFTER_FAILURE="${SMTP_HOLD_AFTER_FAILURE:-25}"
# Name of the e-mail channel of the scenarios' organization.
SMTP_CHANNEL_NAME="${SMTP_CHANNEL_NAME:-outage-central-smtp}"
# The answer of a refused invitation (user.ErrInvitationMail).
SMTP_INVITATION_REFUSED="the invitation e-mail could not be sent"

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

# smtp_delivery <dedup key>: the delivery of the channel with the key as
# JSON (id, recipient e-mail, subject, status, attempts, last_error,
# next_attempt_at, sent_at), empty without one.
smtp_delivery() {
    [ "$(api GET '/notification-deliveries?limit=200')" = 200 ] || return 0
    jq -c --arg c "$SMTP_CHANNEL_ID" --arg k "$1" \
        '[.data[]? | select(.channel_id == $c and (.dedup_key // "") == $k)][0]
         // empty | {id, email: (.recipient.email // ""), subject: (.subject // ""), status, attempts, last_error, next_attempt_at, sent_at}' "$API_OUT"
}

# smtp_export_key <export job id>: sets SMTP_DEDUP_KEY, the dedup key of the
# notification of export.completed of the job ("export.completed:<event
# id>", notify.EventConsumer); fails until the job is completed.
smtp_export_key() {
    local id
    id="$(psql_exec -c "SELECT id FROM event_outbox WHERE organization_id = '$OUTAGE_ORG_ID' AND type = 'export.completed' AND entity_id = '$1' ORDER BY occurred_at DESC LIMIT 1" 2>/dev/null)"
    [ -n "$id" ] || return 1
    SMTP_DEDUP_KEY="export.completed:$id"
}

# smtp_failed_attempt <dedup key>: the delivery is pending after at least
# one failed attempt with its error.
smtp_failed_attempt() {
    SMTP_DELIVERY="$(smtp_delivery "$1")"
    [ -n "$SMTP_DELIVERY" ] && printf '%s' "$SMTP_DELIVERY" |
        jq -e '.status == "pending" and .attempts >= 1 and ((.last_error // "") != "")' >/dev/null
}

# smtp_export_attempt <export job id>: the job is completed and the
# delivery of its export.completed on the channel is pending after a failed
# attempt.
smtp_export_attempt() {
    smtp_export_key "$1" && smtp_failed_attempt "$SMTP_DEDUP_KEY"
}

smtp_sent() {
    SMTP_DELIVERY="$(smtp_delivery "$1")"
    [ -n "$SMTP_DELIVERY" ] && printf '%s' "$SMTP_DELIVERY" | jq -e '.status == "sent"' >/dev/null
}

# delivery_text: SMTP_DELIVERY as evidence for the protocol.
delivery_text() {
    local d="${SMTP_DELIVERY:-}"
    [ -n "$d" ] || d='{}'
    printf '%s' "$d" | jq -j '"id \(.id // "-"), to \(.email // "-"), subject \"\(.subject // "")\", status \(.status // "-"), attempts \(.attempts // "-"), last_error \"\(.last_error // "")\", next attempt \(.next_attempt_at // "-"), sent_at \(.sent_at // "-")"' 2>/dev/null |
        tr '|\n\r' '/  '
}

# mailpit_count <Mailpit search query> prints the messages that match in
# Mailpit and the messages of the mailbox ("<matching> <mailbox>"), "? ?"
# without an answer.
mailpit_count() {
    local query out
    query="$(jq -rn --arg q "$1" '$q | @uri')"
    out="$(mailpit_get "/api/v1/search?query=$query" |
        jq -r '"\(.messages_count // (.messages | length)) \(.total // "?")"' 2>/dev/null)"
    printf '%s' "${out:-? ?}"
}

smtp_outage() {
    local name="smtp outage" restarts failures=() started remaining hold=0 email role invite refusal rows
    local export job initiator subject recovered sent_after="-" counts invitee r
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
    SMTP_DELIVERY="" SMTP_DEDUP_KEY=""

    compose stop mailpit >/dev/null 2>&1
    started=$SECONDS
    wait_for 60 "/readyz to report smtp unavailable" check_unavailable smtp ||
        failures+=("readiness did not report smtp unavailable: $(oneline "$(readyz)")")
    load_send smtp

    # The invitation is refused visibly and rolled back, never deferred
    # (E-144).
    email="outage-$(date -u +%Y%m%d%H%M%S)-$RANDOM@reticora-outage.example"
    invite="$(api POST /invitations "$(jq -cn --arg e "$email" --arg r "$role" '{email: $e, role_id: $r, scope_type: "org"}')")"
    refusal="$(jq -r '.detail // empty | gsub("[|\r\n]"; " ")' "$API_OUT" 2>/dev/null)"
    if [ "$invite" != 503 ] || [ "$refusal" != "$SMTP_INVITATION_REFUSED" ]; then
        failures+=("invitation to $email: HTTP $invite \"$refusal\", want 503 \"$SMTP_INVITATION_REFUSED\" (E-144)")
    fi
    rows="$(psql_exec -c "SELECT count(*) FROM user_invitation WHERE organization_id = '$OUTAGE_ORG_ID' AND lower(email) = lower('$email')" 2>/dev/null)"
    [ "$rows" = 0 ] || failures+=("invitation to $email stored although refused: ${rows:-?} user_invitation rows, want 0")
    detail smtp "- Invitation to \`$email\` during the outage: HTTP $invite \"$refusal\", ${rows:-?} user_invitation rows (E-144: the invitation mail goes synchronously over the central SMTP server; refused and rolled back while it is down)"

    # Deferral and retry: export.completed to the initiator over central_smtp.
    export="$(api POST /exports '{"format":"json"}')"
    job="$(jq -r '.id // empty' "$API_OUT" 2>/dev/null)"
    if [ "$export" != 202 ] || [ -z "$job" ]; then
        failures+=("export (POST /exports): HTTP $export $(oneline "$(head -c 200 "$API_OUT" 2>/dev/null)")")
    fi
    initiator="$(psql_exec -c "SELECT u.email FROM export_job j JOIN app_user u ON u.id = j.initiated_by WHERE j.organization_id = '$OUTAGE_ORG_ID' AND j.id::text = '${job:-none}'" 2>/dev/null)"
    [ -n "$initiator" ] || failures+=("export job ${job:--} has no initiator with an e-mail address (initiated_by of the API key)")
    detail smtp "- E-mail channel \`$SMTP_CHANNEL_NAME\` ($SMTP_CHANNEL_ID, central_smtp); export ${job:--} (POST /exports: HTTP $export) during the outage, export.completed to its initiator \`${initiator:--}\`"
    if [ -n "$job" ] && wait_for "$SMTP_ATTEMPT_TIMEOUT" "a failed delivery attempt of export.completed of $job" smtp_export_attempt "$job"; then
        hold=$((SECONDS - started + SMTP_HOLD_AFTER_FAILURE))
        detail smtp "- During the outage: delivery $(delivery_text)"
        printf '%s' "$SMTP_DELIVERY" | jq -e --arg e "$initiator" '$e != "" and (.email | ascii_downcase) == ($e | ascii_downcase)' >/dev/null ||
            failures+=("delivery of export.completed not addressed to the initiator ${initiator:--}: $(delivery_text)")
    else
        failures+=("no pending delivery of export.completed with a failed attempt and last_error: $(delivery_text)")
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
    if [ -n "$SMTP_DEDUP_KEY" ]; then
        if wait_for "$SMTP_SENT_TIMEOUT" "the delivery of export.completed of $job" smtp_sent "$SMTP_DEDUP_KEY"; then
            sent_after=$((SECONDS - started))
            detail smtp "- After Mailpit was started again: sent after ${sent_after}s (limit ${SMTP_SENT_TIMEOUT}s), delivery $(delivery_text)"
        else
            failures+=("delivery not sent within ${SMTP_SENT_TIMEOUT}s: $(delivery_text)")
        fi
        # Only the mail of this delivery: other mails to the administrator
        # (review digests of the load, collector notices) have other
        # subjects.
        subject="$(printf '%s' "$SMTP_DELIVERY" | jq -r '.subject // empty' 2>/dev/null)"
        counts="$(mailpit_count "to:\"$initiator\" subject:\"$subject\"")"
        detail smtp "- Mailpit: ${counts% *} message(s) to \`$initiator\` with the subject \"$subject\", ${counts#* } in the mailbox"
        [ "${counts% *}" = 1 ] || failures+=("Mailpit holds ${counts% *} messages to $initiator with the subject \"$subject\", want exactly 1")
    fi
    invitee="$(mailpit_count "to:\"$email\"")"
    detail smtp "- Mailpit: ${invitee% *} message(s) to the refused invitee \`$email\`"
    [ "${invitee% *}" = 0 ] || failures+=("Mailpit holds ${invitee% *} messages to the refused invitee $email, want 0")
    wait_for "$RECOVERY_TIMEOUT" "readiness after restarting mailpit" ready ||
        failures+=("no recovery within ${RECOVERY_TIMEOUT}s: $(oneline "$(readyz)")")
    load_verify smtp "$recovered" || failures+=("${LOAD_PROBLEMS[@]}")
    [ "$(server_restarts)" = "$restarts" ] || failures+=("server restarted ($restarts → $(server_restarts))")
    detail recovery "| smtp | $LOAD_RECOVERY | $restarts → $(server_restarts) |"

    if [ "${#failures[@]}" -eq 0 ]; then
        report PASS "$name" "readiness 503 with smtp unavailable; invitation refused with 503, no row, no mail (E-144); export.completed pending (attempts >= 1, last_error) during the outage, sent ${sent_after}s after the restart, 1 mail to the initiator; load: $LOAD_SUMMARY; $LOAD_RECOVERY"
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
