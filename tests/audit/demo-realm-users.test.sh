#!/usr/bin/env bash
#
# demo-realm-users.test.sh — Phase-1 acceptance audit, group
# T4-collector-load. Fixed with WP-433 (F-113, realm part); runs in the CI
# job installer-script-tests (.github/workflows/install-smoke.yml).
#
# WP-203 (SIM-01). Finding P2 "SIM-01 demo admin admin@demo.local cannot sign
# in; seeded demo org unreachable via UI": `make seed` creates the
# organizations demo (plan pro, admin admin@demo.local) and acme (admin
# admin@acme.local) with the deterministic ids md5('/org/<slug>')::uuid
# (backend/cmd/seed: ID("", "org", slug)) and their app_user rows, but login
# is OIDC-only and the dev realm deploy/keycloak/realm-reticora-dev.json (the
# realm docker-compose.yml imports) had no account for them; its only user
# admin@reticora.local belongs to 00000000-0000-0000-0000-000000000001.
#
# Requirement: the seeded demo organization is operable as admin@demo.local
# (SIM-01), and admin@acme.local reaches the acme organization for the
# isolation checks. The test asserts an enabled realm user per seeded admin
# with a verified e-mail, a password credential, the organization_id
# attribute of its seeded organization and, like the installer's
# administrator, the realm role org_admin (which requires the second factor,
# AUT-09) and the group reticora-admin. The passwords exist only in the
# development realm: the production realm has no users and no production path
# (realm-reticora.json, the installers, deploy/docker-compose, scripts) holds
# a demo password. The existing admin@reticora.local user is the control.
#
# Needs python3. Retest: ./tests/audit/demo-realm-users.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
ROOT="$(pwd)"

REALM="$ROOT/deploy/keycloak/realm-reticora-dev.json"
PROD_REALM="$ROOT/deploy/keycloak/realm-reticora.json"
SEED="$ROOT/backend/cmd/seed/seed.go"
if ! command -v python3 >/dev/null 2>&1 || [ ! -f "$REALM" ] || [ ! -f "$PROD_REALM" ] || [ ! -f "$SEED" ]; then
    echo "FAIL python3, $REALM, $PROD_REALM and $SEED are required"
    exit 1
fi
if ! grep -q 'admin: "admin@" + spec.domain' "$SEED" || ! grep -q '{"demo", "Demo", "demo.local"' "$SEED" || ! grep -q '{"acme", "acme", "acme.local"' "$SEED"; then
    echo "FAIL the seed no longer defines demo/acme with admin@<domain>; update this test"
    exit 1
fi

python3 -I - "$ROOT" "$REALM" "$PROD_REALM" <<'PY'
import hashlib, json, os, sys

root, realm_file, prod_file = sys.argv[1], sys.argv[2], sys.argv[3]
realm = json.load(open(realm_file))
prod = json.load(open(prod_file))
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

def passwords(u):
    return [c.get("value") for c in u.get("credentials", []) or [] if c.get("type") == "password" and c.get("value")]

control = find("admin@reticora.local")
if control and control.get("enabled", False) and control.get("credentials"):
    ok("control: admin@reticora.local is an enabled realm user with a credential")
else:
    fail("control: admin@reticora.local missing or without credential")

demo_passwords = {}
for slug, email in (("demo", "admin@demo.local"), ("acme", "admin@acme.local")):
    org = seed_id("", "org", slug)
    u = find(email)
    if u is None:
        fail(f"SIM-01 violated: no realm user {email}; the seeded organization {slug} ({org}) cannot be used through the UI")
        continue
    attrs = u.get("attributes", {}) or {}
    orgs = attrs.get("organization_id", [])
    orgs = orgs if isinstance(orgs, list) else [orgs]
    secret = passwords(u)
    roles = u.get("realmRoles", []) or []
    groups = u.get("groups", []) or []
    demo_passwords[email] = secret
    if (u.get("enabled", False) and u.get("emailVerified", False) and secret and orgs == [org]
            and "org_admin" in roles and "/reticora-admin" in groups):
        ok(f"{email} signs in to the seeded organization {slug} ({org}) as org_admin")
    else:
        fail(f"SIM-01 violated: realm user {email}: enabled={u.get('enabled')} emailVerified={u.get('emailVerified')} "
             f"password={bool(secret)} organization_id={orgs} realmRoles={roles} groups={groups}, "
             f"want {org}, org_admin and /reticora-admin")

# The passwords are development values: never in the production realm or on
# an installation path.
leaks = []
if prod.get("users"):
    leaks.append(f"realm-reticora.json has users {[u.get('username') for u in prod['users']]}")
paths = [prod_file] + [os.path.join(root, f) for f in sorted(os.listdir(root)) if f.startswith("install") and f.endswith(".sh")]
for base in ("deploy/docker-compose", "scripts"):
    for d, _, files in os.walk(os.path.join(root, base)):
        paths += [os.path.join(d, f) for f in files]
for email, secret in demo_passwords.items():
    for path in paths:
        try:
            text = open(path, encoding="utf-8", errors="ignore").read()
        except OSError:
            continue
        if any(s in text for s in secret):
            leaks.append(f"the password of {email} appears in {os.path.relpath(path, root)}")
if leaks:
    for leak in leaks:
        fail(f"AUT-09 violated: {leak}")
elif demo_passwords:
    ok(f"the passwords of {', '.join(sorted(demo_passwords))} exist only in the development realm")

print()
print(f"demo-realm-users: {passed} passed, {failed} failed")
sys.exit(1 if failed else 0)
PY
