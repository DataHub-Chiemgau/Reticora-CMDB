#!/usr/bin/env bash
#
# rep01-structure.test.sh — structure and gate checks of gate G1 (WP-213,
# WP-451):
#   REP-01   repository structure (internal/{notify,jobs,locations,pii,reviews,
#            rules}, api/rules.schema.json, docs/acceptance, docs/decisions,
#            docs/schema-baseline.md; pii after E-15);
#   GATE-01  an open Critical finding in the security review or the pentest
#            summary (every finding table including the annex; severity
#            normalised: bold, case, Kritisch/Hoch) keeps GATE-01 and the gate
#            result from PASS and 'bestanden';
#   GATE-02  every [B]/[Q] ID that docs/acceptance/g1-gate.md carries as N/P,
#            WARN or offen has a reason, and every traceability row pointing to
#            the checklist names an ID listed there; the IDs of
#            docs/acceptance/g1-ausnahmen.csv are not PASS, no status of the
#            annex names a declaration (E-76); an open SR/PT finding that
#            names a catalog ID keeps that ID from PASS; GATE-02 is PASS only
#            when every ID is;
#   GATE-03  an open High/Critical pentest finding keeps GATE-03 from PASS;
#            TEN-09 is PASS only once the review records the rack site-scope
#            bypass (F-040); PASS of an ID bound to a delivery needs the
#            delivery (A-07, A-11, A-02; made unconditional by WP-453 to
#            WP-457);
#   GATE-05  no [O] ID has a traceability row of the gate;
#   SEQ-01   the checklist documents the order per epic (E-07);
#   DOD-01   the PR template carries the Definition of Done;
#   ABN-01   the frozen acceptance artefacts are unchanged (WP-212);
#   GLO-11   the traceability check is a blocking CI job and blocks missing
#            [B]/[Q] IDs (E-30);
#   and every workflow under .github/workflows is valid YAML.
#
# The normal mode checks consistency: open findings may stand in review and
# pentest as long as the checklist does not claim PASS against them. --final
# (gate decision, WP-595/WP-600) additionally demands no open Critical in
# review and pentest, no open High/Critical in the pentest, every GATE row
# PASS and no exception of kind erklaerung or luecke.
#
# The fixture self-tests (rep01_*, tests/fixtures/rep01/) prove that the gate
# checks catch every format variant of an open finding and each consistency
# rule; they run in both modes.
#
# The traceability rows themselves are checked by
# backend/internal/traceability (TestTraceabilityFile).
#
# Usage: ./tests/rep01-structure.test.sh [--final]
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

MODE=normal
case "${1:-}" in
    "") ;;
    --final) MODE=final ;;
    *) echo "usage: $0 [--final]" >&2; exit 2 ;;
esac

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

# --- Gate consistency (WP-451) ----------------------------------------------
# gate_check ROOT MODE checks the gate documents below ROOT and prints one
# line "<rule><TAB><detail>" per violation. The documents are data: they are
# parsed with awk/grep, never passed to eval.
#
# Conventions read here (documented in docs/acceptance/g1-gate.md):
# - Annex "## Anhang …" of the checklist: | ID | Tag | Status | Nachweis | WP |;
#   the status of an ID is PASS when the cell starts with PASS.
# - Result: the first line "**Ergebnis: offen**" or "**Ergebnis: bestanden**"
#   of section "## Ergebnis"; a section status is its line "**Status:** …".
# - A finding row is any table row of review or pentest with a severity cell
#   (Critical/Kritisch, High/Hoch, Medium/Mittel, Low/Niedrig, Info; bold and
#   case ignored); the next cell is its status. "behoben", "geschlossen" and
#   "G1-Anteil geschlossen" close it.
# - An open SR/PT finding names every catalog ID in its row. Such an ID is not
#   PASS, unless the finding is below High and section "Abgleich offener
#   Befunde" of the checklist gives a reason (| Befund | ID | Begründung |,
#   at least five words) why the finding does not touch the requirement.

# section FILE ERE prints the body of the first section whose heading matches,
# up to the next heading of the same or a higher level.
section() {
    [ -f "$1" ] || return 0
    awk -v re="$2" '
        /^#+ / {
            lvl = index($0, " ") - 1
            if (inside && lvl <= level) exit
            if (!inside && $0 ~ re) { inside = 1; level = lvl; next }
        }
        inside { print }' "$1"
}

# finding_rows TAG FILE prints TAG, ID, severity (critical, high, medium,
# low, info), open (1) or closed (0) and the row for every finding row.
finding_rows() {
    [ -f "$2" ] || return 0
    awk -F'|' -v tag="$1" '
        function trim(s) { gsub(/\*/, "", s); gsub(/^[ \t]+|[ \t]+$/, "", s); return s }
        /^[ \t]*\|/ {
            sev = ""
            for (i = 3; i < NF; i++) {
                c = tolower(trim($i))
                if (c ~ /^(critical|kritisch|high|hoch|medium|mittel|low|niedrig|info)$/) {
                    sev = c; st = tolower(trim($(i + 1))); break
                }
            }
            if (sev == "") next
            if (sev == "kritisch") sev = "critical"
            if (sev == "hoch") sev = "high"
            if (sev == "mittel") sev = "medium"
            if (sev == "niedrig") sev = "low"
            open = (st ~ /^(behoben|geschlossen|g1-anteil geschlossen)/) ? 0 : 1
            id = trim($2); if (id == "") id = "-"
            printf "%s\t%s\t%s\t%d\t%s\n", tag, id, sev, open, $0
        }' "$2"
}

# word TEXT: first word of a status, lower case, without bold and trailing
# punctuation. Pure bash, it runs for every status.
word() {
    local w="${1//\*/}" punct='[.,;:]'
    w="${w#"${w%%[![:space:]]*}"}"
    w="${w%%[[:space:]]*}"
    while [[ "$w" == *$punct ]]; do w="${w%?}"; done
    printf '%s' "${w,,}"
}

# is_test_file PATH: the test-file forms of backend/internal/traceability
# (IsTestFile).
is_test_file() {
    case "$1" in
        *_test.go|*.test.ts|*.test.tsx|*.spec.ts|*.tftest.hcl|tests/*.sh|tests/*.js|tests/*.ts) return 0 ;;
    esac
    return 1
}

gate_check() {
    local root="$1" mode="$2"
    local G="$root/docs/acceptance/g1-gate.md" X="$root/docs/acceptance/g1-ausnahmen.csv"
    local RV="$root/docs/security/review-g1.md" PT="$root/docs/security/pentest-g1.md"
    local SUB="$root/docs/legal/subprozessoren.md" SUMS="$root/docs/acceptance/artefakte/SHA256SUMS"
    local V2="$root/docs/spec/v2/aut-05-aut-06-net-04.md"
    local annex findings g03 body xs res crit pthc pentest ten09
    local id s n fid sev open row cid xid xart f p d line field tf rack notpass rows
    # STATUS: annex status per ID (first row), XART: kind of exception,
    # SEC: "**Status:**" of the GATE sections, REASON: words of the reason in
    # the first row of an ID in the GATE-02 tables, RECON: words of the reason
    # per "<finding> <ID>" of "Abgleich offener Befunde".
    local -A STATUS=() XART=() SEC=() REASON=() RECON=()

    v() { printf '%s\t%s\n' "$1" "$2"; }
    is_pass() { [[ "${STATUS[$1]-}" == PASS* ]]; }
    np_exception() { [ "${XART[$1]-}" = np ]; }

    annex="$(section "$G" '^## Anhang' | grep -E '^\| *[A-Z]{2,5}-[0-9]{2} *\|' | awk -F'|' '
        { id = $2; s = $4; gsub(/^[ \t]+|[ \t]+$/, "", id); gsub(/\*/, "", s); gsub(/^[ \t]+|[ \t]+$/, "", s)
          print id "\t" s }')"
    while IFS=$'\t' read -r id s; do
        [ -n "$id" ] && [ -z "${STATUS[$id]+x}" ] && STATUS[$id]="$s"
    done <<< "$annex"
    xs="$( { [ -f "$X" ] && tail -n +2 "$X"; } | awk -F';' 'NF >= 2 { print $1 "\t" $2 }')"
    while IFS=$'\t' read -r id s; do
        [ -n "$id" ] && XART[$id]="$s"
    done <<< "$xs"
    # GATE-03 conditions: condition <TAB> status.
    g03="$(section "$G" '^## GATE-03' | awk -F'|' '
        /^\|/ && !/^\| *Bedingung *\|/ && !/^\|[-|: ]+$/ {
            c = $2; s = $3; gsub(/^[ \t]+|[ \t]+$/, "", c); gsub(/\*/, "", s); gsub(/^[ \t]+|[ \t]+$/, "", s)
            print c "\t" s }')"
    pentest="$(awk -F'\t' '$1 ~ /Pentest/ { print $2; exit }' <<< "$g03")"
    ten09="$(awk -F'\t' '$1 ~ /TEN-09/ { print $2; exit }' <<< "$g03")"
    # Rows of the "| ID | Status | Begründung |" tables of GATE-02: ID <TAB>
    # status <TAB> words of the reason.
    body="$(section "$G" '^## GATE-02' | awk -F'|' '
        /^\| *ID *\| *Status *\|/ { t = 1; next }
        /^\|[-|: ]+$/ { next }
        /^\|/ { if (t) { id = $2; s = $3; gsub(/^[ \t]+|[ \t]+$/, "", id); gsub(/\*/, "", s); gsub(/^[ \t]+|[ \t]+$/, "", s)
                         n = 0; m = split($4, w, /[ \t]+/); for (k = 1; k <= m; k++) if (w[k] != "") n++
                         print id "\t" s "\t" n }
                next }
        { t = 0 }')"
    while IFS=$'\t' read -r id s n; do
        [ -n "$id" ] && [ -z "${REASON[$id]+x}" ] && REASON[$id]="$n"
    done <<< "$body"
    while IFS=$'\t' read -r f id n; do
        [ -n "$f" ] && RECON["$f $id"]="$n"
    done < <(section "$G" '^#+ Abgleich offener Befunde' | grep -E '^\| *\**(SR|PT)-[0-9]+' | awk -F'|' '
        { f = $2; i = $3; gsub(/[ *]/, "", f); gsub(/[ *]/, "", i)
          n = 0; m = split($4, w, /[ \t]+/); for (k = 1; k <= m; k++) if (w[k] != "") n++
          print f "\t" i "\t" n }')
    while IFS=$'\t' read -r id s; do
        [ -n "$id" ] && SEC[$id]="$s"
    done < <( [ -f "$G" ] && awk '
        /^## / { sec = ""; if ($0 ~ /^## GATE-0[0-9]/) sec = substr($0, 4, 7); next }
        sec != "" && /^\*\*Status:\*\*/ { s = $0; sub(/^\*\*Status:\*\* */, "", s); print sec "\t" s; sec = "" }' "$G")
    res="$(section "$G" '^## Ergebnis' | grep -m1 -E '^\*\*Ergebnis: ' | sed -E 's/^\*\*Ergebnis: *([[:alpha:]]+).*/\1/')"
    res="${res,,}"
    findings="$(finding_rows R "$RV"; finding_rows P "$PT")"
    crit="$(awk -F'\t' '$3 == "critical" && $4 == 1 { print $2 }' <<< "$findings" | sort -u | tr '\n' ' ')"
    pthc="$(awk -F'\t' '$1 == "P" && ($3 == "critical" || $3 == "high") && $4 == 1 { print $2 }' <<< "$findings" | sort -u | tr '\n' ' ')"

    # GATE rows that are not PASS (N/P only with an np exception).
    gate_rows_not_pass() {
        local id s
        for id in "${!STATUS[@]}"; do
            [[ "$id" == GATE-* ]] || continue
            s="${STATUS[$id]}"
            [[ "$s" == PASS* ]] && continue
            [[ "$s" == N/P* ]] && np_exception "$id" && continue
            printf '%s\n' "$id"
        done | sort | tr '\n' ' '
        while IFS=$'\t' read -r id s; do
            [ -n "$id" ] || continue
            [ "$(word "$s")" = pass ] || printf "GATE-03 '%s' " "$id"
        done <<< "$g03"
        for id in GATE-01 GATE-02 GATE-03 GATE-05; do
            s="${SEC[$id]-}"
            [ -z "$s" ] || [ "$(word "$s")" = pass ] || printf 'section %s ' "$id"
        done
    }

    # The result is stated.
    case "$res" in
        offen|bestanden) ;;
        *) v result "section Ergebnis has no line '**Ergebnis: offen**' or '**Ergebnis: bestanden**'" ;;
    esac

    # GATE-01: an open Critical keeps GATE-01 and the result from PASS.
    if [ -n "$crit" ]; then
        is_pass GATE-01 && v gate01_critical "GATE-01 is PASS with open Critical findings: $crit"
        [ "$(word "${SEC[GATE-01]-}")" = pass ] && v gate01_critical "section GATE-01 is PASS with open Critical findings: $crit"
        [ "$res" = bestanden ] && v gate01_critical "the result is 'bestanden' with open Critical findings: $crit"
    fi

    # GATE-03: an open High/Critical pentest finding keeps GATE-03 from PASS.
    if [ -n "$pthc" ]; then
        is_pass GATE-03 && v gate03_pentest "GATE-03 is PASS with open High/Critical pentest findings: $pthc"
        [ "$(word "$pentest")" = pass ] && v gate03_pentest "GATE-03 'Pentest' is PASS with open High/Critical pentest findings: $pthc"
    fi
    if is_pass GATE-03; then
        while IFS=$'\t' read -r id s; do
            [ -n "$id" ] || continue
            [ "$(word "$s")" = pass ] || v gate03_rows "GATE-03 is PASS, condition '$id' is '$s'"
        done <<< "$g03"
    fi

    # An open SR/PT finding keeps the catalog IDs it names from PASS.
    while IFS=$'\t' read -r _ fid sev open row; do
        [ "${open:-0}" = 1 ] || continue
        [[ "$fid" =~ ^(SR|PT)-[0-9]+$ ]] || continue
        while IFS= read -r cid; do
            [ -n "$cid" ] || continue
            is_pass "$cid" || continue
            if [ "$sev" != critical ] && [ "$sev" != high ] && [ "${RECON["$fid $cid"]:-0}" -ge 5 ]; then
                continue
            fi
            v finding_ids "$cid is PASS while $fid ($sev) is open and names it"
        done < <(grep -oE '\b[A-Z]{2,5}-[0-9]{2}\b' <<< "$row" | grep -vE '^(SR|PT|WP)-' | sort -u)
    done <<< "$findings"

    # E-76: the IDs of the exception list are not PASS.
    while IFS=$'\t' read -r xid xart; do
        [ -n "$xid" ] || continue
        s="${STATUS[$xid]-}"
        if [ -z "$s" ]; then
            v exceptions "$xid ($xart) has no row in the annex"
        elif [[ "$s" == PASS* ]]; then
            v exceptions "$xid ($xart) is PASS"
        fi
    done <<< "$xs"

    # No PASS by declaration; no declaration in the status column of the annex.
    while IFS=$'\t' read -r id s; do
        [[ "$s" == *rklärung* ]] && v declaration "annex status of $id names a declaration: $s"
    done <<< "$annex"
    while IFS= read -r line; do
        [ -n "$line" ] && v declaration "status PASS by declaration: $line"
    done < <( [ -f "$G" ] && awk -F'|' '
        /^## Anhang/ { exit }
        /^\|/ { for (i = 2; i < NF; i++) { c = $i; gsub(/\*/, "", c); gsub(/^[ \t]+|[ \t]+$/, "", c)
                                           if (c ~ /^PASS/ && c ~ /rklärung/) print c } }' "$G")

    # GATE-02 is PASS only when every [B]/[Q] ID is.
    if is_pass GATE-02; then
        notpass="$(for id in "${!STATUS[@]}"; do
            s="${STATUS[$id]}"
            [[ "$s" == PASS* ]] && continue
            [[ "$s" == N/P* ]] && np_exception "$id" && continue
            printf '%s\n' "$id"
        done | sort | tr '\n' ' ')"
        [ -n "$notpass" ] && v gate02_all "GATE-02 is PASS, not PASS: $notpass"
    fi

    # The result is 'bestanden' only when every GATE row is PASS.
    if [ "$res" = bestanden ]; then
        rows="$(gate_rows_not_pass)"
        [ -n "$rows" ] && v result_rows "the result is 'bestanden', not PASS: $rows"
    fi

    # Every ID that is not PASS has a reason in section GATE-02.
    while IFS=$'\t' read -r id s; do
        [ -n "$id" ] || continue
        [[ "$s" == PASS* ]] && continue
        [ "${REASON[$id]:-0}" -ge 3 ] || v reasons "$id is '$s' without a reason in section GATE-02"
    done <<< "$annex"

    # Section statuses agree with the annex.
    while IFS=$'\t' read -r id s n; do
        [ -n "$id" ] || continue
        d="${STATUS[$id]-}"
        if [ -z "$d" ]; then
            v agree "$id of section GATE-02 has no row in the annex"
        elif [ "$(word "$s")" != "$(word "$d")" ]; then
            v agree "$id is '$s' in section GATE-02, '$d' in the annex"
        fi
    done <<< "$body"
    for id in GATE-01 GATE-02 GATE-03 GATE-05; do
        s="${SEC[$id]-}"
        [ -n "$s" ] || continue
        [ "$(word "$s")" = "$(word "${STATUS[$id]-}")" ] || v agree "section $id is '$s', the annex '${STATUS[$id]-}'"
    done
    if [ -n "$ten09" ] && [ "$(word "$ten09")" != "$(word "${STATUS[TEN-09]-}")" ]; then
        v agree "GATE-03 'TEN-09' is '$ten09', the annex '${STATUS[TEN-09]-}'"
    fi

    # F-040: TEN-09 is PASS only once the review records the rack site-scope
    # bypass.
    rack="$(awk -F'\t' '$2 ~ /^(SR|PT)-[0-9]+$/ { print $5 }' <<< "$findings" | grep -i 'rack' | grep -iE 'site[- ]?scope|TEN-09' || true)"
    if [ -z "$rack" ]; then
        is_pass TEN-09 && v rack_site_scope "TEN-09 is PASS, but no review or pentest finding records the rack site-scope bypass (F-002)"
        [ "$(word "$ten09")" = pass ] && v rack_site_scope "GATE-03 'Isolationstest (TEN-09)' is PASS, but no finding records the rack site-scope bypass (F-002)"
    fi

    # Deliveries (conditional; WP-453 to WP-457 make them unconditional).
    if is_pass GATE-03 || is_pass NFR-08 || [ "$(word "$pentest")" = pass ]; then
        if [ ! -f "$PT" ]; then
            v delivery_pentest "docs/security/pentest-g1.md is missing (A-07)"
        else
            grep -q 'nachzureichen' "$PT" && v delivery_pentest "docs/security/pentest-g1.md has items 'nachzureichen' (A-07)"
            grep -qE '^\| *ID *\| *(Severity|Schweregrad) *\| *Status *\|' "$PT" ||
                v delivery_pentest "docs/security/pentest-g1.md has no finding list | ID | Severity | Status | (A-07)"
            for field in 'Dienstleister' 'Zeitraum|Termin' 'Nachtest'; do
                line="$(grep -E "^\| *[^|]*(${field})[^|]*\|" "$PT" | head -n 1)"
                if [ -z "$line" ] || grep -qiE 'nachzureichen|Erklärung' <<< "$line"; then
                    v delivery_pentest "docs/security/pentest-g1.md: '${field}' is not filled with evidence (A-07)"
                fi
            done
        fi
    fi
    if is_pass NFR-09; then
        f="$(section "$SUB" '^## Liste' | grep -E '^\|' | tail -n +3 | awk -F'|' '
            { s = $2; r = $5; a = $6; gsub(/[ *]/, "", s); gsub(/ /, "", r); gsub(/ /, "", a)
              if (s != "" && s != "–" && s != "-" && r != "" && r != "–" && a != "" && a != "–") print }')"
        if [ -z "$f" ] || grep -q 'vom Betreiber zu liefern' "$SUB" 2>/dev/null; then
            v delivery_subprocessors "NFR-09 is PASS, docs/legal/subprozessoren.md lists no subprocessor with region and AVV (A-11)"
        fi
    fi
    if is_pass ABN-01; then
        for f in abn-01-a-cmdb.md abn-01-b-system.md; do
            p=""
            for d in docs/acceptance docs/acceptance/artefakte; do
                [ -s "$root/$d/$f" ] && p="$d/$f"
            done
            if [ -z "$p" ] || ! grep -qF "$p" "$SUMS" 2>/dev/null; then
                v delivery_abn01 "ABN-01 is PASS, $f is not frozen (present and in SHA256SUMS, A-02)"
            fi
        done
    fi
    for id in AUT-05 AUT-06 NET-04; do
        is_pass "$id" || continue
        grep -qE "^#{2,} +${id}\b" "$V2" 2>/dev/null ||
            v delivery_v2 "$id is PASS, docs/spec/v2/aut-05-aut-06-net-04.md has no section $id (A-02)"
        tf=""
        row="$(section "$G" '^#+ Abgleich v2-Texte' | grep -E "^\| *${id} *\|" | head -n 1)"
        # shellcheck disable=SC2016 # literal backticks of markdown code spans
        while IFS= read -r p; do
            [ -n "$p" ] && is_test_file "$p" && [ -f "$root/$p" ] && tf="$p"
        done < <(grep -oE '`[^`]+`' <<< "$row" | tr -d '`')
        [ -n "$tf" ] || v delivery_v2 "$id is PASS without a row in section 'Abgleich v2-Texte' naming an existing test file"
    done

    [ "$mode" = final ] || return 0
    [ -n "$crit" ] && v final_critical "open Critical findings in review or pentest: $crit"
    [ -n "$pthc" ] && v final_pentest "open High/Critical pentest findings: $pthc"
    rows="$(gate_rows_not_pass)"
    [ -n "$rows" ] && v final_gate_rows "not PASS: $rows"
    rows="$(awk -F'\t' '$2 == "erklaerung" || $2 == "luecke" { printf "%s (%s) ", $1, $2 }' <<< "$xs")"
    [ -n "$rows" ] && v final_exceptions "exceptions of kind erklaerung or luecke: $rows"
    return 0
}

GATE_RULES=(result gate01_critical gate03_pentest gate03_rows finding_ids exceptions declaration gate02_all
    result_rows reasons agree rack_site_scope delivery_pentest delivery_subprocessors delivery_abn01 delivery_v2)
FINAL_RULES=(final_critical final_pentest final_gate_rows final_exceptions)
describe() {
    case "$1" in
        result) echo "the checklist states the gate result (offen or bestanden)" ;;
        gate01_critical) echo "GATE-01 an open Critical finding (review, pentest, annex) keeps GATE-01 and the result from PASS" ;;
        gate03_pentest) echo "GATE-03 an open High/Critical pentest finding keeps GATE-03 from PASS" ;;
        gate03_rows) echo "GATE-03 is PASS only when every condition is" ;;
        finding_ids) echo "GATE-02 an open SR/PT finding keeps the catalog IDs it names from PASS" ;;
        exceptions) echo "GATE-02 the IDs of docs/acceptance/g1-ausnahmen.csv are not PASS (E-76)" ;;
        declaration) echo "GATE-02 no ID is PASS by declaration, no annex status names one (E-76)" ;;
        gate02_all) echo "GATE-02 is PASS only when every [B]/[Q] ID is" ;;
        result_rows) echo "the result is 'bestanden' only when every GATE row is PASS" ;;
        reasons) echo "GATE-02 every ID that is not PASS has a reason" ;;
        agree) echo "the section statuses agree with the annex" ;;
        rack_site_scope) echo "GATE-03 TEN-09 is PASS only once the rack site-scope bypass is a recorded finding (F-040)" ;;
        delivery_pentest) echo "GATE-03 PASS of GATE-03/NFR-08 needs the complete pentest summary (A-07)" ;;
        delivery_subprocessors) echo "GATE-02 PASS of NFR-09 needs the subprocessor list (A-11)" ;;
        delivery_abn01) echo "GATE-02 PASS of ABN-01 needs the frozen texts (a) and (b) (A-02)" ;;
        delivery_v2) echo "GATE-02 PASS of AUT-05/AUT-06/NET-04 needs the v2 texts and their comparison (A-02)" ;;
        final_critical) echo "--final no open Critical finding in review and pentest" ;;
        final_pentest) echo "--final no open High/Critical pentest finding" ;;
        final_gate_rows) echo "--final every GATE row is PASS" ;;
        final_exceptions) echo "--final no exception of kind erklaerung or luecke" ;;
    esac
}

gate_out="$(gate_check . "$MODE")"
rules=("${GATE_RULES[@]}")
[ "$MODE" = final ] && rules+=("${FINAL_RULES[@]}")
for rule in "${rules[@]}"; do
    hits="$(awk -F'\t' -v r="$rule" '$1 == r { print $2 }' <<< "$gate_out")"
    if [ -z "$hits" ]; then
        ok "$(describe "$rule")"
    else
        while IFS= read -r hit; do fail "$(describe "$rule"): $hit"; done <<< "$hits"
    fi
done

# --- Fixture self-tests (WP-451) --------------------------------------------
# Each case copies a fixture of tests/fixtures/rep01, changes it and runs the
# gate checks in both modes. Fixture "final" is a complete gate (every GATE
# row PASS), fixture "open" the transitional state of E-76 (GATE-01 to
# GATE-03 offen). WANT is "pass" or the rule that must report, "-" skips a
# mode.
FIX=tests/fixtures/rep01
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
CASES=0

append() { printf '%s\n' "$2" >> "$1"; }
subst()  { sed -i -E "$2" "$1"; }
drop()   { grep -vE "$2" "$1" > "$1.new" || true; mv "$1.new" "$1"; }

# selftest NAME FIXTURE WANT-NORMAL WANT-FINAL [CHANGE ARGS...]
selftest() {
    local name="$1" base="$2" wn="$3" wf="$4" dir mode want got
    shift 4
    CASES=$((CASES + 1))
    dir="$TMP_ROOT/$CASES"
    if [ ! -d "$FIX/$base" ]; then
        fail "rep01_${name}: fixture $FIX/$base exists"
        return
    fi
    cp -r "$FIX/$base" "$dir"
    if [ $# -gt 0 ]; then
        (cd "$dir" && "$@")
        if diff -rq "$FIX/$base" "$dir" >/dev/null; then
            fail "rep01_${name}: the change '$*' alters the fixture"
            return
        fi
    fi
    for mode in normal final; do
        if [ "$mode" = normal ]; then want="$wn"; else want="$wf"; fi
        [ "$want" = - ] && continue
        got="$(gate_check "$dir" "$mode" | cut -f1 | sort -u | tr '\n' ' ')"
        if [ "$want" = pass ] && [ -z "$got" ]; then
            ok "rep01_${name} (${mode}): the gate checks pass"
        elif [ "$want" != pass ] && grep -qw -- "$want" <<< "$got"; then
            ok "rep01_${name} (${mode}): the gate checks report ${want}"
        else
            fail "rep01_${name} (${mode}): want ${want}, got: ${got:-pass}"
        fi
    done
}

R=docs/security/review-g1.md
P=docs/security/pentest-g1.md
C=docs/acceptance/g1-gate.md

# rep01_control: the repository passes the gate checks in normal mode.
if [ -z "$(gate_check . normal)" ]; then
    ok "rep01_control: the repository passes the gate checks in normal mode"
else
    fail "rep01_control: the repository passes the gate checks in normal mode"
fi
selftest fixture_final final pass pass
selftest fixture_open  open  pass final_exceptions

# F-300: every format of an open Critical (review, pentest, annex) and of an
# open pentest High fails a gate with GATE-01/GATE-03 PASS; with GATE-01 and
# GATE-03 offen it is consistent, --final still fails.
probe() {
    local name="$1" file="$2" row="$3" rule="$4" final_rule="$5"
    selftest "$name" final "$rule" "$final_rule" append "$file" "$row"
    selftest "${name}_open" open pass "$final_rule" append "$file" "$row"
}
probe pt_critical      "$P" '| PT-91 | Critical | offen | probe | WP-999 |'     gate01_critical final_critical
probe pt_bold_critical "$P" '| PT-92 | **Critical** | offen | probe | WP-999 |' gate01_critical final_critical
probe pt_kritisch      "$P" '| PT-93 | Kritisch | offen | probe | WP-999 |'     gate01_critical final_critical
probe pt_lowercase     "$P" '| PT-94 | critical | offen | probe | WP-999 |'     gate01_critical final_critical
probe pt_high          "$P" '| PT-95 | High | offen | probe | WP-999 |'         gate03_pentest  final_pentest
probe pt_hoch          "$P" '| PT-96 | Hoch | offen | probe | WP-999 |'         gate03_pentest  final_pentest
probe pt_bold_high     "$P" '| PT-90 | **HIGH** | **offen** | probe | WP-999 |' gate03_pentest  final_pentest
probe sr_critical      "$R" '| SR-90 | Critical | offen | probe | WP-999 |'     gate01_critical final_critical
probe sr_bold_critical "$R" '| SR-91 | **Critical** | offen | probe | WP-999 |' gate01_critical final_critical
probe sr_bold_id       "$R" '| **SR-92** | Kritisch | offen | probe | WP-999 |' gate01_critical final_critical
probe annex_critical   "$R" '| TEN-09 | [B] | Critical | offen | WP-208 |'     gate01_critical final_critical
selftest pt_fixed  final pass pass append "$P" '| PT-97 | Critical | behoben | probe | WP-999 |'
selftest pt_closed final pass pass append "$P" '| PT-98 | High | geschlossen | probe | WP-999 |'
selftest sr_fixed  final pass pass append "$R" '| SR-93 | **Critical** | **behoben** (WP-999) | probe | WP-999 |'
selftest annex_g1_part final pass pass append "$R" '| AST-09 | [P2] | Critical | G1-Anteil geschlossen, Rest verschoben (E-03) | WP-999 |'
critical_but_bestanden() {
    subst "$C" 's/^\*\*Ergebnis: offen/**Ergebnis: bestanden/'
    append "$P" '| PT-89 | Critical | offen | probe | WP-999 |'
}
selftest result_critical open gate01_critical - critical_but_bestanden
# F-038 (known_defect_SR-11, generalised): an open finding naming CI-13 keeps
# CI-13 from PASS; a finding below High can be reconciled, High cannot.
selftest finding_names_id final finding_ids - append "$R" '| SR-11 | Low | offen | „reaktivieren“ aus CI-13 ist in der Produktionsverdrahtung nicht nutzbar. | Folge-WP |'
selftest finding_reconciled_missing final finding_ids - drop "$C" '^\| SR-08 \| NFR-03 \|'
selftest finding_reconciled_high final finding_ids - subst "$R" 's/^\| SR-08 \| Low \|/| SR-08 | High |/'
selftest finding_reconciled_short final finding_ids - subst "$C" 's/^\| SR-08 \| NFR-03 \| .*/| SR-08 | NFR-03 | erfüllt |/'
selftest finding_closed final pass pass subst "$R" 's/^\| SR-08 \| Low \| offen \|/| SR-08 | Low | behoben |/'
# F-040: TEN-09 PASS needs the recorded rack site-scope finding.
selftest rack_site_scope final rack_site_scope - drop "$R" '^\| SR-12 \|'
selftest rack_site_scope_open open rack_site_scope - subst "$C" 's/^\| TEN-09 \| \[Q\] \| offen/| TEN-09 | [Q] | PASS/'
# F-120: no PASS by declaration, exceptions are not PASS.
selftest pass_by_declaration final declaration - subst "$C" 's/^\| NFR-08 \| \[B\] \| PASS/| NFR-08 | [B] | PASS (Erklärung des Auftraggebers)/'
selftest declaration_in_status open declaration - subst "$C" 's/^\| AUT-05 \| \[B\] \| offen/| AUT-05 | [B] | offen (Erklärung des Auftraggebers)/'
selftest declaration_in_section open declaration - subst "$C" 's/^\| NFR-08 \| offen \|/| NFR-08 | PASS (Erklärung des Auftraggebers) |/'
selftest exception_pass final exceptions - append docs/acceptance/g1-ausnahmen.csv 'NFR-08;erklaerung;Erklärung des Auftraggebers, Nachweis ausstehend;E-76;A-07'
selftest exception_unknown open exceptions - append docs/acceptance/g1-ausnahmen.csv 'NFR-07;luecke;Fixture-Lücke ohne Zeile im Anhang;E-76;WP-999'
# Consistency of GATE-02, the result and the sections.
selftest gate02_all final gate02_all - subst "$C" 's/^\| CI-13 \| \[B\] \| PASS/| CI-13 | [B] | offen/'
selftest reasons open reasons - drop "$C" '^\| CI-13 \| offen \|'
selftest result_missing final result - drop "$C" '^\*\*Ergebnis: '
selftest result_bestanden open result_rows - subst "$C" 's/^\*\*Ergebnis: offen/**Ergebnis: bestanden/'
selftest gate03_rows final gate03_rows - subst "$C" 's/^\| Install-Smoke auf beiden Plattformen \| PASS/| Install-Smoke auf beiden Plattformen | offen/'
selftest agree_section final agree - subst "$C" '/^## GATE-01/,/^## /{s/^\*\*Status:\*\* PASS\./**Status:** offen./}'
# Deliveries: PASS of a delivery-bound ID needs the delivery.
selftest delivery_pentest_pending final delivery_pentest - subst "$P" 's/^\| Nachtest \| .*/| Nachtest | nachzureichen |/'
selftest delivery_pentest_list final delivery_pentest - drop "$P" '^\| ID \| Severity \| Status \|'
selftest delivery_subprocessors final delivery_subprocessors - subst docs/legal/subprozessoren.md 's/^\| Fixture-Hoster .*/| – | *vom Betreiber zu liefern (A-11)* | – | – | – | – |/'
selftest delivery_abn01 final delivery_abn01 - drop docs/acceptance/artefakte/SHA256SUMS 'abn-01-b-system\.md'
selftest delivery_v2_section final delivery_v2 - drop docs/spec/v2/aut-05-aut-06-net-04.md '^## NET-04'
# shellcheck disable=SC2016 # literal backticks of markdown code spans
selftest delivery_v2_test final delivery_v2 - subst "$C" 's#^\| AUT-06 \| (.*)`tests/v2-abgleich\.test\.sh`#| AUT-06 | \1`tests/fehlt.test.sh`#'
# --final: exceptions of kind erklaerung or luecke and open GATE rows fail.
selftest final_gate_rows open pass final_gate_rows

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
