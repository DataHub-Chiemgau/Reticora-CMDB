#!/usr/bin/env bash
#
# db-test-summary.sh — the only evaluation of test-db.json (go test -json /
# gotestsum --jsonfile) in the ci.yml job migrations (WP-499, F-283, TST-04).
#
# The job provides PostgreSQL, Redis, NATS with JetStream, a nats-server
# binary and kustomize, so no test of the backend module may skip there.
# Every skipped test or subtest fails the job unless it is in the allow-list
# below (package relative to backend/, top-level test name, reason); an
# entry also covers the subtests of its test. A skip with the output
# "TEST_DATABASE_URL not set" fails the job even if the test is allowed.
# This general rule replaces the natsedge-specific check of WP-374.
#
# With --require-pass <file> every test listed in <file> ("<package relative
# to backend/> <top-level test>" per line, "#" comments) must have run with
# result pass: a listed test without result or with another result (skip,
# fail) fails the job with its name, also when its skip is allow-listed.
# ci.yml passes tests/tst02-targets.txt, the target tests of the TST-02
# scenarios (WP-559, F-194).
#
# Prints every top-level test as "<action>\t<package>\t<test>", the counts
# and each skip with its reason; with GITHUB_STEP_SUMMARY set it appends the
# counts and the skips to the job summary. Failed tests are reported only:
# the test step itself fails on them.
#
# Exit codes: 0 no skip outside the allow-list and every required test
# passed; 1 a skip outside the allow-list, a "TEST_DATABASE_URL not set"
# skip, a required test missing or not passed, no test output or no test at
# all; 2 usage error (also a missing, unreadable, empty or malformed
# --require-pass file).
#
# Tools: jq, awk.
#
# Usage: scripts/ci/db-test-summary.sh [--require-pass <file>] <test-db.json>
set -euo pipefail

usage() {
    echo "usage: $0 [--require-pass <file>] <test-db.json>" >&2
    exit 2
}
require=""
if [ "${1:-}" = --require-pass ]; then
    [ "$#" -ge 2 ] || usage
    require="$2"
    shift 2
fi
json="${1:-}"
if [ -z "$json" ] || [ "$#" -ne 1 ]; then
    usage
fi
# Required tests: "<package>\t<test>" per line, comments and blank lines
# dropped. A malformed line is a usage error, so a typo cannot drop a test.
required=""
if [ -n "$require" ]; then
    if [ ! -f "$require" ] || [ ! -r "$require" ]; then
        echo "$0: --require-pass: cannot read $require" >&2
        exit 2
    fi
    if ! required="$(awk -v OFS='\t' '
        { sub(/#.*$/, "") }
        NF == 0 { next }
        NF != 2 || $2 ~ /\// { printf "%s:%d: want <package> <top-level test>\n", FILENAME, FNR > "/dev/stderr"; bad = 1; next }
        { print $1, $2 }
        END { exit bad }' "$require")"; then
        exit 2
    fi
    if [ -z "$required" ]; then
        echo "$0: --require-pass: $require lists no test" >&2
        exit 2
    fi
fi
if [ ! -s "$json" ]; then
    echo "::error::no test output recorded ($json)"
    exit 1
fi

# Allow-list: tab-separated, one test per line. Add a test only when the job
# cannot provide what it needs, never for a missing service or variable.
ALLOW_LIST="$(cat <<'EOF'
internal/tenant/rls	TestPoolRefusesRLSBypassingRole	login-role guard: needs a TEST_DATABASE_URL login with SUPERUSER or BYPASSRLS to show that NewPool refuses it; skips when the login is already restricted (the CI login reticora is a superuser, so it runs there)
EOF
)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# One line per test event: "R\t<action>\t<package>\t<test>" for results and
# "O\t<package>\t<test>\t<output>" for test output. Packages are relative to
# the backend module.
jq -r '
    select(.Test != null)
    | (.Package | sub("^.*/backend/"; "") | sub("^.*/backend$"; ".")) as $pkg
    | if .Action == "pass" or .Action == "fail" or .Action == "skip" then
        "R\t\(.Action)\t\($pkg)\t\(.Test)"
      elif .Action == "output" then
        "O\t\($pkg)\t\(.Test)\t\(.Output | rtrimstr("\n") | gsub("\t"; " "))"
      else empty end' "$json" > "$tmp/events"

# Result lines, top-level tests and skips with the last output line before
# the skip (the t.Skip message) as the reason.
awk -F'\t' -v OFS='\t' -v results="$tmp/results" -v skips="$tmp/skips" '
    $1 == "O" {
        line = $4
        sub(/^[ \t]+/, "", line)
        if (line != "" && line !~ /^(=== |--- )/) last[$2 "\t" $3] = line
        next
    }
    $1 == "R" {
        print $2, $3, $4 > results
        if ($2 == "skip") print $3, $4, last[$3 "\t" $4] > skips
    }' "$tmp/events"
touch "$tmp/results" "$tmp/skips"

awk -F'\t' -v OFS='\t' 'index($3, "/") == 0' "$tmp/results" | sort -t$'\t' -k2,2 -k3,3 > "$tmp/top"
cat "$tmp/top"

count() { awk -F'\t' -v a="$1" '$1 == a { n++ } END { print n + 0 }' "$2"; }
pkgs="$(cut -f2 "$tmp/top" | sort -u | grep -c . || true)"
top_pass="$(count pass "$tmp/top")"
top_fail="$(count fail "$tmp/top")"
top_skip="$(count skip "$tmp/top")"
sub_total="$(awk -F'\t' 'index($3, "/") > 0' "$tmp/results" | grep -c . || true)"
skip_total="$(grep -c . "$tmp/skips" || true)"

# Skips outside the allow-list: the test or its top-level parent is not listed.
printf '%s\n' "$ALLOW_LIST" | awk -F'\t' -v OFS='\t' 'NF >= 2 { print $1, $2 }' > "$tmp/allow"
awk -F'\t' -v OFS='\t' '
    NR == FNR { allowed[$1 "\t" $2] = 1; next }
    {
        top = $2
        sub(/\/.*$/, "", top)
        print (($1 "\t" top) in allowed ? "allowed" : "denied"), $1, $2, $3
    }' "$tmp/allow" "$tmp/skips" > "$tmp/skip-verdicts"
denied="$(awk -F'\t' '$1 == "denied"' "$tmp/skip-verdicts" | grep -c . || true)"
allowed="$(awk -F'\t' '$1 == "allowed"' "$tmp/skip-verdicts" | grep -c . || true)"

echo "Tests in $pkgs packages: passed=$top_pass failed=$top_fail skipped=$top_skip (top-level); subtests=$sub_total; skips including subtests=$skip_total (allow-listed=$allowed, not allowed=$denied)"
# A package that fails as a whole (build error, timeout) has no result for
# its remaining tests; the test step fails on it, this only names it.
jq -r 'select(.Test == null and .Action == "fail") | "failed package: \(.Package | sub("^.*/backend/"; ""))"' "$json"

status=0
while IFS=$'\t' read -r verdict pkg test reason; do
    [ -n "$verdict" ] || continue
    if [ "$verdict" = denied ]; then
        echo "::error::test skipped in the DB job: $pkg $test: ${reason:-no reason printed}"
        status=1
    else
        echo "allowed skip: $pkg $test: ${reason:-no reason printed}"
    fi
done < "$tmp/skip-verdicts"

if awk -F'\t' '$1 == "O" && index($4, "TEST_DATABASE_URL not set") > 0 { f = 1 } END { exit !f }' "$tmp/events"; then
    echo "::error::PostgreSQL integration tests were skipped although TEST_DATABASE_URL is set"
    status=1
fi

# Required tests: every result of a listed top-level test must be pass, and
# there must be at least one.
req_total=0
req_failed=0
if [ -n "$required" ]; then
    printf '%s\n' "$required" > "$tmp/required"
    awk -F'\t' -v OFS='\t' '
        NR == FNR { results[$2 "\t" $3] = results[$2 "\t" $3] " " $1; next }
        {
            got = results[$1 "\t" $2]
            if (got == "") verdict = "missing"
            else if (got ~ /^( pass)+$/) verdict = "pass"
            else { verdict = substr(got, 2); gsub(/ /, ",", verdict) }
            print verdict, $1, $2
        }' "$tmp/top" "$tmp/required" > "$tmp/required-verdicts"
    while IFS=$'\t' read -r verdict pkg test; do
        req_total=$((req_total + 1))
        if [ "$verdict" = pass ]; then
            echo "required test passed: $pkg $test"
        elif [ "$verdict" = missing ]; then
            echo "::error::required test did not run in the DB job ($require): $pkg $test"
            req_failed=$((req_failed + 1))
        else
            echo "::error::required test did not pass in the DB job ($require): $pkg $test: $verdict"
            req_failed=$((req_failed + 1))
        fi
    done < "$tmp/required-verdicts"
    echo "Required tests ($require): $((req_total - req_failed)) of $req_total passed"
    [ "$req_failed" -eq 0 ] || status=1
fi

if [ "$((top_pass + top_fail + top_skip))" -eq 0 ]; then
    echo "::error::no test result in $json"
    status=1
fi

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
        echo "### PostgreSQL test run (job migrations)"
        echo
        echo "| packages | passed | failed | skipped | subtests | skips not allowed |"
        echo "|---:|---:|---:|---:|---:|---:|"
        echo "| $pkgs | $top_pass | $top_fail | $top_skip | $sub_total | $denied |"
        if [ "$skip_total" -gt 0 ]; then
            echo
            echo "| verdict | package | test | reason |"
            echo "|---|---|---|---|"
            awk -F'\t' '{ gsub(/\|/, "\\|", $4); printf "| %s | %s | %s | %s |\n", $1, $2, $3, $4 }' "$tmp/skip-verdicts"
        fi
        if [ -n "$required" ]; then
            echo
            echo "Required tests ($require): $((req_total - req_failed)) of $req_total passed"
            if [ "$req_failed" -gt 0 ]; then
                echo
                echo "| result | package | test |"
                echo "|---|---|---|"
                awk -F'\t' '$1 != "pass" { printf "| %s | %s | %s |\n", $1, $2, $3 }' "$tmp/required-verdicts"
            fi
        fi
    } >> "$GITHUB_STEP_SUMMARY"
fi

exit "$status"
