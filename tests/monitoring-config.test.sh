#!/usr/bin/env bash
#
# monitoring-config.test.sh — the Alertmanager configuration and the alert
# rules of deploy/monitoring load and alarm as documented (WP-446, OPS-05).
# Runs in the ci.yml job monitoring-config.
#
#   - alertmanager_smtp: every e-mail receiver has an SMTP smarthost and a
#     sender (retest F-132, case moved from tests/audit/ops-config.test.sh);
#   - alertmanager_placeholders: the global SMTP settings are documented
#     placeholders (.invalid) and the password comes from a file; the file
#     holds no SMTP secret;
#   - alertmanager_amtool: amtool check-config accepts
#     deploy/monitoring/alertmanager.yaml (retest F-132, case moved from
#     tests/audit/ops-config.test.sh);
#   - promtool_check_rules: promtool check rules accepts the groups (the
#     extracted spec) of every PrometheusRule in deploy/monitoring, among them
#     worker-rules.yaml and slo-rules.yaml;
#   - promtool_test_rules: promtool test rules passes every unit test in
#     deploy/monitoring/tests, among them worker-rules.test.yaml (the five
#     worker alerts; ReticoraWorkerNoSuccess for a worker without a
#     last-success series, F-295);
#   - negative_*: fixtures that must be reported: the configuration without
#     global SMTP settings, PrometheusRules with a broken expression or
#     template, and the ReticoraWorkerNoSuccess rule before WP-446 against
#     worker-rules.test.yaml.
#
# Tools: python3 with PyYAML (as tests/rep01-structure.test.sh), amtool
# (AMTOOL=<path> or PATH; CI: v0.27.0) and promtool (PROMTOOL=<path> or PATH;
# CI: v2.54.1). A missing tool is a FAIL of its cases.
#
# Usage: [AMTOOL=<amtool>] [PROMTOOL=<promtool>] ./tests/monitoring-config.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | head -n 30 | sed 's/^/     /'; return 0; }

tool() { # tool <ENVVAR> <name>
    local v="${!1:-}"
    if [ -n "$v" ]; then printf '%s' "$v"; return 0; fi
    command -v "$2" 2>/dev/null
}

MONITORING=deploy/monitoring
ALERTMANAGER="$MONITORING/alertmanager.yaml"
AMTOOL_BIN="$(tool AMTOOL amtool || true)"
PROMTOOL_BIN="$(tool PROMTOOL promtool || true)"

# ─── Alertmanager ───────────────────────────────────────────────────────────
# smtp_problems <alertmanager.yaml>: e-mail receivers without a smarthost or
# sender, neither their own nor a global one.
smtp_problems() {
    python3 - "$1" <<'PY'
import sys, yaml
cfg = yaml.safe_load(open(sys.argv[1]))
g = cfg.get("global") or {}
problems = []
for r in cfg.get("receivers", []):
    for e in r.get("email_configs") or []:
        if not (e.get("smarthost") or g.get("smtp_smarthost")):
            problems.append(f"receiver {r['name']}: email_configs without smarthost and no global smtp_smarthost")
        if not (e.get("from") or g.get("smtp_from")):
            problems.append(f"receiver {r['name']}: email_configs without from and no global smtp_from")
print("\n".join(problems))
PY
}

# placeholder_problems <alertmanager.yaml>: SMTP values that are not
# placeholders of the reserved .invalid domain (RFC 2606), a missing password
# file, and SMTP secrets written into the file.
placeholder_problems() {
    python3 - "$1" <<'PY'
import sys, yaml
cfg = yaml.safe_load(open(sys.argv[1]))
g = cfg.get("global") or {}
problems = []
for key in ("smtp_smarthost", "smtp_from", "smtp_auth_username"):
    value = str(g.get(key) or "")
    host = value.rsplit("@", 1)[-1].rsplit(":", 1)[0]
    if not host.endswith(".invalid"):
        problems.append(f"global.{key}: {value!r} is no .invalid placeholder")
if not g.get("smtp_auth_password_file"):
    problems.append("global.smtp_auth_password_file: missing (the password comes from a file)")
secrets = [("global", g, ("smtp_auth_password", "smtp_auth_secret"))]
for r in cfg.get("receivers", []):
    for e in r.get("email_configs") or []:
        secrets.append((f"receiver {r['name']}", e, ("auth_password", "auth_secret")))
for where, section, keys in secrets:
    for key in keys:
        if key in section:
            problems.append(f"{where}: {key} written into the file")
print("\n".join(problems))
PY
}

problems="$(smtp_problems "$ALERTMANAGER" 2>&1)"
if [ -z "$problems" ]; then
    ok "alertmanager_smtp: every email receiver has an SMTP smarthost and sender"
else
    fail "alertmanager_smtp: every email receiver has an SMTP smarthost and sender" "$problems"
fi

problems="$(placeholder_problems "$ALERTMANAGER" 2>&1)"
if [ -z "$problems" ]; then
    ok "alertmanager_placeholders: the global SMTP settings are .invalid placeholders, the password comes from a file"
else
    fail "alertmanager_placeholders: the global SMTP settings are .invalid placeholders, the password comes from a file" "$problems"
fi

# The configuration before WP-446: no global SMTP settings.
python3 - "$ALERTMANAGER" "$TMP_ROOT/alertmanager-without-global.yaml" <<'PY'
import sys, yaml
cfg = yaml.safe_load(open(sys.argv[1]))
cfg.pop("global", None)
yaml.safe_dump(cfg, open(sys.argv[2], "w"), sort_keys=False)
PY
if [ -n "$(smtp_problems "$TMP_ROOT/alertmanager-without-global.yaml")" ]; then
    ok "negative_alertmanager_smtp: a configuration without global SMTP settings is reported"
else
    fail "negative_alertmanager_smtp: a configuration without global SMTP settings is reported" "no problem found"
fi

if [ -z "$AMTOOL_BIN" ]; then
    fail "alertmanager_amtool: amtool available (set AMTOOL)" "amtool not found"
else
    printf 'amtool: %s\n' "$("$AMTOOL_BIN" --version 2>&1 | head -n 1)"
    if "$AMTOOL_BIN" check-config "$ALERTMANAGER" >"$TMP_ROOT/amtool.log" 2>&1; then
        ok "alertmanager_amtool: amtool check-config accepts $ALERTMANAGER"
    else
        fail "alertmanager_amtool: amtool check-config accepts $ALERTMANAGER" "$(cat "$TMP_ROOT/amtool.log")"
    fi
    if "$AMTOOL_BIN" check-config "$TMP_ROOT/alertmanager-without-global.yaml" >"$TMP_ROOT/amtool-neg.log" 2>&1; then
        fail "negative_alertmanager_amtool: amtool check-config rejects the configuration without global SMTP settings" "exit 0"
    else
        ok "negative_alertmanager_amtool: amtool check-config rejects the configuration without global SMTP settings ($(grep -m1 -o 'FAILED: .*' "$TMP_ROOT/amtool-neg.log"))"
    fi
fi

# ─── Prometheus rules ───────────────────────────────────────────────────────
# extract_groups <PrometheusRule> <out>: promtool reads plain rule groups;
# write spec.groups of the PrometheusRule into <out>.
extract_groups() {
    python3 - "$1" "$2" <<'PY'
import sys, yaml
rule = yaml.safe_load(open(sys.argv[1]))
assert rule.get("kind") == "PrometheusRule", f"{sys.argv[1]}: kind {rule.get('kind')}"
yaml.safe_dump({"groups": rule["spec"]["groups"]}, open(sys.argv[2], "w"), sort_keys=False)
PY
}

# check_rules <dir> <PrometheusRule>...: extract each into
# <dir>/<name>.groups.yaml and run promtool check rules on them; the output
# is in <dir>/check.log.
check_rules() {
    local dir="$1" f groups=()
    shift
    mkdir -p "$dir"
    : >"$dir/check.log"
    for f in "$@"; do
        extract_groups "$f" "$dir/$(basename "$f" .yaml).groups.yaml" >>"$dir/check.log" 2>&1 || return 1
        groups+=("$dir/$(basename "$f" .yaml).groups.yaml")
    done
    "$PROMTOOL_BIN" check rules "${groups[@]}" >>"$dir/check.log" 2>&1
}

# test_rules <dir> <unit test>...: run promtool test rules in <dir>, which
# holds the extracted groups; the output is in <dir>/test.log.
test_rules() {
    local dir="$1" f names=()
    shift
    cp "$@" "$dir/" || return 1
    for f in "$@"; do names+=("$(basename "$f")"); done
    (cd "$dir" && "$PROMTOOL_BIN" test rules "${names[@]}") >"$dir/test.log" 2>&1
}

# Every PrometheusRule of deploy/monitoring and every unit test.
mapfile -t RULE_FILES < <(grep -lE '^kind: PrometheusRule' "$MONITORING"/*.yaml)
UNIT_TESTS=("$MONITORING"/tests/*.test.yaml)
missing=""
for want in "$MONITORING/worker-rules.yaml" "$MONITORING/slo-rules.yaml"; do
    printf '%s\n' "${RULE_FILES[@]}" | grep -qxF "$want" || missing="$missing $want"
done
if printf '%s\n' "${UNIT_TESTS[@]}" | grep -qxF "$MONITORING/tests/worker-rules.test.yaml"; then :; else
    missing="$missing $MONITORING/tests/worker-rules.test.yaml"
fi
if [ -z "$missing" ]; then
    ok "rule_files: PrometheusRules ${RULE_FILES[*]}; unit tests ${UNIT_TESTS[*]}"
else
    fail "rule_files: worker-rules.yaml, slo-rules.yaml and worker-rules.test.yaml are found" "missing:$missing"
fi

if [ -z "$PROMTOOL_BIN" ]; then
    fail "promtool_check_rules: promtool available (set PROMTOOL)" "promtool not found"
    fail "promtool_test_rules: promtool available (set PROMTOOL)" "promtool not found"
else
    printf 'promtool: %s\n' "$("$PROMTOOL_BIN" --version 2>&1 | head -n 1)"
    REPO_RULES="$TMP_ROOT/rules"
    if check_rules "$REPO_RULES" "${RULE_FILES[@]}"; then
        ok "promtool_check_rules: promtool check rules accepts the groups of ${RULE_FILES[*]}"
    else
        fail "promtool_check_rules: promtool check rules accepts the groups of ${RULE_FILES[*]}" "$(cat "$REPO_RULES/check.log")"
    fi
    if test_rules "$REPO_RULES" "${UNIT_TESTS[@]}"; then
        ok "promtool_test_rules: promtool test rules passes ${UNIT_TESTS[*]}"
    else
        fail "promtool_test_rules: promtool test rules passes ${UNIT_TESTS[*]}" "$(cat "$REPO_RULES/test.log")"
    fi

    # Negative fixtures: a broken expression and a broken template.
    mkdir -p "$TMP_ROOT/fixtures"
    cat >"$TMP_ROOT/fixtures/broken-expr.yaml" <<'YAML'
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: broken-expr
spec:
  groups:
    - name: broken
      rules:
        - alert: BrokenExpression
          expr: max by (worker) (reticora_worker_up == 1
          for: 30m
YAML
    cat >"$TMP_ROOT/fixtures/broken-template.yaml" <<'YAML'
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: broken-template
spec:
  groups:
    - name: broken
      rules:
        - alert: BrokenTemplate
          expr: max by (worker) (reticora_worker_up) == 0
          annotations:
            summary: "Worker {{ $labels.worker } stopped"
YAML
    # <fixture>:<reason promtool must give>
    for neg in 'broken-expr:could not parse expression' 'broken-template:annotation "summary": template'; do
        fixture="${neg%%:*}" reason="${neg#*:}"
        log="$TMP_ROOT/neg-$fixture/check.log"
        if check_rules "$TMP_ROOT/neg-$fixture" "$TMP_ROOT/fixtures/$fixture.yaml"; then
            fail "negative_check_rules: promtool check rules rejects the fixture $fixture" "exit 0; $(cat "$log")"
        elif grep -qF "$reason" "$log"; then
            ok "negative_check_rules: promtool check rules rejects the fixture $fixture ($reason)"
        else
            fail "negative_check_rules: promtool check rules rejects the fixture $fixture ($reason)" "$(cat "$log")"
        fi
    done

    # The ReticoraWorkerNoSuccess rule before WP-446 cannot fire for a worker
    # without a last-success series (F-295): worker-rules.test.yaml fails.
    OLD_RULES="$TMP_ROOT/old-nosuccess"
    mkdir -p "$OLD_RULES"
    if python3 - "$MONITORING/worker-rules.yaml" "$OLD_RULES/worker-rules.groups.yaml" <<'PY'
import sys, yaml
rule = yaml.safe_load(open(sys.argv[1]))
groups = rule["spec"]["groups"]
hits = [r for g in groups for r in g["rules"] if r.get("alert") == "ReticoraWorkerNoSuccess"]
assert len(hits) == 1, f"{len(hits)} ReticoraWorkerNoSuccess rules"
hits[0]["expr"] = ("(time() - max by (worker) (reticora_worker_last_success_timestamp_seconds) > 1800)\n"
                   "  and on (worker) max by (worker) (reticora_worker_up) == 1\n")
hits[0]["for"] = "5m"
yaml.safe_dump({"groups": groups}, open(sys.argv[2], "w"), sort_keys=False)
PY
    then
        if test_rules "$OLD_RULES" "$MONITORING/tests/worker-rules.test.yaml"; then
            fail "negative_nosuccess_before_fix: worker-rules.test.yaml fails with the ReticoraWorkerNoSuccess rule before WP-446" "promtool test rules passed"
        elif grep -q 'alertname: ReticoraWorkerNoSuccess' "$OLD_RULES/test.log" && ! grep -qE 'alertname: ReticoraWorker(Stopped|Errors|Backlog|BacklogGrowing),' "$OLD_RULES/test.log"; then
            ok "negative_nosuccess_before_fix: worker-rules.test.yaml fails with the ReticoraWorkerNoSuccess rule before WP-446, only for that alert"
        else
            fail "negative_nosuccess_before_fix: worker-rules.test.yaml fails with the ReticoraWorkerNoSuccess rule before WP-446, only for that alert" "$(cat "$OLD_RULES/test.log")"
        fi
    else
        fail "negative_nosuccess_before_fix: fixture with the ReticoraWorkerNoSuccess rule before WP-446" "worker-rules.yaml has no single ReticoraWorkerNoSuccess rule"
    fi
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
