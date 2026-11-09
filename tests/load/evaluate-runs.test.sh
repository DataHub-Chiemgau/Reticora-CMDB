#!/usr/bin/env bash
#
# evaluate-runs.test.sh — tests for tests/load/evaluate-runs.sh, the
# evaluation of the load-test evidence over three archived load.yml runs by
# the rule of E-58 (supplement of 2026-10-26, WP-439): a k6 threshold is
# held when the median of its values over the three runs is within it (p95,
# max, rate, counter, gauge; the rate of a counter is its rate), a job fails
# when it is red in at least two runs, a missing job counts as red and its
# values as missed. The evidence (WP-206, WP-612, F-417) needs three runs
# of the same server commit, each with every job of the gate: G2 (default,
# WP-327) the five jobs of G1 and phase2, import, template and computed,
# --gate G1 the five jobs of G1; --partial evaluates a subset and never
# yields the evidence line. The write jobs import, template and computed
# carry the thresholds of the ingest beside them (<job>-ingest.json,
# WP-823). Cases: all held with the nine jobs, the phase-2 jobs and the
# ingest beside the write jobs among them; mixed server commits, a run
# without server commit, a job missing in a run and runs of scenarios=api
# (exit 1, not evaluable); the gates: runs with the five jobs of G1 not
# evaluable for G2 and held with --gate G1, --gate G1 on complete G2 runs,
# a G1 job missing with --gate G1, --gate G2 like the default; the
# phase-2 jobs: a phase2 budget and an ingest budget of a write job missed
# in the median, an ingest summary missing in one run with the median held,
# a write job without any ingest summary;
# the budget sets (E-146, tests/load/budgets.json): --gate G2 evaluates
# every p95 threshold the set lists with its G2 budget whatever bound the
# run recorded (runs of G1 and G2 in one table row), --gate G1 with the
# budgets of NFR-02, a value between the two held with G2 and missed with
# G1, a p95 threshold without a budget keeps its bound, G1 of the file
# unchanged (NFR-02) and both sets with the same keys;
# with --partial: one noisy run with the median held; a job red in one run
# with every threshold held; a job missing in one run with the median held
# (the worse of the other two values); thresholds missed in two runs; a job
# missing in one run and red in another and a job in only one run (no
# median of fewer values); mixed server commits (exit 1); wrong arguments
# (count, a file, no archive, the same run twice, a broken summary, no
# job, a broken ingest summary, an unknown or missing gate, --gate twice,
# --partial with --gate).
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
		"ingest_records_missing": {"count": 0, "rate": 0, "thresholds": {"count==0": false}},
		"http_req_duration{name:ingest}": {"p(95)": 240, "max": 610, "count": 4000, "thresholds": {"p(95)<1000": false}}
	}}' >"$1/ingest/ingest.json"
}

# phase2 DIR ASSETS RELATION BULK: phase2.js (WP-822) with the p95 of the
# asset list and the relationship search (ms) and of the asset bulk (ms).
phase2() {
	mkdir -p "$1/phase2"
	jq -n --argjson assets "$2" --argjson relation "$3" --argjson bulk "$4" '{metrics: {
		"http_req_duration{name:asset_list}": {"p(95)": $assets, "max": ($assets * 2), "count": 900, "thresholds": {"p(95)<500": ($assets >= 500)}},
		"http_req_duration{name:relation_search}": {"p(95)": $relation, "max": ($relation * 2), "count": 300, "thresholds": {"p(95)<500": ($relation >= 500)}},
		"bulk_assets_500_duration": {"p(95)": $bulk, "max": $bulk, "count": 3, "thresholds": {"p(95)<30000": ($bulk >= 30000)}},
		"bulk_assets_jobs_succeeded": {"count": 3, "rate": 0.02, "thresholds": {"count==3": false}},
		"http_req_failed": {"passes": 0, "fails": 4000, "value": 0, "thresholds": {"rate==0": false}}
	}}' >"$1/phase2/phase2.json"
}

# write DIR JOB JOBS: writes.js (WP-823) of the write job with the number
# of jobs that succeeded (want: import 4, template and computed 1) and a
# duration without a threshold.
write() {
	local want=1
	if [ "$2" = import ]; then
		want=4
	fi
	mkdir -p "$1/$2"
	jq -n --arg job "$2" --argjson ok "$3" --argjson want "$want" '{metrics: {
		"\($job)_jobs_succeeded": {"count": $ok, "rate": 0.01, "thresholds": {"count==\($want)": ($ok != $want)}},
		"\($job)_duration": {"p(95)": 61000, "max": 61000, "count": 1},
		"http_req_failed": {"passes": 0, "fails": 120, "value": 0, "thresholds": {"rate==0": false}},
		"checks": {"passes": 40, "fails": 0, "value": 1, "thresholds": {"rate==1": false}}
	}}' >"$1/$2/$2.json"
}

# beside DIR JOB DELAY MISSING: ingest.js (load) beside the write job
# (<job>-ingest.json) with the largest delay (ms) and the missing records.
beside() {
	mkdir -p "$1/$2"
	jq -n --argjson delay "$3" --argjson missing "$4" '{metrics: {
		"ingest_visible_delay": {"avg": 900, "p(95)": ($delay * 0.9), "max": $delay, "count": 25, "thresholds": {"max<30000": ($delay >= 30000)}},
		"ingest_records_missing": {"count": $missing, "rate": 0, "thresholds": {"count==0": ($missing != 0)}},
		"http_req_failed": {"passes": 0, "fails": 1200, "value": 0, "thresholds": {"rate==0": false}}
	}}' >"$1/$2/$2-ingest.json"
}

# g1 DIR: a complete run of G1 (scenarios=all before WP-822), each of the
# five jobs green with every threshold held.
g1() {
	run "$1" api ingest ingest-spool ingest-rediscover nfr01-volume
	g1jobs "$1"
}

# g1jobs DIR: the five jobs of G1, green with every threshold held.
g1jobs() {
	result "$1" api success && api "$1" 95 110 870.5 1
	result "$1" ingest success && ingest "$1" 1590
	result "$1" ingest-spool success && spool "$1" 414.7 27410 0
	result "$1" ingest-rediscover success && rediscover "$1" 2350
	result "$1" nfr01-volume success && volume "$1" 103.7
}

# full DIR: a complete run (scenarios=all), each of the nine jobs of G2
# green with every threshold held, the ingest beside the write jobs too.
full() {
	run "$1" api ingest ingest-spool ingest-rediscover nfr01-volume phase2 import template computed
	g1jobs "$1"
	result "$1" phase2 success && phase2 "$1" 120 310 9000
	local job
	for job in import template computed; do
		result "$1" "$job" success
		write "$1" "$job" "$([ "$job" = import ] && echo 4 || echo 1)"
		beside "$1" "$job" 2100 0
	done
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
check "the nine jobs of G2 in the order of load.yml" "$(grep '^## Job' <<<"$OUT" | tr '\n' ' ')" '^## Job `api` ## Job `ingest` ## Job `ingest-spool` ## Job `ingest-rediscover` ## Job `nfr01-volume` ## Job `phase2` ## Job `import` ## Job `template` ## Job `computed` $'
check "the gate G2 and its nine jobs named" "$OUT" '^Jobsatz G2 \(`--gate G2`\)\. Voraussetzungen'
check "a phase2 threshold with its G2 budget" "$OUT" '^\| `http_req_duration\{name:relation_search\}` \| `p\(95\)<1500` \| 310 ms \| 310 ms \| 310 ms \| 310 ms \| ja \|$'
check "a threshold of a write job" "$OUT" '^\| `import_jobs_succeeded` \| `count==4` \| 4 \| 4 \| 4 \| 4 \| ja \|$'
check "a threshold of the ingest beside a write job" "$OUT" '^\| `ingest_visible_delay` \(paralleler Ingest\) \| `max<30000` \| 2100 ms \| 2100 ms \| 2100 ms \| 2100 ms \| ja \|$'
check "the same metric in the write job and the ingest beside it" "$(grep -c '^| `http_req_failed` (paralleler Ingest) | `rate==0` |' <<<"$OUT")" '^3$'
check "the verdict of a write job counts the ingest thresholds" "$(sed -n '/^## Job `template`/,/^Urteil/p' <<<"$OUT" | tail -n 1)" '^Urteil: gehalten \(Schwellen im Median: 6 von 6 gehalten; rot in 0 von 3 Läufen\)\.$'
check "a column per run" "$OUT" '^\| Metrik \| Schwelle \| 1001 \| 1002 \| 1003 \| Median \| gehalten \|$'
check "a p95 threshold: the p95 per run and their median, the G2 budget for the recorded G1 bound" "$OUT" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<1500` \| 89\.4 ms \| 101\.2 ms \| 95 ms \| 95 ms \| ja \|$'
check "the budget set named" "$OUT" '^Latenz- und Dauerbudgets \(p95\): Satz G2 aus `tests/load/budgets\.json` \(E-146\)'
check "a p95 threshold without a budget keeps its bound" "$OUT" '^\| `http_req_duration\{name:ingest\}` \| `p\(95\)<1000` \| 240 ms \| 240 ms \| 240 ms \| 240 ms \| ja \|$'
check "an unchanged budget of G1 in G2" "$OUT" '^\| `http_req_duration\{name:detail\}` \| `p\(95\)<300` \| 120\.1 ms \| 130 ms \| 110 ms \| 120\.1 ms \| ja \|$'
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
sed -i 's/^| Jobs | .*/| Jobs | api, ingest-spool, ingest-rediscover, nfr01-volume, phase2, import, template, computed |/' "$J/1302/ergebnis.md"
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
check "scenarios=api: the commits and the eight missing jobs named" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe\): nicht bewertbar: verschiedene Server-Commits .*; Job `ingest` fehlt in Lauf 1401, 1402, 1403; Job `ingest-spool` fehlt in Lauf 1401, 1402, 1403; Job `ingest-rediscover` fehlt in Lauf 1401, 1402, 1403; Job `nfr01-volume` fehlt in Lauf 1401, 1402, 1403; Job `phase2` fehlt in Lauf 1401, 1402, 1403; Job `import` fehlt in Lauf 1401, 1402, 1403; Job `template` fehlt in Lauf 1401, 1402, 1403; Job `computed` fehlt in Lauf 1401, 1402, 1403$'
evaluate "--partial with three server commits: exit 1" 1 --partial "$P/1401" "$P/1402" "$P/1403"
check "--partial with three server commits: not evaluable" "$(tail -n 1 <<<"$OUT")" '^Teilbewertung nach E-58 \(Median über drei Läufe, kein G1-Nachweis\): nicht bewertbar: verschiedene Server-Commits'
refute "--partial: the missing jobs are not named" "$OUT" 'fehlt in Lauf'

# The gates (WP-327). Runs of G1 (the five jobs, before WP-822) are no G2
# evidence: the four jobs of phase 2 are missing.
O="$TMP_ROOT/g1-runs"
for id in 1501 1502 1503; do g1 "$O/$id"; done
evaluate "G1 runs for G2: exit 1" 1 "$O/1501" "$O/1502" "$O/1503"
check "G1 runs for G2: the four phase-2 jobs named" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe\): nicht bewertbar: Job `phase2` fehlt in Lauf 1501, 1502, 1503; Job `import` fehlt in Lauf 1501, 1502, 1503; Job `template` fehlt in Lauf 1501, 1502, 1503; Job `computed` fehlt in Lauf 1501, 1502, 1503$'
check "G1 runs for G2: the medians of the five jobs still reported" "$(grep -c '^Urteil: gehalten' <<<"$OUT")" '^5$'
# --gate G1 keeps them evaluable (E-58: the evaluation is reproducible).
evaluate "G1 runs with --gate G1: exit 0" 0 --gate G1 "$O/1501" "$O/1502" "$O/1503"
check "--gate G1: the job set G1 in the last line" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe, Jobsatz G1\): alle Budgets gehalten$'
check "--gate G1: the five jobs named" "$OUT" '^drei Läufen und in jedem Lauf jeder der 5 Jobs `api`, `ingest`, `ingest-spool`, `ingest-rediscover`, `nfr01-volume` \('
check "--gate G1: the five jobs evaluated" "$(grep '^## Job' <<<"$OUT" | tr '\n' ' ')" '^## Job `api` ## Job `ingest` ## Job `ingest-spool` ## Job `ingest-rediscover` ## Job `nfr01-volume` $'
refute "--gate G1: never the line of G2" "$OUT" '^Bewertung nach E-58 \(Median über drei Läufe\):'
# --gate G1 on complete G2 runs: the jobs of phase 2 are evaluated as well.
evaluate "G2 runs with --gate G1: exit 0" 0 --gate G1 "$H/1001" "$H/1002" "$H/1003"
check "--gate G1 on G2 runs: every archived job evaluated" "$(grep -c '^## Job' <<<"$OUT")" '^9$'
# A job of G1 missing in a run: not evaluable with --gate G1 too.
rm -r "$O/1502/ingest-spool"
evaluate "--gate G1 with a G1 job missing: exit 1" 1 --gate G1 "$O/1501" "$O/1502" "$O/1503"
check "--gate G1 with a G1 job missing: named" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe, Jobsatz G1\): nicht bewertbar: Job `ingest-spool` fehlt in Lauf 1502$'
# --gate G2 is the default.
evaluate "--gate G2 like the default: exit 0" 0 --gate G2 "$H/1001" "$H/1002" "$H/1003"
check "--gate G2: the evidence line" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe\): alle Budgets gehalten$'

# The jobs of phase 2: a budget of phase2 missed in two runs.
Q="$TMP_ROOT/phase2-missed"
for id in 1601 1602 1603; do full "$Q/$id"; done
result "$Q/1601" phase2 failure && phase2 "$Q/1601" 120 1640 9000
result "$Q/1602" phase2 success && phase2 "$Q/1602" 130 450 9500
result "$Q/1603" phase2 failure && phase2 "$Q/1603" 110 1720 8800
evaluate "a phase2 budget missed in the median: exit 1" 1 "$Q/1601" "$Q/1602" "$Q/1603"
check "phase2: the missed p95 and its median" "$OUT" '^\| `http_req_duration\{name:relation_search\}` \| `p\(95\)<1500` \| 1640 ms \| 450 ms \| 1720 ms \| 1640 ms \| \*\*nein\*\* \|$'
check "phase2: the overall line names the budget and the red runs" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe\): verfehlt: `phase2` `http_req_duration\{name:relation_search\}` `p\(95\)<1500`; `phase2` rot in 2 von 3 Läufen$'

# The ingest beside a write job (WP-823): NFR-03 missed in two runs while
# the write job itself holds its thresholds.
I="$TMP_ROOT/ingest-missed"
for id in 1701 1702 1703; do full "$I/$id"; done
beside "$I/1701" import 41200 0
beside "$I/1702" import 12800 0
beside "$I/1703" import 35900 3
result "$I/1701" import failure
result "$I/1703" import failure
evaluate "an ingest budget of a write job missed in the median: exit 1" 1 "$I/1701" "$I/1702" "$I/1703"
check "the ingest delay beside import: per run and the median" "$OUT" '^\| `ingest_visible_delay` \(paralleler Ingest\) \| `max<30000` \| 41200 ms \| 12800 ms \| 35900 ms \| 35900 ms \| \*\*nein\*\* \|$'
check "the missing records beside import held in the median" "$OUT" '^\| `ingest_records_missing` \(paralleler Ingest\) \| `count==0` \| 0 \| 0 \| 3 \| 0 \| ja \|$'
check "the write job's own threshold held" "$OUT" '^\| `import_jobs_succeeded` \| `count==4` \| 4 \| 4 \| 4 \| 4 \| ja \|$'
check "the overall line names the ingest of the write job" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe\): verfehlt: `import` paralleler Ingest `ingest_visible_delay` `max<30000`; `import` rot in 2 von 3 Läufen$'
refute "the other write jobs are not named" "$(tail -n 1 <<<"$OUT")" '`template`|`computed`'

# An ingest summary missing in one run: the value is missed there, the
# median is the worse of the other two and holds.
K="$TMP_ROOT/ingest-once"
for id in 1801 1802 1803; do full "$K/$id"; done
rm "$K/1802/computed/computed-ingest.json"
beside "$K/1803" computed 4400 0
evaluate "an ingest summary missing in one run, the median held: exit 0" 0 "$K/1801" "$K/1802" "$K/1803"
check "the missing ingest value in its run" "$(sed -n '/^## Job `computed`/,/^Urteil/p' <<<"$OUT")" '^\| `ingest_visible_delay` \(paralleler Ingest\) \| `max<30000` \| 2100 ms \| – \| 4400 ms \| 4400 ms \| ja \|$'

# A write job without any ingest summary misses its budgets: NFR-03 beside
# it is not measured.
W="$TMP_ROOT/no-ingest"
for id in 1901 1902 1903; do
	full "$W/$id"
	rm "$W/$id/template/template-ingest.json"
done
evaluate "a write job without ingest summary: exit 1" 1 "$W/1901" "$W/1902" "$W/1903"
check "no ingest summary: shown in the table" "$OUT" '^\| Paralleler Ingest \| – \| fehlt \| fehlt \| fehlt \| – \| \*\*nein\*\* \|$'
check "no ingest summary: the verdict" "$(sed -n '/^## Job `template`/,/^Urteil/p' <<<"$OUT" | tail -n 1)" '^Urteil: \*\*verfehlt\*\* \(Schwellen im Median: 3 von 3 gehalten, keine Zusammenfassung `template-ingest\.json` des parallelen Ingest; rot in 0 von 3 Läufen\)\.$'
check "no ingest summary: the overall line" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe\): verfehlt: `template` ohne Zusammenfassung des parallelen Ingest \(`template-ingest\.json`\)$'

# The budget sets (E-146): tests/load/budgets.json holds G1 (NFR-02) and G2
# with the same keys; G1 stays the budgets of NFR-02.
BUDGETS=tests/load/budgets.json
check "budgets.json: G1 and G2 with the same metrics" "$(jq -r '(.gates.G1 | keys) == (.gates.G2 | keys)' "$BUDGETS")" '^true$'
check "budgets.json: every budget a positive number" "$(jq -r '[.gates[][]] | all(type == "number" and . > 0)' "$BUDGETS")" '^true$'
check "budgets.json: G2 never below G1" "$(jq -r '.gates.G1 as $g1 | .gates.G2 | to_entries | all(.value >= $g1[.key])' "$BUDGETS")" '^true$'
check "budgets.json: G1 is NFR-02 (detail and rack 300 ms, topology 3 s, impact and blast radius 2 s, bulk 30 s, else 500 ms)" \
	"$(jq -r '.gates.G1 | to_entries | all(.value == (
		if (.key | test("name:(detail|rack)}")) then 300 elif (.key | test("name:topology}")) then 3000
		elif (.key | test("name:(impact|impact_full|blast_radius)}")) then 2000 elif (.key | test("^bulk_")) then 30000 else 500 end))' "$BUDGETS")" '^true$'
check "budgets.json: the 27 budgets of api.js and phase2.js" "$(jq -r '.gates.G1 | length' "$BUDGETS")" '^27$'
# A run recorded with the G2 bounds (load.yml since E-146), two with the G1
# bounds: one row per threshold, with the budget of the gate; a list p95
# between G1 and G2 is held with --gate G2 and missed with --gate G1.
V="$TMP_ROOT/budget-sets"
for id in 2101 2102 2103; do full "$V/$id"; done
result "$V/2101" api success && api "$V/2101" 700 110 870.5 1
result "$V/2102" api success && api "$V/2102" 650 110 870.5 1
result "$V/2103" api success && api "$V/2103" 720 110 870.5 1
jq '.metrics["http_req_duration{name:list}"].thresholds = {"p(95)<1500": false}' "$V/2102/api/api.json" >"$TMP_ROOT/g2.json" && mv "$TMP_ROOT/g2.json" "$V/2102/api/api.json"
evaluate "a list p95 between G1 and G2 with --gate G2: exit 0" 0 --gate G2 "$V/2101" "$V/2102" "$V/2103"
check "--gate G2: one row with the G2 budget for runs of G1 and G2" "$(grep -c '^| `http_req_duration{name:list}` |' <<<"$(sed -n '/^## Job `api`/,/^Urteil/p' <<<"$OUT")")" '^1$'
check "--gate G2: the list held with p(95)<1500" "$OUT" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<1500` \| 700 ms \| 650 ms \| 720 ms \| 700 ms \| ja \|$'
evaluate "the same runs with --gate G1: exit 1" 1 --gate G1 "$V/2101" "$V/2102" "$V/2103"
check "--gate G1: the budget set G1 named" "$OUT" '^Latenz- und Dauerbudgets \(p95\): Satz G1 aus `tests/load/budgets\.json`'
check "--gate G1: the list missed with p(95)<500" "$OUT" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<500` \| 700 ms \| 650 ms \| 720 ms \| 700 ms \| \*\*nein\*\* \|$'
check "--gate G1: a phase2 budget of G1" "$OUT" '^\| `http_req_duration\{name:relation_search\}` \| `p\(95\)<500` \| 310 ms'
check "--gate G1: the overall line names the G1 budget" "$(tail -n 1 <<<"$OUT")" '^Bewertung nach E-58 \(Median über drei Läufe, Jobsatz G1\): verfehlt: `api` `http_req_duration\{name:list\}` `p\(95\)<500`$'

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
check "the missed value in its run, the median within" "$OUT" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<1500` \| 89\.4 ms \| 504 ms \| 120 ms \| 120 ms \| ja \|$'
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
result "$X/5001" api failure && api "$X/5001" 89.44 120.1 3100 0.98
result "$X/5002" api success && api "$X/5002" 101.2 130 1900 0.995
result "$X/5003" api failure && api "$X/5003" 95 110 3300 0.97
result "$X/5001" ingest-spool failure && spool "$X/5001" 150 15100 0
result "$X/5002" ingest-spool success && spool "$X/5002" 250 16000 0
result "$X/5003" ingest-spool failure && spool "$X/5003" 180 17000 0
evaluate "thresholds missed in two runs: exit 1" 1 --partial "$X/5001" "$X/5002" "$X/5003"
check "a p95 missed in the median" "$OUT" '^\| `http_req_duration\{name:impact\}` \| `p\(95\)<3000` \| 3100 ms \| 1900 ms \| 3300 ms \| 3100 ms \| \*\*nein\*\* \|$'
check "a rate missed in the median" "$OUT" '^\| `checks` \| `rate>0\.99` \| 0\.98 \| 0\.995 \| 0\.97 \| 0\.98 \| \*\*nein\*\* \|$'
check "a gauge missed in the median" "$OUT" '^\| `ingest_drain_records_per_s` \| `value>=200` \| 150 Records/s \| 250 Records/s \| 180 Records/s \| 180 Records/s \| \*\*nein\*\* \|$'
check "a threshold held next to them" "$OUT" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<1500` \| 89\.4 ms \| 101\.2 ms \| 95 ms \| 95 ms \| ja \|$'
check "red in two runs fails the job" "$OUT" '^\| Status des Jobs \| – \| \*\*rot\*\* \| grün \| \*\*rot\*\* \| rot in 2 von 3 \| \*\*nein\*\* \|$'
check "the verdict names both" "$OUT" '^Urteil: \*\*verfehlt\*\* \(Schwellen im Median: 2 von 4 gehalten; rot in 2 von 3 Läufen\)\.$'
check "the overall line names every miss" "$(tail -n 1 <<<"$OUT")" '^Teilbewertung nach E-58 \(Median über drei Läufe, kein G1-Nachweis\): verfehlt: `api` `checks` `rate>0\.99`; `api` `http_req_duration\{name:impact\}` `p\(95\)<3000`; `api` rot in 2 von 3 Läufen; `ingest-spool` `ingest_drain_records_per_s` `value>=200`; `ingest-spool` rot in 2 von 3 Läufen$'
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
check "a job in one run has no median" "$OUT" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<1500` \| fehlt \| fehlt \| 300 ms \| – \| \*\*nein\*\* \|$'
check "the overall line names both jobs" "$(tail -n 1 <<<"$OUT")" '^Teilbewertung nach E-58 \(Median über drei Läufe, kein G1-Nachweis\): verfehlt: .*`ingest-rediscover` rot in 2 von 3 Läufen; .*`nfr01-volume` rot in 2 von 3 Läufen$'
refute "the green job is not named" "$(tail -n 1 <<<"$OUT")" '`api`'

# Wrong arguments: exactly three run archives, never a median of two or four.
evaluate "no argument" 2
check "usage" "$(cat "$TMP_ROOT/err")" '^usage: .*evaluate-runs\.sh \[--partial \| --gate G1\|G2\] <archive-1> <archive-2> <archive-3>$'
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
B2="$TMP_ROOT/broken-ingest"
for id in 7101 7102 7103; do full "$B2/$id"; done
printf 'not json\n' >"$B2/7102/import/import-ingest.json"
evaluate "a broken ingest summary" 2 "$B2/7101" "$B2/7102" "$B2/7103"
check "the broken ingest summary is named" "$(cat "$TMP_ROOT/err")" 'import-ingest\.json is not a k6 summary'
evaluate "an unknown gate" 2 --gate G3 "$H/1001" "$H/1002" "$H/1003"
check "the unknown gate is named" "$(cat "$TMP_ROOT/err")" "unknown gate 'G3'"
evaluate "--gate without a value" 2 --gate
evaluate "--gate twice" 2 --gate G1 --gate G2 "$H/1001" "$H/1002" "$H/1003"
evaluate "--partial with --gate" 2 --partial --gate G1 "$H/1001" "$H/1002" "$H/1003"
E="$TMP_ROOT/empty"
for id in 8001 8002 8003; do run "$E/$id"; done
evaluate "no job in the archives" 2 "$E/8001" "$E/8002" "$E/8003"

echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
