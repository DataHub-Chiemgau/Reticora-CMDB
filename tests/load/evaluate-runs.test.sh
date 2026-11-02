#!/usr/bin/env bash
#
# evaluate-runs.test.sh — tests for tests/load/evaluate-runs.sh, the
# evaluation of the load-test evidence over three archived load.yml runs by
# the rule of E-58 (supplement of 2026-10-26, WP-439): a k6 threshold is
# held when the median of its values over the three runs is within it (p95,
# max, rate, counter, gauge; the rate of a counter is its rate), a job fails
# when it is red in at least two runs, a missing job counts as red and its
# values as missed. The evidence (WP-206, WP-612, F-417) needs three runs
# of the same server commit, each with all five jobs of load.yml; --partial
# evaluates a subset and never yields the evidence line. Cases: all held
# with the five jobs; mixed server commits, a run without server commit, a
# job missing in a run and runs of scenarios=api (exit 1, not evaluable);
# with --partial: one noisy run with the median held; a job red in one run
# with every threshold held; a job missing in one run with the median held
# (the worse of the other two values); thresholds missed in two runs; a job
# missing in one run and red in another and a job in only one run (no
# median of fewer values); mixed server commits (exit 1); wrong arguments
# (count, a file, no archive, the same run twice, a broken summary, no
# job).
#
# Usage: ./tests/load/evaluate-runs.test.sh
# shellcheck disable=SC2016 # the backticks of the Markdown code spans are literal
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
SCRIPT="$(pwd)/tests/load/evaluate-runs.sh"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
OUT=

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

# refute NAME OUTPUT PATTERN: no line of the output matches.
refute() {
	if grep -Eq -- "$3" <<<"$2"; then
		FAIL=$((FAIL + 1))
		echo "FAIL $1: a line matches $3"
		indent "$2"
	else
		PASS=$((PASS + 1))
		echo "ok   $1"
	fi
}

# evaluate NAME WANT ARCHIVE...: the script exits with WANT on the
# archives; OUT is its standard output.
evaluate() {
	local name=$1 want=$2 got
	shift 2
	OUT="$("$SCRIPT" "$@" 2>"$TMP_ROOT/err")"
	got=$?
	if [ "$got" -eq "$want" ]; then
		PASS=$((PASS + 1))
		echo "ok   $name"
	else
		FAIL=$((FAIL + 1))
		echo "FAIL $name: exit $got, want $want"
		indent "$OUT$(cat "$TMP_ROOT/err")"
	fi
}

# join ITEM...: the items separated by ", ".
join() {
	local out
	out="$(printf '%s, ' "$@")"
	echo "${out%, }"
}

# run DIR JOB...: an archive <run-id>/ as archive-run.sh writes it, whose
# ergebnis.md lists the jobs; the jobs get their files with result and the
# summary helpers. COMMIT is the server commit (default 8ca7f4b0…, "–"
# as archive-run.sh writes a missing one).
run() {
	local dir=$1 id
	shift
	id="$(basename "$dir")"
	mkdir -p "$dir"
	cat >"$dir/ergebnis.md" <<EOF
# Lastlauf $id (load.yml)

| Punkt | Wert |
|---|---|
| Lauf | https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/$id (Versuch 1, Ereignis workflow_dispatch, Branch main) |
| Lauf-Commit (tests/load) | \`8ca7f4b000000000000000000000000000000000\` |
| Server, Seed und Migrationen | \`${COMMIT:-8ca7f4b000000000000000000000000000000000}\` |
| Eingaben | objects 10000, duration 2m, scenarios all, server_ref – |
| Jobs | $(join "$@") |
| Vollständig | ja: jeder Job hat ein Artefakt |
| Ergebnis | alle Jobs grün |
EOF
}

# result DIR JOB STATUS: the outcome of a job in the archive.
result() {
	mkdir -p "$1/$2"
	printf 'job: %s\nstatus: %s\nseed: success\nvolumes: success\nk6: %s\nintegrity: success\n' "$2" "$3" "$3" >"$1/$2/result-$2.txt"
}

# The k6 summaries (--summary-export) of the jobs; a threshold is true when
# it was crossed, as k6 writes it.

# api DIR LIST DETAIL IMPACT CHECKS: api.js with these p95 values (ms) and
# the rate of the checks.
api() {
	mkdir -p "$1/api"
	jq -n --argjson list "$2" --argjson detail "$3" --argjson impact "$4" --argjson checks "$5" '{metrics: {
		"http_req_duration{name:list}": {"avg": 40, "med": 35, "p(95)": $list, "max": ($list * 2), "count": 900,
			"thresholds": {"p(95)<500": ($list >= 500)}},
		"http_req_duration{name:detail}": {"avg": 30, "med": 25, "p(95)": $detail, "max": ($detail * 2), "count": 900,
			"thresholds": {"p(95)<300": ($detail >= 300)}},
		"http_req_duration{name:impact}": {"avg": 600, "med": 550, "p(95)": $impact, "max": ($impact * 1.5), "count": 120,
			"thresholds": {"p(95)<2000": ($impact >= 2000)}},
		"checks": {"passes": 16889, "fails": 0, "value": $checks, "thresholds": {"rate>0.99": ($checks <= 0.99)}},
		"http_reqs": {"count": 16889, "rate": 140.7}
	}}' >"$1/api/api.json"
}

# spool DIR DRAIN FLUSH MISSING: ingest.js (spool) with the drain rate
# (gauge), the flush (trend, ms) and the missing records (counter); the
# accepted records carry a threshold on the rate of the counter.
spool() {
	mkdir -p "$1/ingest-spool"
	jq -n --argjson drain "$2" --argjson flush "$3" --argjson missing "$4" '{metrics: {
		"ingest_drain_records_per_s": {"value": $drain, "min": $drain, "max": $drain, "thresholds": {"value>=200": ($drain < 200)}},
		"ingest_spool_flush": {"avg": $flush, "p(95)": $flush, "max": $flush, "count": 1, "thresholds": {"max<300000": ($flush >= 300000)}},
		"ingest_records_missing": {"count": $missing, "rate": 0, "thresholds": {"count==0": ($missing != 0)}},
		"ingest_records_accepted": {"count": 10000, "rate": 120.5, "thresholds": {"rate>=100": false}},
		"http_req_failed{name:spool}": {"passes": 0, "fails": 157, "value": 0, "thresholds": {"rate==0": false}}
	}}' >"$1/ingest-spool/ingest-spool.json"
}

# rediscover DIR DELAY: ingest.js (rediscover) with the largest delay (ms).
rediscover() {
	mkdir -p "$1/ingest-rediscover"
	jq -n --argjson delay "$2" '{metrics: {
		"ingest_visible_delay": {"avg": 3000, "p(95)": ($delay * 0.9), "max": $delay, "count": 11, "thresholds": {"max<30000": ($delay >= 30000)}},
		"ingest_new_cis": {"count": 0, "rate": 0, "thresholds": {"count==0": false}}
	}}' >"$1/ingest-rediscover/ingest-rediscover.json"
}

# volume DIR LIST: api.js on the NFR-01 volumes, one threshold.
volume() {
	mkdir -p "$1/nfr01-volume"
	jq -n --argjson list "$2" '{metrics: {
		"http_req_duration{name:list}": {"p(95)": $list, "max": ($list * 2), "count": 900, "thresholds": {"p(95)<500": ($list >= 500)}}
	}}' >"$1/nfr01-volume/nfr01-volume.json"
}

# ingest DIR DELAY: ingest.js (load) with the largest delay (ms).
ingest() {
	mkdir -p "$1/ingest"
	jq -n --argjson delay "$2" '{metrics: {
		"ingest_visible_delay": {"avg": 900, "p(95)": ($delay * 0.9), "max": $delay, "count": 11, "thresholds": {"max<30000": ($delay >= 30000)}},
		"ingest_records_missing": {"count": 0, "rate": 0, "thresholds": {"count==0": false}}
	}}' >"$1/ingest/ingest.json"
}

# full DIR: a complete run (scenarios=all), each of the five jobs green
# with every threshold held.
full() {
	run "$1" api ingest ingest-spool ingest-rediscover nfr01-volume
	result "$1" api success && api "$1" 95 110 870.5 1
	result "$1" ingest success && ingest "$1" 1590
	result "$1" ingest-spool success && spool "$1" 414.7 27410 0
	result "$1" ingest-rediscover success && rediscover "$1" 2350
	result "$1" nfr01-volume success && volume "$1" 103.7
}

# All held: three complete green runs of one server commit.
H="$TMP_ROOT/held"
for id in 1001 1002 1003; do full "$H/$id"; done
result "$H/1001" api success && api "$H/1001" 89.44 120.1 852 1
result "$H/1002" api success && api "$H/1002" 101.2 130 882 0.99534
result "$H/1003" api success && api "$H/1003" 95 110 870.5 1
result "$H/1001" ingest-spool success && spool "$H/1001" 798.04 15100 0
result "$H/1002" ingest-spool success && spool "$H/1002" 388.2 28720 0
result "$H/1003" ingest-spool success && spool "$H/1003" 963.1 20000 0
evaluate "all held: exit 0" 0 "$H/1001" "$H/1002" "$H/1003"
check "the overall line last" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe\): alle Budgets gehalten$'
check "the runs" "$OUT" '^\| 1002 \| https://github\.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/1002 \(Versuch 1, Ereignis workflow_dispatch, Branch main\) \| `8ca7f4b0+` \| objects 10000, duration 2m, scenarios all, server_ref – \| ja: jeder Job hat ein Artefakt \| alle Jobs grün \|$'
check "the preconditions are met" "$OUT" '^Voraussetzungen erfüllt\.$'
check "the five jobs in the order of load.yml" "$(grep '^## Job' <<<"$OUT" | tr '\n' ' ')" '^## Job `api` ## Job `ingest` ## Job `ingest-spool` ## Job `ingest-rediscover` ## Job `nfr01-volume` $'
check "a column per run" "$OUT" '^\| Metrik \| Schwelle \| 1001 \| 1002 \| 1003 \| Median \| gehalten \|$'
check "a p95 threshold: the p95 per run and their median" "$OUT" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<500` \| 89\.4 ms \| 101\.2 ms \| 95 ms \| 95 ms \| ja \|$'
check "a rate threshold with its decimals" "$OUT" '^\| `checks` \| `rate>0\.99` \| 1 \| 0\.9953 \| 1 \| 1 \| ja \|$'
check "a gauge threshold" "$OUT" '^\| `ingest_drain_records_per_s` \| `value>=200` \| 798 Records/s \| 388\.2 Records/s \| 963\.1 Records/s \| 798 Records/s \| ja \|$'
check "a max threshold" "$OUT" '^\| `ingest_spool_flush` \| `max<300000` \| 15100 ms \| 28720 ms \| 20000 ms \| 20000 ms \| ja \|$'
check "a counter threshold" "$OUT" '^\| `ingest_records_missing` \| `count==0` \| 0 \| 0 \| 0 \| 0 \| ja \|$'
check "the rate of a counter" "$OUT" '^\| `ingest_records_accepted` \| `rate>=100` \| 120\.5 \| 120\.5 \| 120\.5 \| 120\.5 \| ja \|$'
check "the rate of a rate metric on a tagged sub-metric" "$OUT" '^\| `http_req_failed\{name:spool\}` \| `rate==0` \| 0 \| 0 \| 0 \| 0 \| ja \|$'
check "the status of a job per run" "$OUT" '^\| Status des Jobs \| – \| grün \| grün \| grün \| rot in 0 von 3 \| ja \|$'
check "the verdict of a job" "$OUT" '^Urteil: gehalten \(Schwellen im Median: 5 von 5 gehalten; rot in 0 von 3 Läufen\)\.$'
refute "nothing missed" "$OUT" '\*\*nein\*\*|\*\*verfehlt\*\*'

# The evidence needs runs of the same code (F-417): mixed server commits are
# not evaluable, every budget held or not.
C="$TMP_ROOT/commits"
full "$C/1101"
COMMIT=9d1e2f3000000000000000000000000000000000 full "$C/1102"
full "$C/1103"
evaluate "mixed server commits: exit 1" 1 "$C/1101" "$C/1102" "$C/1103"
check "mixed server commits: not evaluable, both commits named" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe\): nicht bewertbar: verschiedene Server-Commits \(Server, Seed und Migrationen\): 1101 `8ca7f4b0+`, 1102 `9d1e2f30+`, 1103 `8ca7f4b0+`$'
check "mixed server commits: the preconditions not met" "$OUT" '^Voraussetzungen \*\*nicht erfüllt\*\*: verschiedene Server-Commits'
check "mixed server commits: the reason on stderr" "$(cat "$TMP_ROOT/err")" '^evaluate-runs: nicht bewertbar: verschiedene Server-Commits'
check "mixed server commits: the medians are still reported" "$OUT" '^Urteil: gehalten \(Schwellen im Median: 5 von 5 gehalten; rot in 0 von 3 Läufen\)\.$'
refute "mixed server commits: no evidence line" "$OUT" 'alle Budgets gehalten'
G="$TMP_ROOT/no-commit"
full "$G/1201" && full "$G/1202" && COMMIT=– full "$G/1203"
evaluate "a run without server commit: exit 1" 1 "$G/1201" "$G/1202" "$G/1203"
check "a run without server commit is named" "$(tail -n 1 <<<"$OUT")" ': nicht bewertbar: kein Server-Commit \(Server, Seed und Migrationen\) in Lauf 1203$'

# Every run has each of the five jobs: a job without its directory in one
# run (archive-run.sh: listed, no artifact) and a job the run does not list
# make the runs not evaluable, although the median would hold.
J="$TMP_ROOT/missing-job"
for id in 1301 1302 1303; do full "$J/$id"; done
rm -r "$J/1303/nfr01-volume"
sed -i 's/^| Jobs | .*/| Jobs | api, ingest-spool, ingest-rediscover, nfr01-volume |/' "$J/1302/ergebnis.md"
rm -r "$J/1302/ingest"
evaluate "missing job: exit 1" 1 "$J/1301" "$J/1302" "$J/1303"
check "missing job: the jobs and their runs named" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe\): nicht bewertbar: Job `ingest` fehlt in Lauf 1302; Job `nfr01-volume` fehlt in Lauf 1303$'
check "missing job: the reason on stderr" "$(cat "$TMP_ROOT/err")" '^evaluate-runs: nicht bewertbar: Job `nfr01-volume` fehlt in Lauf 1303$'
check "missing job: the job is still shown as missing" "$OUT" '^\| Status des Jobs \| – \| grün \| grün \| \*\*fehlt\*\* \| rot in 1 von 3 \| ja \|$'

# The probe of F-417: three runs with scenarios=api (only the job api), each
# of another server commit, every budget held.
P="$TMP_ROOT/probe"
for c in 1 2 3; do
	COMMIT="commit$c" run "$P/140$c" api
	result "$P/140$c" api success && api "$P/140$c" 120 110 870.5 1
done
evaluate "three runs of scenarios=api and three server commits: exit 1" 1 "$P/1401" "$P/1402" "$P/1403"
check "scenarios=api: the commits and the four missing jobs named" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe\): nicht bewertbar: verschiedene Server-Commits .*; Job `ingest` fehlt in Lauf 1401, 1402, 1403; Job `ingest-spool` fehlt in Lauf 1401, 1402, 1403; Job `ingest-rediscover` fehlt in Lauf 1401, 1402, 1403; Job `nfr01-volume` fehlt in Lauf 1401, 1402, 1403$'
evaluate "--partial with three server commits: exit 1" 1 --partial "$P/1401" "$P/1402" "$P/1403"
check "--partial with three server commits: not evaluable" "$(tail -n 1 <<<"$OUT")" '^Teilbewertung nach E-58 \(Median über drei Läufe, kein G1-Nachweis\): nicht bewertbar: verschiedene Server-Commits'
refute "--partial: the missing jobs are not named" "$OUT" 'fehlt in Lauf'

# --partial (a diagnosis, never the evidence): the jobs the runs have, a job
# missing in a run counts as red there.
# One noisy run: api misses two budgets in run 2, the median holds.
N="$TMP_ROOT/noisy"
for id in 2001 2002 2003; do run "$N/$id" api; done
result "$N/2001" api success && api "$N/2001" 89.44 120.1 852 1
result "$N/2002" api failure && api "$N/2002" 504 467 1990 1
result "$N/2003" api success && api "$N/2003" 120 110 870.5 1
evaluate "one noisy run, median held: exit 0" 0 --partial "$N/2001" "$N/2002" "$N/2003"
check "--partial: the mode named" "$OUT" '^Teilbewertung \(`--partial`\), kein G1-Nachweis: '
check "--partial: only the jobs of the runs" "$(grep '^## Job' <<<"$OUT" | tr '\n' ' ')" '^## Job `api` $'
check "the missed value in its run, the median within" "$OUT" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<500` \| 89\.4 ms \| 504 ms \| 120 ms \| 120 ms \| ja \|$'
check "the second missed value" "$OUT" '^\| `http_req_duration\{name:detail\}` \| `p\(95\)<300` \| 120\.1 ms \| 467 ms \| 110 ms \| 120\.1 ms \| ja \|$'
check "the red run" "$OUT" '^\| Status des Jobs \| – \| grün \| \*\*rot\*\* \| grün \| rot in 1 von 3 \| ja \|$'
check "held although red in one run" "$OUT" '^Urteil: gehalten \(Schwellen im Median: 4 von 4 gehalten; rot in 1 von 3 Läufen\)\.$'
check "all budgets held, as a partial evaluation" "$(tail -n 1 <<<"$OUT")" '^Teilbewertung nach E-58 \(Median über drei Läufe, kein G1-Nachweis\): alle Budgets gehalten$'
refute "--partial never gives the evidence line" "$OUT" '^Bewertung nach E-58'
evaluate "the same runs without --partial: exit 1" 1 "$N/2001" "$N/2002" "$N/2003"

# A job red in one run for another reason (integrity check), every
# threshold held in every run.
R="$TMP_ROOT/red-once"
for id in 3001 3002 3003; do run "$R/$id" ingest-spool; done
result "$R/3001" ingest-spool failure && spool "$R/3001" 798.04 15100 0
result "$R/3002" ingest-spool success && spool "$R/3002" 663.2 16000 0
result "$R/3003" ingest-spool success && spool "$R/3003" 700 17000 0
evaluate "a job red in one run with the median held: exit 0" 0 --partial "$R/3001" "$R/3002" "$R/3003"
check "red in one of three" "$OUT" '^\| Status des Jobs \| – \| \*\*rot\*\* \| grün \| grün \| rot in 1 von 3 \| ja \|$'
check "all budgets held despite the red run" "$OUT" '^Teilbewertung nach E-58 \(Median über drei Läufe, kein G1-Nachweis\): alle Budgets gehalten$'

# A job missing in one run, held in the other two: the missing value counts
# as missed, the median is the worse of the other two.
M="$TMP_ROOT/missing-once"
for id in 4001 4002 4003; do run "$M/$id" ingest-spool; done
result "$M/4001" ingest-spool success && spool "$M/4001" 798.04 15100 0
result "$M/4003" ingest-spool success && spool "$M/4003" 388.2 28720 0
evaluate "a job missing in one run, the median held: exit 0" 0 --partial "$M/4001" "$M/4002" "$M/4003"
check "a lower bound: the median is the lower of the two" "$OUT" '^\| `ingest_drain_records_per_s` \| `value>=200` \| 798 Records/s \| fehlt \| 388\.2 Records/s \| 388\.2 Records/s \| ja \|$'
check "an upper bound: the median is the higher of the two" "$OUT" '^\| `ingest_spool_flush` \| `max<300000` \| 15100 ms \| fehlt \| 28720 ms \| 28720 ms \| ja \|$'
check "the missing job is red in its run" "$OUT" '^\| Status des Jobs \| – \| grün \| \*\*fehlt\*\* \| grün \| rot in 1 von 3 \| ja \|$'

# Thresholds missed in two runs: an upper bound (p95), a rate and a gauge
# (lower bounds).
X="$TMP_ROOT/missed"
for id in 5001 5002 5003; do run "$X/$id" api ingest-spool; done
result "$X/5001" api failure && api "$X/5001" 89.44 120.1 2100 0.98
result "$X/5002" api success && api "$X/5002" 101.2 130 1900 0.995
result "$X/5003" api failure && api "$X/5003" 95 110 2300 0.97
result "$X/5001" ingest-spool failure && spool "$X/5001" 150 15100 0
result "$X/5002" ingest-spool success && spool "$X/5002" 250 16000 0
result "$X/5003" ingest-spool failure && spool "$X/5003" 180 17000 0
evaluate "thresholds missed in two runs: exit 1" 1 --partial "$X/5001" "$X/5002" "$X/5003"
check "a p95 missed in the median" "$OUT" '^\| `http_req_duration\{name:impact\}` \| `p\(95\)<2000` \| 2100 ms \| 1900 ms \| 2300 ms \| 2100 ms \| \*\*nein\*\* \|$'
check "a rate missed in the median" "$OUT" '^\| `checks` \| `rate>0\.99` \| 0\.98 \| 0\.995 \| 0\.97 \| 0\.98 \| \*\*nein\*\* \|$'
check "a gauge missed in the median" "$OUT" '^\| `ingest_drain_records_per_s` \| `value>=200` \| 150 Records/s \| 250 Records/s \| 180 Records/s \| 180 Records/s \| \*\*nein\*\* \|$'
check "a threshold held next to them" "$OUT" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<500` \| 89\.4 ms \| 101\.2 ms \| 95 ms \| 95 ms \| ja \|$'
check "red in two runs fails the job" "$OUT" '^\| Status des Jobs \| – \| \*\*rot\*\* \| grün \| \*\*rot\*\* \| rot in 2 von 3 \| \*\*nein\*\* \|$'
check "the verdict names both" "$OUT" '^Urteil: \*\*verfehlt\*\* \(Schwellen im Median: 2 von 4 gehalten; rot in 2 von 3 Läufen\)\.$'
check "the overall line names every miss" "$(tail -n 1 <<<"$OUT")" '^Teilbewertung nach E-58 \(Median über drei Läufe, kein G1-Nachweis\): verfehlt: `api` `checks` `rate>0\.99`; `api` `http_req_duration\{name:impact\}` `p\(95\)<2000`; `api` rot in 2 von 3 Läufen; `ingest-spool` `ingest_drain_records_per_s` `value>=200`; `ingest-spool` rot in 2 von 3 Läufen$'
refute "a held threshold is not named" "$(tail -n 1 <<<"$OUT")" 'name:list'

# A job missing in one run and red in another; a job in only one run.
Y="$TMP_ROOT/missing-red"
for id in 6001 6002 6003; do run "$Y/$id" api ingest-rediscover nfr01-volume; done
result "$Y/6001" api success && api "$Y/6001" 89.44 120.1 852 1
result "$Y/6002" api success && api "$Y/6002" 101.2 130 882 1
result "$Y/6003" api success && api "$Y/6003" 95 110 870.5 1
result "$Y/6002" ingest-rediscover failure && rediscover "$Y/6002" 51550
result "$Y/6003" ingest-rediscover success && rediscover "$Y/6003" 4820
result "$Y/6003" nfr01-volume success && volume "$Y/6003" 300
evaluate "a job missing in one run and red in another: exit 1" 1 --partial "$Y/6001" "$Y/6002" "$Y/6003"
check "missing and red: the median is the worse value" "$OUT" '^\| `ingest_visible_delay` \| `max<30000` \| fehlt \| 51550 ms \| 4820 ms \| 51550 ms \| \*\*nein\*\* \|$'
check "missing and red count as two red runs" "$OUT" '^\| Status des Jobs \| – \| \*\*fehlt\*\* \| \*\*rot\*\* \| grün \| rot in 2 von 3 \| \*\*nein\*\* \|$'
check "a job in one run has no median" "$OUT" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<500` \| fehlt \| fehlt \| 300 ms \| – \| \*\*nein\*\* \|$'
check "the overall line names both jobs" "$(tail -n 1 <<<"$OUT")" '^Teilbewertung nach E-58 \(Median über drei Läufe, kein G1-Nachweis\): verfehlt: .*`ingest-rediscover` rot in 2 von 3 Läufen; .*`nfr01-volume` rot in 2 von 3 Läufen$'
refute "the green job is not named" "$(tail -n 1 <<<"$OUT")" '`api`'

# Wrong arguments: exactly three run archives, never a median of two or four.
evaluate "no argument" 2
check "usage" "$(cat "$TMP_ROOT/err")" '^usage: .*evaluate-runs\.sh \[--partial\] <archive-1> <archive-2> <archive-3>$'
evaluate "--partial with two runs" 2 --partial "$H/1001" "$H/1002"
evaluate "two runs" 2 "$H/1001" "$H/1002"
check "no report on wrong arguments" "[$OUT]" '^\[\]$'
evaluate "four runs" 2 "$H/1001" "$H/1002" "$H/1003" "$N/2001"
printf 'x\n' >"$TMP_ROOT/file"
evaluate "a file instead of a directory" 2 "$H/1001" "$H/1002" "$TMP_ROOT/file"
evaluate "a missing directory" 2 "$H/1001" "$H/1002" "$TMP_ROOT/none"
evaluate "a directory that is no run archive" 2 "$H/1001" "$H/1002" "$H"
evaluate "the same run twice" 2 "$H/1001" "$H/1002" "$H/1001/"
check "the run given twice is named" "$(cat "$TMP_ROOT/err")" '1001'
B="$TMP_ROOT/broken"
for id in 7001 7002 7003; do run "$B/$id" api; done
for id in 7001 7002 7003; do result "$B/$id" api success && api "$B/$id" 89.44 120.1 852 1; done
printf '{"metrics": ' >"$B/7002/api/api.json"
evaluate "a broken k6 summary" 2 "$B/7001" "$B/7002" "$B/7003"
E="$TMP_ROOT/empty"
for id in 8001 8002 8003; do run "$E/$id"; done
evaluate "no job in the archives" 2 "$E/8001" "$E/8002" "$E/8003"

echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
