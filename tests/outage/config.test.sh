#!/usr/bin/env bash
#
# config.test.sh — the parts of the outage suite that need no installed
# stack (WP-449, OPS-06). Runs in the ci.yml job gate; the suite itself runs
# in .github/workflows/outage.yml.
#
#   - outage_smtp: tests/outage/run.sh simulates the SMTP outage instead of
#     reporting N/A (retest F-135, case moved from
#     tests/audit/ops-config.test.sh), with the delivery states of the
#     acceptance criterion in smtp.sh;
#   - outage_scenarios: every scenario of ALL_SCENARIOS has its case, the
#     dependency scenarios and smtp run the load of load.sh (50 API writes,
#     50 ingest records by default);
#   - outage_syntax / outage_shellcheck: bash -n and shellcheck -x of the
#     suite;
#   - outage_workflow: outage.yml applies the Mailpit overlay before the
#     installation of the source build, runs the suite with the seed, runs
#     the negative self-test that must fail with exactly the dropped record
#     lost, and uploads both protocols; the ci.yml job gate runs this file;
#   - outage_overlay: smtp.sh overlay on a copy of
#     deploy/docker-compose/docker-compose.override.yml keeps the build
#     sections and adds Mailpit and RETICORA_SMTP_* of the server; docker
#     compose config accepts the merged files;
#   - accounting_*: run.sh with load.sh against stubs (server answers,
#     database, NATS monitor; no Docker): accepted == persisted is decided
#     from the stored serial numbers, every count is in the protocol, and a
#     lost record (also OUTAGE_SELFTEST_DROP=1), a late record, an answer
#     other than 2xx/429/503, a non-empty ingest queue and an unpublished
#     outbox entry fail the scenario;
#   - smtp_scenario_*: smtp_outage against stubs: pending with attempts and
#     last_error, then sent and exactly one mail → PASS; two mails, no failed
#     attempt, never sent, no smtp check → FAIL;
#   - load_setup_db_url: load_setup hands cmd/seed the bootstrap password of
#     the installation's .env, also though the seed runs in backend/;
#   - collector_scenario_*: collector_outage (collector.sh, WP-587) against
#     a stub of the collector containers, the stack and the database: spool
#     rises during the outage of server and nats-edge, backpressure in the
#     heartbeat after the restart, every record ingested, loss counter 0 →
#     PASS; spool limit of one record (the self-test of outage.yml), no
#     backpressure, records lost on delivery, no collector image → FAIL;
#     outage.yml builds the collector image and runs the collector
#     self-test (outage_workflow).
#
# Tools: python3 with PyYAML, jq, shellcheck (SHELLCHECK=<path> or PATH) and
# docker compose. A missing tool is a FAIL of its case.
#
# Usage: [SHELLCHECK=<shellcheck>] ./tests/outage/config.test.sh
#
# Single quotes hold patterns and JSON literally (SC2016); the stubs replace
# functions of the suite and are called from it (SC2317); every stub run sets
# the suite's variables in its own subshell (SC2030, SC2031).
# shellcheck disable=SC2016,SC2317,SC2030,SC2031
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

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

SUITE=(tests/outage/run.sh tests/outage/lib.sh tests/outage/load.sh tests/outage/smtp.sh tests/outage/collector.sh
    tests/outage/config.test.sh)
# The line of the self-test that outage.yml expects (grep -E), here with the
# default load of 50 ingest records.
SELFTEST_LINE='^\| minio outage \| FAIL \| ingest: accepted [0-9]+ != persisted [0-9]+ \(lost 1: OUTAGE-[0-9]+-minio-I50, late 0\) \|$'
# The line of the collector self-test (COLLECTOR_SPOOL_RECORDS=1) that
# outage.yml expects (grep -E).
COLLECTOR_SELFTEST_LINE='^\| collector outage \| FAIL \| records: sent [0-9]+ in [0-9]+ passes != ingested [0-9]+ \(lost [1-9][0-9]*\); loss counter: [1-9][0-9]* reported by the heartbeats, [1-9][0-9]* logged by the collector'

# ─── WP-104/WP-449: the SMTP outage scenario is exercised (F-135) ───────────
smtp_case="$(grep -nE '^ *smtp\)' tests/outage/run.sh || true)"
if [ -z "$smtp_case" ]; then
    fail "outage_smtp: tests/outage/run.sh has an SMTP outage scenario" "<no smtp) case>"
elif printf '%s' "$smtp_case" | grep -q 'report N/A'; then
    fail "outage_smtp: tests/outage/run.sh simulates the SMTP outage instead of reporting N/A" \
        "$smtp_case; SMTP delivery exists: $(ls backend/internal/notify/smtp.go 2>/dev/null)"
else
    missing=""
    printf '%s' "$smtp_case" | grep -q 'smtp_outage' || missing="$missing run.sh-calls-smtp_outage"
    for needle in 'compose stop mailpit' 'check_unavailable smtp' '.status == "pending" and .attempts >= 1' \
        'last_error' 'compose start mailpit' 'SMTP_SENT_TIMEOUT:-120' '.status == "sent"' '/api/v1/search?query=' \
        '= 1 ] || failures+=("Mailpit holds' 'load_send smtp' 'load_verify smtp'; do
        grep -qF -- "$needle" tests/outage/smtp.sh || missing="$missing [$needle]"
    done
    if [ -z "$missing" ]; then
        ok "outage_smtp: tests/outage/run.sh simulates the SMTP outage (smtp.sh: pending with attempts and last_error, smtp unavailable, sent within 120 s, one mail, load)"
    else
        fail "outage_smtp: tests/outage/run.sh simulates the SMTP outage instead of reporting N/A" "missing:$missing"
    fi
fi

# ─── Scenarios and load ─────────────────────────────────────────────────────
problems=""
scenarios="$(sed -nE 's/^ALL_SCENARIOS="([^"]*)"/\1/p' tests/outage/run.sh)"
[ -n "$scenarios" ] || problems="$problems no ALL_SCENARIOS;"
for s in $scenarios; do
    line="$(grep -E "^ *$s\) " tests/outage/run.sh || true)"
    case "$s" in
        postgres|nats|redis|minio)
            printf '%s' "$line" | grep -q "load_scenario $s && dependency_outage $s " || problems="$problems $s without load;" ;;
        smtp) printf '%s' "$line" | grep -q 'load_scenario smtp && smtp_outage' || problems="$problems smtp without load;" ;;
        component) printf '%s' "$line" | grep -q 'component_scenarios' || problems="$problems component;" ;;
        collector) printf '%s' "$line" | grep -q 'load_scenario collector && collector_outage' || problems="$problems collector;" ;;
        *) problems="$problems unknown $s;" ;;
    esac
done
for s in postgres nats redis minio smtp component collector; do
    case " $scenarios " in *" $s "*) ;; *) problems="$problems $s not in ALL_SCENARIOS;" ;; esac
done
grep -q 'LOAD_API_WRITES="${LOAD_API_WRITES:-50}"' tests/outage/load.sh || problems="$problems LOAD_API_WRITES default not 50;"
grep -q 'LOAD_INGEST_RECORDS="${LOAD_INGEST_RECORDS:-50}"' tests/outage/load.sh || problems="$problems LOAD_INGEST_RECORDS default not 50;"
# WP-587: server and nats-edge down for 60 s, a spool of one pass (30
# records of the simulated fleet).
for needle in 'COLLECTOR_OUTAGE_SECONDS="${COLLECTOR_OUTAGE_SECONDS:-60}"' 'COLLECTOR_SPOOL_RECORDS="${COLLECTOR_SPOOL_RECORDS:-30}"' \
    'compose stop server nats-edge' '-e RETICORA_SIMULATE=true' '-e RETICORA_ENROLL_TOKEN '; do
    grep -qF -- "$needle" tests/outage/collector.sh || problems="$problems collector.sh without [$needle];"
done
if [ -z "$problems" ]; then
    ok "outage_scenarios: postgres, nats, redis, minio and smtp run under load (50 API writes, 50 ingest records), collector stops server and nats-edge for 60 s, component as before"
else
    fail "outage_scenarios: every scenario has its case and the outages run under load" "$problems"
fi

# ─── Syntax and shellcheck ──────────────────────────────────────────────────
problems=""
for f in "${SUITE[@]}"; do
    out="$(bash -n "$f" 2>&1)" || problems="$problems$f: $out"$'\n'
done
if [ -z "$problems" ]; then ok "outage_syntax: bash -n of the suite"; else fail "outage_syntax: bash -n of the suite" "$problems"; fi
SHELLCHECK_BIN="$(tool SHELLCHECK shellcheck || true)"
if [ -z "$SHELLCHECK_BIN" ]; then
    fail "outage_shellcheck: shellcheck available (set SHELLCHECK)" "shellcheck not found"
elif out="$("$SHELLCHECK_BIN" -x "${SUITE[@]}" 2>&1)"; then
    ok "outage_shellcheck: shellcheck -x of the suite ($("$SHELLCHECK_BIN" --version | sed -n 's/^version: //p'))"
else
    fail "outage_shellcheck: shellcheck -x of the suite" "$out"
fi

# ─── Workflows ──────────────────────────────────────────────────────────────
if ! python3 -c 'import yaml' 2>/dev/null; then
    fail "outage_workflow: python3 with PyYAML available" "import yaml failed"
elif out="$(python3 - "$SELFTEST_LINE" "$COLLECTOR_SELFTEST_LINE" <<'PY'
import sys, yaml
selftest_line, collector_line = sys.argv[1], sys.argv[2]
problems = []
wf = yaml.safe_load(open(".github/workflows/outage.yml"))
steps = wf["jobs"]["outage"]["steps"]
def index(pred, what):
    for i, s in enumerate(steps):
        if pred(s):
            return i
    problems.append("no step: " + what)
    return -1
run = lambda s: s.get("run") or ""
overlay = index(lambda s: "./tests/outage/smtp.sh overlay deploy/docker-compose/docker-compose.override.yml" in run(s), "smtp.sh overlay")
install = index(lambda s: "./install.sh --cloud --non-interactive --build-from-source" in run(s), "installation from source")
seed = index(lambda s: "go build -o \"$RUNNER_TEMP/seed\" ./cmd/seed" in run(s), "seed build")
suite = index(lambda s: run(s).strip() == "./tests/outage/run.sh", "suite run")
selftest = index(lambda s: (s.get("env") or {}).get("OUTAGE_SELFTEST_DROP") == "1", "negative self-test")
upload = index(lambda s: str(s.get("uses", "")).startswith("actions/upload-artifact@"), "protocol upload")
if -1 not in (overlay, install) and not overlay < install:
    problems.append("the Mailpit overlay must come before the installation")
if -1 not in (install, seed, suite) and not install < seed < suite:
    problems.append("order: installation, seed, suite")
if suite != -1:
    env = steps[suite].get("env") or {}
    if "OUTAGE_SEED_BIN" not in env or "OUTAGE_REPORT" not in env:
        problems.append("suite step without OUTAGE_SEED_BIN/OUTAGE_REPORT")
if selftest != -1:
    s = steps[selftest]
    if "./tests/outage/run.sh minio" not in run(s):
        problems.append("self-test does not run the minio scenario")
    if selftest_line not in run(s):
        problems.append("self-test does not expect exactly the dropped record lost: " + selftest_line)
    if "rc" not in run(s) or "exit 1" not in run(s):
        problems.append("self-test does not fail when the run passes")
    if s.get("if") != "${{ !cancelled() }}":
        problems.append("self-test must run after a failed suite too (if: !cancelled())")
    if (s.get("env") or {}).get("OUTAGE_REPORT", "").endswith("outage-report.md"):
        problems.append("self-test overwrites the protocol of the suite")
# WP-587: the collector image before the suite, the collector self-test.
image = index(lambda s: "docker build -t reticora-collector:outage -f collector/Dockerfile ." in run(s), "collector image build")
if -1 not in (image, suite) and not image < suite:
    problems.append("the collector image must be built before the suite")
if suite != -1 and (steps[suite].get("env") or {}).get("COLLECTOR_IMAGE") != "reticora-collector:outage":
    problems.append("suite step without COLLECTOR_IMAGE=reticora-collector:outage")
colself = index(lambda s: (s.get("env") or {}).get("COLLECTOR_SPOOL_RECORDS") == "1", "collector self-test")
if colself != -1:
    s = steps[colself]
    env = s.get("env") or {}
    if "./tests/outage/run.sh collector" not in run(s):
        problems.append("collector self-test does not run the collector scenario")
    if collector_line not in run(s):
        problems.append("collector self-test does not expect the records lost and the loss counter: " + collector_line)
    if "rc" not in run(s) or "exit 1" not in run(s):
        problems.append("collector self-test does not fail when the run passes")
    if s.get("if") != "${{ !cancelled() }}":
        problems.append("collector self-test must run after a failed suite too (if: !cancelled())")
    if env.get("OUTAGE_REPORT", "").endswith(("outage-report.md", "outage-selftest-report.md")) or "OUTAGE_SEED_BIN" not in env:
        problems.append("collector self-test overwrites another protocol or has no seed")
    if env.get("COLLECTOR_IMAGE") != "reticora-collector:outage":
        problems.append("collector self-test without COLLECTOR_IMAGE")
if upload != -1:
    paths = str(steps[upload].get("with", {}).get("path", ""))
    for p in ("outage-report.md", "outage-selftest-report.md", "outage-collector-selftest-report.md"):
        if p not in paths:
            problems.append("upload without " + p)
ci = yaml.safe_load(open(".github/workflows/ci.yml"))
if not any("./tests/outage/config.test.sh" in (s.get("run") or "") for s in ci["jobs"]["gate"]["steps"]):
    problems.append("ci.yml job gate does not run tests/outage/config.test.sh")
print("\n".join(problems))
PY
)" && [ -z "$out" ]; then
    ok "outage_workflow: overlay before the source installation, seed, collector image, suite, self-test expecting exactly the dropped record lost, collector self-test expecting the records lost and the loss counter, all three protocols uploaded; gate runs this file"
else
    fail "outage_workflow: outage.yml and the ci.yml job gate" "$out"
fi

# ─── Mailpit overlay ────────────────────────────────────────────────────────
DOCKER_BIN="$(command -v docker 2>/dev/null || true)"
cp deploy/docker-compose/docker-compose.override.yml "$TMP_ROOT/override.yml"
if ! out="$(tests/outage/smtp.sh overlay "$TMP_ROOT/override.yml" 2>&1)"; then
    fail "outage_overlay: smtp.sh overlay merges into the override" "$out"
elif out="$(python3 - "$TMP_ROOT/override.yml" deploy/docker-compose/docker-compose.override.yml <<'PY'
import sys, yaml
merged, original = (yaml.safe_load(open(p)) for p in sys.argv[1:3])
problems = []
s = merged.get("services", {})
for name, svc in original["services"].items():
    if s.get(name, {}).get("build") != svc.get("build"):
        problems.append(f"build of {name} changed")
mp = s.get("mailpit", {})
if mp.get("image") != "axllent/mailpit:v1.21" or mp.get("ports") != ["127.0.0.1:8025:8025"]:
    problems.append(f"mailpit: {mp}")
env = s.get("server", {}).get("environment", {})
want = {"RETICORA_SMTP_HOST": "mailpit", "RETICORA_SMTP_PORT": "1025", "RETICORA_SMTP_TLS": "none"}
for k, v in want.items():
    if env.get(k) != v:
        problems.append(f"server {k}={env.get(k)!r}, want {v!r}")
if not env.get("RETICORA_SMTP_FROM"):
    problems.append("server without RETICORA_SMTP_FROM")
if s.get("server", {}).get("depends_on", {}).get("mailpit") != {"condition": "service_started"}:
    problems.append("server does not depend on mailpit")
print("\n".join(problems))
PY
)" && [ -n "$out" ]; then
    fail "outage_overlay: smtp.sh overlay merges into the override" "$out"
elif [ -z "$DOCKER_BIN" ] || ! docker compose version >/dev/null 2>&1; then
    fail "outage_overlay: docker compose available" "docker compose not found"
else
    # The variables the compose file requires (:?) get dummy values.
    required="$(grep -oE '\$\{[A-Z0-9_]+:\?' deploy/docker-compose/docker-compose.yml | sed -E 's/^\$\{//; s/:\?$//' | sort -u)"
    envs=()
    for v in $required; do envs+=("$v=config-test"); done
    if out="$(env "${envs[@]}" docker compose --project-directory deploy/docker-compose --env-file /dev/null \
        -f deploy/docker-compose/docker-compose.yml -f "$TMP_ROOT/override.yml" config --format json 2>&1)" &&
        printf '%s' "$out" | jq -e '.services.mailpit.image == "axllent/mailpit:v1.21" and .services.server.environment.RETICORA_SMTP_HOST == "mailpit"
            and .services.server.depends_on.mailpit.condition == "service_started" and (.services.server.build.dockerfile | test("backend/Dockerfile"))' >/dev/null; then
        ok "outage_overlay: smtp.sh overlay keeps the build sections, adds Mailpit and RETICORA_SMTP_* of the server; docker compose config accepts it"
    else
        fail "outage_overlay: docker compose config of docker-compose.yml with the merged override" "$(printf '%s' "$out" | tail -n 5)"
    fi
fi

# ─── Accounting and SMTP scenario against stubs ─────────────────────────────
# stub_run <case dir> <scenario …>: outage_main of run.sh in a subshell with
# the stack replaced: compose stop/start mark a service down/up, readyz
# answers from that, api is a stub server that stores the accepted serial
# numbers, psql_exec answers from the stub database, nats_jsz and
# mailpit_get from files. FAKE_* select the misbehaviour. Prints the exit
# code of outage_main; the protocol is <case dir>/report.md.
stub_run() {
    local dir="$1"; shift
    mkdir -p "$dir"
    : > "$dir/db"; : > "$dir/down"
    printf '%s' "${FAKE_QUEUE:-0}" > "$dir/queue"
    printf '%s' "${FAKE_OUTBOX:-0}" > "$dir/outbox"
    printf '#!/bin/sh\nwhile [ $# -gt 0 ]; do [ "$1" = -api-key-file ] && printf "rk_test_stub\\n" > "$2"; shift; done\n' > "$dir/seed"
    chmod +x "$dir/seed"
    (
        S="$dir"
        # Nothing of the stub run may reach Docker or start Go tests, also
        # not a run.sh that would run its scenarios when sourced.
        docker() { echo "config.test.sh: docker is not called in a stub run" >&2; return 1; }
        go() { echo "config.test.sh: go is not called in a stub run" >&2; return 1; }
        COMPOSE_DIR="$S/no-compose"
        grep -q '^if \[ "${BASH_SOURCE\[0\]}" = "$0" \]; then' tests/outage/run.sh || {
            printf 'run.sh runs its scenarios when sourced' > "$S/report.md"
            printf 2
            exit 0
        }
        # shellcheck source=tests/outage/run.sh
        . tests/outage/run.sh
        OUTAGE_REPORT="$S/report.md" OUTAGE_SEED_BIN="$S/seed"
        OUTAGE_SECONDS=0 RECOVERY_TIMEOUT=1 SMTP_SENT_TIMEOUT=2 SMTP_ATTEMPT_TIMEOUT=1 SMTP_HOLD_AFTER_FAILURE=0
        LOAD_API_WRITES="${FAKE_LOAD:-50}" LOAD_INGEST_RECORDS="${FAKE_LOAD:-50}"
        COLLECTOR_INTERVAL_SECONDS=0 COLLECTOR_OUTAGE_SECONDS=0 COLLECTOR_PASS_TIMEOUT=1 COLLECTOR_START_TIMEOUT=1
        # The collector model (collector.sh): a pass of 30 records is a spool
        # message of 46460 bytes; it fits a spool limit of at least that and
        # saturates it from 90 %. Online a spooled pass is delivered after
        # the next heartbeat; during the outage of server and nats-edge it
        # stays in the spool until the restart. A pass that does not fit is
        # lost and counted. Every heartbeat with losses or backpressure is a
        # line collector.spool.degraded in the server log.
        printf 0 > "$S/col-spool"; printf 0 > "$S/col-ingested"; printf 0 > "$S/col-heartbeat"; printf 0 > "$S/col-dropped"
        : > "$S/server.log"; : > "$S/removed"; : > "$S/stopped"
        col_report() { # <spool messages> <backpressure> <dropped>
            printf '{"time":"t","level":"WARN","msg":"collector reported spool data loss or backpressure","event":"collector.spool.degraded","collector_id":"col-1","spool_messages":%s,"backpressure":%s,"dropped_messages":%s,"dropped_samples":0}\n' \
                "$1" "$2" "$3" >> "$S/server.log"
        }
        col_saturated() { [ 46460 -ge $(($(cat "$S/col-limit") * 9 / 10)) ]; }
        col_pass() { # <online|outage>
            printf '{"level":"INFO","msg":"discovery completed","total_records":30}\n' >> "$S/col.log"
            if [ 46460 -gt "$(cat "$S/col-limit")" ]; then
                printf '{"level":"ERROR","msg":"spool data lost","event":"collector.spool.data_lost","reason":"spool_full","messages":1,"bytes":46460}\n' >> "$S/col.log"
                printf '%s' $(($(cat "$S/col-dropped") + 1)) > "$S/col-dropped"
                [ "$1" = online ] && col_heartbeat
                return 0
            fi
            printf '%s' $(($(cat "$S/col-spool") + 1)) > "$S/col-spool"
            [ "$1" = online ] && col_heartbeat
            return 0
        }
        col_heartbeat() { # the first accepted heartbeat, then the flush
            local spool dropped bp=false
            spool="$(cat "$S/col-spool")" dropped="$(cat "$S/col-dropped")"
            [ "$spool" -gt 0 ] && col_saturated && [ -z "${FAKE_NO_BACKPRESSURE:-}" ] && bp=true
            { [ "$dropped" -gt 0 ] || [ "$bp" = true ]; } && col_report "$spool" "$bp" "$dropped"
            printf 0 > "$S/col-dropped"
            printf '%s' $(($(cat "$S/col-ingested") + 30 * spool)) > "$S/col-ingested"
            printf 0 > "$S/col-spool"
        }
        collector_docker() {
            local cmdline=" $* " name="" limit="" a prev=""
            for a in "$@"; do
                [ "$prev" = --name ] && name="$a"
                case "$a" in RETICORA_SPOOL_MAX_BYTES=*) limit="${a#*=}" ;; esac
                prev="$a"
            done
            case "$1" in
                image) [ -z "${FAKE_NO_IMAGE:-}" ] ;;
                inspect) printf 'outage_default \n' ;;
                run)
                    case "$cmdline" in
                        *" --network none "*)
                            printf '{"msg":"spool stats","spool_messages":1,"spool_bytes":46460}\n{"msg":"discovery completed","total_records":30}\n' > "$S/col-calibration.log"
                            return 0 ;;
                    esac
                    # The enrollment code reaches docker through the
                    # environment only.
                    case "$cmdline" in *code-1*) echo "enrollment code on the command line" >&2; return 1 ;; esac
                    case "${RETICORA_ENROLL_TOKEN:-}:$cmdline" in
                        "code-1:"*" -e RETICORA_ENROLL_TOKEN "*) ;;
                        *) echo "RETICORA_ENROLL_TOKEN not passed from the environment" >&2; return 1 ;;
                    esac
                    case "$cmdline" in *" --network outage_default "*) ;; *) echo "not on the stack network" >&2; return 1 ;; esac
                    printf '%s' "$limit" > "$S/col-limit"
                    : > "$S/col.log"
                    col_pass online ;;
                logs) case "$2" in *-calibration) cat "$S/col-calibration.log" ;; *) cat "$S/col.log" ;; esac ;;
                exec) cat "$S/col-spool"; echo ;;
                rm) printf '%s\n' "${*: -1}" >> "$S/removed" ;;
            esac
        }
        sleep() { command sleep 0.05; }
        db_admin_url() { printf 'postgres://stub'; }
        server_restarts() { printf 0; }
        server_alive() { true; }
        check_name() { case "$1" in postgres) echo database ;; minio) echo object_storage ;; mailpit) echo smtp ;; *) echo "$1" ;; esac; }
        compose() {
            case "$1" in
                stop) printf '%s\n' "$2" > "$S/down"; printf '%s\n' "${*:2}" >> "$S/stopped"
                    [ "$2" = server ] && col_pass outage ;;
                start) : > "$S/down"
                    [ "$2" = nats-edge ] && { printf 1 > "$S/col-heartbeat"; col_heartbeat; } ;;
                ps) printf 'server-ctr\n' ;;
                logs) cat "$S/server.log" ;;
            esac
            return 0
        }
        readyz() {
            local checks='{"database":"ok","nats":"ok","redis":"ok","object_storage":"ok","smtp":"ok"}' down
            [ -n "${FAKE_NO_SMTP_CHECK:-}" ] && checks='{"database":"ok","nats":"ok","redis":"ok","object_storage":"ok"}'
            down="$(cat "$S/down")"
            if [ "$down" = server ]; then
                printf '000 '
            elif [ -n "$down" ]; then
                printf '503 {"status":"not_ready","checks":%s}' "$(printf '%s' "$checks" | jq -c --arg c "$(check_name "$down")" '.[$c] = "unavailable"')"
            else
                printf '200 {"status":"ready","checks":%s}' "$checks"
            fi
        }
        nats_jsz() { printf '{"account_details":[{"name":"$G","stream_detail":[{"name":"RETICORA_INGEST","state":{"messages":%s}}]}]}' "$(cat "$S/queue")"; }
        mailpit_get() { printf '{"messages_count":%s,"total":%s}' "${FAKE_MAILS:-1}" "${FAKE_MAILS:-1}"; }
        psql_exec() {
            local sql="$*" prefix
            case "$sql" in
                *"FROM organization"*) echo org-1 ;;
                *"FROM ci_type"*) echo type-1 ;;
                *"FROM role "*) echo role-1 ;;
                *"FROM outage_marker"*) echo 1 ;;
                *"last_heartbeat_at"*) cat "$S/col-heartbeat" ;;
                *"FROM collector WHERE organization_id"*) echo col-1 ;;
                *"FROM ingest_record"*) printf '%s\n' $(($(cat "$S/col-ingested") - ${FAKE_FLUSH_LOSE:-0})) ;;
                *"FROM event_outbox"*) cat "$S/outbox" ;;
                *"SELECT serial_number FROM ci "*)
                    prefix="$(printf '%s' "$sql" | sed -nE "s/.*LIKE '([^']*)-%'.*/\1/p")"
                    grep -F -- "$prefix-" "$S/db" | sort ;;
            esac
            return 0
        }
        # The stub server: <kind> <serial> → status; FAKE_REFUSE_<KIND>
        # answers while a dependency is down, FAKE_LOSE/FAKE_LATE name the
        # suffix of a serial number that is lost after 2xx or stored after
        # a refusal.
        stub_write() {
            local kind="$1" serial="$2" ok="$3" refuse
            refuse="FAKE_REFUSE_$kind"
            if [ -s "$S/down" ] && [ -n "${!refuse:-}" ]; then
                case "$serial" in *"${FAKE_LATE:-none}") echo "$serial" >> "$S/db" ;; esac
                echo '{}' > "$API_OUT"
                printf '%s' "${!refuse}"
                return
            fi
            case "$serial" in *"${FAKE_LOSE:-none}") ;; *) echo "$serial" >> "$S/db" ;; esac
            echo '{"received":1,"queued":1,"rejected_future":0}' > "$API_OUT"
            printf '%s' "$ok"
        }
        stub_deliveries() {
            local email status=pending attempts=1 err="central_smtp: smtp connect mailpit:1025: dial tcp: lookup mailpit: no such host"
            email="$(cat "$S/invited" 2>/dev/null)"
            if [ -z "$email" ] || [ -n "${FAKE_NO_ATTEMPT:-}" ]; then
                echo '{"data":[]}' > "$API_OUT"; printf 200; return
            fi
            if [ ! -s "$S/down" ] && [ -z "${FAKE_NEVER_SENT:-}" ]; then status=sent attempts=2 err=""; fi
            jq -n --arg e "$email" --arg s "$status" --argjson a "$attempts" --arg err "$err" \
                '{data: [{id: "d-1", channel_id: "ch-1", recipient: {email: $e}, status: $s, attempts: $a,
                  last_error: (if $err == "" then null else $err end), next_attempt_at: "2026-10-26T00:02:00Z", sent_at: null}]}' > "$API_OUT"
            printf 200
        }
        api() {
            local method="$1" path="$2" body="${3:-}"
            case "$method $path" in
                "POST /cis") stub_write API "$(printf '%s' "$body" | jq -r .serial_number)" 201 ;;
                "POST /ingest/bulk") stub_write INGEST "$(printf '%s' "$body" | jq -r '.records[0].identity.serial_number')" 202 ;;
                "GET /notification-channels") echo '{"data":[]}' > "$API_OUT"; printf 200 ;;
                "POST /notification-channels") echo '{"id":"ch-1"}' > "$API_OUT"; printf 201 ;;
                "POST /invitations") printf '%s' "$body" | jq -r .email > "$S/invited"; echo '{"id":"inv-1"}' > "$API_OUT"; printf 201 ;;
                "POST /collectors/enrollment-codes") echo '{"id":"ec-1","code":"code-1"}' > "$API_OUT"; printf 201 ;;
                "GET /notification-deliveries"*) stub_deliveries ;;
                *) printf 404 ;;
            esac
        }
        outage_main "$@" > "$S/out.log" 2>&1
        printf '%s' "$?"
    )
}

# expect_case <name> <want exit: 0|1> <dir> <grep -E patterns …>: the case
# passes when outage_main exited as wanted and the protocol has every line.
expect_case() {
    local name="$1" want="$2" dir="$3" rc="$4" missing=""; shift 4
    for p in "$@"; do
        grep -qE -- "$p" "$dir/report.md" 2>/dev/null || missing="$missing"$'\n'"  missing: $p"
    done
    if [ "$rc" = "$want" ] && [ -z "$missing" ]; then
        ok "$name"
    else
        fail "$name" "exit $rc (want $want)$missing"$'\n'"$(cat "$dir/report.md" 2>/dev/null; tail -n 5 "$dir/out.log" 2>/dev/null)"
    fi
}

if ! command -v jq >/dev/null; then
    fail "accounting: jq available" "jq not found"
else
    d="$TMP_ROOT/ok"
    rc="$(FAKE_REFUSE_INGEST=503 stub_run "$d" nats)"
    expect_case "accounting_ok: NATS down, writes accepted and stored, records refused with 503 → PASS with every count in the protocol" 0 "$d" "$rc" \
        '^\| nats outage \| PASS \| .*api 50/50 accepted = 50 persisted; ingest 0/50 accepted = 0 persisted, 50×503; ingest queue 0, outbox 0 unpublished \|$' \
        '^\| nats \| api \| 50 \| 50 \| 0 \| 0 \| 0 \| 50 \| 0 \| 0 \|$' \
        '^\| nats \| ingest \| 50 \| 0 \| 0 \| 50 \| 0 \| 0 \| 0 \| 0 \|$' \
        '^\| nats \| ingest queue 0, outbox 0 unpublished \| 0 → 0 \|$' \
        '^\*\*Result:\*\* 1 passed, 0 failed'

    d="$TMP_ROOT/refused"
    rc="$(FAKE_LOAD=10 FAKE_REFUSE_API=503 FAKE_REFUSE_INGEST=503 stub_run "$d" postgres)"
    expect_case "accounting_refused: everything refused with 503, nothing stored → PASS (0 accepted = 0 persisted)" 0 "$d" "$rc" \
        '^\| postgres outage \| PASS \| .*api 0/10 accepted = 0 persisted, 10×503; ingest 0/10 accepted = 0 persisted, 10×503' \
        '^\| postgres \| api \| 10 \| 0 \| 0 \| 10 \| 0 \| 0 \| 0 \| 0 \|$'

    d="$TMP_ROOT/selftest"
    rc="$(OUTAGE_SELFTEST_DROP=1 stub_run "$d" minio)"
    expect_case "accounting_selftest_drop: OUTAGE_SELFTEST_DROP=1 → FAIL with exactly the dropped record lost (the line outage.yml expects)" 1 "$d" "$rc" \
        "$SELFTEST_LINE" \
        '^\| minio \| ingest \| 50 \| 50 \| 0 \| 0 \| 0 \| 49 \| 1 \| 0 \|$' \
        'Negative self-test OUTAGE_SELFTEST_DROP=1'

    d="$TMP_ROOT/lost"
    rc="$(FAKE_LOAD=10 FAKE_LOSE=-I7 stub_run "$d" redis)"
    expect_case "accounting_lost: a record answered 202 but not stored → FAIL lost" 1 "$d" "$rc" \
        '^\| redis outage \| FAIL \| ingest: accepted 10 != persisted 9 \(lost 1: OUTAGE-[0-9]+-redis-I7, late 0\) \|$'

    d="$TMP_ROOT/late"
    rc="$(FAKE_LOAD=10 FAKE_REFUSE_INGEST=503 FAKE_LATE=-I3 stub_run "$d" nats)"
    expect_case "accounting_late: a record refused with 503 but stored anyway → FAIL late" 1 "$d" "$rc" \
        '^\| nats outage \| FAIL \| ingest: accepted 0 != persisted 1 \(lost 0, late 1: OUTAGE-[0-9]+-nats-I3\) \|$' \
        '^\| nats \| ingest \| 10 \| 0 \| 0 \| 10 \| 0 \| 1 \| 0 \| 1 \|$'

    d="$TMP_ROOT/status"
    rc="$(FAKE_LOAD=10 FAKE_REFUSE_API=401 FAKE_REFUSE_INGEST=500 stub_run "$d" postgres)"
    expect_case "accounting_status: 401 and 500 are neither accepted nor visibly refused → FAIL" 1 "$d" "$rc" \
        '^\| postgres outage \| FAIL \| api: 10 answers neither 2xx nor 429/503 \(10×401\); ingest: 10 answers neither 2xx nor 429/503 \(10×500\) \|$'

    d="$TMP_ROOT/queue"
    rc="$(FAKE_LOAD=5 FAKE_QUEUE=3 FAKE_OUTBOX=2 stub_run "$d" minio)"
    expect_case "accounting_queue_outbox: ingest queue not empty and outbox entries unpublished after the recovery → FAIL" 1 "$d" "$rc" \
        '^\| minio outage \| FAIL \| ingest queue not empty: 3 messages; event outbox: 2 entries up to .* unpublished \|$'

    d="$TMP_ROOT/smtp-ok"
    rc="$(FAKE_LOAD=10 stub_run "$d" smtp)"
    expect_case "smtp_scenario_ok: pending with attempts 1 and last_error during the outage, sent after the restart, exactly one mail → PASS" 0 "$d" "$rc" \
        '^\| smtp outage \| PASS \| readiness 503 with smtp unavailable; delivery pending .*api 10/10 accepted = 10 persisted; ingest 10/10 accepted = 10 persisted' \
        '^- During the outage: delivery id d-1, status pending, attempts 1, last_error "central_smtp: smtp connect mailpit:1025' \
        '^- /readyz during the outage: `503 .*"smtp":"unavailable"' \
        '^- After Mailpit was started again: sent after [0-9]+s \(limit 2s\), delivery id d-1, status sent, attempts 2' \
        '^- Mailpit: 1 message\(s\) to `outage-[0-9]+-[0-9]+@reticora-outage.example`' \
        '^\| smtp \| api \| 10 \| 10 \| 0 \| 0 \| 0 \| 10 \| 0 \| 0 \|$'

    d="$TMP_ROOT/smtp-two"
    rc="$(FAKE_LOAD=2 FAKE_MAILS=2 stub_run "$d" smtp)"
    expect_case "smtp_scenario_two_mails: two mails to the invitee → FAIL" 1 "$d" "$rc" \
        '^\| smtp outage \| FAIL \| Mailpit holds 2 messages to outage-'

    d="$TMP_ROOT/smtp-noattempt"
    rc="$(FAKE_LOAD=2 FAKE_NO_ATTEMPT=1 stub_run "$d" smtp)"
    expect_case "smtp_scenario_no_attempt: no pending delivery with a failed attempt → FAIL" 1 "$d" "$rc" \
        '^\| smtp outage \| FAIL \| no pending delivery with a failed attempt and last_error'

    d="$TMP_ROOT/smtp-unsent"
    rc="$(FAKE_LOAD=2 FAKE_NEVER_SENT=1 stub_run "$d" smtp)"
    expect_case "smtp_scenario_never_sent: still pending after the restart → FAIL" 1 "$d" "$rc" \
        '^\| smtp outage \| FAIL \| delivery not sent within 2s'

    d="$TMP_ROOT/smtp-nocheck"
    rc="$(FAKE_LOAD=2 FAKE_NO_SMTP_CHECK=1 stub_run "$d" smtp)"
    expect_case "smtp_scenario_no_smtp_check: /readyz without smtp (no RETICORA_SMTP_HOST) → FAIL" 1 "$d" "$rc" \
        '^\| smtp outage \| FAIL \| /readyz has no smtp check'

    # WP-587: the collector scenario against the collector model.
    d="$TMP_ROOT/collector-ok"
    rc="$(stub_run "$d" collector)"
    expect_case "collector_scenario_ok: spool 0 → 1 while server and nats-edge are down, backpressure in the heartbeat after the restart, 60 sent = 60 ingested, loss counter 0 → PASS" 0 "$d" "$rc" \
        '^\| collector outage \| PASS \| server and nats-edge down [0-9]+s; spool 0 → 1, backpressure reported by the heartbeat after the restart; 60 records sent in 2 passes = 60 ingested; loss counter 0 \|$' \
        '^- Spool limit: 30 record\(s\) = 48783 bytes \(calibration: one spooled pass of 30 records = 46460 bytes, plus 5 %\)$' \
        '^- Before the outage: 1 pass\(es\), 30 records sent, 30 ingested$' \
        '^- Outage: server and nats-edge stopped for [0-9]+s; spool counter 0 → 1 message\(s\); 1 pass\(es\) during the outage$' \
        '^- Heartbeat after the restart: spool report `\{"spool_messages":1,"backpressure":true,"dropped_messages":0,"dropped_samples":0\}`$' \
        '^- After the recovery: 60 records sent in 2 passes, 60 ingested; loss counter: 0 reported by the heartbeats, 0 logged by the collector$' \
        '^## Collector: server and nats-edge unreachable$'
    if [ "$(cat "$d/stopped" 2>/dev/null)" = "server nats-edge" ] && grep -q -- '-calibration$' "$d/removed" &&
        grep -qE '^outage-collector-[0-9]+$' "$d/removed"; then
        ok "collector_scenario_stack: docker compose stop server nats-edge together; calibration and collector containers removed"
    else
        fail "collector_scenario_stack: docker compose stop server nats-edge together; calibration and collector containers removed" \
            "stopped: $(cat "$d/stopped" 2>/dev/null); removed: $(cat "$d/removed" 2>/dev/null)"
    fi

    d="$TMP_ROOT/collector-selftest"
    rc="$(COLLECTOR_SPOOL_RECORDS=1 stub_run "$d" collector)"
    expect_case "collector_selftest_spool_one_record: COLLECTOR_SPOOL_RECORDS=1 → FAIL with the records lost and the loss counter above 0 (the line outage.yml expects)" 1 "$d" "$rc" \
        "$COLLECTOR_SELFTEST_LINE" \
        '^\| collector outage \| FAIL \| records: sent 60 in 2 passes != ingested 0 \(lost 60\); loss counter: 2 reported by the heartbeats, 2 logged by the collector; before the outage: 0 of 30 records ingested' \
        '^- Spool limit: 1 record\(s\) = 1627 bytes' \
        'Collector spool limit COLLECTOR_SPOOL_RECORDS=1'

    d="$TMP_ROOT/collector-nobp"
    rc="$(FAKE_NO_BACKPRESSURE=1 stub_run "$d" collector)"
    expect_case "collector_scenario_no_backpressure: no heartbeat after the restart reports backpressure → FAIL" 1 "$d" "$rc" \
        '^\| collector outage \| FAIL \| no heartbeat after the restart reported backpressure \|$'

    d="$TMP_ROOT/collector-lost"
    rc="$(FAKE_FLUSH_LOSE=3 stub_run "$d" collector)"
    expect_case "collector_scenario_lost: three records of the spool not ingested after the restart → FAIL lost" 1 "$d" "$rc" \
        '^\| collector outage \| FAIL \| records: sent 60 in 2 passes != ingested 57 \(lost 3\); before the outage: 27 of 30 records ingested'

    d="$TMP_ROOT/collector-noimage"
    rc="$(FAKE_NO_IMAGE=1 stub_run "$d" collector)"
    expect_case "collector_scenario_no_image: collector image missing → FAIL" 1 "$d" "$rc" \
        '^\| collector outage \| FAIL \| collector image reticora-collector:outage not found'

    # load_setup with the real db_admin_url and env_value: the seed runs in
    # backend/, the default COMPOSE_DIR is relative to the repository root,
    # so the URL has to be read before the cd (outage.yml once seeded with
    # an empty password: "grep: deploy/docker-compose/.env: No such file").
    d="$TMP_ROOT/seed-url"
    mkdir -p "$d/repo/deploy/docker-compose" "$d/repo/backend"
    printf 'RETICORA_DB_PASSWORD="p@ss/w0rd"\n' > "$d/repo/deploy/docker-compose/.env"
    printf '#!/bin/sh\nprintf "%%s" "$RETICORA_DATABASE_URL" > "%s/url"\nwhile [ $# -gt 0 ]; do [ "$1" = -api-key-file ] && printf "rk_test_stub\\n" > "$2"; shift; done\n' "$d" > "$d/seed"
    chmod +x "$d/seed"
    repo="$PWD"
    (
        cd "$d/repo" || exit 1
        unset COMPOSE_DIR ENV_FILE
        # shellcheck source=tests/outage/lib.sh
        . "$repo/tests/outage/lib.sh"
        # shellcheck source=tests/outage/load.sh
        . "$repo/tests/outage/load.sh"
        psql_exec() { printf 'id-1\n'; }
        OUTAGE_SEED_BIN="$d/seed" load_setup "$d/load"
    ) > "$d/out.log" 2>&1
    want='postgres://reticora:p%40ss%2Fw0rd@127.0.0.1:5432/reticora?sslmode=disable'
    if [ "$(cat "$d/url" 2>/dev/null)" = "$want" ]; then
        ok "load_setup_db_url: cmd/seed gets the bootstrap password of the installation's .env (URL read before cd backend)"
    else
        fail "load_setup_db_url: cmd/seed gets the bootstrap password of the installation's .env (URL read before cd backend)" \
            "got: $(cat "$d/url" 2>/dev/null)"$'\n'"want: $want"$'\n'"$(cat "$d/out.log")"
    fi
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
