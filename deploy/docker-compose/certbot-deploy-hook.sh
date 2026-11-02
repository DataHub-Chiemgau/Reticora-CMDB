#!/bin/sh
#
# certbot-deploy-hook.sh — certbot --deploy-hook of the compose stack
# (WP-419, F-034). certbot runs it as root in the certbot container after it
# has stored an issued or renewed certificate: install-cloud.sh passes it to
# `certbot certonly`, the certbot service to `certbot renew`
# (docker-compose.yml mounts this file at
# /usr/local/bin/reticora-certbot-deploy-hook.sh). certbot sets
# RENEWED_LINEAGE to the live directory of the lineage
# (/etc/letsencrypt/live/<name>).
#
# certbot keeps letsencrypt/archive/ and letsencrypt/live/ at 0700 and the
# private key at 0600, all owned by root. nginx in the frontend container runs
# as uid/gid 101 and reads /etc/letsencrypt/live/<domain>/{fullchain,privkey}.pem
# through the read-only bind mount, so it could load neither. Afterwards:
#   - the private key belongs to group 101 with mode 0640: readable by root
#     and gid 101, never world-readable;
#   - the directories on the path are 0755 and the certificates 0644: the
#     chain is public, and the server container (nobody) also reads
#     live/<domain>/fullchain.pem as RETICORA_OIDC_CA_CERT_FILE.
# ./letsencrypt is a bind mount, so group 101 applies on the host as well:
# members of the host group with gid 101 can read the key there
# (docs/troubleshooting-installation.md, "Issued certificate not loadable by
# nginx").
set -eu

NGINX_GID=101

lineage="${RENEWED_LINEAGE:?RENEWED_LINEAGE is not set (run by certbot --deploy-hook)}"
name="${lineage##*/}"
config_dir="${lineage%/live/*}"
[ "$config_dir/live/$name" = "$lineage" ] || {
    echo "certbot-deploy-hook: unexpected lineage path $lineage" >&2
    exit 1
}
archive="$(readlink -f "$config_dir/archive/$name")"

# Only files of this lineage's archive directory are changed.
target() {
    t="$(readlink -f "$lineage/$1.pem")"
    case "$t" in
        "$archive"/"$1"[0-9]*.pem) printf '%s' "$t" ;;
        *) echo "certbot-deploy-hook: $lineage/$1.pem does not point into $archive" >&2; exit 1 ;;
    esac
}

key="$(target privkey)"
chgrp "$NGINX_GID" "$key"
chmod 0640 "$key"
for kind in cert chain fullchain; do
    file="$(target "$kind")"
    chmod 0644 "$file"
done
chmod 0755 "$config_dir/archive" "$config_dir/live" "$archive" "$lineage"
