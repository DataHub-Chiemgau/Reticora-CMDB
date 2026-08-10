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
# configuration and startup.
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
  --ref <ref>         branch/tag to check out (default: main)
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

pkg_manager() {
    local mgr
    for mgr in apt-get dnf yum zypper pacman apk; do
        if have_cmd "$mgr"; then printf '%s' "$mgr"; return 0; fi
    done
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
        dnf|yum)  $SUDO "$mgr" install -y "$@" ;;
        zypper)   $SUDO zypper --non-interactive install -y "$@" ;;
        pacman)   $SUDO pacman -Sy --noconfirm "$@" ;;
        apk)      $SUDO apk add --no-cache "$@" ;;
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

# ─── Source bootstrap ─────────────────────────────────────────────────────────
have_sources() {
    [ -f "$1/install-cloud.sh" ] && [ -f "$1/install-vm.sh" ]
}

download_sources() {
    info "Downloading the Reticora sources from $REPO_URL ($REPO_REF) …"
    ensure_command curl curl ca-certificates

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
    else
        # No git available: fall back to the codeload tarball.
        ensure_command tar
        local base tarball tmp
        base="${REPO_URL%.git}"
        case "$base" in
            https://github.com/*) tarball="$base/archive/refs/heads/$REPO_REF.tar.gz" ;;
            *) die "git is required to download sources from $REPO_URL." ;;
        esac
        tmp="$(mktemp -d)"
        curl -fsSL "$tarball" -o "$tmp/reticora.tar.gz" \
            || die "Could not download $tarball."
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

resolve_sources

case "$MODE" in
    cloud) exec "$SOURCE_DIR/install-cloud.sh" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"} ;;
    vm)    exec "$SOURCE_DIR/install-vm.sh" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"} ;;
esac
