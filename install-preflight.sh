#!/usr/bin/env bash
#
# install-preflight.sh — prerequisite checks shared by install-cloud.sh and
# install-vm.sh (INS-07, TEC-09, CH22). Sourcing it defines functions only.
#
#   check_platform           Ubuntu 22.04/24.04 LTS or Debian 12, else abort
#   check_docker_engine      Docker Engine >= 24
#   check_compose_v2         Docker Compose v2 (plugin or v2 binary)
#   check_sizing <profile>   warns below the minimum sizing (docs/ops/sizing.md)
#   cosign_verify_blob, verify_release_image, verify_source_checkout, …
#                            signature verification before the installation
#                            (TEC-15, WP-387; default per E-69, WP-388;
#                            shared signature block below)
#
# The functions use info/success/warn/die, have_cmd and ensure_command of the
# sourcing installer.

# Supported platforms (CH22). RETICORA_ALLOW_UNSUPPORTED_PLATFORM=1 continues
# on other systems with a warning and without support.
SUPPORTED_PLATFORMS="ubuntu:22.04 ubuntu:24.04 debian:12"
MIN_DOCKER_MAJOR=24
MIN_COMPOSE_MAJOR=2

# Minimum sizing per profile: vCPU, RAM in GiB, free disk in GiB
# (docs/ops/sizing.md). "cloud" is the central stack for up to 10,000
# objects (INS-07), "collector" the discovery VM.
sizing_minimum() {
    case "$1" in
        cloud) printf '4 8 100' ;;
        collector) printf '2 2 20' ;;
        *) return 1 ;;
    esac
}

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

# major_version <version> prints the leading number of a version string
# ("24.0.7" → 24, "v2.29.1" → 2, "27.3.1-1~ubuntu" → 27).
major_version() {
    local v="${1#v}"
    v="${v%%[!0-9]*}"
    printf '%s' "${v:-0}"
}

# check_docker_engine [docker command] — Docker Engine (server) >= 24.
check_docker_engine() {
    local docker="${1:-docker}" version
    version="$($docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
    [ -n "$version" ] || die "Cannot determine the Docker Engine version ('$docker version' failed). Is the daemon running?"
    if [ "$(major_version "$version")" -lt "$MIN_DOCKER_MAJOR" ]; then
        die "Docker Engine $version is too old; Reticora requires Docker Engine >= $MIN_DOCKER_MAJOR. Upgrade Docker (https://docs.docker.com/engine/install/) and re-run."
    fi
    info "Docker Engine $version"
}

# compose_version <compose command …> prints the Compose version or nothing.
compose_version() {
    "$@" version --short 2>/dev/null || true
}

# check_compose_v2 <compose command …> — Compose v2; the Python v1
# docker-compose is rejected.
check_compose_v2() {
    local version
    version="$(compose_version "$@")"
    [ -n "$version" ] || die "Cannot determine the Docker Compose version ('$* version' failed)."
    if [ "$(major_version "$version")" -lt "$MIN_COMPOSE_MAJOR" ]; then
        die "Docker Compose $version is not supported; Reticora requires Compose v2 (the docker compose plugin). Install docker-compose-plugin and re-run."
    fi
    info "Docker Compose $version"
}

# host_cpus, host_memory_gib and host_disk_gib <dir> read the host's
# resources; RETICORA_HOST_CPUS/_MEMORY_GIB/_DISK_GIB override them (tests).
host_cpus() {
    if [ -n "${RETICORA_HOST_CPUS:-}" ]; then printf '%s' "$RETICORA_HOST_CPUS"; return; fi
    nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null || printf '0'
}

host_memory_gib() {
    if [ -n "${RETICORA_HOST_MEMORY_GIB:-}" ]; then printf '%s' "$RETICORA_HOST_MEMORY_GIB"; return; fi
    awk '/^MemTotal:/ { printf "%d", ($2 / 1048576) + 0.5 }' /proc/meminfo 2>/dev/null || printf '0'
}

host_disk_gib() {
    if [ -n "${RETICORA_HOST_DISK_GIB:-}" ]; then printf '%s' "$RETICORA_HOST_DISK_GIB"; return; fi
    local dir="$1"
    while [ ! -d "$dir" ] && [ "$dir" != "/" ]; do dir="$(dirname "$dir")"; done
    df -Pk "$dir" 2>/dev/null | awk 'NR == 2 { printf "%d", $4 / 1048576 }' || printf '0'
}

# check_sizing <profile> [data dir] warns for every resource below the
# minimum of the profile; it never aborts (INS-07).
check_sizing() {
    local profile="$1" dir="${2:-/var/lib/docker}" min cpus mem disk below=0 min_cpus min_mem min_disk
    min="$(sizing_minimum "$profile")" || return 0
    read -r min_cpus min_mem min_disk <<<"$min"
    cpus="$(host_cpus)"; mem="$(host_memory_gib)"; disk="$(host_disk_gib "$dir")"
    if [ "$cpus" -lt "$min_cpus" ]; then
        warn "Only $cpus vCPU available; the $profile minimum is $min_cpus vCPU (docs/ops/sizing.md)."
        below=1
    fi
    if [ "$mem" -lt "$min_mem" ]; then
        warn "Only $mem GiB RAM available; the $profile minimum is $min_mem GiB (docs/ops/sizing.md)."
        below=1
    fi
    if [ "$disk" -lt "$min_disk" ]; then
        warn "Only $disk GiB free disk at $dir; the $profile minimum is $min_disk GiB SSD (docs/ops/sizing.md)."
        below=1
    fi
    if [ "$below" -eq 0 ]; then
        info "Sizing: $cpus vCPU, $mem GiB RAM, $disk GiB free — meets the $profile minimum."
    fi
    return 0
}

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
#
# Default (E-69, Option 1): the signed release artifacts of the named or the
# newest release are installed. Only --build-from-source (BUILD_FROM_SOURCE=1,
# set by the installer from the option or RETICORA_BUILD_FROM_SOURCE=1) builds
# locally — not signature-verified — and accepts a ref that is no release tag.
RELEASE_REPOSITORY="DataHub-Chiemgau/Reticora-CMDB"
RELEASE_DOWNLOAD_URL="https://github.com/$RELEASE_REPOSITORY/releases/download"
# The newest release: GitHub's latest release (no pre-release, no draft).
RELEASE_LATEST_URL="https://api.github.com/repos/$RELEASE_REPOSITORY/releases/latest"
# The registry release.yml pushes the signed images to.
# shellcheck disable=SC2034 # used by install-cloud.sh and install-vm.sh
RELEASE_REGISTRY="ghcr.io/datahub-chiemgau"
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

# latest_release_tag <variable> — sets <variable> to the tag of the newest
# release. Only the tag is taken from GitHub; everything installed for it is
# verified for exactly that tag.
latest_release_tag() {
    local latest release_url tag_prefix
    ensure_command curl curl ca-certificates
    latest="$(curl -fsSL --connect-timeout 10 --max-time 30 -H 'Accept: application/vnd.github+json' "$RELEASE_LATEST_URL" 2>/dev/null \
        | tr -d '\r\n' | sed -nE 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p')" || latest=""
    if ! is_release_tag "$latest"; then
        # The public redirect also works when the API is unavailable or rate limited.
        release_url="$(curl -fsSL --connect-timeout 10 --max-time 30 -o /dev/null \
            -w '%{url_effective}' "https://github.com/$RELEASE_REPOSITORY/releases/latest" 2>/dev/null)" || release_url=""
        tag_prefix="https://github.com/$RELEASE_REPOSITORY/releases/tag/"
        case "$release_url" in
            "$tag_prefix"*) latest="${release_url#"$tag_prefix"}" ;;
            "https://github.com/$RELEASE_REPOSITORY/releases")
                die "No published stable release is available for $RELEASE_REPOSITORY. To install main from source, run ./install.sh --cloud --build-from-source (collector: ./install.sh --vm --build-from-source). Source builds are not signature-verified ($SIGNATURE_TROUBLESHOOTING)." ;;
        esac
    fi
    is_release_tag "$latest" \
        || die "Could not determine the newest release of $RELEASE_REPOSITORY ($RELEASE_LATEST_URL); name the release with --release v<version> (install.sh: --ref v<version>) or build from source with --build-from-source ($SIGNATURE_TROUBLESHOOTING)."
    printf -v "$1" '%s' "$latest"
    info "Newest release: $latest"
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

# verify_release_image <registry> <image> <tag> <variable> — 'cosign verify'
# of <registry>/<image>:<tag> against the identity of the release workflow
# (with a version tag: of exactly that release). The signature must name
# <image> (no other signed Reticora image under its name); <variable> is set
# to <registry>/<image>@<digest> of the verified image. Needs jq.
verify_release_image() {
    local image="$2" ref out err digest
    ref="$1/$image:$3"
    ensure_cosign
    err="$(mktemp)"
    if ! out="$("$COSIGN_BIN" verify "$ref" \
        --certificate-identity-regexp "$(release_identity_regexp "$3")" \
        --certificate-oidc-issuer "$RELEASE_OIDC_ISSUER" 2>"$err")"; then
        tail -n 3 "$err" >&2
        rm -f "$err"
        die "Signature check failed: $ref is not signed by the release workflow of $RELEASE_REPOSITORY; nothing was started ($SIGNATURE_TROUBLESHOOTING)."
    fi
    rm -f "$err"
    digest="$(jq -r --arg image "$image" '[.[] | select((.critical.identity["docker-reference"] // "") | endswith("/" + $image))
        | .critical.image["docker-manifest-digest"]] | unique | if length == 1 then .[0] else empty end' <<<"$out" 2>/dev/null)" || digest=""
    [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] \
        || die "Signature check failed: the verified signatures of $ref do not name $image with one digest; nothing was started ($SIGNATURE_TROUBLESHOOTING)."
    printf -v "$4" '%s@%s' "$1/$image" "$digest"
    success "Signed image verified: $image@$digest"
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

# check_source_ref <ref> — E-69 (Option 1): by default only the source of a
# release tag is installed, checked against the signed release. Another ref
# (a branch, a commit) needs --build-from-source and is then used unverified,
# with a warning.
check_source_ref() {
    is_release_tag "$1" && return 0
    if [ "${BUILD_FROM_SOURCE:-0}" = 1 ]; then
        warn "Source not signature-verified (no release tag): '$1' is used as downloaded (--build-from-source). A release tag (install.sh --ref v<version>, RETICORA_REPO_REF) is checked against the signed release ($SIGNATURE_TROUBLESHOOTING)."
        return 0
    fi
    die "Source not signature-verified (no release tag): '$1' is not installed by default. Install a signed release (--ref v<version>; without a ref the newest release), or add --build-from-source to build '$1' locally, not signature-verified ($SIGNATURE_TROUBLESHOOTING)."
}
# END shared signature block
