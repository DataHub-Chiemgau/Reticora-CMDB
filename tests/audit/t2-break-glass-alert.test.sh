#!/usr/bin/env bash
#
# t2-break-glass-alert.test.sh — Phase-1 acceptance audit, group
# T2-auth-authz, DEFECT test (fails until the finding is fixed; not wired
# into CI).
#
# WP-070 (SEC-07), finding P2 "Break-glass 'alarm' is only a log line and a
# counter; no alert rule is shipped". SEC-07: "RETICORA_OPERATOR_TOKEN nur
# fuer Bootstrap und Break-Glass, jede Nutzung alarmiert". The backend
# exports reticora_operator_break_glass_total (internal/operator/auth.go),
# but no alert rule of deploy/ fires on it, so nobody is notified.
#
# Checks:
#   1. some Prometheus rule file under deploy/ has an alert whose expression
#      uses reticora_operator_break_glass_total;
#   2. that alert fires on any increase (increase(...) > 0 or rate(...) > 0)
#      and has severity critical;
#   3. control: the counter is still exported by the backend.
#
# Retest: ./tests/audit/t2-break-glass-alert.test.sh
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

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
