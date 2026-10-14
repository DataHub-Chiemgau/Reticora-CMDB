#!/usr/bin/env bash
#
# setup-onboarding.sh — prepares a freshly installed stack for the onboarding
# E2E suite (WP-200):
#   1. creates the confidential Keycloak client whose service account creates
#      the accounts of self-signups (realm-management: manage-users,
#      view-users, query-users),
#   2. applies docker-compose.e2e.yml (mail catcher, signup configuration)
#      and waits until the server answers again,
#   3. builds the collector the suite enrolls (simulated fleet, SIM-02).
#
# Usage: frontend/e2e/fixtures/setup-onboarding.sh <compose-dir> [env-file]
# Writes the variables the suite reads to $GITHUB_ENV when set, else prints
# them as export lines.
set -euo pipefail

compose_dir="$(cd "${1:?usage: setup-onboarding.sh <compose-dir> [env-file]}" && pwd)"
env_file="${2:-.env}"
here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"

env_value() { sed -n "s/^$1=//p" "$compose_dir/$env_file" | tail -n 1 | tr -d "\"'"; }

kc_port="$(env_value RETICORA_KEYCLOAK_PORT)"; kc_port="${kc_port:-8180}"
kc="http://localhost:${kc_port}"
admin="$(env_value KEYCLOAK_ADMIN)"; admin="${admin:-admin}"
admin_password="$(env_value KEYCLOAK_ADMIN_PASSWORD)"
realm=reticora
client_id=reticora-e2e-signup
client_secret="$(openssl rand -hex 24)"
captcha_key="$(openssl rand -hex 32)"

token=$(curl -fsS "$kc/realms/master/protocol/openid-connect/token" \
    -d grant_type=password -d client_id=admin-cli \
    --data-urlencode "username=$admin" --data-urlencode "password=$admin_password" | jq -r .access_token)
api() { curl -fsS -H "Authorization: Bearer $token" -H 'Content-Type: application/json' "$@"; }

echo "== Keycloak client $client_id"
api -X POST "$kc/admin/realms/$realm/clients" -d "$(jq -n --arg id "$client_id" --arg secret "$client_secret" '{
    clientId: $id, enabled: true, protocol: "openid-connect", publicClient: false,
    clientAuthenticatorType: "client-secret", secret: $secret,
    serviceAccountsEnabled: true, standardFlowEnabled: false, directAccessGrantsEnabled: false}')"
client_uuid=$(api "$kc/admin/realms/$realm/clients?clientId=$client_id" | jq -r '.[0].id')
service_user=$(api "$kc/admin/realms/$realm/clients/$client_uuid/service-account-user" | jq -r .id)
rm_uuid=$(api "$kc/admin/realms/$realm/clients?clientId=realm-management" | jq -r '.[0].id')
roles=$(api "$kc/admin/realms/$realm/clients/$rm_uuid/roles" |
    jq '[.[] | select(.name == "manage-users" or .name == "view-users" or .name == "query-users")]')
api -X POST "$kc/admin/realms/$realm/users/$service_user/role-mappings/clients/$rm_uuid" -d "$roles"

echo "== Signup configuration and mail catcher"
export E2E_CAPTCHA_KEY="$captcha_key" E2E_KC_CLIENT_ID="$client_id" E2E_KC_CLIENT_SECRET="$client_secret"
files=(-f docker-compose.yml)
[ -f "$compose_dir/docker-compose.override.yml" ] && files+=(-f docker-compose.override.yml)
files+=(-f "$here/docker-compose.e2e.yml")
(cd "$compose_dir" && docker compose --env-file "$env_file" "${files[@]}" up -d mailpit server)

server_port="$(env_value RETICORA_SERVER_PORT)"; server_port="${server_port:-8080}"
for _ in $(seq 1 60); do
    if curl -fsS "http://localhost:${server_port}/healthz" >/dev/null 2>&1 &&
        curl -fsS "http://localhost:${server_port}/api/v1/signup/challenge" >/dev/null 2>&1; then
        break
    fi
    sleep 2
done
curl -fsS "http://localhost:${server_port}/api/v1/signup/challenge" >/dev/null || {
    echo "FAIL: self-signup is not available after applying the overlay"
    (cd "$compose_dir" && docker compose --env-file "$env_file" "${files[@]}" logs --tail 80 server)
    exit 1
}

echo "== Collector (simulated fleet)"
collector_bin="${RUNNER_TEMP:-/tmp}/reticora-collector"
(cd "$repo/collector" && GOWORK=off CGO_ENABLED=0 go build -o "$collector_bin" ./cmd/collector)

frontend_port="$(env_value RETICORA_FRONTEND_PORT)"; frontend_port="${frontend_port:-3000}"
host_ip="$(hostname -I | awk '{print $1}')"
vars=(
    "BASE_URL=http://localhost:${frontend_port}"
    "E2E_SERVER_URL=http://localhost:${server_port}"
    "E2E_MAILPIT_URL=http://localhost:8025"
    "E2E_INSECURE_URL=http://${host_ip}:${frontend_port}"
    "E2E_ISSUER_URL=$(env_value RETICORA_OIDC_ISSUER_URL)"
    "E2E_COLLECTOR_BIN=$collector_bin"
)
for v in "${vars[@]}"; do
    if [ -n "${GITHUB_ENV:-}" ]; then echo "$v" >>"$GITHUB_ENV"; else echo "export $v"; fi
done
echo "OK: onboarding fixtures ready"
