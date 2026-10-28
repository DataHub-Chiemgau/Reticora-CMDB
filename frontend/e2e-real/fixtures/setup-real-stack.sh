#!/usr/bin/env bash
#
# setup-real-stack.sh — prepares an installed stack for the real-stack suite
# frontend/e2e-real/ (WP-338). Runs after frontend/e2e/fixtures/
# setup-onboarding.sh (self-signup, mail catcher, collector), which stays
# unchanged as a frozen ABN-01 artefact:
#   1. writes a test variant of frontend/nginx.conf that passes a client
#      X-Real-IP header through (fallback $remote_addr); the specs give each
#      tenant its own address for the signup budget (E-33),
#   2. applies docker-compose.e2e-real.yml (that nginx.conf, raised rate
#      limits) on top of the onboarding overlay and waits for the stack,
#   3. exports the variables of frontend/e2e-real/README.md that the
#      onboarding setup does not set: E2E_DATABASE_URL (PostgreSQL of the
#      installation on 127.0.0.1:5432) and the Keycloak administrator,
#   4. checks that the host resolves "minio": presigned export URLs name the
#      endpoint the server uses (minio:9000), which the stack publishes on
#      127.0.0.1:9000. On GitHub Actions the script adds the /etc/hosts entry,
#   5. checks that an unprivileged process may bind UDP 161: the SNMP fake of
#      onboarding-bundle.spec.ts (collector/cmd/fakesnmp, WP-384) listens on
#      127.0.0.161:161, the port the collector's SNMP plugin queries. On
#      GitHub Actions the script sets net.ipv4.ip_unprivileged_port_start=0.
#
# WP-646 (F-375): an installation with RETICORA_TLS_DOMAIN in the env file
# (frontend/e2e-real/fixtures/setup-tls-origin.sh, the WebKit projects) is
# addressed over its TLS origin https://<domain> as setup-onboarding.sh does.
# Its server block on port 443 comes from the TLS template of the
# installation (RETICORA_NGINX_TLS_TEMPLATE), not from nginx.conf: the
# script writes a test variant of that template with the same X-Real-IP
# passthrough and applies docker-compose.e2e-real-tls.yml as well. The
# verification and invitation links of the onboarding overlay keep the
# origin setup-onboarding.sh gave the server (E2E_BASE_URL).
#
# Usage: frontend/e2e-real/fixtures/setup-real-stack.sh <compose-dir> [env-file]
# Writes the variables to $GITHUB_ENV when set, else prints export lines.
set -euo pipefail

compose_dir="$(cd "${1:?usage: setup-real-stack.sh <compose-dir> [env-file]}" && pwd)"
env_file="${2:-.env}"
here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"

env_value() { sed -n "s/^$1=//p" "$compose_dir/$env_file" | tail -n 1 | tr -d "\"'"; }

files=(-f docker-compose.yml)
[ -f "$compose_dir/docker-compose.override.yml" ] && files+=(-f docker-compose.override.yml)
files+=(-f "$repo/frontend/e2e/fixtures/docker-compose.e2e.yml" -f "$here/docker-compose.e2e-real.yml")

# The onboarding overlay needs its signup secrets again when the server is
# recreated; they live only in the running server container.
server_id=$(cd "$compose_dir" && docker compose --env-file "$env_file" ps -q server)
container_env() { docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$server_id" | sed -n "s/^$1=//p"; }
E2E_CAPTCHA_KEY="$(container_env RETICORA_SIGNUP_CAPTCHA_KEY)"
E2E_KC_CLIENT_ID="$(container_env RETICORA_KEYCLOAK_ADMIN_CLIENT_ID)"
E2E_KC_CLIENT_SECRET="$(container_env RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET)"
[ -n "$E2E_CAPTCHA_KEY" ] && [ -n "$E2E_KC_CLIENT_SECRET" ] || {
    echo "FAIL: run frontend/e2e/fixtures/setup-onboarding.sh first (no signup configuration on the server)"
    exit 1
}
export E2E_CAPTCHA_KEY E2E_KC_CLIENT_ID E2E_KC_CLIENT_SECRET
verify_url="$(container_env RETICORA_SIGNUP_VERIFY_URL)"
E2E_BASE_URL="${verify_url%/onboarding}"
if [ -z "$E2E_BASE_URL" ] || [ "$E2E_BASE_URL" = "$verify_url" ]; then
    echo "FAIL: the server has no verification URL <origin>/onboarding of setup-onboarding.sh (RETICORA_SIGNUP_VERIFY_URL '$verify_url')"
    exit 1
fi
export E2E_BASE_URL

tls_domain="$(env_value RETICORA_TLS_DOMAIN)"
frontend_port="$(env_value RETICORA_FRONTEND_PORT)"; frontend_port="${frontend_port:-3000}"
if [ -n "$tls_domain" ]; then
    https_port="$(env_value RETICORA_HTTPS_PORT)"
    base_url="https://${tls_domain}"
    [ -z "$https_port" ] || [ "$https_port" = 443 ] || base_url="${base_url}:${https_port}"
else
    base_url="http://localhost:${frontend_port}"
fi
base_curl=(curl -fsS)
[ -z "${E2E_CA_CERT:-}" ] || base_curl+=(--cacert "$E2E_CA_CERT")
[ "$E2E_BASE_URL" = "$base_url" ] || {
    echo "FAIL: the server links to $E2E_BASE_URL, the installation is served at $base_url (run setup-onboarding.sh for this installation)"
    exit 1
}

echo "== nginx.conf with X-Real-IP passthrough"
E2E_NGINX_CONF="${RUNNER_TEMP:-/tmp}/nginx-e2e-real.conf"
awk '
    /^http \{/ { print; print "    # Test variant (frontend/e2e-real/fixtures/setup-real-stack.sh): keep a client X-Real-IP."; print "    map $http_x_real_ip $e2e_client_ip { \"\" $remote_addr; default $http_x_real_ip; }"; next }
    { gsub(/proxy_set_header X-Real-IP \$remote_addr;/, "proxy_set_header X-Real-IP $e2e_client_ip;"); print }
' "$repo/frontend/nginx.conf" >"$E2E_NGINX_CONF"
grep -q 'X-Real-IP $e2e_client_ip' "$E2E_NGINX_CONF" || { echo "FAIL: no X-Real-IP line rewritten in frontend/nginx.conf"; exit 1; }
chmod 644 "$E2E_NGINX_CONF"
export E2E_NGINX_CONF

if [ -n "$tls_domain" ]; then
    echo "== TLS template with X-Real-IP passthrough ($base_url)"
    template="$(env_value RETICORA_NGINX_TLS_TEMPLATE)"; template="${template:-./nginx-tls.conf.template}"
    case "$template" in /*) ;; *) template="$compose_dir/${template#./}" ;; esac
    E2E_NGINX_TLS_TEMPLATE="${RUNNER_TEMP:-/tmp}/nginx-e2e-real-tls.conf.template"
    # shellcheck disable=SC2016 # nginx variables, not shell expansions
    sed 's/proxy_set_header X-Real-IP \$remote_addr;/proxy_set_header X-Real-IP $e2e_client_ip;/' "$template" >"$E2E_NGINX_TLS_TEMPLATE"
    # shellcheck disable=SC2016
    grep -q 'X-Real-IP $e2e_client_ip' "$E2E_NGINX_TLS_TEMPLATE" || { echo "FAIL: no X-Real-IP line rewritten in $template"; exit 1; }
    # shellcheck disable=SC2016
    if grep -q 'X-Real-IP $remote_addr' "$E2E_NGINX_TLS_TEMPLATE"; then
        echo "FAIL: $template keeps an X-Real-IP \$remote_addr line"; exit 1
    fi
    chmod 644 "$E2E_NGINX_TLS_TEMPLATE"
    export E2E_NGINX_TLS_TEMPLATE
    files+=(-f "$here/docker-compose.e2e-real-tls.yml")
fi

echo "== Real-stack overlay"
(cd "$compose_dir" && docker compose --env-file "$env_file" "${files[@]}" up -d server)
server_port="$(env_value RETICORA_SERVER_PORT)"; server_port="${server_port:-8080}"
for _ in $(seq 1 60); do
    curl -fsS "http://localhost:${server_port}/api/v1/signup/challenge" >/dev/null 2>&1 && break
    sleep 2
done
# The frontend resolves the server at its start: recreate it after the server.
(cd "$compose_dir" && docker compose --env-file "$env_file" "${files[@]}" up -d --force-recreate frontend)
for _ in $(seq 1 60); do
    "${base_curl[@]}" "${base_url}/api/v1/auth/config" >/dev/null 2>&1 && break
    sleep 2
done
"${base_curl[@]}" "${base_url}/api/v1/auth/config" >/dev/null || {
    echo "FAIL: the frontend at $base_url does not reach the server after the real-stack overlay"
    (cd "$compose_dir" && docker compose --env-file "$env_file" "${files[@]}" logs --tail 60 frontend server)
    exit 1
}
# The passthrough must reach the server: a signup challenge with two
# different X-Real-IP values must not share one budget (checked by the specs).

kc_port="$(env_value RETICORA_KEYCLOAK_PORT)"; kc_port="${kc_port:-8180}"
admin="$(env_value KEYCLOAK_ADMIN)"; admin="${admin:-admin}"
vars=(
    "E2E_DATABASE_URL=postgres://reticora:$(env_value RETICORA_DB_PASSWORD)@127.0.0.1:5432/reticora?sslmode=disable"
    "E2E_KEYCLOAK_URL=http://localhost:${kc_port}"
    "E2E_KEYCLOAK_ADMIN=$admin"
    "E2E_KEYCLOAK_ADMIN_PASSWORD=$(env_value KEYCLOAK_ADMIN_PASSWORD)"
)
if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "::add-mask::$(env_value RETICORA_DB_PASSWORD)"
    echo "::add-mask::$(env_value KEYCLOAK_ADMIN_PASSWORD)"
fi
psql "${vars[0]#E2E_DATABASE_URL=}" -X -At -c 'SELECT 1' >/dev/null || { echo "FAIL: psql cannot reach the database of the installation"; exit 1; }
for v in "${vars[@]}"; do
    if [ -n "${GITHUB_ENV:-}" ]; then echo "$v" >>"$GITHUB_ENV"; else echo "export $v"; fi
done

if ! getent hosts minio >/dev/null; then
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
        echo "127.0.0.1 minio" | sudo tee -a /etc/hosts >/dev/null
    else
        echo "FAIL: the host does not resolve minio; add '127.0.0.1 minio' to /etc/hosts (presigned export URLs)"
        exit 1
    fi
fi
curl -sS -o /dev/null "http://minio:9000/minio/health/live" || { echo "FAIL: http://minio:9000 is not reachable from the host"; exit 1; }

if [ "$(id -u)" -ne 0 ] && [ "$(sysctl -n net.ipv4.ip_unprivileged_port_start)" -gt 161 ]; then
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
        sudo sysctl -q -w net.ipv4.ip_unprivileged_port_start=0
    else
        echo "FAIL: UDP 161 needs net.ipv4.ip_unprivileged_port_start <= 161 (SNMP fake of onboarding-bundle.spec.ts): sudo sysctl -w net.ipv4.ip_unprivileged_port_start=0"
        exit 1
    fi
fi
echo "OK: real-stack fixtures ready"
