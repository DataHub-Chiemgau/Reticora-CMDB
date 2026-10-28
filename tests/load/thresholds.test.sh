#!/usr/bin/env bash
#
# thresholds.test.sh — the failure thresholds of the load scripts (F-388,
# WP-659, NFR-10): a run with a failed request or a bulk job that did not
# succeed is red, and the job summary lists the failed requests by status.
#
#   - k6 inspect: api.js (jobs api and nfr01-volume) and ingest.js with
#     SCENARIO load, spool and rediscover have http_req_failed rate==0 for
#     the run and for every scenario of it; api.js also
#     bulk_jobs_succeeded count==3 and checks{scenario:bulk} rate==1.
#   - k6 run of api.js (2 s) against a stub of the API (python3): every
#     answer as expected → no threshold crossed (the latency budgets aside,
#     which the stub on a busy host may miss), three bulk jobs with their
#     duration, no failed request; every POST /cis/bulk 429 (as in load
#     run 40713089267) → exit 99, http_req_failed{scenario:bulk} crossed,
#     no bulk_500_duration sample, tests/load/failed-requests.sh lists
#     `bulk` 429 three times; bulk jobs that end "failed" → exit 99 and no
#     duration; one 503 of the rack layout among some hundred requests
#     (under 1 %, which checks rate>0.99 lets pass) → exit 99.
#   - load.yml writes the k6 console to console-<job>.log, lists the failed
#     requests in the job summary and keeps them as failed-<job>.txt for
#     the archive (tests/load/archive-run.sh).
#
# Needs k6 (K6, default k6; load.yml pins 0.54.0), python3 and jq.
#
# Usage: ./tests/load/thresholds.test.sh
# shellcheck disable=SC2016 # jq programs, Markdown code spans and literal $ in patterns
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
K6="${K6:-k6}"
export K6_NO_USAGE_REPORT=true

TMP_ROOT="$(mktemp -d)"
STUB_PID=""
cleanup() {
	if [ -n "$STUB_PID" ]; then
		kill "$STUB_PID" 2>/dev/null
		wait "$STUB_PID" 2>/dev/null
	fi
	rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

PASS=0
FAIL=0

indent() { while IFS= read -r line; do printf '     %s\n' "$line"; done <<<"$1"; }

ok() {
	PASS=$((PASS + 1))
	echo "ok   $1"
}

bad() {
	FAIL=$((FAIL + 1))
	echo "FAIL $1"
	if [ -n "${2:-}" ]; then
		indent "$2"
	fi
}

# expect NAME JSON JQ: the jq filter is true on the JSON.
expect() {
	if jq -e "$3" <<<"$2" >/dev/null 2>&1; then
		ok "$1"
	else
		bad "$1: $3" "$(jq -c "$4" <<<"$2" 2>/dev/null | head -c 2000)"
	fi
}

for tool in "$K6" python3 jq; do
	if ! command -v "$tool" >/dev/null; then
		echo "FAIL $tool not found"
		exit 1
	fi
done

# 1. The thresholds as k6 reads them.
inspect() {
	"$K6" inspect "$@" 2>"$TMP_ROOT/inspect.err" || cat "$TMP_ROOT/inspect.err" >&2
}
# The rule: http_req_failed rate==0 for the run and for every scenario.
FAILED_RULE='(.thresholds.http_req_failed // [] | any(. == "rate==0"))
	and (.thresholds as $t | .scenarios | keys | all(. as $s | $t["http_req_failed{scenario:\($s)}"] // [] | any(. == "rate==0")))'
for args in "tests/load/api.js" "-e SCENARIO=load tests/load/ingest.js" "-e SCENARIO=spool tests/load/ingest.js" \
	"-e SCENARIO=rediscover tests/load/ingest.js"; do
	# shellcheck disable=SC2086 # the arguments are split on purpose
	opts="$(inspect $args)"
	expect "k6 inspect $args: http_req_failed rate==0 for the run and every scenario" "$opts" \
		"$FAILED_RULE" '{thresholds: (.thresholds | with_entries(select(.key | test("failed")))), scenarios: (.scenarios | keys)}'
done
opts="$(inspect tests/load/api.js)"
expect "api.js: every bulk job succeeds (bulk_jobs_succeeded count==3, checks{scenario:bulk} rate==1)" "$opts" \
	'(.thresholds.bulk_jobs_succeeded // [] | any(. == "count==3")) and (.thresholds["checks{scenario:bulk}"] // [] | any(. == "rate==1"))
	and .scenarios.bulk.iterations == 3' '.thresholds'

# 2. api.js against a stub of the API. MODE: ok, bulk429, bulkfailed,
# rack503 (the first rack layout 503).
cat >"$TMP_ROOT/stub.py" <<'EOF'
import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

MODE = os.environ["MODE"]
lock = threading.Lock()
state = {"rack": 0, "jobs": 0}


class Stub(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    disable_nagle_algorithm = True

    def log_message(self, *args):
        pass

    def answer(self, status, body):
        # Compact like the server: api.js reads "count":<n> from the body.
        raw = json.dumps(body, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        url = urlparse(self.path)
        q = parse_qs(url.query)
        parts = url.path.removeprefix("/api/v1/").split("/")
        if parts == ["cis"]:
            offset = int(q.get("offset", ["0"])[0])
            limit = min(int(q.get("limit", ["50"])[0]), 200)
            return self.answer(200, {"data": [{"id": "ci-%d" % (offset + i)} for i in range(limit)], "total": 10})
        if parts == ["search"]:
            return self.answer(200, {"data": [{"title": q["q"][0]}]})
        if parts == ["racks"]:
            return self.answer(200, {"data": [{"id": "rack-1"}]})
        if parts[0] == "racks" and parts[-1] == "layout":
            with lock:
                state["rack"] += 1
                first = state["rack"] == 1
            if MODE == "rack503" and first:
                return self.answer(503, {"detail": "a required service is temporarily unavailable"})
            return self.answer(200, {"units": []})
        if parts[0] == "impact":
            return self.answer(200, {"count": 10, "impacted": []})
        if parts[0] == "jobs":
            return self.answer(200, {"status": "failed" if MODE == "bulkfailed" else "succeeded"})
        if parts[0] == "cis" and parts[-1] == "relationships":
            return self.answer(200, {"data": [{"id": "rel-1"}]})
        if parts[0] in ("cis", "topology"):
            return self.answer(200, {"id": parts[-1], "nodes": []})
        return self.answer(404, {"detail": "not found"})

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if urlparse(self.path).path == "/api/v1/cis/bulk":
            if MODE == "bulk429":
                return self.answer(429, {"detail": "too many authentication attempts, try again later"})
            with lock:
                state["jobs"] += 1
                job = state["jobs"]
            return self.answer(202, {"job_id": "job-%d" % job, "status": "queued"})
        return self.answer(404, {"detail": "not found"})


class Server(ThreadingHTTPServer):
    # Room for the connections of every VU at the start (default 5).
    request_queue_size = 128
    daemon_threads = True


server = Server(("127.0.0.1", 0), Stub)
with open(sys.argv[1], "w") as f:
    f.write(str(server.server_address[1]))
server.serve_forever()
EOF

# run MODE: api.js for 2 s against the stub in MODE; sets RUN_EXIT,
# SUMMARY (the summary export), CONSOLE (the console log) and FAILED (the
# output of failed-requests.sh).
run() {
	local dir="$TMP_ROOT/$1" port
	mkdir -p "$dir"
	MODE="$1" python3 -I "$TMP_ROOT/stub.py" "$dir/port" &
	STUB_PID=$!
	for _ in $(seq 50); do
		[ -s "$dir/port" ] && break
		sleep 0.1
	done
	port="$(cat "$dir/port" 2>/dev/null)"
	"$K6" run -q --no-usage-report -e BASE_URL="http://127.0.0.1:$port" -e API_KEY=stub -e DURATION=2s -e OBJECTS=10 \
		--console-output "$dir/console.log" --summary-export "$dir/summary.json" tests/load/api.js >"$dir/k6.txt" 2>&1
	RUN_EXIT=$?
	kill "$STUB_PID" 2>/dev/null
	wait "$STUB_PID" 2>/dev/null
	STUB_PID=""
	SUMMARY="$(cat "$dir/summary.json" 2>/dev/null || echo '{}')"
	CONSOLE="$(cat "$dir/console.log" 2>/dev/null)"
	if [ -x tests/load/failed-requests.sh ]; then
		FAILED="$(tests/load/failed-requests.sh "$dir/console.log" 2>&1)"
	else
		FAILED="tests/load/failed-requests.sh missing"
	fi
}

# exit_is NAME CODE: the k6 run of the case ended with CODE.
exit_is() {
	if [ "$RUN_EXIT" -eq "$2" ]; then
		ok "$1: k6 exit $2"
	else
		bad "$1: k6 exit $RUN_EXIT, want $2" "$(grep -E 'thresholds|level=error' "$TMP_ROOT/$1/k6.txt" | head -5)"
	fi
}

# has NAME TEXT PATTERN: TEXT has a line matching the extended regex.
has() {
	if grep -Eq -- "$3" <<<"$2"; then
		ok "$1"
	else
		bad "$1: no line matches $3" "$(head -20 <<<"$2")"
	fi
}

# crossed: the thresholds the run crossed, apart from the latency budgets
# (http_req_duration), which the stub on a busy host may miss.
CROSSED='[.metrics | to_entries[] | select(.key | startswith("http_req_duration") | not)
	| .key as $m | .value.thresholds // {} | to_entries[] | select(.value) | "\($m) \(.key)"]'

run ok
if [ "$RUN_EXIT" -eq 0 ] || { [ "$RUN_EXIT" -eq 99 ] && jq -e "$CROSSED | length == 0" <<<"$SUMMARY" >/dev/null; }; then
	ok "ok: k6 exit $RUN_EXIT, no threshold crossed but the stub's latency"
else
	bad "ok: k6 exit $RUN_EXIT" "$(jq -c "$CROSSED" <<<"$SUMMARY")"
fi
expect "ok: three bulk jobs succeeded with their duration, no failed request" "$SUMMARY" \
	'.metrics.bulk_jobs_succeeded.count == 3 and .metrics.bulk_500_duration.max > 0 and .metrics.http_req_failed.value == 0' \
	'.metrics | {bulk_jobs_succeeded, bulk_500_duration, http_req_failed}'
has "ok: the job summary says no request failed" "$FAILED" '^Fehlgeschlagene Anfragen: keine\.$'

run bulk429
exit_is bulk429 99
expect "bulk429: http_req_failed of the run and of the scenario bulk crossed" "$SUMMARY" \
	'.metrics.http_req_failed.thresholds["rate==0"] == true and .metrics["http_req_failed{scenario:bulk}"].thresholds["rate==0"] == true' \
	'.metrics | with_entries(select(.key | test("failed")))'
expect "bulk429: no bulk job succeeded, the bulk check threshold crossed" "$SUMMARY" \
	'.metrics.bulk_jobs_succeeded.thresholds["count==3"] == true and .metrics["checks{scenario:bulk}"].thresholds["rate==1"] == true' \
	'.metrics | {bulk_jobs_succeeded, "checks{scenario:bulk}"}'
expect "bulk429: a refused bulk request records no bulk_500_duration" "$SUMMARY" \
	'(.metrics.bulk_500_duration.max // 0) == 0' '.metrics.bulk_500_duration'
has "bulk429: the job summary lists bulk 429 three times" "$FAILED" '^\| `bulk` \| 429 \| 3 \|$'
has "bulk429: the example names the answer" "$FAILED" '^failed request name=bulk status=429 POST /api/v1/cis/bulk .*too many authentication attempts'
has "bulk429: the console says which bulk job did not succeed" "$CONSOLE" 'bulk job 1 of 3 did not succeed: HTTP 429'

run bulkfailed
exit_is bulkfailed 99
expect "bulkfailed: no failed request, but the bulk jobs failed and record no duration" "$SUMMARY" \
	'.metrics.http_req_failed.value == 0 and .metrics.bulk_jobs_succeeded.thresholds["count==3"] == true
	and .metrics["checks{scenario:bulk}"].thresholds["rate==1"] == true and (.metrics.bulk_500_duration.max // 0) == 0' \
	'.metrics | {http_req_failed, bulk_jobs_succeeded, "checks{scenario:bulk}", bulk_500_duration}'
has "bulkfailed: the console names the job state" "$CONSOLE" 'bulk job 1 of 3 did not succeed: failed'

run rack503
exit_is rack503 99
expect "rack503: the one failed request of the run (under 1 %) crosses http_req_failed and http_req_failed{scenario:rack}" "$SUMMARY" \
	'.metrics.http_req_failed.passes == 1 and .metrics.http_req_failed.value < 0.01 and .metrics.http_req_failed.thresholds["rate==0"] == true
	and .metrics["http_req_failed{scenario:rack}"].thresholds["rate==0"] == true
	and .metrics["http_req_failed{scenario:list}"].thresholds["rate==0"] == false' \
	'.metrics | {http_req_failed, "http_req_failed{scenario:rack}", "http_req_failed{scenario:list}"}'
has "rack503: the job summary lists rack 503 once" "$FAILED" '^\| `rack` \| 503 \| 1 \|$'

# 3. load.yml: console log, failed requests in the job summary and in the
# archive.
WF="$(cat .github/workflows/load.yml)"
has "load.yml: k6 writes its console to console-<job>.log" "$WF" '--console-output "load-results/console-\$SCENARIO_NAME\.log"'
has "load.yml: the failed requests go to the job summary" "$WF" 'tests/load/failed-requests\.sh "load-results/console-\$SCENARIO_NAME\.log"'
has "load.yml: ... and to failed-<job>.txt" "$WF" 'load-results/failed-\$SCENARIO_NAME\.txt'
has "archive-run.sh keeps failed-<job>.txt" "$(cat tests/load/archive-run.sh)" 'failed-%s\.txt'
has "load.yml: the prepare job runs this test" "$WF" 'run: \./tests/load/thresholds\.test\.sh'

echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
