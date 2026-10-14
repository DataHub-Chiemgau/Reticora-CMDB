#!/usr/bin/env bash
#
# rep01-structure.test.sh — structure and gate checks of gate G1 (WP-213):
#   REP-01   repository structure (internal/{notify,jobs,locations,pii,reviews,
#            rules}, api/rules.schema.json, docs/acceptance, docs/decisions,
#            docs/schema-baseline.md; pii after E-15);
#   GATE-01  no open Critical in the security review and the pentest summary;
#   GATE-02  every [B]/[Q] ID that docs/acceptance/g1-gate.md carries as N/P
#            or WARN has a reason, and every traceability row pointing to the
#            checklist names an ID listed there;
#   GATE-05  no [O] ID has a traceability row of the gate;
#   SEQ-01   the checklist documents the order per epic (E-07);
#   DOD-01   the PR template carries the Definition of Done;
#   ABN-01   the frozen acceptance artefacts are unchanged (WP-212);
#   GLO-11   the traceability check is a blocking CI job and blocks missing
#            [B]/[Q] IDs (E-30);
#   and every workflow under .github/workflows is valid YAML.
#
# The traceability rows themselves are checked by
# backend/internal/traceability (TestTraceabilityFile).
#
# Usage: ./tests/rep01-structure.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }
check() { if eval "$2"; then ok "$1"; else fail "$1"; fi; }

GATE=docs/acceptance/g1-gate.md
TRACE=docs/traceability.csv

# --- REP-01 -----------------------------------------------------------------
for pkg in notify jobs locations pii reviews rules; do
    check "REP-01 backend/internal/${pkg} contains Go code" \
        "compgen -G 'backend/internal/${pkg}/*.go' >/dev/null"
done
check "REP-01 api/rules.schema.json is a JSON schema" \
    "python3 -c 'import json,sys; d=json.load(open(\"api/rules.schema.json\")); sys.exit(0 if \"\$schema\" in d else 1)'"
check "REP-01 docs/acceptance exists" "[ -d docs/acceptance ]"
check "REP-01 docs/decisions has an index" "[ -f docs/decisions/README.md ]"
for adr in docs/decisions/[0-9][0-9][0-9][0-9]-*.md; do
    check "REP-01 $(basename "$adr") is listed in docs/decisions/README.md" \
        "grep -qF '$(basename "$adr")' docs/decisions/README.md"
done
check "REP-01 docs/schema-baseline.md exists" "[ -s docs/schema-baseline.md ]"

# --- Gate checklist ---------------------------------------------------------
check "gate checklist ${GATE} exists" "[ -s ${GATE} ]"
for section in GATE-01 GATE-02 GATE-03 GATE-05 SEQ-01; do
    check "gate checklist has a section for ${section}" "grep -qE '^## .*${section}' ${GATE}"
done

# GATE-01: no Critical finding is open. Finding tables list
# "| ID | Severity | Status | …"; a Critical row must be fixed ("behoben").
# shellcheck disable=SC2034 # read by check through eval
open_critical=$(grep -hE '^\| *(SR|PT)-[0-9]+ *\| *Critical *\|' docs/security/review-g1.md docs/security/pentest-g1.md \
    | grep -viE '\| *\**behoben' || true)
check "GATE-01 no open Critical finding" "[ -z \"\$open_critical\" ]"

# GATE-02: every row of the N/P and WARN table carries a reason.
np_rows=$(sed -nE '/^#{2,3} N\/P und WARN/,/^#{2,3} [^N]/p' "$GATE" | grep -E '^\| *[A-Z]{2,5}-[0-9]{2} *\|' || true)
check "GATE-02 the checklist lists N/P and WARN IDs" "[ -n \"\$np_rows\" ]"
while IFS='|' read -r _ id status reason _; do
    id=$(echo "$id" | xargs)
    [ -z "$id" ] && continue
    # The table text is data: it is measured here, never passed to eval.
    status_words=$(wc -w <<< "$status")
    reason_words=$(wc -w <<< "$reason")
    check "GATE-02 ${id} has a status and a reason" \
        "[ ${status_words} -ge 1 ] && [ ${reason_words} -ge 3 ]"
done <<< "$np_rows"
while IFS=';' read -r id _; do
    check "GATE-02 traceability row ${id} -> ${GATE} names an ID listed there" \
        "grep -qE '^\| *${id} *\|' ${GATE}"
done < <(grep -F ";${GATE};" "$TRACE")

# GATE-05: [O] IDs never block a gate.
check "GATE-05 no [O] ID in the gate rows of docs/traceability.csv" \
    "! grep -E '^[A-Z]{2,5}-[0-9]{2};\[O\];G1;' ${TRACE} >/dev/null"
check "GATE-05 the traceability check selects only [B]/[Q] for the gate" \
    "grep -q 'gateTag = regexp.MustCompile' backend/internal/traceability/traceability.go && ! grep -E '^var gateTag.*O' backend/internal/traceability/traceability.go >/dev/null"

# SEQ-01: order per epic (E-07).
# shellcheck disable=SC2034 # read by check through eval
seq=$(sed -n '/^## .*SEQ-01/,/^## /p' "$GATE")
for step in 'Epic A' 'Epic B' 'Epic D' 'Epic C' 'Lasttest' 'Sicherheitsreview' 'G1'; do
    check "SEQ-01 the order names ${step}" "grep -qF '${step}' <<< \"\$seq\""
done

# ABN-01: the accepted scenarios stay frozen (WP-212).
check "ABN-01 protocol docs/acceptance/abn-01.md exists" "[ -s docs/acceptance/abn-01.md ]"
check "ABN-01 frozen artefacts match docs/acceptance/artefakte/SHA256SUMS" \
    "sha256sum --quiet -c docs/acceptance/artefakte/SHA256SUMS >/dev/null 2>&1"

# --- DOD-01, GLO-11 ---------------------------------------------------------
check "DOD-01 the PR template carries the Definition of Done" \
    "grep -q 'Definition of Done (DOD-01)' .github/pull_request_template.md && grep -q 'Traceability-Eintrag' .github/pull_request_template.md"
check "GLO-11 the CI job traceability runs TestTraceabilityFile" \
    "grep -q \"run 'TestTraceabilityFile'\" .github/workflows/ci.yml"
check "GLO-11 the CI job traceability is not allowed to fail" \
    "! sed -n '/^  traceability:/,/^  [a-z-]*:\$/p' .github/workflows/ci.yml | grep -q 'continue-on-error'"
check "GLO-11 missing [B]/[Q] IDs block the traceability check" \
    "grep -q 'MissingForGate(entries, catalog)' backend/internal/traceability/traceability_test.go"

# --- Workflows --------------------------------------------------------------
for wf in .github/workflows/*.yml; do
    check "workflow $(basename "$wf") is valid YAML with triggers" \
        "python3 -c 'import sys,yaml; d=yaml.safe_load(open(sys.argv[1])); sys.exit(0 if isinstance(d,dict) and (\"on\" in d or True in d) and d.get(\"jobs\") else 1)' '$wf'"
done

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
