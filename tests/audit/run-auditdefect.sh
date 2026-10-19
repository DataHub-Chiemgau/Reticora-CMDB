#!/usr/bin/env bash
# Ratchet for the defect tests of the Phase-1 acceptance audit (WP-337, TST-04).
#
# The audit encoded every confirmed finding as a test that fails until the
# finding is fixed (audit/phase1-final/PHASE1_FINDINGS.md):
#   go       test functions in files with "//go:build auditdefect"
#   sh       cases of tests/audit/*.test.sh
#   vitest   tests in frontend/src/**/*.auditdefect.ts(x)
#   e2e-real tests of frontend/e2e-real/ (compared by install-smoke.yml)
# tests/audit/expected-failures.txt lists every test that is red today. This
# script runs the tests and compares each result with the list:
#   listed and red          -> allowed
#   listed and green        -> error "überführen": move the test into the
#                              regular suite and delete its line
#   listed and skipped      -> error "nicht ausgeführt"
#   listed, not in the run  -> error "verwaist": delete the line
#   not listed and red      -> error "Rückfall"
#   aborted (build failure, panic, missing setup) -> error, listed or not
# Only the lines of the kinds a run executes are compared.
#
# Usage:
#   tests/audit/run-auditdefect.sh [--kinds go,sh,vitest] [--out DIR]
#       run the tests of the given kinds, then compare
#   tests/audit/run-auditdefect.sh compare --kinds KINDS --results FILE
#       compare an existing result file (lines "art;ort;test;status;detail",
#       status pass|fail|skip|error)
#   tests/audit/run-auditdefect.sh sh-parse SCRIPT LOG RC
#       print the result lines of one shell test log
#   tests/audit/run-auditdefect.sh playwright --report report.json --results FILE
#       convert a Playwright JSON report into result lines (kind e2e-real)
# Environment: EXPECTED (default tests/audit/expected-failures.txt). Go
# defect tests need TEST_DATABASE_URL, TEST_NATS_URL, TEST_REDIS_URL and
# TEST_NATS_SERVER_BIN like the CI job "migrations".
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
EXPECTED="${EXPECTED:-$ROOT/tests/audit/expected-failures.txt}"

py() { python3 - "$@"; }

compare() { # kinds results
    py "$1" "$2" "$EXPECTED" <<'PY'
import sys
kinds = set(sys.argv[1].split(","))
results_path, expected_path = sys.argv[2], sys.argv[3]

def key(art, ort, test):
    return (art.strip(), ort.strip(), test.strip())

expected = {}
for n, line in enumerate(open(expected_path, encoding="utf-8"), 1):
    line = line.rstrip("\n")
    if not line.strip() or line.lstrip().startswith("#"):
        continue
    parts = line.split(";")
    if len(parts) != 4:
        print(f"FEHLER {expected_path}:{n}: Zeile braucht 4 Felder art;ort;test;befund: {line}")
        sys.exit(2)
    art, ort, test, befund = parts
    if art not in ("go", "sh", "vitest", "e2e-real"):
        print(f"FEHLER {expected_path}:{n}: unbekannte Art {art!r}")
        sys.exit(2)
    k = key(art, ort, test)
    if k in expected:
        print(f"FEHLER {expected_path}:{n}: doppelte Zeile {line}")
        sys.exit(2)
    expected[k] = befund.strip()

results = {}
for line in open(results_path, encoding="utf-8"):
    line = line.rstrip("\n")
    if not line:
        continue
    art, ort, test, status, detail = (line.split(";", 4) + [""])[:5]
    results[key(art, ort, test)] = (status.strip(), detail.strip())

errors, allowed = [], 0
for k, (status, detail) in sorted(results.items()):
    if k[0] not in kinds:
        continue
    listed = k in expected
    label = ";".join(k)
    if status == "error":
        errors.append(f"Abbruch      {label}  ({detail})")
    elif listed and status == "fail":
        allowed += 1
    elif listed and status == "pass":
        errors.append(f"überführen   {label}  (grün; Test in die reguläre Suite verschieben und Zeile löschen, {expected[k]})")
    elif listed and status == "skip":
        errors.append(f"nicht ausgeführt {label}  (übersprungen, {expected[k]})")
    elif not listed and status == "fail":
        errors.append(f"Rückfall     {label}  (rot, aber nicht in der Erwartungsliste{': ' + detail if detail else ''})")
for k, befund in sorted(expected.items()):
    if k[0] in kinds and k not in results:
        errors.append(f"verwaist     {';'.join(k)}  (kommt im Lauf nicht vor; Zeile löschen, {befund})")

ran = sum(1 for k in results if k[0] in kinds)
print(f"Ratchet ({','.join(sorted(kinds))}): {ran} Ergebnisse, {allowed} erwartet rot, {len(errors)} Fehler")
for e in errors:
    print("FEHLER " + e)
sys.exit(1 if errors else 0)
PY
}

# ---------------------------------------------------------------- go
run_go() { # out results
    local out="$1" results="$2" mod files dir pkg names
    for mod in backend collector edgecore; do
        files=$(cd "$ROOT/$mod" && grep -rl --include='*_test.go' '^//go:build auditdefect' . | sort)
        [ -z "$files" ] && continue
        for dir in $(printf '%s\n' "$files" | xargs -n1 dirname | sort -u); do
            pkg="$mod/${dir#./}"
            names=$(cd "$ROOT/$mod" && for f in $(printf '%s\n' "$files" | grep "^$dir/[^/]*$"); do
                grep -oE '^func Test[A-Za-z0-9_]+\(t \*testing\.T\)' "$f" | sed -E 's/^func (Test[A-Za-z0-9_]+).*/\1/'
            done | sort -u)
            [ -z "$names" ] && continue
            local log="$out/go-$(echo "$pkg" | tr / _).json"
            (cd "$ROOT/$mod" && go test -tags auditdefect -count=1 -p 1 -json \
                -run "^($(printf '%s\n' "$names" | paste -sd'|'))\$" "$dir" >"$log" 2>"$log.stderr")
            py "$pkg" "$log" "$names" >>"$results" <<'PY'
import json, re, sys
pkg, log, names = sys.argv[1], sys.argv[2], sys.argv[3].split()
status, output, pkg_out = {}, {}, []
for line in open(log, encoding="utf-8", errors="replace"):
    try:
        e = json.loads(line)
    except ValueError:
        pkg_out.append(line)
        continue
    t = e.get("Test")
    if t is None:
        pkg_out.append(e.get("Output") or "")
        continue
    top = t.split("/")[0]
    if e.get("Action") == "output":
        output.setdefault(top, []).append(e.get("Output", ""))
    if "/" not in t and e.get("Action") in ("pass", "fail", "skip"):
        status[t] = e["Action"]
stderr = open(log + ".stderr", encoding="utf-8", errors="replace").read()
build_failed = "build failed" in stderr or "setup failed" in "".join(pkg_out) or "[build failed]" in "".join(pkg_out)
# Aborts: a panic, or a missing test service or variable (TEST_..._URL/BIN).
setup = re.compile(r"panic:|\[setup failed\]|TEST_[A-Z_]+[^\n]*(required|not set)")
for n in names:
    text = "".join(output.get(n, []))
    st = status.get(n)
    if build_failed:
        print(f"go;{pkg};{n};error;Build-Fehler im Paket")
    elif st is None:
        print(f"go;{pkg};{n};error;kein Ergebnis (Abbruch vor dem Test)")
    elif st != "pass" and setup.search(text):
        print(f"go;{pkg};{n};error;Abbruch ({setup.search(text).group(0)[:80]})")
    else:
        print(f"go;{pkg};{n};{st};")
PY
        done
    done
}

# ---------------------------------------------------------------- sh
# A case is the text of an "ok   <text>" or "FAIL <text>" line up to the first
# ": " (the whole text if there is none); several lines with the same case
# form one case, which is red as soon as one line is FAIL. A FAIL line that
# reports a missing tool or variable ("... available (set X)", "... required")
# is a setup error, as is a script that reports no case at all.
parse_sh() { # script-path log rc
    py "$1" "$2" "$3" <<'PY'
import re, sys
script, log, rc = sys.argv[1], sys.argv[2], int(sys.argv[3])
cases, order, setup = {}, [], []
for line in open(log, encoding="utf-8", errors="replace"):
    m = re.match(r"^(ok|FAIL) {1,3}(\S.*)$", line.rstrip("\n"))
    if not m:
        continue
    verdict, text = m.groups()
    if verdict == "FAIL" and re.search(r"available \(set [A-Z_]+\)| (is|are) required", text):
        setup.append(text)
        continue
    case = text.split(": ", 1)[0].strip().replace(";", ",")
    if case not in cases:
        order.append(case)
        cases[case] = "pass"
    if verdict == "FAIL":
        cases[case] = "fail"
for text in setup:
    print(f"sh;{script};<setup>;error;{text.replace(';', ',')[:160]}")
if not cases and not setup:
    print(f"sh;{script};<setup>;error;keine Fälle ausgegeben (Exit {rc})")
for case in order:
    print(f"sh;{script};{case};{cases[case]};")
PY
}

run_sh() { # out results
    local out="$1" results="$2" s rel log rc
    for s in "$ROOT"/tests/audit/*.test.sh; do
        rel="${s#$ROOT/}"
        [ "$rel" = "tests/audit/run-auditdefect.test.sh" ] && continue
        log="$out/sh-$(basename "$s").log"
        (cd "$ROOT" && timeout 900 bash "$rel" >"$log" 2>&1); rc=$?
        parse_sh "$rel" "$log" "$rc" >>"$results"
    done
}

# ---------------------------------------------------------------- vitest
run_vitest() { # out results
    local out="$1" results="$2"
    (cd "$ROOT/frontend" && npx vitest run -c vitest.auditdefect.config.ts --reporter=json \
        --outputFile="$out/vitest.json" >"$out/vitest.log" 2>&1)
    py "$ROOT" "$out/vitest.json" >>"$results" <<'PY'
import json, os, sys
root, path = sys.argv[1], sys.argv[2]
if not os.path.exists(path):
    print("vitest;frontend;<setup>;error;kein JSON-Bericht (siehe vitest.log)")
    sys.exit(0)
report = json.load(open(path, encoding="utf-8"))
for f in report.get("testResults", []):
    rel = os.path.relpath(f["name"], root)
    asserts = f.get("assertionResults", [])
    if not asserts:
        msg = (f.get("message") or "Datei ohne Tests").replace("\n", " ").replace(";", ",")[:160]
        print(f"vitest;{rel};<setup>;error;{msg}")
    for a in asserts:
        name = " ".join(a.get("ancestorTitles", []) + [a["title"]]).replace(";", ",")
        st = {"passed": "pass", "failed": "fail", "skipped": "skip", "pending": "skip", "todo": "skip"}.get(a["status"], "error")
        print(f"vitest;{rel};{name};{st};")
PY
}

# ---------------------------------------------------------------- playwright (e2e-real)
playwright_results() { # report.json
    py "$1" <<'PY'
import json, os, sys
report = json.load(open(sys.argv[1], encoding="utf-8"))
def walk(suite, titles):
    for s in suite.get("suites", []) or []:
        yield from walk(s, titles + ([s["title"]] if s.get("title") and not s["title"].endswith((".ts", ".js")) else []))
    for spec in suite.get("specs", []) or []:
        for t in spec.get("tests", []):
            yield spec, titles, t
for top in report.get("suites", []):
    for spec, titles, t in walk(top, []):
        f = spec.get("file") or top.get("file") or top.get("title")
        name = " ".join(titles + [spec["title"]]).replace(";", ",")
        project = t.get("projectName") or "chromium"
        res = t.get("results") or []
        status = t.get("status")  # expected | unexpected | flaky | skipped
        last = res[-1]["status"] if res else "skipped"
        st = {"passed": "pass", "failed": "fail", "timedOut": "fail", "interrupted": "error", "skipped": "skip"}.get(last, "error")
        if status == "skipped":
            st = "skip"
        print(f"e2e-real;{project}:frontend/e2e-real/{os.path.basename(f)};{name};{st};")
PY
}

# ---------------------------------------------------------------- main
case "${1:-}" in
sh-parse)
    # sh-parse SCRIPT LOG RC: result lines of one shell test log (self-test)
    parse_sh "$2" "$3" "$4"; exit $?
    ;;
compare)
    shift; kinds=""; res=""
    while [ $# -gt 0 ]; do
        case "$1" in --kinds) kinds="$2"; shift 2 ;; --results) res="$2"; shift 2 ;; *) echo "unknown argument $1" >&2; exit 2 ;; esac
    done
    compare "$kinds" "$res"; exit $?
    ;;
playwright)
    shift; rep=""; res="/dev/stdout"
    while [ $# -gt 0 ]; do
        case "$1" in --report) rep="$2"; shift 2 ;; --results) res="$2"; shift 2 ;; *) echo "unknown argument $1" >&2; exit 2 ;; esac
    done
    playwright_results "$rep" >"$res"; exit $?
    ;;
esac

kinds="go,sh,vitest"; out=""
while [ $# -gt 0 ]; do
    case "$1" in --kinds) kinds="$2"; shift 2 ;; --out) out="$2"; shift 2 ;; *) echo "unknown argument $1" >&2; exit 2 ;; esac
done
out="${out:-$(mktemp -d)}"; mkdir -p "$out"
results="$out/results.txt"; : >"$results"
case ",$kinds," in *,go,*) echo "== go"; run_go "$out" "$results" ;; esac
case ",$kinds," in *,sh,*) echo "== sh"; run_sh "$out" "$results" ;; esac
case ",$kinds," in *,vitest,*) echo "== vitest"; run_vitest "$out" "$results" ;; esac
echo "Ergebnisse: $results"
compare "$kinds" "$results"
