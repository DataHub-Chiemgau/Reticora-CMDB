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
#   WP-211 P2 "G1-Installationsnachweis gilt für 1b6671f/37752e1, nicht für den
#          aktuellen Release-Kandidaten"                     -> case install_evidence_current
#
# Known reproduced production defects (audit probes, see the Go defect tests
# of group T5/T1) that must not stand as PASS while open:
#   SR-11 -> CI-13: backend/internal/tenant/rls/audit_t5_sr11_decorator_chain_auditdefect_test.go
#   rack site-scope bypass -> TEN-09: T1 TestAuditT1RackChannelSiteScope
#
# Retest:
#   ./tests/audit/g1-gate-evidence.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
REPO_ROOT="$(pwd)"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/     /' | head -n 12; return 0; }

GATE=docs/acceptance/g1-gate.md
REVIEW=docs/security/review-g1.md
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

# ─── WP-213 gap: the gate checker must catch every open Critical/High ───────
# Fixture: a copy of everything tests/rep01-structure.test.sh reads.
FIX="$TMP_ROOT/rep01"
mkdir -p "$FIX/backend/internal" "$FIX/frontend"
cp -r api docs .github tests "$FIX/"
for pkg in notify jobs locations pii reviews rules traceability; do
    cp -r "backend/internal/$pkg" "$FIX/backend/internal/"
done
cp -r frontend/e2e "$FIX/frontend/"

rep01_in() { (cd "$1" && ./tests/rep01-structure.test.sh >/dev/null 2>&1); }

if rep01_in "$FIX"; then
    ok "rep01_control: the unmodified fixture passes the gate checker"
    rep01_case() {
        local name="$1" file="$2" row="$3" want="$4" dir="$TMP_ROOT/rep01-$5"
        rm -rf "$dir"
        cp -r "$FIX" "$dir"
        printf '\n%s\n' "$row" >> "$dir/$file"
        local got=pass
        rep01_in "$dir" || got=fail
        if [ "$got" = "$want" ]; then
            ok "rep01_${name}: '${row}' in ${file} -> gate checker must ${want}"
        else
            fail "rep01_${name}: '${row}' in ${file} -> gate checker must ${want}" "got: ${got}"
        fi
    }
    rep01_case pt_critical      "$PENTEST" '| PT-91 | Critical | offen | probe | WP-999 |'       fail a
    rep01_case pt_bold_critical "$PENTEST" '| PT-92 | **Critical** | offen | probe | WP-999 |'   fail b
    rep01_case pt_kritisch      "$PENTEST" '| PT-93 | Kritisch | offen | probe | WP-999 |'       fail c
    rep01_case pt_lowercase     "$PENTEST" '| PT-94 | critical | offen | probe | WP-999 |'       fail d
    rep01_case pt_high          "$PENTEST" '| PT-95 | High | offen | probe | WP-999 |'           fail e
    rep01_case pt_hoch          "$PENTEST" '| PT-96 | Hoch | offen | probe | WP-999 |'           fail f
    rep01_case sr_bold_critical "$REVIEW"  '| SR-91 | **Critical** | offen | probe | WP-999 |'   fail g
    rep01_case annex_critical   "$REVIEW"  '| TEN-09 | [B] | Critical | offen | WP-208 |'       fail h
    rep01_case pt_fixed         "$PENTEST" '| PT-97 | Critical | behoben | probe | WP-999 |'     pass i
    rep01_case pt_closed        "$PENTEST" '| PT-98 | High | geschlossen | probe | WP-999 |'    pass j
else
    fail "rep01_control: the unmodified fixture passes the gate checker" "$(cd "$FIX" && ./tests/rep01-structure.test.sh 2>&1 | grep FAIL | head -5)"
fi

# ─── WP-213 P1 / WP-209 P2: known open defects must not stand as PASS ───────
# known_defect <finding id> <catalog id>: while the finding is open, the
# checklist annex must not carry the ID as PASS, and a [B] function that is
# unusable in production must be rated at least High (gate-relevant).
known_defect() {
    local fid="$1" cid="$2" row sev status annex
    row="$(grep -E "^\| *${fid} *\|" "$REVIEW" | head -n 1)"
    if [ -z "$row" ]; then
        fail "known_defect_${fid}: ${fid} is recorded in ${REVIEW}" "<no row>"
        return
    fi
    sev="$(printf '%s' "$row" | cut -d'|' -f3)"
    status="$(printf '%s' "$row" | cut -d'|' -f4)"
    if is_closed "$status"; then
        ok "known_defect_${fid}: ${fid} is fixed; ${cid} may be PASS"
        return
    fi
    annex="$(grep -E "^\| *${cid} *\|" "$GATE" | tail -n 1)"
    if printf '%s' "$annex" | cut -d'|' -f4 | grep -qE '^ *\**PASS'; then
        fail "known_defect_${fid}: ${cid} is not PASS while ${fid} is open" "$annex"
    else
        ok "known_defect_${fid}: ${cid} is not PASS while ${fid} is open"
    fi
    case "$(norm_severity "$sev")" in
        critical|high) ok "known_defect_${fid}: ${fid} (${cid} [B] unusable in production) is rated High or Critical" ;;
        *) fail "known_defect_${fid}: ${fid} (${cid} [B] unusable in production) is rated High or Critical" "severity:$(printf '%s' "$sev")" ;;
    esac
}
known_defect SR-11 CI-13

# ─── WP-213 P2 / WP-209 P2: rack site-scope bypass is a recorded finding ────
rack_rows="$(grep -hE '^\| *(SR|PT)-[0-9]+ *\|' "$REVIEW" "$PENTEST" | grep -iE 'rack' | grep -iE 'site[- ]?scope|TEN-09' || true)"
ten09="$(sed -n '/^## GATE-03/,/^## /p' "$GATE" | grep -E 'TEN-09' | head -n 1)"
if [ -n "$rack_rows" ]; then
    ok "rack_site_scope: the reproduced rack site-scope bypass is a finding of the review"
elif ! printf '%s' "$ten09" | grep -qE '\| *PASS *\|'; then
    ok "rack_site_scope: GATE-03 does not claim TEN-09 complete"
else
    fail "rack_site_scope: the reproduced rack site-scope bypass is recorded (review finding) or TEN-09 is not PASS" "$ten09"
fi

# ─── WP-213 P2: no PASS without test/traceability evidence ──────────────────
declared="$(sed -n '/^## Anhang/,$p' "$GATE" | grep -E '^\| *[A-Z]{2,5}-[0-9]{2} *\|' | awk -F'|' '$4 ~ /Erklärung/ {print $2 "|" $4}' || true)"
if [ -z "$declared" ]; then
    ok "pass_by_declaration: no [B]/[Q] ID is PASS by declaration only"
else
    fail "pass_by_declaration: no [B]/[Q] ID is PASS by declaration only" "$declared"
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

# ─── WP-211 P2: installation evidence belongs to the release candidate ──────
# The install smoke run documented in g1-installation.md must be on the last
# commit that changed the product (code, deploy, installer; test-only and
# audit paths excluded) or on a later one.
product_paths=(backend frontend deploy install.sh install-cloud.sh install-vm.sh install-preflight.sh
    ':(exclude)frontend/e2e' ':(exclude)frontend/e2e-real' ':(exclude)frontend/vitest.auditdefect.config.ts'
    ':(glob,exclude)**/*_test.go' ':(glob,exclude)**/*.test.ts' ':(glob,exclude)**/*.test.tsx'
    ':(glob,exclude)**/*.auditdefect.ts' ':(glob,exclude)**/*.auditdefect.tsx')
if ! git -C "$REPO_ROOT" rev-parse --git-dir >/dev/null 2>&1; then
    fail "install_evidence_current: git history available" "not a git checkout"
else
    last_change="$(git -C "$REPO_ROOT" log -1 --format='%h %s' -- "${product_paths[@]}")"
    last="${last_change%% *}"
    named="$(sed -n '/^## Release-Kandidat/,/^## Ergebnisse/p' docs/acceptance/g1-installation.md | grep -oE '`[0-9a-f]{7,40}`' | tr -d '`' | sort -u)"
    covered=""
    for c in $named; do
        git -C "$REPO_ROOT" cat-file -e "$c^{commit}" 2>/dev/null || continue
        if git -C "$REPO_ROOT" merge-base --is-ancestor "$last" "$c"; then covered="$c"; fi
    done
    if [ -n "$covered" ]; then
        ok "install_evidence_current: the install smoke evidence (${covered}) covers the last product change ${last}"
    else
        fail "install_evidence_current: the install smoke evidence covers the last product change" \
            "last product change: ${last_change}; commits named in g1-installation.md: $(echo $named)"
    fi
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
