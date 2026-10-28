#!/usr/bin/env bash
#
# simulate-ingest.test.sh — RETICORA_SIMULATE (WP-162: SIM-02). Checks that
# the simulated fleet is deterministic (collector/plugins/simulate), that the
# collector's discovery cycle uploads the 30 devices to the ingest endpoint,
# that the built binary allows RETICORA_SIMULATE only with
# RETICORA_ENVIRONMENT development or test and refuses it unset, prod,
# staging and production with exit 1 and collector.simulate.refused
# (WP-586, F-276), and that the real ingest handler turns the
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

# run_simulated <name> [RETICORA_ENVIRONMENT=<value>] — starts the built
# collector with RETICORA_SIMULATE=true and no other RETICORA_* variable
# (no ENVIRONMENT argument: the variable is unset) and stops it once it either
# exited or logged collector.simulate.enabled. Sets $code (exit code, or
# "running" when it was stopped) and the log $TMP/<name>.log.
run_simulated() {
    local name="$1" log="$TMP/$1.log" pid
    shift
    mkdir -p "$TMP/$name"
    env -i HOME="$TMP/$name" PATH="$PATH" \
        RETICORA_SIMULATE=true \
        RETICORA_CREDENTIALS_PATH="$TMP/$name/none.json" \
        RETICORA_SPOOL_DIR="$TMP/$name/spool" \
        RETICORA_AGENT_RELAY_ADDR=127.0.0.1:0 \
        "$@" "$TMP/collector" >"$log" 2>&1 &
    pid=$!
    code=running
    for _ in $(seq 1 200); do
        if ! kill -0 "$pid" 2>/dev/null; then
            wait "$pid"
            code=$?
            return
        fi
        grep -q 'collector.simulate.enabled' "$log" && break
        sleep 0.1
    done
    kill "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
}

# F-276: the simulation is fail-closed. Only an explicit development or test
# environment allows it; unset, prod, staging and production exit with 1 and
# collector.simulate.refused before any discovery.
if (cd "$ROOT/collector" && GOWORK=off go build -o "$TMP/collector" ./cmd/collector); then
    for env in unset "" prod staging production PRODUCTION; do
        name="refused-${env:-empty}"
        if [ "$env" = unset ]; then
            run_simulated "$name"
        else
            run_simulated "$name" RETICORA_ENVIRONMENT="$env"
        fi
        if [ "$code" = 1 ] && grep -q 'collector.simulate.refused' "$TMP/$name.log" &&
            grep -q 'only allowed with RETICORA_ENVIRONMENT=development or test' "$TMP/$name.log" &&
            ! grep -q 'collector.simulate.enabled' "$TMP/$name.log"; then
            ok "RETICORA_SIMULATE is refused with RETICORA_ENVIRONMENT ${env:-empty}"
        else
            cat "$TMP/$name.log"; fail "RETICORA_SIMULATE is refused with RETICORA_ENVIRONMENT ${env:-empty} (exit $code)"
        fi
    done
    for env in development test; do
        run_simulated "allowed-$env" RETICORA_ENVIRONMENT="$env"
        if [ "$code" = running ] && grep -q 'collector.simulate.enabled' "$TMP/allowed-$env.log" &&
            ! grep -q 'collector.simulate.refused' "$TMP/allowed-$env.log"; then
            ok "RETICORA_SIMULATE runs the simulated fleet with RETICORA_ENVIRONMENT=$env"
        else
            cat "$TMP/allowed-$env.log"; fail "RETICORA_SIMULATE runs the simulated fleet with RETICORA_ENVIRONMENT=$env (exit $code)"
        fi
    done
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
