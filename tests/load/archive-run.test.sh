#!/usr/bin/env bash
#
# archive-run.test.sh — tests for tests/load/archive-run.sh, the archive of
# a load.yml run for docs/acceptance/artefakte/lasttest/<run-id>/ (NFR-10,
# E-58, WP-439): the archive keeps the k6 summaries, run records, outcomes
# and reports of every job but no logs or binary profiles; ergebnis.md
# names the run, every k6 threshold with its measured value and result
# (time, rate, counter and gauge thresholds), the measures without a
# threshold, the failed requests by name and status (F-388), a red and a
# missing job; SHA256SUMS verifies; a complete green run says so; wrong
# arguments fail before anything is written.
#
# Usage: ./tests/load/archive-run.test.sh
# shellcheck disable=SC2016 # the backticks of the Markdown code spans are literal
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
SCRIPT="$(pwd)/tests/load/archive-run.sh"

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

# status NAME WANT CMD...: the command exits with WANT.
status() {
	local name=$1 want=$2 got
	shift 2
	"$@" >"$TMP_ROOT/out" 2>&1
	got=$?
	if [ "$got" -eq "$want" ]; then
		PASS=$((PASS + 1))
		echo "ok   $name"
	else
		FAIL=$((FAIL + 1))
		echo "FAIL $name: exit $got, want $want"
		indent "$(cat "$TMP_ROOT/out")"
	fi
}

# job DIR NAME STATUS: the files a load job leaves in its artifact.
job() {
	local d="$1/load-results-$2"
	mkdir -p "$d"
	printf 'job: %s\nstatus: %s\nseed: success\nvolumes: success\nk6: %s\nintegrity: success\n' "$2" "$3" "$3" >"$d/result-$2.txt"
	cat >"$d/run-$2.txt" <<EOF
run commit: 0f6b3220000000000000000000000000000000aa
server, seed and migrations: 0f6b3220000000000000000000000000000000aa
script: ${4:-ingest.js}
k6 scenario: ${5:-load}
objects: 10000
duration: 2m
seed options: ${6:-none}
ingest lanes: 4
rate limit rpm: 1000000
rate limit org rpm: 1000000
k6: k6 v0.54.0
Architecture:                         x86_64
CPU(s):                               4
Model name:                           AMD EPYC 7763 64-Core Processor
MemTotal:       16373108 kB
EOF
	cat >"$d/$2.txt" <<EOF
     scenarios: (100.00%) 2 scenarios, 7 max VUs, 2m30s max duration (incl. graceful stop):


running (0m01.0s), 7/7 VUs, 3 complete and 0 interrupted iterations
list           [   1% ] 5 VUs  0m01.0s/2m0s
impact_full    [   1% ] 2 VUs  0m01.0s/2m0s

running (2m00.5s), 0/7 VUs, 900 complete and 0 interrupted iterations
list         ✓ [ 100% ] 5 VUs  2m0s
impact_full  ✗ [ 100% ] 2 VUs  2m0s

     ✓ checks.........................: 100.00% 16889 out of 16889 ($2)
EOF
	printf 'orphan|0\nduplicate|0\n' >"$d/integrity-$2.txt"
	printf 'server log\n' >"$d/server-$2.log"
	printf 'postgres log\n' >"$d/postgres-$2.log"
	printf 'binary' >"$d/cpu-$2.pprof"
}

ART="$TMP_ROOT/artifacts"
job "$ART" api success api.js default
cat >"$ART/load-results-api/api.json" <<'EOF'
{"metrics": {
  "http_req_duration{name:list}": {"avg": 40.1, "p(95)": 89.44, "max": 300, "count": 900, "thresholds": {"p(95)<500": false}},
  "http_req_duration{name:impact}": {"avg": 400, "p(95)": 704.2, "max": 900, "count": 120, "thresholds": {"p(95)<2000": false}},
  "bulk_500_duration": {"avg": 5000, "p(95)": 8400, "max": 8400, "count": 3, "thresholds": {"p(95)<30000": false}},
  "checks": {"passes": 16889, "fails": 0, "value": 1, "thresholds": {"rate>0.99": false}},
  "http_reqs": {"count": 16889, "rate": 140.7},
  "http_req_failed": {"passes": 0, "fails": 16889, "value": 0}
}}
EOF
printf 'demo CIs|10000\norganizations|3\n' >"$ART/load-results-api/volumes-api.txt"
printf 'seed seconds: 17\n' >"$ART/load-results-api/seed-api.txt"
cat >"$ART/load-results-api/failed-api.txt" <<'EOF'
Fehlgeschlagene Anfragen: 4

| Anfrage (`name`) | Status | Anzahl |
|---|---:|---:|
| `bulk` | 429 | 3 |
| `list` | 0 | 1 |

Erstes Beispiel je Anfrage und Status:
EOF
job "$ART" ingest-spool success
printf 'Fehlgeschlagene Anfragen: keine.\n' >"$ART/load-results-ingest-spool/failed-ingest-spool.txt"
cat >"$ART/load-results-ingest-spool/ingest-spool.json" <<'EOF'
{"metrics": {
  "ingest_drain_records_per_s": {"value": 798.04, "min": 798.04, "max": 798.04, "thresholds": {"value>=200": false}},
  "ingest_drain_inclusive_records_per_s": {"value": 663.21, "min": 663.21, "max": 663.21},
  "ingest_spool_flush": {"avg": 15100, "p(95)": 15100, "max": 15100, "count": 1, "thresholds": {"max<300000": false}},
  "ingest_records_missing": {"count": 0, "rate": 0, "thresholds": {"count==0": false}},
  "ingest_records_accepted": {"count": 10000, "rate": 120.5},
  "http_req_failed{name:spool}": {"passes": 0, "fails": 157, "value": 0, "thresholds": {"rate==0": false}},
  "checks": {"passes": 160, "fails": 0, "value": 1, "thresholds": {"rate>0.99": false}},
  "http_reqs": {"count": 170, "rate": 2.1},
  "http_req_failed": {"passes": 0, "fails": 170, "value": 0}
}}
EOF
job "$ART" ingest-rediscover failure ingest.js rediscover
cat >"$ART/load-results-ingest-rediscover/ingest-rediscover.json" <<'EOF'
{"metrics": {
  "ingest_visible_delay": {"avg": 30000, "p(95)": 45100, "max": 49612.3, "count": 11, "thresholds": {"max<30000": true}},
  "ingest_new_cis": {"count": 0, "rate": 0, "thresholds": {"count==0": false}},
  "ingest_drain_records_per_s": {"value": 77.61, "min": 77.61, "max": 77.61},
  "checks": {"passes": 460, "fails": 0, "value": 1, "thresholds": {"rate>0.99": false}},
  "http_reqs": {"count": 470, "rate": 5},
  "http_req_failed": {"passes": 0, "fails": 470, "value": 0}
}}
EOF

export GITHUB_RUN_ID=40257485919 GITHUB_RUN_ATTEMPT=1 GITHUB_SHA=0f6b3220000000000000000000000000000000aa
export GITHUB_REF_NAME=main GITHUB_EVENT_NAME=workflow_dispatch SERVER_SHA=0f6b3220000000000000000000000000000000aa
export RUN_URL=https://github.com/DataHub-Chiemgau/Reticora-CMDB/actions/runs/40257485919
export INPUT_OBJECTS=10000 INPUT_DURATION=2m INPUT_SCENARIOS=all INPUT_SERVER_REF=

OUT="$TMP_ROOT/lasttest/40257485919"
log="$("$SCRIPT" "$ART" "$OUT" api,ingest-spool,ingest-rediscover,nfr01-volume 2>&1)"
check "a red and a missing job still give an archive" "$log" 'missing: 1, not green: 1\)$'
res="$(cat "$OUT/ergebnis.md" 2>/dev/null)"
check "the run with its link and attempt" "$res" '^\| Lauf \| https://github.com/.*/runs/40257485919 \(Versuch 1, Ereignis workflow_dispatch, Branch main\) \|$'
check "the commits" "$res" '^\| Server, Seed und Migrationen \| `0f6b3220+aa` \|$'
check "the inputs" "$res" '^\| Eingaben \| objects 10000, duration 2m, scenarios all, server_ref – \|$'
check "a missing job makes the run incomplete" "$res" '^\| Vollständig \| \*\*nein\*\*, ohne Artefakt: nfr01-volume \|$'
check "the red and the missing job" "$res" '^\| Ergebnis \| \*\*nicht grün\*\*: ingest-rediscover, nfr01-volume \|$'
check "a time threshold in ms" "$res" '^\| `http_req_duration\{name:list\}` \| `p\(95\)<500` \| 89\.4 ms \| eingehalten \|$'
check "a custom trend" "$res" '^\| `bulk_500_duration` \| `p\(95\)<30000` \| 8400 ms \| eingehalten \|$'
check "a crossed threshold" "$res" '^\| `ingest_visible_delay` \| `max<30000` \| 49612\.3 ms \| \*\*verfehlt\*\* \|$'
check "a gauge threshold" "$res" '^\| `ingest_drain_records_per_s` \| `value>=200` \| 798 Records/s \| eingehalten \|$'
check "a rate threshold" "$res" '^\| `http_req_failed\{name:spool\}` \| `rate==0` \| 0 \| eingehalten \|$'
check "a counter threshold" "$res" '^\| `ingest_records_missing` \| `count==0` \| 0 \| eingehalten \|$'
check "a measure without threshold" "$res" '^\| `ingest_drain_inclusive_records_per_s` \| value 663\.2 \|$'
check "the drain of a red job without threshold" "$res" '^\| `ingest_drain_records_per_s` \| value 77\.6 \|$'
check "the checks" "$res" '^\| `checks` \| 16889 bestanden, 0 fehlgeschlagen \|$'
check "the outcome of a job" "$res" '^\| Status \| failure \(Seed success, Volumina success, k6 failure, Integritätsprüfung success\) \|$'
check "host" "$res" '^\| CPU \| AMD EPYC 7763 64-Core Processor, 4 CPUs \|$'
check "memory" "$res" '^\| RAM \| 16373108 kB \|$'
check "configuration" "$res" '^\| Konfiguration \| Skript api\.js \(Szenario default\), objects 10000, duration 2m, Seed-Optionen none, Spuren 4, Rate-Limit 1000000/min und 1000000/min je Org, k6 k6 v0\.54\.0 \|$'
check "seeded volumes" "$res" '^\| Datenbestand \| demo CIs 10000, organizations 3 \|$'
check "seed duration" "$res" '^\| Seed-Dauer \| 17 s \|$'
check "integrity check" "$res" '^\| Integritätsprüfung \| 0 Verletzungen in 2 Prüfungen \|$'
check "failed requests by name and status" "$res" '^\| Fehlgeschlagene Anfragen \| 4 \(`bulk` 429: 3, `list` 0: 1\) \|$'
check "no failed request" "$res" '^\| Fehlgeschlagene Anfragen \| keine\. \|$'
check "failed requests archived" "$(cd "$OUT" && find . -type f | sort)" '^\./api/failed-api\.txt$'
check "the missing job" "$res" '^Kein Artefakt `load-results-nfr01-volume`\.$'
files="$(cd "$OUT" && find . -type f | sort)"
check "k6 summary archived" "$files" '^\./api/api\.json$'
check "k6 output archived" "$files" '^\./ingest-spool/ingest-spool\.txt$'
k6out="$(cat "$OUT/api/api.txt" 2>/dev/null)"
check "the k6 output keeps its summary" "$k6out" '^     ✓ checks\.+: 100\.00% 16889 out of 16889 \(api\)$'
check "and its scenarios" "$k6out" '^     scenarios: \(100\.00%\) 2 scenarios'
refute "but not its progress lines" "$k6out" '^running \(|\[ +[0-9]+% \]'
refute "nor two blank lines in a row" "$(uniq -c <<<"$k6out")" '^ +[2-9] $'
check "run record archived" "$files" '^\./ingest-rediscover/run-ingest-rediscover\.txt$'
check "outcome archived" "$files" '^\./api/result-api\.txt$'
check "seed duration and volumes archived" "$files" '^\./api/seed-api\.txt$'
refute "no server or PostgreSQL log, no binary profile" "$files" '\.(log|pprof)$'
refute "no directory of the missing job" "$files" 'nfr01-volume'
check "the checksums verify" "$(cd "$OUT" && sha256sum -c SHA256SUMS 2>&1)" '^api/api\.json: OK$'
check "every archived file has a checksum" "$(wc -l <"$OUT/SHA256SUMS")" "^$(($(wc -l <<<"$files") - 1))\$"

GREEN="$TMP_ROOT/green/1"
"$SCRIPT" "$ART" "$GREEN" api,ingest-spool >/dev/null 2>&1
res="$(cat "$GREEN/ergebnis.md" 2>/dev/null)"
check "a complete run" "$res" '^\| Vollständig \| ja: jeder Job hat ein Artefakt \|$'
check "a green run" "$res" '^\| Ergebnis \| alle Jobs grün \|$'

status "usage" 2 "$SCRIPT" "$ART" "$TMP_ROOT/x"
status "no artifact directory" 2 "$SCRIPT" "$TMP_ROOT/none" "$TMP_ROOT/y" api
status "a non-empty archive" 2 "$SCRIPT" "$ART" "$OUT" api
status "a job name with a path" 2 "$SCRIPT" "$ART" "$TMP_ROOT/z" "api,../x"
refute "nothing written on wrong arguments" "$(ls "$TMP_ROOT")" '^(y|z)$'

echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
