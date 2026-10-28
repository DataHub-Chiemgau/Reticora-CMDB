#!/usr/bin/env bash
#
# ci-db-summary.test.sh — the skip rule of the ci.yml job migrations
# (WP-499, retest F-283, TST-04). Runs in the ci.yml job gate.
#
#   - skip_*: scripts/ci/db-test-summary.sh on synthetic test2json output:
#     a NATS skip, a skipped test and a skipped subtest of
#     internal/discovery/natsedge_integration_test.go, "TEST_DATABASE_URL not
#     set" (also in an allow-listed test) and a Redis skip fail (exit != 0);
#     no skip and an allow-listed skip (test and subtest) pass (exit 0); no
#     output and a run without tests fail; a usage error exits 2;
#   - require_*: --require-pass (WP-559, F-194, TST-02): all required tests
#     passed exits 0; a required test skipped (also an allow-listed skip),
#     failed, missing, present only as subtest or only in another package
#     exits != 0 with its name; a missing, empty or malformed list exits 2;
#     the real tests/tst02-targets.txt on a green run, without
#     TestCollectorPKI and with TestCollectorPKI skipped;
#   - step_summary: the counts, the skips and the required tests that did
#     not pass reach GITHUB_STEP_SUMMARY;
#   - allow_list_tests_exist: every allow-list entry names a test function
#     of its package;
#   - tst02_targets_exist: every entry of tests/tst02-targets.txt names a
#     test function of its package outside the auditdefect build tag;
#   - ci_wiring: the job migrations evaluates test-db.json only through the
#     script, with --require-pass tests/tst02-targets.txt (no inline grep
#     for "TEST_DATABASE_URL not set", no natsedge-specific skip step of
#     WP-374 left).
#
# Tools: jq, python3 with PyYAML (as tests/ci-config.test.sh).
#
# Usage: ./tests/ci-db-summary.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | head -n 10 | sed 's/^/     /'; return 0; }

SUMMARY=scripts/ci/db-test-summary.sh
CI=.github/workflows/ci.yml
TARGETS=tests/tst02-targets.txt
MOD=github.com/DataHub-Chiemgau/Reticora-CMDB/backend

# ev <action> <package> <test> [output]: one test2json event.
ev() {
    if [ "$1" = output ]; then
        jq -cn --arg p "$MOD/$2" --arg t "$3" --arg o "$4" '{Action: "output", Package: $p, Test: $t, Output: ($o + "\n")}'
    else
        jq -cn --arg a "$1" --arg p "$MOD/$2" --arg t "$3" '{Action: $a, Package: $p, Test: $t, Elapsed: 0.01}'
    fi
}
# skipped <package> <test> <message>: the events of one skipped test.
skipped() {
    ev run "$1" "$2"
    ev output "$1" "$2" "=== RUN   $2"
    ev output "$1" "$2" "    x_test.go:10: $3"
    ev output "$1" "$2" "--- SKIP: $2 (0.00s)"
    ev skip "$1" "$2"
}
passed() {
    ev run "$1" "$2"
    ev output "$1" "$2" "=== RUN   $2"
    ev output "$1" "$2" "--- PASS: $2 (0.01s)"
    ev pass "$1" "$2"
}
# A green base run: three packages, one subtest, a package without tests.
base() {
    passed internal/database TestMigrationsRoundtrip
    passed internal/jobs TestQueueRetries
    passed internal/discovery TestCollectorNATSEndpointAuthorization
    passed internal/discovery TestCollectorNATSEndpointAuthorization/publish
    jq -cn --arg p "$MOD/internal/version" '{Action: "skip", Package: $p, Elapsed: 0}'
}

# run_case <name> <want: 0|nonzero|2> <json file> [grep pattern for stdout]
run_case() {
    local name="$1" want="$2" file="$3" pattern="${4:-}"
    check_case "$name" "$want" "$pattern" "$file"
}
# req_case <name> <want> <required list> <json file> [pattern]: the same
# with --require-pass <required list>.
req_case() {
    local name="$1" want="$2" list="$3" file="$4" pattern="${5:-}"
    check_case "$name" "$want" "$pattern" --require-pass "$list" "$file"
}
# check_case <name> <want> <pattern> <script arguments...>
check_case() {
    local name="$1" want="$2" pattern="$3" out rc
    shift 3
    out="$(GITHUB_STEP_SUMMARY='' "$SUMMARY" "$@" 2>&1)"
    rc=$?
    case "$want" in
        0) [ "$rc" -eq 0 ] || { fail "$name: exit 0" "exit $rc: $out"; return; } ;;
        2) [ "$rc" -eq 2 ] || { fail "$name: exit 2" "exit $rc: $out"; return; } ;;
        *) [ "$rc" -ne 0 ] || { fail "$name: exit != 0" "exit 0: $out"; return; } ;;
    esac
    if [ -n "$pattern" ] && ! grep -qE -- "$pattern" <<<"$out"; then
        fail "$name: output matches /$pattern/" "$out"
        return
    fi
    ok "$name (exit $rc)"
}

{ base; } > "$TMP_ROOT/green.json"
run_case "skip_none: no skip" 0 "$TMP_ROOT/green.json" 'passed=3 failed=0 skipped=0 \(top-level\); subtests=1'

{ base; skipped internal/jobs TestQueueIntegration "TEST_NATS_URL not set"; } > "$TMP_ROOT/nats.json"
run_case "skip_nats: TEST_NATS_URL not set" nonzero "$TMP_ROOT/nats.json" '::error::test skipped in the DB job: internal/jobs TestQueueIntegration: x_test.go:10: TEST_NATS_URL not set'

{ base; skipped internal/discovery TestDisabledCollectorCutFallsBackToJWTExpiry "TEST_NATS_SERVER_BIN not set; skipping the end-to-end run of nats-edge.conf"; } > "$TMP_ROOT/natsedge.json"
run_case "skip_natsedge: test of natsedge_integration_test.go" nonzero "$TMP_ROOT/natsedge.json" 'internal/discovery TestDisabledCollectorCutFallsBackToJWTExpiry'

{ base; passed internal/discovery TestDisabledCollectorCutFallsBackToJWTExpiry
  skipped internal/discovery TestDisabledCollectorCutFallsBackToJWTExpiry/cut "no edge server"; } > "$TMP_ROOT/natsedge-sub.json"
run_case "skip_natsedge_subtest: subtest of natsedge_integration_test.go" nonzero "$TMP_ROOT/natsedge-sub.json" 'TestDisabledCollectorCutFallsBackToJWTExpiry/cut'

{ base; skipped internal/ci TestCIList "TEST_DATABASE_URL not set; skipping PostgreSQL integration test"; } > "$TMP_ROOT/nodb.json"
run_case "skip_no_database: TEST_DATABASE_URL not set" nonzero "$TMP_ROOT/nodb.json" 'skipped although TEST_DATABASE_URL is set'

{ base; skipped internal/tenant/rls TestPoolRefusesRLSBypassingRole "TEST_DATABASE_URL not set; skipping PostgreSQL integration test"; } > "$TMP_ROOT/nodb-allowed.json"
run_case "skip_no_database_allowed_test: TEST_DATABASE_URL not set in an allow-listed test" nonzero "$TMP_ROOT/nodb-allowed.json" 'skipped although TEST_DATABASE_URL is set'

{ base; skipped internal/identity TestRevocationIntegration "TEST_REDIS_URL not set; skipping Redis integration test"; } > "$TMP_ROOT/redis.json"
run_case "skip_redis: TEST_REDIS_URL not set" nonzero "$TMP_ROOT/redis.json" 'internal/identity TestRevocationIntegration'

{ base; skipped internal/tenant/rls TestPoolRefusesRLSBypassingRole "login role already restricted; the bypass guard cannot be exercised"; } > "$TMP_ROOT/allowed.json"
run_case "skip_allow_list: allow-listed test" 0 "$TMP_ROOT/allowed.json" 'allowed skip: internal/tenant/rls TestPoolRefusesRLSBypassingRole: x_test.go:10: login role already restricted'

{ base; passed internal/tenant/rls TestPoolRefusesRLSBypassingRole
  skipped internal/tenant/rls TestPoolRefusesRLSBypassingRole/sub "restricted"; } > "$TMP_ROOT/allowed-sub.json"
run_case "skip_allow_list_subtest: subtest of an allow-listed test" 0 "$TMP_ROOT/allowed-sub.json" 'allow-listed=1, not allowed=0'

# The allow-list names exact tests: the same name in another package and a
# test with the allowed name as prefix are not covered.
{ base; skipped internal/tenant TestPoolRefusesRLSBypassingRole "restricted"; } > "$TMP_ROOT/allowed-other-pkg.json"
run_case "skip_allow_list_other_package: allowed name in another package" nonzero "$TMP_ROOT/allowed-other-pkg.json"
{ base; skipped internal/tenant/rls TestPoolRefusesRLSBypassingRoleTwice "restricted"; } > "$TMP_ROOT/allowed-prefix.json"
run_case "skip_allow_list_prefix: test with the allowed name as prefix" nonzero "$TMP_ROOT/allowed-prefix.json"

: > "$TMP_ROOT/empty.json"
run_case "no_output: empty test-db.json" nonzero "$TMP_ROOT/empty.json" 'no test output recorded'
jq -cn --arg p "$MOD/internal/version" '{Action: "skip", Package: $p, Elapsed: 0}' > "$TMP_ROOT/no-tests.json"
run_case "no_tests: no test result" nonzero "$TMP_ROOT/no-tests.json" 'no test result'
out="$("$SUMMARY" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ]; then ok "usage: missing argument exits 2"; else fail "usage: missing argument exits 2" "exit $rc: $out"; fi

# ─── --require-pass (WP-559, F-194) ─────────────────────────────────────────
# Fixture list: two tests of the base run, with a comment and a blank line.
printf '# required\n\ninternal/jobs TestQueueRetries\ninternal/discovery   TestCollectorNATSEndpointAuthorization  # mTLS\n' > "$TMP_ROOT/req.txt"
req_case "require_all_pass: all required tests passed" 0 "$TMP_ROOT/req.txt" "$TMP_ROOT/green.json" '^Required tests \(.*req\.txt\): 2 of 2 passed$'

{ base; skipped internal/jobs TestQueueRetries "TEST_NATS_URL not set"; } | jq -c 'select(.Test != "TestQueueRetries" or .Action != "pass")' > "$TMP_ROOT/req-skip.json"
req_case "require_skip: a required test skipped" nonzero "$TMP_ROOT/req.txt" "$TMP_ROOT/req-skip.json" '::error::required test did not pass in the DB job \(.*\): internal/jobs TestQueueRetries: skip'

# An allow-listed skip satisfies the skip rule but not --require-pass.
{ base; skipped internal/tenant/rls TestPoolRefusesRLSBypassingRole "login role already restricted"; } > "$TMP_ROOT/req-allowed.json"
printf 'internal/tenant/rls TestPoolRefusesRLSBypassingRole\n' > "$TMP_ROOT/req-allowed.txt"
req_case "require_allow_listed_skip: an allow-listed skip of a required test" nonzero "$TMP_ROOT/req-allowed.txt" "$TMP_ROOT/req-allowed.json" 'required test did not pass in the DB job \(.*\): internal/tenant/rls TestPoolRefusesRLSBypassingRole: skip'

{ base; } | jq -c 'if .Test == "TestQueueRetries" and .Action == "pass" then .Action = "fail" else . end' > "$TMP_ROOT/req-fail.json"
req_case "require_fail: a required test failed" nonzero "$TMP_ROOT/req.txt" "$TMP_ROOT/req-fail.json" 'required test did not pass in the DB job \(.*\): internal/jobs TestQueueRetries: fail'

{ base; } | jq -c 'select(.Test != "TestQueueRetries")' > "$TMP_ROOT/req-missing.json"
req_case "require_missing: a required test without result" nonzero "$TMP_ROOT/req.txt" "$TMP_ROOT/req-missing.json" '::error::required test did not run in the DB job \(.*\): internal/jobs TestQueueRetries$'

# A passed subtest or the same name in another package is not the test.
{ base; passed internal/jobs TestQueueRetries/sub; } | jq -c 'select(.Test != "TestQueueRetries")' > "$TMP_ROOT/req-subtest.json"
req_case "require_subtest_only: only a subtest of the required test passed" nonzero "$TMP_ROOT/req.txt" "$TMP_ROOT/req-subtest.json" 'did not run in the DB job \(.*\): internal/jobs TestQueueRetries$'
printf 'internal/queue TestQueueRetries\n' > "$TMP_ROOT/req-other-pkg.txt"
req_case "require_other_package: the name passed in another package" nonzero "$TMP_ROOT/req-other-pkg.txt" "$TMP_ROOT/green.json" 'did not run in the DB job \(.*\): internal/queue TestQueueRetries$'

# Pass in one run and skip in another (-count > 1) is not pass.
{ base; skipped internal/jobs TestQueueRetries "flaky"; } > "$TMP_ROOT/req-mixed.json"
req_case "require_mixed: pass and skip of the same test" nonzero "$TMP_ROOT/req.txt" "$TMP_ROOT/req-mixed.json" 'internal/jobs TestQueueRetries: (pass,skip|skip,pass)'

: > "$TMP_ROOT/req-empty.txt"
req_case "require_usage_empty_list: list without tests" 2 "$TMP_ROOT/req-empty.txt" "$TMP_ROOT/green.json" 'lists no test'
req_case "require_usage_missing_list: list does not exist" 2 "$TMP_ROOT/no-such-list.txt" "$TMP_ROOT/green.json" 'cannot read'
printf 'internal/jobs\n' > "$TMP_ROOT/req-bad.txt"
req_case "require_usage_malformed: line without test name" 2 "$TMP_ROOT/req-bad.txt" "$TMP_ROOT/green.json" 'req-bad\.txt:1: want <package> <top-level test>'
printf 'internal/jobs TestQueueRetries/sub\n' > "$TMP_ROOT/req-bad-sub.txt"
req_case "require_usage_subtest: subtest instead of top-level test" 2 "$TMP_ROOT/req-bad-sub.txt" "$TMP_ROOT/green.json" 'want <package> <top-level test>'
out="$("$SUMMARY" --require-pass 2>&1)"; rc=$?
if [ "$rc" -eq 2 ]; then ok "require_usage_no_value: --require-pass without file exits 2"; else fail "require_usage_no_value: --require-pass without file exits 2" "exit $rc: $out"; fi

# The real target list of the TST-02 scenarios: a run in which every target
# passed, one without TestCollectorPKI and one with TestCollectorPKI skipped.
targets="$(sed 's/#.*$//' "$TARGETS" | awk 'NF == 2 { print $1 "\t" $2 }')"
target_count="$(printf '%s\n' "$targets" | grep -c .)"
{ base; while IFS=$'\t' read -r pkg test; do passed "$pkg" "$test"; done <<<"$targets"; } > "$TMP_ROOT/tst02-green.json"
req_case "require_tst02_green: every TST-02 target passed" 0 "$TARGETS" "$TMP_ROOT/tst02-green.json" "^Required tests \\(tests/tst02-targets\\.txt\\): $target_count of $target_count passed$"
jq -c 'select(.Test != "TestCollectorPKI")' "$TMP_ROOT/tst02-green.json" > "$TMP_ROOT/tst02-missing.json"
req_case "require_tst02_missing: TestCollectorPKI did not run" nonzero "$TARGETS" "$TMP_ROOT/tst02-missing.json" '::error::required test did not run in the DB job \(tests/tst02-targets\.txt\): internal/discovery TestCollectorPKI$'
{ jq -c 'select(.Test != "TestCollectorPKI")' "$TMP_ROOT/tst02-green.json"
  skipped internal/discovery TestCollectorPKI "login role already restricted"; } > "$TMP_ROOT/tst02-skip.json"
req_case "require_tst02_skip: TestCollectorPKI skipped" nonzero "$TARGETS" "$TMP_ROOT/tst02-skip.json" '::error::required test did not pass in the DB job \(tests/tst02-targets\.txt\): internal/discovery TestCollectorPKI: skip'

# ─── step summary ───────────────────────────────────────────────────────────
: > "$TMP_ROOT/step-summary.md"
GITHUB_STEP_SUMMARY="$TMP_ROOT/step-summary.md" "$SUMMARY" "$TMP_ROOT/nats.json" >/dev/null 2>&1
if grep -q '^| 3 | 3 | 0 | 1 | 1 | 1 |$' "$TMP_ROOT/step-summary.md" &&
   grep -q '^| denied | internal/jobs | TestQueueIntegration | x_test.go:10: TEST_NATS_URL not set |$' "$TMP_ROOT/step-summary.md"; then
    ok "step_summary: counts and skips in GITHUB_STEP_SUMMARY"
else
    fail "step_summary: counts and skips in GITHUB_STEP_SUMMARY" "$(cat "$TMP_ROOT/step-summary.md")"
fi
: > "$TMP_ROOT/step-summary-req.md"
GITHUB_STEP_SUMMARY="$TMP_ROOT/step-summary-req.md" "$SUMMARY" --require-pass "$TARGETS" "$TMP_ROOT/tst02-missing.json" >/dev/null 2>&1
if grep -q "^Required tests (tests/tst02-targets.txt): $((target_count - 1)) of $target_count passed$" "$TMP_ROOT/step-summary-req.md" &&
   grep -q '^| missing | internal/discovery | TestCollectorPKI |$' "$TMP_ROOT/step-summary-req.md"; then
    ok "step_summary_required: required tests not passed in GITHUB_STEP_SUMMARY"
else
    fail "step_summary_required: required tests not passed in GITHUB_STEP_SUMMARY" "$(cat "$TMP_ROOT/step-summary-req.md")"
fi

# ─── allow-list entries name existing tests ─────────────────────────────────
missing=""
while IFS=$'\t' read -r pkg test _; do
    [ -n "$pkg" ] || continue
    grep -qE "^func ${test}\(t \*testing\.T\)" "backend/$pkg"/*_test.go 2>/dev/null || missing="$missing $pkg.$test"
done < <(sed -n "/^ALLOW_LIST=\"\$(cat <<'EOF'$/,/^EOF$/p" "$SUMMARY" | sed '1d;$d')
entries="$(sed -n "/^ALLOW_LIST=\"\$(cat <<'EOF'$/,/^EOF$/p" "$SUMMARY" | sed '1d;$d' | grep -c .)"
if [ -z "$missing" ] && [ "$entries" -gt 0 ]; then
    ok "allow_list_tests_exist: $entries allow-list entries name test functions of their packages"
else
    fail "allow_list_tests_exist: allow-list entries name test functions of their packages" "entries=$entries missing:$missing"
fi

# ─── TST-02 targets name existing tests ─────────────────────────────────────
# Outside the auditdefect build tag, so the job migrations compiles them.
missing=""
while IFS=$'\t' read -r pkg test; do
    [ -n "$pkg" ] || continue
    found=""
    while IFS= read -r f; do
        grep -q '^//go:build.*auditdefect' "$f" || found=1
    done < <(grep -lE "^func ${test}\(t \*testing\.T\)" "backend/$pkg"/*_test.go 2>/dev/null)
    [ -n "$found" ] || missing="$missing $pkg.$test"
done <<<"$targets"
if [ -z "$missing" ] && [ "$target_count" -gt 0 ]; then
    ok "tst02_targets_exist: $target_count entries of $TARGETS name test functions of their packages"
else
    fail "tst02_targets_exist: entries of $TARGETS name test functions of their packages" "entries=$target_count missing:$missing"
fi

# ─── ci.yml wiring ──────────────────────────────────────────────────────────
wiring="$(python3 - "$CI" <<'PY'
import sys, yaml
job = yaml.safe_load(open(sys.argv[1]))["jobs"]["migrations"]
errs = []
readers = [s for s in job["steps"] if "test-db.json" in s.get("run", "") and "--jsonfile test-db.json" not in s.get("run", "")]
if [s.get("run", "").strip() for s in readers] != ["../scripts/ci/db-test-summary.sh --require-pass ../tests/tst02-targets.txt test-db.json"]:
    errs.append("test-db.json is read by: %s" % [s.get("name") for s in readers])
elif readers[0].get("if") != "always()" or readers[0].get("working-directory") != "backend":
    errs.append("summary step must run if: always() in backend/")
for s in job["steps"]:
    run = s.get("run", "")
    if "TEST_DATABASE_URL not set" in run or "natsedge-tests.txt" in run or "natsedge_integration_test.go" in run:
        errs.append("inline skip check left in step %r" % s.get("name"))
env = job.get("env", {})
test_env = next((s.get("env", {}) for s in job["steps"] if "--jsonfile test-db.json" in s.get("run", "")), {})
for var in ("TEST_DATABASE_URL", "TEST_APP_DATABASE_URL", "TEST_REDIS_URL", "TEST_NATS_URL"):
    if var not in env and var not in test_env:
        errs.append("%s not set for the test step" % var)
print("\n".join(errs))
PY
)"
if [ -z "$wiring" ]; then
    ok "ci_wiring: the job migrations evaluates test-db.json only through $SUMMARY"
else
    fail "ci_wiring: the job migrations evaluates test-db.json only through $SUMMARY" "$wiring"
fi

echo
echo "ci-db-summary: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
