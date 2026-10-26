#!/bin/sh
# Serves a renewed certificate without a restart (WP-440, F-139). The certbot
# service renews live/<domain>/ in ./letsencrypt (mounted read-only here),
# but nginx keeps the certificate it loaded at start in memory and would serve
# the old one until it expires. With RETICORA_TLS_DOMAIN set, this hook starts
# a background loop: every RETICORA_TLS_RELOAD_INTERVAL seconds (default
# 21600 = 6 h) it compares the checksum of
# /etc/letsencrypt/live/<domain>/fullchain.pem with the one nginx loaded and,
# only when it changed, runs 'nginx -t' and then 'nginx -s reload'. A failed
# test is logged, nothing is reloaded and nginx keeps serving its running
# configuration; the next round tries again. Without a domain no loop starts.
#
# Runs as the container user (nginx, uid 101) right before nginx starts, so
# the checksum taken here is the one of the chain nginx loads. The loop ends
# with the container.
set -eu

ME="30-reticora-cert-reload.sh"
DEFAULT_INTERVAL=21600

log() { echo "$ME: $*" >&2; }

domain="${RETICORA_TLS_DOMAIN:-}"
[ -n "$domain" ] || exit 0
# The domain becomes a path; a failing hook would stop the container start.
case "$domain" in
    *[!A-Za-z0-9.-]* | .* | *..*)
        log "RETICORA_TLS_DOMAIN '$domain' is not a domain name; no certificate reload loop"
        exit 0
        ;;
esac

interval="${RETICORA_TLS_RELOAD_INTERVAL:-$DEFAULT_INTERVAL}"
case "$interval" in
    '' | *[!0-9]* | 0*)
        log "RETICORA_TLS_RELOAD_INTERVAL '$interval' is not a positive number of seconds; using $DEFAULT_INTERVAL"
        interval="$DEFAULT_INTERVAL"
        ;;
esac

chain="/etc/letsencrypt/live/$domain/fullchain.pem"

# Empty while the file is missing or unreadable (e.g. moved aside by the
# installer during an issuance); nginx -t then fails and nothing is reloaded.
checksum() { sha256sum "$chain" 2>/dev/null | cut -d ' ' -f 1; }

reload_loop() {
    set +e
    while :; do
        sleep "$interval"
        current="$(checksum)"
        [ "$current" != "$loaded" ] || continue
        if out="$(nginx -t 2>&1)"; then
            if nginx -s reload; then
                loaded="$current"
                log "live/$domain/fullchain.pem changed, nginx reloaded"
            else
                log "live/$domain/fullchain.pem changed, but nginx -s reload failed; nginx keeps serving the previous certificate"
            fi
        else
            log "live/$domain/fullchain.pem changed, but nginx -t failed; nothing reloaded, nginx keeps serving the previous certificate:"
            printf '%s\n' "$out" >&2
        fi
    done
}

loaded="$(checksum)"
reload_loop </dev/null &
log "reloads nginx when live/$domain/fullchain.pem changes (checked every $interval s)"
