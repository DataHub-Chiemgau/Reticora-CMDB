#!/bin/sh
# Runs after 20-envsubst-on-templates.sh of the nginx image. That script
# renders every mounted template, including the TLS server block, even when
# no domain is configured; an empty RETICORA_TLS_DOMAIN would leave
# "server_name ;" behind and nginx would refuse to start. Without a domain
# the plain-HTTP server in nginx.conf serves the UI, so the rendered TLS
# configuration is removed.
set -eu
if [ -z "${RETICORA_TLS_DOMAIN:-}" ]; then
    rm -f /etc/nginx/conf.d/reticora-tls.conf
fi
