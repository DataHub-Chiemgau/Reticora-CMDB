#!/usr/bin/env bash
#
# g1-gate-evidence.test.sh — Phase-1 acceptance audit, group T5, DEFECT tests
# for gate G1 evidence (fail until gate documents / gate checker are fixed;
# not wired into CI).
#
# Requirement IDs: GATE-01 [Q] "Kein offenes Critical im jeweiligen Gate",
# GATE-02 [Q] "Alle Anforderungen der Phase sind PASS (Nachweis per
# Test/Traceability)", GATE-03 [Q] "Pentest ohne offene High/Critical,
# Isolationstest (TEN-09) vollständig, Install-Smoke-Test auf beiden
# Plattformen", GLO-11, NFR-08, NFR-09, ABN-01, CI-13 [B], TEN-09 [B].
#
# Findings / gaps encoded (one case each):
#   WP-210 P1 "Kein Pentest-Nachweis im Repository; GATE-03 nur durch
#          Erklärung PASS" + gap high "Automatische GATE-03-Prüfung der
#          Pentest-Zusammenfassung"                         -> case pentest_*
#   WP-213 gap high "GATE-01/GATE-03 robust: Severity normalisieren, alle
#          Befundtabellen inklusive Anhang, High bei Pentest": the gate checker
#          tests/rep01-structure.test.sh only matches the literal
#          "| SR/PT-nn | Critical |"; "**Critical**", "critical", "Kritisch",
#          an open PT "High"/"Hoch" and an open Critical row of the review
#          annex pass unnoticed                             -> case rep01_*
#   WP-213 P1 "Gate-Checkliste führt CI-13 [B] als PASS trotz bestätigtem
#          Defekt SR-11 (Reaktivieren → 501)" + WP-209 P2 "SR-11 als 'Low'
#          eingestuft" + gap high "Abgleich Gate-Status ↔ offene
#          Sicherheitsbefunde"                               -> case known_defect_*
#   WP-213 P2 / WP-209 P2 "Gate meldet 'kein offenes Critical' und
#          'Isolationstest vollständig' trotz reproduziertem Site-Scope-Bypass
#          (Racks)"                                          -> case rack_site_scope
#   WP-213 P2 "PASS-Status ohne Test-/Traceability-Nachweis (Erklärungen …)"
#                                                            -> case pass_by_declaration
#   WP-212 P2 "ABN-01 (a)/(b) nur durch Erklärung abgenommen; v2-Texte fehlen"
#                                                            -> case abn01_texts
#   WP-105 P2 "Subprocessor list empty; NFR-09 gate PASS rests only on a
#          declaration"                                      -> case subprocessors
#
# Moved into tests/rep01-structure.test.sh (WP-451): rep01_* (fixture
# self-tests on tests/fixtures/rep01/), rack_site_scope, pass_by_declaration
# and assertion 1 of known_defect, generalised (an open SR/PT finding keeps
# every catalog ID it names from PASS). Assertion 2 of known_defect (SR-11
# rated High or Critical) moved there with WP-452 as rule sr11_severity, also
# for a fixed SR-11. The production defects behind them are fixed: SR-11 by
# WP-418 (TestAuditT5CIReactivationThroughProductionDecoratorChain), the rack
# site-scope bypass (SR-12) by WP-340 (TestRackChannelSiteScope).
# Case install_evidence_current (WP-211 P2, F-122) moved there with WP-595
# into the --final mode: the install smoke evidence in
# docs/acceptance/g1-installation.md names the last product change or a
# later commit (release candidate v1.0.0-rc.<n>).
#
# Retest:
#   ./tests/audit/g1-gate-evidence.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/     /' | head -n 12; return 0; }

PENTEST=docs/security/pentest-g1.md

# Normalised severity of a markdown cell: strips **, blanks, case; maps the
# German terms.
norm_severity() {
    local s
    s="$(printf '%s' "$1" | tr -d '*' | xargs | tr '[:upper:]' '[:lower:]')"
    case "$s" in
        critical|kritisch) echo critical ;;
        high|hoch) echo high ;;
        medium|mittel) echo medium ;;
        low|niedrig) echo low ;;
        *) echo "$s" ;;
    esac
}
is_closed() { printf '%s' "$1" | tr -d '*' | grep -qiE '^ *(behoben|geschlossen)'; }

# ─── WP-210: the pentest summary is evidence, not a declaration ─────────────
pending="$(grep -n 'nachzureichen' "$PENTEST" || true)"
if [ -z "$pending" ]; then
    ok "pentest_summary: docs/security/pentest-g1.md has no 'nachzureichen' item"
else
    fail "pentest_summary: docs/security/pentest-g1.md has no 'nachzureichen' item" "$pending"
fi
if grep -qE '^\| *ID *\| *(Severity|Schweregrad) *\| *Status *\|' "$PENTEST"; then
    ok "pentest_findings: the summary carries a finding list | ID | Severity | Status |"
else
    fail "pentest_findings: the summary carries a finding list | ID | Severity | Status |" \
        "no finding table; PT rows: $(grep -cE '^\| *PT-[0-9]+' "$PENTEST")"
fi
for field in 'Dienstleister' 'Zeitraum|Termin' 'Nachtest'; do
    # First table cell naming the field (e.g. "| Dienstleister, Umfang, Termin |").
    line="$(grep -E "^\| *[^|]*(${field})[^|]*\|" "$PENTEST" | head -n 1)"
    if [ -n "$line" ] && ! printf '%s' "$line" | grep -qiE 'nachzureichen|Erklärung'; then
        ok "pentest_field: '${field}' is filled with evidence"
    else
        fail "pentest_field: '${field}' is filled with evidence" "${line:-<missing>}"
    fi
done
open_pt="$(grep -E '^\| *PT-[0-9]+ *\|' "$PENTEST" | while IFS='|' read -r _ id sev status _; do
    s="$(norm_severity "$sev")"
    if { [ "$s" = critical ] || [ "$s" = high ]; } && ! is_closed "$status"; then echo "$id $s $status"; fi
done)"
if [ -z "$open_pt" ]; then
    ok "pentest_open: no open High/Critical PT finding"
else
    fail "pentest_open: no open High/Critical PT finding" "$open_pt"
fi

# ─── WP-212 P2: ABN-01 (a)/(b) frozen original texts ────────────────────────
for f in abn-01-a-cmdb.md abn-01-b-system.md; do
    path=""
    for d in docs/acceptance docs/acceptance/artefakte; do
        [ -s "$d/$f" ] && path="$d/$f"
    done
    if [ -n "$path" ] && grep -qF "$path" docs/acceptance/artefakte/SHA256SUMS; then
        ok "abn01_texts: ${f} is frozen (present and in SHA256SUMS)"
    else
        fail "abn01_texts: ${f} is frozen (present and in SHA256SUMS)" "${path:-<missing>}"
    fi
done

# ─── WP-105 P2: subprocessor list (NFR-09) ──────────────────────────────────
subs="$(sed -n '/^## Liste/,$p' docs/legal/subprozessoren.md | grep -E '^\|' | tail -n +3 \
    | awk -F'|' '{ s=$2; r=$5; a=$6; gsub(/[ *]/,"",s); gsub(/ /,"",r); gsub(/ /,"",a);
                   if (s != "" && s != "–" && s != "-" && r != "" && r != "–" && a != "" && a != "–") print }')"
if [ -n "$subs" ] && ! grep -q 'vom Betreiber zu liefern' docs/legal/subprozessoren.md; then
    ok "subprocessors: docs/legal/subprozessoren.md lists subprocessors with region and AVV"
else
    fail "subprocessors: docs/legal/subprozessoren.md lists subprocessors with region and AVV" \
        "$(grep -E '^\| *–' docs/legal/subprozessoren.md | head -n 2)"
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
