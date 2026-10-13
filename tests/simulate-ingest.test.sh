#!/usr/bin/env bash
#
# simulate-ingest.test.sh — RETICORA_SIMULATE (WP-162: SIM-02). Checks that
# the simulated fleet is deterministic (collector/plugins/simulate), that the
# collector's discovery cycle uploads the 30 devices to the ingest endpoint
# and refuses RETICORA_SIMULATE in production (the built binary exits with
# collector.simulate.refused), and that the real ingest handler turns the
# fleet into 30 CIs with LLDP and power edges against PostgreSQL
# (backend/internal/discovery/simulate_integration_test.go).
#
# Needs Go and a migrated PostgreSQL in TEST_DATABASE_URL.
# Usage: TEST_DATABASE_URL=postgres://… ./tests/simulate-ingest.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
ROOT="$(pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

if [ -z "${TEST_DATABASE_URL:-}" ]; then
    echo "TEST_DATABASE_URL is required (migrated PostgreSQL)" >&2
    exit 2
fi

if (cd "$ROOT/collector" && GOWORK=off go test -count=1 ./plugins/simulate/ >"$TMP/sim.log" 2>&1); then
    ok "simulated fleet is deterministic"
else
    cat "$TMP/sim.log"; fail "simulated fleet is deterministic"
fi

if (cd "$ROOT/collector" && GOWORK=off go test -count=1 -run 'TestSimulation' ./collectorcmd/ >"$TMP/cmd.log" 2>&1); then
    ok "discovery cycle uploads the simulated fleet"
else
    cat "$TMP/cmd.log"; fail "discovery cycle uploads the simulated fleet"
fi

if (cd "$ROOT/collector" && GOWORK=off go build -o "$TMP/collector" ./cmd/collector); then
    RETICORA_SIMULATE=true RETICORA_ENVIRONMENT=production RETICORA_CREDENTIALS_PATH="$TMP/none.json" \
        timeout 20 "$TMP/collector" >"$TMP/prod.log" 2>&1
    code=$?
    if [ "$code" -eq 1 ] && grep -q 'collector.simulate.refused' "$TMP/prod.log"; then
        ok "RETICORA_SIMULATE is refused in production"
    else
        cat "$TMP/prod.log"; fail "RETICORA_SIMULATE is refused in production (exit $code)"
    fi
else
    fail "collector builds"
fi

if (cd "$ROOT/backend" && go test -v -count=1 -run 'TestSimulatedFleetIngest' ./internal/discovery/ >"$TMP/ingest.log" 2>&1) &&
    grep -q -- '--- PASS: TestSimulatedFleetIngest' "$TMP/ingest.log"; then
    ok "real ingest creates the 30 simulated CIs with edges"
else
    grep -v '^{' "$TMP/ingest.log"; fail "real ingest creates the 30 simulated CIs with edges"
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
