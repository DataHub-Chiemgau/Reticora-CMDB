#!/usr/bin/env bash
#
# demo-realm-users.test.sh — Phase-1 acceptance audit, group
# T4-collector-load, DEFECT test (fails until the defect is fixed; not wired
# into CI).
#
# WP-203 (SIM-01). Finding P2 "SIM-01 demo admin admin@demo.local cannot sign
# in; seeded demo org unreachable via UI": `make seed` creates the
# organizations demo (plan pro, admin admin@demo.local) and acme (admin
# admin@acme.local) with the deterministic ids md5('/org/<slug>')::uuid
# (backend/cmd/seed: ID("", "org", slug)) and their app_user rows, but login
# is OIDC-only and the dev realm deploy/keycloak/realm-reticora-dev.json (the
# realm docker-compose.yml imports) has no account for them; its only user
# admin@reticora.local belongs to 00000000-0000-0000-0000-000000000001.
#
# Requirement: the seeded demo organization is operable as admin@demo.local
# (SIM-01), and admin@acme.local reaches the acme organization for the
# isolation checks. The test asserts an enabled realm user per seeded admin
# with a password credential and the organization_id attribute of its seeded
# organization. The existing admin@reticora.local user is the control.
#
# Needs python3. Retest: ./tests/audit/demo-realm-users.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
ROOT="$(pwd)"

REALM="$ROOT/deploy/keycloak/realm-reticora-dev.json"
SEED="$ROOT/backend/cmd/seed/seed.go"
if ! command -v python3 >/dev/null 2>&1 || [ ! -f "$REALM" ] || [ ! -f "$SEED" ]; then
    echo "FAIL python3, $REALM and $SEED are required"
    exit 1
fi
if ! grep -q 'admin: "admin@" + spec.domain' "$SEED" || ! grep -q '{"demo", "Demo", "demo.local"' "$SEED" || ! grep -q '{"acme", "acme", "acme.local"' "$SEED"; then
    echo "FAIL the seed no longer defines demo/acme with admin@<domain>; update this test"
    exit 1
fi

python3 -I - "$REALM" <<'PY'
import hashlib, json, sys

realm = json.load(open(sys.argv[1]))
users = realm.get("users", [])
passed = failed = 0

def ok(msg):
    global passed
    passed += 1
    print("ok   " + msg)

def fail(msg):
    global failed
    failed += 1
    print("FAIL " + msg)

def seed_id(*parts):
    h = hashlib.md5("/".join(parts).encode()).hexdigest()
    return f"{h[0:8]}-{h[8:12]}-{h[12:16]}-{h[16:20]}-{h[20:32]}"

def find(email):
    for u in users:
        if (u.get("email") or "").lower() == email or (u.get("username") or "").lower() == email:
            return u
    return None

control = find("admin@reticora.local")
if control and control.get("enabled", False) and control.get("credentials"):
    ok("control: admin@reticora.local is an enabled realm user with a credential")
else:
    fail("control: admin@reticora.local missing or without credential")

for slug, email in (("demo", "admin@demo.local"), ("acme", "admin@acme.local")):
    org = seed_id("", "org", slug)
    u = find(email)
    if u is None:
        fail(f"SIM-01 violated: no realm user {email}; the seeded organization {slug} ({org}) cannot be used through the UI")
        continue
    attrs = u.get("attributes", {}) or {}
    orgs = attrs.get("organization_id", [])
    orgs = orgs if isinstance(orgs, list) else [orgs]
    has_password = any(c.get("type") == "password" for c in u.get("credentials", []) or [])
    if u.get("enabled", False) and has_password and org in orgs:
        ok(f"{email} signs in to the seeded organization {slug} ({org})")
    else:
        fail(f"SIM-01 violated: realm user {email}: enabled={u.get('enabled')} password={has_password} organization_id={orgs}, want {org}")

print()
print(f"demo-realm-users: {passed} passed, {failed} failed")
sys.exit(1 if failed else 0)
PY
