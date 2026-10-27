#!/usr/bin/env bash
#
# lint-toolchain.test.sh — contract of the Go lint toolchain (WP-499, F-123,
# retest of WP-001, TST-04). Runs in the ci.yml job lint.
#
#   - schema: backend/.golangci.yml uses the golangci-lint v2 schema
#     (version "2", no v1 keys); with GOLANGCI_LINT set (required with
#     REQUIRE_GOLANGCI_LINT=1) also "golangci-lint config verify";
#   - linters_superset: the enabled linters and formatters are a superset
#     of the v1 linters of cd23a05^ (before the v2 migration of WP-001),
#     mapped to v2 (gosimple -> staticcheck, exportloopref -> copyloopvar,
#     gofmt/goimports -> formatters, typecheck always on); the embedded v1
#     list is compared with cd23a05^ when the history contains it;
#   - pin: the golangci-lint version of the ci.yml job lint equals the
#     Makefile pin, and the action installs exactly that version;
#   - module_matrix: every module of go.work is in the lint matrix and in
#     tests/lint-debt-baseline.txt;
#   - wiring: the job lint runs this test and scripts/ci/lint-debt.sh for its
#     module, 'make lint-debt' runs the script, backend/.golangci.yml points
#     to the follow-up work packages in docs/plan/phase2/
#     implementierungsplan.md and they exist there;
#   - negative fixtures: a different pin, a module missing from the matrix,
#     a removed linter, a v1 key and a missing follow-up reference FAIL;
#   - lint_debt_*: scripts/ci/lint-debt.sh with a stub golangci-lint: a
#     number above the baseline exits 1, equal or lower exits 0, a
#     golangci-lint error or a module without baseline exits 2, the numbers
#     reach GITHUB_STEP_SUMMARY.
#
# Tools: python3 with PyYAML (as tests/ci-config.test.sh), jq, git.
#
# Usage: ./tests/lint-toolchain.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | head -n 10 | sed 's/^/     /'; return 0; }

# The v1 linters of backend/.golangci.yml at cd23a05^ (before WP-001).
V1_LINTERS="errcheck govet staticcheck unused gosimple ineffassign typecheck gofmt goimports misspell unconvert gocritic revive exportloopref"
V1_REV=cd23a05

# check <part> <root>: the problems of one contract part for the toolchain
# files below <root> (backend/.golangci.yml, .github/workflows/ci.yml,
# Makefile, go.work, tests/lint-debt-baseline.txt, docs/plan/phase2/
# implementierungsplan.md); empty output means the part holds.
check() {
    V1_LINTERS="$V1_LINTERS" python3 - "$1" "$2" <<'PY'
import os, re, sys, yaml
part, root = sys.argv[1], sys.argv[2]
p = lambda rel: os.path.join(root, rel)
errs = []
cfg = yaml.safe_load(open(p("backend/.golangci.yml")))
ci = yaml.safe_load(open(p(".github/workflows/ci.yml")))
lint = ci["jobs"]["lint"]

if part == "schema":
    if str(cfg.get("version")) != "2":
        errs.append("version is %r, not \"2\"" % cfg.get("version"))
    allowed = {"version", "run", "linters", "formatters", "issues", "output", "severity"}
    errs += ["unknown or v1 top-level key %r" % k for k in cfg if k not in allowed]
    for k in (cfg.get("linters") or {}):
        if k not in {"default", "enable", "disable", "settings", "exclusions"}:
            errs.append("unknown or v1 key linters.%s" % k)
    for k in (cfg.get("issues") or {}):
        if k.startswith("exclude"):
            errs.append("v1 key issues.%s (v2: linters.exclusions)" % k)

elif part == "superset":
    linters = cfg.get("linters") or {}
    if linters.get("default", "standard") not in ("none", "standard", "all", "fast"):
        errs.append("unknown linters.default %r" % linters.get("default"))
    enabled = set(linters.get("enable") or []) - set(linters.get("disable") or [])
    enabled |= set((cfg.get("formatters") or {}).get("enable") or [])
    v2 = {"gosimple": "staticcheck", "exportloopref": "copyloopvar"}
    for name in os.environ["V1_LINTERS"].split():
        if name == "typecheck":
            continue  # always on in v2
        if v2.get(name, name) not in enabled:
            errs.append("v1 linter %s missing (v2: %s)" % (name, v2.get(name, name)))

elif part == "pin":
    ci_pin = str((lint.get("env") or {}).get("GOLANGCI_LINT_VERSION", ""))
    mk = re.findall(r"^GOLANGCI_LINT_VERSION\s*:?=\s*(\S+)\s*$", open(p("Makefile")).read(), re.M)
    if not ci_pin:
        errs.append("ci.yml job lint has no env GOLANGCI_LINT_VERSION")
    if len(mk) != 1:
        errs.append("Makefile has %d GOLANGCI_LINT_VERSION lines" % len(mk))
    elif mk[0] != ci_pin:
        errs.append("pin differs: ci.yml %s, Makefile %s" % (ci_pin, mk[0]))
    actions = [s for job in ci["jobs"].values() for s in job.get("steps", [])
               if str(s.get("uses", "")).startswith("golangci/golangci-lint-action@")]
    if not actions:
        errs.append("no golangci-lint-action step")
    for s in actions:
        v = str((s.get("with") or {}).get("version", ""))
        if v not in ("${{ env.GOLANGCI_LINT_VERSION }}", ci_pin):
            errs.append("golangci-lint-action installs %r, not the pin" % v)

elif part == "matrix":
    work = open(p("go.work")).read()
    block = re.search(r"^use\s*\(([^)]*)\)", work, re.M)
    mods = re.findall(r"^\s*\./(\S+)\s*$", block.group(1), re.M) if block else []
    mods += re.findall(r"^use\s+\./(\S+)\s*$", work, re.M)
    if not mods:
        errs.append("no module in go.work")
    matrix = ((lint.get("strategy") or {}).get("matrix") or {}).get("module") or []
    base = [l.split()[0] for l in open(p("tests/lint-debt-baseline.txt"))
            if l.strip() and not l.startswith("#")]
    errs += ["go.work module %s not in the lint matrix" % m for m in mods if m not in matrix]
    errs += ["go.work module %s not in tests/lint-debt-baseline.txt" % m for m in mods if m not in base]

elif part == "wiring":
    runs = [str(s.get("run", "")) for s in lint.get("steps", [])]
    if not any("./tests/lint-toolchain.test.sh" in r for r in runs):
        errs.append("job lint does not run tests/lint-toolchain.test.sh")
    if not any(re.search(r"scripts/ci/lint-debt\.sh\s+\"?\$\{\{\s*matrix\.module\s*\}\}", r) for r in runs):
        errs.append("job lint does not run scripts/ci/lint-debt.sh for its module")
    mk = open(p("Makefile")).read()
    if not re.search(r"^lint-debt:.*\n(\t.*\n)*\t.*scripts/ci/lint-debt\.sh", mk, re.M):
        errs.append("Makefile has no target lint-debt running scripts/ci/lint-debt.sh")
    raw = open(p("backend/.golangci.yml")).read()
    plan = open(p("docs/plan/phase2/implementierungsplan.md")).read()
    refs = re.findall(r"WP-\d+", raw)
    if "new-from-rev" in raw:
        if "docs/plan/phase2/implementierungsplan.md" not in raw or not refs:
            errs.append("backend/.golangci.yml does not point to the follow-up work packages")
        for wp in sorted(set(refs) - {"WP-001", "WP-499"}):
            if not re.search(r"^### %s – Lint-Altlast" % wp, plan, re.M):
                errs.append("%s is not a Lint-Altlast work package of docs/plan/phase2/implementierungsplan.md" % wp)
        blocks = re.findall(r"^### (WP-\d+) – Lint-Altlast", plan, re.M)
        if not blocks:
            errs.append("no Lint-Altlast work package in docs/plan/phase2/implementierungsplan.md")
        else:
            last = plan[plan.find("### %s – Lint-Altlast" % blocks[-1]):]
            last = last[:last.find("\n### ", 1)] if "\n### " in last[1:] else last
            if "new-from-rev" not in last:
                errs.append("the last Lint-Altlast work package %s does not remove new-from-rev" % blocks[-1])
print("\n".join(errs))
PY
}

# expect_ok <part> <description>; expect_fail <part> <root> <description>
expect_ok() {
    local out
    out="$(check "$1" . 2>&1)"
    if [ -z "$out" ]; then ok "$1: $2"; else fail "$1: $2" "$out"; fi
}
expect_fail() {
    local out
    out="$(check "$1" "$2" 2>&1)"
    if [ -n "$out" ]; then ok "$1 negative: $3 → FAIL ($(head -n 1 <<<"$out"))"; else fail "$1 negative: $3 → FAIL" "no problem reported"; fi
}
# fixture <name>: a copy of the toolchain files to mutate.
fixture() {
    local d="$TMP_ROOT/$1"
    mkdir -p "$d/backend" "$d/.github/workflows" "$d/tests" "$d/docs/plan/phase2"
    cp backend/.golangci.yml "$d/backend/"
    cp .github/workflows/ci.yml "$d/.github/workflows/"
    cp Makefile go.work "$d/"
    cp tests/lint-debt-baseline.txt "$d/tests/"
    cp docs/plan/phase2/implementierungsplan.md "$d/docs/plan/phase2/"
    printf '%s\n' "$d"
}

# ─── contract on the repository ─────────────────────────────────────────────
expect_ok schema "backend/.golangci.yml uses the v2 schema"
expect_ok superset "enabled linters are a superset of the v1 linters of $V1_REV^"
expect_ok pin "golangci-lint pin of ci.yml equals the Makefile"
expect_ok matrix "every go.work module is in the lint matrix and the debt baseline"
expect_ok wiring "job lint, make lint-debt and the follow-up work packages are wired"

if git cat-file -e "$V1_REV^:backend/.golangci.yml" 2>/dev/null; then
    hist="$(git show "$V1_REV^:backend/.golangci.yml" | python3 -c 'import sys, yaml; print(" ".join(sorted(yaml.safe_load(sys.stdin)["linters"]["enable"])))')"
    want="$(tr ' ' '\n' <<<"$V1_LINTERS" | sort | tr '\n' ' ' | sed 's/ $//')"
    if [ "$hist" = "$want" ]; then
        ok "superset: the embedded v1 list equals $V1_REV^:backend/.golangci.yml"
    else
        fail "superset: the embedded v1 list equals $V1_REV^:backend/.golangci.yml" "history: $hist; embedded: $want"
    fi
else
    echo "note superset: $V1_REV^ not in the local history (shallow clone); the embedded v1 list is used"
fi

GL="${GOLANGCI_LINT:-}"
if [ -z "$GL" ] && [ "${REQUIRE_GOLANGCI_LINT:-0}" = 1 ]; then GL=golangci-lint; fi
if [ -n "$GL" ]; then
    if out="$(cd backend && "$GL" config verify --config .golangci.yml 2>&1)"; then
        ok "schema: golangci-lint config verify accepts backend/.golangci.yml"
    else
        fail "schema: golangci-lint config verify accepts backend/.golangci.yml" "$out"
    fi
else
    echo "note schema: golangci-lint config verify not run (GOLANGCI_LINT not set)"
fi

# ─── negative fixtures ──────────────────────────────────────────────────────
d="$(fixture pin-ci)"
sed -i 's/^\(      GOLANGCI_LINT_VERSION: \).*/\1v2.13.0/' "$d/.github/workflows/ci.yml"
expect_fail pin "$d" "ci.yml pin differs from the Makefile"

d="$(fixture pin-make)"
sed -i 's/^GOLANGCI_LINT_VERSION := .*/GOLANGCI_LINT_VERSION := v2.1.0/' "$d/Makefile"
expect_fail pin "$d" "Makefile pin differs from ci.yml"

d="$(fixture pin-action)"
python3 - "$d/.github/workflows/ci.yml" <<'PY'
import sys, yaml
ci = yaml.safe_load(open(sys.argv[1]))
for s in ci["jobs"]["lint"]["steps"]:
    if str(s.get("uses", "")).startswith("golangci/golangci-lint-action@"):
        s["with"]["version"] = "latest"
yaml.safe_dump(ci, open(sys.argv[1], "w"))
PY
expect_fail pin "$d" "golangci-lint-action installs another version"

d="$(fixture matrix)"
python3 - "$d/.github/workflows/ci.yml" <<'PY'
import sys, yaml
ci = yaml.safe_load(open(sys.argv[1]))
ci["jobs"]["lint"]["strategy"]["matrix"]["module"].remove("edgecore")
yaml.safe_dump(ci, open(sys.argv[1], "w"))
PY
expect_fail matrix "$d" "edgecore missing from the lint matrix"

d="$(fixture matrix-new-module)"
sed -i 's#^\t\./edgecore$#\t./edgecore\n\t./agent#' "$d/go.work"
expect_fail matrix "$d" "new go.work module ./agent not linted"

d="$(fixture linter-removed)"
sed -i '/^    - gocritic$/d' "$d/backend/.golangci.yml"
expect_fail superset "$d" "gocritic removed"

d="$(fixture formatter-removed)"
sed -i '/^    - goimports$/d' "$d/backend/.golangci.yml"
expect_fail superset "$d" "formatter goimports removed"

d="$(fixture v1-key)"
printf 'linters-settings:\n  govet:\n    enable-all: true\n' >> "$d/backend/.golangci.yml"
expect_fail schema "$d" "v1 key linters-settings"

d="$(fixture v1-version)"
sed -i 's/^version: "2"$/version: "1"/' "$d/backend/.golangci.yml"
expect_fail schema "$d" "version 1"

d="$(fixture no-followup)"
sed -i 's#docs/plan/phase2/implementierungsplan\.md#docs/plan/elsewhere.md#g' "$d/backend/.golangci.yml"
expect_fail wiring "$d" "backend/.golangci.yml without the follow-up reference"

d="$(fixture debt-step)"
python3 - "$d/.github/workflows/ci.yml" <<'PY'
import sys, yaml
ci = yaml.safe_load(open(sys.argv[1]))
ci["jobs"]["lint"]["steps"] = [s for s in ci["jobs"]["lint"]["steps"] if "lint-debt.sh" not in str(s.get("run", ""))]
yaml.safe_dump(ci, open(sys.argv[1], "w"))
PY
expect_fail wiring "$d" "job lint without scripts/ci/lint-debt.sh"

# ─── scripts/ci/lint-debt.sh with a stub golangci-lint ──────────────────────
STUB="$TMP_ROOT/golangci-lint-stub"
cat > "$STUB" <<'SH'
#!/usr/bin/env bash
# Writes STUB_ISSUES findings (STUB_LINTER) to --output.json.path; exits
# STUB_EXIT. Records that --new-from-rev= was passed.
[ "${STUB_EXIT:-0}" -eq 0 ] || { echo "stub failure" >&2; exit "$STUB_EXIT"; }
out=""
for a in "$@"; do
    case "$a" in
        --output.json.path=*) out="${a#--output.json.path=}" ;;
        --new-from-rev=) : > "$STUB_SEEN" ;;
    esac
done
jq -cn --argjson n "${STUB_ISSUES:-0}" --arg l "${STUB_LINTER:-govet}" \
    '{Issues: [range($n) | {FromLinter: $l, Text: "x", Pos: {Filename: "a.go", Line: 1}}]}' > "$out"
SH
chmod +x "$STUB"
printf '# test\nbackend 10\nedgecore 6\n' > "$TMP_ROOT/baseline.txt"

# debt <want-exit> <description> <issues> [module] [pattern]
debt() {
    local want="$1" name="$2" n="$3" mod="${4:-edgecore}" pattern="${5:-}" out rc
    rm -f "$TMP_ROOT/seen"
    out="$(STUB_ISSUES="$n" STUB_SEEN="$TMP_ROOT/seen" GITHUB_STEP_SUMMARY='' GOLANGCI_LINT="$STUB" \
        LINT_DEBT_BASELINE="$TMP_ROOT/baseline.txt" scripts/ci/lint-debt.sh "$mod" 2>&1)"
    rc=$?
    if [ "$rc" -ne "$want" ]; then fail "lint_debt: $name → exit $want" "exit $rc: $out"; return; fi
    if [ "$want" -ne 2 ] && [ ! -e "$TMP_ROOT/seen" ]; then fail "lint_debt: $name" "--new-from-rev= not passed"; return; fi
    if [ -n "$pattern" ] && ! grep -qE -- "$pattern" <<<"$out"; then fail "lint_debt: $name" "output: $out"; return; fi
    ok "lint_debt: $name → exit $rc"
}
debt 1 "number above the baseline" 7 edgecore '::error::lint debt of edgecore rose: 7 findings, baseline 6 \(govet=7\)'
debt 0 "number equal to the baseline" 6 edgecore 'lint debt of edgecore: 6 findings, baseline 6'
debt 0 "number below the baseline" 2 edgecore '::notice::lint debt of edgecore fell: 2 findings, baseline 6'
debt 0 "no finding" 0 edgecore
debt 2 "module without a baseline line" 0 collector 'collector has no line'
out="$(STUB_EXIT=3 GOLANGCI_LINT="$STUB" LINT_DEBT_BASELINE="$TMP_ROOT/baseline.txt" scripts/ci/lint-debt.sh edgecore 2>&1)"; rc=$?
if [ "$rc" -eq 2 ]; then ok "lint_debt: golangci-lint error → exit 2"; else fail "lint_debt: golangci-lint error → exit 2" "exit $rc: $out"; fi
: > "$TMP_ROOT/step-summary.md"
STUB_ISSUES=11 STUB_SEEN="$TMP_ROOT/seen" GITHUB_STEP_SUMMARY="$TMP_ROOT/step-summary.md" GOLANGCI_LINT="$STUB" \
    LINT_DEBT_BASELINE="$TMP_ROOT/baseline.txt" scripts/ci/lint-debt.sh >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 1 ] && grep -q '^| backend | 11 | 10 | above baseline | govet=11 |$' "$TMP_ROOT/step-summary.md" &&
   grep -q '^| edgecore | 11 | 6 | above baseline | govet=11 |$' "$TMP_ROOT/step-summary.md"; then
    ok "lint_debt: every baseline module by default, numbers in GITHUB_STEP_SUMMARY"
else
    fail "lint_debt: every baseline module by default, numbers in GITHUB_STEP_SUMMARY" "exit $rc: $(cat "$TMP_ROOT/step-summary.md")"
fi

echo
echo "lint-toolchain: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
