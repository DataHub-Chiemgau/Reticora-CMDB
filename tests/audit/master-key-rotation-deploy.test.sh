#!/usr/bin/env bash
#
# master-key-rotation-deploy.test.sh — the shipped deployments carry the
# previous master key, so the documented master key rotation (SEC-06,
# docs/ops/workers.md "Key rotation") can be done on Compose and Kubernetes.
# Runs in the CI job k8s-manifests (.github/workflows/ci.yml).
#
# docs/ops/workers.md: "set the new key as RETICORA_MASTER_KEY and the old
# one in RETICORA_MASTER_KEY_PREVIOUS (comma-separated for several), restart,
# and let rotation.master_key run". The server reads
# RETICORA_MASTER_KEY_PREVIOUS (backend/internal/config/config.go); with the
# new key alone it cannot open the derivation root and the data keys sealed
# with the old key and does not start ("failed to open the derivation root of
# the derived secrets").
#
# Phase-1 re-audit, found by WP-547 (F-370, fixed by WP-642):
# deploy/docker-compose/docker-compose.yml passed only RETICORA_MASTER_KEY to
# the server container and deploy/k8s/base/deployment.yaml only the secret
# key master-key. Compose now passes RETICORA_MASTER_KEY_PREVIOUS from .env
# (empty when unset), the base reads it from reticora-secrets, key
# master-key-previous, with optional: true. The real-stack case K01 of
# frontend/e2e-real/key-rotation.spec.ts runs the rotation on the installed
# stack.
#
# Cases (fix-agnostic: the value must reach the server's environment):
#   compose_master_key_previous   RETICORA_MASTER_KEY_PREVIOUS of the .env
#                                 reaches the environment of the service
#                                 server (docker compose config)
#   compose_without_previous      control: without the variable the stack
#                                 still renders (it is optional)
#   k8s_master_key_previous       the container reticora-server of the
#                                 rendered base and of the overlays staging
#                                 and prod (kustomize build) takes
#                                 RETICORA_MASTER_KEY_PREVIOUS from
#                                 reticora-secrets/master-key-previous, a
#                                 reference marked optional, so the pod starts
#                                 without it after the old key is retired
#
# Needs docker compose (only `docker compose config`, no daemon), jq,
# kustomize (KUSTOMIZE or on PATH) and python3 with PyYAML.
# Retest: ./tests/audit/master-key-rotation-deploy.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
ROOT="$(pwd)"
COMPOSE="$ROOT/deploy/docker-compose/docker-compose.yml"
KUSTOMIZE="${KUSTOMIZE:-kustomize}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/     /' | head -n 8; return 0; }

# clean <command…> — the command without the RETICORA_* and KEYCLOAK_*
# variables of the caller's environment (they override .env values).
clean() {
    local unset=() v
    for v in $(compgen -e); do
        case "$v" in RETICORA_*|KEYCLOAK_*) unset+=(-u "$v") ;; esac
    done
    env "${unset[@]}" "$@"
}

OLD_KEY="b2xkLW1hc3Rlci1rZXktb2YtdGhlLXJvdGF0aW9uLXRlc3Q="
NEW_KEY="bmV3LW1hc3Rlci1rZXktb2YtdGhlLXJvdGF0aW9uLXRlc3Q="

# server_env <case> <extra .env lines…> — the environment of the service
# server as docker compose renders it with a minimal .env.
server_env() {
    local dir="$TMP/$1"
    shift
    mkdir -p "$dir"
    printf '%s\n' \
        "RETICORA_DB_PASSWORD=db-password" "RETICORA_DB_APP_PASSWORD=app-password" \
        "KEYCLOAK_ADMIN_PASSWORD=kc-admin" "KEYCLOAK_DB_PASSWORD=kc-db" \
        "RETICORA_S3_ACCESS_KEY=s3-access" "RETICORA_S3_SECRET_KEY=s3-secret-key" \
        "RETICORA_BACKUP_CIPHER_PASS=backup-pass" "RETICORA_MASTER_KEY=$NEW_KEY" "$@" > "$dir/.env"
    if ! clean docker compose --env-file "$dir/.env" -f "$COMPOSE" config --format json > "$dir/config.json" 2> "$dir/config.err"; then
        return 1
    fi
    jq -c '.services.server.environment // {}' "$dir/config.json" > "$dir/server-env.json"
}

if ! docker compose version >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then
    fail "compose_master_key_previous: docker compose and jq are required"
    fail "compose_without_previous: docker compose and jq are required"
else
    if server_env with-previous "RETICORA_MASTER_KEY_PREVIOUS=$OLD_KEY"; then
        got="$(jq -r '.RETICORA_MASTER_KEY_PREVIOUS // "<not set>"' "$TMP/with-previous/server-env.json")"
        cur="$(jq -r '.RETICORA_MASTER_KEY // "<not set>"' "$TMP/with-previous/server-env.json")"
        if [ "$got" = "$OLD_KEY" ] && [ "$cur" = "$NEW_KEY" ]; then
            ok "compose_master_key_previous: the server gets RETICORA_MASTER_KEY and RETICORA_MASTER_KEY_PREVIOUS from .env"
        else
            fail "compose_master_key_previous: RETICORA_MASTER_KEY_PREVIOUS of .env does not reach the server (got $got, RETICORA_MASTER_KEY $cur); the rotation of docs/ops/workers.md is impossible on Compose" \
                "$(jq -r 'keys[] | select(startswith("RETICORA_MASTER"))' "$TMP/with-previous/server-env.json")"
        fi
    else
        fail "compose_master_key_previous: docker compose config failed" "$(grep -v level=warning "$TMP/with-previous/config.err")"
    fi
    if server_env without-previous; then
        got="$(jq -r '.RETICORA_MASTER_KEY_PREVIOUS // ""' "$TMP/without-previous/server-env.json")"
        if [ -z "$got" ]; then
            ok "compose_without_previous: the stack renders without RETICORA_MASTER_KEY_PREVIOUS"
        else
            fail "compose_without_previous: RETICORA_MASTER_KEY_PREVIOUS is '$got' although .env does not set it"
        fi
    else
        fail "compose_without_previous: docker compose config failed without RETICORA_MASTER_KEY_PREVIOUS" "$(grep -v level=warning "$TMP/without-previous/config.err")"
    fi
fi

# The base and the overlays that inherit it (staging, prod).
if ! command -v "$KUSTOMIZE" >/dev/null 2>&1 || ! python3 -I -c 'import yaml' 2>/dev/null; then
    fail "k8s_master_key_previous: kustomize (set KUSTOMIZE) and python3 with PyYAML are required"
else
    for build in base overlays/staging overlays/prod; do
        out="$TMP/k8s-${build//\//-}.yaml"
        if ! "$KUSTOMIZE" build "$ROOT/deploy/k8s/$build" > "$out" 2> "$TMP/kustomize.err"; then
            fail "k8s_master_key_previous: kustomize build deploy/k8s/$build failed" "$(cat "$TMP/kustomize.err")"
            continue
        fi
        verdict="$(python3 -I - "$out" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
for d in docs:
    if d.get("kind") != "Deployment" or d["metadata"]["name"] != "reticora-server":
        continue
    for c in d["spec"]["template"]["spec"]["containers"]:
        env = {e["name"]: e for e in c.get("env") or []}
        prev = env.get("RETICORA_MASTER_KEY_PREVIOUS")
        if prev is None:
            print(f"missing: container {c['name']} has no RETICORA_MASTER_KEY_PREVIOUS (env: "
                  + ", ".join(n for n in env if n.startswith("RETICORA_MASTER")) + ")")
            sys.exit()
        ref = (prev.get("valueFrom") or {}).get("secretKeyRef")
        if not ref:
            print("not a secret: RETICORA_MASTER_KEY_PREVIOUS is not read from a secret key")
        elif ref.get("optional") is not True:
            print(f"mandatory: secretKeyRef {ref} is not optional, the pod does not start after the old key is retired")
        elif (ref.get("name"), ref.get("key")) != ("reticora-secrets", "master-key-previous"):
            print(f"undocumented: secretKeyRef {ref} is not reticora-secrets/master-key-previous (deploy/README.md)")
        else:
            print("ok")
        sys.exit()
print("missing: no Deployment reticora-server in the rendered build")
PY
)"
        if [ "$verdict" = "ok" ]; then
            ok "k8s_master_key_previous: reticora-server of deploy/k8s/$build reads RETICORA_MASTER_KEY_PREVIOUS from the optional secret key reticora-secrets/master-key-previous"
        else
            fail "k8s_master_key_previous: deploy/k8s/$build: $verdict; the rotation of docs/ops/workers.md is impossible on Kubernetes"
        fi
    done
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
