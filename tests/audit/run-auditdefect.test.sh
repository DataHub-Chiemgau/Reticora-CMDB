#!/usr/bin/env bash
#
# run-auditdefect.test.sh — self-test of the defect-test ratchet (WP-337,
# TST-04). Feeds fixture results to tests/audit/run-auditdefect.sh and checks
# the verdicts: a listed test that turned green, an unlisted red test, an
# orphaned or skipped line and an aborted test fail the run; a consistent
# result passes. Also checks how shell test logs and Playwright reports are
# turned into result lines.
#
# Run: ./tests/audit/run-auditdefect.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
RUN=tests/audit/run-auditdefect.sh

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/     /' | head -n 8; return 0; }

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

cat >"$TMP/expected.txt" <<'EOF'
# art;ort;test;befund
go;backend/internal/x;TestRed;F-001
sh;tests/audit/a.test.sh;case one;F-002
vitest;frontend/src/A.auditdefect.tsx;suite red case;F-003
e2e-real;chromium:frontend/e2e-real/a.spec.ts;A01 red;F-004
EOF

consistent() {
    cat <<'EOF'
go;backend/internal/x;TestRed;fail;
go;backend/internal/x;TestHelperMain;pass;
sh;tests/audit/a.test.sh;case one;fail;
sh;tests/audit/a.test.sh;control;pass;
vitest;frontend/src/A.auditdefect.tsx;suite red case;fail;
EOF
}

# check NAME WANT_RC KINDS RESULTS [PATTERN]
check() {
    local name="$1" want="$2" kinds="$3" results="$4" pattern="${5:-}" out rc
    out=$(EXPECTED="$TMP/expected.txt" bash "$RUN" compare --kinds "$kinds" --results "$results" 2>&1); rc=$?
    if { [ "$want" = 0 ] && [ "$rc" = 0 ]; } || { [ "$want" != 0 ] && [ "$rc" != 0 ]; }; then
        if [ -n "$pattern" ] && ! printf '%s\n' "$out" | grep -q "$pattern"; then
            fail "$name: rc $rc, but no line matches '$pattern'" "$out"
        else
            ok "$name"
        fi
    else
        fail "$name: rc $rc, want $([ "$want" = 0 ] && echo 0 || echo '!= 0')" "$out"
    fi
}

consistent >"$TMP/r-consistent.txt"
check "consistent result passes (listed red, unlisted green, e2e-real ignored)" 0 go,sh,vitest "$TMP/r-consistent.txt" "3 erwartet rot, 0 Fehler"

sed 's/^go;backend\/internal\/x;TestRed;fail;/go;backend\/internal\/x;TestRed;pass;/' "$TMP/r-consistent.txt" >"$TMP/r-green.txt"
check "listed test turned green -> überführen" 1 go,sh,vitest "$TMP/r-green.txt" "überführen   go;backend/internal/x;TestRed"

{ consistent; echo "go;backend/internal/x;TestNewRed;fail;"; } >"$TMP/r-regress.txt"
check "unlisted red test -> Rückfall" 1 go,sh,vitest "$TMP/r-regress.txt" "Rückfall     go;backend/internal/x;TestNewRed"

grep -v '^vitest;' "$TMP/r-consistent.txt" >"$TMP/r-orphan.txt"
check "listed test missing from the run -> verwaist" 1 go,sh,vitest "$TMP/r-orphan.txt" "verwaist     vitest;frontend/src/A.auditdefect.tsx;suite red case"

sed 's/^sh;tests\/audit\/a.test.sh;case one;fail;/sh;tests\/audit\/a.test.sh;case one;skip;/' "$TMP/r-consistent.txt" >"$TMP/r-skip.txt"
check "listed test skipped -> nicht ausgeführt" 1 go,sh,vitest "$TMP/r-skip.txt" "nicht ausgeführt sh;tests/audit/a.test.sh;case one"

sed 's/^go;backend\/internal\/x;TestRed;fail;/go;backend\/internal\/x;TestRed;error;Build-Fehler im Paket/' "$TMP/r-consistent.txt" >"$TMP/r-error.txt"
check "aborted listed test (build failure) -> Abbruch" 1 go,sh,vitest "$TMP/r-error.txt" "Abbruch      go;backend/internal/x;TestRed"

grep '^go;' "$TMP/r-consistent.txt" >"$TMP/r-go.txt"
check "a run compares only its kinds (go only)" 0 go "$TMP/r-go.txt" "1 erwartet rot, 0 Fehler"

check "e2e-real line is compared by an e2e-real run (missing -> verwaist)" 1 e2e-real "$TMP/r-go.txt" "verwaist     e2e-real;chromium:frontend/e2e-real/a.spec.ts;A01 red"

printf 'go;backend/internal/x;TestRed;F-001\ngo;backend/internal/x;TestRed;F-001\n' >"$TMP/dup.txt"
out=$(EXPECTED="$TMP/dup.txt" bash "$RUN" compare --kinds go --results "$TMP/r-go.txt" 2>&1); rc=$?
if [ "$rc" != 0 ] && printf '%s' "$out" | grep -q 'doppelte Zeile'; then ok "duplicate line in the expected list is rejected"; else fail "duplicate line in the expected list is rejected (rc $rc)" "$out"; fi

# Shell logs: case = text before the first ": "; one FAIL makes the case red.
cat >"$TMP/sh.log" <<'EOF'
ok   control: the counter is exported
ok   acme: failed challenge keeps the placeholder
FAIL acme: fresh install serves the issued certificate
FAIL no alert fires on any increase
     indented detail line
3 passed, 2 failed
EOF
got=$(bash "$RUN" sh-parse tests/audit/a.test.sh "$TMP/sh.log" 1)
want=$'sh;tests/audit/a.test.sh;control;pass;\nsh;tests/audit/a.test.sh;acme;fail;\nsh;tests/audit/a.test.sh;no alert fires on any increase;fail;'
if [ "$got" = "$want" ]; then ok "shell log: two cases, the one with a FAIL line is red"; else fail "shell log: two cases, the one with a FAIL line is red" "$got"; fi

printf 'FAIL k8s_wal_archive: kustomize available (set KUSTOMIZE)\nok   other: fine\n' >"$TMP/sh-setup.log"
got=$(bash "$RUN" sh-parse tests/audit/a.test.sh "$TMP/sh-setup.log" 1)
if printf '%s\n' "$got" | grep -q '^sh;tests/audit/a.test.sh;<setup>;error;'; then ok "shell log: missing tool is a setup error"; else fail "shell log: missing tool is a setup error" "$got"; fi

printf 'some crash\n' >"$TMP/sh-empty.log"
got=$(bash "$RUN" sh-parse tests/audit/a.test.sh "$TMP/sh-empty.log" 2)
if printf '%s\n' "$got" | grep -q '<setup>;error;keine Fälle'; then ok "shell log without cases is a setup error"; else fail "shell log without cases is a setup error" "$got"; fi

# Playwright JSON -> e2e-real result lines.
cat >"$TMP/pw.json" <<'EOF'
{"suites":[{"title":"a.spec.ts","file":"a.spec.ts","suites":[{"title":"Group","specs":[
 {"title":"A01 red","file":"a.spec.ts","tests":[{"projectName":"chromium","status":"unexpected","results":[{"status":"failed"}]}]},
 {"title":"A02 green","file":"a.spec.ts","tests":[{"projectName":"chromium","status":"expected","results":[{"status":"passed"}]}]},
 {"title":"A03 skipped","file":"a.spec.ts","tests":[{"projectName":"chromium","status":"skipped","results":[]}]}]}]}]}
EOF
got=$(bash "$RUN" playwright --report "$TMP/pw.json")
want=$'e2e-real;chromium:frontend/e2e-real/a.spec.ts;Group A01 red;fail;\ne2e-real;chromium:frontend/e2e-real/a.spec.ts;Group A02 green;pass;\ne2e-real;chromium:frontend/e2e-real/a.spec.ts;Group A03 skipped;skip;'
if [ "$got" = "$want" ]; then ok "Playwright report becomes e2e-real result lines"; else fail "Playwright report becomes e2e-real result lines" "$got"; fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
