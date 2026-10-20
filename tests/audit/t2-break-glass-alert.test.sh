#!/usr/bin/env bash
#
# t2-break-glass-alert.test.sh — Phase-1 acceptance audit, group
# T2-auth-authz. Fixed with WP-362 (F-068); runs in the CI job gate.
#
# WP-070 (SEC-07), finding P2 "Break-glass 'alarm' is only a log line and a
# counter; no alert rule is shipped". SEC-07: "RETICORA_OPERATOR_TOKEN nur
# fuer Bootstrap und Break-Glass, jede Nutzung alarmiert". The backend
# exports reticora_operator_break_glass_total (internal/operator/auth.go);
# deploy/monitoring/operator-rules.yaml alarms on it.
#
# Checks:
#   1. some Prometheus rule file under deploy/ has an alert whose expression
#      uses reticora_operator_break_glass_total;
#   2. that alert fires on any increase (increase(...) > 0 or rate(...) > 0)
#      and has severity critical;
#   3. control: the counter is still exported by the backend;
#   4. with promtool (PROMTOOL=<path> or PATH; the CI job gate sets it and
#      requires it with REQUIRE_PROMTOOL=1): promtool check rules and
#      promtool test rules deploy/monitoring/tests/operator-rules.test.yaml
#      on the groups extracted from the PrometheusRule.
#
# Retest: [PROMTOOL=<promtool>] ./tests/audit/t2-break-glass-alert.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

if grep -q 'operator_break_glass_total' backend/internal/operator/auth.go; then
    ok "control: backend exports reticora_operator_break_glass_total"
else
    fail "control: backend no longer exports the break-glass counter"
fi

rules=$(grep -rl --include='*.yaml' --include='*.yml' -E '^\s*(groups:|kind: PrometheusRule)' deploy/ 2>/dev/null)
hits=""
for f in $rules; do
    if grep -q 'reticora_operator_break_glass_total' "$f"; then
        hits="$hits $f"
    fi
done
if [ -n "$hits" ]; then
    ok "alert rule on reticora_operator_break_glass_total shipped in:$hits"
else
    fail "no Prometheus rule under deploy/ uses reticora_operator_break_glass_total (rule files: $(echo $rules | tr '\n' ' ')); SEC-07: every break-glass use alarms"
fi

fires=0
for f in $hits; do
    # The rule block around the expression: any increase alarms, critical.
    block=$(grep -B6 -A8 'reticora_operator_break_glass_total' "$f")
    if echo "$block" | grep -Eq '(increase|rate|delta)\(reticora_operator_break_glass_total[^)]*\)\s*>\s*0' &&
       echo "$block" | grep -Eq 'severity:\s*"?critical'; then
        fires=1
    fi
done
if [ "$fires" -eq 1 ]; then
    ok "the break-glass alert fires on any increase with severity critical"
else
    fail "no break-glass alert that fires on any increase (increase(...) > 0) with severity critical"
fi

PROMTOOL_BIN="${PROMTOOL:-$(command -v promtool 2>/dev/null || true)}"
if [ -n "$PROMTOOL_BIN" ]; then
    TMP_ROOT="$(mktemp -d)"
    trap 'rm -rf "$TMP_ROOT"' EXIT
    log="$TMP_ROOT/promtool.log"
    # promtool reads plain rule groups: extract spec.groups of the PrometheusRule.
    if python3 -c '
import sys, yaml
rule = yaml.safe_load(open(sys.argv[1]))
assert rule["kind"] == "PrometheusRule", rule["kind"]
yaml.safe_dump({"groups": rule["spec"]["groups"]}, open(sys.argv[2], "w"), sort_keys=False)
' deploy/monitoring/operator-rules.yaml "$TMP_ROOT/operator-rules.groups.yaml" >"$log" 2>&1 &&
        cp deploy/monitoring/tests/operator-rules.test.yaml "$TMP_ROOT/" &&
        "$PROMTOOL_BIN" check rules "$TMP_ROOT/operator-rules.groups.yaml" >>"$log" 2>&1 &&
        (cd "$TMP_ROOT" && "$PROMTOOL_BIN" test rules operator-rules.test.yaml) >>"$log" 2>&1; then
        ok "promtool: check rules and test rules pass for deploy/monitoring/operator-rules.yaml"
    else
        fail "promtool: check rules and test rules pass for deploy/monitoring/operator-rules.yaml"
        sed 's/^/     /' "$log" | head -n 40
    fi
elif [ "${REQUIRE_PROMTOOL:-0}" = 1 ]; then
    fail "promtool: not found (set PROMTOOL)"
else
    printf 'skip promtool: not found (set PROMTOOL)\n'
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
