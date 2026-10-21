#!/usr/bin/env bash
# shellcheck disable=SC2016 # the scripts in single quotes run inside the sandbox
#
# install-signature.test.sh — signature verification before the installation
# (WP-387: TEC-15, COL-06; tools per E-31, transition rule of E-69):
#
#   - install-cloud.sh release mode (RETICORA_IMAGE_REGISTRY): reticora-server,
#     reticora-frontend and reticora-postgres pass 'cosign verify' against the
#     release workflow identity and are pinned by digest in .env before
#     anything starts; the local source build warns "not signature-verified";
#   - install-vm.sh --release: the collector binary passes 'cosign
#     verify-blob' and its sha256 in the signed SHA256SUMS before it is copied;
#     the Go toolchain of the source build is checked against go.dev;
#   - install.sh and install-vm.sh with a release tag as source: the signed
#     SHA256SUMS, the commit of the checkout (SOURCE_COMMIT) or the source
#     tarball, checked before anything of it runs or is built;
#   - release.yml publishes what the installers verify: its steps run here
#     with stubbed cosign/go/syft and produce the release the cases install.
#
# cosign and curl are stubbed, git is real (local repositories). The cosign
# stub accepts a bundle only for the content, signer identity and OIDC issuer
# it records — what the real check of a Fulcio certificate guarantees — and
# rejects unknown flags. Optional: RETICORA_TEST_COSIGN=<cosign v2.4.1 binary>
# also checks the pinned cosign checksum and runs the installers' verify and
# verify-blob command lines against the real cosign (CI: install-smoke.yml).
#
# Tools: python3 with PyYAML (release.yml steps), git, jq, openssl.
#
# Usage: ./tests/install-signature.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
REPO_ROOT="$(pwd)"

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | tail -n 8 | sed 's/^/     /'; return 0; }

TAG="v1.2.3"
REPOSITORY="DataHub-Chiemgau/Reticora-CMDB"
ISSUER="https://token.actions.githubusercontent.com"
identity_of() { printf 'https://github.com/%s/.github/workflows/release.yml@refs/tags/%s' "${2:-$REPOSITORY}" "$1"; }
# The signer the installers pin: release.yml on exactly the tag, or on any
# version tag for an image tag that is none.
TAG_IDENTITY_RE='^https://github\.com/DataHub-Chiemgau/Reticora-CMDB/\.github/workflows/release\.yml@refs/tags/v1\.2\.3$'
ANY_IDENTITY_RE='^https://github\.com/DataHub-Chiemgau/Reticora-CMDB/\.github/workflows/release\.yml@refs/tags/v.+$'
SIGNER_ARGS="--certificate-identity-regexp $TAG_IDENTITY_RE --certificate-oidc-issuer $ISSUER"
case "$(uname -m)" in
    aarch64|arm64) ARCH="arm64"; OTHER_ARCH="amd64" ;;
    *) ARCH="amd64"; OTHER_ARCH="arm64" ;;
esac

# ─── Retest F-131 (cases moved from tests/audit/ops-config.test.sh) ──────────
for f in install-vm.sh install-cloud.sh install.sh; do
    if grep -vE '^[[:space:]]*#' "$f" | grep -qE '(cosign|COSIGN)[^#]*verify(-blob)?'; then
        ok "install_signature_${f%.sh}: ${f} verifies the cosign signature of what it installs"
    else
        fail "install_signature_${f%.sh}: ${f} verifies the cosign signature of what it installs" \
            "no 'cosign verify'/'cosign verify-blob' in ${f}"
    fi
done

# install.sh runs before the sources are downloaded and carries its own copy
# of the shared signature block of install-preflight.sh.
block() { sed -n '/^# BEGIN shared signature block/,/^# END shared signature block/p' "$1"; }
if [ -n "$(block install-preflight.sh)" ] && diff <(block install.sh) <(block install-preflight.sh) >"$TMP_ROOT/block.diff"; then
    ok "install.sh and install-preflight.sh carry the same signature block (identity, cosign pin, checks)"
else
    fail "install.sh and install-preflight.sh carry the same signature block (identity, cosign pin, checks)" "$(head -n 8 "$TMP_ROOT/block.diff")"
fi

# ─── Sandbox: stubs and a clean PATH ─────────────────────────────────────────
SYS="$TMP_ROOT/sys"; GITBIN="$TMP_ROOT/gitbin"
STUB_CURL="$TMP_ROOT/stub-curl"; STUB_COSIGN="$TMP_ROOT/stub-cosign"; STUB_GO="$TMP_ROOT/stub-go"
mkdir -p "$SYS" "$GITBIN" "$STUB_CURL" "$STUB_COSIGN" "$STUB_GO"
for c in bash sh env cat cp mv rm mkdir rmdir chmod ln mktemp sed awk grep tr cut head tail wc sort uniq \
    tar gzip sha256sum uname id dirname basename date ls find install jq stat hostname sleep tee touch \
    readlink true false od xargs openssl python3 diff; do
    p="$(type -P "$c" 2>/dev/null)" && ln -sf "$p" "$SYS/$c"
done
ln -sf "$(type -P git)" "$GITBIN/git"
for c in python3 jq git openssl; do
    type -P "$c" >/dev/null || { fail "tools: $c available" "$c not found"; printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"; exit 1; }
done

cat > "$STUB_CURL/curl" <<'STUB'
#!/usr/bin/env bash
# Fake curl: serves the release, the cosign download and go.dev from fixtures.
out="" url=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) out="$2"; shift ;;
        -*) ;;
        *) url="$1" ;;
    esac
    shift
done
printf 'curl %s\n' "$url" >> "$FAKE_LOG"
src=""
case "$url" in
    https://github.com/DataHub-Chiemgau/Reticora-CMDB/releases/download/*)
        src="$FAKE_RELEASES/${url#https://github.com/DataHub-Chiemgau/Reticora-CMDB/releases/download/}" ;;
    https://github.com/sigstore/cosign/releases/download/*) src="${FAKE_COSIGN_DOWNLOAD:-}" ;;
    'https://go.dev/dl/?mode=json&include=all') src="${FAKE_GO_JSON:-}" ;;
    https://go.dev/dl/*) src="${FAKE_GO_DL:-/nonexistent}/${url#https://go.dev/dl/}" ;;
esac
if [ -z "$src" ] || [ ! -f "$src" ]; then
    echo "curl: (22) The requested URL returned error: 404" >&2
    exit 22
fi
if [ -n "$out" ]; then cp "$src" "$out"; else cat "$src"; fi
STUB

cat > "$STUB_COSIGN/cosign" <<'STUB'
#!/usr/bin/env bash
# Fake cosign: a bundle (blob) or a registry record (image) is a valid
# signature only for the sha256, signer identity and issuer it records.
printf 'cosign %s\n' "$*" >> "$FAKE_LOG"
if [ -n "${FAKE_COSIGN_RC:-}" ]; then echo "Error: verification failed (forced)" >&2; exit "$FAKE_COSIGN_RC"; fi
cmd="$1"; shift
bundle="" regexp="" exact="" issuer="" pos=()
while [ "$#" -gt 0 ]; do
    case "$1" in
        --bundle) bundle="$2"; shift ;;
        --certificate-identity-regexp) regexp="$2"; shift ;;
        --certificate-identity) exact="$2"; shift ;;
        --certificate-oidc-issuer) issuer="$2"; shift ;;
        --yes) ;;
        -*) echo "Error: unknown flag: $1" >&2; exit 2 ;;
        *) pos+=("$1") ;;
    esac
    shift
done
field() { sed -n "s/^$1=//p" "$2" | head -n 1; }
signer_ok() {
    local id iss
    id="$(field identity "$1")"; iss="$(field issuer "$1")"
    if [ -z "$regexp$exact" ] || [ -z "$issuer" ]; then
        echo "Error: --certificate-identity(-regexp) and --certificate-oidc-issuer are required" >&2; exit 1
    fi
    if [ -n "$regexp" ] && ! [[ "$id" =~ $regexp ]]; then
        echo "Error: none of the expected identities matched what was in the certificate, got subjects [$id]" >&2; exit 1
    fi
    if [ -n "$exact" ] && [ "$id" != "$exact" ]; then
        echo "Error: expected identity not found in certificate, got subjects [$id]" >&2; exit 1
    fi
    [ "$iss" = "$issuer" ] || { echo "Error: expected oidc issuer not found in certificate" >&2; exit 1; }
}
case "$cmd" in
    verify-blob)
        [ "${#pos[@]}" -eq 1 ] && [ -n "$bundle" ] || { echo "Error: usage" >&2; exit 2; }
        [ -f "$bundle" ] || { echo "Error: reading $bundle: no such file or directory" >&2; exit 1; }
        grep -qx 'fake-sigstore-bundle' "$bundle" || { echo "Error: invalid bundle" >&2; exit 1; }
        if [ "$(field sha256 "$bundle")" != "$(sha256sum "${pos[0]}" | cut -d' ' -f1)" ]; then
            echo "Error: invalid signature when validating ASN.1 encoded signature" >&2; exit 1
        fi
        signer_ok "$bundle"
        echo "Verified OK" >&2
        ;;
    verify)
        [ "${#pos[@]}" -eq 1 ] || { echo "Error: usage" >&2; exit 2; }
        rec="$FAKE_REGISTRY/$(printf '%s' "${pos[0]}" | tr '/:@' '___')"
        [ -f "$rec" ] || { echo "Error: no signatures found" >&2; exit 1; }
        signer_ok "$rec"
        printf '[{"critical":{"identity":{"docker-reference":"%s"},"image":{"docker-manifest-digest":"%s"},"type":"cosign container image signature"},"optional":null}]\n' \
            "$(field reference "$rec")" "$(field digest "$rec")"
        ;;
    sign-blob)
        # The release workflow: Fulcio certificate of its run (repository, ref).
        [ "${#pos[@]}" -eq 1 ] && [ -n "$bundle" ] || { echo "Error: usage" >&2; exit 2; }
        printf 'fake-sigstore-bundle\nidentity=https://github.com/%s/.github/workflows/release.yml@%s\nissuer=https://token.actions.githubusercontent.com\nsha256=%s\n' \
            "$GITHUB_REPOSITORY" "$GITHUB_REF" "$(sha256sum "${pos[0]}" | cut -d' ' -f1)" > "$bundle"
        ;;
    *) echo "Error: unknown command $cmd" >&2; exit 2 ;;
esac
STUB

cat > "$STUB_GO/go" <<'STUB'
#!/usr/bin/env bash
# Fake go: 'go env GOVERSION' and 'go build … -o <file> <package>'.
case "$1" in
    env) echo "go1.26.9" ;;
    build)
        shift; out="" pkg=""
        while [ "$#" -gt 0 ]; do
            case "$1" in -o) out="$2"; shift ;; -*) ;; *) pkg="$1" ;; esac
            shift
        done
        printf 'fake binary %s GOARCH=%s\n' "$pkg" "${GOARCH:-amd64}" > "$out"
        printf 'go build %s\n' "$pkg" >> "$FAKE_LOG"
        ;;
    *) exit 2 ;;
esac
STUB
printf '#!/usr/bin/env bash\nfor a; do case "$a" in spdx-json=*) printf "{}\\n" > "${a#spdx-json=}" ;; esac; done\n' > "$STUB_GO/syft"
chmod +x "$STUB_CURL/curl" "$STUB_COSIGN/cosign" "$STUB_GO/go" "$STUB_GO/syft"

OS_RELEASE="$TMP_ROOT/os-release"
printf 'ID=ubuntu\nVERSION_ID="24.04"\n' > "$OS_RELEASE"

# sandbox <case> <PATH> [VAR=value …] <command …> — runs in a clean
# environment; FAKE_* select the fixtures, the call log is <case dir>/calls.
sandbox() {
    local dir="$TMP_ROOT/case-$1" path="$2"; shift 2
    mkdir -p "$dir/home" "$dir/tools" "$dir/tmp"
    : > "$dir/calls"
    env -i PATH="$path" HOME="$dir/home" TMPDIR="$dir/tmp" CASE="$dir" FAKE_LOG="$dir/calls" \
        FAKE_RELEASES="$RELEASES" FAKE_REGISTRY="$REGISTRY" FAKE_GO_JSON="$GO_FIX/dl.json" FAKE_GO_DL="$GO_FIX" \
        RETICORA_TOOLS_DIR="$dir/tools" RETICORA_OS_RELEASE="$OS_RELEASE" "$@"
}
case_dir() { printf '%s' "$TMP_ROOT/case-$1"; }
P_ALL="$STUB_COSIGN:$STUB_CURL:$GITBIN:$SYS"
P_NOGIT="$STUB_COSIGN:$STUB_CURL:$SYS"
P_NOCOSIGN="$STUB_CURL:$GITBIN:$SYS"

# ─── The release as release.yml builds it ────────────────────────────────────
# A source repository with the tag, and a "mirror" whose tag points to another
# commit (a manipulated source).
SRC_REPO="$TMP_ROOT/repo"
EVIL_REPO="$TMP_ROOT/evil-repo"
make_repo() {
    local dir="$1" marker="$2" s
    mkdir -p "$dir/collector/cmd/collector" "$dir/backend/cmd/agent"
    for s in install-cloud.sh install-vm.sh; do
        printf '#!/usr/bin/env bash\necho "RAN %s ($(cat "$(dirname "$0")/MARKER")) $*"\n' "$s" > "$dir/$s"
        chmod +x "$dir/$s"
    done
    printf '# preflight\n' > "$dir/install-preflight.sh"
    printf 'go 1.26.9\n' > "$dir/go.work"
    printf '%s\n' "$marker" > "$dir/MARKER"
    printf 'package main\n' > "$dir/collector/cmd/collector/main.go"
    git -C "$dir" init -q -b main
    git -C "$dir" add -A
    git -C "$dir" -c user.name=t -c user.email=t@example.test -c commit.gpgsign=false commit -q -m "$marker"
    git -C "$dir" -c user.name=t -c user.email=t@example.test -c tag.gpgsign=false tag -a "$TAG" -m "$TAG"
}
make_repo "$SRC_REPO" release >/dev/null
make_repo "$EVIL_REPO" manipulated >/dev/null

python3 - "$REPO_ROOT/.github/workflows/release.yml" "$TMP_ROOT/steps" <<'PY' 2>"$TMP_ROOT/steps.err"
import os, sys, yaml
wf = yaml.safe_load(open(sys.argv[1]))
os.makedirs(sys.argv[2])
n = 0
for step in wf["jobs"]["binaries"]["steps"]:
    if "run" not in step or step.get("name") == "Publish the GitHub release":
        continue
    n += 1
    with open(os.path.join(sys.argv[2], "%02d.sh" % n), "w") as f:
        f.write("# %s\n%s" % (step["name"], step["run"]))
PY
steps_rc=$?
release_log="$TMP_ROOT/release.log"
if [ "$steps_rc" -ne 0 ]; then
    fail "release.yml: the steps of the binaries job can be read (python3 with PyYAML)" "$(cat "$TMP_ROOT/steps.err")"
    printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
    exit 1
fi
(
    cd "$SRC_REPO" && git checkout -q "$TAG" || exit 1
    for step in "$TMP_ROOT"/steps/*.sh; do
        env -i PATH="$STUB_COSIGN:$STUB_GO:$GITBIN:$SYS" HOME="$TMP_ROOT" FAKE_LOG="$release_log" \
            GITHUB_REF_NAME="$TAG" GITHUB_REF="refs/tags/$TAG" GITHUB_REPOSITORY="$REPOSITORY" \
            bash --noprofile --norc -eo pipefail "$step" || { echo "step failed: $(head -n 1 "$step")"; exit 1; }
    done
) >>"$release_log" 2>&1
release_rc=$?
DIST="$SRC_REPO/dist"
commit="$(git -C "$SRC_REPO" rev-parse "$TAG^{commit}")"
if [ "$release_rc" -eq 0 ] && [ -f "$DIST/SHA256SUMS.sigstore.json" ] \
    && grep -qE "  SOURCE_COMMIT$" "$DIST/SHA256SUMS" && grep -qE "  reticora-source-$TAG\.tar\.gz$" "$DIST/SHA256SUMS" \
    && grep -qE "  reticora-collector-linux-$ARCH$" "$DIST/SHA256SUMS" \
    && [ "$(cat "$DIST/SOURCE_COMMIT")" = "$commit" ] \
    && tar -tzf "$DIST/reticora-source-$TAG.tar.gz" | grep -qx "reticora-source-$TAG/install-cloud.sh" \
    && grep -q "cosign verify-blob --bundle dist/SHA256SUMS.sigstore.json" "$release_log"; then
    ok "release.yml: SOURCE_COMMIT (the tagged commit) and the git-archive tarball are in SHA256SUMS; SHA256SUMS is signed and verified"
else
    fail "release.yml: SOURCE_COMMIT (the tagged commit) and the git-archive tarball are in SHA256SUMS; SHA256SUMS is signed and verified" \
        "$(tail -n 5 "$release_log"; printf '%s ' "$DIST"/*)"
fi
RELEASES="$TMP_ROOT/releases"
mkdir -p "$RELEASES/$TAG"
cp "$DIST"/* "$RELEASES/$TAG/"

# variant <name> — a copy of the release to manipulate; prints its root.
variant() {
    local root="$TMP_ROOT/variant-$1"
    if [ ! -d "$root/$TAG" ]; then
        mkdir -p "$root/$TAG"
        cp "$RELEASES/$TAG"/* "$root/$TAG/"
    fi
    printf '%s' "$root"
}
# resign <root> <identity> — every bundle of the release signed by <identity>.
resign() {
    local b f
    for b in "$1/$TAG"/*.sigstore.json; do
        f="${b%.sigstore.json}"
        printf 'fake-sigstore-bundle\nidentity=%s\nissuer=%s\nsha256=%s\n' "$2" "$ISSUER" "$(sha256sum "$f" | cut -d' ' -f1)" > "$b"
    done
}
sums_line() { awk -v n="$2" '$2 == n { print $1 }' "$1/$TAG/SHA256SUMS"; }

# Images: a registry record per <registry>/<image>:<tag> (digest, signer,
# docker-reference of the signature).
REGISTRY="$TMP_ROOT/registry"
IMAGE_REGISTRY="ghcr.io/datahub-chiemgau"
make_registry() { # <dir> <image tag> <release tag of the signature>
    local image n=0
    mkdir -p "$1"
    for image in reticora-server reticora-frontend reticora-postgres; do
        n=$((n + 1))
        printf 'digest=sha256:%s\nidentity=%s\nissuer=%s\nreference=%s\n' "$(printf '%064d' "$n")" \
            "$(identity_of "$3")" "$ISSUER" "$IMAGE_REGISTRY/$image" > "$1/$(printf '%s' "$IMAGE_REGISTRY/$image:$2" | tr '/:@' '___')"
    done
}
make_registry "$REGISTRY" "$TAG" "$TAG"
make_registry "$REGISTRY" latest "$TAG"
record() { printf '%s/%s' "$1" "$(printf '%s' "$IMAGE_REGISTRY/$2:$3" | tr '/:@' '___')"; }

# go.dev: the toolchain tarball and the JSON with its sha256 (pretty-printed
# like go.dev, plus another file of the release).
GO_FIX="$TMP_ROOT/go-dev"
mkdir -p "$GO_FIX/tree/go/bin"
cp "$STUB_GO/go" "$GO_FIX/tree/go/bin/go"
tar -C "$GO_FIX/tree" -czf "$GO_FIX/go1.26.9.linux-$ARCH.tar.gz" go
go_json() { # <sha256 of the linux-$ARCH tarball> [file name]
    cat <<JSON
[
 {
  "version": "go1.26.9",
  "stable": true,
  "files": [
   {
    "filename": "go1.26.9.src.tar.gz",
    "os": "",
    "arch": "",
    "version": "go1.26.9",
    "sha256": "$(printf '%064d' 7)",
    "size": 1,
    "kind": "source"
   },
   {
    "filename": "${2:-go1.26.9.linux-$ARCH.tar.gz}",
    "os": "linux",
    "arch": "$ARCH",
    "version": "go1.26.9",
    "sha256": "$1",
    "size": 1,
    "kind": "archive"
   }
  ]
 }
]
JSON
}
go_json "$(sha256sum "$GO_FIX/go1.26.9.linux-$ARCH.tar.gz" | cut -d' ' -f1)" > "$GO_FIX/dl.json"

# expect <name> <rc> <must-match regex> <output> [<must-not-match regex>]
expect() {
    local name="$1" want="$2" pattern="$3" out="$4" reject="${5:-}" rc="$RC"
    if [ "$rc" -eq "$want" ] && { [ -z "$pattern" ] || printf '%s' "$out" | grep -qE -- "$pattern"; } \
        && { [ -z "$reject" ] || ! printf '%s' "$out" | grep -qE -- "$reject"; }; then
        ok "$name"
    else
        fail "$name" "rc $rc (want $want); $(printf '%s' "$out" | tail -n 6)"
    fi
}

# ─── install.sh: source of a release tag (git and tarball) ───────────────────
INSTALL_SH="$TMP_ROOT/install-sh/install.sh"
mkdir -p "$(dirname "$INSTALL_SH")"
cp install.sh "$INSTALL_SH"
# install_sh <case> <PATH> [VAR=value …] -- <arguments>
install_sh() {
    local name="$1" path="$2"; shift 2
    local vars=()
    while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do vars+=("$1"); shift; done
    shift
    sandbox "$name" "$path" RETICORA_SOURCE_DIR="$(case_dir "$name")/src" "${vars[@]}" \
        bash "$INSTALL_SH" "$@" </dev/null 2>&1
}

OUT="$(install_sh sh-git "$P_ALL" -- --cloud --non-interactive --repo "file://$SRC_REPO" --ref "$TAG")"; RC=$?
expect "install.sh: release tag (git) — commit matches the signed release, install-cloud.sh runs" 0 \
    "Source verified: $TAG is commit $commit.*RAN install-cloud.sh \(release\) --non-interactive" "$(printf '%s' "$OUT" | tr '\n' ' ')"
if grep -qE '^cosign verify-blob --bundle [^ ]*/SHA256SUMS\.sigstore\.json ' "$(case_dir sh-git)/calls" \
    && grep -qF -- "/SHA256SUMS.sigstore.json $SIGNER_ARGS " "$(case_dir sh-git)/calls"; then
    ok "install.sh: the manifest is verified for exactly the tag (release identity, GitHub Actions issuer)"
else
    fail "install.sh: the manifest is verified for exactly the tag (release identity, GitHub Actions issuer)" "$(cat "$(case_dir sh-git)/calls")"
fi

OUT="$(install_sh sh-evil "$P_ALL" -- --cloud --non-interactive --repo "file://$EVIL_REPO" --ref "$TAG")"; RC=$?
expect "install.sh: a tag checkout with another commit stops before anything runs" 1 \
    "Source check failed: the checkout of $TAG .* is commit [0-9a-f]{40}, the signed release names $commit" "$OUT" "RAN install"

EVIL_COMMIT="$TMP_ROOT/evil-commit"
git -C "$EVIL_REPO" rev-parse "$TAG^{commit}" > "$EVIL_COMMIT"
root="$(variant commit-swapped)"
cp "$EVIL_COMMIT" "$root/$TAG/SOURCE_COMMIT"
OUT="$(install_sh sh-sum "$P_ALL" FAKE_RELEASES="$root" -- --cloud --non-interactive --repo "file://$EVIL_REPO" --ref "$TAG")"; RC=$?
expect "install.sh: a manipulated SOURCE_COMMIT (checksum not in the signed manifest) stops" 1 \
    "Checksum mismatch: SOURCE_COMMIT" "$OUT" "RAN install"

root="$(variant sums-rewritten)"
sed -i "s/^[0-9a-f]*  SOURCE_COMMIT\$/$(sha256sum "$EVIL_COMMIT" | cut -d' ' -f1)  SOURCE_COMMIT/" "$root/$TAG/SHA256SUMS"
cp "$EVIL_COMMIT" "$root/$TAG/SOURCE_COMMIT"
OUT="$(install_sh sh-rc1 "$P_ALL" FAKE_RELEASES="$root" -- --cloud --non-interactive --repo "file://$EVIL_REPO" --ref "$TAG")"; RC=$?
expect "install.sh: a rewritten SHA256SUMS (cosign verify-blob rc 1) stops" 1 \
    "invalid signature.*Signature check failed: SHA256SUMS is not signed by the release workflow" "$(printf '%s' "$OUT" | tr '\n' ' ')" "RAN install"

root="$(variant fork)"
resign "$root" "$(identity_of "$TAG" attacker/Reticora-CMDB)"
OUT="$(install_sh sh-fork "$P_ALL" FAKE_RELEASES="$root" -- --cloud --non-interactive --repo "file://$SRC_REPO" --ref "$TAG")"; RC=$?
expect "install.sh: a release signed by another identity (fork workflow) stops" 1 \
    "none of the expected identities matched.*Signature check failed: SHA256SUMS" "$(printf '%s' "$OUT" | tr '\n' ' ')" "RAN install"

OUT="$(install_sh sh-nocosign "$P_NOCOSIGN" -- --cloud --non-interactive --repo "file://$SRC_REPO" --ref "$TAG")"; RC=$?
expect "install.sh: without cosign (not installed, download fails) nothing runs" 1 \
    "cosign is required to verify the signed release artifacts" "$OUT" "RAN install"

OUT="$(install_sh sh-tar "$P_NOGIT" -- --vm --non-interactive --ref "$TAG")"; RC=$?
expect "install.sh: release tag without git — the source tarball matches the signed manifest, install-vm.sh runs" 0 \
    "Source verified: reticora-source-$TAG.tar.gz matches the signed release manifest.*RAN install-vm.sh \(release\)" "$(printf '%s' "$OUT" | tr '\n' ' ')"

root="$(variant tarball)"
tar -C "$EVIL_REPO" --exclude=.git -czf "$root/$TAG/reticora-source-$TAG.tar.gz" --transform "s,^\.,reticora-source-$TAG," .
OUT="$(install_sh sh-tar-evil "$P_NOGIT" FAKE_RELEASES="$root" -- --cloud --non-interactive --ref "$TAG")"; RC=$?
if [ -e "$(case_dir sh-tar-evil)/src/install-cloud.sh" ]; then RC=99; fi
expect "install.sh: a manipulated source tarball stops before it is unpacked" 1 \
    "Checksum mismatch: reticora-source-$TAG.tar.gz" "$OUT" "RAN install"

OUT="$(install_sh sh-main "$P_ALL" -- --cloud --non-interactive --repo "file://$SRC_REPO" --ref main)"; RC=$?
[ -s "$(case_dir sh-main)/calls" ] && RC=84
expect "install.sh: a ref that is no release tag runs with 'Source not signature-verified (no release tag)'" 0 \
    "Source not signature-verified \(no release tag\): 'main'.*RAN install-cloud.sh" "$(printf '%s' "$OUT" | tr '\n' ' ')"

# ─── install-vm.sh: collector release, toolchain, source of a release tag ────
VM_DIR="$TMP_ROOT/vm"
mkdir -p "$VM_DIR"
sed -e '/^main "\$@"$/d' install-vm.sh > "$VM_DIR/install-vm-lib.sh"
cp install-preflight.sh "$VM_DIR/"
# vm <case> <PATH> [VAR=value …] -- <script> — the installer functions with
# INSTALL_DIR, the spool and the Go toolchain below the case directory.
vm() {
    local name="$1" path="$2"; shift 2
    local vars=()
    while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do vars+=("$1"); shift; done
    shift
    sandbox "$name" "$path" "${vars[@]}" bash -c 'set -euo pipefail; set --; . "$0"
        INSTALL_DIR="$CASE/opt"; RETICORA_SPOOL_DIR="$CASE/spool"; GO_INSTALL_PARENT="$CASE/usr-local"
        mkdir -p "$GO_INSTALL_PARENT"
        '"$1" "$VM_DIR/install-vm-lib.sh" 2>&1
}
installed() { [ -f "$(case_dir "$1")/opt/collector" ]; }
collector_sum() { sha256sum "$(case_dir "$1")/opt/collector" | cut -d' ' -f1; }

OUT="$(vm vm-rel "$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" -- provide_collector_systemd)"; RC=$?
if installed vm-rel && [ "$(collector_sum vm-rel)" = "$(sums_line "$RELEASES" "reticora-collector-linux-$ARCH")" ]; then :; else RC=98; fi
expect "install-vm.sh --release: signed binary (verify-blob, sha256 in the signed SHA256SUMS) is installed" 0 \
    "Collector $TAG verified \(cosign verify-blob, sha256 in the signed SHA256SUMS\)" "$OUT"
calls="$(cat "$(case_dir vm-rel)/calls")"
if [ "$(grep '^cosign verify-blob ' <<<"$calls" | grep -cF -- " $SIGNER_ARGS ")" -eq 2 ] \
    && grep -qE "^cosign verify-blob .*/reticora-collector-linux-$ARCH\$" <<<"$calls"; then
    ok "install-vm.sh --release: manifest and binary are checked against the release identity of the tag"
else
    fail "install-vm.sh --release: manifest and binary are checked against the release identity of the tag" "$calls"
fi

root="$(variant binary)"
printf 'manipulated\n' >> "$root/$TAG/reticora-collector-linux-$ARCH"
OUT="$(vm vm-bin "$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" FAKE_RELEASES="$root" -- provide_collector_systemd)"; RC=$?
installed vm-bin && RC=97
expect "install-vm.sh --release: a manipulated binary (verify-blob rc 1) is not installed" 1 \
    "Signature check failed: reticora-collector-linux-$ARCH is not signed" "$OUT"

root="$(variant other-arch)"
cp "$RELEASES/$TAG/reticora-collector-linux-$OTHER_ARCH" "$root/$TAG/reticora-collector-linux-$ARCH"
cp "$RELEASES/$TAG/reticora-collector-linux-$OTHER_ARCH.sigstore.json" "$root/$TAG/reticora-collector-linux-$ARCH.sigstore.json"
OUT="$(vm vm-swap "$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" FAKE_RELEASES="$root" -- provide_collector_systemd)"; RC=$?
installed vm-swap && RC=97
expect "install-vm.sh --release: another signed binary under the name (checksum not in the manifest) is not installed" 1 \
    "Checksum mismatch: reticora-collector-linux-$ARCH" "$OUT"

root="$(variant sums)"
sed -i "s/^[0-9a-f]*  reticora-collector-linux-$ARCH\$/$(printf '%064d' 1)  reticora-collector-linux-$ARCH/" "$root/$TAG/SHA256SUMS"
OUT="$(vm vm-sums "$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" FAKE_RELEASES="$root" -- provide_collector_systemd)"; RC=$?
installed vm-sums && RC=97
expect "install-vm.sh --release: a manipulated checksum in SHA256SUMS (signature no longer valid) stops" 1 \
    "Signature check failed: SHA256SUMS" "$OUT"

root="$(variant vm-fork)"
resign "$root" "$(identity_of "$TAG" attacker/Reticora-CMDB)"
OUT="$(vm vm-fork "$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" FAKE_RELEASES="$root" -- provide_collector_systemd)"; RC=$?
installed vm-fork && RC=97
expect "install-vm.sh --release: a release signed by another identity is not installed" 1 \
    "none of the expected identities matched.*Signature check failed" "$(printf '%s' "$OUT" | tr '\n' ' ')"

root="$(variant older)"
resign "$root" "$(identity_of v1.2.2)"
OUT="$(vm vm-older "$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" FAKE_RELEASES="$root" -- provide_collector_systemd)"; RC=$?
installed vm-older && RC=97
expect "install-vm.sh --release: the signed files of another release (v1.2.2) do not pass as $TAG" 1 \
    "Signature check failed" "$OUT"

OUT="$(vm vm-rc1 "$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" FAKE_COSIGN_RC=1 -- provide_collector_systemd)"; RC=$?
installed vm-rc1 && RC=97
expect "install-vm.sh --release: cosign verify-blob rc 1 stops before the copy" 1 "Signature check failed" "$OUT"

OUT="$(vm vm-nocosign "$P_NOCOSIGN" RETICORA_COLLECTOR_RELEASE="$TAG" -- provide_collector_systemd)"; RC=$?
installed vm-nocosign && RC=97
expect "install-vm.sh --release: without cosign (not installed, download fails) nothing is installed" 1 \
    "cosign is required to verify the signed release artifacts" "$OUT"

OUT="$(vm vm-badcosign "$P_NOCOSIGN" RETICORA_COLLECTOR_RELEASE="$TAG" FAKE_COSIGN_DOWNLOAD="$STUB_COSIGN/cosign" -- provide_collector_systemd)"; RC=$?
installed vm-badcosign && RC=97
expect "install-vm.sh --release: a downloaded cosign with another checksum is not used" 1 \
    "The downloaded cosign v2.4.1 does not match its published sha256 checksum" "$OUT"

OUT="$(sandbox vm-docker "$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" bash "$REPO_ROOT/install-vm.sh" --docker --non-interactive 2>&1)"; RC=$?
expect "install-vm.sh --release with --docker is refused (the signed release is the systemd binary)" 1 \
    "the Docker mode builds the collector image from source" "$OUT"
OUT="$(sandbox vm-notag "$P_ALL" bash "$REPO_ROOT/install-vm.sh" --release main --non-interactive 2>&1)"; RC=$?
expect "install-vm.sh --release accepts only a version tag" 1 "--release expects a version tag" "$OUT"

# Messages at the start (transition rule of E-69).
MAIN_STUBS='preflight() { :; }; collect_config() { :; }; write_env() { :; }; ensure_sources() { :; }
    provide_collector_systemd() { :; }; install_systemd_unit() { :; }; verify() { :; }; print_summary() { :; }
    RETICORA_SERVER_URL=https://cmdb.example.test RETICORA_ORGANIZATION_ID=o RETICORA_COLLECTOR_ID=c
    RETICORA_SCAN_SUBNETS=10.0.0.0/24 RETICORA_DISCOVERY_PROTOCOLS=sweep RETICORA_DISCOVERY_INTERVAL=15m
    RETICORA_HEARTBEAT_INTERVAL=1m RETICORA_TLS_CLIENT_CERT_FILE=; NON_INTERACTIVE=1; main'
OUT="$(vm vm-main-src "$P_ALL" -- "$MAIN_STUBS")"; RC=$?
expect "install-vm.sh: the source build reports 'not signature-verified' at the start" 0 \
    "Collector: built from source \(or a local prebuilt binary\) — not signature-verified" "$OUT"
OUT="$(vm vm-main-rel "$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" -- "$MAIN_STUBS")"; RC=$?
expect "install-vm.sh --release: the start names the signed release" 0 "Collector: signed release $TAG" "$OUT" "not signature-verified"

# Source of a release tag (RETICORA_REPO_REF), then the build.
SRC_ENV=(RETICORA_REPO_REF="$TAG")
OUT="$(vm vm-src "$STUB_GO:$P_ALL" RETICORA_REPO_URL="file://$SRC_REPO" "${SRC_ENV[@]}" -- \
    'RETICORA_SOURCE_DIR="$CASE/src"; ensure_sources || true; provide_collector_systemd')"; RC=$?
calls="$(cat "$(case_dir vm-src)/calls")"
grep -q '^go build ./collector/cmd/collector$' <<<"$calls" || RC=96
expect "install-vm.sh: release tag as source (git) — the commit matches the signed release, then the build" 0 \
    "Source verified: $TAG is commit $commit" "$OUT"

OUT="$(vm vm-src-evil "$STUB_GO:$P_ALL" RETICORA_REPO_URL="file://$EVIL_REPO" "${SRC_ENV[@]}" -- \
    'RETICORA_SOURCE_DIR="$CASE/src"; ensure_sources || true; provide_collector_systemd')"; RC=$?
grep -q '^go build' "$(case_dir vm-src-evil)/calls" && RC=95
expect "install-vm.sh: a tag checkout with another commit stops before the build" 1 \
    "Source check failed: the checkout of $TAG" "$OUT"

OUT="$(vm vm-src-tar "$STUB_GO:$STUB_COSIGN:$STUB_CURL:$SYS" "${SRC_ENV[@]}" -- \
    'RETICORA_SOURCE_DIR="$CASE/src"; ensure_sources || true; provide_collector_systemd')"; RC=$?
grep -q '^go build ./collector/cmd/collector$' "$(case_dir vm-src-tar)/calls" || RC=96
expect "install-vm.sh: release tag as source without git — verified tarball, then the build" 0 \
    "Source verified: reticora-source-$TAG.tar.gz matches the signed release manifest" "$OUT"

OUT="$(vm vm-src-tar-evil "$STUB_GO:$STUB_COSIGN:$STUB_CURL:$SYS" FAKE_RELEASES="$(variant tarball)" "${SRC_ENV[@]}" -- \
    'RETICORA_SOURCE_DIR="$CASE/src"; ensure_sources || true; provide_collector_systemd')"; RC=$?
{ grep -q '^go build' "$(case_dir vm-src-tar-evil)/calls" || [ -e "$(case_dir vm-src-tar-evil)/src/go.work" ]; } && RC=95
expect "install-vm.sh: a manipulated source tarball stops before it is unpacked and built" 1 \
    "Checksum mismatch: reticora-source-$TAG.tar.gz" "$OUT"

OUT="$(vm vm-src-main "$STUB_GO:$P_ALL" RETICORA_REPO_URL="file://$SRC_REPO" RETICORA_REPO_REF=main -- \
    'RETICORA_SOURCE_DIR="$CASE/src"; ensure_sources || true; provide_collector_systemd')"; RC=$?
grep -q '^cosign' "$(case_dir vm-src-main)/calls" && RC=94
expect "install-vm.sh: a ref that is no release tag is built with 'Source not signature-verified (no release tag)'" 0 \
    "Source not signature-verified \(no release tag\): 'main'" "$OUT"

# Go toolchain of the source build, checked against go.dev. The cases run
# only when the installer unpacks below GO_INSTALL_PARENT: an installer
# without it would replace /usr/local/go of the machine running the test.
TOOLCHAIN='SOURCE_DIR="$CASE/src"; mkdir -p "$SOURCE_DIR/collector"; printf "go 1.26.9\n" > "$SOURCE_DIR/go.work"
    provide_collector_systemd'
if grep -qF 'rm -rf "$GO_INSTALL_PARENT/go"' install-vm.sh && ! grep -qE 'rm -rf /usr/local/go|tar -C /usr/local ' install-vm.sh; then
OUT="$(vm vm-go "$P_ALL" -- "$TOOLCHAIN")"; RC=$?
{ [ -x "$(case_dir vm-go)/usr-local/go/bin/go" ] && grep -q '^go build ./collector/cmd/collector$' "$(case_dir vm-go)/calls"; } || RC=93
expect "install-vm.sh: a Go toolchain with the sha256 of go.dev is installed and builds" 0 "Go toolchain: go1.26.9" "$OUT"

printf 'x' > "$TMP_ROOT/other"
go_json "$(sha256sum "$TMP_ROOT/other" | cut -d' ' -f1)" > "$TMP_ROOT/dl-mismatch.json"
OUT="$(vm vm-go-bad "$P_ALL" FAKE_GO_JSON="$TMP_ROOT/dl-mismatch.json" -- "$TOOLCHAIN")"; RC=$?
{ [ -e "$(case_dir vm-go-bad)/usr-local/go" ] || grep -q '^go build' "$(case_dir vm-go-bad)/calls"; } && RC=92
expect "install-vm.sh: a Go toolchain that does not match the sha256 of go.dev is not unpacked" 1 \
    "Checksum mismatch: the downloaded go1.26.9.linux-$ARCH.tar.gz is not the file go.dev publishes" "$OUT"

go_json "$(printf '%064d' 3)" "go1.26.9.linux-s390x.tar.gz" > "$TMP_ROOT/dl-missing.json"
OUT="$(vm vm-go-none "$P_ALL" FAKE_GO_JSON="$TMP_ROOT/dl-missing.json" -- "$TOOLCHAIN")"; RC=$?
[ -e "$(case_dir vm-go-none)/usr-local/go" ] && RC=92
expect "install-vm.sh: without a published sha256 the Go toolchain is not installed unverified" 1 \
    "the Go toolchain is not installed unverified" "$OUT"
else
    for c in "a Go toolchain with the sha256 of go.dev is installed and builds" \
        "a Go toolchain that does not match the sha256 of go.dev is not unpacked" \
        "without a published sha256 the Go toolchain is not installed unverified"; do
        fail "install-vm.sh: $c" "install-vm.sh does not install the toolchain below GO_INSTALL_PARENT (not run)"
    done
fi

# ─── install-cloud.sh: release mode ───────────────────────────────────────────
CLOUD_DIR="$TMP_ROOT/cloud"
mkdir -p "$CLOUD_DIR"
head -n -1 install-cloud.sh > "$CLOUD_DIR/harness.sh"
cp install-preflight.sh "$CLOUD_DIR/"
# cloud <case> <PATH> [VAR=value …] -- <script> — install-cloud.sh functions
# with compose stubbed (logged) and .env/override below the case directory.
cloud() {
    local name="$1" path="$2"; shift 2
    local vars=()
    while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do vars+=("$1"); shift; done
    shift
    sandbox "$name" "$path" "${vars[@]}" bash -c 'set -euo pipefail; set --; . "$0"; NON_INTERACTIVE=1
        ENV_FILE="$CASE/.env"; OVERRIDE_FILE="$CASE/override.yml"; : > "$OVERRIDE_FILE"
        printf "RETICORA_IMAGE_REGISTRY=x\nRETICORA_SERVER_IMAGE=\nRETICORA_FRONTEND_IMAGE=\nRETICORA_POSTGRES_IMAGE=\nRETICORA_HTTP_BIND=127.0.0.1\n" > "$ENV_FILE"
        compose_cmd() { printf "compose %s\n" "$*" >> "$FAKE_LOG"; }
        RETICORA_IMAGE_REGISTRY="${T_REGISTRY-ghcr.io/datahub-chiemgau}"; RETICORA_IMAGE_TAG="${T_TAG:-v1.2.3}"; RETICORA_TLS_DOMAIN=""
        '"$1" "$CLOUD_DIR/harness.sh" 2>&1
}
pinned() { grep -c -E "^RETICORA_(SERVER|FRONTEND|POSTGRES)_IMAGE=ghcr\.io/datahub-chiemgau/reticora-(server|frontend|postgres)@sha256:[0-9]{64}$" "$(case_dir "$1")/.env"; }
compose_calls() { grep '^compose' "$(case_dir "$1")/calls"; }

OUT="$(cloud cl-ok "$P_ALL" -- provide_images)"; RC=$?
env_file="$(case_dir cl-ok)/.env"
if [ "$(pinned cl-ok)" -eq 3 ] \
    && grep -qx "RETICORA_SERVER_IMAGE=$IMAGE_REGISTRY/reticora-server@sha256:$(printf '%064d' 1)" "$env_file" \
    && grep -qx "RETICORA_POSTGRES_IMAGE=$IMAGE_REGISTRY/reticora-postgres@sha256:$(printf '%064d' 3)" "$env_file" \
    && grep -qx 'RETICORA_HTTP_BIND=127.0.0.1' "$env_file" && [ "$(stat -c %a "$env_file")" = 600 ] \
    && [ "$(compose_calls cl-ok)" = "compose pull server frontend postgres" ] \
    && [ -f "$(case_dir cl-ok)/override.yml.disabled" ] && [ ! -e "$(case_dir cl-ok)/override.yml" ]; then :; else RC=91; fi
expect "install-cloud.sh release mode: server, frontend and postgres verified, pinned by digest in .env, pulled — nothing built" 0 \
    "Signed image verified: reticora-postgres@sha256:" "$OUT"
calls="$(cat "$(case_dir cl-ok)/calls")"
if [ "$(grep -cE "^cosign verify $IMAGE_REGISTRY/reticora-(server|frontend|postgres):$TAG " <<<"$calls")" -eq 3 ] \
    && [ "$(grep -cF -- ":$TAG $SIGNER_ARGS" <<<"$calls")" -eq 3 ]; then
    ok "install-cloud.sh release mode: cosign verify pins the release identity of the tag and the GitHub Actions issuer"
else
    fail "install-cloud.sh release mode: cosign verify pins the release identity of the tag and the GitHub Actions issuer" "$calls"
fi

# Every failure stops before compose (no pull, no start), .env unpinned.
cloud_refused() { # <case> <name> <pattern> [VAR=value …]
    local c="$1" name="$2" pattern="$3"; shift 3
    OUT="$(cloud "$c" "${CLOUD_PATH:-$P_ALL}" "$@" -- provide_images)"; RC=$?
    { [ -n "$(compose_calls "$c")" ] || [ "$(pinned "$c")" -ne 0 ]; } && RC=90
    expect "$name" 1 "$pattern" "$OUT"
}
cloud_refused cl-rc1 "install-cloud.sh release mode: cosign verify rc 1 stops before compose" \
    "Signature check failed: $IMAGE_REGISTRY/reticora-server:$TAG is not signed" FAKE_COSIGN_RC=1
reg="$TMP_ROOT/registry-fork"; cp -r "$REGISTRY" "$reg"
sed -i "s|^identity=.*|identity=$(identity_of "$TAG" attacker/Reticora-CMDB)|" "$(record "$reg" reticora-frontend "$TAG")"
cloud_refused cl-fork "install-cloud.sh release mode: an image signed by another identity stops before compose" \
    "Signature check failed: $IMAGE_REGISTRY/reticora-frontend:$TAG is not signed" FAKE_REGISTRY="$reg"
reg="$TMP_ROOT/registry-older"; cp -r "$REGISTRY" "$reg"
sed -i "s|^identity=.*|identity=$(identity_of v1.2.2)|" "$(record "$reg" reticora-postgres "$TAG")"
cloud_refused cl-older "install-cloud.sh release mode: an image of another release (v1.2.2) under $TAG stops" \
    "Signature check failed: $IMAGE_REGISTRY/reticora-postgres:$TAG is not signed" FAKE_REGISTRY="$reg"
reg="$TMP_ROOT/registry-swap"; cp -r "$REGISTRY" "$reg"
cp "$(record "$reg" reticora-frontend "$TAG")" "$(record "$reg" reticora-server "$TAG")"
cloud_refused cl-swap "install-cloud.sh release mode: the signed frontend image under the server name stops" \
    "do not name reticora-server with one digest" FAKE_REGISTRY="$reg"
CLOUD_PATH="$P_NOCOSIGN" cloud_refused cl-nocosign "install-cloud.sh release mode: without cosign nothing is pulled or started" \
    "cosign is required to verify the signed release artifacts"

OUT="$(cloud cl-latest "$P_ALL" T_TAG=latest -- provide_images)"; RC=$?
grep -qF -- ":latest --certificate-identity-regexp $ANY_IDENTITY_RE --certificate-oidc-issuer $ISSUER" "$(case_dir cl-latest)/calls" || RC=89
expect "install-cloud.sh release mode: a tag that is no version tag accepts any release of the workflow (digest pinned)" 0 \
    "Signed image verified: reticora-server@sha256:" "$OUT"

# The whole run: verification before the start; a failure never reaches it.
CLOUD_MAIN='preflight() { :; }; collect_config() { :; }; write_env() { :; }; ensure_session_key() { :; }
    configure_realm() { :; }; bootstrap_tls_cert() { :; }; issue_tls_certificate() { :; }; verify_stack() { :; }
    gc_images() { :; }; print_summary() { :; }; start_stack() { printf "start_stack\n" >> "$FAKE_LOG"; }
    RETICORA_PUBLIC_BASE_URL=http://h RETICORA_OIDC_ISSUER_URL=http://h/realms/reticora RETICORA_OIDC_REDIRECT_URL=http://h/cb
    RETICORA_FRONTEND_PORT=3000 RETICORA_SERVER_PORT=8080 RETICORA_KEYCLOAK_PORT=8180; main'
OUT="$(cloud cl-main "$P_ALL" -- "$CLOUD_MAIN")"; RC=$?
order="$(grep -E '^(cosign verify|compose pull|start_stack)' "$(case_dir cl-main)/calls" | cut -d' ' -f1-2 | uniq | tr '\n' ',')"
[ "$order" = "cosign verify,compose pull,start_stack," ] || RC=88
expect "install-cloud.sh release mode: verify < pull < start of the stack" 0 "Release mode: the images are verified with cosign" "$OUT"
OUT="$(cloud cl-main-fail "$P_ALL" FAKE_COSIGN_RC=1 -- "$CLOUD_MAIN")"; RC=$?
grep -qE '^(start_stack|compose)' "$(case_dir cl-main-fail)/calls" && RC=87
expect "install-cloud.sh release mode: a failed verification stops before compose up" 1 "Signature check failed" "$OUT"

OUT="$(cloud cl-src "$P_ALL" T_REGISTRY= -- "$CLOUD_MAIN")"; RC=$?
calls="$(cat "$(case_dir cl-src)/calls")"
{ grep -qx 'compose build server frontend' <<<"$calls" && grep -qx 'compose build postgres' <<<"$calls" \
    && ! grep -q '^cosign' <<<"$calls"; } || RC=86
expect "install-cloud.sh: the local source build reports 'not signature-verified' at the start and builds postgres" 0 \
    "Local source build: the images are built from this checkout and are not signature-verified" "$OUT"

# A pin of an earlier release-mode run is never reused: collect_config clears
# it, write_env writes the keys empty for the source build.
LEGACY='printf "%s\n" RETICORA_DB_PASSWORD=legacy-db-password RETICORA_S3_ACCESS_KEY=reticora \
    RETICORA_S3_SECRET_KEY=legacy-s3-secret KEYCLOAK_ADMIN=admin KEYCLOAK_ADMIN_PASSWORD=legacy-kc-password \
    KEYCLOAK_DB_PASSWORD=legacy-kc-db-password RETICORA_INITIAL_ADMIN_EMAIL=ops@acme.example \
    RETICORA_OIDC_CLIENT_SECRET=legacy-client-secret RETICORA_MASTER_KEY=bGVnYWN5LW1hc3Rlci1rZXktMzItYnl0ZXMtbG9uZyE= \
    RETICORA_BACKUP_CIPHER_PASS=legacy-cipher \
    RETICORA_PUBLIC_BASE_URL=http://192.0.2.10:3000 RETICORA_OIDC_ISSUER_URL=http://192.0.2.10:8180/realms/reticora \
    RETICORA_OIDC_CLIENT_ID=reticora-app RETICORA_OIDC_REDIRECT_URL=http://192.0.2.10:3000/auth/callback RETICORA_TLS_DOMAIN= \
    RETICORA_FRONTEND_PORT=3000 RETICORA_SERVER_PORT=8080 RETICORA_KEYCLOAK_PORT=8180 RETICORA_IMAGE_REGISTRY= \
    RETICORA_IMAGE_TAG=latest RETICORA_SERVER_IMAGE=ghcr.io/datahub-chiemgau/reticora-server@sha256:'"$(printf '%064d' 9)"' > "$ENV_FILE"
    collect_config >/dev/null; write_env'
OUT="$(cloud cl-stale "$P_ALL" RETICORA_FRONTEND_IMAGE=evil/frontend:latest -- "$LEGACY")"; RC=$?
env_file="$(case_dir cl-stale)/.env"
{ [ "$(grep -c '^RETICORA_SERVER_IMAGE=$' "$env_file")" -eq 1 ] && grep -qx 'RETICORA_FRONTEND_IMAGE=' "$env_file" \
    && grep -qx 'RETICORA_POSTGRES_IMAGE=' "$env_file" && ! grep -q 'sha256:\|evil' "$env_file"; } || RC=85
expect "install-cloud.sh: write_env drops image pins of an earlier run and from the environment (source build)" 0 "" "$OUT"

# ─── The hint of every message leads to the troubleshooting section ─────────
doc="$(sed -n '/^## 9\. Signature verification$/,/^## /p' docs/troubleshooting-installation.md)"
missing=""
for m in "Signature check failed" "Checksum mismatch" "Source check failed" "cosign is required" \
    "does not match its published sha256 checksum" "Could not download" "Source not signature-verified (no release tag)" \
    "not signature-verified" "RETICORA_IMAGE_REGISTRY" "--release" "SOURCE_COMMIT"; do
    grep -qF -- "$m" <<<"$doc" || missing="$missing '$m'"
done
if grep -qF "SIGNATURE_TROUBLESHOOTING=\"see docs/troubleshooting-installation.md, 'Signature verification'\"" install-preflight.sh \
    && [ -z "$missing" ]; then
    ok "docs/troubleshooting-installation.md 'Signature verification' describes the modes and every error message"
else
    fail "docs/troubleshooting-installation.md 'Signature verification' describes the modes and every error message" "missing:$missing"
fi

# ─── Optional: the real cosign (RETICORA_TEST_COSIGN) ────────────────────────
if [ -z "${RETICORA_TEST_COSIGN:-}" ]; then
    printf 'skip real cosign cases (RETICORA_TEST_COSIGN not set)\n'
else
    REAL="$TMP_ROOT/real-cosign"
    mkdir -p "$REAL"
    OUT="$(vm real-pin "$P_NOCOSIGN" FAKE_COSIGN_DOWNLOAD="$RETICORA_TEST_COSIGN" -- \
        'ensure_cosign; "$COSIGN_BIN" version 2>&1 | grep -E "GitVersion"')"; RC=$?
    expect "real cosign: the pinned sha256 of cosign v2.4.1 linux-$ARCH matches the release binary" 0 "GitVersion: +v2\.4\.1" "$OUT"
    printf 'payload\n' > "$REAL/blob"
    (cd "$REAL" && COSIGN_PASSWORD="" "$RETICORA_TEST_COSIGN" generate-key-pair >/dev/null 2>&1 \
        && COSIGN_PASSWORD="" "$RETICORA_TEST_COSIGN" sign-blob --yes --key cosign.key --tlog-upload=false \
            --bundle blob.sigstore.json blob >/dev/null 2>&1)
    ln -sf "$RETICORA_TEST_COSIGN" "$REAL/cosign"
    OUT="$(vm real-blob "$REAL:$P_NOCOSIGN" -- "cosign_verify_blob '$REAL/blob' '$REAL/blob.sigstore.json' $TAG")"; RC=$?
    expect "real cosign: the installers' verify-blob rejects a bundle not signed by the release workflow" 1 \
        "Signature check failed: blob is not signed" "$OUT" "unknown (flag|shorthand)|required flag"
    OUT="$(cloud real-image "$REAL:$P_NOCOSIGN" T_REGISTRY=127.0.0.1:9/reticora -- provide_images)"; RC=$?
    expect "real cosign: the installers' verify command line is accepted and fails closed without a signature" 1 \
        "Signature check failed: 127\.0\.0\.1:9/reticora/reticora-server:$TAG is not signed" "$OUT" "unknown (flag|shorthand)|required flag"
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
