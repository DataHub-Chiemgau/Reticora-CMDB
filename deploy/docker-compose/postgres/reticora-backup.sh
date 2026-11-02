#!/bin/sh
# reticora-backup.sh — scheduler of the backup container (WP-100, OPS-04).
#
#   reticora-backup.sh run          create the stanza, take a full backup when
#                                   none exists, then one full backup per day
#                                   at RETICORA_BACKUP_HOUR (UTC, 0..23,
#                                   default 2); each backup expires those
#                                   older than 35 days
#   reticora-backup.sh health       healthy while every S3 repository is
#                                   versioned and the newest backup is younger
#                                   than RETICORA_BACKUP_MAX_AGE_HOURS (26)
#   reticora-backup.sh verify-repo  checks that the bucket of every S3
#                                   repository has versioning Enabled
#
# WAL is archived continuously by the postgres container (archive_command,
# archive_timeout 300 s), so the restore point objective stays below 15
# minutes between the daily base backups (NFR-05).
set -eu

STANZA="${PGBACKREST_STANZA:-reticora}"
HOUR="${RETICORA_BACKUP_HOUR:-2}"
MAX_AGE_HOURS="${RETICORA_BACKUP_MAX_AGE_HOURS:-26}"
CONFIG="${PGBACKREST_CONFIG:-/etc/pgbackrest/pgbackrest.conf}"
NL='
'
CR="$(printf '\r')"

log() { printf '%s reticora-backup: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"; }

# last_backup_stop prints the stop time (epoch) of the newest backup.
last_backup_stop() {
    pgbackrest --stanza="$STANZA" --output=json info 2>/dev/null \
        | tr ',' '\n' | sed -n 's/.*"stop":\([0-9][0-9]*\).*/\1/p' | sort -n | tail -n 1
}

backup() {
    log "starting full backup"
    if pgbackrest --stanza="$STANZA" --type=full backup; then
        log "backup finished"
        return 0
    fi
    log "backup FAILED"
    return 1
}

# backup_hour <value> prints the hour 0..23 without leading zeros ("08" is
# 8; shell arithmetic would read it as a bad octal number) and fails for
# anything else (F-290).
backup_hour() {
    case "$1" in
        '' | *[!0-9]*) return 1 ;;
    esac
    v="$1"
    while [ "${#v}" -gt 1 ] && [ "${v#0}" != "$v" ]; do v="${v#0}"; done
    [ "${#v}" -le 2 ] && [ "$v" -le 23 ] || return 1
    printf '%s' "$v"
}

seconds_until_hour() {
    h="$(date -u +%H)"; m="$(date -u +%M)"; s="$(date -u +%S)"
    secs=$(( (HOUR - ${h#0}) * 3600 - ${m#0} * 60 - ${s#0} ))
    [ "$secs" -gt 0 ] || secs=$((secs + 86400))
    printf '%s' "$secs"
}

# ─── Bucket versioning of the S3 repositories (NFR-05, WP-443) ──────────────

# repo_opt <n> <option> prints repo<n>-<option> as pgBackRest reads it: the
# environment (PGBACKREST_REPO<n>_<OPTION>) before [global] of the
# configuration file.
repo_opt() {
    eval "v=\${PGBACKREST_REPO$1_$(printf '%s' "$2" | tr 'a-z-' 'A-Z_'):-}"
    if [ -z "$v" ] && [ -r "$CONFIG" ]; then
        v="$(awk -v key="repo$1-$2" '
            /^[[:space:]]*\[/ { global = ($0 ~ /^[[:space:]]*\[global\][[:space:]]*$/); next }
            global && index($0, "=") {
                k = $0; sub(/^[[:space:]]*/, "", k); sub(/[[:space:]]*=.*$/, "", k)
                if (k == key) { v = $0; sub(/^[^=]*=[[:space:]]*/, "", v); sub(/[[:space:]]*$/, "", v) }
            }
            END { print v }' "$CONFIG")"
    fi
    printf '%s' "$v"
}

# s3_repos prints the index of every repository of type s3.
s3_repos() {
    {
        env | sed -n 's/^PGBACKREST_REPO\([0-9][0-9]*\)_TYPE=.*/\1/p'
        [ ! -r "$CONFIG" ] || sed -n 's/^[[:space:]]*repo\([0-9][0-9]*\)-type[[:space:]]*=.*/\1/p' "$CONFIG"
    } | sort -un | while read -r n; do
        if [ "$(repo_opt "$n" type)" = s3 ]; then echo "$n"; fi
    done
}

# curl_config <key> <secret> [<token>] prints the curl configuration with
# the credentials. curl reads it from stdin, so they appear neither on a
# command line nor in a message.
curl_config() {
    printf 'user = "%s:%s"\n' "$(cfg_quote "$1")" "$(cfg_quote "$2")"
    [ -z "${3:-}" ] || printf 'header = "x-amz-security-token: %s"\n' "$(cfg_quote "$3")"
}
cfg_quote() { printf '%s' "$1" | sed 's/[\\"]/\\&/g'; }

# verify_bucket <n> reads the versioning of the bucket of S3 repository
# <n> with a signed GET ?versioning (SigV4, the repository's own key) and
# fails unless the status is Enabled. Messages name the repository, bucket
# and endpoint, never the key, the secret or the signature.
verify_bucket() {
    r="$1"
    bucket="$(repo_opt "$r" s3-bucket)"
    endpoint="$(repo_opt "$r" s3-endpoint)"
    region="$(repo_opt "$r" s3-region)"
    if [ -z "$bucket" ] || [ -z "$endpoint" ] || [ -z "$region" ]; then
        echo "repo$r: s3-bucket, s3-endpoint and s3-region are required to check the bucket versioning"
        return 1
    fi
    key="$(repo_opt "$r" s3-key)"
    secret="$(repo_opt "$r" s3-key-secret)"
    if [ -z "$key" ] || [ -z "$secret" ]; then
        echo "repo$r: s3-key and s3-key-secret are required to check the versioning of bucket $bucket"
        return 1
    fi
    token="$(repo_opt "$r" s3-token)"
    # One line each: a line break would end the curl configuration line
    # (and curl would quote the rest in its warning).
    case "$key$secret$token" in
        *"$NL"* | *"$CR"*)
            echo "repo$r: the S3 credentials of bucket $bucket contain a line break"
            return 1
            ;;
    esac
    authority="$endpoint"
    port="$(repo_opt "$r" storage-port)"
    [ -z "$port" ] || [ "$port" = 443 ] || authority="$endpoint:$port"
    if [ "$(repo_opt "$r" s3-uri-style)" = path ]; then
        url="https://$authority/$bucket?versioning="
    else
        url="https://$bucket.$authority/?versioning="
    fi
    # The SHA-256 of the empty body; S3 requires it with SigV4.
    set -- --silent --show-error --connect-timeout 5 --max-time 15 \
        --aws-sigv4 "aws:amz:$region:s3" \
        --header 'x-amz-content-sha256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855' \
        --write-out '\n%{http_code}'
    ca="$(repo_opt "$r" storage-ca-file)"
    [ -z "$ca" ] || set -- "$@" --cacert "$ca"
    ca="$(repo_opt "$r" storage-ca-path)"
    [ -z "$ca" ] || set -- "$@" --capath "$ca"
    [ "$(repo_opt "$r" storage-verify-tls)" != n ] || set -- "$@" --insecure
    # --disable (first): no ~/.curlrc.
    if ! resp="$(curl_config "$key" "$secret" "$token" | curl --disable --config - "$@" "$url")"; then
        echo "repo$r: the versioning of bucket $bucket could not be read from $endpoint"
        return 1
    fi
    code="$(printf '%s\n' "$resp" | tail -n 1)"
    body="$(printf '%s\n' "$resp" | sed '$d')"
    if [ "$code" != 200 ]; then
        # Only the error code: S3 error bodies can carry the access key and
        # the string to sign.
        err="$(printf '%s\n' "$body" | sed -n 's:.*<Code>\([A-Za-z0-9.]*\)</Code>.*:\1:p' | head -n 1)"
        echo "repo$r: reading the versioning of bucket $bucket from $endpoint failed: HTTP $code${err:+ $err}"
        return 1
    fi
    status="$(printf '%s\n' "$body" | sed -n 's:.*<Status>\([A-Za-z]*\)</Status>.*:\1:p' | head -n 1)"
    if [ "$status" = Enabled ]; then
        echo "repo$r: versioning of bucket $bucket is Enabled"
        return 0
    fi
    echo "repo$r: versioning of bucket $bucket is ${status:-not configured}, it must be Enabled (NFR-05)"
    return 1
}

verify_repo() {
    # Compose derives repo2 from RETICORA_BACKUP_S3_* in
    # reticora-pgbackrest-env.sh; without it the bucket would go unchecked.
    if [ -n "${RETICORA_BACKUP_S3_BUCKET:-}" ] && [ -z "${PGBACKREST_REPO2_TYPE:-}" ]; then
        echo "RETICORA_BACKUP_S3_BUCKET is set: run through reticora-pgbackrest-env.sh to check its versioning"
        return 1
    fi
    repos="$(s3_repos)"
    if [ -z "$repos" ]; then
        echo "no S3 repository configured, bucket versioning not checked"
        return 0
    fi
    if ! command -v curl >/dev/null 2>&1; then
        echo "curl is required to check the bucket versioning"
        return 1
    fi
    rc=0
    for n in $repos; do verify_bucket "$n" || rc=1; done
    return "$rc"
}

case "${1:-run}" in
    health)
        verify_repo || exit 1
        stop="$(last_backup_stop)"
        [ -n "$stop" ] || { echo "no backup yet"; exit 1; }
        age=$(( $(date +%s) - stop ))
        if [ "$age" -gt $((MAX_AGE_HOURS * 3600)) ]; then
            echo "newest backup is $((age / 3600)) h old"
            exit 1
        fi
        echo "newest backup is $((age / 60)) min old"
        ;;
    verify-repo)
        verify_repo
        ;;
    run)
        # Invalid settings stop the start with a message instead of a crash
        # or a silent midnight schedule later (F-290), and backups go only to
        # versioned buckets (NFR-05).
        if ! hour="$(backup_hour "$HOUR")"; then
            log "RETICORA_BACKUP_HOUR must be an hour 0..23 (UTC), not '$HOUR'"
            exit 2
        fi
        HOUR="$hour"
        if ! verify_repo; then
            log "bucket versioning check FAILED, not starting"
            exit 1
        fi
        until pg_isready -q -h /var/run/postgresql -U "${PGUSER:-reticora}"; do sleep 2; done
        pgbackrest --stanza="$STANZA" stanza-create
        pgbackrest --stanza="$STANZA" check
        [ -n "$(last_backup_stop)" ] || backup || true
        while :; do
            wait="$(seconds_until_hour)"
            log "next backup in ${wait}s"
            sleep "$wait"
            # A failed backup is retried after an hour; the health check
            # turns unhealthy once the newest backup is too old.
            until backup; do sleep 3600; done
        done
        ;;
    *)
        echo "usage: reticora-backup.sh run|health|verify-repo" >&2
        exit 2
        ;;
esac
