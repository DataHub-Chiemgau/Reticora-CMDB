#!/usr/bin/env bash
#
# install-cloud.sh — Reticora CMDB central-cloud installer.
#
# Interactive ("foolproof") installer for the central Reticora cloud stack
# (PostgreSQL/TimescaleDB, NATS JetStream, Redis, MinIO, Keycloak, backend
# server, frontend). It installs missing prerequisites (Docker Engine,
# Docker Compose, curl, openssl) automatically, prompts for every important
# setting, generates all required secrets, writes a .env file, builds or pulls
# the container images, applies the database migrations, starts the stack and
# verifies its health.
#
# Optionally provisions HTTPS: when a public domain name is entered, the
# installer obtains a free Let's Encrypt certificate via Certbot (nginx
# serves the ACME http-01 challenge) and configures nginx for TLS with
# automatic renewal.
#
# Re-running the script reuses the existing .env values as defaults and is
# idempotent: existing volumes, keys and configuration are preserved.
#
# Release mode (TEC-15; the default per E-69, Option 1): the signed release
# images reticora-server, reticora-frontend and reticora-postgres of the
# release (--release, otherwise the release tag of this checkout or the
# newest release) are verified with 'cosign verify' against the identity of
# the release workflow and pinned by digest in .env before anything starts.
# This checkout — compose file, realm, migrations — must be the signed source
# of that release. --build-from-source builds the images from this checkout
# instead; they are not signature-verified.
#
# Usage:
#   ./install-cloud.sh                    interactive installation
#   ./install-cloud.sh --non-interactive  use existing .env / defaults /
#                                         generated secrets without prompting
#   ./install-cloud.sh --release v1.2.3   the signed images of that release
#   ./install-cloud.sh --build-from-source  local build, not signature-verified
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_DIR="$SCRIPT_DIR/deploy/docker-compose"
COMPOSE_FILE="$COMPOSE_DIR/docker-compose.yml"
OVERRIDE_FILE="$COMPOSE_DIR/docker-compose.override.yml"
# json-file log rotation of a host without systemd-journald (NFR-06, see
# configure_log_retention).
LOGGING_JSON_FILE="$COMPOSE_DIR/docker-compose.logging-json.yml"
ENV_FILE="$COMPOSE_DIR/.env"
NON_INTERACTIVE=0
# Release whose signed images are installed (empty: chosen in select_release).
RELEASE="${RETICORA_RELEASE:-}"
# Registry of the signed images (empty: .env, otherwise RELEASE_REGISTRY).
IMAGE_REGISTRY_OVERRIDE="${RETICORA_IMAGE_REGISTRY:-}"
# ACME server of the TLS certificate and the CA bundle the certbot container
# trusts for it (empty: .env, otherwise Let's Encrypt production with the
# image's public CAs; WP-420, staging and test CAs).
ACME_SERVER_OVERRIDE="${RETICORA_ACME_SERVER:-}"
ACME_CA_BUNDLE_OVERRIDE="${RETICORA_ACME_CA_BUNDLE:-}"
# Central SMTP server of the installation (TEC-13; empty: .env, otherwise
# asked for in an interactive run, see collect_smtp_config).
SMTP_HOST_OVERRIDE="${RETICORA_SMTP_HOST:-}"
SMTP_PORT_OVERRIDE="${RETICORA_SMTP_PORT:-}"
SMTP_TLS_OVERRIDE="${RETICORA_SMTP_TLS:-}"
SMTP_USERNAME_OVERRIDE="${RETICORA_SMTP_USERNAME:-}"
SMTP_PASSWORD_OVERRIDE="${RETICORA_SMTP_PASSWORD:-}"
SMTP_FROM_OVERRIDE="${RETICORA_SMTP_FROM:-}"
# Names on the server certificate of the collector NATS endpoint (empty:
# derived on every run, see nats_edge_hosts).
NATS_EDGE_HOSTS_OVERRIDE="${RETICORA_NATS_EDGE_HOSTS:-}"
# E-69 (Option 1): the signed release artifacts are the default; the local
# source build only with --build-from-source (RETICORA_BUILD_FROM_SOURCE=1).
case "${RETICORA_BUILD_FROM_SOURCE:-0}" in
    1) BUILD_FROM_SOURCE=1 ;;
    0) BUILD_FROM_SOURCE=0 ;;
    *) echo "RETICORA_BUILD_FROM_SOURCE must be 1 (build from source) or 0, not '$RETICORA_BUILD_FROM_SOURCE'" >&2; exit 2 ;;
esac

print_usage() {
    cat <<'EOF'
install-cloud.sh — Reticora CMDB central-cloud installer.

Interactive ("foolproof") installer for the central Reticora cloud stack
(PostgreSQL/TimescaleDB, NATS JetStream, Redis, MinIO, Keycloak, backend
server, frontend). It installs missing prerequisites (Docker Engine,
Docker Compose, curl, openssl) automatically, prompts for every important
setting, generates all required secrets, writes a .env file, builds or pulls
the container images, applies the database migrations, starts the stack and
verifies its health.

Optionally provisions HTTPS: when a public domain name is entered, the
installer obtains a free Let's Encrypt certificate via Certbot (nginx
serves the ACME http-01 challenge) and configures nginx for TLS with
automatic renewal.

Re-running the script reuses the existing .env values as defaults and is
idempotent: existing volumes, keys and configuration are preserved.

By default the signed release images are installed: those of --release
v<version>, otherwise of the release tag this checkout is on, otherwise of the
newest release — verified with cosign before the start and pinned by digest.
This checkout must be the signed source of that release (a git checkout of
its tag, or its source tarball); ./install.sh --cloud --ref v<version>
downloads it. --build-from-source builds the images from this checkout
instead; they are not signature-verified.

Usage:
  ./install-cloud.sh                    interactive installation
  ./install-cloud.sh --non-interactive  use existing .env / defaults /
                                        generated secrets without prompting
  ./install-cloud.sh --release v1.2.3   the signed images of that release
                                        (RETICORA_RELEASE; registry:
                                        RETICORA_IMAGE_REGISTRY, default
                                        ghcr.io/datahub-chiemgau)
  ./install-cloud.sh --build-from-source  build the images from this checkout
                                        — not signature-verified
                                        (RETICORA_BUILD_FROM_SOURCE=1)

Settings taken from the environment, otherwise from .env (a non-interactive
run asks for nothing):
  RETICORA_SMTP_HOST, RETICORA_SMTP_PORT, RETICORA_SMTP_TLS (starttls|tls|none),
  RETICORA_SMTP_USERNAME, RETICORA_SMTP_PASSWORD, RETICORA_SMTP_FROM
                                        outgoing e-mail: self-signup (its e-mail
                                        verification is mandatory), invitations,
                                        notifications; empty host = no e-mail
  RETICORA_NATS_EDGE_HOSTS              names on the collector endpoint's
                                        certificate (default: derived from the
                                        domain, the public base URL and the host IP)
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --non-interactive) NON_INTERACTIVE=1 ;;
        --build-from-source) BUILD_FROM_SOURCE=1 ;;
        --release) [ "$#" -ge 2 ] || { echo "--release requires a version tag" >&2; exit 2; }; RELEASE="$2"; shift ;;
        --release=*) RELEASE="${1#*=}" ;;
        -h|--help)
            print_usage
            exit 0
            ;;
        *) echo "Unknown option: $1" >&2; exit 2 ;;
    esac
    shift
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

# Platform, Docker/Compose and sizing checks (INS-07), signature verification
# (TEC-15).
# shellcheck source=install-preflight.sh
. "$SCRIPT_DIR/install-preflight.sh"

if [ -n "$RELEASE" ]; then
    is_release_tag "$RELEASE" \
        || die "--release expects a version tag v<version> of a signed release, not '$RELEASE'."
    [ "$BUILD_FROM_SOURCE" != 1 ] \
        || die "--release installs the signed images of a release, --build-from-source builds them from this checkout; use one of them."
fi

# ─── Generic helpers ──────────────────────────────────────────────────────────
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

# ensure_command <command> [package …] — makes sure <command> is available.
ensure_command() {
    local cmd="$1"; shift
    have_cmd "$cmd" && return 0
    local pkgs=("$@")
    [ "${#pkgs[@]}" -gt 0 ] || pkgs=("$cmd")
    install_packages "${pkgs[@]}" || true
    have_cmd "$cmd" || die "'$cmd' is required but could not be installed automatically. Please install it and re-run."
}

# ─── Docker / compose detection ───────────────────────────────────────────────
DOCKER="docker"
COMPOSE=()

install_docker() {
    info "Installing Docker Engine (https://get.docker.com) …"
    ensure_command curl curl ca-certificates
    curl -fsSL https://get.docker.com -o /tmp/reticora-get-docker.sh \
        || die "Could not download the Docker installation script."
    $SUDO sh /tmp/reticora-get-docker.sh || die "Automatic Docker installation failed."
    rm -f /tmp/reticora-get-docker.sh
    if [ -n "$SUDO" ] && [ -n "${USER:-}" ]; then
        $SUDO usermod -aG docker "$USER" >/dev/null 2>&1 || true
    fi
}

start_docker_daemon() {
    if have_cmd systemctl; then
        info "Starting the Docker daemon …"
        $SUDO systemctl enable --now docker >/dev/null 2>&1 || true
    elif have_cmd service; then
        $SUDO service docker start >/dev/null 2>&1 || true
    fi
}

detect_container_tooling() {
    if ! have_cmd docker; then
        warn "Docker was not found on this machine — installing it now."
        install_docker
        have_cmd docker || die "Docker is still not available after the installation attempt."
        start_docker_daemon
    fi

    if ! docker info >/dev/null 2>&1 && ! sudo -n docker info >/dev/null 2>&1; then
        # The daemon may simply not be running yet (fresh install, minimal image).
        start_docker_daemon
    fi

    if ! docker info >/dev/null 2>&1; then
        if sudo -n docker info >/dev/null 2>&1; then
            warn "Your user cannot talk to the Docker daemon directly; using 'sudo docker'."
            DOCKER="sudo docker"
        else
            die "Cannot reach the Docker daemon. Is it running, and is your user in the 'docker' group?"
        fi
    fi

    if ! $DOCKER compose version >/dev/null 2>&1 && ! have_cmd docker-compose; then
        warn "Docker Compose was not found — installing the compose plugin."
        install_packages docker-compose-plugin || install_packages docker-compose || true
    fi

    if $DOCKER compose version >/dev/null 2>&1; then
        COMPOSE=(docker compose)
        if [ "$DOCKER" != "docker" ]; then
            COMPOSE=(sudo docker compose)
        fi
    elif have_cmd docker-compose; then
        COMPOSE=(docker-compose)
        if [ "$DOCKER" != "docker" ]; then
            COMPOSE=(sudo docker-compose)
        fi
    else
        die "Docker Compose (plugin or docker-compose binary) is required but could not be installed automatically."
    fi

    # Docker Engine >= 24 and Compose v2 (INS-07).
    check_docker_engine "$DOCKER"
    check_compose_v2 "${COMPOSE[@]}"
}

compose_cmd() {
    # Passing -f explicitly disables compose's automatic merging of
    # docker-compose.override.yml, so merge it by hand whenever it exists
    # (it carries the build: sections for the local source build). The
    # prebuilt-image path renames it to .disabled to opt out. -f also
    # disables COMPOSE_FILE of .env: the json-file log rotation that it
    # selects on a host without systemd-journald is merged here as well.
    local files=(-f "$COMPOSE_FILE")
    if [ -f "$OVERRIDE_FILE" ]; then
        files+=(-f "$OVERRIDE_FILE")
    fi
    if log_rotation_selected; then
        files+=(-f "$LOGGING_JSON_FILE")
    fi
    "${COMPOSE[@]}" --env-file "$ENV_FILE" "${files[@]}" "$@"
}

# ─── Prompting helpers ────────────────────────────────────────────────────────
# ask <variable> <prompt> [default]
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

# ask_secret <variable> <prompt> [default] — hidden input, empty keeps default.
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

# ask_optional_secret <variable> <prompt> [default] — hidden input that may
# stay empty: Enter keeps the default (empty without one), '-' removes it.
# The answer is never echoed.
ask_optional_secret() {
    local var="$1" prompt="$2" default="${3:-}" reply
    if ! is_tty; then
        printf -v "$var" '%s' "$default"
        return
    fi
    if [ -n "$default" ]; then
        read -r -s -p "$prompt [Enter keeps the current value, '-' removes it]: " reply || true
    else
        read -r -s -p "$prompt [Enter = none]: " reply || true
    fi
    echo
    case "$reply" in
        '') reply="$default" ;;
        -) reply="" ;;
    esac
    printf -v "$var" '%s' "$reply"
}

# ask_validated <variable> <prompt> [default] <validator-function> [hint-function]
#
# The optional hint function receives the rejected value plus a nameref; it
# warns why the value was rejected and may set the nameref to a corrected
# value, which — if it passes validation — is offered as the next prompt's
# default (e.g. a missing https:// scheme is filled in). It is called
# in-process (no command substitution) so the nameref assignment survives.
ask_validated() {
    local var="$1" prompt="$2" default="${3:-}" validator="$4" hint="${5:-}" value suggested
    while true; do
        ask value "$prompt" "$default"
        if "$validator" "$value"; then
            break
        fi
        if ! is_tty; then
            die "Invalid value for $prompt: '$value'"
        fi
        if [ -n "$hint" ]; then
            suggested=''
            # Run the hint in-process (no command substitution) so the
            # nameref assignment to 'suggested' survives. The hint prints its
            # message via warn; if the suggested value passes validation, it
            # is offered as the next prompt's default.
            "$hint" "$value" suggested
            if [ -n "$suggested" ] && "$validator" "$suggested"; then
                default="$suggested"
            else
                # Keep showing the user's last entry on the next prompt.
                default="$value"
            fi
        else
            warn "Invalid input — please try again."
            # Keep showing the user's last entry on the next prompt.
            default="$value"
        fi
    done
    printf -v "$var" '%s' "$value"
}

# confirm <prompt> [yes|no] — returns 0 for yes, 1 for no.
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
valid_port() {
    case "$1" in
        ''|*[!0-9]*) return 1 ;;
    esac
    [ "$1" -ge 1 ] && [ "$1" -le 65535 ]
}

# valid_url <url> — an http(s) URL the server accepts (config.Validate,
# absoluteHTTPURL: Go's url.Parse): a host, a port only of digits, no user
# info. "https://https:host" (host "https", port "host") passed the former
# pattern check and stopped the server at its start.
URL_AUTHORITY_RE='^(\[[0-9A-Fa-f:.]+\]|[^][/@:%\^`{|}[:space:][:cntrl:]]+)(:[0-9]+)?$'
valid_url() {
    local authority
    case "$1" in
        http://?*|https://?*) ;;
        *) return 1 ;;
    esac
    authority="${1#*://}"
    authority="${authority%%[/?#]*}"
    [[ "$authority" =~ $URL_AUTHORITY_RE ]]
}

# explain_url_error <value> <suggestion-nameref> — warns why a URL was
# rejected and suggests the corrected value via the nameref: a missing
# scheme, a missing "//" ("https:host") or the scheme typed twice
# ("https://https:host", "https://https://host").
explain_url_error() {
    local -n _suggest="$2"
    local rest
    case "$1" in
        http://http:*|http://https:*|https://http:*|https://https:*)
            rest="${1#*://}"
            rest="${rest#*:}"
            while [[ "$rest" == /* ]]; do rest="${rest#/}"; done
            _suggest="${1%%://*}://$rest"
            warn "Invalid input — the scheme is in the URL twice, e.g. $_suggest" ;;
        http://*|https://*)
            warn "Invalid input — the URL needs a host, optionally ':' and a port number, and no user@ (e.g. https://cmdb.example.com)." ;;
        *"://"*)
            warn "Invalid input — only http:// and https:// URLs are supported." ;;
        http:*|https:*)
            rest="${1#*:}"
            while [[ "$rest" == /* ]]; do rest="${rest#/}"; done
            _suggest="${1%%:*}://$rest"
            warn "Invalid input — the URL must start with ${1%%:*}:// (two slashes), e.g. $_suggest" ;;
        *)
            _suggest="https://$1"
            warn "Invalid input — the URL must start with http:// or https://, e.g. $_suggest" ;;
    esac
}

valid_password() { [ "${#1}" -ge 8 ]; }
# The runtime login's password is embedded unescaped in the server's database
# URL and in a psql string literal, so it is restricted to URL-safe characters.
valid_app_password() { [[ "$1" =~ ^[A-Za-z0-9._~-]{16,}$ ]]; }

valid_domain() {
    # Hostname labels: 1-63 chars each, alphanumerics and hyphens (no
    # leading/trailing hyphen), at least one dot, no leading/trailing dot.
    case "$1" in
        ''|.*|*..*|*' '|*'/'*) return 1 ;;
    esac
    printf '%s' "$1" | grep -qE '^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$'
}

valid_email() {
    # Deliberately permissive; only used for the Let's Encrypt expiry notices.
    printf '%s' "$1" | grep -qE '^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$'
}

# The initial administrator must not reuse the demo account of earlier
# releases, whose password is publicly known; that account is removed.
valid_initial_admin_email() {
    valid_email "$1" && [ "$1" != "$LEGACY_DEMO_ADMIN" ]
}

# Optional variants: empty input keeps the feature disabled.
valid_optional_domain() { [ -z "$1" ] || valid_domain "$1"; }
valid_optional_email()  { [ -z "$1" ] || valid_email "$1"; }

# explain_domain_error <value> <suggestion-nameref> — a URL or host:port in
# place of the domain suggests its host name.
explain_domain_error() {
    local -n _suggest="$2"
    local host
    host="$(url_host "$1")"
    if [ "$host" != "${1,,}" ] && valid_domain "$host"; then
        _suggest="$host"
        warn "Invalid input — enter the domain name only, without scheme, port or path, e.g. $host"
    else
        warn "Invalid input — enter a public DNS name such as cmdb.example.com (no IP address, no localhost)."
    fi
}

# valid_optional_acme_server <url> — empty, or the https:// directory URL of
# an ACME server. It is written to .env and reaches certbot's command line,
# so only URL characters without quotes, blanks or '$' are accepted.
valid_optional_acme_server() {
    [ -z "$1" ] || [[ "$1" =~ ^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~%/-]*)?$ ]]
}

# Host names (labels of letters, digits and hyphens, dot-separated; a single
# label such as an internal relay name is allowed).
HOST_NAME_RE='^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$'

# valid_host <host> — a host name, an IPv4 address or an IPv6 address
# (without brackets).
valid_host() {
    [ -n "$1" ] && [ "${#1}" -le 253 ] || return 1
    if [[ "$1" == *:* ]]; then
        # An IPv6 literal has at least two colons ("host:port" is refused).
        [[ "$1" == *:*:* ]] && [[ "$1" =~ ^[0-9A-Fa-f:.]+$ ]] && [[ "$1" != *:::* ]]
        return
    fi
    [[ "$1" =~ $HOST_NAME_RE ]]
}

# host_is_loopback <host> — localhost, 127.x, ::1 or the unspecified address.
host_is_loopback() {
    case "${1,,}" in
        ''|localhost|*.localhost|127.*|0.0.0.0|::1|0:0:0:0:0:0:0:1|::) return 0 ;;
    esac
    return 1
}

# host_is_ip <host> — an IPv4 or IPv6 literal.
host_is_ip() { [[ "$1" == *:* ]] || [[ "$1" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; }

# url_host <url> — the host of an http(s) URL without user info, port and
# path, lower case; an IPv6 literal without its brackets.
url_host() {
    local rest="${1#*://}"
    rest="${rest%%[/?#]*}"
    rest="${rest##*@}"
    if [[ "$rest" == \[*\]* ]]; then
        rest="${rest#\[}"
        rest="${rest%%\]*}"
    else
        rest="${rest%%:*}"
    fi
    printf '%s' "${rest,,}"
}

# tls_domain_url <url> — <url> on https:// when it is an http:// URL of
# RETICORA_TLS_DOMAIN (its port dropped: HTTPS is on RETICORA_HTTPS_PORT,
# default 443); any other URL unchanged.
tls_domain_url() {
    local url="$1" rest path="" port
    if [[ "$url" != http://* ]] || [ "$(url_host "$url")" != "${RETICORA_TLS_DOMAIN,,}" ]; then
        printf '%s' "$url"
        return 0
    fi
    rest="${url#http://}"
    [[ "$rest" != */* ]] || path="/${rest#*/}"
    port="$(env_get RETICORA_HTTPS_PORT || true)"
    case "$port" in
        ''|443) port="" ;;
        *) port=":$port" ;;
    esac
    printf 'https://%s%s%s' "$RETICORA_TLS_DOMAIN" "$port" "$path"
}

# SMTP settings (TEC-13). The SMTP server is reached from the server and the
# Keycloak containers: a loopback name would be the container itself.
valid_optional_smtp_host() { [ -z "$1" ] || { valid_host "$1" && ! host_is_loopback "$1"; }; }
# Interactive answer: '-' removes a configured server.
valid_smtp_host_answer() { [ "$1" = - ] || valid_optional_smtp_host "$1"; }
valid_smtp_tls() { case "$1" in starttls|tls|none) return 0 ;; esac; return 1; }

# explain_smtp_host_error <value> <suggestion-nameref> — why an SMTP host was
# rejected.
explain_smtp_host_error() {
    if valid_host "$1" && host_is_loopback "$1"; then
        warn "Invalid input — '$1' is the loopback address of the server container, not of this host. Enter the host name or IP address of the SMTP relay as the containers reach it."
    else
        warn "Invalid input — enter a host name (e.g. smtp.example.com) or an IP address, without port (asked next)."
    fi
}

# valid_env_value <value> — a value written unquoted to .env: no blanks,
# control characters, quotes, backslash, backtick or '$' (docker compose
# interpolates '$' in .env values).
valid_env_value() {
    case "$1" in
        *[[:space:]]*|*[[:cntrl:]]*|*\"*|*\'*|*\`*|*\\*|*\$*) return 1 ;;
    esac
    return 0
}
# Interactive answer for the optional SMTP user name: '-' removes it.
valid_smtp_username_answer() { [ "$1" = - ] || valid_env_value "$1"; }

# valid_smtp_from <value> — the sender of the e-mails: an address or
# 'Display Name <address>', within what the server accepts: it parses the
# value with Go's net/mail.ParseAddress (notify.SMTPConfig.Validate) and
# exits at start on a refusal, which the installer would only see as an
# unhealthy server. Accepted here (a subset of net/mail):
#   - the address as dot-atom: no leading, trailing or double dot in the
#     local part, a host name of non-empty labels as domain;
#   - the display name as atoms: letters (also non-ASCII, valid UTF-8),
#     digits, blanks and . ! % & * + / = ? ^ _ { | } ~ -, none of the
#     specials , ; : @ [ ] ( ) < > and no word that starts with '=?' (an RFC
#     2047 encoded word, refused for an unknown charset);
#   - no quotes, '$', '#', backslash or backtick anywhere, since the value is
#     written unquoted to .env.
SMTP_LOCAL_RE='[A-Za-z0-9_%+=-]+(\.[A-Za-z0-9_%+=-]+)*'
SMTP_LABEL_RE='[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?'
SMTP_ADDRESS_RE="^${SMTP_LOCAL_RE}@(${SMTP_LABEL_RE}\\.)+[A-Za-z]{2,63}\$"
# Matched byte-wise (LC_ALL=C): the ASCII characters of an atom plus any
# byte of a multi-byte character, whose encoding valid_utf8 checks.
SMTP_DISPLAY_NAME_RE=$'^([ A-Za-z0-9.!%&*+/=?^_{|}~-]|[\x80-\xff])*$'
UTF8_RE=$'^([\x01-\x7f]|[\xc2-\xdf][\x80-\xbf]|\xe0[\xa0-\xbf][\x80-\xbf]|[\xe1-\xec\xee\xef][\x80-\xbf][\x80-\xbf]|\xed[\x80-\x9f][\x80-\xbf]|\xf0[\x90-\xbf][\x80-\xbf][\x80-\xbf]|[\xf1-\xf3][\x80-\xbf][\x80-\xbf][\x80-\xbf]|\xf4[\x80-\x8f][\x80-\xbf][\x80-\xbf])*$'

# valid_utf8 <value> — well-formed UTF-8 (no overlong form, surrogate or
# code point above U+10FFFF), as Go's utf8.DecodeRune requires.
valid_utf8() { local LC_ALL=C; [[ "$1" =~ $UTF8_RE ]]; }

# valid_smtp_display_name <name> — the display name before '<'.
valid_smtp_display_name() {
    local LC_ALL=C
    case " $1" in *' =?'*) return 1 ;; esac
    [[ "$1" =~ $SMTP_DISPLAY_NAME_RE ]] && valid_utf8 "$1"
}

valid_smtp_from() {
    local addr="$1"
    case "$1" in
        *[[:cntrl:]]*|*\"*|*\'*|*\`*|*\\*|*\$*|*'#'*) return 1 ;;
    esac
    if [[ "$1" == *'<'*'>' ]]; then
        valid_smtp_display_name "${1%%<*}" || return 1
        addr="${1#*<}"
        addr="${addr%>}"
    fi
    [[ "$addr" =~ $SMTP_ADDRESS_RE ]]
}

# explain_smtp_from_error <value> <suggestion-nameref>
explain_smtp_from_error() {
    warn "Invalid input — enter an e-mail address (noreply@example.com) or 'Name <address>' (Reticora CMDB <noreply@example.com>). The name must not contain , ; : @ [ ] ( ) < > (the server refuses them, e.g. 'Reticora, Inc.' — write 'Reticora Inc.'); the address has no leading, trailing or double dot; no quotes, '\$', '#' or '\\' anywhere."
}

# valid_optional_ca_bundle <path> — empty, or the absolute path of a readable
# PEM certificate file. docker-compose.yml mounts it into the certbot
# container (short volume syntax: no ':' or ',') and it is written to .env.
valid_optional_ca_bundle() {
    [ -z "$1" ] && return 0
    [[ "$1" =~ ^/[A-Za-z0-9._/+-]+$ ]] && [ -f "$1" ] && [ -r "$1" ] &&
        openssl x509 -in "$1" -noout >/dev/null 2>&1
}

# ─── .env handling ────────────────────────────────────────────────────────────
env_get() {
    # env_get <key> [file] — prints the raw value from the existing .env (or
    # the given file), if any.
    local key="$1" file="${2:-$ENV_FILE}" line
    [ -f "$file" ] || return 1
    line="$(grep -E "^${key}=" "$file" | tail -n 1)" || return 1
    printf '%s' "${line#*=}"
}

# save_env_previous — copies the current .env to .env.previous (mode 0600,
# like .env): the values last applied to Keycloak (INS-05, see
# apply_keycloak_rotation).
save_env_previous() {
    rm -f "$ENV_FILE.previous"
    (umask 077 && cp "$ENV_FILE" "$ENV_FILE.previous") || die "Could not write $ENV_FILE.previous."
}

write_env() {
    local tmp
    tmp="$(mktemp)"
    cat > "$tmp" <<EOF
# Reticora CMDB — central cloud environment
# Generated by install-cloud.sh on $(date -u '+%Y-%m-%d %H:%M:%S UTC')
# Keep this file secret — it contains passwords and private key material.

# Database: the bootstrap superuser (reticora) runs only the migrations and
# the backups; the server logs in as reticora_runtime (NOSUPERUSER,
# NOBYPASSRLS, member of reticora_app only, TEN-03).
RETICORA_DB_PASSWORD=$RETICORA_DB_PASSWORD
RETICORA_DB_APP_PASSWORD=$RETICORA_DB_APP_PASSWORD

# Object storage (MinIO / S3-compatible)
RETICORA_S3_ACCESS_KEY=$RETICORA_S3_ACCESS_KEY
RETICORA_S3_SECRET_KEY=$RETICORA_S3_SECRET_KEY

# Keycloak (identity & access management)
KEYCLOAK_ADMIN=$KEYCLOAK_ADMIN
KEYCLOAK_ADMIN_PASSWORD=$KEYCLOAK_ADMIN_PASSWORD
KEYCLOAK_DB_PASSWORD=$KEYCLOAK_DB_PASSWORD
RETICORA_INITIAL_ADMIN_EMAIL=$RETICORA_INITIAL_ADMIN_EMAIL
RETICORA_OIDC_CLIENT_SECRET=$RETICORA_OIDC_CLIENT_SECRET
# Keycloak admin client of the server: a confidential client whose service
# account holds realm-management manage-users, view-users and query-users.
# The server creates the accounts of self-signups, accepted invitations and
# operator-created organizations with it and binds org_admins to MFA. The
# installer creates it and applies a changed secret on every run
# (ensure_admin_client); generated once, never asked for.
RETICORA_KEYCLOAK_ADMIN_CLIENT_ID=${RETICORA_KEYCLOAK_ADMIN_CLIENT_ID:-}
RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET=${RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET:-}

# Self-signup at <RETICORA_PUBLIC_BASE_URL>/onboarding (TLC-01): key that signs
# the ALTCHA challenges (at least 32 characters, generated once). Signup
# also needs the SMTP server below: its e-mail verification is mandatory.
RETICORA_SIGNUP_CAPTCHA_KEY=${RETICORA_SIGNUP_CAPTCHA_KEY:-}

# Outgoing e-mail: the central SMTP server (TEC-13) for the verification
# e-mail of the self-signup, invitations and notification mails; the realm
# 'reticora' of Keycloak sends over it as well (apply_realm_smtp). Empty host
# = no e-mail, self-signup disabled. With a host, the server's readiness
# (/readyz) depends on it. TLS: starttls, tls (implicit) or none.
RETICORA_SMTP_HOST=${RETICORA_SMTP_HOST:-}
RETICORA_SMTP_PORT=${RETICORA_SMTP_PORT:-587}
RETICORA_SMTP_TLS=${RETICORA_SMTP_TLS:-starttls}
RETICORA_SMTP_USERNAME=${RETICORA_SMTP_USERNAME:-}
RETICORA_SMTP_PASSWORD=${RETICORA_SMTP_PASSWORD:-}
RETICORA_SMTP_FROM=${RETICORA_SMTP_FROM:-}

# Credential envelope encryption (32-byte key, base64-encoded)
RETICORA_MASTER_KEY=$RETICORA_MASTER_KEY

# Encryption passphrase of the pgBackRest backups (docs/backup-dr.md). Keep a
# copy outside this host: without it no backup can be restored.
RETICORA_BACKUP_CIPHER_PASS=$RETICORA_BACKUP_CIPHER_PASS

# Public URLs
RETICORA_PUBLIC_BASE_URL=$RETICORA_PUBLIC_BASE_URL
RETICORA_OIDC_ISSUER_URL=$RETICORA_OIDC_ISSUER_URL
RETICORA_OIDC_CLIENT_ID=$RETICORA_OIDC_CLIENT_ID
RETICORA_OIDC_REDIRECT_URL=$RETICORA_OIDC_REDIRECT_URL

# HTTPS / Let's Encrypt (empty = plain HTTP)
RETICORA_TLS_DOMAIN=$RETICORA_TLS_DOMAIN
RETICORA_CERT_EMAIL=$RETICORA_CERT_EMAIL
# ACME server of the certificate request and the renewals (empty = Let's
# Encrypt production) and the CA bundle the certbot container trusts for its
# HTTPS endpoint (empty = the image's public CAs); staging and test CAs, see
# docs/troubleshooting-installation.md
RETICORA_ACME_SERVER=${RETICORA_ACME_SERVER:-}
RETICORA_ACME_CA_BUNDLE=${RETICORA_ACME_CA_BUNDLE:-}
# nginx TLS template mounted into the frontend (empty one without a domain)
RETICORA_NGINX_TLS_TEMPLATE=$(tls_template_source)

# Extra CA bundle the server trusts when it calls the OIDC issuer. The server
# reaches Keycloak through the public HTTPS endpoint, so while only the
# self-signed bootstrap certificate is installed (Let's Encrypt not issued
# yet) Go would reject the token request with
# "x509: certificate signed by unknown authority". Trusting the certificate
# that this very host serves keeps the login working until certbot succeeds;
# with a real Let's Encrypt certificate the file simply duplicates a chain
# that the system trust store already accepts.
RETICORA_OIDC_CA_CERT_FILE=$(tls_ca_cert_path_in_container)

# Published host ports
RETICORA_FRONTEND_PORT=$RETICORA_FRONTEND_PORT
RETICORA_SERVER_PORT=$RETICORA_SERVER_PORT
RETICORA_KEYCLOAK_PORT=$RETICORA_KEYCLOAK_PORT
# REST API and Keycloak (plain HTTP, admin console included) are published
# on 127.0.0.1 only; on all interfaces (true, bind 0.0.0.0) only in an
# installation without a TLS domain. Derived on every run from
# RETICORA_TLS_DOMAIN (COL-08, docs/betrieb/firewall-matrix.md).
RETICORA_PUBLISH_DIRECT_PORTS=${RETICORA_PUBLISH_DIRECT_PORTS:-false}
RETICORA_DIRECT_PORTS_BIND=${RETICORA_DIRECT_PORTS_BIND:-127.0.0.1}

# Names on the server certificate of the collector NATS endpoint (nats-edge,
# mTLS on 4222). A collector connects to tls://<host of its server URL>:4222,
# the URL of its enrollment command, so the list must name that host; the
# first entry is the endpoint the installer shows. Derived on every run from
# RETICORA_TLS_DOMAIN, the host of RETICORA_PUBLIC_BASE_URL and, for an IP or
# localhost installation, the host IP (RETICORA_HOST_IP); names added here
# are kept (a name that is not derived is dropped by removing it here). The
# server reissues the certificate when the list changes
# (docs/betrieb/firewall-matrix.md).
RETICORA_NATS_EDGE_HOSTS=${RETICORA_NATS_EDGE_HOSTS:-}

# Registry and release of the signed images (E-69: the default; both empty
# when the images were built from this checkout with --build-from-source)
RETICORA_IMAGE_REGISTRY=$RETICORA_IMAGE_REGISTRY
RETICORA_IMAGE_TAG=$RETICORA_IMAGE_TAG
# Signed release images, pinned by the digest that cosign verified (set by the
# installer in the release mode; empty = images built from this checkout)
RETICORA_SERVER_IMAGE=${RETICORA_SERVER_IMAGE:-}
RETICORA_FRONTEND_IMAGE=${RETICORA_FRONTEND_IMAGE:-}
RETICORA_POSTGRES_IMAGE=${RETICORA_POSTGRES_IMAGE:-}

# Container image garbage collection
RETICORA_GC_KEEP_IMAGES=$RETICORA_GC_KEEP_IMAGES
EOF
    # Before the merge the existing .env is kept as .env.previous — unless the
    # file of an earlier run is still there: it holds the values Keycloak got
    # last, e.g. the old admin password when .env was edited for a rotation.
    if [ -f "$ENV_FILE" ] && [ ! -f "$ENV_FILE.previous" ]; then
        save_env_previous
    fi
    merge_env_extras "$tmp"
    mv "$tmp" "$ENV_FILE"
    chmod 600 "$ENV_FILE"
}

# env_set <key> <value> — replaces the line <key>=… that write_env wrote
# (the image digests verified after write_env).
env_set() {
    local tmp
    tmp="$(mktemp)"
    if ! awk -v k="$1" -v v="$2" 'index($0, k "=") == 1 { print k "=" v; found = 1; next } { print }
        END { if (!found) print k "=" v }' "$ENV_FILE" > "$tmp" || ! mv "$tmp" "$ENV_FILE"; then
        rm -f "$tmp"
        die "Could not update $1 in $ENV_FILE."
    fi
    chmod 600 "$ENV_FILE"
}

# merge_env_extras <new env file> — appends every KEY=value of the existing
# .env that the installer does not write itself (e.g. RETICORA_HTTP_BIND), so
# a re-run merges instead of dropping additional configuration (INS-05). The
# COMPOSE_FILE of the json-file log rotation is the installer's own:
# configure_log_retention sets it again when the host still needs it.
merge_env_extras() {
    local new="$1" line key extras=()
    [ -f "$ENV_FILE" ] || return 0
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in ''|'#'*) continue ;; esac
        key="${line%%=*}"
        [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
        grep -q "^${key}=" "$new" && continue
        [ "$key" = COMPOSE_FILE ] && names_log_rotation "${line#*=}" && continue
        extras+=("$line")
    done < "$ENV_FILE"
    [ "${#extras[@]}" -gt 0 ] || return 0
    {
        printf '\n# Additional settings kept from the previous .env\n'
        printf '%s\n' "${extras[@]}"
    } >> "$new"
}

# ─── Preflight checks ─────────────────────────────────────────────────────────
preflight() {
    info "Checking prerequisites …"

    check_platform

    # Tools the installer itself needs; missing ones are installed automatically.
    ensure_command curl curl ca-certificates
    ensure_command openssl openssl
    # Compares the rendered realm with the running Keycloak on a re-run.
    ensure_command jq jq

    detect_container_tooling
    success "Container tooling: ${COMPOSE[*]}"
    check_sizing cloud "$($DOCKER info --format '{{.DockerRootDir}}' 2>/dev/null || printf '/var/lib/docker')"

    if [ ! -f "$COMPOSE_FILE" ]; then
        die "Compose file not found at $COMPOSE_FILE — run this script from a full checkout, or use ./install.sh which downloads the sources for you."
    fi

    if [ "$BUILD_FROM_SOURCE" = 1 ] && [ ! -f "$SCRIPT_DIR/backend/Dockerfile" ]; then
        warn "backend/Dockerfile not found; the image build of --build-from-source will fail (it needs a full checkout)."
    fi
}

# ─── Interactive configuration ────────────────────────────────────────────────
collect_config() {
    echo
    printf '%s%sReticora CMDB — central cloud installation%s\n' "$C_BOLD" "$C_BLUE" "$C_RESET"
    echo "The installer now asks for all important settings. Suggested defaults"
    echo "can be accepted with Enter; existing values are reused on re-runs."
    echo

    # ── Secrets (generated by default; reused on re-run) ──
    local def_db_pw def_s3_key def_s3_secret def_kc_pw def_kc_db_pw def_client_secret def_master_key
    def_db_pw="$(env_get RETICORA_DB_PASSWORD || true)"
    def_db_pw="${def_db_pw:-$(openssl rand -hex 16)}"
    def_s3_key="$(env_get RETICORA_S3_ACCESS_KEY || true)"
    def_s3_key="${def_s3_key:-reticora}"
    def_s3_secret="$(env_get RETICORA_S3_SECRET_KEY || true)"
    def_s3_secret="${def_s3_secret:-$(openssl rand -hex 16)}"
    def_kc_pw="$(env_get KEYCLOAK_ADMIN_PASSWORD || true)"
    def_kc_pw="${def_kc_pw:-$(openssl rand -hex 16)}"
    def_kc_db_pw="$(env_get KEYCLOAK_DB_PASSWORD || true)"
    def_kc_db_pw="${def_kc_db_pw:-$(openssl rand -hex 16)}"
    def_client_secret="$(env_get RETICORA_OIDC_CLIENT_SECRET || true)"
    def_client_secret="${def_client_secret:-$(openssl rand -hex 24)}"
    def_master_key="$(env_get RETICORA_MASTER_KEY || true)"
    def_master_key="${def_master_key:-$(openssl rand -base64 32)}"
    # Passphrase of the encrypted pgBackRest repositories (WP-100). It is
    # generated once and never asked for: changing it makes the existing
    # backups unreadable.
    RETICORA_BACKUP_CIPHER_PASS="$(env_get RETICORA_BACKUP_CIPHER_PASS || true)"
    RETICORA_BACKUP_CIPHER_PASS="${RETICORA_BACKUP_CIPHER_PASS:-$(openssl rand -hex 32)}"
    # Password of the runtime login reticora_runtime (TEN-03). Generated once
    # and never asked for; an installation from before this login gets it on
    # the next run, and a changed value in .env rotates the password.
    RETICORA_DB_APP_PASSWORD="$(env_get RETICORA_DB_APP_PASSWORD || true)"
    RETICORA_DB_APP_PASSWORD="${RETICORA_DB_APP_PASSWORD:-$(openssl rand -hex 24)}"
    valid_app_password "$RETICORA_DB_APP_PASSWORD" \
        || die "RETICORA_DB_APP_PASSWORD in $ENV_FILE must be at least 16 characters of A-Z, a-z, 0-9, '.', '_', '~' or '-' (it is part of the server's database URL)."

    ask_validated RETICORA_DB_PASSWORD "Database password (PostgreSQL)" "$def_db_pw" valid_password
    ask RETICORA_S3_ACCESS_KEY "S3/MinIO access key" "$def_s3_key"
    ask_validated RETICORA_S3_SECRET_KEY "S3/MinIO secret key" "$def_s3_secret" valid_password

    local def_kc_admin
    def_kc_admin="$(env_get KEYCLOAK_ADMIN || true)"
    ask KEYCLOAK_ADMIN "Keycloak admin user" "${def_kc_admin:-admin}"
    ask_validated KEYCLOAK_ADMIN_PASSWORD "Keycloak admin password" "$def_kc_pw" valid_password
    ask_validated KEYCLOAK_DB_PASSWORD "Keycloak database password" "$def_kc_db_pw" valid_password

    # The individual administrator of the first organization (AUT-09). The
    # realm ships without any user; the installer creates this one with a
    # random temporary password that is shown once.
    local def_initial_admin
    def_initial_admin="$(env_get RETICORA_INITIAL_ADMIN_EMAIL || true)"
    ask_validated RETICORA_INITIAL_ADMIN_EMAIL "E-mail of the initial Reticora administrator" "${def_initial_admin:-admin@reticora.example}" valid_initial_admin_email

    local def_client_id
    def_client_id="$(env_get RETICORA_OIDC_CLIENT_ID || true)"
    ask RETICORA_OIDC_CLIENT_ID "OIDC client ID" "${def_client_id:-reticora-app}"
    ask RETICORA_OIDC_CLIENT_SECRET "OIDC client secret" "$def_client_secret"

    # The Keycloak admin client of the server (ensure_admin_client) and the
    # key of the signup challenges (E-33): generated once and never asked
    # for, like the runtime login's password; a changed value in .env is
    # applied (the client secret is rotated in Keycloak).
    RETICORA_KEYCLOAK_ADMIN_CLIENT_ID="$(env_get RETICORA_KEYCLOAK_ADMIN_CLIENT_ID || true)"
    RETICORA_KEYCLOAK_ADMIN_CLIENT_ID="${RETICORA_KEYCLOAK_ADMIN_CLIENT_ID:-reticora-server-admin}"
    [[ "$RETICORA_KEYCLOAK_ADMIN_CLIENT_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] \
        || die "RETICORA_KEYCLOAK_ADMIN_CLIENT_ID in $ENV_FILE must consist of letters, digits, '.', '_' and '-' (default reticora-server-admin)."
    [ "$RETICORA_KEYCLOAK_ADMIN_CLIENT_ID" != "$RETICORA_OIDC_CLIENT_ID" ] \
        || die "RETICORA_KEYCLOAK_ADMIN_CLIENT_ID must name a client of its own, not the login client $RETICORA_OIDC_CLIENT_ID (default reticora-server-admin)."
    RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET="$(env_get RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET || true)"
    RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET="${RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET:-$(openssl rand -hex 24)}"
    { [ "${#RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET}" -ge 16 ] && valid_env_value "$RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET"; } \
        || die "RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET in $ENV_FILE must have at least 16 characters without blanks, quotes, '\\', '\`' or '\$' (remove it to generate a new one)."
    RETICORA_SIGNUP_CAPTCHA_KEY="$(env_get RETICORA_SIGNUP_CAPTCHA_KEY || true)"
    RETICORA_SIGNUP_CAPTCHA_KEY="${RETICORA_SIGNUP_CAPTCHA_KEY:-$(openssl rand -hex 32)}"
    { [ "${#RETICORA_SIGNUP_CAPTCHA_KEY}" -ge 32 ] && valid_env_value "$RETICORA_SIGNUP_CAPTCHA_KEY"; } \
        || die "RETICORA_SIGNUP_CAPTCHA_KEY in $ENV_FILE must have at least 32 characters without blanks, quotes, '\\', '\`' or '\$' (remove it to generate a new one)."

    if is_tty; then
        ask_secret RETICORA_MASTER_KEY "Master key for credential encryption (base64, 32 bytes)" "$def_master_key"
    else
        RETICORA_MASTER_KEY="$def_master_key"
    fi

    # ── Public URLs / ports ──
    local def_base def_issuer def_redirect def_port
    def_base="$(env_get RETICORA_PUBLIC_BASE_URL || true)"
    def_base="${def_base:-http://localhost:3000}"
    ask_validated RETICORA_PUBLIC_BASE_URL "Public base URL of the web UI (e.g. https://cmdb.example.com)" "$def_base" valid_url explain_url_error
    # docker-compose.yml appends the paths of the mail links to it
    # (/onboarding, /invitations/accept): no trailing slash.
    RETICORA_PUBLIC_BASE_URL="${RETICORA_PUBLIC_BASE_URL%/}"

    # The issuer URL is handed to the browser as the Keycloak authorization
    # endpoint, so it must be reachable from the users' machines — a
    # "localhost" issuer only works when the browser runs on the server itself.
    # Derive the default from the public base URL instead of silently falling
    # back to localhost, which breaks sign-in for every remote user.
    def_issuer="$(env_get RETICORA_OIDC_ISSUER_URL || true)"
    if [ -z "$def_issuer" ]; then
        local issuer_host issuer_scheme def_kc_port
        issuer_scheme="$(printf '%s' "$RETICORA_PUBLIC_BASE_URL" | sed -E 's|^(https?)://.*$|\1|')"
        issuer_host="$(printf '%s' "$RETICORA_PUBLIC_BASE_URL" | sed -E 's|^https?://||; s|[:/].*$||')"
        def_kc_port="$(env_get RETICORA_KEYCLOAK_PORT || true)"
        case "$issuer_host" in
            localhost|127.*|0.0.0.0)
                # Loopback install: the backend container cannot reach a
                # localhost issuer (INS-02), so Keycloak is addressed via the
                # host's primary IP — reachable from the host, the containers
                # and browsers on the network alike.
                ensure_command ip iproute2
                def_issuer="${issuer_scheme}://$(host_primary_ip || true):${def_kc_port:-8180}/realms/reticora"
                case "$def_issuer" in
                    *://:*) def_issuer="${issuer_scheme}://${issuer_host}:${def_kc_port:-8180}/realms/reticora" ;;
                esac
                ;;
            [0-9]*.[0-9]*.[0-9]*.[0-9]*)
                # Literal-IP install: Keycloak is published directly on
                # its host port (8180 unless already configured in .env).
                def_issuer="${issuer_scheme}://${issuer_host}:${def_kc_port:-8180}/realms/reticora"
                ;;
            *)
                # Public hostname: Keycloak is expected to be reachable on the
                # same public address (e.g. behind the reverse proxy).
                def_issuer="${RETICORA_PUBLIC_BASE_URL%/}/realms/reticora"
                ;;
        esac
    fi
    ask_validated RETICORA_OIDC_ISSUER_URL "OIDC issuer URL (Keycloak realm; must be reachable from users' browsers)" "$def_issuer" valid_url explain_url_error

    def_redirect="$(env_get RETICORA_OIDC_REDIRECT_URL || true)"
    def_redirect="${def_redirect:-${RETICORA_PUBLIC_BASE_URL%/}/auth/callback}"
    ask_validated RETICORA_OIDC_REDIRECT_URL "OIDC redirect URL (backend callback)" "$def_redirect" valid_url explain_url_error

    # ── HTTPS / Let's Encrypt (optional) ──
    # A public domain enables automatic TLS via nginx + Certbot: the installer
    # bootstraps a self-signed certificate so nginx can start, then issues the
    # real certificate with the ACME http-01 challenge and reloads nginx.
    local def_tls_domain
    def_tls_domain="$(env_get RETICORA_TLS_DOMAIN || true)"
    if [ -z "$def_tls_domain" ]; then
        # Derive a suggestion from the public base URL when its host is a
        # DNS name (Let's Encrypt requires one: no localhost, no literal IP).
        def_tls_domain="$(printf '%s' "$RETICORA_PUBLIC_BASE_URL" | sed -E 's|^https?://||; s|[:/].*$||')"
        valid_domain "$def_tls_domain" || def_tls_domain=""
    fi
    if is_tty; then
        echo
        echo "HTTPS (recommended for production): enter a public domain name to"
        echo "automatically obtain a free Let's Encrypt certificate (requires ports"
        echo "80/443 reachable from the internet; the UI is then served at"
        echo "https://<domain> on port 443). Leave empty to keep plain HTTP."
        ask_validated RETICORA_TLS_DOMAIN "Public domain for HTTPS (empty = no TLS, optional)" "$def_tls_domain" valid_optional_domain \
            explain_domain_error
    elif [ -n "$def_tls_domain" ] && valid_domain "$def_tls_domain"; then
        # Non-interactive run: reuse the configured domain (from .env, or
        # derived above from an https:// public base URL with a DNS name).
        RETICORA_TLS_DOMAIN="$def_tls_domain"
    else
        RETICORA_TLS_DOMAIN=""
    fi
    # With a TLS domain nginx serves the UI only at https://<domain>; port 80
    # redirects everything but the ACME challenge there, and Keycloak behind
    # the /realms/ proxy announces https://<domain>/realms/reticora
    # (KC_PROXY_HEADERS=xforwarded). An http:// URL of that host never answers itself (the issuer
    # check failed with "announces itself as '<none>'"): base URL, OIDC issuer
    # and redirect URL follow to https://.
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        local var url moved=()
        for var in RETICORA_PUBLIC_BASE_URL RETICORA_OIDC_ISSUER_URL RETICORA_OIDC_REDIRECT_URL; do
            url="$(tls_domain_url "${!var}")"
            [ "$url" = "${!var}" ] && continue
            printf -v "$var" '%s' "$url"
            moved+=("$var=$url")
        done
        [ "${#moved[@]}" -eq 0 ] \
            || info "HTTPS for $RETICORA_TLS_DOMAIN: http://$RETICORA_TLS_DOMAIN only redirects to HTTPS, so ${moved[*]}"
    fi

    local def_cert_email
    def_cert_email="$(env_get RETICORA_CERT_EMAIL || true)"
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        ask_validated RETICORA_CERT_EMAIL "E-mail for Let's Encrypt expiry notices (optional)" "$def_cert_email" valid_optional_email
    else
        RETICORA_CERT_EMAIL="$def_cert_email"
    fi

    # ACME server and the certbot container's CA bundle for it (WP-420): from
    # the environment, otherwise from .env; never asked for. Empty means
    # Let's Encrypt production (certbot's default) and the public CAs of the
    # certbot image. Through .env both reach the certbot service, which
    # requests and renews the certificate (docker-compose.yml).
    RETICORA_ACME_SERVER="${ACME_SERVER_OVERRIDE:-$(env_get RETICORA_ACME_SERVER || true)}"
    valid_optional_acme_server "$RETICORA_ACME_SERVER" \
        || die "RETICORA_ACME_SERVER ('$RETICORA_ACME_SERVER') must be the https:// directory URL of an ACME server, e.g. https://acme-staging-v02.api.letsencrypt.org/directory (see docs/troubleshooting-installation.md, 'Staging and test CAs')."
    RETICORA_ACME_CA_BUNDLE="${ACME_CA_BUNDLE_OVERRIDE:-$(env_get RETICORA_ACME_CA_BUNDLE || true)}"
    valid_optional_ca_bundle "$RETICORA_ACME_CA_BUNDLE" \
        || die "RETICORA_ACME_CA_BUNDLE ('$RETICORA_ACME_CA_BUNDLE') must be the absolute path of a readable PEM certificate file (letters, digits, '.', '_', '+', '-' and '/' only; see docs/troubleshooting-installation.md, 'Staging and test CAs')."

    # With TLS enabled, the UI is served on the standard ports: HTTPS on 443
    # (RETICORA_HTTPS_PORT) and plain HTTP on 80, which must be reachable from
    # the internet for the Let's Encrypt ACME http-01 challenge and redirects
    # everything else to HTTPS. Point RETICORA_FRONTEND_PORT (the host port
    # published for the container's plain-HTTP port) at 80 then; RETICORA_
    # HTTP_BIND in .env can bind it elsewhere when port 80 is already taken
    # (e.g. by an external reverse proxy terminating TLS in front).
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        def_port="$(env_get RETICORA_FRONTEND_PORT || true)"
        if [ -n "$def_port" ] && [ "$def_port" != "80" ]; then
            warn "TLS is enabled: the plain-HTTP UI port is redirected from $def_port to 80 (ACME http-01 challenge + HTTPS redirect)."
        fi
        RETICORA_FRONTEND_PORT=80
    else
        def_port="$(env_get RETICORA_FRONTEND_PORT || true)"
        ask_validated RETICORA_FRONTEND_PORT "Host port for the web UI" "${def_port:-3000}" valid_port
    fi
    def_port="$(env_get RETICORA_SERVER_PORT || true)"
    ask_validated RETICORA_SERVER_PORT "Host port for the REST API" "${def_port:-8080}" valid_port
    def_port="$(env_get RETICORA_KEYCLOAK_PORT || true)"
    ask_validated RETICORA_KEYCLOAK_PORT "Host port for Keycloak" "${def_port:-8180}" valid_port
    select_direct_ports
    nats_edge_hosts

    # ── Outgoing e-mail (SMTP, optional) ──
    collect_smtp_config

    # ── Images (E-69, Option 1) ──
    # Default: the signed release images, verified and pinned in
    # provide_images; the release is chosen in select_release.
    # --build-from-source builds them from this checkout instead.
    if [ "$BUILD_FROM_SOURCE" = 1 ]; then
        RETICORA_IMAGE_REGISTRY=""
        RETICORA_IMAGE_TAG=""
    else
        local def_registry
        def_registry="${IMAGE_REGISTRY_OVERRIDE:-$(env_get RETICORA_IMAGE_REGISTRY || true)}"
        ask RETICORA_IMAGE_REGISTRY "Registry of the signed release images" "${def_registry:-$RELEASE_REGISTRY}"
        RETICORA_IMAGE_REGISTRY="$(trim "$RETICORA_IMAGE_REGISTRY")"
        RETICORA_IMAGE_REGISTRY="${RETICORA_IMAGE_REGISTRY:-$RELEASE_REGISTRY}"
        RETICORA_IMAGE_TAG="$RELEASE"
    fi
    # Set by provide_images from the verified digests (release mode only); a
    # pin of an earlier run is never reused unverified.
    RETICORA_SERVER_IMAGE=""
    RETICORA_FRONTEND_IMAGE=""
    RETICORA_POSTGRES_IMAGE=""

    RETICORA_GC_KEEP_IMAGES="$(env_get RETICORA_GC_KEEP_IMAGES || true)"
    RETICORA_GC_KEEP_IMAGES="${RETICORA_GC_KEEP_IMAGES:-3}"
}

# select_direct_ports — the REST API (RETICORA_SERVER_PORT) and Keycloak with
# its admin console (RETICORA_KEYCLOAK_PORT) speak plain HTTP. With a TLS
# domain they stay on 127.0.0.1 (RETICORA_DIRECT_PORTS_BIND, also the default
# of docker-compose.yml): from outside only 80/443 (frontend with its /api/
# and /realms/ proxies) and 4222 (nats-edge) are open (COL-08,
# docs/betrieb/firewall-matrix.md). An installation without a TLS domain (IP
# address or localhost) addresses its OIDC issuer on the Keycloak port of the
# host IP, which the browsers and the backend container reach only when the
# port is published on all interfaces: there the installer opts in with
# RETICORA_PUBLISH_DIRECT_PORTS=true and warns. Derived on every run; a value
# in .env is not taken over.
select_direct_ports() {
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        RETICORA_PUBLISH_DIRECT_PORTS=false
        RETICORA_DIRECT_PORTS_BIND=127.0.0.1
        case "$RETICORA_OIDC_ISSUER_URL" in
            *://*:"$RETICORA_KEYCLOAK_PORT"/*)
                warn "RETICORA_OIDC_ISSUER_URL ($RETICORA_OIDC_ISSUER_URL) names the Keycloak port $RETICORA_KEYCLOAK_PORT, which is bound to 127.0.0.1 with a TLS domain: the browsers and the backend container cannot reach it. Use https://$RETICORA_TLS_DOMAIN/realms/reticora (frontend proxy)."
                ;;
        esac
        return 0
    fi
    RETICORA_PUBLISH_DIRECT_PORTS=true
    RETICORA_DIRECT_PORTS_BIND=0.0.0.0
    warn "Installation without a TLS domain: the REST API (port $RETICORA_SERVER_PORT) and Keycloak including its admin console (port $RETICORA_KEYCLOAK_PORT) are published on all interfaces without TLS (RETICORA_PUBLISH_DIRECT_PORTS=true; the OIDC issuer of an IP installation is served on the Keycloak port of the host IP). Allow both ports only from trusted networks in the host firewall, or install with a TLS domain (docs/betrieb/firewall-matrix.md)."
}

# ─── Collector NATS endpoint (COL-08) ─────────────────────────────────────────
# nats_edge_hosts — RETICORA_NATS_EDGE_HOSTS, the names on the server
# certificate of nats-edge (mTLS, port 4222). A collector connects to
# tls://<host of its server URL>:4222, and the enrollment command of the
# Discovery page names the origin of the browser, i.e. the public base URL;
# a certificate without that name fails the handshake ("doesn't contain any
# IP SANs", "certificate is valid for …, not …") and the collector delivers
# nothing. Derived on every run, in this order and without duplicates: the
# TLS domain, the host of RETICORA_PUBLIC_BASE_URL (not a loopback name,
# without a trailing dot; a host that is no valid certificate name, e.g.
# with an underscore or non-ASCII characters, is skipped with a warning) and,
# for a localhost or IP installation, the host IP (RETICORA_HOST_IP,
# otherwise the source address of the default route; behind NAT the base
# URL names the public address), then the names of the previous .env value
# (additions of the operator are kept), last nats-edge (the server's own
# connections) and localhost. RETICORA_NATS_EDGE_HOSTS in the environment
# replaces the derived names. The server reissues the certificate when the
# list changes; collectors also accept the fixed name reticora-collector-nats
# that the server adds to every certificate.
nats_edge_hosts() {
    local candidates=() hosts=() h base seen=","
    if [ -n "$NATS_EDGE_HOSTS_OVERRIDE" ]; then
        IFS=',' read -r -a candidates <<< "$NATS_EDGE_HOSTS_OVERRIDE"
    else
        [ -z "${RETICORA_TLS_DOMAIN:-}" ] || candidates+=("$RETICORA_TLS_DOMAIN")
        base="$(url_host "${RETICORA_PUBLIC_BASE_URL:-}")"
        # An FQDN with a trailing dot is the same certificate name (the
        # server's normalizeEdgeHost and x509 host matching drop it).
        base="${base%.}"
        if host_is_loopback "$base"; then
            :
        elif valid_host "$base"; then
            candidates+=("$base")
        else
            # The base URL passed valid_url, which earlier releases installed
            # with: a name the operator did not give as an edge host must not
            # stop the run.
            warn "The host '$base' of RETICORA_PUBLIC_BASE_URL cannot be named on the certificate of the collector NATS endpoint as written (underscore or non-ASCII characters); it is not added to RETICORA_NATS_EDGE_HOSTS. Set RETICORA_NATS_EDGE_HOSTS to the names collectors connect to, e.g. the punycode form of an internationalized domain name (docs/betrieb/firewall-matrix.md)."
        fi
        if host_is_loopback "$base" || host_is_ip "$base"; then
            h="$(host_primary_ip 2>/dev/null || true)"
            [ -z "$h" ] || candidates+=("$h")
        fi
        local previous=()
        IFS=',' read -r -a previous <<< "$(env_get RETICORA_NATS_EDGE_HOSTS || true)"
        candidates+=("${previous[@]}")
    fi
    for h in "${candidates[@]}"; do
        h="$(trim "$h")"
        h="${h#\[}"
        h="${h%\]}"
        h="${h,,}"
        case "$h" in ''|nats-edge|localhost) continue ;; esac
        valid_host "$h" \
            || die "RETICORA_NATS_EDGE_HOSTS: '$h' is not a host name or IP address (comma-separated list of the names collectors connect to; docs/betrieb/firewall-matrix.md)."
        [[ "$seen" == *",$h,"* ]] && continue
        seen="$seen$h,"
        hosts+=("$h")
    done
    hosts+=(nats-edge localhost)
    RETICORA_NATS_EDGE_HOSTS="$(IFS=','; printf '%s' "${hosts[*]}")"
}

# collector_endpoint — the collector NATS endpoint the installation announces:
# tls://<first name of RETICORA_NATS_EDGE_HOSTS>:<published port>.
collector_endpoint() {
    local host="${RETICORA_NATS_EDGE_HOSTS:-localhost}" port
    host="${host%%,*}"
    port="${RETICORA_NATS_EDGE_PORT:-$(env_get RETICORA_NATS_EDGE_PORT 2>/dev/null || true)}"
    [[ "$host" != *:* ]] || host="[$host]"
    printf 'tls://%s:%s' "$host" "${port:-4222}"
}

# ─── Outgoing e-mail (SMTP, TEC-13) ───────────────────────────────────────────
SMTP_TROUBLESHOOTING="see docs/troubleshooting-installation.md, 'Self-signup answers 503'"

# smtp_default_port <tls-mode> — 465 for implicit TLS, 587 (submission) for
# starttls and none; the server's defaults as well (notify.SMTPConfig).
smtp_default_port() { if [ "$1" = tls ]; then printf 465; else printf 587; fi; }

# smtp_port_for_mode <port> <mode-of-port> <mode> — the SMTP port for the TLS
# mode <mode>: an empty port, or the default port of <mode-of-port> after the
# mode changed, becomes the default of <mode>; any other port is kept. A
# .env written by a run without SMTP holds the STARTTLS default 587, and a
# later run with RETICORA_SMTP_TLS=tls would otherwise try implicit TLS on
# 587: the TCP pre-check passes, /readyz does not.
smtp_port_for_mode() {
    if [ -z "$1" ] || { [ "$2" != "$3" ] && [ "$1" = "$(smtp_default_port "$2")" ]; }; then
        smtp_default_port "$3"
    else
        printf '%s' "$1"
    fi
}

# collect_smtp_config — the central SMTP server of the installation: from the
# environment, otherwise from .env; an interactive run asks for it with these
# as defaults. The server sends the verification e-mail of the self-signup,
# invitations and the notification mails of the central_smtp channels over
# it, and apply_realm_smtp gives it to Keycloak's realm. An empty host keeps
# e-mail off: self-signup then stays disabled, because its e-mail
# verification is mandatory. The other values are kept for a later run.
# TLS mode none asks for no user name and password: the server never sends
# a login unencrypted (validate_smtp_config).
collect_smtp_config() {
    local def_host def_port def_tls def_user def_pw def_from port_tls port
    def_host="${SMTP_HOST_OVERRIDE:-$(env_get RETICORA_SMTP_HOST || true)}"
    def_port="${SMTP_PORT_OVERRIDE:-$(env_get RETICORA_SMTP_PORT || true)}"
    def_tls="${SMTP_TLS_OVERRIDE:-$(env_get RETICORA_SMTP_TLS || true)}"
    def_user="${SMTP_USERNAME_OVERRIDE:-$(env_get RETICORA_SMTP_USERNAME || true)}"
    def_pw="${SMTP_PASSWORD_OVERRIDE:-$(env_get RETICORA_SMTP_PASSWORD || true)}"
    def_from="${SMTP_FROM_OVERRIDE:-$(env_get RETICORA_SMTP_FROM || true)}"
    RETICORA_SMTP_HOST="$def_host"
    RETICORA_SMTP_TLS="${def_tls:-starttls}"
    RETICORA_SMTP_PORT="$def_port"
    RETICORA_SMTP_USERNAME="$def_user"
    RETICORA_SMTP_PASSWORD="$def_pw"
    RETICORA_SMTP_FROM="$def_from"
    # The TLS mode the port was chosen for: a port from .env belongs to the
    # mode in .env (written as starttls without one). A port from the
    # environment (SMTP_PORT_OVERRIDE) is explicit and kept for every mode.
    port_tls="$(env_get RETICORA_SMTP_TLS || true)"
    port_tls="${port_tls:-starttls}"
    while true; do
        if is_tty; then
            echo
            echo "Outgoing e-mail (SMTP, optional): the server sends the verification e-mail"
            echo "of the self-signup (${RETICORA_PUBLIC_BASE_URL}/onboarding), invitations and"
            echo "notification mails over this relay. Without it self-signup stays disabled"
            echo "(the e-mail verification is mandatory) and no e-mail is sent. Leave the host"
            echo "empty to keep e-mail off; '-' removes a configured server."
            ask_validated RETICORA_SMTP_HOST "SMTP server host name or IP (empty = no e-mail, optional)" "$RETICORA_SMTP_HOST" \
                valid_smtp_host_answer explain_smtp_host_error
            [ "$RETICORA_SMTP_HOST" != - ] || RETICORA_SMTP_HOST=""
            if [ -n "$RETICORA_SMTP_HOST" ]; then
                while true; do
                    ask_validated RETICORA_SMTP_TLS "SMTP TLS mode: starttls (port 587), tls (implicit TLS, port 465) or none (unencrypted, no login)" \
                        "$RETICORA_SMTP_TLS" valid_smtp_tls
                    [ "$RETICORA_SMTP_TLS" = none ] && [ -n "$RETICORA_SMTP_USERNAME" ] || break
                    # A user name of .env or the environment: the server never
                    # sends it unencrypted (validate_smtp_config).
                    warn "TLS mode none sends no login: the server (Go net/smtp) never authenticates over an unencrypted connection, so the SMTP user name '$RETICORA_SMTP_USERNAME' needs starttls or tls."
                    if confirm "Use TLS mode none without authentication (removes the SMTP user name and password)?" "no"; then
                        RETICORA_SMTP_USERNAME=""
                        RETICORA_SMTP_PASSWORD=""
                        break
                    fi
                    RETICORA_SMTP_TLS=starttls
                done
                [ -n "$SMTP_PORT_OVERRIDE" ] \
                    || RETICORA_SMTP_PORT="$(smtp_port_for_mode "$RETICORA_SMTP_PORT" "$port_tls" "$RETICORA_SMTP_TLS")"
                ask_validated RETICORA_SMTP_PORT "SMTP port" "$RETICORA_SMTP_PORT" valid_port
                port_tls="$RETICORA_SMTP_TLS"
                if [ "$RETICORA_SMTP_TLS" = none ]; then
                    # No login over an unencrypted connection: no user name
                    # and password to ask for.
                    RETICORA_SMTP_USERNAME=""
                    RETICORA_SMTP_PASSWORD=""
                else
                    ask_validated RETICORA_SMTP_USERNAME "SMTP user name (empty = no authentication, optional; '-' removes it)" \
                        "$RETICORA_SMTP_USERNAME" valid_smtp_username_answer
                    [ "$RETICORA_SMTP_USERNAME" != - ] || RETICORA_SMTP_USERNAME=""
                    if [ -n "$RETICORA_SMTP_USERNAME" ]; then
                        while true; do
                            ask_optional_secret RETICORA_SMTP_PASSWORD "SMTP password" "$RETICORA_SMTP_PASSWORD"
                            valid_env_value "$RETICORA_SMTP_PASSWORD" && break
                            warn "Invalid input — the password is written unquoted to $ENV_FILE: no blanks, quotes, '\\', '\`' or '\$'."
                            RETICORA_SMTP_PASSWORD=""
                        done
                    else
                        RETICORA_SMTP_PASSWORD=""
                    fi
                fi
                ask_validated RETICORA_SMTP_FROM "Sender address (From), e.g. Reticora CMDB <noreply@example.com>" \
                    "$RETICORA_SMTP_FROM" valid_smtp_from explain_smtp_from_error
            fi
        fi
        if [ -z "$SMTP_PORT_OVERRIDE" ]; then
            port="$(smtp_port_for_mode "$RETICORA_SMTP_PORT" "$port_tls" "$RETICORA_SMTP_TLS")"
            [ -z "$RETICORA_SMTP_PORT" ] || [ "$port" = "$RETICORA_SMTP_PORT" ] || [ -z "$RETICORA_SMTP_HOST" ] \
                || info "SMTP port $port for TLS mode $RETICORA_SMTP_TLS (the port $RETICORA_SMTP_PORT in $ENV_FILE is the default of $port_tls; set RETICORA_SMTP_PORT to keep another one)"
            RETICORA_SMTP_PORT="$port"
            port_tls="$RETICORA_SMTP_TLS"
        fi
        [ -n "$RETICORA_SMTP_HOST" ] || return 0
        validate_smtp_config
        smtp_relay_check && return 0
        if [ "$SMTP_CHECK_PHASE" = auth ]; then
            # /readyz does not log in: the server starts, the e-mails fail.
            warn "The SMTP server $RETICORA_SMTP_HOST:$RETICORA_SMTP_PORT $SMTP_CHECK_PROBLEM. The server still starts (/readyz does not log in), but every e-mail fails until RETICORA_SMTP_USERNAME and RETICORA_SMTP_PASSWORD are correct ($SMTP_TROUBLESHOOTING)."
            if is_tty && confirm "Enter the SMTP settings again?" "yes"; then
                continue
            fi
            return 0
        fi
        is_tty || die "The SMTP server $RETICORA_SMTP_HOST:$RETICORA_SMTP_PORT $SMTP_CHECK_PROBLEM. With RETICORA_SMTP_HOST set the server's readiness (/readyz) depends on it, so the installation would end in 'Reticora server did not become healthy'. Check host, port, TLS mode and the outbound firewall, or install without e-mail: remove RETICORA_SMTP_HOST from the environment and from $ENV_FILE ($SMTP_TROUBLESHOOTING)."
        warn "The SMTP server $RETICORA_SMTP_HOST:$RETICORA_SMTP_PORT $SMTP_CHECK_PROBLEM. With RETICORA_SMTP_HOST set the server's readiness (/readyz) depends on it: the installation would end in 'Reticora server did not become healthy'."
        if confirm "Continue without e-mail? (self-signup and e-mail notifications stay off until a re-run sets a reachable SMTP server)" "yes"; then
            RETICORA_SMTP_HOST=""
            warn "Continuing without SMTP: self-signup is disabled; re-run the installer and enter a reachable SMTP server to enable it."
            return 0
        fi
        confirm "Enter the SMTP settings again?" "yes" \
            || die "Aborted: make $RETICORA_SMTP_HOST:$RETICORA_SMTP_PORT reachable from this host, or continue without e-mail, and re-run the installer."
    done
}

# validate_smtp_config — the SMTP settings the server accepts (config.Validate,
# notify.SMTPConfig) and that fit unquoted into .env; no secret is printed.
validate_smtp_config() {
    valid_optional_smtp_host "$RETICORA_SMTP_HOST" \
        || die "RETICORA_SMTP_HOST ('$RETICORA_SMTP_HOST') must be the host name or IP address of the SMTP server, without port and not a loopback address (inside the server container that is the container itself) ($SMTP_TROUBLESHOOTING)."
    valid_port "$RETICORA_SMTP_PORT" \
        || die "RETICORA_SMTP_PORT ('$RETICORA_SMTP_PORT') must be a port number between 1 and 65535 (587 STARTTLS, 465 implicit TLS)."
    valid_smtp_tls "$RETICORA_SMTP_TLS" \
        || die "RETICORA_SMTP_TLS ('$RETICORA_SMTP_TLS') must be starttls, tls or none."
    valid_env_value "$RETICORA_SMTP_USERNAME" \
        || die "RETICORA_SMTP_USERNAME must not contain blanks, quotes, '\\', '\`' or '\$' (it is written unquoted to $ENV_FILE)."
    valid_env_value "$RETICORA_SMTP_PASSWORD" \
        || die "RETICORA_SMTP_PASSWORD must not contain blanks, quotes, '\\', '\`' or '\$' (it is written unquoted to $ENV_FILE)."
    [ -z "$RETICORA_SMTP_PASSWORD" ] || [ -n "$RETICORA_SMTP_USERNAME" ] \
        || die "RETICORA_SMTP_PASSWORD needs RETICORA_SMTP_USERNAME: the server authenticates with both or none."
    # Go's net/smtp sends no login over an unencrypted connection to a host
    # other than localhost, and a loopback SMTP host is refused above: every
    # mail would fail at AUTH while /readyz (EHLO only) stays green.
    [ "$RETICORA_SMTP_TLS" != none ] || [ -z "$RETICORA_SMTP_USERNAME" ] \
        || die "RETICORA_SMTP_USERNAME needs RETICORA_SMTP_TLS=starttls or tls: the server (Go net/smtp) never sends a login over an unencrypted connection, so with RETICORA_SMTP_TLS=none every e-mail would fail. Use starttls or tls, or remove RETICORA_SMTP_USERNAME and RETICORA_SMTP_PASSWORD for a relay without authentication ($SMTP_TROUBLESHOOTING)."
    [ -n "$RETICORA_SMTP_FROM" ] \
        || die "RETICORA_SMTP_FROM is required with RETICORA_SMTP_HOST: the sender of the e-mails, e.g. 'Reticora CMDB <noreply@example.com>' ($SMTP_TROUBLESHOOTING)."
    valid_smtp_from "$RETICORA_SMTP_FROM" \
        || die "RETICORA_SMTP_FROM ('$RETICORA_SMTP_FROM') must be an e-mail address or 'Name <address>' that the server accepts (Go net/mail): no , ; : @ [ ] ( ) < > in the name (e.g. 'Reticora Inc.', not 'Reticora, Inc.'), no leading, trailing or double dot in the address, and no quotes, '\$', '#' or '\\' ($SMTP_TROUBLESHOOTING)."
}

# Result of smtp_relay_reachable: on failure the phase (dns, tcp, smtp, tls
# or auth) and the text that completes "The SMTP server <host>:<port> …", on
# success the address the session connected to.
SMTP_CHECK_PHASE=""
SMTP_CHECK_PROBLEM=""
SMTP_CHECK_VIA=""

# smtp_relay_reachable <host> <port> [tls-mode] [user] [password] [curl-option…]
# — opens one SMTP session the way the server's /readyz does
# (server.SMTPReadiness): a TCP connection to one of the addresses <host>
# resolves to (curl tries all of them, IPv6 and IPv4, and falls back between
# them; a single connect with one deadline for the whole list never reached
# the second address when the first one dropped the packets), the 220
# greeting, EHLO, STARTTLS for starttls or implicit TLS for tls with the
# certificate verified for <host> against the system CAs, then NOOP and QUIT:
# no mail is sent. With a user name it also logs in with AUTH PLAIN like the
# server's sender (/readyz itself does not log in); the password reaches curl
# on stdin only, never its command line, and is never printed. Each attempt
# may take RETICORA_SMTP_CHECK_TIMEOUT seconds (default 10; glibc's resolver
# alone waits 5 s for a lost answer) to connect and twice that in all.
# Transient failures (a timeout before the server answered, a refused or
# unreachable connection, a connection lost during the session) are tried up
# to RETICORA_SMTP_CHECK_ATTEMPTS times (default 3),
# RETICORA_SMTP_CHECK_RETRY_DELAY seconds apart (default 2); an unknown host
# name, TLS, certificate, STARTTLS and login failures are not repeated.
smtp_relay_reachable() {
    local host="$1" port="$2" mode="${3:-starttls}" user="${4:-}" pw="${5:-}"
    shift "$(( $# < 5 ? $# : 5 ))"
    local t="${RETICORA_SMTP_CHECK_TIMEOUT:-10}" attempts="${RETICORA_SMTP_CHECK_ATTEMPTS:-3}"
    local delay="${RETICORA_SMTP_CHECK_RETRY_DELAY:-2}" url=smtp attempt=1 rc out ip err transient
    [[ "$t" =~ ^[1-9][0-9]*$ ]] || t=10
    [[ "$attempts" =~ ^[1-9][0-9]*$ ]] || attempts=3
    [[ "$delay" =~ ^[0-9]+$ ]] || delay=2
    # -q first: no ~/.curlrc (e.g. 'insecure'); no proxy: the server
    # connects directly.
    local -a opts=(-q --silent --globoff --noproxy '*' --connect-timeout "$t" --max-time "$((2 * t))"
        --request NOOP --output /dev/null --write-out '%{remote_ip}|%{errormsg}' --config -)
    case "$mode" in
        tls) url=smtps ;;
        starttls) opts+=(--ssl-reqd) ;;
    esac
    [ -z "$user" ] || opts+=(--login-options AUTH=PLAIN)
    if [[ "$host" == *:* ]]; then url+="://[$host]:$port"; else url+="://$host:$port"; fi
    SMTP_CHECK_VIA=""
    while true; do
        rc=0
        out="$({ [ -z "$user" ] || printf 'user = "%s:%s"\n' "$user" "$pw"; } | curl "${opts[@]}" "$@" "$url" 2>/dev/null)" || rc=$?
        ip="${out%%|*}"
        err="${out#*|}"
        if [ "$rc" -eq 0 ]; then
            SMTP_CHECK_VIA="$ip"
            return 0
        fi
        err="${err:-curl exit code $rc}"
        transient=0
        case "$rc" in
            6) SMTP_CHECK_PHASE=dns ;;
            7) SMTP_CHECK_PHASE=tcp transient=1 ;;
            # Connected (remote_ip set) but no answer in time: a wrong TLS
            # mode for the port, not a passing outage.
            28) if [ -n "$ip" ]; then SMTP_CHECK_PHASE=smtp
                elif [[ "$err" == Resolv* ]]; then SMTP_CHECK_PHASE=dns transient=1
                else SMTP_CHECK_PHASE=tcp transient=1; fi ;;
            52|55|56) SMTP_CHECK_PHASE=smtp transient=1 ;;
            35|51|53|54|58|59|60|64|66|77|80|82|83|90|91|98) SMTP_CHECK_PHASE=tls ;;
            67|94) SMTP_CHECK_PHASE=auth ;;
            *) SMTP_CHECK_PHASE=smtp ;;
        esac
        if [ "$transient" -eq 0 ] || [ "$attempt" -ge "$attempts" ]; then
            break
        fi
        info "SMTP check, attempt $attempt of $attempts: $err — trying again in $delay s …"
        sleep "$delay"
        attempt=$((attempt + 1))
    done
    case "$SMTP_CHECK_PHASE" in
        dns) SMTP_CHECK_PROBLEM="is not reachable from this host: its name does not resolve here ($err)" ;;
        tcp) info "Connecting to every address of $host on its own for the diagnosis …"
            SMTP_CHECK_PROBLEM="is not reachable from this host: no TCP connection after $attempt attempt(s) ($err; addresses: $(smtp_address_report "$host" "$port"))" ;;
        smtp) SMTP_CHECK_PROBLEM="accepts the TCP connection${ip:+ ($ip)} but does not complete the SMTP session in TLS mode $mode ($err) — starttls and none expect a plain-text greeting (usually port 587 or 25), tls implicit TLS (usually port 465)" ;;
        tls) SMTP_CHECK_PROBLEM="fails in TLS mode $mode ($err)"
            case "$rc" in
                64) SMTP_CHECK_PROBLEM+=": it offers no STARTTLS on port $port; for implicit TLS (usually port 465) use the TLS mode tls" ;;
                35) SMTP_CHECK_PROBLEM+=": no TLS handshake — implicit TLS (tls) on a STARTTLS port or the other way round, or a server below TLS 1.2" ;;
                *) SMTP_CHECK_PROBLEM+=": the certificate must be valid for $host and issued by a trusted CA — the server verifies it the same way" ;;
            esac ;;
        auth) SMTP_CHECK_PROBLEM="refuses the login of '$user' (AUTH PLAIN: $err)" ;;
    esac
    return 1
}

# smtp_resolve <host> — the addresses <host> resolves to on this host, IPv6
# and IPv4 in the order of the resolver, one per line.
smtp_resolve() {
    timeout "${RETICORA_SMTP_CHECK_TIMEOUT:-10}" getent ahosts "$1" 2>/dev/null | awk '$2 == "STREAM" && !seen[$1]++ { print $1 }'
}

# smtp_address_report <host> <port> — every address of <host> with the result
# of its own TCP connection (at most 5 s each), e.g. "2001:db8::25 timeout
# after 5 s, 192.0.2.25 connected": shows which address family or address
# fails.
# shellcheck disable=SC2016 # expanded by the inner shell
smtp_address_report() {
    local t="${RETICORA_SMTP_CHECK_TIMEOUT:-10}" addr err rc report=""
    [[ "$t" =~ ^[1-9][0-9]*$ ]] && [ "$t" -lt 5 ] || t=5
    for addr in $(smtp_resolve "$1"); do
        rc=0
        err="$(timeout "$t" bash -c 'exec 3<>"/dev/tcp/$1/$2"' _ "$addr" "$2" 2>&1)" || rc=$?
        case "$rc" in
            0) err=connected ;;
            124) err="timeout after $t s" ;;
            *) err="${err##*: }" ;;
        esac
        report+="${report:+, }$addr $err"
    done
    printf '%s' "${report:-none resolved}"
}

# smtp_relay_check — the pre-check of the SMTP server before anything starts:
# with RETICORA_SMTP_HOST set, /readyz of the server opens an SMTP session to
# it in the configured TLS mode, and a relay it cannot use would only surface
# as an unhealthy server. The login is checked as well; /readyz does not log
# in, so a refused login (SMTP_CHECK_PHASE auth) does not stop the server.
smtp_relay_check() {
    local via
    info "Checking the SMTP server $RETICORA_SMTP_HOST:$RETICORA_SMTP_PORT (TLS mode $RETICORA_SMTP_TLS${RETICORA_SMTP_USERNAME:+, login as $RETICORA_SMTP_USERNAME}) …"
    SMTP_CHECK_PHASE=tcp
    SMTP_CHECK_PROBLEM="is not reachable from this host"
    smtp_relay_reachable "$RETICORA_SMTP_HOST" "$RETICORA_SMTP_PORT" "$RETICORA_SMTP_TLS" \
        "$RETICORA_SMTP_USERNAME" "$RETICORA_SMTP_PASSWORD" || return 1
    via="$SMTP_CHECK_VIA"
    success "SMTP server $RETICORA_SMTP_HOST:$RETICORA_SMTP_PORT works in TLS mode $RETICORA_SMTP_TLS${via:+ (connected to $via)}${RETICORA_SMTP_USERNAME:+, login accepted}"
    # The server container uses IPv4 unless IPv6 is enabled for Docker's
    # networks: a relay reached over IPv6 here must answer over IPv4 too.
    if [[ "$via" == *:* ]] && [[ "$RETICORA_SMTP_HOST" != *:* ]] &&
        ! RETICORA_SMTP_CHECK_ATTEMPTS=1 smtp_relay_reachable "$RETICORA_SMTP_HOST" "$RETICORA_SMTP_PORT" "$RETICORA_SMTP_TLS" "" "" --ipv4; then
        warn "The SMTP server $RETICORA_SMTP_HOST:$RETICORA_SMTP_PORT works over IPv6 ($via) but over IPv4 it $SMTP_CHECK_PROBLEM. The server container connects over IPv4 unless IPv6 is enabled for Docker's networks, so its /readyz may fail ('Reticora server did not become healthy'); check the IPv4 route and firewall ($SMTP_TROUBLESHOOTING)."
    fi
    return 0
}

# ─── Session signing key ──────────────────────────────────────────────────────
ensure_session_key() {
    local key_dir="$COMPOSE_DIR/secrets"
    local key_file="$key_dir/session-private.pem"

    # Docker creates an empty *directory* at the source path of a file bind
    # mount when that path does not exist yet, which happens whenever the
    # stack was started once before the key was generated. The server then
    # exits with "read session key: is a directory" and the container never
    # becomes healthy ("container docker-compose-server-1 is unhealthy").
    # The directory is owned by root, so remove it (with sudo if needed)
    # before regenerating the key — otherwise every re-run fails again.
    if [ -e "$key_file" ] && [ ! -f "$key_file" ]; then
        warn "$key_file is not a regular file (Docker created it as a mount point); recreating it."
        rm -rf "$key_file" 2>/dev/null || sudo -n rm -rf "$key_file" 2>/dev/null || true
        if [ -e "$key_file" ]; then
            die "Cannot remove $key_file. Delete it manually (sudo rm -rf '$key_file') and re-run this installer."
        fi
    fi
    # A previous interrupted run can leave a zero-byte key behind, which fails
    # the same way; treat it as missing.
    if [ -f "$key_file" ] && [ ! -s "$key_file" ]; then
        warn "$key_file is empty; regenerating it."
        rm -f "$key_file" 2>/dev/null || sudo -n rm -f "$key_file" 2>/dev/null || true
    fi

    if [ ! -f "$key_file" ]; then
        info "Generating RS256 session signing key …"
        mkdir -p "$key_dir" || die "Cannot create $key_dir."
        openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:4096 -out "$key_file" 2>/dev/null \
            || die "Generating the session signing key at $key_file failed."
        [ -s "$key_file" ] || die "The session signing key at $key_file is empty."
    fi
    # The key is bind-mounted into the server container, whose process runs as
    # the unprivileged nobody user (uid/gid 65534, see backend/Dockerfile).
    # A root-only 0600 file is therefore unreadable in the container and the
    # server exits with "read session key: permission denied", which surfaces
    # as "container docker-compose-server-1 is unhealthy". Group the file to
    # the nobody gid (0640) so only the owner on the host and the server
    # process can read it; fall back to world-readable 0644 when chown is not
    # permitted (e.g. the installer itself runs unprivileged).
    if chown "$(id -u):65534" "$key_file" 2>/dev/null; then
        chmod 640 "$key_file"
    else
        warn "Cannot chown the session key to the container's nobody group; making it world-readable (0644) so the server container can read it."
        chmod 644 "$key_file"
    fi
    success "Session signing key: $key_file"
}

# ─── HTTPS / Let's Encrypt (Certbot) ──────────────────────────────────────────
# Layout on the host (all under $COMPOSE_DIR, git-ignored):
#   letsencrypt/          /etc/letsencrypt for the certbot + frontend containers
#   certbot-webroot/      ACME http-01 challenge directory (named compose volume)
#
# Flow when RETICORA_TLS_DOMAIN is set:
#   1. bootstrap_tls_cert   — create a self-signed placeholder certificate so
#                             nginx can start with the TLS configuration before
#                             the real certificate exists.
#   2. issue_tls_certificate — move the placeholder aside, run certbot with
#                             the webroot plugin (nginx must already serve
#                             /.well-known/acme-challenge/) and its deploy
#                             hook, check that nginx can load the new pair,
#                             then reload nginx to pick up the real cert.
# The long-running certbot service in the compose file renews the certificate
# twice a day (no-op until it is close to expiry) with the same deploy hook.

letsencrypt_dir() { printf '%s/letsencrypt' "$COMPOSE_DIR"; }

tls_live_dir() { printf '%s/letsencrypt/live/%s' "$COMPOSE_DIR" "$RETICORA_TLS_DOMAIN"; }

# certbot --deploy-hook (WP-419, F-034): deploy/docker-compose/certbot-deploy-hook.sh,
# mounted into the certbot service, gives the private key to group 101 (0640)
# and makes the chain readable, so nginx (uid/gid 101) in the frontend can
# load both. Run through sh: certbot only checks that the first word of a hook
# is executable, and the mounted file keeps its host mode.
CERTBOT_DEPLOY_HOOK="sh /usr/local/bin/reticora-certbot-deploy-hook.sh"

TLS_TROUBLESHOOTING="see docs/troubleshooting-installation.md, 'Issued certificate not loadable by nginx'"

# tls_ca_cert_path_in_container — path of the served certificate as seen by the
# server container (./letsencrypt is bind-mounted at /etc/letsencrypt there).
# Empty when TLS is disabled, so the server keeps using the system trust store.
tls_ca_cert_path_in_container() {
    [ -n "$RETICORA_TLS_DOMAIN" ] || return 0
    printf '/etc/letsencrypt/live/%s/fullchain.pem' "$RETICORA_TLS_DOMAIN"
}

# tls_cert_has_san — returns 0 when the certificate carries a subjectAltName
# extension. Go (and therefore the backend's OIDC client) rejects certificates
# that only have a Common Name with
# "x509: certificate relies on legacy Common Name field, use SANs instead".
tls_cert_has_san() {
    local cert="$1"
    [ -f "$cert" ] || return 1
    openssl x509 -in "$cert" -noout -ext subjectAltName 2>/dev/null | grep -q 'DNS:'
}

# tls_cert_expires_soon — returns 0 when the certificate is expired or expires
# within a day, so a stale bootstrap placeholder gets replaced.
tls_cert_expires_soon() {
    local cert="$1"
    [ -f "$cert" ] || return 0
    ! openssl x509 -in "$cert" -noout -checkend 86400 >/dev/null 2>&1
}

# tls_cert_is_trusted <fullchain> — returns 0 when the leaf certificate
# verifies against the system trust store with the intermediates of the
# chain file (INS-03). openssl verify reads the first certificate of the file
# as the leaf; RETICORA_TLS_CA_BUNDLE adds a CA file (tests, private CAs).
tls_cert_is_trusted() {
    local cert="$1"
    [ -f "$cert" ] || return 1
    local -a ca_args=()
    [ -n "${RETICORA_TLS_CA_BUNDLE:-}" ] && ca_args=(-CAfile "$RETICORA_TLS_CA_BUNDLE")
    openssl verify "${ca_args[@]}" -untrusted "$cert" "$cert" >/dev/null 2>&1
}

# tls_cert_is_selfsigned — returns 0 when no trusted certificate is served
# yet: none exists, it is the self-signed bootstrap placeholder, or its chain
# does not verify (INS-03). The installer then requests a Let's Encrypt
# certificate.
tls_cert_is_selfsigned() {
    ! tls_cert_is_trusted "$(tls_live_dir)/fullchain.pem"
}

# tls_template_source — the nginx TLS template mounted into the frontend
# container: the TLS server blocks with a domain, an empty template without
# one, so no TLS configuration is rendered (INS-03).
tls_template_source() {
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        printf './nginx-tls.conf.template'
    else
        printf './nginx-no-tls.conf.template'
    fi
}

# Set to 1 when an existing bootstrap certificate was replaced, so an already
# running nginx is told to reload it instead of serving the old one from memory.
TLS_BOOTSTRAP_REGENERATED=0

bootstrap_tls_cert() {
    [ -n "$RETICORA_TLS_DOMAIN" ] || return 0
    local live_dir; live_dir="$(tls_live_dir)"
    if [ -f "$live_dir/fullchain.pem" ]; then
        if tls_cert_is_selfsigned; then
            # Placeholders created by older installer versions have no
            # subjectAltName, which makes the Go OIDC client abort the token
            # exchange ("certificate relies on legacy Common Name field").
            # Others may simply have expired. Both must be replaced, otherwise
            # re-running the installer silently keeps the broken certificate.
            if ! tls_cert_has_san "$live_dir/fullchain.pem"; then
                warn "Bootstrap certificate for $RETICORA_TLS_DOMAIN has no subjectAltName; regenerating it."
            elif tls_cert_expires_soon "$live_dir/fullchain.pem"; then
                warn "Bootstrap certificate for $RETICORA_TLS_DOMAIN is expired or expires within a day; regenerating it."
            else
                info "Bootstrap self-signed certificate already exists for $RETICORA_TLS_DOMAIN"
                return 0
            fi
            TLS_BOOTSTRAP_REGENERATED=1
            rm -f "$live_dir/fullchain.pem" "$live_dir/privkey.pem" 2>/dev/null \
                || sudo -n rm -f "$live_dir/fullchain.pem" "$live_dir/privkey.pem" 2>/dev/null || true
            [ ! -f "$live_dir/fullchain.pem" ] \
                || die "Cannot replace $live_dir/fullchain.pem. Delete it manually and re-run this installer."
        else
            success "Let's Encrypt certificate for $RETICORA_TLS_DOMAIN already present"
            return 0
        fi
    fi
    info "Creating a bootstrap self-signed certificate for $RETICORA_TLS_DOMAIN …"
    mkdir -p "$live_dir" || die "Cannot create $live_dir."
    # The frontend container runs as the unprivileged nginx user and reads the
    # certificate through the ./letsencrypt bind mount, so every directory on
    # the path must be world-traversable and the files world-readable.
    chmod 755 "$(letsencrypt_dir)" "$live_dir" || true
    # Validity: 30 days. The placeholder is replaced by the real Let's Encrypt
    # certificate later in this run; a longer validity keeps nginx serving a
    # (still untrusted but parseable) certificate when issuance fails and the
    # installer is not re-run immediately. A 1-day certificate silently expired
    # and left browsers with a permanent "insecure" warning.
    openssl req -x509 -newkey rsa:2048 -nodes \
        -keyout "$live_dir/privkey.pem" \
        -out "$live_dir/fullchain.pem" \
        -days 30 -subj "/CN=$RETICORA_TLS_DOMAIN" \
        -addext "subjectAltName=DNS:$RETICORA_TLS_DOMAIN" 2>/dev/null \
        || die "Generating the bootstrap certificate failed."
    # The frontend container reads these as the unprivileged nginx user via a
    # read-only bind mount; letsencrypt/ stays host-owned.
    chmod 644 "$live_dir/fullchain.pem" "$live_dir/privkey.pem"
    # Sanity-check the freshly created material: a broken certificate or key
    # makes nginx fail to start much later with a less obvious message.
    validate_tls_material "$RETICORA_TLS_DOMAIN"
    success "Bootstrap certificate created (will be replaced by Let's Encrypt)"
}

# validate_tls_material <domain> [chain] — verifies that the certificate chain
# and private key under letsencrypt/live/<domain> form a consistent, usable
# pair: the certificate must parse, carry a SAN for the domain, be unexpired,
# and match the private key. With chain=1 the chain must also verify against
# the trust store (INS-03). Called after bootstrap generation and again
# (with chain=1) after certbot issuance so TLS problems surface at install
# time.
validate_tls_material() {
    local domain="$1" chain="${2:-0}"
    local live_dir; live_dir="$(tls_live_dir)"
    local cert="$live_dir/fullchain.pem" key="$live_dir/privkey.pem"

    [ -f "$cert" ] || die "TLS certificate missing: $cert"
    [ -f "$key" ]  || die "TLS private key missing: $key"

    openssl x509 -in "$cert" -noout -subject >/dev/null 2>&1 \
        || die "TLS certificate $cert cannot be parsed by openssl."

    tls_cert_has_san "$cert" \
        || die "TLS certificate $cert has no subjectAltName — Go clients (and modern browsers) reject CN-only certificates."

    openssl x509 -in "$cert" -noout -checkend 0 >/dev/null 2>&1 \
        || die "TLS certificate $cert is already expired."

    if ! openssl x509 -in "$cert" -noout -ext subjectAltName 2>/dev/null | grep -q "DNS:$domain\b"; then
        warn "TLS certificate SAN list does not contain $domain — HTTPS will warn for this hostname."
    fi

    # The public keys of certificate and private key must be identical,
    # otherwise nginx refuses to load the pair ("key values mismatch").
    # certbot's key (root, group 101, 0640 after the deploy hook) is not
    # readable for an installer that runs without root; after the issuance
    # (chain=1) nginx -t in the frontend checks the pair instead
    # (issue_tls_certificate).
    local cert_pub key_pub key_note="key matches"
    if [ "$chain" = "1" ] && [ ! -r "$key" ]; then
        key_note="key checked by nginx"
    else
        cert_pub="$(openssl x509 -in "$cert" -noout -pubkey 2>/dev/null | openssl sha256)"
        key_pub="$(openssl pkey -in "$key" -pubout 2>/dev/null | openssl sha256)"
        [ -n "$cert_pub" ] && [ "$cert_pub" = "$key_pub" ] \
            || die "TLS certificate $cert and private key $key do not match."
    fi
    if [ "$chain" = "1" ]; then
        tls_cert_is_trusted "$cert" \
            || die "TLS certificate chain $cert does not verify against the system trust store (incomplete chain or untrusted issuer)."
        success "TLS material for $domain is consistent (parses, SAN present, $key_note, chain verifies)"
        return 0
    fi
    success "TLS material for $domain is consistent (parses, SAN present, $key_note)"
}

# reload_frontend_after_bootstrap — `compose up -d` leaves an unchanged
# frontend container running, so a regenerated bootstrap certificate would only
# be served after an explicit nginx reload.
reload_frontend_after_bootstrap() {
    [ "$TLS_BOOTSTRAP_REGENERATED" = "1" ] || return 0
    compose_cmd exec -T frontend nginx -s reload >/dev/null 2>&1 \
        || compose_cmd restart frontend >/dev/null 2>&1 \
        || warn "Could not reload the frontend to activate the regenerated bootstrap certificate."
}

# tls_lineage_paths — where certbot stores the lineage of the domain, relative
# to letsencrypt/: the live and archive directories and the renewal
# configuration.
tls_lineage_paths() {
    printf '%s\n' "live/$RETICORA_TLS_DOMAIN" "archive/$RETICORA_TLS_DOMAIN" "renewal/$RETICORA_TLS_DOMAIN.conf"
}

# tls_placeholder_backup_dir — keeps the placeholder while certbot creates the
# lineage; inside letsencrypt/ (git-ignored, same file system), which certbot
# does not read.
tls_placeholder_backup_dir() { printf '%s/reticora-placeholder/%s' "$(letsencrypt_dir)" "$RETICORA_TLS_DOMAIN"; }

# tls_rm <path> — removes a path; files certbot created belong to root (it
# runs as root in its container), so with sudo -n when that is needed.
tls_rm() {
    rm -rf "$1" 2>/dev/null || sudo -n rm -rf "$1" 2>/dev/null || true
    [ ! -e "$1" ] && [ ! -L "$1" ]
}

# tls_move_lineage <from> <to> — moves the existing paths of
# tls_lineage_paths from one letsencrypt tree into the other; stops at the
# first path that cannot be moved.
tls_move_lineage() {
    local from="$1" to="$2" rel
    while IFS= read -r rel; do
        [ -e "$from/$rel" ] || [ -L "$from/$rel" ] || continue
        [ ! -e "$to/$rel" ] && [ ! -L "$to/$rel" ] || return 1
        mkdir -p "$to/${rel%/*}" 2>/dev/null || sudo -n mkdir -p "$to/${rel%/*}" 2>/dev/null || return 1
        mv "$from/$rel" "$to/$rel" 2>/dev/null || sudo -n mv "$from/$rel" "$to/$rel" 2>/dev/null || return 1
    done < <(tls_lineage_paths)
}

# stash_tls_placeholder — F-033: certbot refuses to store a new lineage while
# live/<domain> holds the bootstrap placeholder ("CertStorageError: live
# directory exists", after it has placed the ACME order), and an existing
# renewal/<domain>.conf turns the request into a second lineage
# <domain>-0001. Before the request live/<domain>, archive/<domain> and
# renewal/<domain>.conf therefore move into the backup directory.
stash_tls_placeholder() {
    local le backup
    # The domain names the paths that are moved and removed (also with sudo).
    valid_domain "$RETICORA_TLS_DOMAIN" || die "Invalid TLS domain '$RETICORA_TLS_DOMAIN'."
    le="$(letsencrypt_dir)"
    backup="$(tls_placeholder_backup_dir)"
    # A backup an interrupted run left behind is stale: bootstrap_tls_cert has
    # created a new placeholder in live/<domain> since.
    tls_rm "$backup" \
        || die "Cannot remove the stale placeholder backup $backup. Delete it manually and re-run this installer."
    if ! tls_move_lineage "$le" "$backup"; then
        tls_move_lineage "$backup" "$le" || true
        die "Cannot move the placeholder certificate of $RETICORA_TLS_DOMAIN (live/, archive/, renewal/ under $le) aside before the Let's Encrypt request; certbot cannot store the certificate while it is there. Move it into $backup manually and re-run this installer."
    fi
}

# restore_tls_placeholder — after a failed request: removes what certbot left
# of the lineage (e.g. the empty renewal configuration it writes before it
# stores the certificate) and moves the placeholder back.
restore_tls_placeholder() {
    local le backup rel
    le="$(letsencrypt_dir)"
    backup="$(tls_placeholder_backup_dir)"
    while IFS= read -r rel; do
        tls_rm "$le/$rel" || warn "Could not remove $le/$rel, left behind by the failed certbot run."
    done < <(tls_lineage_paths)
    if tls_move_lineage "$backup" "$le"; then
        tls_rm "$backup" || true
    else
        warn "Could not restore the placeholder certificate from $backup; move its contents back into $le manually."
    fi
}

# tls_check_failed <message> — aborts when nginx cannot load the issued
# certificate. issue_tls_certificate runs after create_initial_admin, so — as
# in issuer_check_failed — the run that has just created the administrator
# shows its temporary password first.
tls_check_failed() {
    if [ -n "$INITIAL_ADMIN_PASSWORD" ]; then
        printf '%s\n' "$(initial_login_line)" >&2
    fi
    die "$*"
}

issue_tls_certificate() {
    [ -n "$RETICORA_TLS_DOMAIN" ] || return 0
    if ! tls_cert_is_selfsigned; then
        return 0  # real certificate already issued (idempotent re-run)
    fi

    info "Requesting a Let's Encrypt certificate for $RETICORA_TLS_DOMAIN …"
    info "(requires ports 80/443 reachable from the internet and DNS pointing at this host)"

    local -a email_args=(--register-unsafely-without-email)
    if [ -n "$RETICORA_CERT_EMAIL" ]; then
        email_args=(--email "$RETICORA_CERT_EMAIL")
    fi
    # RETICORA_ACME_SERVER (WP-420): another ACME directory than certbot's
    # default, Let's Encrypt production, e.g. staging or a test CA. certbot
    # records it in renewal/<domain>.conf; the renewal loop of the certbot
    # service passes it as well. The certbot service mounts
    # RETICORA_ACME_CA_BUNDLE as REQUESTS_CA_BUNDLE (docker-compose.yml).
    local -a server_args=()
    if [ -n "${RETICORA_ACME_SERVER:-}" ]; then
        server_args=(--server "$RETICORA_ACME_SERVER")
        info "(ACME server: $RETICORA_ACME_SERVER)"
    fi

    stash_tls_placeholder

    # The certbot container shares the webroot volume with the frontend, and
    # ./letsencrypt is bind-mounted as /etc/letsencrypt in both. nginx is
    # already running with the bootstrap certificate (in memory), serving the
    # challenge directory over plain HTTP on port 80. The service's entrypoint
    # is the renewal loop shell, so the one-off run selects the certbot binary.
    # --cert-name keeps the lineage at live/<domain>; certbot records the
    # deploy hook in renewal/<domain>.conf as well.
    local issued=0 out
    if compose_cmd run --rm --no-deps --entrypoint certbot certbot certonly \
        --webroot -w /var/www/certbot \
        -d "$RETICORA_TLS_DOMAIN" --cert-name "$RETICORA_TLS_DOMAIN" \
        --non-interactive --agree-tos \
        --deploy-hook "$CERTBOT_DEPLOY_HOOK" \
        "${email_args[@]}" "${server_args[@]}"; then
        issued=1
    elif ! tls_cert_is_selfsigned; then
        # An error after the lineage was stored: keep the certificate and check
        # it, a later run would otherwise place another order.
        warn "certbot reported an error after storing the certificate for $RETICORA_TLS_DOMAIN; checking the stored certificate."
        issued=1
    fi
    if [ "$issued" != "1" ]; then
        restore_tls_placeholder
        warn "Let's Encrypt certificate issuance failed."
        warn "Common causes: DNS does not point at this host, or ports 80/443"
        warn "are not reachable from the internet. The stack keeps running with"
        warn "the self-signed bootstrap certificate; re-run this installer after"
        warn "fixing connectivity, or check the logs:"
        warn "  ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs certbot"
        return 0
    fi
    tls_rm "$(tls_placeholder_backup_dir)" || warn "Could not remove the placeholder backup $(tls_placeholder_backup_dir)."

    validate_tls_material "$RETICORA_TLS_DOMAIN" 1
    # F-034: nginx in the frontend runs as uid 101. It must be able to load
    # the stored chain and key (deploy hook) before it is told to reload: a
    # failed reload goes unnoticed, nginx keeps the placeholder in memory and
    # the next start of the frontend fails.
    if ! out="$(compose_cmd exec -T frontend nginx -t 2>&1)"; then
        printf '%s\n' "$out" | tail -n 5 >&2
        tls_check_failed "nginx in the frontend (uid 101) cannot load the certificate and key certbot stored in $(tls_live_dir) (nginx -t failed). HTTPS still serves the bootstrap certificate, and the frontend will not start again until the files are readable ($TLS_TROUBLESHOOTING)."
    fi
    success "Let's Encrypt certificate issued for $RETICORA_TLS_DOMAIN"
    if compose_cmd exec -T frontend nginx -s reload >/dev/null 2>&1; then
        success "HTTPS is now active at https://$RETICORA_TLS_DOMAIN"
    else
        warn "Could not reload nginx automatically; restart the frontend to activate the certificate: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE restart frontend"
    fi
}

# ─── Keycloak realm provisioning ──────────────────────────────────────────────
configure_realm() {
    local realm_src="$SCRIPT_DIR/deploy/keycloak/realm-reticora.json"
    local out_dir="$COMPOSE_DIR/.generated"
    [ -f "$realm_src" ] || die "Realm template not found: $realm_src"
    # Docker creates the bind-mount source as a root-owned *directory* when it
    # did not exist before the first `up`; if a previous run then fails to
    # write into it, every re-run dies with "permission denied". Recover
    # instead of forcing the operator to clean up manually.
    if [ -d "$out_dir" ] && [ ! -w "$out_dir" ]; then
        warn "$out_dir is not writable (likely created by Docker as root); taking ownership."
        chown -R "$(id -u):$(id -g)" "$out_dir" 2>/dev/null \
            || sudo -n chown -R "$(id -u):$(id -g)" "$out_dir" 2>/dev/null \
            || die "Cannot write to $out_dir. Fix the ownership (sudo chown -R \$USER '$out_dir') and re-run this installer."
    fi
    mkdir -p "$out_dir"
    # Substitute the configured client secret so the backend and Keycloak agree
    # on it; the .generated copy is git-ignored and never committed. Also
    # whitelist the configured OIDC redirect URL (and its origin) so sign-in
    # does not fail with "Invalid redirect uri" for non-localhost installs,
    # and the issuer origin so the browser may call Keycloak endpoints from
    # the SPA origin (CORS) when both are served behind the same reverse
    # proxy.
    local redirect_origin issuer_origin
    redirect_origin="$(printf '%s' "$RETICORA_OIDC_REDIRECT_URL" | sed -E 's|^(https?://[^/]*).*$|\1|')"
    issuer_origin="$(printf '%s' "$RETICORA_OIDC_ISSUER_URL" | sed -E 's|^(https?://[^/]*).*$|\1|')"
    sed -e "s/RETICORA_OIDC_CLIENT_SECRET_PLACEHOLDER/$RETICORA_OIDC_CLIENT_SECRET/g" \
        -e "s|RETICORA_OIDC_REDIRECT_URL_PLACEHOLDER|$RETICORA_OIDC_REDIRECT_URL|g" \
        -e "s|RETICORA_OIDC_REDIRECT_ORIGIN_PLACEHOLDER|$redirect_origin|g" \
        -e "s|RETICORA_OIDC_ISSUER_ORIGIN_PLACEHOLDER|$issuer_origin|g" \
        "$realm_src" > "$out_dir/realm-reticora.json"
    # The file is bind-mounted into the Keycloak container, whose process runs
    # as the unprivileged keycloak user — uid 1000 with primary group *root*
    # (gid 0), not gid 1000. A copy owned by the installing user with mode 600
    # (or 640 with group 1000) is therefore unreadable inside the container, so
    # Keycloak aborts the realm import with
    # "java.nio.file.AccessDeniedException: …/data/import/realm-reticora.json",
    # crash-loops and the UI's nginx answers every /realms/ request with
    # 502 Bad Gateway.
    #
    # Preferred fix: hand the file to uid 1000 / gid 0 and keep mode 640, so the
    # client secret stays unreadable for unprivileged host users. When the
    # ownership cannot be changed (non-root installer), fall back to 644 — the
    # bind mount only works when the container can read the file.
    #
    # The directory itself must be traversable (o+x) for the same reason; a
    # restrictive umask would otherwise create it as 700 and the import fails
    # even with a world-readable file.
    local realm_file="$out_dir/realm-reticora.json"
    if chown 1000:0 "$realm_file" 2>/dev/null; then
        chmod 640 "$realm_file"
    else
        chmod 644 "$realm_file"
    fi
    chmod o+rx "$out_dir" 2>/dev/null || true
    success "Keycloak realm prepared with the configured OIDC client secret and redirect URL"

    validate_realm_json "$realm_file"
}

# validate_realm_json <file> — sanity-checks the rendered realm file before
# it is bind-mounted into Keycloak. A syntactically invalid file (or one that
# still contains an unsubstituted placeholder) makes the import fail with a
# cryptic Keycloak stack trace and the container crash-loops; the UI then
# answers every /realms/ request with 502 Bad Gateway. Catching that here
# produces a clear error at install time instead.
validate_realm_json() {
    local realm_file="$1"

    if grep -q '_PLACEHOLDER' "$realm_file"; then
        local leftover
        leftover="$(grep -o '[A-Z_]*_PLACEHOLDER' "$realm_file" | sort -u | tr '\n' ' ')"
        die "The rendered realm file $realm_file still contains unsubstituted placeholders: $leftover"
    fi

    if have_cmd python3; then
        python3 -c 'import json, sys; json.load(open(sys.argv[1]))' "$realm_file" 2>/dev/null \
            || die "The rendered realm file $realm_file is not valid JSON — check the configured URLs for characters that break the template (e.g. unquoted backslashes)."
    elif have_cmd jq; then
        jq empty "$realm_file" 2>/dev/null \
            || die "The rendered realm file $realm_file is not valid JSON — check the configured URLs for characters that break the template (e.g. unquoted backslashes)."
    else
        warn "Neither python3 nor jq is available; skipping JSON validation of the realm file."
    fi

    if ! grep -q '"realm"[[:space:]]*:[[:space:]]*"reticora"' "$realm_file"; then
        die "The rendered realm file $realm_file does not define the 'reticora' realm — the template $SCRIPT_DIR/deploy/keycloak/realm-reticora.json may be corrupted."
    fi
    success "Keycloak realm file validated (JSON, no leftover placeholders)"
}

# validate_keycloak_bootstrap — post-start checks that go beyond the container
# healthcheck: the realm must actually be imported and serve its OIDC
# discovery document, otherwise sign-in fails later in the browser with a
# hard-to-diagnose error. The configured issuer itself is checked by
# start_stack once the frontend runs (check_issuer_from_host/_container).
validate_keycloak_bootstrap() {
    info "Validating the Keycloak bootstrap …"

    # A localhost issuer resolves to the backend container itself, not to
    # Keycloak: the backend could not verify any token (INS-02).
    if issuer_is_loopback "$RETICORA_OIDC_ISSUER_URL"; then
        die "RETICORA_OIDC_ISSUER_URL ($RETICORA_OIDC_ISSUER_URL) points at localhost, which the backend container cannot reach. Set it to an address reachable from this host, the containers and the users' browsers, e.g. http://$(host_primary_ip || printf '<host-ip>'):${RETICORA_KEYCLOAK_PORT:-8180}/realms/reticora (see docs/troubleshooting-installation.md, 'OIDC issuer check failed')."
    fi

    # Verify the realm's OIDC discovery document is served (proves the realm
    # import succeeded). The issuer may be a public URL not resolvable from
    # this host during install, so probe the local Keycloak port first and
    # fall back to the configured issuer URL.
    local probe_url="$RETICORA_OIDC_ISSUER_URL"
    case "$probe_url" in
        */realms/reticora) probe_url="${probe_url%/}/.well-known/openid-configuration" ;;
        *) probe_url="${probe_url%/}/realms/reticora/.well-known/openid-configuration" ;;
    esac
    local local_probe="http://localhost:${RETICORA_KEYCLOAK_PORT}/realms/reticora/.well-known/openid-configuration"

    local discovered=""
    for _ in 1 2 3 4 5; do
        if discovered="$(curl -fsS --max-time 5 "$local_probe" 2>/dev/null)"; then
            break
        fi
        if discovered="$(curl -fsS --max-time 5 "$probe_url" 2>/dev/null)"; then
            break
        fi
        sleep 3
    done

    if [ -z "$discovered" ]; then
        show_service_logs keycloak
        warn "Keycloak is running but the 'reticora' realm does not answer its OIDC"
        warn "discovery document. The realm import most likely failed — common causes:"
        warn "  - realm file unreadable inside the container (AccessDeniedException)"
        warn "  - invalid JSON in the rendered realm file"
        warn "Check: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs keycloak | grep -i import"
        die "Keycloak bootstrap validation failed — the realm was not imported."
    fi

    if ! printf '%s' "$discovered" | grep -q '"issuer"'; then
        die "Keycloak answered the discovery document without an 'issuer' field — the realm import is incomplete."
    fi
    success "Keycloak realm 'reticora' is imported and serving OIDC discovery"
}

# host_primary_ip — the host's primary IPv4 address (the source address of
# its default route); RETICORA_HOST_IP overrides it.
host_primary_ip() {
    if [ -n "${RETICORA_HOST_IP:-}" ]; then printf '%s' "$RETICORA_HOST_IP"; return 0; fi
    local addr=""
    if command -v ip >/dev/null 2>&1; then
        addr="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{ for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit } }')"
    fi
    if [ -z "$addr" ] && command -v hostname >/dev/null 2>&1; then
        addr="$(hostname -I 2>/dev/null | awk '{ print $1 }')"
    fi
    [ -n "$addr" ] || return 1
    printf '%s' "$addr"
}

# issuer_is_loopback <url> — returns 0 for an issuer on localhost/127.x.
issuer_is_loopback() {
    case "$1" in
        http://localhost:*|http://localhost/*|https://localhost:*|https://localhost/*|http://127.*|https://127.*|http://0.0.0.0*|https://0.0.0.0*) return 0 ;;
    esac
    return 1
}

# discovery_issuer — prints the "issuer" of the discovery document on stdin.
discovery_issuer() {
    tr -d '\n' | sed -nE 's/.*"issuer"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p'
}

ISSUER_TROUBLESHOOTING="see docs/troubleshooting-installation.md, 'OIDC issuer check failed'"

# issuer_check_failed <message> — aborts after a failed issuer check. The
# checks run after create_initial_admin, so the run that has just created the
# administrator shows its temporary password here; a re-run does not show it
# again.
issuer_check_failed() {
    if [ -n "$INITIAL_ADMIN_PASSWORD" ]; then
        printf '%s\n' "$(initial_login_line)" >&2
    fi
    die "$*"
}

# check_issuer_from_host — the configured issuer serves its discovery
# document to this host and announces exactly itself (INS-02). A
# self-signed certificate of the installation is trusted explicitly.
check_issuer_from_host() {
    local issuer="${RETICORA_OIDC_ISSUER_URL%/}" doc="" announced cert ca_args=()
    cert="$(tls_live_dir)/fullchain.pem"
    if [ -n "$RETICORA_TLS_DOMAIN" ] && [ -f "$cert" ] && tls_cert_is_selfsigned "$cert"; then
        ca_args=(--cacert "$cert")
    fi
    for _ in 1 2 3; do
        doc="$(curl -fsS --max-time 5 "${ca_args[@]}" "$issuer/.well-known/openid-configuration" 2>/dev/null)" && break
        doc=""
        sleep "${RETICORA_ISSUER_RETRY_DELAY:-3}"
    done
    if [ -z "$doc" ]; then
        issuer_check_failed "The OIDC issuer $issuer is not reachable from this host (no discovery document at $issuer/.well-known/openid-configuration). Check DNS, firewall and RETICORA_OIDC_ISSUER_URL ($ISSUER_TROUBLESHOOTING)."
    fi
    announced="$(printf '%s' "$doc" | discovery_issuer)"
    if [ -z "$announced" ]; then
        # curl -f takes a redirect as an answer: name its target instead of
        # an issuer '<none>'.
        local redirect
        redirect="$(curl -sS -o /dev/null --max-time 5 "${ca_args[@]}" -w '%{redirect_url}' "$issuer/.well-known/openid-configuration" 2>/dev/null || true)"
        [ -z "$redirect" ] \
            || issuer_check_failed "The OIDC issuer $issuer redirects to $redirect instead of serving its discovery document. Keycloak announces the address it is reached at, so RETICORA_OIDC_ISSUER_URL must be that address${RETICORA_TLS_DOMAIN:+ — with the TLS domain https://$RETICORA_TLS_DOMAIN/realms/reticora} ($ISSUER_TROUBLESHOOTING)."
    fi
    if [ "${announced%/}" != "$issuer" ]; then
        issuer_check_failed "The OIDC issuer $issuer announces itself as '${announced:-<none>}' — tokens would not match the configured issuer. Align RETICORA_OIDC_ISSUER_URL with the address Keycloak is published under ($ISSUER_TROUBLESHOOTING)."
    fi
    success "OIDC issuer $issuer is reachable from this host"
}

# check_issuer_from_container — the backend container reaches the same
# issuer through the client it uses for sign-in (including
# RETICORA_OIDC_CA_CERT_FILE); the server binary checks it and exits.
check_issuer_from_container() {
    local out
    if ! out="$(compose_cmd run --rm --no-deps --entrypoint server server -check-oidc-issuer 2>&1)"; then
        printf '%s\n' "$out" | tail -n 5 >&2
        issuer_check_failed "The backend container cannot reach the OIDC issuer $RETICORA_OIDC_ISSUER_URL — sign-in would fail. The issuer must resolve to Keycloak from inside the containers as well ($ISSUER_TROUBLESHOOTING)."
    fi
    success "OIDC issuer is reachable from the backend container"
}

# ─── Release: selection and source ────────────────────────────────────────────
# select_release — E-69 (Option 1): the release whose signed images are
# installed: --release (RETICORA_RELEASE), the release tag this checkout is
# on, otherwise the newest release.
select_release() {
    [ "$BUILD_FROM_SOURCE" != 1 ] || return 0
    local tag="$RELEASE"
    if [ -z "$tag" ] && have_cmd git && [ -e "$SCRIPT_DIR/.git" ]; then
        tag="$(git -C "$SCRIPT_DIR" describe --tags --exact-match HEAD 2>/dev/null)" || tag=""
        is_release_tag "$tag" || tag=""
    fi
    [ -n "$tag" ] || latest_release_tag tag
    RETICORA_IMAGE_TAG="$tag"
}

# verify_source_tree <dir> <tag> — every file of the signed source tarball of
# release <tag> is unchanged in <dir> (a checkout without git). Files the
# installer adds (.env, secrets, the rendered realm) are not part of it; the
# release mode keeps the source-build override as .disabled.
verify_source_tree() {
    local tmp out override="deploy/docker-compose/docker-compose.override.yml"
    tmp="$(mktemp -d)"
    fetch_source_tarball "$2" "$tmp/source.tar.gz"
    mkdir "$tmp/src"
    tar -xzf "$tmp/source.tar.gz" -C "$tmp/src" --strip-components=1 \
        || { rm -rf "$tmp"; die "Could not unpack the verified source tarball of $2."; }
    (cd "$tmp/src" && find . -type f -print0 | sort -z | xargs -0 -r sha256sum) > "$tmp/files.sha256" \
        || { rm -rf "$tmp"; die "Could not read the verified source tarball of $2."; }
    if [ ! -e "$1/$override" ] && [ -e "$1/$override.disabled" ]; then
        sed -i "s|  \./$override\$|  ./$override.disabled|" "$tmp/files.sha256"
    fi
    if ! out="$(cd "$1" && sha256sum -c --quiet "$tmp/files.sha256" 2>/dev/null)"; then
        rm -rf "$tmp"
        die "Source check failed: $1 is not the source of release $2 ($(printf '%s\n' "$out" | head -n 3 | tr '\n' ' ')…); nothing was installed ($SIGNATURE_TROUBLESHOOTING)."
    fi
    rm -rf "$tmp"
    success "Source verified: $1 matches reticora-source-$2.tar.gz of the signed release"
}

# verify_release_checkout — release mode: the compose file, the realm and the
# migrations come from this checkout, so it must be the signed source of the
# release: a git checkout its commit (SOURCE_COMMIT), any other directory
# every file of the signed source tarball. Before anything is written.
verify_release_checkout() {
    [ "$BUILD_FROM_SOURCE" != 1 ] || return 0
    info "Checking that $SCRIPT_DIR is the signed source of release $RETICORA_IMAGE_TAG (compose file, realm, migrations; another checkout: ./install.sh --cloud --ref $RETICORA_IMAGE_TAG downloads it, --build-from-source installs this one unverified) …"
    if have_cmd git && [ -e "$SCRIPT_DIR/.git" ]; then
        verify_source_checkout "$SCRIPT_DIR" "$RETICORA_IMAGE_TAG"
    else
        verify_source_tree "$SCRIPT_DIR" "$RETICORA_IMAGE_TAG"
    fi
}

# ─── Images: build / pull ─────────────────────────────────────────────────────
provide_images() {
    if [ "$BUILD_FROM_SOURCE" != 1 ]; then
        # Release mode (TEC-15, the default per E-69): only the signed release
        # images, pinned in .env by the digest cosign verified; nothing is
        # built locally, also not PostgreSQL. Disabling the source-build
        # override removes every build: section, so compose can only use the
        # pinned images.
        if [ -z "$RETICORA_IMAGE_REGISTRY" ] || [ -z "$RETICORA_IMAGE_TAG" ]; then
            die "No registry or release selected for the signed images; name the release with --release v<version>, or build from this checkout with --build-from-source."
        fi
        if [ -f "$OVERRIDE_FILE" ]; then
            mv "$OVERRIDE_FILE" "$OVERRIDE_FILE.disabled"
        fi
        info "Verifying the signed release images from $RETICORA_IMAGE_REGISTRY (tag $RETICORA_IMAGE_TAG) …"
        verify_release_image "$RETICORA_IMAGE_REGISTRY" reticora-server "$RETICORA_IMAGE_TAG" RETICORA_SERVER_IMAGE
        verify_release_image "$RETICORA_IMAGE_REGISTRY" reticora-frontend "$RETICORA_IMAGE_TAG" RETICORA_FRONTEND_IMAGE
        verify_release_image "$RETICORA_IMAGE_REGISTRY" reticora-postgres "$RETICORA_IMAGE_TAG" RETICORA_POSTGRES_IMAGE
        env_set RETICORA_SERVER_IMAGE "$RETICORA_SERVER_IMAGE"
        env_set RETICORA_FRONTEND_IMAGE "$RETICORA_FRONTEND_IMAGE"
        env_set RETICORA_POSTGRES_IMAGE "$RETICORA_POSTGRES_IMAGE"
        compose_cmd pull server frontend postgres || die "Pulling the verified images failed."
    else
        # Local source build (--build-from-source): make sure the build
        # override is active. The images are not signature-verified.
        if [ -f "$OVERRIDE_FILE.disabled" ]; then
            mv "$OVERRIDE_FILE.disabled" "$OVERRIDE_FILE"
        fi
        info "Building the Reticora images (this can take a few minutes) …"
        compose_cmd build server frontend || die "Image build failed."
        # PostgreSQL with pgBackRest (WP-100).
        info "Building the PostgreSQL image with pgBackRest …"
        compose_cmd build postgres || die "Building the PostgreSQL image failed."
    fi
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        compose_cmd pull certbot || die "Pulling the certbot image failed."
    fi
    success "Server and frontend images are available"
}

# ─── Container logs: 30 days (NFR-06, E-77) ───────────────────────────────────
# The services log to the journal of the host (docker-compose.yml, logging
# driver journald). Where systemd-journald runs, the drop-in reticora.conf
# limits the journal: MaxRetentionSec=30day deletes a journal file once its
# first entry is older than 30 days, MaxFileSec=1day starts a new file every
# day, so the entries go after about 30 days. The drop-in applies to the
# whole journal of the host, not only to Reticora. A key that the host's
# configuration already holds at least as strictly (at most 30 days, at most
# one day; also a stricter value written into reticora.conf by the operator)
# stays in effect and is left out. Without systemd-journald (e.g. Docker in a
# container without systemd) the journald driver cannot start a container:
# .env then selects docker-compose.logging-json.yml (json-file rotation,
# bounded in size, not in age) through COMPOSE_FILE.
# Root of the journald configuration (tests use a directory of their own).
JOURNALD_ROOT=""
JOURNALD_DROPIN="reticora.conf"
LOG_ROTATION_MARKER="# Container logs without systemd-journald: json-file rotation (NFR-06, docs/ops/workers.md)"

# names_log_rotation <COMPOSE_FILE value> — the value names the json-file
# log rotation.
names_log_rotation() {
    local name="${LOGGING_JSON_FILE##*/}"
    case ":$1:" in
        *":$name:"*|*"/$name:"*) return 0 ;;
    esac
    return 1
}

# log_rotation_selected — .env selects the json-file log rotation.
log_rotation_selected() {
    local files
    files="$(env_get COMPOSE_FILE)" || return 1
    names_log_rotation "$files"
}

# journald_running — systemd-journald runs on this host (a container without
# systemd has no systemctl or answers "not booted with systemd").
journald_running() {
    have_cmd systemctl && systemctl is-active --quiet systemd-journald >/dev/null 2>&1
}

# timespan_seconds <value> — a systemd time span (systemd.time(7): 30day,
# 1month, "2w 3d", a bare number of seconds) in whole seconds; 0 for an empty
# value, 0 or infinity (no limit). Fails on a value journald does not accept.
timespan_seconds() {
    printf '%s\n' "$1" | awk '
        function unit(u) {
            if (u == "" || u ~ /^(s|sec|second|seconds)$/) return 1
            if (u ~ /^(ms|msec)$/) return 0.001
            if (u ~ /^(us|usec)$/) return 0.000001
            if (u ~ /^(m|min|minute|minutes)$/) return 60
            if (u ~ /^(h|hr|hour|hours)$/) return 3600
            if (u ~ /^(d|day|days)$/) return 86400
            if (u ~ /^(w|week|weeks)$/) return 604800
            if (u ~ /^(M|month|months)$/) return 2629800
            if (u ~ /^(y|year|years)$/) return 31557600
            return -1
        }
        NR == 1 {
            s = $0
            gsub(/[ \t]/, "", s)
            if (s == "" || s == "infinity") { print 0; exit }
            total = 0
            while (s != "") {
                if (!match(s, /^[0-9]+(\.[0-9]+)?/)) { bad = 1; exit }
                n = substr(s, 1, RLENGTH); s = substr(s, RLENGTH + 1)
                match(s, /^[A-Za-z]*/); u = substr(s, 1, RLENGTH); s = substr(s, RLENGTH + 1)
                f = unit(u)
                if (f < 0) { bad = 1; exit }
                total += n * f
            }
            printf "%d\n", total
        }
        END { exit bad }'
}

# journald_conf_value <file> <key> — the value of the last <key>= line of a
# journald configuration file; fails when the file does not set <key>.
journald_conf_value() {
    [ -f "$1" ] && grep -qE "^[[:space:]]*$2[[:space:]]*=" "$1" 2>/dev/null || return 1
    trim "$(sed -nE "s/^[[:space:]]*$2[[:space:]]*=//p" "$1" | tail -n 1)"
}

# journald_setting <key> [drop-in name to leave out] — the value of <key>
# that systemd-journald applies, in seconds (0: not set, no limit):
# journald.conf, then the drop-ins of all journald.conf.d directories sorted
# by file name (of equal names the one in /etc before /run, /usr/local/lib,
# /usr/lib; a link to /dev/null masks it), the last setting wins
# (journald.conf(5)). An unparsable value is ignored like journald does.
journald_setting() {
    local key="$1" skip="${2:-}" dir f name raw secs=0 s
    local -a files=()
    local -A dropins=()
    for dir in /etc /run /usr/local/lib /usr/lib; do
        if [ -f "$JOURNALD_ROOT$dir/systemd/journald.conf" ]; then
            files+=("$JOURNALD_ROOT$dir/systemd/journald.conf")
            break
        fi
    done
    for dir in /usr/lib /usr/local/lib /run /etc; do
        for f in "$JOURNALD_ROOT$dir/systemd/journald.conf.d/"*.conf; do
            [ -e "$f" ] || [ -L "$f" ] || continue
            name="${f##*/}"
            [ "$name" != "$skip" ] || continue
            dropins["$name"]="$f"
        done
    done
    while IFS= read -r name; do
        [ -n "$name" ] && files+=("${dropins[$name]}")
    done < <(printf '%s\n' "${!dropins[@]}" | LC_ALL=C sort)
    for f in ${files[@]+"${files[@]}"}; do
        raw="$(journald_conf_value "$f" "$key")" || continue
        s="$(timespan_seconds "$raw")" && secs="$s"
    done
    printf '%s\n' "$secs"
}

# restart_journald — systemd-journald reads its configuration only at start.
restart_journald() {
    $SUDO systemctl restart systemd-journald \
        || warn "Could not restart systemd-journald; the log retention applies from its next start ($SUDO systemctl restart systemd-journald)."
}

# write_journald_dropin — the drop-in /etc/systemd/journald.conf.d/
# reticora.conf with MaxRetentionSec=30day and MaxFileSec=1day, each only
# where the host's configuration without it is not at least as strict;
# systemd-journald is restarted when the drop-in changed. Fails when it
# cannot be written (root or sudo needed).
write_journald_dropin() {
    local dir="$JOURNALD_ROOT/etc/systemd/journald.conf.d" file spec key value target own_raw own other
    local -a lines=() kept=()
    file="$dir/$JOURNALD_DROPIN"
    for spec in "MaxRetentionSec 30day 2592000" "MaxFileSec 1day 86400"; do
        read -r key value target <<<"$spec"
        # A stricter value the operator wrote into reticora.conf is kept.
        if own_raw="$(journald_conf_value "$file" "$key")" && own="$(timespan_seconds "$own_raw")" \
            && [ "$own" -gt 0 ] && [ "$own" -lt "$target" ]; then
            value="$own_raw"
            target="$own"
        fi
        other="$(journald_setting "$key" "$JOURNALD_DROPIN")"
        if [ "$other" -gt 0 ] && [ "$other" -le "$target" ]; then
            kept+=("$key")
        else
            lines+=("$key=$value")
        fi
    done
    if [ "${#kept[@]}" -gt 0 ]; then
        info "Container logs: the journald configuration of this host already sets ${kept[*]} at least as strictly; it stays in effect."
    fi
    if [ "${#lines[@]}" -eq 0 ]; then
        if [ -e "$file" ]; then
            $SUDO rm -f "$file" || return 1
            restart_journald
        fi
    else
        local content
        content="$(printf '%s\n' \
            "# Written by install-cloud.sh (Reticora CMDB, NFR-06): container logs of the" \
            "# Reticora stack are kept 30 days. Applies to the whole journal of this host." \
            "# A stricter value in reticora.conf or another drop-in is kept by the" \
            "# installer; see docs/ops/workers.md, \"Container logs\"." \
            "[Journal]" "${lines[@]}")"
        if [ "$(cat "$file" 2>/dev/null)" != "$content" ]; then
            { $SUDO mkdir -p "$dir" && printf '%s\n' "$content" | $SUDO tee "$file" >/dev/null && $SUDO chmod 644 "$file"; } 2>/dev/null \
                || return 1
            restart_journald
            warn "Container logs: wrote $file (${lines[*]}). It applies to the whole journal of this host, not only to Reticora: systemd-journald deletes every entry after at most 30 days (docs/ops/workers.md, 'Container logs')."
        fi
    fi
    other="$(journald_setting MaxRetentionSec)"
    if [ "$other" -eq 0 ] || [ "$other" -gt 2592000 ]; then
        warn "Container logs: a journald drop-in sorting after $JOURNALD_DROPIN sets a longer MaxRetentionSec; the container logs are kept longer than 30 days (NFR-06, docs/ops/workers.md)."
    else
        success "Container logs: systemd-journald keeps them at most 30 days"
    fi
}

# set_log_rotation <on|off> — COMPOSE_FILE in .env selects the json-file log
# rotation (on: the files of compose_cmd plus docker-compose.logging-json.yml,
# relative to deploy/docker-compose) or nothing (off). A COMPOSE_FILE of the
# operator without the rotation stays.
set_log_rotation() {
    local tmp files
    tmp="$(mktemp)"
    # The managed lines go with the blank line set_log_rotation put before them.
    if ! awk -v marker="$LOG_ROTATION_MARKER" -v name="${LOGGING_JSON_FILE##*/}" '
        $0 == "" { blank++; next }
        $0 == marker { blank = 0; next }
        index($0, "COMPOSE_FILE=") == 1 && (index(":" substr($0, 14) ":", ":" name ":") || index(substr($0, 14) ":", "/" name ":")) { next }
        { for (; blank > 0; blank--) print ""; print }
        END { for (; blank > 0; blank--) print "" }' "$ENV_FILE" > "$tmp" || ! mv "$tmp" "$ENV_FILE"; then
        rm -f "$tmp"
        die "Could not update COMPOSE_FILE in $ENV_FILE."
    fi
    if [ "$1" = on ]; then
        env_get COMPOSE_FILE >/dev/null \
            && die "COMPOSE_FILE in $ENV_FILE selects other compose files. This host has no systemd-journald, so the installer selects the json-file log rotation there (${LOGGING_JSON_FILE##*/}); remove COMPOSE_FILE from $ENV_FILE and re-run (docs/ops/workers.md, 'Container logs')."
        files="${COMPOSE_FILE##*/}"
        [ -f "$OVERRIDE_FILE" ] && files="$files:${OVERRIDE_FILE##*/}"
        files="$files:${LOGGING_JSON_FILE##*/}"
        printf '\n%s\nCOMPOSE_FILE=%s\n' "$LOG_ROTATION_MARKER" "$files" >> "$ENV_FILE"
    fi
    chmod 600 "$ENV_FILE"
}

# configure_log_retention — container logs at most 30 days (NFR-06): the
# journald drop-in where systemd-journald runs, otherwise the json-file
# rotation through COMPOSE_FILE in .env. Runs after provide_images, which
# settles whether the source-build override is in use.
configure_log_retention() {
    if journald_running; then
        if write_journald_dropin; then
            set_log_rotation off
            return 0
        fi
        warn "Container logs: could not write $JOURNALD_ROOT/etc/systemd/journald.conf.d/$JOURNALD_DROPIN (root or sudo needed); using the json-file rotation instead."
    else
        info "Container logs: no running systemd-journald on this host."
    fi
    set_log_rotation on
    warn "Container logs: json-file rotation (${LOGGING_JSON_FILE##*/}, 5 files of 20 MB per container), selected through COMPOSE_FILE in $ENV_FILE. It bounds the size, not the age of the logs; the 30 days of NFR-06 need systemd-journald (docs/ops/workers.md, 'Container logs')."
}

# ─── Stack startup ────────────────────────────────────────────────────────────
wait_for_service() {
    local service="$1" attempts=60
    local cid status
    cid="$(compose_cmd ps -q "$service" 2>/dev/null || true)"
    [ -n "$cid" ] || return 1
    while [ "$attempts" -gt 0 ]; do
        status="$($DOCKER inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || echo unknown)"
        case "$status" in
            healthy|running) return 0 ;;
            unhealthy|exited|dead) return 1 ;;
            # A restarting container crash-loops (e.g. the frontend exiting on
            # an nginx '[emerg]' config error) and will never become healthy;
            # fail fast instead of waiting out the whole timeout.
            restarting) return 1 ;;
        esac
        sleep 2
        attempts=$((attempts - 1))
    done
    return 1
}

# show_service_logs <service> — print the tail of a service's log so the
# failure reason (e.g. an unreadable session key) is visible immediately
# instead of hidden behind a separate `docker compose logs` invocation.
show_service_logs() {
    local service="$1"
    warn "Last log lines of the '$service' container:"
    compose_cmd logs --tail 40 "$service" >&2 || true
}

# db_volume_name [service] [volume suffix] — prints the name of the Docker
# volume holding the data directory of a PostgreSQL service (default: the
# main database, <project>_pgdata), resolved from the running container's
# mounts (falling back to the compose project name).
db_volume_name() {
    local service="${1:-postgres}" suffix="${2:-pgdata}" cid project name
    cid="$(compose_cmd ps -q "$service" 2>/dev/null || true)"
    if [ -n "$cid" ]; then
        name="$($DOCKER inspect --format '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}' \
            "$cid" 2>/dev/null || true)"
        if [ -n "$name" ]; then
            printf '%s' "$name"
            return 0
        fi
    fi
    project="$("${COMPOSE[@]}" --env-file "$ENV_FILE" -f "$COMPOSE_FILE" config --format json 2>/dev/null \
        | sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
    printf '%s_%s' "${project:-docker-compose}" "$suffix"
}

# db_image [service] — prints the container image configured for a
# PostgreSQL service, so the temporary password-reset instance runs exactly
# the same PostgreSQL version as the real stack (an older binary refuses a
# newer data directory).
db_image() {
    local service="${1:-postgres}" fallback img
    case "$service" in
        postgres) fallback="reticora-postgres:latest" ;;
        *) fallback="postgres:16.15-alpine@sha256:aa6cb33cf4c19c765cefcf48d96176258800870a9a1e930ad126200f9d825139" ;;
    esac
    img="$("${COMPOSE[@]}" --env-file "$ENV_FILE" -f "$COMPOSE_FILE" config 2>/dev/null \
        | awk -v svc="$service" '$0 == "  " svc ":" {f=1; next} f && /^  [^ ]/ {exit} f && /^    image:/ {print $2; exit}')"
    printf '%s' "${img:-$fallback}"
}

# db_password_matches <service> <role> <database> <password> — probes the
# password over TCP (INS-05). The postgres images trust every connection on
# the local socket and on 127.0.0.1, so only a connection to the service's
# network address proves the password.
db_password_matches() {
    local service="$1" role="$2" database="$3" password="$4"
    compose_cmd exec -T -e PGPASSWORD="$password" -e PGCONNECT_TIMEOUT=5 "$service" \
        psql -h "$service" -U "$role" -d "$database" -tAc 'SELECT 1' >/dev/null 2>&1
}

# sync_db_password — the postgres image applies POSTGRES_PASSWORD only when it
# initializes an *empty* data directory. When the installer is re-run with a
# changed password, the persistent volume still carries the previous one, so
# every connection fails with SQLSTATE 28P01 (password authentication failed)
# and the server or Keycloak never becomes healthy. Both databases — the
# CMDB (RETICORA_DB_PASSWORD) and Keycloak's (KEYCLOAK_DB_PASSWORD) — are
# probed and, on mismatch, resynced to .env (INS-05).
sync_db_password() {
    sync_role_password postgres reticora reticora "$RETICORA_DB_PASSWORD" pgdata
    sync_role_password keycloak-db keycloak keycloak "$KEYCLOAK_DB_PASSWORD" keycloakdb
}

# sync_role_password <service> <role> <database> <password> <volume suffix>
sync_role_password() {
    local service="$1" role="$2" database="$3" password="$4" suffix="$5"
    info "Checking the password of database role '$role' ($service) …"
    if db_password_matches "$service" "$role" "$database" "$password"; then
        success "Password of '$role' matches the configuration"
        return 0
    fi

    warn "The existing '$service' volume still uses a different password for '$role' (SQLSTATE 28P01);"
    warn "PostgreSQL ignores POSTGRES_PASSWORD once its data directory is initialized."
    info "Updating the password of database role '$role' to the configured value …"

    # Start a temporary PostgreSQL instance on the same data volume, but with
    # 'trust' authentication for local connections (via a throwaway hba config
    # outside the volume) so the role password can be changed without knowing
    # the old one. The new password is passed through the PGPASSWORD
    # environment variable and a psql variable, never on a command line.
    local data_vol image container="reticora-db-pwreset"
    data_vol="$(db_volume_name "$service" "$suffix")"
    image="$(db_image "$service")"
    compose_cmd rm -sf "$service" >/dev/null 2>&1 || true
    $DOCKER run -d --rm --name "$container" \
        --entrypoint sh \
        -v "$data_vol:/var/lib/postgresql/data" \
        -e POSTGRES_PASSWORD="$password" \
        -e RESET_HBA='local all all trust
host all all all scram-sha-256' \
        "$image" \
        -c 'mkdir -p /etc/reticora-pwreset && printf "%s\n" "$RESET_HBA" > /etc/reticora-pwreset/pg_hba.conf && chmod 644 /etc/reticora-pwreset/pg_hba.conf && exec docker-entrypoint.sh postgres -c hba_file=/etc/reticora-pwreset/pg_hba.conf' \
        >/dev/null 2>&1 \
        || die "Cannot start the temporary PostgreSQL instance on volume '$data_vol' — is another postgres container still running?"

    local ok=1
    for _ in $(seq 1 30); do
        # The password travels only via stdin: the \set meta-command reads it
        # from the PGPASSWORD environment variable inside psql, so it never
        # appears in a process list or on a command line.
        if printf '%s\n' '\set pw `printenv PGPASSWORD`' "ALTER ROLE \"$role\" WITH PASSWORD :'pw'" | \
            $DOCKER exec -i -e PGPASSWORD="$password" "$container" \
                psql -U "$role" -d "$database" -v ON_ERROR_STOP=1 -q >/dev/null 2>&1; then
            ok=0
            break
        fi
        sleep 2
    done
    $DOCKER rm -f "$container" >/dev/null 2>&1 || true
    if [ "$ok" -ne 0 ]; then
        die "Could not reset the password of '$role'. Remove the stale volume with '$DOCKER volume rm $data_vol' (its data is lost) and re-run this installer."
    fi
    success "Password of '$role' updated"

    info "Restarting $service with normal authentication …"
    compose_cmd up -d "$service"
    wait_for_service "$service" || die "$service did not become healthy."
    if db_password_matches "$service" "$role" "$database" "$password"; then
        success "Authentication of '$role' verified over TCP"
    else
        die "Authentication of '$role' still fails after the password reset. Remove the stale volume with '$DOCKER volume rm $data_vol' (its data is lost) and re-run this installer."
    fi
}

start_stack() {
    info "Starting the infrastructure services …"
    compose_cmd up -d postgres nats redis minio keycloak-db
    wait_for_service postgres || die "PostgreSQL did not become healthy."
    wait_for_service keycloak-db || die "Keycloak PostgreSQL did not become healthy."
    success "Database and infrastructure services are running"

    sync_db_password

    # The server of the previous run must not work on the database while its
    # TimescaleDB extension is updated and the schema migrated (WP-604).
    info "Stopping the Reticora server for the database update …"
    compose_cmd stop server || die "Could not stop the Reticora server before the database update."
    update_timescaledb

    run_migrations
    seed_default_organization
    ensure_runtime_login

    info "Starting the backup service (pgBackRest) …"
    compose_cmd up -d backup
    wait_for_service backup || {
        show_service_logs backup
        die "The backup service did not become healthy — no base backup could be taken. See docs/backup-dr.md."
    }
    success "WAL archiving and base backups are active"

    info "Starting Keycloak …"
    compose_cmd up -d keycloak
    wait_for_service keycloak || {
        show_service_logs keycloak
        warn "Keycloak did not become healthy. Without it the login screen cannot be"
        warn "served: the web UI answers /realms/ requests with 502 Bad Gateway."
        warn "A common cause is an unreadable bind-mounted realm file"
        warn "(java.nio.file.AccessDeniedException); re-running this installer re-renders"
        warn "deploy/docker-compose/.generated/realm-reticora.json with container-readable"
        warn "permissions."
        die "Keycloak failed to start — see the log output above."
    }
    validate_keycloak_bootstrap
    create_initial_admin
    # The server reads its admin client only at start: it must exist in
    # Keycloak, with the secret of .env, before the server (re)starts.
    ensure_admin_client
    apply_realm_smtp

    info "Starting the Reticora server and frontend …"
    # Compose itself can fail while waiting for the server healthcheck before
    # starting the dependent frontend. Under set -e that used to bypass the
    # diagnostics below and hide the actual startup error.
    compose_cmd up -d server frontend || {
        show_service_logs server
        show_service_logs frontend
        die "Reticora server or frontend failed to start — see the container logs above."
    }
    wait_for_service server || {
        show_service_logs server
        die "Reticora server did not become healthy — inspect the logs: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs server"
    }
    wait_for_service frontend || {
        show_service_logs frontend
        die "Frontend did not become healthy — inspect the logs: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs frontend"
    }
    restart_frontend_after_server_start
    # Serve a regenerated bootstrap certificate before the issuer checks below.
    reload_frontend_after_bootstrap
    # The collector NATS endpoint (mTLS, port 4222) starts once the server
    # has written the collector PKI into the shared volume. It reads the
    # generated pki/edge.conf only at start, so it is recreated on every run:
    # on an existing installation the edge.conf the updated server just wrote
    # (e.g. the system login that cuts disabled collectors, COL-04) applies.
    info "Starting the collector NATS endpoint …"
    compose_cmd up -d --force-recreate nats-edge
    wait_for_service nats-edge || {
        show_service_logs nats-edge
        die "Collector NATS endpoint did not become healthy — inspect the logs: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs nats-edge"
    }
    # Start the certbot renewal daemon only when TLS is configured; without a
    # domain it has nothing to do.
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        compose_cmd up -d certbot
    fi
    success "All services are up"

    # The strict issuer checks (INS-02) run only now: the default issuer
    # https://<domain>/realms/reticora is served by the frontend's /realms/
    # proxy, which cannot answer before the frontend runs (F-035).
    check_issuer_from_host
    check_issuer_from_container
}

# container_started_at <container id> — when the container last started, in
# nanoseconds since the epoch.
container_started_at() {
    local started
    started="$($DOCKER inspect --format '{{.State.StartedAt}}' "$1" 2>/dev/null)" && [ -n "$started" ] || return 1
    date -d "$started" +%s%N 2>/dev/null
}

# restart_frontend_after_server_start — nginx in the frontend resolves the
# address of the server container once, when it starts (proxy_pass
# http://server:8080, frontend/nginx.conf). Every run stops the server for
# the database update, and `up -d server frontend` starts or recreates it
# (e.g. with a changed SMTP setting), possibly under a new address, but
# leaves a running, unchanged frontend alone: its /api/ would answer 502. A
# frontend that started before the server's current start is restarted
# (as frontend/e2e/fixtures/setup-onboarding.sh does).
restart_frontend_after_server_start() {
    local server frontend server_start frontend_start
    server="$(compose_cmd ps -q server 2>/dev/null || true)"
    frontend="$(compose_cmd ps -q frontend 2>/dev/null || true)"
    [ -n "$server" ] && [ -n "$frontend" ] || return 0
    server_start="$(container_started_at "$server")" || return 0
    frontend_start="$(container_started_at "$frontend")" || return 0
    [ "$frontend_start" -lt "$server_start" ] || return 0
    info "Restarting the frontend: its nginx still holds the address of the server from before the server's start …"
    compose_cmd restart frontend >/dev/null || {
        show_service_logs frontend
        die "Could not restart the frontend after the server start — inspect the logs: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs frontend"
    }
    wait_for_service frontend || {
        show_service_logs frontend
        die "Frontend did not become healthy after its restart — inspect the logs: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs frontend"
    }
    success "Frontend restarted: /api/ reaches the started server"
}

# update_timescaledb — a database created with an older image keeps its
# TimescaleDB extension version; scripts/ops/timescaledb-update.sh raises it
# to the version of the current image (WP-604, INS-05), each statement in a
# psql -X session of its own as the bootstrap superuser, with the event
# trigger reticora_restrict_app_ddl disabled only around the update. A current
# extension is left alone. Runs with the server stopped, before the
# migrations.
update_timescaledb() {
    info "Checking the TimescaleDB extension version …"
    # The compose files of compose_cmd.
    local files=(-f "$COMPOSE_FILE")
    if [ -f "$OVERRIDE_FILE" ]; then
        files+=(-f "$OVERRIDE_FILE")
    fi
    bash "$SCRIPT_DIR/scripts/ops/timescaledb-update.sh" --database reticora -- \
        "${COMPOSE[@]}" --env-file "$ENV_FILE" "${files[@]}" exec -T postgres psql -U reticora \
        || die "The TimescaleDB extension update failed — see docs/troubleshooting-installation.md, 'TimescaleDB extension update failed'."
    success "TimescaleDB extension is current"
}

run_migrations() {
    info "Applying database migrations …"
    local sql_files=()
    while IFS= read -r -d '' f; do
        sql_files+=("$(basename "$f")")
    done < <(find "$SCRIPT_DIR/backend/migrations" -maxdepth 1 -name '*.up.sql' -print0 | sort -z)

    [ "${#sql_files[@]}" -gt 0 ] || die "No migrations found in backend/migrations."

    local db_url="postgres://reticora:${RETICORA_DB_PASSWORD}@localhost:5432/reticora?sslmode=disable"
    if have_cmd migrate; then
        migrate -path "$SCRIPT_DIR/backend/migrations" -database "$db_url" up \
            || die "Database migrations failed."
    else
        # Fallback: apply the migrations with psql inside the postgres
        # container. *.up.sql files are applied in name order, exactly like
        # golang-migrate would. Applied versions are tracked in a bookkeeping
        # table so re-runs are no-ops.
        compose_cmd exec -T postgres psql -U reticora -d reticora -v ON_ERROR_STOP=1 -q <<'SQL'
CREATE TABLE IF NOT EXISTS public.reticora_schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
SQL
        local f version applied
        for f in "${sql_files[@]}"; do
            version="${f%%_*}"
            applied="$(compose_cmd exec -T postgres psql -U reticora -d reticora -tAc \
                "SELECT 1 FROM public.reticora_schema_migrations WHERE version = '$version'" || true)"
            if [ "$applied" = "1" ]; then
                continue
            fi
            info "  → migration $f"
            # One transaction per file, like golang-migrate: a failed migration
            # leaves nothing behind, and ON COMMIT DROP temp tables live until
            # the end of the file instead of the end of their statement.
            compose_cmd exec -T postgres psql -U reticora -d reticora -v ON_ERROR_STOP=1 -q --single-transaction \
                < "$SCRIPT_DIR/backend/migrations/$f" \
                || die "Migration $f failed."
            compose_cmd exec -T postgres psql -U reticora -d reticora -q -c \
                "INSERT INTO public.reticora_schema_migrations (version) VALUES ('$version')"
        done
    fi
    success "Database schema is up to date"
}

# seed_default_organization makes sure the organization row exists that the
# bundled Keycloak realm assigns its users to (the UUID-named group
# 00000000-0000-0000-0000-000000000001). Migration 000054 seeds it for new
# installs; this step additionally repairs existing installations whose
# migration bookkeeping was already current while the row was still missing —
# without it the first login fails with
#   identity: provision user: ensure oidc user: ERROR: insert or update on
#   table "app_user" violates foreign key constraint "app_user_organization_id_fkey"
# Runs as the database superuser, so the org_isolation RLS policy does not
# apply; the INSERT is idempotent and a re-run leaves existing rows untouched.
# A newly inserted row gets its default client (slug default, named like the
# organization) from the trigger trg_organization_default_client (migration
# 000282, WP-854); an existing row got it from that migration's backfill.
seed_default_organization() {
    info "Ensuring the default organization exists …"
    compose_cmd exec -T postgres psql -U reticora -d reticora -v ON_ERROR_STOP=1 -q \
        -c "INSERT INTO organization (id, name, slug) VALUES ('00000000-0000-0000-0000-000000000001', 'Reticora Demo', 'reticora-demo') ON CONFLICT (id) DO NOTHING" \
        || die "Could not seed the default organization."
    success "Default organization is present"
}

# ─── Runtime database login (TEN-03, CH19) ───────────────────────────────────
# The server must not log in as the bootstrap superuser: NewPool switches into
# reticora_app with SET ROLE, and a RESET ROLE would hand every pooled session
# SUPERUSER and BYPASSRLS back (F-057). The server therefore logs in as
# reticora_runtime — LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE,
# member of reticora_app and of no other role. The superuser keeps running the
# migrations, the seed and the backups.
RUNTIME_DB_ROLE="reticora_runtime"

# runtime_login_sql <password> — psql script that creates or repairs the
# runtime login. Idempotent: an existing role (also one created by hand with
# more rights) is reset to the attributes above, loses every other
# membership and gets the configured password, so a re-run rotates it.
runtime_login_sql() {
    cat <<SQL
\\set pw '$1'
SET client_min_messages = warning;
SELECT 'CREATE ROLE $RUNTIME_DB_ROLE' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$RUNTIME_DB_ROLE') \\gexec
ALTER ROLE $RUNTIME_DB_ROLE WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION INHERIT PASSWORD :'pw';
SELECT format('REVOKE %I FROM $RUNTIME_DB_ROLE GRANTED BY %I', r.rolname, pg_get_userbyid(m.grantor))
  FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.roleid
 WHERE m.member = '$RUNTIME_DB_ROLE'::regrole AND r.rolname <> 'reticora_app' \\gexec
GRANT reticora_app TO $RUNTIME_DB_ROLE;
SQL
}

# ensure_runtime_login — runs after the migrations, which create reticora_app
# (000056). The password travels only on psql's stdin, never on a command
# line; sync_role_password then proves it over TCP like the other logins.
ensure_runtime_login() {
    info "Ensuring the runtime database login '$RUNTIME_DB_ROLE' …"
    runtime_login_sql "$RETICORA_DB_APP_PASSWORD" | \
        compose_cmd exec -T postgres psql -U reticora -d reticora -v ON_ERROR_STOP=1 -q --single-transaction >/dev/null \
        || die "Could not create the runtime database login '$RUNTIME_DB_ROLE'."
    sync_role_password postgres "$RUNTIME_DB_ROLE" reticora "$RETICORA_DB_APP_PASSWORD" pgdata
    success "The server logs in as '$RUNTIME_DB_ROLE' (no SUPERUSER, no BYPASSRLS)"
}

# ─── Initial administrator ────────────────────────────────────────────────────
# Organization the first administrator belongs to (seed_default_organization).
DEFAULT_ORGANIZATION_ID="00000000-0000-0000-0000-000000000001"
# Demo account of earlier releases, whose password is publicly known.
LEGACY_DEMO_ADMIN="admin@reticora.local"
# Set by create_initial_admin when it created the administrator; shown once
# by print_summary and never written to disk.
INITIAL_ADMIN_PASSWORD=""

# generate_initial_password — 24 random alphanumeric characters plus one
# character of each class the realm password policy requires.
generate_initial_password() {
    local core
    core="$(openssl rand -base64 64 | tr -dc 'A-Za-z0-9' | head -c 24)"
    [ "${#core}" -eq 24 ] || return 1
    printf '%sAa1-' "$core"
}

# kcadm <args…> — runs the Keycloak admin CLI inside the keycloak container.
kcadm() {
    compose_cmd exec -T keycloak /opt/keycloak/bin/kcadm.sh "$@"
}

# kc_user_id <username> [realm] — prints the id of the user in the realm
# (default reticora), empty if absent.
kc_user_id() {
    kcadm get users -r "${2:-reticora}" -q username="$1" -q exact=true --fields id --format csv --noquotes 2>/dev/null | head -n 1
}

# ─── Rotation on a re-run (INS-05) ───────────────────────────────────────────
# Keycloak takes KEYCLOAK_ADMIN_PASSWORD only when it creates the master
# administrator at its first start, and `start --import-realm` skips an
# existing realm: neither a rotated admin password nor a rotated
# RETICORA_OIDC_CLIENT_SECRET or a new redirect URL would reach an existing
# installation (F-125). apply_keycloak_rotation applies them through the
# admin CLI. New values travel on kcadm's stdin (-f -), no secret is logged.
KC_ROTATION_TROUBLESHOOTING="see docs/troubleshooting-installation.md, 'Keycloak admin login or rotation failed'"

# .env settings that reach Keycloak (the redirect URL and the issuer feed the
# client's redirect URIs and web origins; the secret of the server's admin
# client is applied by ensure_admin_client).
KC_ROTATED_SETTINGS=(KEYCLOAK_ADMIN_PASSWORD RETICORA_OIDC_CLIENT_SECRET RETICORA_OIDC_REDIRECT_URL RETICORA_OIDC_ISSUER_URL
    RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET)

# report_rotated_settings — names (never values) the settings of
# KC_ROTATED_SETTINGS whose value differs from the one in .env.previous.
report_rotated_settings() {
    local key previous names=()
    for key in "${KC_ROTATED_SETTINGS[@]}"; do
        previous="$(env_get "$key" "$ENV_FILE.previous")" || continue
        [ "$previous" = "${!key:-}" ] || names+=("$key")
    done
    [ "${#names[@]}" -eq 0 ] || info "Changed since the last run: ${names[*]} — applying to Keycloak"
}

# kc_login <password> — logs the admin CLI in as KEYCLOAK_ADMIN. (kcadm's
# password prompt on stdin loses its input now and then and hangs.)
kc_login() {
    kcadm config credentials --server http://localhost:8080 --realm master \
        --user "$KEYCLOAK_ADMIN" --password "$1" >/dev/null
}

# kc_admin_login — logs in with the previous admin password (.env.previous),
# then with KEYCLOAK_ADMIN_PASSWORD. A login with the previous password sets
# the configured one, so the old password stops working.
kc_admin_login() {
    local previous user_id
    previous="$(env_get KEYCLOAK_ADMIN_PASSWORD "$ENV_FILE.previous" || true)"
    if [ -n "$previous" ] && [ "$previous" != "$KEYCLOAK_ADMIN_PASSWORD" ] && kc_login "$previous" 2>/dev/null; then
        user_id="$(kc_user_id "$KEYCLOAK_ADMIN" master)"
        [ -n "$user_id" ] || die "The Keycloak administrator $KEYCLOAK_ADMIN is missing in the master realm ($KC_ROTATION_TROUBLESHOOTING)."
        printf '%s' "$KEYCLOAK_ADMIN_PASSWORD" | jq -Rs '{type: "password", value: ., temporary: false}' \
            | kcadm update "users/$user_id/reset-password" -r master -f - >/dev/null \
            || die "Could not set the rotated KEYCLOAK_ADMIN_PASSWORD for $KEYCLOAK_ADMIN ($KC_ROTATION_TROUBLESHOOTING)."
        kc_login "$KEYCLOAK_ADMIN_PASSWORD" \
            || die "Keycloak does not accept the rotated KEYCLOAK_ADMIN_PASSWORD for $KEYCLOAK_ADMIN ($KC_ROTATION_TROUBLESHOOTING)."
        success "Keycloak rotation: password of the administrator $KEYCLOAK_ADMIN set to KEYCLOAK_ADMIN_PASSWORD"
        return 0
    fi
    kc_login "$KEYCLOAK_ADMIN_PASSWORD" \
        || die "Could not log in to the Keycloak admin API as $KEYCLOAK_ADMIN with KEYCLOAK_ADMIN_PASSWORD${previous:+ or the previous password of $ENV_FILE.previous} ($KC_ROTATION_TROUBLESHOOTING)."
}

# Client attributes of the rendered realm that sync_realm_client keeps like
# the redirect URIs: post.logout.redirect.uris ("+", the valid redirect URIs)
# lets the sign-out of the SPA return from Keycloak's end-session endpoint to
# its sign-in page (RP-initiated logout, F-351). A realm imported by an
# earlier release lacks it (Keycloak 24 then falls back to the redirect URIs);
# a value set in the console, such as "-" (none), is reset like them.
KC_CLIENT_ATTRIBUTES='["post.logout.redirect.uris"]'

# kc_client_drift <realm file> <clientId> — reads the client of the running
# realm (JSON) on stdin and prints each of secret, redirectUris, webOrigins
# and the attributes of KC_CLIENT_ATTRIBUTES the rendered realm sets whose
# value differs from the rendered realm; lists compare as sets, the way
# Keycloak stores them. Other attributes are not compared.
kc_client_drift() {
    jq -r --arg id "$2" --slurpfile realm "$1" --argjson attrs "$KC_CLIENT_ATTRIBUTES" '
        def norm: if type == "array" then unique else . end;
        . as $have
        | ($realm[0].clients[] | select(.clientId == $id)) as $want
        | (("secret", "redirectUris", "webOrigins") | select(($want[.] | norm) != ($have[.] | norm))),
          ($attrs[] | select(($want.attributes // {})[.] != null
            and ($want.attributes // {})[.] != ($have.attributes // {})[.]))'
}

# sync_realm_client — applies secret, redirectUris, webOrigins and the
# attributes of KC_CLIENT_ATTRIBUTES of each client of the rendered realm that
# has a secret to the running realm: one update per differing field (an
# attribute update names only that attribute, Keycloak keeps the others);
# nothing else of the realm is changed.
sync_realm_client() {
    local realm_file="$COMPOSE_DIR/.generated/realm-reticora.json" client_id uuid current drift field changed=0
    [ -f "$realm_file" ] || die "The rendered realm file $realm_file is missing."
    for client_id in $(jq -r '.clients[] | select(has("secret")) | .clientId' "$realm_file"); do
        uuid="$(kcadm get clients -r reticora -q clientId="$client_id" --fields id --format csv --noquotes 2>/dev/null | head -n 1)"
        [ -n "$uuid" ] || die "The Keycloak realm 'reticora' has no client $client_id ($KC_ROTATION_TROUBLESHOOTING)."
        if ! current="$(kcadm get "clients/$uuid" -r reticora 2>/dev/null)" \
            || ! drift="$(printf '%s' "$current" | kc_client_drift "$realm_file" "$client_id")"; then
            die "Could not read the Keycloak client $client_id ($KC_ROTATION_TROUBLESHOOTING)."
        fi
        for field in $drift; do
            jq -c --arg id "$client_id" --arg f "$field" --argjson attrs "$KC_CLIENT_ATTRIBUTES" '.clients[] | select(.clientId == $id)
                | if ($attrs | index([$f])) then {attributes: {($f): .attributes[$f]}} else {($f): .[$f]} end' "$realm_file" \
                | kcadm update "clients/$uuid" -r reticora -f - >/dev/null \
                || die "Could not apply $field of the rendered realm to the Keycloak client $client_id ($KC_ROTATION_TROUBLESHOOTING)."
            if [ "$field" = secret ]; then
                success "Keycloak rotation: client $client_id secret set to RETICORA_OIDC_CLIENT_SECRET"
            else
                success "Keycloak rotation: client $client_id $field set to $(jq -r --arg id "$client_id" --arg f "$field" \
                    '.clients[] | select(.clientId == $id) | .[$f] // .attributes[$f] | if type == "array" then join(", ") else . end' \
                    "$realm_file")"
            fi
            changed=1
        done
    done
    [ "$changed" -eq 1 ] || success "Keycloak client secret, redirect URIs, web origins and post-logout redirect URIs match the configuration"
}

# apply_keycloak_rotation — logs the admin CLI in (rotating the admin
# password) and applies the client settings of the rendered realm; afterwards
# .env is kept as .env.previous, the baseline of the next run.
apply_keycloak_rotation() {
    info "Applying the Keycloak administrator password and client settings …"
    report_rotated_settings
    kc_admin_login
    sync_realm_client
    [ ! -f "$ENV_FILE" ] || save_env_previous
}

# ─── Realm settings on every run (AUT-09, SEC-07, F-061) ─────────────────────
# `start --import-realm` imports the realm file only while the realm does not
# exist ("Realm 'reticora' already exists. Import skipped"): the password and
# lockout policy, the MFA flow, the roles, groups and the acr scope of a newer
# release never reached an installation whose realm an earlier release
# imported (F-061), and Keycloak 24 dropped the organization_id of every user
# the installer created in such a realm (attribute not declared).
# apply_realm_settings applies them from the rendered realm file on every run
# (first installation and re-run): missing settings are added and weaker
# values raised; stricter values and further settings of the operator are
# kept. Every change is logged; none of these settings is a secret.
KC_REALM_TROUBLESHOOTING="see deploy/keycloak/README.md, 'Upgrade'"

# Password policies of the realm file whose value is a minimum (a higher value
# is stricter); the others (notUsername, notEmail) only have to be present.
KC_POLICY_MINIMUMS='["length","upperCase","lowerCase","digits","specialChars","passwordHistory"]'
# Lockout: fewer failures until the lockout are stricter (failureFactor);
# longer waits and a longer failure memory as well.
KC_LOCKOUT_MAXIMUMS='["failureFactor"]'
KC_LOCKOUT_MINIMUMS='["waitIncrementSeconds","maxFailureWaitSeconds","minimumQuickLoginWaitSeconds","quickLoginCheckMilliSeconds","maxDeltaTimeSeconds"]'

# The client scope acr as Keycloak creates it in every new realm; added only
# when a realm lacks it.
KC_ACR_SCOPE='{"name":"acr","description":"OpenID Connect scope for add acr (authentication context class reference) to the token","protocol":"openid-connect","attributes":{"include.in.token.scope":"false","display.on.consent.screen":"false"},"protocolMappers":[{"name":"acr loa level","protocol":"openid-connect","protocolMapper":"oidc-acr-mapper","consentRequired":false,"config":{"id.token.claim":"true","introspection.token.claim":"true","access.token.claim":"true"}}]}'

# kc_realm_plan <realm file> — reads the running realm (partial export) on
# stdin and prints what apply_realm_settings has to change, as JSON: roles,
# composites, groups, group roles and default client scopes to add, the
# state of the browser flow, the realm fields and attributes to set, and a
# log line per change ("log") and per stricter or different value that is
# kept ("kept"). Flows compare by what Keycloak runs (requirement,
# authenticator or sub-flow, authenticator config), not by names.
kc_realm_plan() {
    jq -c --slurpfile realm "$1" --argjson minimums "$KC_POLICY_MINIMUMS" \
        --argjson lockmax "$KC_LOCKOUT_MAXIMUMS" --argjson lockmin "$KC_LOCKOUT_MINIMUMS" '
        def items: if . == null or . == "" then [] else [splits(" and ") | capture("^(?<name>[^(]+)(\\((?<value>.*)\\))?$")] end;
        def policy: .name + (if (.value // "") == "" then "" else "(\(.value))" end);
        def num: (tonumber? // null);
        def has_name($n): index([$n]) != null;
        def tree($r; $alias):
            ([($r.authenticationFlows // [])[] | select(.alias == $alias) | .authenticationExecutions // []] | first // [])
            | sort_by(.priority)
            | map(. as $e | {requirement: $e.requirement,
                step: (if $e.authenticatorFlow then {flow: tree($r; $e.flowAlias)} else $e.authenticator end),
                config: (if $e.authenticatorConfig then
                    [($r.authenticatorConfig // [])[] | select(.alias == $e.authenticatorConfig) | .config] | first
                    else null end)});
        def subflows($r; $alias):
            [($r.authenticationFlows // [])[] | select(.alias == $alias) | (.authenticationExecutions // [])[]
                | select(.authenticatorFlow) | .flowAlias][] | ., subflows($r; .);
        . as $have | $realm[0] as $want | $want.browserFlow as $fa
        | ([($have.authenticationFlows // [])[] | select(.alias == $fa)] | first) as $hflow
        | ([$fa, subflows($want; $fa)]) as $wanted_flows
        | (if $hflow == null then [] else [$fa, subflows($have; $fa)] end) as $old_flows
        | ($have.passwordPolicy | items) as $hp
        | (reduce ($want.passwordPolicy | items)[] as $w ({p: $hp, log: [], kept: []};
            ([.p[].name] | index([$w.name])) as $i
            | if $i == null then .p += [$w] | .log += ["password policy \($w | policy) added"]
              elif ($minimums | has_name($w.name)) and ($w.value | num) != null then
                (.p[$i].value | num) as $h | ($w.value | num) as $n
                | if $h == null or $h < $n then
                    .log += ["password policy \(.p[$i] | policy) raised to \($w | policy)"] | .p[$i].value = $w.value
                  elif $h > $n then .kept += ["password policy \(.p[$i] | policy) (stricter than \($w | policy))"]
                  else . end
              else . end)) as $pp
        | (reduce ("bruteForceProtected", $lockmax[], $lockmin[]) as $f ({set: {}, log: [], kept: []};
            $want[$f] as $w | $have[$f] as $h
            | if $w == null then .
              elif $f == "bruteForceProtected" then
                (if $w == true and $h != true then .set[$f] = true | .log += ["brute-force detection enabled"] else . end)
              elif ($lockmax | has_name($f)) then
                (if $h == null or $h < 1 or $h > $w then .set[$f] = $w | .log += ["\($f) \($h // "unset") lowered to \($w)"]
                 elif $h < $w then .kept += ["\($f) \($h) (stricter than \($w))"] else . end)
              else
                (if $h == null or $h < $w then .set[$f] = $w | .log += ["\($f) \($h // "unset") raised to \($w)"]
                 elif $h > $w then .kept += ["\($f) \($h) (stricter than \($w))"] else . end)
              end)) as $lock
        | (reduce (($want.attributes // {}) | to_entries[]) as $e ({set: {}, log: [], kept: []};
            (($have.attributes // {})[$e.key]) as $h
            | if $h == null or $h == "" then .set[$e.key] = $e.value | .log += ["attribute \($e.key) set to \($e.value)"]
              elif $h != $e.value then .kept += ["attribute \($e.key) \($h) (bundled: \($e.value))"]
              else . end)) as $attrs
        | {
            roles: [$want.roles.realm[] | select(.name | startswith("default-roles-") | not)
                | select(.name as $n | [($have.roles.realm // [])[].name] | has_name($n) | not) | {name, description}],
            composites: [$want.roles.realm[] | select(.composites.realm) | . as $w
                | ([($have.roles.realm // [])[] | select(.name == $w.name) | .composites.realm // []] | first // []) as $h
                | $w.composites.realm[] | select(. as $c | $h | has_name($c) | not) | {role: $w.name, composite: .}],
            groups: [$want.groups[].name | select(. as $n | [($have.groups // [])[].name] | has_name($n) | not)],
            group_roles: [$want.groups[] | . as $w
                | ([($have.groups // [])[] | select(.name == $w.name)] | first) as $hg
                | ($w.realmRoles // [])[] | select(. as $r | ($hg.realmRoles // []) | has_name($r) | not)
                | {group: $w.name, id: $hg.id, role: .}],
            scopes: [$want.clients[] | select(.defaultClientScopes) | . as $w
                | ([($have.clients // [])[] | select(.clientId == $w.clientId)] | first) as $hc | select($hc != null)
                | $w.defaultClientScopes[] | select(. as $s | ($hc.defaultClientScopes // []) | has_name($s) | not)
                | {client: $w.clientId, client_uuid: $hc.id, scope: .,
                   scope_id: (. as $s | [($have.clientScopes // [])[] | select(.name == $s) | .id] | first)}],
            flow: {
                alias: $fa,
                state: (if $hflow == null then "missing" elif tree($have; $fa) == tree($want; $fa) then "same" else "different" end),
                id: $hflow.id,
                binding: ($have.browserFlow // ""),
                collisions: [($have.authenticationFlows // [])[].alias
                    | select(. as $a | ($wanted_flows | has_name($a)) and ($old_flows | has_name($a) | not))]
            },
            realm: ($lock.set + (if ($pp.log | length) > 0 then {passwordPolicy: ($pp.p | map(policy) | join(" and "))} else {} end)),
            attributes: $attrs.set,
            log: ($pp.log + $lock.log + $attrs.log),
            kept: ($pp.kept + $lock.kept + $attrs.kept)
        }'
}

# kc_call <args…> — kcadm with its messages shown only on failure: it
# reports every created object ("Created new … with id …") and an empty line
# per read on stderr.
kc_call() {
    local err rc=0
    { err="$(kcadm "$@" 2>&1 >&3 3>&-)" || rc=$?; } 3>&1
    [ "$rc" -eq 0 ] || printf '%s\n' "$err" >&2
    return "$rc"
}

# kc_flow_path <alias> — the admin API path of a flow (aliases contain spaces).
kc_flow_path() { printf 'authentication/flows/%s' "$(jq -rn --arg a "$1" '$a | @uri')"; }

# kc_flow_executions <realm file> <alias> — the executions of the flow in the
# realm file, one JSON object per line, in the order of their priorities.
kc_flow_executions() {
    jq -c --arg a "$2" '.authenticationFlows[] | select(.alias == $a) | .authenticationExecutions | sort_by(.priority)[]' "$1"
}

# kc_build_flow <realm file> <alias> — fills the empty flow <alias> as the
# realm file defines it: authenticators and sub-flows in the order of their
# priorities, then the requirement and authenticator config of each (new
# executions are DISABLED); sub-flows recursively. The executions are read
# into an array first: kcadm (docker compose exec) would consume the input of
# a read loop.
kc_build_flow() {
    local realm_file="$1" alias="$2" path ex listing i=0 config sub
    local executions=() ids=()
    path="$(kc_flow_path "$alias")"
    mapfile -t executions < <(kc_flow_executions "$realm_file" "$alias")
    for ex in "${executions[@]}"; do
        if [ "$(jq -r .authenticatorFlow <<<"$ex")" = true ]; then
            jq -c --arg a "$(jq -r .flowAlias <<<"$ex")" \
                '.authenticationFlows[] | select(.alias == $a) | {alias, description, type: .providerId}' "$realm_file" \
                | kc_call create "$path/executions/flow" -r reticora -f - >/dev/null || return 1
        else
            kc_call create "$path/executions/execution" -r reticora -s provider="$(jq -r .authenticator <<<"$ex")" \
                </dev/null >/dev/null || return 1
        fi
    done
    # Keycloak lists the executions of the flow (level 0) in creation order.
    listing="$(kc_call get "$path/executions" -r reticora </dev/null)" || return 1
    mapfile -t ids < <(jq -r '.[] | select(.level == 0) | .id' <<<"$listing")
    [ "${#ids[@]}" -eq "${#executions[@]}" ] || return 1
    for ex in "${executions[@]}"; do
        if [ "$(jq -r .requirement <<<"$ex")" != DISABLED ]; then
            jq -c --arg id "${ids[$i]}" '{id: $id, requirement}' <<<"$ex" \
                | kc_call update "$path/executions" -r reticora -f - >/dev/null || return 1
        fi
        config="$(jq -r '.authenticatorConfig // empty' <<<"$ex")"
        if [ -n "$config" ]; then
            jq -c --arg a "$config" '[.authenticatorConfig[] | select(.alias == $a) | {alias, config}] | first' "$realm_file" \
                | kc_call create "authentication/executions/${ids[$i]}/config" -r reticora -f - >/dev/null || return 1
        fi
        sub="$(jq -r 'select(.authenticatorFlow) | .flowAlias' <<<"$ex")"
        if [ -n "$sub" ]; then
            kc_build_flow "$realm_file" "$sub" || return 1
        fi
        i=$((i + 1))
    done
}

# kc_create_flow <realm file> <alias> — creates the top-level flow <alias>
# of the realm file with its executions.
kc_create_flow() {
    jq -c --arg a "$2" '.authenticationFlows[] | select(.alias == $a) | {alias, description, providerId, topLevel, builtIn}' "$1" \
        | kc_call create authentication/flows -r reticora -f - >/dev/null && kc_build_flow "$1" "$2"
}

# kc_apply_flow <realm file> <plan> — makes the browser flow of the realm file
# (browser-mfa) exist as the realm file defines it: created when missing,
# rebuilt when it differs and is not bound (left over from an interrupted run
# or an earlier release). Returns 1 when the flow differs and is bound: it
# may be a customization and is kept (warning); it must not be rebound.
kc_apply_flow() {
    local realm_file="$1" plan="$2" alias state binding collisions listing config configs=()
    alias="$(jq -r .flow.alias <<<"$plan")"
    state="$(jq -r .flow.state <<<"$plan")"
    binding="$(jq -r .flow.binding <<<"$plan")"
    [ "$state" != same ] || return 0
    if [ "$state" = different ] && [ "$binding" = "$alias" ]; then
        warn "Keycloak realm: the bound browser flow $alias differs from the bundled realm (customized or from an earlier release) and is kept; compare it with deploy/keycloak/realm-reticora.json ($KC_REALM_TROUBLESHOOTING)."
        return 1
    fi
    collisions="$(jq -r '.flow.collisions | join(", ")' <<<"$plan")"
    [ -z "$collisions" ] || die "Cannot create the Keycloak flow $alias: the realm already has flows named $collisions ($KC_REALM_TROUBLESHOOTING)."
    if [ "$state" = different ]; then
        # Its authenticator configs go too; they are referenced by id.
        if ! listing="$(kc_call get "$(kc_flow_path "$alias")/executions" -r reticora </dev/null)" \
            || ! mapfile -t configs < <(jq -r '.[] | .authenticationConfig // empty' <<<"$listing") \
            || ! kc_call delete "authentication/flows/$(jq -r .flow.id <<<"$plan")" -r reticora </dev/null; then
            die "Could not remove the unbound Keycloak flow $alias that differs from the bundled realm ($KC_REALM_TROUBLESHOOTING)."
        fi
        for config in "${configs[@]}"; do
            kc_call delete "authentication/config/$config" -r reticora </dev/null \
                || die "Could not remove an authenticator config of the old flow $alias ($KC_REALM_TROUBLESHOOTING)."
        done
        success "Keycloak realm: unbound flow $alias that differed from the bundled realm removed"
    fi
    kc_create_flow "$realm_file" "$alias" \
        || die "Could not create the Keycloak flow $alias ($KC_REALM_TROUBLESHOOTING)."
    success "Keycloak realm: flow $alias created (OTP for mfa_required, acr levels 1 and 2)"
}

# apply_realm_settings — applies the realm settings of the rendered realm file
# to the running realm (the admin CLI is logged in): the declared user profile
# attributes (organization_id), the realm roles and their composites, the
# groups and their realm roles, the default client scopes of the clients
# (acr), the browser flow browser-mfa and its binding, the password policy,
# the brute-force detection and the realm attributes (acr.loa.map). A browser
# flow other than Keycloak's built-in "browser" is the operator's choice and
# stays bound (warning).
apply_realm_settings() {
    local realm_file="$COMPOSE_DIR/.generated/realm-reticora.json" current plan profile declared line changed=0
    local alias binding realm_set attributes client scope client_uuid scope_id roles=() flow_kept=0
    [ -f "$realm_file" ] || die "The rendered realm file $realm_file is missing."
    info "Applying the realm settings (policies, MFA flow, roles, groups, acr scope) …"
    # A partial export lists the realm in the format of the realm file (client
    # secrets masked); it is only read.
    if ! current="$(kc_call create partial-export -r reticora -q exportGroupsAndRoles=true -q exportClients=true -o </dev/null)" \
        || ! plan="$(printf '%s' "$current" | kc_realm_plan "$realm_file")"; then
        die "Could not read the Keycloak realm 'reticora' ($KC_REALM_TROUBLESHOOTING)."
    fi

    # Declared attributes first: Keycloak 24 drops undeclared ones, such as
    # the organization_id of the initial administrator.
    if ! profile="$(kc_call get users/profile -r reticora </dev/null)" \
        || ! declared="$(jq -c --slurpfile realm "$realm_file" '[.attributes[].name] as $have
            | [$realm[0].components["org.keycloak.userprofile.UserProfileProvider"][0].config["kc.user.profile.config"][0]
               | fromjson | .attributes[] | select(.name as $n | $have | index([$n]) == null)]' <<<"$profile")"; then
        die "Could not read the user profile of the Keycloak realm ($KC_REALM_TROUBLESHOOTING)."
    fi
    if [ "$declared" != "[]" ]; then
        jq -c --argjson add "$declared" '.attributes += $add' <<<"$profile" \
            | kc_call update users/profile -r reticora -f - >/dev/null \
            || die "Could not declare the user profile attributes of the realm ($KC_REALM_TROUBLESHOOTING)."
        success "Keycloak realm: user profile attribute $(jq -r 'map(.name) | join(", ")' <<<"$declared") declared (admin only)"
        changed=1
    fi

    mapfile -t roles < <(jq -c '.roles[]' <<<"$plan")
    for line in "${roles[@]}"; do
        printf '%s' "$line" | kc_call create roles -r reticora -f - >/dev/null \
            || die "Could not create the realm role $(jq -r .name <<<"$line") ($KC_REALM_TROUBLESHOOTING)."
        success "Keycloak realm: role $(jq -r .name <<<"$line") created"
        changed=1
    done
    for line in $(jq -r '.composites[] | "\(.role)=\(.composite)"' <<<"$plan"); do
        kc_call add-roles -r reticora --rname "${line%%=*}" --rolename "${line#*=}" </dev/null \
            || die "Could not add the role ${line#*=} to the realm role ${line%%=*} ($KC_REALM_TROUBLESHOOTING)."
        success "Keycloak realm: role ${line%%=*} contains ${line#*=}"
        changed=1
    done
    for line in $(jq -r '.groups[]' <<<"$plan"); do
        kc_call create groups -r reticora -s name="$line" </dev/null >/dev/null \
            || die "Could not create the group $line ($KC_REALM_TROUBLESHOOTING)."
        success "Keycloak realm: group $line created"
        changed=1
    done
    for line in $(jq -r '.group_roles[] | "\(.group)=\(.role)"' <<<"$plan"); do
        kc_call add-roles -r reticora --gpath "/${line%%=*}" --rolename "${line#*=}" </dev/null \
            || die "Could not map the realm role ${line#*=} to the group ${line%%=*} ($KC_REALM_TROUBLESHOOTING)."
        success "Keycloak realm: group ${line%%=*} carries the realm role ${line#*=}"
        changed=1
    done
    for line in $(jq -r '.scopes[] | "\(.client)=\(.scope)=\(.client_uuid)=\(.scope_id // "")"' <<<"$plan"); do
        IFS='=' read -r client scope client_uuid scope_id <<<"$line"
        if [ -z "$scope_id" ]; then
            [ "$scope" = acr ] || die "The Keycloak realm has no client scope $scope ($KC_REALM_TROUBLESHOOTING)."
            scope_id="$(printf '%s' "$KC_ACR_SCOPE" | kc_call create client-scopes -r reticora -f - -i)" || scope_id=""
            [ -n "$scope_id" ] || die "Could not create the client scope acr ($KC_REALM_TROUBLESHOOTING)."
            success "Keycloak realm: client scope acr created"
        fi
        kc_call update "clients/$client_uuid/default-client-scopes/$scope_id" -r reticora </dev/null \
            || die "Could not add the default client scope $scope to the client $client ($KC_REALM_TROUBLESHOOTING)."
        success "Keycloak realm: client $client has the default client scope $scope"
        changed=1
    done

    # The flow first, then its binding with the realm fields: a flow is bound
    # only as the realm file defines it.
    alias="$(jq -r .flow.alias <<<"$plan")"
    binding="$(jq -r .flow.binding <<<"$plan")"
    realm_set="$(jq -c .realm <<<"$plan")"
    attributes="$(jq -c .attributes <<<"$plan")"
    if kc_apply_flow "$realm_file" "$plan"; then
        [ "$(jq -r .flow.state <<<"$plan")" = same ] || changed=1
        if [ -z "$binding" ] || [ "$binding" = browser ]; then
            realm_set="$(jq -c --arg a "$alias" '. + {browserFlow: $a}' <<<"$realm_set")"
            plan="$(jq -c --arg l "browser flow ${binding:-unset} replaced by $alias" '.log += [$l]' <<<"$plan")"
        elif [ "$binding" != "$alias" ]; then
            warn "Keycloak realm: the browser flow $binding is bound instead of $alias and kept; make sure it requires the second factor for mfa_required ($KC_REALM_TROUBLESHOOTING)."
            flow_kept=1
        fi
    else
        flow_kept=1
    fi
    if [ "$realm_set" != "{}" ] || [ "$attributes" != "{}" ]; then
        # The whole representation goes back: Keycloak drops the realm
        # attributes (and security headers) a partial one does not list.
        kc_call get realms/reticora </dev/null \
            | jq -c --argjson set "$realm_set" --argjson attrs "$attributes" '. * $set | .attributes = ((.attributes // {}) + $attrs)' \
            | kc_call update realms/reticora -f - >/dev/null \
            || die "Could not update the password policy, lockout and browser flow of the realm ($KC_REALM_TROUBLESHOOTING)."
        changed=1
    fi
    while IFS= read -r line; do
        success "Keycloak realm: $line"
    done < <(jq -r '.log[]' <<<"$plan")
    while IFS= read -r line; do
        info "Keycloak realm: kept $line"
    done < <(jq -r '.kept[]' <<<"$plan")
    [ "$changed" -eq 1 ] || [ "$flow_kept" -eq 1 ] \
        || success "Keycloak realm settings match the bundled realm (policies, MFA flow, roles, groups, acr scope)"
}

# create_initial_admin — applies a rotation (apply_keycloak_rotation) and the
# realm settings (apply_realm_settings), removes the demo administrator of
# earlier releases and creates the individual administrator once: org_admin
# (MFA required), member of reticora-admin, organization attribute of the
# default organization, random temporary password and mandatory password
# change and OTP setup at first login. Re-runs leave an existing
# administrator alone.
create_initial_admin() {
    INITIAL_ADMIN_PASSWORD=""
    apply_keycloak_rotation
    apply_realm_settings
    info "Ensuring the initial administrator exists …"

    local legacy_id
    legacy_id="$(kc_user_id "$LEGACY_DEMO_ADMIN")"
    if [ -n "$legacy_id" ]; then
        kcadm delete "users/$legacy_id" -r reticora \
            || die "Could not remove the demo administrator $LEGACY_DEMO_ADMIN (known password)."
        warn "Removed the demo administrator $LEGACY_DEMO_ADMIN of an earlier release (known password)."
    fi

    local email="$RETICORA_INITIAL_ADMIN_EMAIL" user_id group_id password
    if [ -n "$(kc_user_id "$email")" ]; then
        success "Initial administrator $email already exists"
        return 0
    fi
    password="$(generate_initial_password)" || die "Could not generate a password for the initial administrator."
    kcadm create users -r reticora \
        -s username="$email" -s email="$email" -s emailVerified=true -s enabled=true \
        -s "attributes.organization_id=[\"$DEFAULT_ORGANIZATION_ID\"]" \
        -s 'requiredActions=["UPDATE_PASSWORD","CONFIGURE_TOTP"]' >/dev/null \
        || die "Could not create the initial administrator $email."
    user_id="$(kc_user_id "$email")"
    [ -n "$user_id" ] || die "The initial administrator $email was not found after creation."
    kcadm set-password -r reticora --username "$email" --new-password "$password" --temporary \
        || die "Could not set the temporary password of $email."
    kcadm add-roles -r reticora --uusername "$email" --rolename org_admin \
        || die "Could not grant org_admin to $email."
    group_id="$(kcadm get groups -r reticora -q search=reticora-admin --fields id --format csv --noquotes 2>/dev/null | head -n 1)"
    [ -n "$group_id" ] || die "The reticora-admin group is missing in the realm."
    kcadm update "users/$user_id/groups/$group_id" -r reticora -s realm=reticora -s userId="$user_id" -s groupId="$group_id" -n \
        || die "Could not add $email to reticora-admin."
    INITIAL_ADMIN_PASSWORD="$password"
    success "Initial administrator $email created"
}

# ─── Keycloak admin client of the server (TLC-01, TLC-03, AUT-09) ────────────
# The server creates the accounts of self-signups, accepted invitations and
# operator-created organizations and binds org_admins to MFA through a
# confidential client with a service account (client credentials grant)
# that holds realm-management manage-users, view-users and query-users.
# Without it self-signup answers 503 and every org_admin grant fails. The
# realm file does not carry the client: the production realm ships without
# users, and `start --import-realm` skips an existing realm anyway.
# ensure_admin_client creates it through the admin CLI (logged in by
# create_initial_admin) on every run, before the server starts: missing
# client, drifted settings, a secret that differs from .env and missing
# roles are applied, an unchanged client is left alone. The secret travels
# only on kcadm's stdin (-f -) and in the environment of jq, never on a
# command line or in the log.
KC_ADMIN_CLIENT_ROLES=(manage-users view-users query-users)
# The settings of the client besides its secret: confidential, service
# account only (no browser login, no password grant).
KC_ADMIN_CLIENT_SPEC='{enabled: true, protocol: "openid-connect", publicClient: false, clientAuthenticatorType: "client-secret",
    serviceAccountsEnabled: true, standardFlowEnabled: false, implicitFlowEnabled: false, directAccessGrantsEnabled: false}'

ensure_admin_client() {
    local id="$RETICORA_KEYCLOAK_ADMIN_CLIENT_ID" uuid current drift="" body field sa_id assigned role changed=0 missing=() args=()
    info "Ensuring the Keycloak admin client $id of the server (service account) …"
    uuid="$(kc_call get clients -r reticora -q clientId="$id" --fields id --format csv --noquotes </dev/null | head -n 1)" \
        || die "Could not look up the Keycloak client $id ($KC_ROTATION_TROUBLESHOOTING)."
    if [ -z "$uuid" ]; then
        uuid="$(KC_CLIENT_SECRET="$RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET" \
            jq -n -c --arg id "$id" "{clientId: \$id, name: \"Reticora server (admin API)\"} + $KC_ADMIN_CLIENT_SPEC + {secret: \$ENV.KC_CLIENT_SECRET}" \
            | kc_call create clients -r reticora -f - -i)" && [ -n "$uuid" ] \
            || die "Could not create the Keycloak client $id ($KC_ROTATION_TROUBLESHOOTING)."
        success "Keycloak: confidential client $id created (service account of the server; secret RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET)"
        changed=1
    else
        current="$(kc_call get "clients/$uuid" -r reticora </dev/null)" \
            || die "Could not read the Keycloak client $id ($KC_ROTATION_TROUBLESHOOTING)."
        drift="$(jq -r "$KC_ADMIN_CLIENT_SPEC as \$want | . as \$have | [\$want | keys[] | select(\$want[.] != \$have[.])] | join(\" \")" <<< "$current")" \
            || die "Could not read the Keycloak client $id ($KC_ROTATION_TROUBLESHOOTING)."
        if ! kc_call get "clients/$uuid/client-secret" -r reticora </dev/null \
            | KC_CLIENT_SECRET="$RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET" jq -e '.value == $ENV.KC_CLIENT_SECRET' >/dev/null; then
            drift="$drift${drift:+ }secret"
        fi
        if [ -n "$drift" ]; then
            # Only the differing fields go back, the secret over stdin.
            body="$(KC_CLIENT_SECRET="$RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET" jq -n -c --arg fields "$drift" \
                "($KC_ADMIN_CLIENT_SPEC + {secret: \$ENV.KC_CLIENT_SECRET}) as \$want | (\$fields | split(\" \")) as \$f
                 | \$want | with_entries(select(.key as \$k | \$f | index([\$k]) != null))")"
            printf '%s' "$body" | kc_call update "clients/$uuid" -r reticora -f - >/dev/null \
                || die "Could not update the Keycloak client $id ($KC_ROTATION_TROUBLESHOOTING)."
            for field in $drift; do
                if [ "$field" = secret ]; then
                    success "Keycloak: client $id secret set to RETICORA_KEYCLOAK_ADMIN_CLIENT_SECRET"
                else
                    success "Keycloak: client $id $field set to $(jq -r -n "$KC_ADMIN_CLIENT_SPEC | .$field")"
                fi
            done
            changed=1
        fi
    fi

    # The service account's roles of the client realm-management; only the
    # missing ones are added.
    sa_id="$(kc_call get "clients/$uuid/service-account-user" -r reticora </dev/null | jq -r '.id // empty')" && [ -n "$sa_id" ] \
        || die "The Keycloak client $id has no service account user ($KC_ROTATION_TROUBLESHOOTING)."
    assigned="$(kc_call get-roles -r reticora --uid "$sa_id" --cclientid realm-management </dev/null | jq -r '.[].name')" \
        || die "Could not read the roles of the service account of $id ($KC_ROTATION_TROUBLESHOOTING)."
    for role in "${KC_ADMIN_CLIENT_ROLES[@]}"; do
        grep -qx -- "$role" <<< "$assigned" && continue
        missing+=("$role")
        args+=(--rolename "$role")
    done
    if [ "${#missing[@]}" -gt 0 ]; then
        kc_call add-roles -r reticora --uid "$sa_id" --cclientid realm-management "${args[@]}" </dev/null \
            || die "Could not grant realm-management ${missing[*]} to the service account of $id ($KC_ROTATION_TROUBLESHOOTING)."
        success "Keycloak: service account of $id holds realm-management ${missing[*]}"
        changed=1
    fi
    [ "$changed" -eq 1 ] \
        || success "Keycloak admin client $id matches the configuration (service account: realm-management ${KC_ADMIN_CLIENT_ROLES[*]})"
}

# apply_realm_smtp — gives the central SMTP server to the realm 'reticora'
# (smtpServer), so Keycloak's own e-mails work: the invitation of an
# operator-created organization's first administrator (execute-actions-email,
# which would otherwise fail and roll the creation back) and password
# resets. Applied on every run with RETICORA_SMTP_HOST; the representation
# goes back whole over stdin (Keycloak drops the realm attributes a partial
# one does not list), the password only in the environment of jq. Without
# RETICORA_SMTP_HOST the realm's mail settings are left alone.
apply_realm_smtp() {
    if [ -z "${RETICORA_SMTP_HOST:-}" ]; then
        info "No SMTP server configured (RETICORA_SMTP_HOST): Keycloak's realm e-mail settings are left unchanged"
        return 0
    fi
    local current want
    # Keycloak's smtpServer map holds strings; the sender is split into the
    # address (from) and the display name. Further settings of the operator
    # (replyTo, envelopeFrom) are kept.
    want="$(jq -n -c --arg host "$RETICORA_SMTP_HOST" --arg port "$RETICORA_SMTP_PORT" --arg tls "$RETICORA_SMTP_TLS" \
        --arg user "${RETICORA_SMTP_USERNAME:-}" --arg from "$RETICORA_SMTP_FROM" '
        (($from | capture("^(?<name>[^<]*?)\\s*<(?<addr>[^<>]+)>$")) // {name: "", addr: $from}) as $f
        | {host: $host, port: $port, from: $f.addr, fromDisplayName: $f.name, starttls: ($tls == "starttls" | tostring),
           ssl: ($tls == "tls" | tostring), auth: ($user != "" | tostring), user: $user}')" \
        || die "Could not build the SMTP settings of the realm ($KC_REALM_TROUBLESHOOTING)."
    current="$(kc_call get realms/reticora </dev/null)" \
        || die "Could not read the Keycloak realm 'reticora' ($KC_REALM_TROUBLESHOOTING)."
    # Keycloak masks a stored password, so only settings without one compare.
    if [ -z "${RETICORA_SMTP_PASSWORD:-}" ] &&
        jq -e --argjson want "$want" '(.smtpServer // {}) as $s | $want | to_entries | all(.value == $s[.key])' <<< "$current" >/dev/null; then
        success "Keycloak realm: SMTP server $RETICORA_SMTP_HOST:$RETICORA_SMTP_PORT matches the configuration"
        return 0
    fi
    printf '%s' "$current" \
        | KC_SMTP_PASSWORD="${RETICORA_SMTP_PASSWORD:-}" jq -c --argjson want "$want" \
            '.smtpServer = ((.smtpServer // {}) + $want + (if $ENV.KC_SMTP_PASSWORD != "" then {password: $ENV.KC_SMTP_PASSWORD} else {} end))' \
        | kc_call update realms/reticora -f - >/dev/null \
        || die "Could not set the SMTP server of the Keycloak realm ($KC_REALM_TROUBLESHOOTING)."
    success "Keycloak realm: SMTP server $RETICORA_SMTP_HOST:$RETICORA_SMTP_PORT (TLS mode $RETICORA_SMTP_TLS) set for Keycloak's e-mails"
}

# ─── Health verification ──────────────────────────────────────────────────────
verify_stack() {
    info "Verifying the installation …"
    local url="http://localhost:${RETICORA_SERVER_PORT}/healthz" attempts=30
    while [ "$attempts" -gt 0 ]; do
        if curl -fsS "$url" >/dev/null 2>&1; then
            success "Backend health check passed ($url)"
            check_signup
            return 0
        fi
        sleep 2
        attempts=$((attempts - 1))
    done
    die "Backend health check failed at $url — inspect the logs: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs server"
}

# State of the self-signup after check_signup: available, disabled (no SMTP
# server) or unavailable (SMTP configured, the server refuses signups);
# SIGNUP_HTTP_STATUS is the answer of the probe. Read by print_summary.
SIGNUP_STATUS=""
SIGNUP_HTTP_STATUS=""

# check_signup — asks the started server whether it takes self-signups: GET
# /api/v1/signup/challenge answers 200 only with the database, the Keycloak
# admin client, the SMTP server and the verification page, otherwise 503
# ("self-signup is not available"). Never fatal.
check_signup() {
    local url="http://127.0.0.1:${RETICORA_SERVER_PORT}/api/v1/signup/challenge" attempts=1 code=""
    [ -z "${RETICORA_SMTP_HOST:-}" ] || attempts=3
    while [ "$attempts" -gt 0 ]; do
        code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$url" 2>/dev/null)" || true
        [ "$code" != 200 ] || break
        attempts=$((attempts - 1))
        [ "$attempts" -eq 0 ] || sleep "${RETICORA_SIGNUP_RETRY_DELAY:-2}"
    done
    SIGNUP_HTTP_STATUS="${code:-000}"
    if [ "$SIGNUP_HTTP_STATUS" = 200 ]; then
        SIGNUP_STATUS=available
        success "Self-signup is available: ${RETICORA_PUBLIC_BASE_URL}/onboarding"
    elif [ -z "${RETICORA_SMTP_HOST:-}" ]; then
        SIGNUP_STATUS=disabled
        info "Self-signup is disabled: no SMTP server configured (its e-mail verification is mandatory)"
    else
        SIGNUP_STATUS=unavailable
        warn "Self-signup is not available although an SMTP server is configured: GET /api/v1/signup/challenge answered $SIGNUP_HTTP_STATUS. The server names the cause at its start: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs server | grep -E 'self-signup|Keycloak admin client|central SMTP' ($SMTP_TROUBLESHOOTING)."
    fi
}

# ─── Container image garbage collection ───────────────────────────────────────
gc_images() {
    local keep="${RETICORA_GC_KEEP_IMAGES:-3}"
    local pattern img ids

    if is_tty; then
        confirm "Run container image garbage collection now (keep newest $keep per image)?" "no" || return 0
    fi

    for pattern in "reticora-server" "reticora-frontend"; do
        # Newest-first list of image IDs for this repository.
        ids="$($DOCKER images --format '{{.CreatedAt}} {{.ID}}' "$pattern" 2>/dev/null \
            | sort -r | awk '!seen[$2]++ {print $2}' || true)"
        [ -n "$ids" ] || continue
        echo "$ids" | tail -n +"$((keep + 1))" | while read -r img; do
            [ -n "$img" ] || continue
            $DOCKER rmi "$img" >/dev/null 2>&1 || true
        done
    done

    $DOCKER image prune -f >/dev/null 2>&1 || true
    success "Image garbage collection finished (kept newest $keep per image)"
}

# ─── Summary ──────────────────────────────────────────────────────────────────
# initial_login_line — the temporary password is shown exactly once, by the
# run that created the administrator.
initial_login_line() {
    if [ -n "$INITIAL_ADMIN_PASSWORD" ]; then
        printf '  Initial login:     %s / %s\n' "$RETICORA_INITIAL_ADMIN_EMAIL" "$INITIAL_ADMIN_PASSWORD"
        printf '                     temporary — shown only now; password change and OTP setup are required at first login'
    else
        printf '  Initial login:     %s (created by an earlier run; password not shown again)' "$RETICORA_INITIAL_ADMIN_EMAIL"
    fi
}

# signup_summary_line — the state of the self-signup (check_signup).
signup_summary_line() {
    case "$SIGNUP_STATUS" in
        available)
            printf '  Self-signup:       %s/onboarding  (new organizations; open it logged out or in a private window)' "$RETICORA_PUBLIC_BASE_URL" ;;
        unavailable)
            printf '  ⚠ Self-signup:     not available — GET /api/v1/signup/challenge answered %s although SMTP is configured; see the server log line '"'"'self-signup configured'"'"' (%s)' \
                "$SIGNUP_HTTP_STATUS" "$SMTP_TROUBLESHOOTING" ;;
        *)
            printf '  ⚠ Self-signup disabled: no SMTP configured — re-run the installer and enter an SMTP server (the e-mail verification of the signup is mandatory)' ;;
    esac
}

print_summary() {
    echo
    printf '%s%sInstallation complete%s\n' "$C_BOLD" "$C_GREEN" "$C_RESET"
    local ui_url="http://localhost:$RETICORA_FRONTEND_PORT"
    local tls_note="" ports_note="  Direct ports:      REST API and Keycloak on 127.0.0.1 only"
    if [ "${RETICORA_PUBLISH_DIRECT_PORTS:-false}" = true ]; then
        ports_note="  ⚠ Direct ports:    REST API and Keycloak (admin console included) on all interfaces without TLS — allow them only from trusted networks in the host firewall (docs/betrieb/firewall-matrix.md)"
    fi
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        if tls_cert_is_selfsigned; then
            ui_url="https://$RETICORA_TLS_DOMAIN"
            tls_note="  ⚠ TLS:             self-signed bootstrap certificate (issuance failed — see warnings above)"
        else
            ui_url="https://$RETICORA_TLS_DOMAIN"
            tls_note="  TLS certificate:   Let's Encrypt (auto-renewed by the certbot service)"
            if [ -n "${RETICORA_ACME_SERVER:-}" ]; then
                tls_note="  TLS certificate:   $RETICORA_ACME_SERVER (auto-renewed by the certbot service)"
            fi
        fi
    fi
    cat <<EOF

  Web UI:            $ui_url  (host port $RETICORA_FRONTEND_PORT)
  REST API:          http://localhost:$RETICORA_SERVER_PORT  (health: /healthz)
  Keycloak console:  http://localhost:$RETICORA_KEYCLOAK_PORT  (user: $KEYCLOAK_ADMIN)
$ports_note
$(initial_login_line)
${tls_note:+$tls_note
}$(signup_summary_line)
  Collector endpoint: $(collector_endpoint)  (mTLS; certificate names: ${RETICORA_NATS_EDGE_HOSTS:-})
  ⚠ Port 4222/TCP:   must be reachable inbound from the customer networks — allow it in the
                     host firewall and in the cloud firewall / security group
                     (docs/betrieb/firewall-matrix.md); without it a collector delivers nothing

  Configuration:     $ENV_FILE
  Session key:       $COMPOSE_DIR/secrets/session-private.pem

  Useful commands:
    ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE ps
    ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs -f server
    ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE down

  Next steps — connect a collector in the customer network:
    1. Install it there: ./install-vm.sh (same release).
    2. Web UI → Discovery → "Connect collector" ("Collector verbinden"): generate the single-use enrollment
       code and copy the enrollment command (open the UI under $RETICORA_PUBLIC_BASE_URL:
       the command names its origin, whose host must be one of the certificate names).
    3. On the collector host: sudo reticora-collector enroll --server <URL> --code <code>,
       then sudo systemctl restart reticora-collector (an unenrolled collector never
       uses the NATS endpoint; see docs/troubleshooting-installation.md,
       'Collector delivers nothing').
    4. Discovery page: confirm the suggested scopes (subnets the collector reports) or
       create a scope — without an enabled scope the collector discovers nothing.
EOF
}

# ─── Main ─────────────────────────────────────────────────────────────────────
main() {
    preflight
    collect_config
    select_release
    local image_source="<local build from this checkout (--build-from-source)>"
    if [ "$BUILD_FROM_SOURCE" != 1 ]; then
        image_source="$RETICORA_IMAGE_REGISTRY (signed release $RETICORA_IMAGE_TAG)"
    fi
    local smtp_setting="<disabled — no SMTP server: self-signup and e-mail stay off>"
    if [ -n "${RETICORA_SMTP_HOST:-}" ]; then
        smtp_setting="$RETICORA_SMTP_HOST:${RETICORA_SMTP_PORT:-587} (TLS ${RETICORA_SMTP_TLS:-starttls}, from ${RETICORA_SMTP_FROM:-})"
    fi
    echo
    info "Configuration:"
    cat <<EOF
  Public base URL:   $RETICORA_PUBLIC_BASE_URL
  OIDC issuer:       $RETICORA_OIDC_ISSUER_URL
  OIDC redirect:     $RETICORA_OIDC_REDIRECT_URL
  HTTPS (TLS):       ${RETICORA_TLS_DOMAIN:-<disabled — plain HTTP>}
  Ports:             UI=$RETICORA_FRONTEND_PORT API=$RETICORA_SERVER_PORT Keycloak=$RETICORA_KEYCLOAK_PORT
  API and Keycloak:  bound to ${RETICORA_DIRECT_PORTS_BIND:-127.0.0.1} (RETICORA_PUBLISH_DIRECT_PORTS=${RETICORA_PUBLISH_DIRECT_PORTS:-false})
  Collector endpoint: $(collector_endpoint) (certificate names: ${RETICORA_NATS_EDGE_HOSTS:-<compose default>}; 4222/TCP must be reachable from the customer networks)
  Outgoing e-mail:   $smtp_setting
  Image source:      $image_source
EOF
    if [ "$BUILD_FROM_SOURCE" != 1 ]; then
        info "Release mode: the images are verified with cosign (release workflow identity) and pinned by digest before the start; this checkout must be the signed source of release $RETICORA_IMAGE_TAG."
    else
        warn "Local source build: the images are built from this checkout and are not signature-verified (--build-from-source). Without the switch the signed release images are installed ($SIGNATURE_TROUBLESHOOTING)."
    fi
    if is_tty; then
        confirm "Start the installation with these settings?" "yes" || die "Aborted by user."
    fi

    verify_release_checkout
    write_env
    success "Environment written to $ENV_FILE"
    ensure_session_key
    configure_realm
    bootstrap_tls_cert
    provide_images
    configure_log_retention
    start_stack
    issue_tls_certificate
    verify_stack
    gc_images
    print_summary
}

main "$@"
