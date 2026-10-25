#!/usr/bin/env bash
#
# ci-config.test.sh — CI configuration of the dependency and container scans
# (WP-389: SEC-11, TST-04; tools per E-31). Runs in the ci.yml job gate.
#
#   - image_scan_coverage: every image of the release.yml matrix (Dockerfile
#     and context) is in the ci.yml image-scan matrix (retest F-124, case
#     moved from tests/audit/ops-config.test.sh), with a negative fixture;
#   - release_scan: release.yml scans every image with Trivy before the push,
#     with the threshold and the pinned CLI image of the ci.yml image-scan job;
#   - scan_exceptions_*: scripts/ci/check-scan-exceptions.sh runs before
#     govulncheck, npm audit and Trivy and enforces exp:, since: and sev: with
#     the SEC-11 SLA (Critical 7, High 30 days) on fixtures (F-284);
#   - db_image_pin: the reticora-postgres Dockerfile pins its base by version
#     and digest and checks the pgBackRest tarball by sha256 (F-291); it
#     replaces the Go helpers of the base image with ones rebuilt by the
#     pinned repository toolchain at pinned module versions (E-96);
#   - timescale_image_refs: every TimescaleDB image reference of the
#     repository (CI, deploy, compose, docs; not the historical records) is
#     the TIMESCALE_IMAGE of that Dockerfile, version and digest (E-96), with
#     a negative fixture;
#   - release_images_documented: docs/README.md and
#     docs/security/abhaengigkeitsscans.md name every release image (F-294).
#
# Tools: python3 with PyYAML (as tests/rep01-structure.test.sh), GNU date,
# git.
#
# Usage: ./tests/ci-config.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | head -n 10 | sed 's/^/     /'; return 0; }

CI=.github/workflows/ci.yml
RELEASE=.github/workflows/release.yml
CHECK=scripts/ci/check-scan-exceptions.sh
DOCKERFILE=deploy/docker-compose/postgres/Dockerfile

# ─── Retest F-124: every release image is scanned ───────────────────────────
# scan_gap <release.yml> <ci.yml>: the release images whose Dockerfile and
# context are not in the image-scan matrix, as "<image>(<dockerfile>)".
scan_gap() {
    python3 - "$1" "$2" <<'PY'
import sys, yaml
rel = yaml.safe_load(open(sys.argv[1]))
ci = yaml.safe_load(open(sys.argv[2]))
def matrix(job):
    return [e for e in job["strategy"]["matrix"]["include"] if e.get("dockerfile")]
released = [e for job in rel["jobs"].values()
            if "include" in (job.get("strategy") or {}).get("matrix", {}) for e in matrix(job)]
scanned = {(e["dockerfile"], e.get("context")) for e in matrix(ci["jobs"]["image-scan"])}
print(" ".join(f"{e['image']}({e['dockerfile']})" for e in released
               if (e["dockerfile"], e.get("context")) not in scanned))
PY
}

gap="$(scan_gap "$RELEASE" "$CI" 2>&1)"
if [ -z "$gap" ]; then
    ok "image_scan_coverage: every Dockerfile of the release image matrix is in the ci.yml image-scan matrix"
else
    fail "image_scan_coverage: every Dockerfile of the release image matrix is in the ci.yml image-scan matrix" "not scanned: $gap"
fi

# Negative fixtures: a release entry without a scan entry is reported — the
# state before WP-389 (postgres missing in image-scan) and a new release image.
python3 - "$CI" "$TMP_ROOT/ci-without-postgres.yml" <<'PY'
import sys, yaml
ci = yaml.safe_load(open(sys.argv[1]))
inc = ci["jobs"]["image-scan"]["strategy"]["matrix"]["include"]
ci["jobs"]["image-scan"]["strategy"]["matrix"]["include"] = [e for e in inc if e["image"] != "postgres"]
yaml.safe_dump(ci, open(sys.argv[2], "w"))
PY
python3 - "$RELEASE" "$TMP_ROOT/release-extra.yml" <<'PY'
import sys, yaml
rel = yaml.safe_load(open(sys.argv[1]))
rel["jobs"]["images"]["strategy"]["matrix"]["include"].append(
    {"image": "reticora-extra", "context": ".", "dockerfile": "extra/Dockerfile"})
yaml.safe_dump(rel, open(sys.argv[2], "w"))
PY
gap="$(scan_gap "$RELEASE" "$TMP_ROOT/ci-without-postgres.yml" 2>&1)"
if [ "$gap" = "reticora-postgres(deploy/docker-compose/postgres/Dockerfile)" ]; then
    ok "image_scan_coverage_negative: a release image without an image-scan entry is reported (postgres removed)"
else
    fail "image_scan_coverage_negative: a release image without an image-scan entry is reported (postgres removed)" "gap: '${gap}'"
fi
gap="$(scan_gap "$TMP_ROOT/release-extra.yml" "$CI" 2>&1)"
if [ "$gap" = "reticora-extra(extra/Dockerfile)" ]; then
    ok "image_scan_coverage_negative: a release image without an image-scan entry is reported (new release image)"
else
    fail "image_scan_coverage_negative: a release image without an image-scan entry is reported (new release image)" "gap: '${gap}'"
fi

# ─── release.yml scans before the push, like ci.yml ─────────────────────────
release_scan="$(python3 - "$CI" "$RELEASE" <<'PY'
import re, sys, yaml
ci = yaml.safe_load(open(sys.argv[1]))
rel = yaml.safe_load(open(sys.argv[2]))
REQUIRED = ["--severity CRITICAL,HIGH", "--ignore-unfixed",
            "--ignorefile security/scan-exceptions.txt", "--exit-code 1"]
def trivy(job):
    for i, st in enumerate(job["steps"]):
        run = re.sub(r"\\\n\s*", " ", st.get("run", ""))
        m = re.search(r"(aquasec/trivy\S*) image (.*)", run)
        if m:
            return i, m.group(1), " ".join(m.group(2).split())
    return None, None, ""
problems = []
_, ci_ref, ci_args = trivy(ci["jobs"]["image-scan"])
if not ci_ref or not re.fullmatch(r"aquasec/trivy:[0-9.]+@sha256:[0-9a-f]{64}", ci_ref):
    problems.append(f"ci.yml image-scan: Trivy image not pinned by version and digest: {ci_ref}")
job = rel["jobs"]["images"]
idx, ref, args = trivy(job)
pushes = [i for i, st in enumerate(job["steps"]) if (st.get("with") or {}).get("push") is True]
loads = [i for i, st in enumerate(job["steps"]) if (st.get("with") or {}).get("load") is True]
if idx is None:
    problems.append("release.yml images: no Trivy step")
else:
    if ref != ci_ref:
        problems.append(f"release.yml Trivy image {ref} differs from ci.yml {ci_ref}")
    for flag in REQUIRED:
        if flag not in args:
            problems.append(f"release.yml Trivy lacks '{flag}'")
        if flag not in ci_args:
            problems.append(f"ci.yml Trivy lacks '{flag}'")
    if not pushes or not all(idx < p for p in pushes):
        problems.append(f"release.yml: Trivy (step {idx}) does not run before every push (steps {pushes})")
    if not loads or not loads[0] < idx:
        problems.append("release.yml: no local build (load: true) before the Trivy step")
print("\n".join(problems))
PY
)"
if [ -z "$release_scan" ]; then
    ok "release_scan: release.yml scans every image with the ci.yml Trivy threshold before the push"
else
    fail "release_scan: release.yml scans every image with the ci.yml Trivy threshold before the push" "$release_scan"
fi

# ─── F-284: the exception check runs before every scanner ───────────────────
order="$(python3 - "$CI" "$RELEASE" "$CHECK" <<'PY'
import sys, yaml
ci = yaml.safe_load(open(sys.argv[1]))
rel = yaml.safe_load(open(sys.argv[2]))
check = sys.argv[3]
problems = []
for name, job, scanner in [("ci.yml govulncheck", ci["jobs"]["govulncheck"], "govulncheck -format"),
                           ("ci.yml npm-audit", ci["jobs"]["npm-audit"], "npm audit"),
                           ("ci.yml image-scan", ci["jobs"]["image-scan"], "aquasec/trivy"),
                           ("release.yml images", rel["jobs"]["images"], "aquasec/trivy")]:
    runs = [st.get("run", "") for st in job["steps"]]
    c = next((i for i, r in enumerate(runs) if check in r), None)
    s = next((i for i, r in enumerate(runs) if scanner in r), None)
    if s is None:
        problems.append(f"{name}: no '{scanner}' step")
    elif c is None or c > s:
        problems.append(f"{name}: {check} (step {c}) does not run before '{scanner}' (step {s})")
print("\n".join(problems))
PY
)"
if [ -z "$order" ]; then
    ok "scan_exceptions_order: check-scan-exceptions.sh runs before govulncheck, npm audit and Trivy"
else
    fail "scan_exceptions_order: check-scan-exceptions.sh runs before govulncheck, npm audit and Trivy" "$order"
fi

# ─── F-284: SLA check of the exceptions ─────────────────────────────────────
TODAY=2026-10-21
# exceptions <name> <case> <expect: ok|fail> <line>: one entry under the
# file's header; a failure must name line 3, the entry.
exceptions() {
    local f="$TMP_ROOT/exceptions-$2.txt" want=ok out rc
    [ "$3" = ok ] || want=FAIL
    printf '# fixture\n\n%s\n' "$4" >"$f"
    out="$(SCAN_EXCEPTIONS_TODAY=$TODAY "$CHECK" "$f" 2>&1)"
    rc=$?
    if { [ "$want" = ok ] && [ "$rc" -eq 0 ]; } \
        || { [ "$want" = FAIL ] && [ "$rc" -ne 0 ] && grep -qF "::error file=$f,line=3::" <<<"$out"; }; then
        ok "scan_exceptions_$2: $1 → $want"
    else
        fail "scan_exceptions_$2: $1 → $want" "rc=$rc; $out"
    fi
}
exceptions "a line without exp:" missing_exp fail \
    "CVE-2026-0001 since:2026-10-20 sev:high # no expiry"
exceptions "High with exp > since + 30 days" high_over_sla fail \
    "CVE-2026-0002 exp:2026-11-20 since:2026-10-20 sev:high # 31 days"
exceptions "Critical with exp > since + 7 days" critical_over_sla fail \
    "GO-2026-0003 exp:2026-10-28 since:2026-10-20 sev:critical # 8 days"
exceptions "a valid High line (exp = since + 30 days)" valid_high ok \
    "GHSA-abcd-2345-wxyz exp:2026-11-19 since:2026-10-20 sev:high # npm, ticket RET-1"
exceptions "a valid Critical line (exp = since + 7 days)" valid_critical ok \
    "GO-2026-0004 exp:2026-10-27 since:2026-10-20 sev:critical # ticket RET-2"
exceptions "an expired line (suppresses nothing)" expired ok \
    "CVE-2026-0005 exp:2026-10-01 since:2026-09-30 sev:critical # fixed"
exceptions "a line without since:" missing_since fail \
    "CVE-2026-0006 exp:2026-10-25 sev:high"
exceptions "a line without sev:" missing_sev fail \
    "CVE-2026-0007 exp:2026-10-25 since:2026-10-20"
exceptions "sev: other than critical or high" sev_medium fail \
    "CVE-2026-0008 exp:2026-10-25 since:2026-10-20 sev:medium"
exceptions "since: in the future (window moved)" since_future fail \
    "CVE-2026-0009 exp:2026-11-20 since:2026-10-22 sev:high"
exceptions "exp: before since:" exp_before_since fail \
    "CVE-2026-0010 exp:2026-10-19 since:2026-10-20 sev:high"
exceptions "an invalid date" invalid_date fail \
    "CVE-2026-0011 exp:2026-02-30 since:2026-02-01 sev:high"
exceptions "two exp: fields (Trivy reads the first, ci.yml the last)" duplicate_exp fail \
    "CVE-2026-0012 exp:2099-01-01 exp:2026-10-25 since:2026-10-20 sev:high"
exceptions "exp: only inside the comment" exp_in_comment fail \
    "CVE-2026-0013 since:2026-10-20 sev:high # exp:2026-10-25"
exceptions "an ID that is no CVE, GHSA or GO ID" bad_id fail \
    "* exp:2026-10-25 since:2026-10-20 sev:high"

if out="$("$CHECK" 2>&1)"; then
    ok "scan_exceptions_repository: security/scan-exceptions.txt keeps the SLA ($out)"
else
    fail "scan_exceptions_repository: security/scan-exceptions.txt keeps the SLA" "$out"
fi

# ─── F-291, E-96: reproducible database image ───────────────────────────────
db_pin="$(python3 - "$DOCKERFILE" backend/Dockerfile <<'PY'
import re, sys
df = open(sys.argv[1]).read()
backend = open(sys.argv[2]).read()
problems = []
m = re.search(r"^ARG TIMESCALE_IMAGE=timescale/timescaledb:(\S+?)@sha256:[0-9a-f]{64}$", df, re.M)
if not m:
    problems.append("ARG TIMESCALE_IMAGE is not timescale/timescaledb:<version>@sha256:<digest>")
elif not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+-pg[0-9]+", m.group(1)):
    problems.append(f"TIMESCALE_IMAGE uses a floating tag: {m.group(1)}")
# E-96: the Go helpers of the base image are rebuilt with the toolchain of
# the repository (backend/Dockerfile), pinned by digest, at pinned module
# versions verified against sum.golang.org.
g = re.search(r"^ARG GO_IMAGE=golang:(\S+?)@sha256:[0-9a-f]{64}$", df, re.M)
b = re.search(r"^FROM golang:(\S+)", backend, re.M)
if not g:
    problems.append("ARG GO_IMAGE is not golang:<version>@sha256:<digest>")
elif not b or g.group(1) != b.group(1):
    problems.append(f"GO_IMAGE golang:{g.group(1)} is not the toolchain of backend/Dockerfile ({b and b.group(1)})")
froms = re.findall(r"^FROM\s+(\S+)", df, re.M)
if froms.count("${GO_IMAGE}") != 1 or froms[-1] != "${TIMESCALE_IMAGE}" \
        or any(f not in ("${TIMESCALE_IMAGE}", "${GO_IMAGE}") for f in froms):
    problems.append(f"FROM must use the pinned ${{TIMESCALE_IMAGE}} and once ${{GO_IMAGE}}: {froms}")
if not re.search(r"^ENV [^\n]*\bGOSUMDB=sum\.golang\.org\b", df, re.M) \
        or re.search(r"GONOSUMDB|GONOSUMCHECK|GOINSECURE|GOPRIVATE|GOFLAGS=[^\n]*insecure|GOSUMDB=off", df):
    problems.append("the Go stage does not verify modules against sum.golang.org (ENV GOSUMDB=sum.golang.org)")
for mod, arg, binary in (
        ("github.com/timescale/timescaledb-tune/cmd/timescaledb-tune", "TIMESCALEDB_TUNE_VERSION", "timescaledb-tune"),
        ("github.com/tianon/gosu", "GOSU_VERSION", "gosu")):
    if not re.search(rf"^ARG {arg}=v[0-9]+\.[0-9]+\.[0-9]+(-[0-9]{{14}}-[0-9a-f]{{12}})?$", df, re.M):
        problems.append(f"ARG {arg} is not a module version or pseudo-version")
    if f'go install -trimpath -ldflags=\'-d -w\' "{mod}@${{{arg}}}"' not in df:
        problems.append(f"{binary} is not built with go install {mod}@${{{arg}}}")
    if f"COPY --from=go-tools /go/bin/{binary} /usr/local/bin/{binary}" not in df:
        problems.append(f"the final stage does not replace /usr/local/bin/{binary} of the base image")
if "@latest" in df:
    problems.append("a go install uses @latest")
if not re.search(r"^RUN rm /usr/local/bin/timescaledb-parallel-copy\b", df, re.M):
    problems.append("the unused /usr/local/bin/timescaledb-parallel-copy of the base image is not removed")
if not re.search(r'\[ "\$\(gosu postgres id -un\)" = postgres \]', df):
    problems.append("the build does not check that the rebuilt gosu drops to postgres")
if not re.search(r"^ARG PGBACKREST_SHA256=[0-9a-f]{64}$", df, re.M):
    problems.append("ARG PGBACKREST_SHA256=<sha256> missing")
run = re.sub(r"\\\n\s*", " ", df)
dl = re.search(r"curl [^\n]*?-o (\S+)[^\n]*?pgbackrest/archive[^\n]*", run)
if not dl:
    problems.append("the pgBackRest tarball is not downloaded to a file (curl -o)")
else:
    tarball, rest = dl.group(1), dl.group(0)
    check = rest.find(f'echo "${{PGBACKREST_SHA256}}  {tarball}" | sha256sum -c -')
    unpack = rest.find(f"tar xzf {tarball}")
    if check < 0 or unpack < 0 or check > unpack:
        problems.append(f"{tarball} is not checked with sha256sum -c against PGBACKREST_SHA256 before tar")
if re.search(r"curl[^\n]*\|\s*tar", run):
    problems.append("a download is piped into tar without a checksum")
print("\n".join(problems))
PY
)"
if [ -z "$db_pin" ]; then
    ok "db_image_pin: the postgres Dockerfile pins TimescaleDB and the Go toolchain by version and digest, rebuilds gosu and timescaledb-tune, checks the pgBackRest sha256"
else
    fail "db_image_pin: the postgres Dockerfile pins TimescaleDB and the Go toolchain by version and digest, rebuilds gosu and timescaledb-tune, checks the pgBackRest sha256" "$db_pin"
fi

# ─── E-96: one TimescaleDB reference everywhere ─────────────────────────────
# timescale_refs <root>: every TimescaleDB image reference in the tracked
# files of <root> must be the TIMESCALE_IMAGE of the postgres Dockerfile,
# version and digest. Historical records (audit, plan, acceptance evidence)
# keep the version they were made with. First output line: "<refs> <files>",
# then one line per deviating reference.
timescale_refs() {
    python3 - "$1" <<'PY'
import os, re, subprocess, sys
root = sys.argv[1]
HISTORY = ("audit/", "docs/audit/", "docs/plan/", "docs/acceptance/")
SELF = "tests/ci-config.test.sh"
# Where the CI database, the deploy and dev stacks and the docs name the image.
REQUIRED = {".github/workflows/ci.yml", ".github/workflows/load.yml",
            ".github/workflows/restore-test.yml", "deploy/k8s/base/components/backup/cronjob.yaml",
            "docker-compose.yml", "deploy/docker-compose/postgres/Dockerfile", "docs/README.md",
            "docs/security/abhaengigkeitsscans.md", "docs/decisions/0001-timescale-rls.md"}
df = open(os.path.join(root, "deploy/docker-compose/postgres/Dockerfile")).read()
m = re.search(r"^ARG TIMESCALE_IMAGE=(timescale/timescaledb:\S+@sha256:[0-9a-f]{64})$", df, re.M)
pin = m.group(1) if m else "<no pinned ARG TIMESCALE_IMAGE>"
ref = re.compile(r"timescale/timescaledb:[\w.-]+(?:@sha256:[0-9a-f]*)?")
files = subprocess.run(["git", "-C", root, "ls-files", "-z"], capture_output=True, check=True).stdout.decode().split("\0")
problems, refs, where = [], 0, set()
for f in files:
    if not f or f == SELF or f.startswith(HISTORY):
        continue
    try:
        text = open(os.path.join(root, f), encoding="utf-8").read()
    except (OSError, UnicodeDecodeError):
        continue
    for i, line in enumerate(text.splitlines(), 1):
        for r in ref.findall(line):
            refs += 1
            where.add(f)
            if r != pin:
                problems.append(f"{f}:{i}: {r} is not {pin}")
for f in sorted(REQUIRED - where):
    problems.append(f"{f}: names no TimescaleDB image")
print(refs, len(where))
print("\n".join(problems))
PY
}

out="$(timescale_refs . 2>&1)"
problems="$(sed 1d <<<"$out")"
if [ -z "$problems" ]; then
    ok "timescale_image_refs: every TimescaleDB image reference is the pinned TIMESCALE_IMAGE ($(head -n1 <<<"$out" | awk '{print $1" references in "$2" files"}'))"
else
    fail "timescale_image_refs: every TimescaleDB image reference is the pinned TIMESCALE_IMAGE" "$problems"
fi

# Negative fixture (copies of the files that name the image): the dev stack
# back on the floating tag, a CI service with another digest and a workflow
# without the image are reported, nothing else.
fx="$TMP_ROOT/refs-fixture"
mkdir -p "$fx"
while IFS= read -r -d '' f; do
    mkdir -p "$fx/$(dirname "$f")" && cp "$f" "$fx/$f"
done < <(git ls-files -z | xargs -0 grep -lZ 'timescale/timescaledb:' --)
sed -i -E 's#timescale/timescaledb:[^ ]+#timescale/timescaledb:latest-pg16#' "$fx/docker-compose.yml"
sed -i -E 's#@sha256:[0-9a-f]{64}#@sha256:0000000000000000000000000000000000000000000000000000000000000000#' "$fx/.github/workflows/load.yml"
sed -i '/timescale\/timescaledb:/d' "$fx/.github/workflows/restore-test.yml"
git -C "$fx" init -q && git -C "$fx" add -A
problems="$(timescale_refs "$fx" 2>&1 | sed 1d)"
if [ "$(grep -c . <<<"$problems")" -eq 3 ] \
    && grep -q '^docker-compose\.yml:[0-9]*: timescale/timescaledb:latest-pg16 is not ' <<<"$problems" \
    && grep -q '^\.github/workflows/load\.yml:[0-9]*: timescale/timescaledb:[^ ]*@sha256:0\{64\} is not ' <<<"$problems" \
    && grep -qx '\.github/workflows/restore-test\.yml: names no TimescaleDB image' <<<"$problems"; then
    ok "timescale_image_refs_negative: a floating tag, another digest and a missing reference are reported"
else
    fail "timescale_image_refs_negative: a floating tag, another digest and a missing reference are reported" "$problems"
fi

# ─── F-294: documentation of the release images ─────────────────────────────
docs="$(python3 - "$RELEASE" "$CI" <<'PY'
import sys, yaml
rel = yaml.safe_load(open(sys.argv[1]))
ci = yaml.safe_load(open(sys.argv[2]))
readme = open("docs/README.md").read()
scans = open("docs/security/abhaengigkeitsscans.md").read()
problems = []
for e in rel["jobs"]["images"]["strategy"]["matrix"]["include"]:
    if f"`{e['image']}`" not in readme:
        problems.append(f"docs/README.md does not name the release image {e['image']}")
for e in ci["jobs"]["image-scan"]["strategy"]["matrix"]["include"]:
    if f"`{e['image']}`" not in scans:
        problems.append(f"docs/security/abhaengigkeitsscans.md does not name the image-scan entry {e['image']}")
for needle in ("since:", "sev:", "scripts/ci/check-scan-exceptions.sh", "tests/ci-config.test.sh"):
    if needle not in scans:
        problems.append(f"docs/security/abhaengigkeitsscans.md does not describe {needle}")
print("\n".join(problems))
PY
)"
if [ -z "$docs" ]; then
    ok "release_images_documented: README and abhaengigkeitsscans.md name every release and scan image, format and SLA check"
else
    fail "release_images_documented: README and abhaengigkeitsscans.md name every release and scan image, format and SLA check" "$docs"
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
