#!/usr/bin/env bash
#
# install-vm.sh — Reticora CMDB collector installer for the customer network.
#
# Interactive ("foolproof") installer for the on-prem collector VM. It prompts
# for every important setting (cloud URL, tenant/collector identity, scan
# subnets, discovery protocols, credentials, TLS/mTLS material), writes an
# environment file and either
#   - generates a systemd unit that runs the collector binary (default), or
#   - generates a minimal docker-compose setup that runs the collector image.
#
# Everything the collector needs is provisioned automatically: missing packages
# (curl, git, tar), Docker/Compose in --docker mode, the Go toolchain for the
# source build and the repository sources themselves when the script is run
# outside a checkout.
#
# The installer is idempotent: re-running it reuses the existing configuration
# as defaults and upgrades the installation in place.
#
# Usage:
#   sudo ./install-vm.sh                      interactive installation (systemd)
#   sudo ./install-vm.sh --docker             use Docker instead of systemd
#   sudo ./install-vm.sh --non-interactive    use existing config / defaults
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="/opt/reticora-collector"
SPOOL_DIR="/var/lib/reticora-collector/spool"
ENV_FILE="$INSTALL_DIR/collector.env"
SYSTEMD_UNIT="/etc/systemd/system/reticora-collector.service"
MODE="systemd"
NON_INTERACTIVE=0
REPO_URL="${RETICORA_REPO_URL:-https://github.com/DataHub-Chiemgau/Reticora-CMDB.git}"
REPO_REF="${RETICORA_REPO_REF:-main}"

print_usage() {
    cat <<'EOF'
install-vm.sh — Reticora CMDB collector installer for the customer network.

Interactive ("foolproof") installer for the on-prem collector VM. It prompts
for every important setting (cloud URL, tenant/collector identity, scan
subnets, discovery protocols, credentials, TLS/mTLS material), writes an
environment file and either
  - generates a systemd unit that runs the collector binary (default), or
  - generates a minimal docker-compose setup that runs the collector image.

Everything the collector needs is provisioned automatically: missing packages
(curl, git, tar), Docker/Compose in --docker mode, the Go toolchain for the
source build and the repository sources themselves when the script is run
outside a checkout.

The installer is idempotent: re-running it reuses the existing configuration
as defaults and upgrades the installation in place.

Usage:
  sudo ./install-vm.sh                      interactive installation (systemd)
  sudo ./install-vm.sh --docker             use Docker instead of systemd
  sudo ./install-vm.sh --non-interactive    use existing config / defaults
EOF
}

for arg in "$@"; do
    case "$arg" in
        --docker) MODE="docker" ;;
        --systemd) MODE="systemd" ;;
        --non-interactive) NON_INTERACTIVE=1 ;;
        -h|--help)
            print_usage
            exit 0
            ;;
        *) echo "Unknown option: $arg" >&2; exit 2 ;;
    esac
done

# ─── Output helpers ───────────────────────────────────────────────────────────
if [ -t 1 ]; then
    C_BLUE=$'\033[0;34m'; C_GREEN=$'\033[0;32m'; C_YELLOW=$'\033[1;33m'
    C_RED=$'\033[0;31m'; C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
else
    C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_BOLD=''; C_RESET=''
fi

info()    { printf '%s==>%s %s\n' "$C_BLUE" "$C_RESET" "$*"; }
success() { printf '%s✔%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn()    { printf '%s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
die()     { printf '%s✘ Error:%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; exit 1; }

trim() {
    local s="$1"
    s="${s#"${s%%[![:space:]]*}"}"
    s="${s%"${s##*[![:space:]]}"}"
    printf '%s' "$s"
}

is_tty() { [ "$NON_INTERACTIVE" -eq 0 ] && [ -t 0 ]; }
have_cmd() { command -v "$1" >/dev/null 2>&1; }

# ─── Dependency installation ──────────────────────────────────────────────────
SUDO=""
if [ "$(id -u)" -ne 0 ] && have_cmd sudo; then
    SUDO="sudo"
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

# ensure_command <command> [package …] — makes sure <command> is available.
ensure_command() {
    local cmd="$1"; shift
    have_cmd "$cmd" && return 0
    local pkgs=("$@")
    [ "${#pkgs[@]}" -gt 0 ] || pkgs=("$cmd")
    install_packages "${pkgs[@]}" || true
    have_cmd "$cmd" || die "'$cmd' is required but could not be installed automatically. Please install it and re-run."
}

install_docker() {
    info "Installing Docker Engine (https://get.docker.com) …"
    ensure_command curl curl ca-certificates
    curl -fsSL https://get.docker.com -o /tmp/reticora-get-docker.sh \
        || die "Could not download the Docker installation script."
    $SUDO sh /tmp/reticora-get-docker.sh || die "Automatic Docker installation failed."
    rm -f /tmp/reticora-get-docker.sh
}

start_docker_daemon() {
    if have_cmd systemctl; then
        $SUDO systemctl enable --now docker >/dev/null 2>&1 || true
    elif have_cmd service; then
        $SUDO service docker start >/dev/null 2>&1 || true
    fi
}

# ─── Prompting helpers ────────────────────────────────────────────────────────
ask() {
    local var="$1" prompt="$2" default="${3:-}" reply
    if ! is_tty; then
        printf -v "$var" '%s' "$default"
        return
    fi
    if [ -n "$default" ]; then
        read -r -p "$prompt [$default]: " reply || true
        reply="$(trim "$reply")"
        printf -v "$var" '%s' "${reply:-$default}"
    else
        # Questions whose prompt is explicitly marked as optional accept an
        # empty answer; all others require a value.
        local optional=0
        case "$prompt" in
            *"optional"*|*"usually empty"*|*"empty = "*) optional=1 ;;
        esac
        while true; do
            read -r -p "$prompt: " reply || true
            reply="$(trim "$reply")"
            [ -n "$reply" ] && break
            [ "$optional" -eq 1 ] && break
            warn "A value is required."
        done
        printf -v "$var" '%s' "$reply"
    fi
}

ask_secret() {
    local var="$1" prompt="$2" default="${3:-}" reply
    if ! is_tty; then
        printf -v "$var" '%s' "$default"
        return
    fi
    if [ -n "$default" ]; then
        read -r -s -p "$prompt [press Enter to keep current value]: " reply || true
        echo
        printf -v "$var" '%s' "${reply:-$default}"
    else
        while true; do
            read -r -s -p "$prompt: " reply || true
            echo
            [ -n "$reply" ] && break
            warn "A value is required."
        done
        printf -v "$var" '%s' "$reply"
    fi
}

ask_validated() {
    local var="$1" prompt="$2" default="${3:-}" validator="$4" value
    while true; do
        ask value "$prompt" "$default"
        if "$validator" "$value"; then
            break
        fi
        if ! is_tty; then
            die "Invalid value for $prompt: '$value'"
        fi
        warn "Invalid input — please try again."
        default="$value"
    done
    printf -v "$var" '%s' "$value"
}

confirm() {
    local prompt="$1" default="${2:-yes}" yn reply
    if [ "$default" = "yes" ]; then yn="Y/n"; else yn="y/N"; fi
    if ! is_tty; then
        [ "$default" = "yes" ]
        return
    fi
    while true; do
        read -r -p "$prompt [$yn]: " reply || true
        reply="$(trim "$reply")"
        case "${reply:-$default}" in
            y|Y|yes|YES|j|J|ja|JA) return 0 ;;
            n|N|no|NO|nein|NEIN)   return 1 ;;
            *) warn "Please answer yes or no." ;;
        esac
    done
}

# ─── Validators ───────────────────────────────────────────────────────────────
valid_url() {
    case "$1" in
        http://*/*|https://*/*|http://?*|https://?*) return 0 ;;
    esac
    return 1
}

valid_cidr_list() {
    local list="$1" item
    [ -n "$list" ] || return 1
    local old_ifs="$IFS"
    IFS=','
    read -r -a items <<< "$list"
    IFS="$old_ifs"
    for item in "${items[@]}"; do
        item="$(trim "$item")"
        # IPv4 CIDR: a.b.c.d/n
        case "$item" in
            *.*.*.*/*) ;;
            *) return 1 ;;
        esac
    done
    return 0
}

valid_duration() {
    case "$1" in
        *[!0-9smh]*) return 1 ;;
        *[0-9]*) return 0 ;;
    esac
    return 1
}

valid_protocols() {
    local list="$1" p
    [ -n "$list" ] || return 1
    local old_ifs="$IFS"
    IFS=','
    read -r -a parts <<< "$list"
    IFS="$old_ifs"
    for p in "${parts[@]}"; do
        p="$(trim "$p")"
        case "$p" in
            sweep|snmp|ssh|wmi|ipmi|redfish|power|nas) ;;
            *) warn "Unknown protocol: $p (supported: sweep, snmp, ssh, wmi, ipmi, redfish, power, nas)"; return 1 ;;
        esac
    done
    return 0
}

valid_existing_file_or_empty() {
    [ -z "$1" ] || [ -f "$1" ]
}

# ─── .env handling ────────────────────────────────────────────────────────────
env_get() {
    local key="$1" line
    [ -f "$ENV_FILE" ] || return 1
    line="$(grep -E "^${key}=" "$ENV_FILE" | tail -n 1)" || return 1
    printf '%s' "${line#*=}"
}

write_env() {
    mkdir -p "$INSTALL_DIR"
    local tmp
    tmp="$(mktemp)"
    cat > "$tmp" <<EOF
# Reticora Collector — customer-network configuration
# Generated by install-vm.sh on $(date -u '+%Y-%m-%d %H:%M:%S UTC')
# Keep this file secret — it may contain device credentials.

# Connection to the Reticora cloud
RETICORA_SERVER_URL=$RETICORA_SERVER_URL
RETICORA_ORGANIZATION_ID=$RETICORA_ORGANIZATION_ID
RETICORA_COLLECTOR_ID=$RETICORA_COLLECTOR_ID

# Discovery scope
RETICORA_SCAN_SUBNETS=$RETICORA_SCAN_SUBNETS
RETICORA_DISCOVERY_PROTOCOLS=$RETICORA_DISCOVERY_PROTOCOLS
RETICORA_DISCOVERY_INTERVAL=$RETICORA_DISCOVERY_INTERVAL
RETICORA_HEARTBEAT_INTERVAL=$RETICORA_HEARTBEAT_INTERVAL

# Device credentials used by the discovery plugins
RETICORA_SNMP_COMMUNITY=$RETICORA_SNMP_COMMUNITY
RETICORA_SSH_USERNAME=$RETICORA_SSH_USERNAME
RETICORA_SSH_PASSWORD=$RETICORA_SSH_PASSWORD

# Offline spool (results are buffered here while the cloud is unreachable)
RETICORA_SPOOL_DIR=$RETICORA_SPOOL_DIR

# mTLS identity (leave empty to use the enrollment keystore)
RETICORA_TLS_CLIENT_CERT_FILE=$RETICORA_TLS_CLIENT_CERT_FILE
RETICORA_TLS_CLIENT_KEY_FILE=$RETICORA_TLS_CLIENT_KEY_FILE
RETICORA_TLS_CA_FILE=$RETICORA_TLS_CA_FILE
RETICORA_TLS_SERVER_NAME=$RETICORA_TLS_SERVER_NAME
RETICORA_CREDENTIALS_PATH=$RETICORA_CREDENTIALS_PATH

RETICORA_ENVIRONMENT=production
EOF
    mv "$tmp" "$ENV_FILE"
    chmod 600 "$ENV_FILE"
    chown root:root "$ENV_FILE" 2>/dev/null || true
}

# ─── Preflight ────────────────────────────────────────────────────────────────
preflight() {
    info "Checking prerequisites …"

    if [ "$(id -u)" -ne 0 ]; then
        die "Please run the collector installer as root (sudo $0)."
    fi

    if [ "$MODE" = "docker" ]; then
        if ! have_cmd docker; then
            warn "Docker was not found on this machine — installing it now."
            install_docker
            have_cmd docker || die "Docker is still not available after the installation attempt."
        fi
        docker info >/dev/null 2>&1 || start_docker_daemon
        docker info >/dev/null 2>&1 || die "Cannot reach the Docker daemon."
        if ! docker compose version >/dev/null 2>&1 && ! have_cmd docker-compose; then
            warn "Docker Compose was not found — installing the compose plugin."
            install_packages docker-compose-plugin || install_packages docker-compose || true
        fi
        docker compose version >/dev/null 2>&1 || have_cmd docker-compose \
            || die "Docker Compose (plugin or docker-compose binary) is required but could not be installed automatically."
        success "Docker detected"
    else
        have_cmd systemctl || die "systemd is required for the systemd mode; use --docker on systems without systemd."
        success "systemd detected"
    fi
}

# ─── Interactive configuration ────────────────────────────────────────────────
collect_config() {
    echo
    printf '%s%sReticora CMDB — collector installation (customer network)%s\n' "$C_BOLD" "$C_BLUE" "$C_RESET"
    echo "The installer now asks for all important settings. Suggested defaults"
    echo "can be accepted with Enter; existing values are reused on re-runs."
    echo

    # Connection to the cloud
    local def_server
    def_server="$(env_get RETICORA_SERVER_URL || true)"
    def_server="${def_server:-https://cmdb.example.com}"
    ask_validated RETICORA_SERVER_URL "URL of the central Reticora cloud (e.g. https://cmdb.example.com)" "$def_server" valid_url

    local def_org
    def_org="$(env_get RETICORA_ORGANIZATION_ID || true)"
    ask RETICORA_ORGANIZATION_ID "Organization ID (tenant UUID from the cloud UI)" "${def_org:-reticora-demo}"
    local def_collector
    def_collector="$(env_get RETICORA_COLLECTOR_ID || true)"
    def_collector="${def_collector:-$(hostname -s 2>/dev/null || hostname)}"
    ask RETICORA_COLLECTOR_ID "Collector ID (unique name for this VM)" "$def_collector"

    # Discovery scope
    local def_subnets
    def_subnets="$(env_get RETICORA_SCAN_SUBNETS || true)"
    ask_validated RETICORA_SCAN_SUBNETS "Subnets to scan (comma-separated CIDR, e.g. 192.168.1.0/24,10.0.0.0/24)" \
        "${def_subnets:-127.0.0.1/32}" valid_cidr_list
    local def_protocols
    def_protocols="$(env_get RETICORA_DISCOVERY_PROTOCOLS || true)"
    ask_validated RETICORA_DISCOVERY_PROTOCOLS "Discovery protocols (comma-separated: sweep, snmp, ssh, wmi, ipmi, redfish, power, nas)" \
        "${def_protocols:-sweep,snmp,ssh}" valid_protocols

    local def_interval
    def_interval="$(env_get RETICORA_DISCOVERY_INTERVAL || true)"
    ask_validated RETICORA_DISCOVERY_INTERVAL "Discovery interval (e.g. 15m)" \
        "${def_interval:-15m}" valid_duration
    def_interval="$(env_get RETICORA_HEARTBEAT_INTERVAL || true)"
    ask_validated RETICORA_HEARTBEAT_INTERVAL "Heartbeat interval (e.g. 1m)" \
        "${def_interval:-1m}" valid_duration

    # Credentials used by the discovery plugins
    ask RETICORA_SNMP_COMMUNITY "SNMP community string (read-only, optional)" "$(env_get RETICORA_SNMP_COMMUNITY || true)"
    ask RETICORA_SSH_USERNAME "SSH username for network devices/servers (optional)" "$(env_get RETICORA_SSH_USERNAME || true)"
    if [ -n "$RETICORA_SSH_USERNAME" ]; then
        ask_secret RETICORA_SSH_PASSWORD "SSH password" "$(env_get RETICORA_SSH_PASSWORD || true)"
    else
        RETICORA_SSH_PASSWORD=""
    fi

    local def_spool
    def_spool="$(env_get RETICORA_SPOOL_DIR || true)"
    ask RETICORA_SPOOL_DIR "Directory for the offline result buffer" "${def_spool:-$SPOOL_DIR}"

    # mTLS identity
    echo
    info "mTLS configuration: the collector authenticates to the cloud with a client certificate."
    echo "    Provide certificate files here, or leave them empty to use the"
    echo "    enrollment keystore ($INSTALL_DIR/credentials.json)."
    ask_validated RETICORA_TLS_CLIENT_CERT_FILE "Path to the client certificate (PEM, empty = enrollment keystore)" \
        "$(env_get RETICORA_TLS_CLIENT_CERT_FILE || true)" valid_existing_file_or_empty
    if [ -n "$RETICORA_TLS_CLIENT_CERT_FILE" ]; then
        ask_validated RETICORA_TLS_CLIENT_KEY_FILE "Path to the client private key (PEM)" \
            "$(env_get RETICORA_TLS_CLIENT_KEY_FILE || true)" valid_existing_file_or_empty
    else
        RETICORA_TLS_CLIENT_KEY_FILE=""
    fi
    ask_validated RETICORA_TLS_CA_FILE "Path to the cloud CA certificate (PEM, optional)" \
        "$(env_get RETICORA_TLS_CA_FILE || true)" valid_existing_file_or_empty
    ask RETICORA_TLS_SERVER_NAME "TLS server name override (usually empty)" \
        "$(env_get RETICORA_TLS_SERVER_NAME || true)"

    RETICORA_CREDENTIALS_PATH="$(env_get RETICORA_CREDENTIALS_PATH || true)"
    RETICORA_CREDENTIALS_PATH="${RETICORA_CREDENTIALS_PATH:-$INSTALL_DIR/credentials.json}"
}

# ─── Source bootstrap ─────────────────────────────────────────────────────────
# The collector is built from the repository sources. When the installer is
# executed on its own (e.g. only install-vm.sh was copied to the VM), the
# sources are downloaded automatically.
SOURCE_DIR="$SCRIPT_DIR"

# Downloads the sources when they are not next to this script. Failure is not
# fatal: the systemd mode can still fall back to a prebuilt binary or an
# existing installation.
ensure_sources() {
    [ -f "$SOURCE_DIR/go.work" ] && [ -d "$SOURCE_DIR/collector" ] && return 0

    SOURCE_DIR="${RETICORA_SOURCE_DIR:-/opt/reticora-cmdb}"
    if [ -f "$SOURCE_DIR/go.work" ] && [ -d "$SOURCE_DIR/collector" ] && [ ! -d "$SOURCE_DIR/.git" ]; then
        return 0
    fi

    info "Downloading the Reticora sources from $REPO_URL ($REPO_REF) …"
    ensure_command curl curl ca-certificates
    have_cmd git || install_packages git || true

    if have_cmd git; then
        if [ -d "$SOURCE_DIR/.git" ]; then
            git -C "$SOURCE_DIR" fetch --depth 1 origin "$REPO_REF" \
                && git -C "$SOURCE_DIR" checkout -q FETCH_HEAD \
                || warn "Could not update the existing checkout in $SOURCE_DIR."
        else
            mkdir -p "$(dirname "$SOURCE_DIR")"
            git clone --depth 1 --branch "$REPO_REF" "$REPO_URL" "$SOURCE_DIR" \
                || warn "Could not clone $REPO_URL into $SOURCE_DIR."
        fi
    else
        local base tarball tmp
        base="${REPO_URL%.git}"
        case "$base" in
            https://github.com/*)
                tarball="$base/archive/refs/heads/$REPO_REF.tar.gz"
                ensure_command tar
                tmp="$(mktemp -d)"
                if curl -fsSL "$tarball" -o "$tmp/reticora.tar.gz"; then
                    mkdir -p "$SOURCE_DIR"
                    tar -xzf "$tmp/reticora.tar.gz" -C "$SOURCE_DIR" --strip-components=1 \
                        || warn "Could not unpack the downloaded archive."
                else
                    warn "Could not download $tarball."
                fi
                rm -rf "$tmp"
                ;;
            *) warn "git is required to download the sources from $REPO_URL." ;;
        esac
    fi

    if [ -f "$SOURCE_DIR/go.work" ]; then
        success "Sources are available in $SOURCE_DIR"
        return 0
    fi
    warn "The Reticora sources could not be downloaded to $SOURCE_DIR."
    return 1
}

# ─── Go toolchain ─────────────────────────────────────────────────────────────
# Returns 0 when the installed Go is new enough for the workspace.
go_version_ok() {
    have_cmd go || return 1
    local want have
    want="$(sed -n 's/^go \([0-9.]*\).*/\1/p' "$SOURCE_DIR/go.work" 2>/dev/null | head -n 1)"
    [ -n "$want" ] || return 0
    have="$(go env GOVERSION 2>/dev/null || true)"
    have="${have#go}"
    [ -n "$have" ] || return 1
    [ "$(printf '%s\n%s\n' "$want" "$have" | sort -V | head -n 1)" = "$want" ]
}

ensure_go_toolchain() {
    go_version_ok && return 0

    local want arch tarball tmp
    want="$(sed -n 's/^go \([0-9.]*\).*/\1/p' "$SOURCE_DIR/go.work" 2>/dev/null | head -n 1)"
    want="${want:-1.25.0}"
    case "$(uname -m)" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        armv6l|armv7l) arch="armv6l" ;;
        *) arch="" ;;
    esac
    if [ -z "$arch" ]; then
        warn "Unsupported CPU architecture $(uname -m) for the automatic Go installation."
        return 1
    fi

    info "Installing the Go $want toolchain to /usr/local/go …"
    ensure_command curl curl ca-certificates
    ensure_command tar
    tarball="https://go.dev/dl/go${want}.linux-${arch}.tar.gz"
    tmp="$(mktemp -d)"
    if ! curl -fsSL "$tarball" -o "$tmp/go.tar.gz"; then
        rm -rf "$tmp"
        warn "Could not download $tarball."
        return 1
    fi
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$tmp/go.tar.gz" || { rm -rf "$tmp"; warn "Could not unpack the Go toolchain."; return 1; }
    rm -rf "$tmp"
    export PATH="/usr/local/go/bin:$PATH"
    go_version_ok || { warn "The installed Go toolchain is still not usable."; return 1; }
    success "Go toolchain: $(go env GOVERSION)"
}

# ─── Collector binary / image provisioning ────────────────────────────────────
provide_collector_systemd() {
    info "Installing the collector binary …"
    mkdir -p "$INSTALL_DIR" "$RETICORA_SPOOL_DIR"

    local bin_src=""
    if [ -f "$SOURCE_DIR/go.work" ] && ensure_go_toolchain; then
        info "Building the collector from source …"
        (cd "$SOURCE_DIR" && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" \
            -o "$INSTALL_DIR/collector.new" ./collector/cmd/collector) \
            || die "Collector build failed."
        bin_src="$INSTALL_DIR/collector.new"
    elif [ -x "$SOURCE_DIR/bin/collector" ]; then
        bin_src="$SOURCE_DIR/bin/collector"
    elif [ -x "$SCRIPT_DIR/bin/collector" ]; then
        bin_src="$SCRIPT_DIR/bin/collector"
    elif [ -x "$INSTALL_DIR/collector" ]; then
        warn "No build source found; keeping the existing collector binary."
    else
        die "No usable Go toolchain, no prebuilt binary at bin/collector and no existing installation. Provide one of them."
    fi

    if [ -n "$bin_src" ]; then
        mv -f "$bin_src" "$INSTALL_DIR/collector"
        chmod 755 "$INSTALL_DIR/collector"
    fi
    success "Collector binary: $INSTALL_DIR/collector"
}

install_systemd_unit() {
    info "Installing the systemd unit …"
    cat > "$SYSTEMD_UNIT" <<EOF
[Unit]
Description=Reticora CMDB Collector
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$ENV_FILE
ExecStart=$INSTALL_DIR/collector
Restart=always
RestartSec=5
# The collector needs raw-socket access for network sweeps.
AmbientCapabilities=CAP_NET_RAW
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
EOF
    chmod 644 "$SYSTEMD_UNIT"
    systemctl daemon-reload
    systemctl enable reticora-collector.service >/dev/null 2>&1 || true
    systemctl restart reticora-collector.service
    success "systemd service reticora-collector is enabled and running"
}

install_docker_setup() {
    info "Installing the Docker-based collector …"
    mkdir -p "$INSTALL_DIR" "$RETICORA_SPOOL_DIR"

    [ -f "$SOURCE_DIR/collector/Dockerfile" ] \
        || die "Collector sources not found in $SOURCE_DIR — the Docker mode needs them as build context."

    cat > "$INSTALL_DIR/docker-compose.yml" <<EOF
# Reticora Collector — generated by install-vm.sh
services:
  collector:
    image: reticora-collector:latest
    build:
      context: $SOURCE_DIR
      dockerfile: collector/Dockerfile
    env_file:
      - $ENV_FILE
    network_mode: host
    restart: unless-stopped
    volumes:
      - $RETICORA_SPOOL_DIR:$RETICORA_SPOOL_DIR
      - $INSTALL_DIR:$INSTALL_DIR
EOF

    if docker compose version >/dev/null 2>&1; then
        (cd "$INSTALL_DIR" && docker compose build && docker compose up -d) \
            || die "Docker collector startup failed."
    elif have_cmd docker-compose; then
        (cd "$INSTALL_DIR" && docker-compose build && docker-compose up -d) \
            || die "Docker collector startup failed."
    else
        die "docker compose not found."
    fi
    success "Collector container is running"
}

# ─── Verification ─────────────────────────────────────────────────────────────
verify() {
    info "Verifying the collector …"
    sleep 3
    if [ "$MODE" = "docker" ]; then
        if docker ps --format '{{.Image}}' | grep -q reticora-collector; then
            success "Collector container is running"
        else
            warn "Collector container is not running — check the container logs with 'docker logs'."
        fi
    else
        if systemctl is-active --quiet reticora-collector.service; then
            success "reticora-collector.service is active"
        else
            warn "Service is not active — check: journalctl -u reticora-collector -e"
        fi
    fi
}

# ─── Summary ──────────────────────────────────────────────────────────────────
print_summary() {
    echo
    printf '%s%sCollector installation complete%s\n' "$C_BOLD" "$C_GREEN" "$C_RESET"
    cat <<EOF

  Cloud URL:         $RETICORA_SERVER_URL
  Organization:      $RETICORA_ORGANIZATION_ID
  Collector ID:      $RETICORA_COLLECTOR_ID
  Scan subnets:      $RETICORA_SCAN_SUBNETS
  Protocols:         $RETICORA_DISCOVERY_PROTOCOLS
  Mode:              $MODE

  Configuration:     $ENV_FILE
EOF
    if [ "$MODE" = "systemd" ]; then
        cat <<EOF
  Service:           systemctl status reticora-collector
  Logs:              journalctl -u reticora-collector -f
EOF
    else
        cat <<EOF
  Compose project:   $INSTALL_DIR/docker-compose.yml
  Logs:              docker compose -f $INSTALL_DIR/docker-compose.yml logs -f
EOF
    fi
    echo
}

# ─── Main ─────────────────────────────────────────────────────────────────────
main() {
    preflight
    collect_config
    echo
    info "Configuration:"
    cat <<EOF
  Cloud URL:         $RETICORA_SERVER_URL
  Organization:      $RETICORA_ORGANIZATION_ID
  Collector ID:      $RETICORA_COLLECTOR_ID
  Subnets:           $RETICORA_SCAN_SUBNETS
  Protocols:         $RETICORA_DISCOVERY_PROTOCOLS
  Interval:          $RETICORA_DISCOVERY_INTERVAL (heartbeat $RETICORA_HEARTBEAT_INTERVAL)
  mTLS cert file:    ${RETICORA_TLS_CLIENT_CERT_FILE:-<enrollment keystore>}
EOF
    if is_tty; then
        confirm "Start the installation with these settings?" "yes" || die "Aborted by user."
    fi

    write_env
    success "Environment written to $ENV_FILE"

    ensure_sources || true

    if [ "$MODE" = "docker" ]; then
        install_docker_setup
    else
        provide_collector_systemd
        install_systemd_unit
    fi

    verify
    print_summary
}

main "$@"
