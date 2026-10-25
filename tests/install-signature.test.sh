#!/usr/bin/env bash
# shellcheck disable=SC2016 # the scripts in single quotes run inside the sandbox
#
# install-signature.test.sh — signature verification before the installation
# (WP-387: TEC-15, COL-06; tools per E-31) and its default (WP-388: E-69,
# Option 1 — the signed release artifacts, the local build only with
# --build-from-source):
#
#   - install-cloud.sh release mode (the default): reticora-server,
#     reticora-frontend and reticora-postgres pass 'cosign verify' against the
#     release workflow identity and are pinned by digest in .env before
#     anything starts, nothing is built; the release is --release, the tag of
#     the checkout or the newest release, and the checkout must be its signed
#     source; --build-from-source builds and warns "not signature-verified";
#   - install-vm.sh (default: the signed collector of a release): the binary
#     passes 'cosign verify-blob' and its sha256 in the signed SHA256SUMS
#     before it is copied, the Docker mode pins the verified image; the Go
#     toolchain of the source build is checked against go.dev;
#   - install.sh and install-vm.sh with a release tag as source (by default
#     the newest release): the signed SHA256SUMS, the commit of the checkout
#     (SOURCE_COMMIT) or the source tarball, checked before anything of it
#     runs or is built; another ref only with --build-from-source;
#   - release.yml publishes what the installers verify: its steps run here
#     with stubbed cosign/go/syft and produce the release the cases install;
#     the workflows install the unpublished state only with --build-from-source.
#
# cosign, curl and docker are stubbed, git is real (local repositories). The cosign
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
# The cosign calls live in the shared signature block (install-preflight.sh,
# sourced by install-cloud.sh and install-vm.sh; an identical copy in
# install.sh); each installer calls its checks in its own code.
block() { sed -n '/^# BEGIN shared signature block/,/^# END shared signature block/p' "$1"; }
own_code() { sed '/^# BEGIN shared signature block/,/^# END shared signature block/d' "$1" | grep -vE '^[[:space:]]*#'; }
shared_code="$(block install-preflight.sh | grep -vE '^[[:space:]]*#')"
for f in install-vm.sh install-cloud.sh install.sh; do
    calls_made="$(own_code "$f" | grep -oE '\b(verify_release_image|cosign_verify_blob|verify_source_checkout|fetch_source_tarball)\b' | sort -u | tr '\n' ' ')"
    if grep -qE '"\$COSIGN_BIN" verify "' <<<"$shared_code" && grep -qE '"\$COSIGN_BIN" verify-blob ' <<<"$shared_code" \
        && [ -n "$calls_made" ] && { [ "$f" = install.sh ] || grep -q '^\. "\$SCRIPT_DIR/install-preflight.sh"' "$f"; }; then
        ok "install_signature_${f%.sh}: ${f} verifies the cosign signature of what it installs (${calls_made% })"
    else
        fail "install_signature_${f%.sh}: ${f} verifies the cosign signature of what it installs" \
            "no 'cosign verify'/'cosign verify-blob' in the shared block, or ${f} calls none of its checks"
    fi
done

# install.sh runs before the sources are downloaded and carries its own copy
# of the shared signature block of install-preflight.sh.
if [ -n "$(block install-preflight.sh)" ] && diff <(block install.sh) <(block install-preflight.sh) >"$TMP_ROOT/block.diff"; then
    ok "install.sh and install-preflight.sh carry the same signature block (identity, cosign pin, checks)"
else
    fail "install.sh and install-preflight.sh carry the same signature block (identity, cosign pin, checks)" "$(head -n 8 "$TMP_ROOT/block.diff")"
fi

# ─── Sandbox: stubs and a clean PATH ─────────────────────────────────────────
SYS="$TMP_ROOT/sys"; GITBIN="$TMP_ROOT/gitbin"
STUB_CURL="$TMP_ROOT/stub-curl"; STUB_COSIGN="$TMP_ROOT/stub-cosign"; STUB_GO="$TMP_ROOT/stub-go"
STUB_DOCKER="$TMP_ROOT/stub-docker"
mkdir -p "$SYS" "$GITBIN" "$STUB_CURL" "$STUB_COSIGN" "$STUB_GO" "$STUB_DOCKER"
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
# Fake curl: serves the release, the newest release (GitHub API), the cosign
# download and go.dev from fixtures.
out="" url=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) out="$2"; shift ;;
        -H) shift ;;
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
    https://api.github.com/repos/DataHub-Chiemgau/Reticora-CMDB/releases/latest) src="${FAKE_LATEST:-}" ;;
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
cat > "$STUB_DOCKER/docker" <<'STUB'
#!/usr/bin/env bash
# Fake docker: logs every call; 'compose version' answers like Compose v2.
printf 'docker %s\n' "$*" >> "$FAKE_LOG"
[ "$*" != "compose version" ] || echo "Docker Compose version v2.29.1"
STUB
chmod +x "$STUB_CURL/curl" "$STUB_COSIGN/cosign" "$STUB_GO/go" "$STUB_GO/syft" "$STUB_DOCKER/docker"

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
        FAKE_LATEST="$LATEST_JSON" RETICORA_TOOLS_DIR="$dir/tools" RETICORA_OS_RELEASE="$OS_RELEASE" "$@"
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
    # The component installers report their arguments and the mode install.sh
    # hands over (RETICORA_RELEASE, RETICORA_BUILD_FROM_SOURCE).
    for s in install-cloud.sh install-vm.sh; do
        printf '#!/usr/bin/env bash\necho "RAN %s ($(cat "$(dirname "$0")/MARKER")) $* [release=${RETICORA_RELEASE:-} build=${RETICORA_BUILD_FROM_SOURCE:-}]"\n' "$s" > "$dir/$s"
        chmod +x "$dir/$s"
    done
    printf '# preflight\n' > "$dir/install-preflight.sh"
    mkdir -p "$dir/deploy/docker-compose"
    printf 'services: {}\n' > "$dir/deploy/docker-compose/docker-compose.override.yml"
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

# The newest release as the GitHub API reports it (pretty-printed, the tag
# also in the release notes).
LATEST_JSON="$TMP_ROOT/latest.json"
cat > "$LATEST_JSON" <<JSON
{
  "url": "https://api.github.com/repos/$REPOSITORY/releases/1",
  "tag_name": "$TAG",
  "name": "$TAG",
  "draft": false,
  "prerelease": false,
  "body": "Changes since \"tag_name\": \"v0.9.0\"",
  "assets": [
    {
      "name": "SHA256SUMS"
    }
  ]
}
JSON

# Images: a registry record per <registry>/<image>:<tag> (digest, signer,
# docker-reference of the signature).
REGISTRY="$TMP_ROOT/registry"
IMAGE_REGISTRY="ghcr.io/datahub-chiemgau"
make_registry() { # <dir> <image tag> <release tag of the signature>
    local image n=0
    mkdir -p "$1"
    for image in reticora-server reticora-frontend reticora-postgres reticora-collector; do
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

# ─── install.sh: the default (E-69, Option 1) and --build-from-source ────────
OUT="$(install_sh sh-latest "$P_ALL" -- --cloud --non-interactive --repo "file://$SRC_REPO")"; RC=$?
grep -qx 'curl https://api.github.com/repos/DataHub-Chiemgau/Reticora-CMDB/releases/latest' "$(case_dir sh-latest)/calls" || RC=83
expect "install.sh: without --ref the newest release is downloaded, verified and handed to the installer" 0 \
    "Newest release: $TAG.*Source verified: $TAG is commit $commit.*RAN install-cloud.sh \(release\) --non-interactive \[release=$TAG build=0\]" \
    "$(printf '%s' "$OUT" | tr '\n' ' ')" "not signature-verified"

OUT="$(install_sh sh-latest-down "$P_ALL" FAKE_LATEST=/nonexistent -- --cloud --non-interactive --repo "file://$SRC_REPO")"; RC=$?
[ -e "$(case_dir sh-latest-down)/src" ] && RC=82
expect "install.sh: without a reachable newest release nothing is downloaded or run" 1 \
    "Could not determine the newest release of DataHub-Chiemgau/Reticora-CMDB" "$OUT" "RAN install"

OUT="$(install_sh sh-main "$P_ALL" -- --cloud --non-interactive --repo "file://$SRC_REPO" --ref main)"; RC=$?
{ [ -s "$(case_dir sh-main)/calls" ] || [ -e "$(case_dir sh-main)/src" ]; } && RC=84
expect "install.sh: --ref main without --build-from-source stops before the download, with the hint" 1 \
    "Source not signature-verified \(no release tag\): 'main' is not installed by default.*--build-from-source" \
    "$(printf '%s' "$OUT" | tr '\n' ' ')" "RAN install"

OUT="$(install_sh sh-bfs "$P_ALL" -- --cloud --non-interactive --repo "file://$SRC_REPO" --build-from-source)"; RC=$?
[ -s "$(case_dir sh-bfs)/calls" ] && RC=84
expect "install.sh --build-from-source: main is downloaded with 'Source not signature-verified', the installer builds" 0 \
    "Source not signature-verified \(no release tag\): 'main' is used as downloaded \(--build-from-source\).*RAN install-cloud.sh \(release\) --non-interactive \[release= build=1\]" \
    "$(printf '%s' "$OUT" | tr '\n' ' ')"

OUT="$(install_sh sh-bfs-env "$P_ALL" RETICORA_BUILD_FROM_SOURCE=1 -- --vm --non-interactive --repo "file://$SRC_REPO" --ref main)"; RC=$?
expect "install.sh: RETICORA_BUILD_FROM_SOURCE=1 works like --build-from-source" 0 \
    "'main' is used as downloaded \(--build-from-source\).*RAN install-vm.sh \(release\) --non-interactive \[release= build=1\]" "$(printf '%s' "$OUT" | tr '\n' ' ')"

OUT="$(install_sh sh-bfs-tag "$P_ALL" RETICORA_RELEASE="$TAG" -- --cloud --non-interactive --repo "file://$SRC_REPO" --build-from-source)"; RC=$?
expect "install.sh --build-from-source with RETICORA_RELEASE: the verified source of the tag is built, no release handed over" 0 \
    "Source verified: $TAG is commit $commit.*RAN install-cloud.sh \(release\) --non-interactive \[release= build=1\]" "$(printf '%s' "$OUT" | tr '\n' ' ')"

OUT="$(install_sh sh-bfs-bad "$P_ALL" RETICORA_BUILD_FROM_SOURCE=yes -- --cloud --non-interactive --repo "file://$SRC_REPO")"; RC=$?
expect "install.sh: RETICORA_BUILD_FROM_SOURCE accepts only 1 or 0" 2 "RETICORA_BUILD_FROM_SOURCE must be 1" "$OUT" "RAN install"

# install.sh next to the sources (a checkout): no download; a ref that is no
# release tag still needs the switch, a release tag is handed over.
LOCAL_CO="$TMP_ROOT/local-checkout"
git clone -q "file://$SRC_REPO" "$LOCAL_CO"
cp install.sh "$LOCAL_CO/install.sh"
local_sh() { # <case> [VAR=value …] -- <arguments>
    local name="$1"; shift
    local vars=()
    while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do vars+=("$1"); shift; done
    shift
    sandbox "$name" "$P_ALL" "${vars[@]}" bash "$LOCAL_CO/install.sh" "$@" </dev/null 2>&1
}
OUT="$(local_sh sh-local-main -- --cloud --non-interactive --ref main)"; RC=$?
expect "install.sh in a checkout: --ref main without --build-from-source stops" 1 \
    "'main' is not installed by default" "$OUT" "RAN install"
OUT="$(local_sh sh-local-tag -- --cloud --non-interactive --ref "$TAG")"; RC=$?
expect "install.sh in a checkout: --ref $TAG is handed to the installer as the release" 0 \
    "RAN install-cloud.sh \(release\) --non-interactive \[release=$TAG build=0\]" "$OUT"
OUT="$(local_sh sh-local-default -- --cloud --non-interactive)"; RC=$?
[ -s "$(case_dir sh-local-default)/calls" ] && RC=84
expect "install.sh in a checkout: without --ref the installer chooses the release itself" 0 \
    "RAN install-cloud.sh \(release\) --non-interactive \[release= build=0\]" "$OUT"
OUT="$(local_sh sh-local-bfs -- --vm --non-interactive --build-from-source --ref main)"; RC=$?
expect "install.sh in a checkout: --build-from-source --ref main runs the installer with the switch" 0 \
    "RAN install-vm.sh \(release\) --non-interactive \[release= build=1\]" "$OUT" "is not installed by default"

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

OUT="$(sandbox vm-notag "$P_ALL" bash "$REPO_ROOT/install-vm.sh" --release main --non-interactive 2>&1)"; RC=$?
expect "install-vm.sh --release accepts only a version tag" 1 "--release expects a version tag" "$OUT"
OUT="$(sandbox vm-rel-bfs "$P_ALL" bash "$REPO_ROOT/install-vm.sh" --release "$TAG" --build-from-source --non-interactive 2>&1)"; RC=$?
expect "install-vm.sh: --release and --build-from-source together are refused" 1 "use one of them" "$OUT"
OUT="$(sandbox vm-ref-main "$P_ALL" RETICORA_REPO_REF=main bash "$REPO_ROOT/install-vm.sh" --non-interactive 2>&1)"; RC=$?
[ -s "$(case_dir vm-ref-main)/calls" ] && RC=84
expect "install-vm.sh: RETICORA_REPO_REF=main without --build-from-source stops at the start, with the hint" 1 \
    "'main' is not installed by default.*--build-from-source" "$(printf '%s' "$OUT" | tr '\n' ' ')"
OUT="$(vm vm-norelease "$P_ALL" -- 'SOURCE_DIR="$CASE/src"; mkdir -p "$SOURCE_DIR/bin"; printf "#!/bin/sh\n" > "$SOURCE_DIR/bin/collector"
    chmod +x "$SOURCE_DIR/bin/collector"; provide_collector_systemd')"; RC=$?
installed vm-norelease && RC=97
expect "install-vm.sh: without a release and without --build-from-source no local binary is installed" 1 \
    "No release selected for the signed collector" "$OUT"

# Docker mode: the signed image reticora-collector, pinned by digest.
DOCKER_SETUP='MODE=docker; SOURCE_DIR="$CASE/src"; ENV_FILE="$CASE/opt/collector.env"; install_docker_setup'
COLLECTOR_PIN="$IMAGE_REGISTRY/reticora-collector@sha256:$(printf '%064d' 4)"
OUT="$(vm vm-docker "$STUB_DOCKER:$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" -- "$DOCKER_SETUP")"; RC=$?
compose_file="$(case_dir vm-docker)/opt/docker-compose.yml"
order="$(grep -E '^(cosign verify|docker compose (pull|build|up))' "$(case_dir vm-docker)/calls" | cut -d' ' -f1-3 | tr '\n' ',')"
{ grep -qx "    image: $COLLECTOR_PIN" "$compose_file" && ! grep -q 'build:' "$compose_file" \
    && [ "$order" = "cosign verify $IMAGE_REGISTRY/reticora-collector:$TAG,docker compose pull,docker compose up," ] \
    && grep -qF -- "reticora-collector:$TAG $SIGNER_ARGS" "$(case_dir vm-docker)/calls"; } || RC=81
expect "install-vm.sh --docker (default): the signed collector image is verified, pinned by digest and pulled — nothing built" 0 \
    "Signed image verified: reticora-collector@sha256:" "$OUT"
OUT="$(vm vm-docker-rc1 "$STUB_DOCKER:$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" FAKE_COSIGN_RC=1 -- "$DOCKER_SETUP")"; RC=$?
{ grep -q '^docker compose' "$(case_dir vm-docker-rc1)/calls" || [ -e "$(case_dir vm-docker-rc1)/opt/docker-compose.yml" ]; } && RC=80
expect "install-vm.sh --docker: cosign verify rc 1 stops before compose" 1 \
    "Signature check failed: $IMAGE_REGISTRY/reticora-collector:$TAG is not signed" "$OUT"
OUT="$(vm vm-docker-bfs "$STUB_DOCKER:$P_ALL" RETICORA_BUILD_FROM_SOURCE=1 -- \
    'mkdir -p "$CASE/src/collector"; : > "$CASE/src/collector/Dockerfile"; '"$DOCKER_SETUP")"; RC=$?
calls="$(cat "$(case_dir vm-docker-bfs)/calls")"
{ grep -qx 'docker compose build' <<<"$calls" && ! grep -q '^cosign' <<<"$calls" \
    && grep -qx '    image: reticora-collector:latest' "$(case_dir vm-docker-bfs)/opt/docker-compose.yml" \
    && grep -qx "      context: $(case_dir vm-docker-bfs)/src" "$(case_dir vm-docker-bfs)/opt/docker-compose.yml"; } || RC=79
expect "install-vm.sh --docker --build-from-source: the image is built from the sources, no cosign" 0 "Collector container is running" "$OUT"

# The whole run (main): the default selects a release and installs the
# signed binary; --build-from-source says 'not signature-verified'.
MAIN_STUBS='preflight() { :; }; collect_config() { :; }; write_env() { :; }
    install_systemd_unit() { :; }; verify() { :; }; print_summary() { :; }
    RETICORA_SERVER_URL=https://cmdb.example.test RETICORA_ORGANIZATION_ID=o RETICORA_COLLECTOR_ID=c
    RETICORA_SCAN_SUBNETS=10.0.0.0/24 RETICORA_DISCOVERY_PROTOCOLS=sweep RETICORA_DISCOVERY_INTERVAL=15m
    RETICORA_HEARTBEAT_INTERVAL=1m RETICORA_TLS_CLIENT_CERT_FILE=; NON_INTERACTIVE=1; RETICORA_SOURCE_DIR="$CASE/src"; main'
OUT="$(vm vm-main-default "$STUB_GO:$P_ALL" RETICORA_REPO_URL="file://$SRC_REPO" -- "$MAIN_STUBS")"; RC=$?
calls="$(cat "$(case_dir vm-main-default)/calls")"
{ installed vm-main-default && [ "$(collector_sum vm-main-default)" = "$(sums_line "$RELEASES" "reticora-collector-linux-$ARCH")" ] \
    && grep -qE "^cosign verify-blob .*/reticora-collector-linux-$ARCH\$" <<<"$calls" && ! grep -q '^go build' <<<"$calls" \
    && [ ! -e "$(case_dir vm-main-default)/src" ]; } || RC=78
expect "install-vm.sh (default): the newest release is selected and its signed binary installed — no sources, no build" 0 \
    "Newest release: $TAG.*Collector: signed release $TAG, verified with cosign verify-blob" "$(printf '%s' "$OUT" | tr '\n' ' ')" "not signature-verified"
OUT="$(vm vm-main-rc1 "$STUB_GO:$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" FAKE_COSIGN_RC=1 -- "$MAIN_STUBS")"; RC=$?
{ installed vm-main-rc1 || grep -q '^go build' "$(case_dir vm-main-rc1)/calls"; } && RC=97
expect "install-vm.sh (default): cosign verify-blob rc 1 stops, no fallback to a build" 1 "Signature check failed" "$OUT"
OUT="$(vm vm-main-src "$STUB_GO:$P_ALL" RETICORA_REPO_URL="file://$SRC_REPO" RETICORA_BUILD_FROM_SOURCE=1 -- "$MAIN_STUBS")"; RC=$?
calls="$(cat "$(case_dir vm-main-src)/calls")"
{ grep -q '^go build ./collector/cmd/collector$' <<<"$calls" && ! grep -q '^cosign' <<<"$calls"; } || RC=96
expect "install-vm.sh --build-from-source: 'not signature-verified' at the start, then the local build" 0 \
    "Collector: built from source \(or a local prebuilt binary\) — not signature-verified \(--build-from-source\)" "$OUT"
OUT="$(vm vm-main-rel "$P_ALL" RETICORA_COLLECTOR_RELEASE="$TAG" -- "$MAIN_STUBS")"; RC=$?
grep -q '^curl https://api.github.com' "$(case_dir vm-main-rel)/calls" && RC=77
expect "install-vm.sh --release: the start names the signed release" 0 "Collector: signed release $TAG" "$OUT" "not signature-verified"

# Source of a release tag (RETICORA_REPO_REF), then the build (--build-from-source).
SRC_ENV=(RETICORA_BUILD_FROM_SOURCE=1 RETICORA_REPO_REF="$TAG")
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

OUT="$(vm vm-src-main "$STUB_GO:$P_ALL" RETICORA_REPO_URL="file://$SRC_REPO" RETICORA_BUILD_FROM_SOURCE=1 RETICORA_REPO_REF=main -- \
    'RETICORA_SOURCE_DIR="$CASE/src"; ensure_sources || true; provide_collector_systemd')"; RC=$?
grep -q '^cosign' "$(case_dir vm-src-main)/calls" && RC=94
expect "install-vm.sh --build-from-source: a ref that is no release tag is built with 'Source not signature-verified (no release tag)'" 0 \
    "Source not signature-verified \(no release tag\): 'main'" "$OUT"

# Go toolchain of the source build, checked against go.dev. The cases run
# only when the installer unpacks below GO_INSTALL_PARENT: an installer
# without it would replace /usr/local/go of the machine running the test.
TOOLCHAIN='SOURCE_DIR="$CASE/src"; mkdir -p "$SOURCE_DIR/collector"; printf "go 1.26.9\n" > "$SOURCE_DIR/go.work"
    provide_collector_systemd'
if grep -qF 'rm -rf "$GO_INSTALL_PARENT/go"' install-vm.sh && ! grep -qE 'rm -rf /usr/local/go|tar -C /usr/local ' install-vm.sh; then
OUT="$(vm vm-go "$P_ALL" RETICORA_BUILD_FROM_SOURCE=1 -- "$TOOLCHAIN")"; RC=$?
{ [ -x "$(case_dir vm-go)/usr-local/go/bin/go" ] && grep -q '^go build ./collector/cmd/collector$' "$(case_dir vm-go)/calls"; } || RC=93
expect "install-vm.sh: a Go toolchain with the sha256 of go.dev is installed and builds" 0 "Go toolchain: go1.26.9" "$OUT"

printf 'x' > "$TMP_ROOT/other"
go_json "$(sha256sum "$TMP_ROOT/other" | cut -d' ' -f1)" > "$TMP_ROOT/dl-mismatch.json"
OUT="$(vm vm-go-bad "$P_ALL" RETICORA_BUILD_FROM_SOURCE=1 FAKE_GO_JSON="$TMP_ROOT/dl-mismatch.json" -- "$TOOLCHAIN")"; RC=$?
{ [ -e "$(case_dir vm-go-bad)/usr-local/go" ] || grep -q '^go build' "$(case_dir vm-go-bad)/calls"; } && RC=92
expect "install-vm.sh: a Go toolchain that does not match the sha256 of go.dev is not unpacked" 1 \
    "Checksum mismatch: the downloaded go1.26.9.linux-$ARCH.tar.gz is not the file go.dev publishes" "$OUT"

go_json "$(printf '%064d' 3)" "go1.26.9.linux-s390x.tar.gz" > "$TMP_ROOT/dl-missing.json"
OUT="$(vm vm-go-none "$P_ALL" RETICORA_BUILD_FROM_SOURCE=1 FAKE_GO_JSON="$TMP_ROOT/dl-missing.json" -- "$TOOLCHAIN")"; RC=$?
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
# with compose stubbed (logged) and .env/override below the case directory;
# T_ARGS are the installer's options, T_CHECKOUT the checkout it runs in.
cloud() {
    local name="$1" path="$2"; shift 2
    local vars=()
    while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do vars+=("$1"); shift; done
    shift
    sandbox "$name" "$path" "${vars[@]}" bash -c 'set -euo pipefail; set -- ${T_ARGS:-}; . "$0"; NON_INTERACTIVE=1
        [ -z "${T_CHECKOUT:-}" ] || SCRIPT_DIR="$T_CHECKOUT"
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

# Checkouts the whole run installs from: the release tag (git), a later commit
# of main (git), the extracted source tarball (no git) and a changed copy.
CO_TAG="$TMP_ROOT/co-tag"; CO_MAIN="$TMP_ROOT/co-main"; CO_TREE="$TMP_ROOT/co-tree"; CO_EVIL="$TMP_ROOT/co-tree-evil"
git clone -q --branch "$TAG" "file://$SRC_REPO" "$CO_TAG" 2>/dev/null
git clone -q --branch main "file://$SRC_REPO" "$CO_MAIN"
git -C "$CO_MAIN" -c user.name=t -c user.email=t@example.test -c commit.gpgsign=false commit -q --allow-empty -m "after $TAG"
mkdir -p "$CO_TREE"
tar -xzf "$RELEASES/$TAG/reticora-source-$TAG.tar.gz" -C "$CO_TREE" --strip-components=1
cp -r "$CO_TREE" "$CO_EVIL"
printf 'manipulated\n' > "$CO_EVIL/MARKER"
# After a release-mode run the source-build override is kept as .disabled.
CO_RENAMED="$TMP_ROOT/co-tree-renamed"
cp -r "$CO_TREE" "$CO_RENAMED"
mv "$CO_RENAMED/deploy/docker-compose/docker-compose.override.yml" "$CO_RENAMED/deploy/docker-compose/docker-compose.override.yml.disabled"

# The whole run: verification before the start; a failure never reaches it.
CLOUD_MAIN='preflight() { :; }; collect_config() { :; }; write_env() { :; }; ensure_session_key() { :; }
    configure_realm() { :; }; bootstrap_tls_cert() { :; }; issue_tls_certificate() { :; }; verify_stack() { :; }
    gc_images() { :; }; print_summary() { :; }; start_stack() { printf "start_stack\n" >> "$FAKE_LOG"; }
    RETICORA_PUBLIC_BASE_URL=http://h RETICORA_OIDC_ISSUER_URL=http://h/realms/reticora RETICORA_OIDC_REDIRECT_URL=http://h/cb
    RETICORA_FRONTEND_PORT=3000 RETICORA_SERVER_PORT=8080 RETICORA_KEYCLOAK_PORT=8180; main'
OUT="$(cloud cl-main "$P_ALL" T_CHECKOUT="$CO_TAG" -- "$CLOUD_MAIN")"; RC=$?
order="$(grep -E '^(cosign verify|compose pull|start_stack)' "$(case_dir cl-main)/calls" | cut -d' ' -f1-2 | uniq | tr '\n' ',')"
[ "$order" = "cosign verify-blob,cosign verify,compose pull,start_stack," ] || RC=88
expect "install-cloud.sh release mode: checkout verified < images verified < pull < start of the stack" 0 "Release mode: the images are verified with cosign" "$OUT"
OUT="$(cloud cl-main-fail "$P_ALL" T_CHECKOUT="$CO_TAG" FAKE_COSIGN_RC=1 -- "$CLOUD_MAIN")"; RC=$?
grep -qE '^(start_stack|compose)' "$(case_dir cl-main-fail)/calls" && RC=87
expect "install-cloud.sh release mode: a failed verification stops before compose up" 1 "Signature check failed" "$OUT"

OUT="$(cloud cl-src "$P_ALL" T_ARGS=--build-from-source -- "$CLOUD_MAIN")"; RC=$?
calls="$(cat "$(case_dir cl-src)/calls")"
{ grep -qx 'compose build server frontend' <<<"$calls" && grep -qx 'compose build postgres' <<<"$calls" \
    && ! grep -qE '^(cosign|curl)' <<<"$calls"; } || RC=86
expect "install-cloud.sh --build-from-source: 'not signature-verified' at the start, then server, frontend and postgres are built" 0 \
    "Local source build: the images are built from this checkout and are not signature-verified \(--build-from-source\)" "$OUT"

# The default of a real configuration (E-69, Option 1): an .env of an earlier
# source build (no registry, tag latest, a pin) installs the signed release.
LEGACY_ENV='printf "%s\n" RETICORA_DB_PASSWORD=legacy-db-password RETICORA_S3_ACCESS_KEY=reticora \
    RETICORA_S3_SECRET_KEY=legacy-s3-secret KEYCLOAK_ADMIN=admin KEYCLOAK_ADMIN_PASSWORD=legacy-kc-password \
    KEYCLOAK_DB_PASSWORD=legacy-kc-db-password RETICORA_INITIAL_ADMIN_EMAIL=ops@acme.example \
    RETICORA_OIDC_CLIENT_SECRET=legacy-client-secret RETICORA_MASTER_KEY=bGVnYWN5LW1hc3Rlci1rZXktMzItYnl0ZXMtbG9uZyE= \
    RETICORA_BACKUP_CIPHER_PASS=legacy-cipher \
    RETICORA_PUBLIC_BASE_URL=http://192.0.2.10:3000 RETICORA_OIDC_ISSUER_URL=http://192.0.2.10:8180/realms/reticora \
    RETICORA_OIDC_CLIENT_ID=reticora-app RETICORA_OIDC_REDIRECT_URL=http://192.0.2.10:3000/auth/callback RETICORA_TLS_DOMAIN= \
    RETICORA_FRONTEND_PORT=3000 RETICORA_SERVER_PORT=8080 RETICORA_KEYCLOAK_PORT=8180 RETICORA_IMAGE_REGISTRY= \
    RETICORA_IMAGE_TAG=latest RETICORA_SERVER_IMAGE=ghcr.io/datahub-chiemgau/reticora-server@sha256:'"$(printf '%064d' 9)"' > "$ENV_FILE"
    '
CLOUD_DEFAULT="$LEGACY_ENV"'preflight() { :; }; ensure_session_key() { :; }; configure_realm() { :; }
    bootstrap_tls_cert() { :; }; issue_tls_certificate() { :; }; verify_stack() { :; }; gc_images() { :; }
    print_summary() { :; }; start_stack() { printf "start_stack\n" >> "$FAKE_LOG"; }; main'
api_called() { grep -qx 'curl https://api.github.com/repos/DataHub-Chiemgau/Reticora-CMDB/releases/latest' "$(case_dir "$1")/calls"; }
release_env() { # <case> — .env names the signed release and pins its three images
    local f; f="$(case_dir "$1")/.env"
    grep -qx "RETICORA_IMAGE_REGISTRY=$IMAGE_REGISTRY" "$f" && grep -qx "RETICORA_IMAGE_TAG=$TAG" "$f" && [ "$(pinned "$1")" -eq 3 ]
}
OUT="$(cloud cl-default "$P_ALL" T_CHECKOUT="$CO_TAG" -- "$CLOUD_DEFAULT")"; RC=$?
calls="$(cat "$(case_dir cl-default)/calls")"
order="$(grep -E '^(cosign verify|compose|start_stack)' <<<"$calls" | cut -d' ' -f1-2 | uniq | tr '\n' ',')"
{ [ "$order" = "cosign verify-blob,cosign verify,compose pull,start_stack," ] && release_env cl-default \
    && [ "$(grep -cE "^cosign verify $IMAGE_REGISTRY/reticora-(server|frontend|postgres):$TAG " <<<"$calls")" -eq 3 ] \
    && [ "$(grep -cF -- ":$TAG $SIGNER_ARGS" <<<"$calls")" -eq 3 ] \
    && ! grep -q '^compose build' <<<"$calls" && ! api_called cl-default; } || RC=76
expect "install-cloud.sh default (no switch): the release of the checkout's tag — checkout and images verified, pinned, nothing built" 0 \
    "Release mode: the images are verified with cosign.*Source verified: $TAG is commit $commit" "$(printf '%s' "$OUT" | tr '\n' ' ')" "not signature-verified"

OUT="$(cloud cl-default-tree "$P_ALL" T_CHECKOUT="$CO_TREE" -- "$CLOUD_DEFAULT")"; RC=$?
{ api_called cl-default-tree && release_env cl-default-tree && grep -qx 'start_stack' "$(case_dir cl-default-tree)/calls" \
    && ! grep -q '^compose build' "$(case_dir cl-default-tree)/calls"; } || RC=75
expect "install-cloud.sh default: a checkout without git — the newest release, every file checked against its signed source tarball" 0 \
    "Newest release: $TAG.*Source verified: $CO_TREE matches reticora-source-$TAG.tar.gz of the signed release" "$(printf '%s' "$OUT" | tr '\n' ' ')"

OUT="$(cloud cl-default-renamed "$P_ALL" T_CHECKOUT="$CO_RENAMED" -- "$CLOUD_DEFAULT")"; RC=$?
release_env cl-default-renamed || RC=75
expect "install-cloud.sh default: a re-run in a checkout without git whose override is disabled still passes the source check" 0 \
    "Source verified: $CO_RENAMED matches reticora-source-$TAG.tar.gz" "$OUT"

OUT="$(cloud cl-release "$P_ALL" T_CHECKOUT="$CO_TREE" T_ARGS="--release $TAG" FAKE_LATEST=/nonexistent -- "$CLOUD_DEFAULT")"; RC=$?
{ ! api_called cl-release && release_env cl-release; } || RC=74
expect "install-cloud.sh --release $TAG: that release is installed, GitHub is not asked for the newest" 0 "Signed image verified: reticora-server@sha256:" "$OUT"

# The default never starts anything unverified; .env stays as it was.
cloud_default_refused() { # <case> <name> <pattern> [VAR=value …]
    local c="$1" name="$2" pattern="$3"; shift 3
    OUT="$(cloud "$c" "$P_ALL" "$@" -- "$CLOUD_DEFAULT")"; RC=$?
    # No compose call, no pin of a registry image (the legacy pin ends in 9).
    { grep -qE '^(compose|start_stack)' "$(case_dir "$c")/calls" \
        || grep -qE '^RETICORA_[A-Z]+_IMAGE=.*@sha256:0{63}[1-4]$' "$(case_dir "$c")/.env"; } && RC=73
    expect "$name" 1 "$pattern" "$(printf '%s' "$OUT" | tr '\n' ' ')"
}
cloud_default_refused cl-default-main "install-cloud.sh default: a checkout of main (no release tag) stops before anything is written, with the hint" \
    "--build-from-source installs this one unverified.*Source check failed: the checkout of $TAG in $CO_MAIN is commit [0-9a-f]{40}, the signed release names $commit" \
    T_CHECKOUT="$CO_MAIN"
if grep -qx 'RETICORA_IMAGE_TAG=latest' "$(case_dir cl-default-main)/.env" \
    && ! grep -q '^# Generated by install-cloud.sh' "$(case_dir cl-default-main)/.env"; then
    ok "install-cloud.sh default: the refused run left .env unchanged"
else
    fail "install-cloud.sh default: the refused run left .env unchanged" "$(head -n 3 "$(case_dir cl-default-main)/.env")"
fi
cloud_default_refused cl-default-evil "install-cloud.sh default: a changed file in a checkout without git stops" \
    "Source check failed: $CO_EVIL is not the source of release $TAG \(\./MARKER: FAILED" T_CHECKOUT="$CO_EVIL"
cloud_default_refused cl-default-fork "install-cloud.sh default: cosign verify rc 1 for an image stops before compose up" \
    "Signature check failed: $IMAGE_REGISTRY/reticora-frontend:$TAG is not signed" T_CHECKOUT="$CO_TAG" FAKE_REGISTRY="$TMP_ROOT/registry-fork"
cloud_default_refused cl-default-down "install-cloud.sh default: without a reachable newest release nothing is installed" \
    "Could not determine the newest release" T_CHECKOUT="$CO_TREE" FAKE_LATEST=/nonexistent
OUT="$(cloud cl-release-bfs "$P_ALL" T_ARGS="--release $TAG --build-from-source" -- "$CLOUD_DEFAULT")"; RC=$?
expect "install-cloud.sh: --release and --build-from-source together are refused" 1 "use one of them" "$OUT"

OUT="$(cloud cl-default-bfs "$P_ALL" T_CHECKOUT="$CO_MAIN" T_ARGS=--build-from-source -- "$CLOUD_DEFAULT")"; RC=$?
calls="$(cat "$(case_dir cl-default-bfs)/calls")"
env_file="$(case_dir cl-default-bfs)/.env"
{ grep -qx 'compose build server frontend' <<<"$calls" && ! grep -qE '^(cosign|curl)' <<<"$calls" \
    && grep -qx 'RETICORA_IMAGE_REGISTRY=' "$env_file" && [ "$(grep -c '^RETICORA_SERVER_IMAGE=$' "$env_file")" -eq 1 ]; } || RC=72
expect "install-cloud.sh --build-from-source: any checkout is built, with the warning, without pins" 0 "not signature-verified" "$OUT"

# A pin of an earlier release-mode run is never reused: collect_config clears
# it, write_env writes the keys empty for the source build.
LEGACY="$LEGACY_ENV"'collect_config >/dev/null; write_env'
OUT="$(cloud cl-stale "$P_ALL" RETICORA_BUILD_FROM_SOURCE=1 RETICORA_FRONTEND_IMAGE=evil/frontend:latest -- "$LEGACY")"; RC=$?
env_file="$(case_dir cl-stale)/.env"
{ [ "$(grep -c '^RETICORA_SERVER_IMAGE=$' "$env_file")" -eq 1 ] && grep -qx 'RETICORA_FRONTEND_IMAGE=' "$env_file" \
    && grep -qx 'RETICORA_POSTGRES_IMAGE=' "$env_file" && ! grep -q 'sha256:\|evil' "$env_file"; } || RC=85
expect "install-cloud.sh: write_env drops image pins of an earlier run and from the environment (source build)" 0 "" "$OUT"

# ─── The hint of every message leads to the troubleshooting section ─────────
doc="$(sed -n '/^## 9\. Signature verification$/,/^## /p' docs/troubleshooting-installation.md)"
missing=""
for m in "Signature check failed" "Checksum mismatch" "Source check failed" "cosign is required" \
    "does not match its published sha256 checksum" "Could not download" "Source not signature-verified (no release tag)" \
    "not signature-verified" "RETICORA_IMAGE_REGISTRY" "--release" "SOURCE_COMMIT" "--build-from-source" \
    "RETICORA_BUILD_FROM_SOURCE=1" "is not installed by default" "Could not determine the newest release" \
    "is not the source of release" "use one of them" "No release selected"; do
    grep -qF -- "$m" <<<"$doc" || missing="$missing '$m'"
done
if grep -qF "SIGNATURE_TROUBLESHOOTING=\"see docs/troubleshooting-installation.md, 'Signature verification'\"" install-preflight.sh \
    && [ -z "$missing" ]; then
    ok "docs/troubleshooting-installation.md 'Signature verification' describes the modes and every error message"
else
    fail "docs/troubleshooting-installation.md 'Signature verification' describes the modes and every error message" "missing:$missing"
fi

# ─── Workflows: the unpublished state only with --build-from-source ──────────
# Every workflow run of an installer builds the checked-out state, which is
# not published (PR, main, nightly, the outage suite before a release) — except
# the job release-mode: on a tag v* it installs the default path against the
# artifacts release.yml publishes and checks the stack's health.
if python3 - .github/workflows >"$TMP_ROOT/workflows.out" 2>&1 <<'PY'
import glob, os, re, sys, yaml
problems, runs, release_jobs = [], 0, []
installer = re.compile(r"(^|[\s/])install(-cloud|-vm)?\.sh\b(?! *\|)")
for path in sorted(glob.glob(os.path.join(sys.argv[1], "*.yml"))):
    wf = yaml.safe_load(open(path))
    for name, job in (wf.get("jobs") or {}).items():
        steps = job.get("steps") or []
        if name == "release-mode":
            release_jobs.append((path, job))
        for step in steps:
            for line in str(step.get("run", "")).splitlines():
                if not installer.search(line) or line.lstrip().startswith(("#", "chmod ")) or "tests/" in line:
                    continue
                runs += 1
                if name == "release-mode":
                    if "--build-from-source" in line or "BUILD_FROM_SOURCE" in line:
                        problems.append("%s %s: the release mode uses the switch: %s" % (path, name, line.strip()))
                elif "--build-from-source" not in line:
                    problems.append("%s %s: installs without --build-from-source: %s" % (path, name, line.strip()))
if runs < 6:
    problems.append("only %d installer runs found" % runs)
if len(release_jobs) != 1 or not release_jobs[0][0].endswith("install-smoke.yml"):
    problems.append("install-smoke.yml has no job release-mode")
else:
    job = release_jobs[0][1]
    text = "\n".join(str(s.get("run", "")) for s in job.get("steps") or [])
    if "refs/tags/v" not in str(job.get("if", "")):
        problems.append("release-mode does not run only on tags v*")
    if "./tests/install-smoke-health.sh" not in text or "--ref \"$GITHUB_REF_NAME\"" not in text:
        problems.append("release-mode does not install the tag and run tests/install-smoke-health.sh")
print("\n".join(problems) or "%d installer runs" % runs)
sys.exit(1 if problems else 0)
PY
then
    ok "workflows: every installer run of the unpublished state uses --build-from-source; release-mode installs the tag by default (tags v*)"
else
    fail "workflows: every installer run of the unpublished state uses --build-from-source; release-mode installs the tag by default (tags v*)" \
        "$(cat "$TMP_ROOT/workflows.out")"
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
