#!/usr/bin/env bash
#
# install.sh — Reticora CMDB installer entry point.
#
# Asks which component should be installed and delegates to the matching
# interactive installer:
#   1) Central cloud          → install-cloud.sh
#   2) VM in customer network → install-vm.sh
#
# The script is self-bootstrapping: when it is executed on its own (for
# example piped from curl) and the rest of the sources are missing, it
# installs the tools it needs (git/curl/tar), downloads the repository and
# re-executes the matching installer from the downloaded checkout. The
# component installers then take care of the remaining dependencies,
# configuration and startup. A release tag (--ref v<version>) is checked
# against the signed release manifest before it is used (TEC-15); any other
# ref is used unverified, with a warning (transition rule of E-69).
#
# Both installers are "foolproof": they prompt for all important information,
# validate the input and can be re-run safely (idempotent).
#
# Usage:
#   ./install.sh                asks which component to install
#   ./install.sh --cloud        central cloud (add --non-interactive for CI)
#   ./install.sh --vm           collector VM   (add --docker for Docker mode)
#
#   curl -fsSL https://raw.githubusercontent.com/DataHub-Chiemgau/Reticora-CMDB/main/install.sh | bash -s -- --cloud
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || pwd)"

REPO_URL="${RETICORA_REPO_URL:-https://github.com/DataHub-Chiemgau/Reticora-CMDB.git}"
REPO_REF="${RETICORA_REPO_REF:-main}"
if [ "$(id -u)" -eq 0 ]; then
    SOURCE_DIR="${RETICORA_SOURCE_DIR:-/opt/reticora-cmdb}"
else
    SOURCE_DIR="${RETICORA_SOURCE_DIR:-$HOME/reticora-cmdb}"
fi

print_usage() {
    cat <<'EOF'
install.sh — Reticora CMDB installer entry point.

Asks which component should be installed and delegates to the matching
interactive installer:
  1) Central cloud          → install-cloud.sh
  2) VM in customer network → install-vm.sh

When the sources are missing (e.g. the script was piped from curl), it
downloads the repository first and installs the tools required to do so.

Both installers are "foolproof": they prompt for all important information,
validate the input and can be re-run safely (idempotent).

Usage:
  ./install.sh                asks which component to install
  ./install.sh --cloud        central cloud (add --non-interactive for CI)
  ./install.sh --vm           collector VM   (add --docker for Docker mode)

Options:
  --repo <url>        Git repository to download the sources from
                      (default: $RETICORA_REPO_URL or the public repository)
  --ref <ref>         branch/tag to check out (default: main). A release tag
                      v<version> is checked against the signed release
                      (cosign, SHA256SUMS) before anything of it runs; any
                      other ref is used unverified, with a warning
  --source-dir <dir>  where the sources are downloaded to
                      (default: /opt/reticora-cmdb as root, ~/reticora-cmdb otherwise)
EOF
}

MODE=""
declare -a PASSTHROUGH=()
while [ "$#" -gt 0 ]; do
    case "$1" in
        --cloud) MODE="cloud" ;;
        --vm|--collector) MODE="vm" ;;
        --repo) [ "$#" -ge 2 ] || { echo "--repo requires a value" >&2; exit 2; }; REPO_URL="$2"; shift ;;
        --repo=*) REPO_URL="${1#*=}" ;;
        --ref) [ "$#" -ge 2 ] || { echo "--ref requires a value" >&2; exit 2; }; REPO_REF="$2"; shift ;;
        --ref=*) REPO_REF="${1#*=}" ;;
        --source-dir) [ "$#" -ge 2 ] || { echo "--source-dir requires a value" >&2; exit 2; }; SOURCE_DIR="$2"; shift ;;
        --source-dir=*) SOURCE_DIR="${1#*=}" ;;
        -h|--help)
            print_usage
            exit 0
            ;;
        *) PASSTHROUGH+=("$1") ;;
    esac
    shift
done

# ─── Output helpers ───────────────────────────────────────────────────────────
if [ -t 1 ]; then
    C_BLUE=$'\033[0;34m'; C_GREEN=$'\033[0;32m'; C_YELLOW=$'\033[1;33m'
    C_RED=$'\033[0;31m'; C_RESET=$'\033[0m'
else
    C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_RESET=''
fi

info()    { printf '%s==>%s %s\n' "$C_BLUE" "$C_RESET" "$*"; }
success() { printf '%s✔%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn()    { printf '%s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
die()     { printf '%s✘ Error:%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; exit 1; }

have_cmd() { command -v "$1" >/dev/null 2>&1; }

# When the script is piped into bash, stdin is the script itself; prompt on the
# controlling terminal instead so the mode question still works.
if [ ! -t 0 ] && [ -c /dev/tty ] && (exec 0</dev/tty) 2>/dev/null; then
    exec 0</dev/tty
fi

# ─── Dependency installation ──────────────────────────────────────────────────
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    if have_cmd sudo; then SUDO="sudo"; fi
fi

# Only the package manager of the supported platforms (Ubuntu, Debian; CH22)
# is used; other systems install the prerequisites themselves.
pkg_manager() {
    if have_cmd apt-get; then printf 'apt-get'; return 0; fi
    return 1
}

# install_packages <package> [...] — installs distro packages, best effort.
install_packages() {
    local mgr
    mgr="$(pkg_manager)" || return 1
    info "Installing missing packages ($*) with $mgr …"
    case "$mgr" in
        apt-get)
            $SUDO apt-get update -qq || true
            DEBIAN_FRONTEND=noninteractive $SUDO apt-get install -y "$@"
            ;;
    esac
}

# ensure_command <command> [package …] — makes sure <command> exists.
ensure_command() {
    local cmd="$1"; shift
    have_cmd "$cmd" && return 0
    local pkgs=("$@")
    [ "${#pkgs[@]}" -gt 0 ] || pkgs=("$cmd")
    install_packages "${pkgs[@]}" || true
    have_cmd "$cmd" || die "'$cmd' is required but could not be installed automatically. Please install it and re-run."
}

# ─── Platform check (CH22, TEC-09, INS-07) ────────────────────────────────────
# Supported platforms: Ubuntu 22.04/24.04 LTS and Debian 12; other systems are
# refused with a support message. RETICORA_ALLOW_UNSUPPORTED_PLATFORM=1
# continues with a warning. The component installers repeat the check
# (install-preflight.sh); this copy runs before the sources are downloaded.
SUPPORTED_PLATFORMS="ubuntu:22.04 ubuntu:24.04 debian:12"

detect_platform() {
    local os_release="${RETICORA_OS_RELEASE:-/etc/os-release}"
    [ -r "$os_release" ] || { printf 'unknown'; return; }
    # shellcheck disable=SC1090
    ( . "$os_release" && printf '%s:%s' "${ID:-unknown}" "${VERSION_ID:-unknown}" )
}

check_platform() {
    local platform
    platform="$(detect_platform)"
    case " $SUPPORTED_PLATFORMS " in
        *" $platform "*)
            info "Platform $platform is supported."
            return 0
            ;;
    esac
    if [ "${RETICORA_ALLOW_UNSUPPORTED_PLATFORM:-0}" = "1" ]; then
        warn "Platform $platform is not supported (supported: Ubuntu 22.04/24.04 LTS, Debian 12); continuing because RETICORA_ALLOW_UNSUPPORTED_PLATFORM=1 — this installation is not supported."
        return 0
    fi
    die "Platform $platform is not supported. Reticora supports Ubuntu 22.04 LTS, Ubuntu 24.04 LTS and Debian 12 (see docs/troubleshooting-installation.md). Set RETICORA_ALLOW_UNSUPPORTED_PLATFORM=1 to continue without support."
}

# ─── Signature verification (TEC-15) ─────────────────────────────────────────
# BEGIN shared signature block (TEC-15, E-31) — install.sh carries an
# identical copy, because it runs before the sources are downloaded
# (tests/install-signature.test.sh compares both copies).
#
# Signature verification before the installation: .github/workflows/release.yml
# signs keyless with cosign (Fulcio certificate of release.yml of this
# repository on a version tag, GitHub Actions OIDC issuer), the identity the
# updater (edgecore/update CosignVerifier) pins as well. The signed manifest
# SHA256SUMS of a release lists its files, among them SOURCE_COMMIT and the
# source tarball reticora-source-<tag>.tar.gz. Every check is fail-closed.
RELEASE_REPOSITORY="DataHub-Chiemgau/Reticora-CMDB"
RELEASE_DOWNLOAD_URL="https://github.com/$RELEASE_REPOSITORY/releases/download"
RELEASE_OIDC_ISSUER="https://token.actions.githubusercontent.com"
# cosign used when none is installed: the release release.yml signs with,
# checked against the sha256 of its cosign_checksums.txt.
COSIGN_VERSION="v2.4.1"
COSIGN_SHA256_AMD64="8b24b946dd5809c6bd93de08033bcf6bc0ed7d336b7785787c080f574b89249b"
COSIGN_SHA256_ARM64="3b2e2e3854d0356c45fe6607047526ccd04742d20bd44afb5be91fa2a6e7cb4a"
COSIGN_BIN=""
SIGNATURE_TROUBLESHOOTING="see docs/troubleshooting-installation.md, 'Signature verification'"

# is_release_tag <ref> — a version tag (v*) the release workflow publishes.
is_release_tag() {
    [[ "$1" =~ ^v[0-9][0-9A-Za-z.+-]*$ ]]
}

# release_identity_regexp [tag] — the signer identity: release.yml on a
# version tag; with a version tag on exactly that one, so the signed files of
# another release do not pass as this release.
release_identity_regexp() {
    local ref='v.+'
    if [ -n "${1:-}" ] && is_release_tag "$1"; then
        ref="$(printf '%s' "$1" | sed 's/[.+]/\\&/g')"
    fi
    printf '^https://github\\.com/%s/\\.github/workflows/release\\.yml@refs/tags/%s$' \
        "$(printf '%s' "$RELEASE_REPOSITORY" | sed 's/[.]/\\&/g')" "$ref"
}

# ensure_cosign — sets COSIGN_BIN: the installed cosign, otherwise the pinned
# release, downloaded once to RETICORA_TOOLS_DIR and checked against its
# sha256 before every use. Without cosign nothing is installed. As root the
# cache is /var/cache/reticora, never a directory of the invoking user.
ensure_cosign() {
    [ -z "$COSIGN_BIN" ] || return 0
    if have_cmd cosign; then
        COSIGN_BIN="$(command -v cosign)"
        return 0
    fi
    local arch sha dir bin
    case "$(uname -m)" in
        x86_64|amd64) arch="amd64"; sha="$COSIGN_SHA256_AMD64" ;;
        aarch64|arm64) arch="arm64"; sha="$COSIGN_SHA256_ARM64" ;;
        *) die "cosign is required to verify the signed release artifacts and is not installed; there is no cosign $COSIGN_VERSION for $(uname -m). Install cosign and re-run ($SIGNATURE_TROUBLESHOOTING)." ;;
    esac
    dir="${RETICORA_TOOLS_DIR:-}"
    if [ -z "$dir" ] && [ "$(id -u)" -eq 0 ]; then
        dir="/var/cache/reticora"
    elif [ -z "$dir" ]; then
        dir="${XDG_CACHE_HOME:-${HOME:-}/.cache}/reticora"
    fi
    bin="$dir/cosign-$COSIGN_VERSION-linux-$arch"
    if ! printf '%s  %s\n' "$sha" "$bin" | sha256sum -c - >/dev/null 2>&1; then
        info "Downloading cosign $COSIGN_VERSION to verify the release signatures …"
        ensure_command curl curl ca-certificates
        mkdir -p "$dir"
        rm -f "$bin"
        if ! curl -fsSL "https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION/cosign-linux-$arch" -o "$bin.part"; then
            rm -f "$bin.part"
            die "cosign is required to verify the signed release artifacts; it is not installed and could not be downloaded. Install cosign and re-run ($SIGNATURE_TROUBLESHOOTING)."
        fi
        if ! printf '%s  %s\n' "$sha" "$bin.part" | sha256sum -c - >/dev/null 2>&1; then
            rm -f "$bin.part"
            die "The downloaded cosign $COSIGN_VERSION does not match its published sha256 checksum; nothing was installed ($SIGNATURE_TROUBLESHOOTING)."
        fi
        mv "$bin.part" "$bin"
    fi
    chmod 755 "$bin"
    COSIGN_BIN="$bin"
}

# cosign_verify_blob <file> <bundle> <tag> — the keyless signature in
# <bundle> is the release workflow's on <tag> for exactly <file>.
cosign_verify_blob() {
    local out
    ensure_cosign
    if ! out="$("$COSIGN_BIN" verify-blob --bundle "$2" \
        --certificate-identity-regexp "$(release_identity_regexp "$3")" \
        --certificate-oidc-issuer "$RELEASE_OIDC_ISSUER" "$1" 2>&1)"; then
        printf '%s\n' "$out" | tail -n 3 >&2
        die "Signature check failed: $(basename "$1") is not signed by the release workflow of $RELEASE_REPOSITORY for $3; nothing was installed ($SIGNATURE_TROUBLESHOOTING)."
    fi
}

# fetch_release_file <tag> <name> <dir> — downloads <name> of release <tag>.
fetch_release_file() {
    curl -fsSL "$RELEASE_DOWNLOAD_URL/$1/$2" -o "$3/$2" \
        || die "Could not download $2 of release $1 from $RELEASE_DOWNLOAD_URL/$1/; nothing was installed ($SIGNATURE_TROUBLESHOOTING)."
}

# fetch_release_manifest <tag> <dir> — SHA256SUMS of release <tag> with its
# verified signature.
fetch_release_manifest() {
    ensure_command curl curl ca-certificates
    fetch_release_file "$1" SHA256SUMS "$2"
    fetch_release_file "$1" SHA256SUMS.sigstore.json "$2"
    cosign_verify_blob "$2/SHA256SUMS" "$2/SHA256SUMS.sigstore.json" "$1"
}

# check_release_file <dir> <name> — <dir>/<name> has the sha256 that the
# verified manifest <dir>/SHA256SUMS lists for <name>.
check_release_file() {
    local want have
    want="$(awk -v n="$2" '$2 == n || $2 == "*" n { print $1 }' "$1/SHA256SUMS")" || want=""
    if [ -z "$want" ] || [ "$(printf '%s\n' "$want" | wc -l)" -ne 1 ]; then
        die "The signed manifest SHA256SUMS lists no single checksum for $2; nothing was installed ($SIGNATURE_TROUBLESHOOTING)."
    fi
    have="$(sha256sum "$1/$2" 2>/dev/null | cut -d' ' -f1)" || have=""
    [ "$have" = "$want" ] \
        || die "Checksum mismatch: $2 (sha256 ${have:-<none>}) is not the file of the signed manifest SHA256SUMS (sha256 $want); nothing was installed ($SIGNATURE_TROUBLESHOOTING)."
}

# verify_source_checkout <dir> <tag> — the git checkout in <dir> is the
# commit that the signed manifest of release <tag> names (SOURCE_COMMIT).
verify_source_checkout() {
    local tmp want have
    tmp="$(mktemp -d)"
    fetch_release_manifest "$2" "$tmp"
    fetch_release_file "$2" SOURCE_COMMIT "$tmp"
    check_release_file "$tmp" SOURCE_COMMIT
    want="$(tr -d '[:space:]' < "$tmp/SOURCE_COMMIT")"
    rm -rf "$tmp"
    [[ "$want" =~ ^[0-9a-f]{40}([0-9a-f]{24})?$ ]] \
        || die "SOURCE_COMMIT of release $2 is not a commit id; nothing was built ($SIGNATURE_TROUBLESHOOTING)."
    have="$(git -C "$1" rev-parse HEAD 2>/dev/null)" || have=""
    [ "$have" = "$want" ] \
        || die "Source check failed: the checkout of $2 in $1 is commit ${have:-<none>}, the signed release names $want; nothing was built ($SIGNATURE_TROUBLESHOOTING)."
    success "Source verified: $2 is commit $want of the signed release"
}

# fetch_source_tarball <tag> <file> — the source tarball of release <tag>,
# checked against the signed manifest before it is unpacked.
fetch_source_tarball() {
    local tmp name="reticora-source-$1.tar.gz"
    tmp="$(mktemp -d)"
    fetch_release_manifest "$1" "$tmp"
    fetch_release_file "$1" "$name" "$tmp"
    check_release_file "$tmp" "$name"
    mv "$tmp/$name" "$2"
    rm -rf "$tmp"
    success "Source verified: $name matches the signed release manifest"
}

# warn_unverified_source <ref> — transition rule of E-69: a ref that is no
# release tag is still installed, with this warning.
warn_unverified_source() {
    warn "Source not signature-verified (no release tag): '$1' is used as downloaded. RETICORA_REPO_REF=v<version> (install.sh --ref v<version>) checks the source against the signed release ($SIGNATURE_TROUBLESHOOTING)."
}
# END shared signature block

# ─── Source bootstrap ─────────────────────────────────────────────────────────
have_sources() {
    [ -f "$1/install-cloud.sh" ] && [ -f "$1/install-vm.sh" ] && [ -f "$1/install-preflight.sh" ]
}

download_sources() {
    info "Downloading the Reticora sources from $REPO_URL ($REPO_REF) …"
    ensure_command curl curl ca-certificates
    # A release tag is checked against its signed manifest before anything of
    # it runs; any other ref is used unverified (transition rule of E-69).
    is_release_tag "$REPO_REF" || warn_unverified_source "$REPO_REF"

    have_cmd git || install_packages git || true

    if have_cmd git; then
        if [ -d "$SOURCE_DIR/.git" ]; then
            info "Updating the existing checkout in $SOURCE_DIR …"
            git -C "$SOURCE_DIR" fetch --depth 1 origin "$REPO_REF" \
                || die "Could not fetch '$REPO_REF' from $REPO_URL."
            git -C "$SOURCE_DIR" checkout -q FETCH_HEAD \
                || die "Could not check out '$REPO_REF'."
        else
            mkdir -p "$(dirname "$SOURCE_DIR")"
            git clone --depth 1 --branch "$REPO_REF" "$REPO_URL" "$SOURCE_DIR" \
                || die "Could not clone $REPO_URL into $SOURCE_DIR."
        fi
        if is_release_tag "$REPO_REF"; then
            verify_source_checkout "$SOURCE_DIR" "$REPO_REF"
        fi
    else
        # No git available: fall back to a tarball — for a release tag the
        # source tarball of the release, checked before it is unpacked.
        ensure_command tar
        local base tarball tmp
        base="${REPO_URL%.git}"
        tmp="$(mktemp -d)"
        if is_release_tag "$REPO_REF"; then
            fetch_source_tarball "$REPO_REF" "$tmp/reticora.tar.gz"
        else
            case "$base" in
                https://github.com/*) tarball="$base/archive/refs/heads/$REPO_REF.tar.gz" ;;
                *) die "git is required to download sources from $REPO_URL." ;;
            esac
            curl -fsSL "$tarball" -o "$tmp/reticora.tar.gz" \
                || die "Could not download $tarball."
        fi
        mkdir -p "$SOURCE_DIR"
        tar -xzf "$tmp/reticora.tar.gz" -C "$SOURCE_DIR" --strip-components=1 \
            || die "Could not unpack the downloaded archive."
        rm -rf "$tmp"
    fi

    have_sources "$SOURCE_DIR" || die "The downloaded sources in $SOURCE_DIR are incomplete."
    chmod +x "$SOURCE_DIR"/install*.sh 2>/dev/null || true
    success "Sources are available in $SOURCE_DIR"
}

resolve_sources() {
    if have_sources "$SCRIPT_DIR"; then
        SOURCE_DIR="$SCRIPT_DIR"
        return
    fi
    if have_sources "$SOURCE_DIR"; then
        info "Using the existing checkout in $SOURCE_DIR (refreshing) …"
    fi
    download_sources
}

if [ -z "$MODE" ]; then
    if [ -t 0 ]; then
        echo "Reticora CMDB — installation"
        echo
        echo "  1) Central cloud (server, frontend, database, identity)"
        echo "  2) VM in the customer network (discovery collector)"
        echo
        choice=""
        while true; do
            read -r -p "Which component should be installed? [1/2]: " choice || true
            case "$choice" in
                1) MODE="cloud"; break ;;
                2) MODE="vm"; break ;;
                *) echo "Please enter 1 or 2." ;;
            esac
        done
    else
        echo "Non-interactive invocation requires --cloud or --vm." >&2
        exit 2
    fi
fi

check_platform
resolve_sources

case "$MODE" in
    cloud) exec "$SOURCE_DIR/install-cloud.sh" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"} ;;
    vm)    exec "$SOURCE_DIR/install-vm.sh" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"} ;;
esac
