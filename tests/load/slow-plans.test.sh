#!/usr/bin/env bash
#
# slow-plans.test.sh — tests for tests/load/slow-plans.sh, the auto_explain
# summary of the api.js job of .github/workflows/load.yml (F-117, WP-438),
# on a PostgreSQL log in the image's stderr format: entries of the same
# statement (also a query text over several lines) are counted together
# with their total and longest duration, ordered by total; the plan of the
# longest execution follows; a log without entries says so; the log may
# come on stdin.
#
# Usage: ./tests/load/slow-plans.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
SCRIPT="$(pwd)/tests/load/slow-plans.sh"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0

# indent prints the output of a failed case below it.
indent() { while IFS= read -r line; do printf '     %s\n' "$line"; done <<<"$1"; }

# check NAME OUTPUT PATTERN: the output has a line matching the extended
# regular expression.
check() {
	if grep -Eq -- "$3" <<<"$2"; then
		PASS=$((PASS + 1))
		echo "ok   $1"
	else
		FAIL=$((FAIL + 1))
		echo "FAIL $1: no line matches $3"
		indent "$2"
	fi
}

LOG="$TMP_ROOT/postgres.log"
tab=$'\t'
cat >"$LOG" <<EOF
2026-10-26 00:44:30.000 UTC [1] LOG:  checkpoint starting: time
2026-10-26 00:44:31.946 UTC [385] LOG:  duration: 300.5 ms  plan:
${tab}Query Text: SELECT
${tab}${tab}id::text,
${tab}${tab}name
${tab}FROM ci WHERE deleted_at IS NULL ORDER BY ci.created_at DESC LIMIT (SELECT \$1::bigint)
${tab}Query Parameters: \$1 = '50'
${tab}Limit  (cost=2159.08..2159.09 rows=1 width=894) (actual rows=50 loops=1)
${tab}  ->  Sort  (cost=2159.07..2159.08 rows=1 width=894) (actual rows=50 loops=1)
${tab}        Sort Key: ci.created_at DESC
2026-10-26 00:44:32.017 UTC [371] LOG:  duration: 112.984 ms  plan:
${tab}Query Text: SELECT pg_advisory_xact_lock(\$1, \$2)
${tab}Query Parameters: \$1 = '1382380649', \$2 = '-2094055860'
${tab}Result  (cost=0.00..0.01 rows=1 width=4) (actual rows=1 loops=1)
2026-10-26 00:44:33.000 UTC [385] LOG:  duration: 500.1 ms  plan:
${tab}Query Text: SELECT id::text, name FROM ci WHERE deleted_at IS NULL ORDER BY ci.created_at DESC LIMIT (SELECT \$1::bigint)
${tab}Query Parameters: \$1 = '200'
${tab}Limit  (cost=2159.08..2159.09 rows=1 width=894) (actual rows=200 loops=1)
${tab}  ->  Seq Scan on ci  (cost=0.00..2159.06 rows=1 width=894) (actual rows=10000 loops=1)
2026-10-26 00:44:34.000 UTC [1] LOG:  checkpoint complete: wrote 3 buffers
EOF

out="$("$SCRIPT" "$LOG" 2>&1)"
rows="$(grep -E '^ +[0-9]+ ' <<<"$out")"
check "the statement over three lines and in one line counts twice" "$(head -1 <<<"$rows")" \
	'^ +2 +801 +500  SELECT id::text, name FROM ci WHERE deleted_at IS NULL ORDER BY'
check "ordered by total: the lock wait second" "$(sed -n 2p <<<"$rows")" '^ +1 +113 +113  SELECT pg_advisory_xact_lock'
check "two statements" "$(wc -l <<<"$rows")" '^2$'
longest="$(sed -n '/^== longest execution of: SELECT id::text/,/^== /p' <<<"$out")"
check "the plan of the longest execution follows" "$longest" 'duration: 500.1 ms'
check "with its plan" "$longest" 'Seq Scan on ci .*actual rows=10000'
if grep -q 'duration: 300.5 ms' <<<"$longest"; then
	FAIL=$((FAIL + 1))
	echo "FAIL the shorter execution is not shown"
	indent "$longest"
else
	PASS=$((PASS + 1))
	echo "ok   the shorter execution is not shown"
fi
check "other log lines end an entry" "$(sed -n '/^== longest execution of: SELECT pg_advisory/,$p' <<<"$out")" \
	'^.Result  \(cost'
if grep -q 'checkpoint complete' <<<"$out"; then
	FAIL=$((FAIL + 1))
	echo "FAIL a log line after an entry is part of its plan"
else
	PASS=$((PASS + 1))
	echo "ok   a log line after an entry is not part of its plan"
fi

check "stdin" "$("$SCRIPT" <"$LOG" 2>&1)" '^ +1 +113 +113  SELECT pg_advisory_xact_lock'
check "no entry" "$(printf '2026-10-26 00:44:30.000 UTC [1] LOG:  checkpoint starting: time\n' | "$SCRIPT" 2>&1)" \
	'^no statement over auto_explain.log_min_duration$'

echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
