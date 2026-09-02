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
# Usage:
#   ./install-cloud.sh                    interactive installation
#   ./install-cloud.sh --non-interactive  use existing .env / defaults /
#                                         generated secrets without prompting
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_DIR="$SCRIPT_DIR/deploy/docker-compose"
COMPOSE_FILE="$COMPOSE_DIR/docker-compose.yml"
OVERRIDE_FILE="$COMPOSE_DIR/docker-compose.override.yml"
ENV_FILE="$COMPOSE_DIR/.env"
NON_INTERACTIVE=0

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

Usage:
  ./install-cloud.sh                    interactive installation
  ./install-cloud.sh --non-interactive  use existing .env / defaults /
                                        generated secrets without prompting
EOF
}

for arg in "$@"; do
    case "$arg" in
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
}

compose_cmd() {
    # Passing -f explicitly disables compose's automatic merging of
    # docker-compose.override.yml, so merge it by hand whenever it exists
    # (it carries the build: sections for the local source build). The
    # prebuilt-image path renames it to .disabled to opt out.
    local files=(-f "$COMPOSE_FILE")
    if [ -f "$OVERRIDE_FILE" ]; then
        files+=(-f "$OVERRIDE_FILE")
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

valid_url() {
    case "$1" in
        http://*/*|https://*/*|http://?*|https://?*) return 0 ;;
    esac
    return 1
}

# explain_url_error <value> <suggestion-nameref> — warns why a URL was
# rejected and, for a missing scheme, suggests the corrected value via the
# nameref.
explain_url_error() {
    local -n _suggest="$2"
    case "$1" in
        http://*|https://*)
            warn "Invalid input — the URL must contain a host (e.g. https://cmdb.example.com)." ;;
        *"://"*)
            warn "Invalid input — only http:// and https:// URLs are supported." ;;
        *)
            _suggest="https://$1"
            warn "Invalid input — the URL must start with http:// or https://, e.g. $_suggest" ;;
    esac
}

valid_password() { [ "${#1}" -ge 8 ]; }

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

# Optional variants: empty input keeps the feature disabled.
valid_optional_domain() { [ -z "$1" ] || valid_domain "$1"; }
valid_optional_email()  { [ -z "$1" ] || valid_email "$1"; }

# ─── .env handling ────────────────────────────────────────────────────────────
env_get() {
    # env_get <key> — prints the raw value from the existing .env, if any.
    local key="$1" line
    [ -f "$ENV_FILE" ] || return 1
    line="$(grep -E "^${key}=" "$ENV_FILE" | tail -n 1)" || return 1
    printf '%s' "${line#*=}"
}

write_env() {
    local tmp
    tmp="$(mktemp)"
    cat > "$tmp" <<EOF
# Reticora CMDB — central cloud environment
# Generated by install-cloud.sh on $(date -u '+%Y-%m-%d %H:%M:%S UTC')
# Keep this file secret — it contains passwords and private key material.

# Database
RETICORA_DB_PASSWORD=$RETICORA_DB_PASSWORD

# Object storage (MinIO / S3-compatible)
RETICORA_S3_ACCESS_KEY=$RETICORA_S3_ACCESS_KEY
RETICORA_S3_SECRET_KEY=$RETICORA_S3_SECRET_KEY

# Keycloak (identity & access management)
KEYCLOAK_ADMIN=$KEYCLOAK_ADMIN
KEYCLOAK_ADMIN_PASSWORD=$KEYCLOAK_ADMIN_PASSWORD
KEYCLOAK_DB_PASSWORD=$KEYCLOAK_DB_PASSWORD
RETICORA_OIDC_CLIENT_SECRET=$RETICORA_OIDC_CLIENT_SECRET

# Credential envelope encryption (32-byte key, base64-encoded)
RETICORA_MASTER_KEY=$RETICORA_MASTER_KEY

# Public URLs
RETICORA_PUBLIC_BASE_URL=$RETICORA_PUBLIC_BASE_URL
RETICORA_OIDC_ISSUER_URL=$RETICORA_OIDC_ISSUER_URL
RETICORA_OIDC_CLIENT_ID=$RETICORA_OIDC_CLIENT_ID
RETICORA_OIDC_REDIRECT_URL=$RETICORA_OIDC_REDIRECT_URL

# HTTPS / Let's Encrypt (empty = plain HTTP)
RETICORA_TLS_DOMAIN=$RETICORA_TLS_DOMAIN
RETICORA_CERT_EMAIL=$RETICORA_CERT_EMAIL

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

# Source of prebuilt images (leave empty to build from this checkout)
RETICORA_IMAGE_REGISTRY=$RETICORA_IMAGE_REGISTRY
RETICORA_IMAGE_TAG=$RETICORA_IMAGE_TAG

# Container image garbage collection
RETICORA_GC_KEEP_IMAGES=$RETICORA_GC_KEEP_IMAGES
EOF
    mv "$tmp" "$ENV_FILE"
    chmod 600 "$ENV_FILE"
}

# ─── Preflight checks ─────────────────────────────────────────────────────────
preflight() {
    info "Checking prerequisites …"

    # Tools the installer itself needs; missing ones are installed automatically.
    ensure_command curl curl ca-certificates
    ensure_command openssl openssl

    detect_container_tooling
    success "Container tooling: ${COMPOSE[*]}"

    if [ ! -f "$COMPOSE_FILE" ]; then
        die "Compose file not found at $COMPOSE_FILE — run this script from a full checkout, or use ./install.sh which downloads the sources for you."
    fi

    if [ ! -f "$SCRIPT_DIR/backend/Dockerfile" ] && [ -z "$(env_get RETICORA_IMAGE_REGISTRY || true)" ]; then
        warn "backend/Dockerfile not found; image build will fail unless RETICORA_IMAGE_REGISTRY points to prebuilt images."
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

    ask_validated RETICORA_DB_PASSWORD "Database password (PostgreSQL)" "$def_db_pw" valid_password
    ask RETICORA_S3_ACCESS_KEY "S3/MinIO access key" "$def_s3_key"
    ask_validated RETICORA_S3_SECRET_KEY "S3/MinIO secret key" "$def_s3_secret" valid_password

    local def_kc_admin
    def_kc_admin="$(env_get KEYCLOAK_ADMIN || true)"
    ask KEYCLOAK_ADMIN "Keycloak admin user" "${def_kc_admin:-admin}"
    ask_validated KEYCLOAK_ADMIN_PASSWORD "Keycloak admin password" "$def_kc_pw" valid_password
    ask_validated KEYCLOAK_DB_PASSWORD "Keycloak database password" "$def_kc_db_pw" valid_password

    local def_client_id
    def_client_id="$(env_get RETICORA_OIDC_CLIENT_ID || true)"
    ask RETICORA_OIDC_CLIENT_ID "OIDC client ID" "${def_client_id:-reticora-app}"
    ask RETICORA_OIDC_CLIENT_SECRET "OIDC client secret" "$def_client_secret"

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
            localhost|127.*|0.0.0.0|[0-9]*.[0-9]*.[0-9]*.[0-9]*)
                # Local/literal-IP install: Keycloak is published directly on
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
        # Derive a suggestion from the public base URL when it is not a
        # localhost/literal-IP address (Let's Encrypt requires a public DNS name).
        def_tls_domain="$(printf '%s' "$RETICORA_PUBLIC_BASE_URL" | sed -E 's|^https?://||; s|[:/].*$||')"
        case "$def_tls_domain" in
            localhost|127.*|0.0.0.0|[0-9]*.[0-9]*.[0-9]*.[0-9]*) def_tls_domain="" ;;
        esac
    fi
    if is_tty; then
        echo
        echo "HTTPS (recommended for production): enter a public domain name to"
        echo "automatically obtain a free Let's Encrypt certificate (requires ports"
        echo "80/443 reachable from the internet; the UI is then served at"
        echo "https://<domain> on port 443). Leave empty to keep plain HTTP."
        ask_validated RETICORA_TLS_DOMAIN "Public domain for HTTPS (empty = no TLS, optional)" "$def_tls_domain" valid_optional_domain
    elif [ -n "$def_tls_domain" ] && valid_domain "$def_tls_domain"; then
        # Non-interactive run: reuse the configured domain (from .env, or
        # derived above from an https:// public base URL with a DNS name).
        RETICORA_TLS_DOMAIN="$def_tls_domain"
    else
        RETICORA_TLS_DOMAIN=""
    fi

    local def_cert_email
    def_cert_email="$(env_get RETICORA_CERT_EMAIL || true)"
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        ask_validated RETICORA_CERT_EMAIL "E-mail for Let's Encrypt expiry notices (optional)" "$def_cert_email" valid_optional_email
    else
        RETICORA_CERT_EMAIL="$def_cert_email"
    fi

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

    # ── Images ──
    ask RETICORA_IMAGE_REGISTRY "Image registry for prebuilt images (empty = build locally)" "$(env_get RETICORA_IMAGE_REGISTRY || true)"
    # Normalize a whitespace-only answer to empty so the summary shows
    # "<local build>" and provide_images takes the local-build branch.
    RETICORA_IMAGE_REGISTRY="$(trim "$RETICORA_IMAGE_REGISTRY")"
    local def_tag
    def_tag="$(env_get RETICORA_IMAGE_TAG || true)"
    ask RETICORA_IMAGE_TAG "Image tag" "${def_tag:-latest}"

    RETICORA_GC_KEEP_IMAGES="$(env_get RETICORA_GC_KEEP_IMAGES || true)"
    RETICORA_GC_KEEP_IMAGES="${RETICORA_GC_KEEP_IMAGES:-3}"
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
#   2. issue_tls_certificate — run certbot with the webroot plugin (nginx must
#                             already serve /.well-known/acme-challenge/),
#                             then reload nginx to pick up the real cert.
# The long-running certbot service in the compose file renews the certificate
# twice a day (no-op until it is close to expiry).

letsencrypt_dir() { printf '%s/letsencrypt' "$COMPOSE_DIR"; }

tls_live_dir() { printf '%s/letsencrypt/live/%s' "$COMPOSE_DIR" "$RETICORA_TLS_DOMAIN"; }

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

# tls_cert_is_selfsigned — returns 0 when the current certificate is the
# bootstrap placeholder (issuer == subject) or no certificate exists yet.
tls_cert_is_selfsigned() {
    local cert
    cert="$(tls_live_dir)/fullchain.pem"
    [ -f "$cert" ] || return 0
    local subject issuer
    subject="$(openssl x509 -in "$cert" -noout -subject 2>/dev/null || true)"
    issuer="$(openssl x509 -in "$cert" -noout -issuer 2>/dev/null || true)"
    [ -z "$subject" ] || [ "$subject" = "$issuer" ]
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

# validate_tls_material <domain> — verifies that the certificate chain and
# private key under letsencrypt/live/<domain> form a consistent, usable pair:
# the certificate must parse, carry a SAN for the domain, be unexpired, and
# match the private key. Called after bootstrap generation and again after
# certbot issuance so TLS problems surface at install time.
validate_tls_material() {
    local domain="$1"
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
    local cert_pub key_pub
    cert_pub="$(openssl x509 -in "$cert" -noout -pubkey 2>/dev/null | openssl sha256)"
    key_pub="$(openssl pkey -in "$key" -pubout 2>/dev/null | openssl sha256)"
    [ -n "$cert_pub" ] && [ "$cert_pub" = "$key_pub" ] \
        || die "TLS certificate $cert and private key $key do not match."
    success "TLS material for $domain is consistent (parses, SAN present, key matches)"
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

    # The certbot container shares the webroot volume with the frontend, and
    # ./letsencrypt is bind-mounted as /etc/letsencrypt in both. nginx is
    # already running with the bootstrap certificate, serving the challenge
    # directory over plain HTTP on port 80.
    if compose_cmd run --rm --no-deps certbot certonly \
        --webroot -w /var/www/certbot \
        -d "$RETICORA_TLS_DOMAIN" \
        --non-interactive --agree-tos \
        "${email_args[@]}"; then
        validate_tls_material "$RETICORA_TLS_DOMAIN"
        compose_cmd exec -T frontend nginx -s reload >/dev/null 2>&1 \
            || warn "Could not reload nginx automatically; restart the frontend to activate the certificate: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE restart frontend"
        success "Let's Encrypt certificate issued for $RETICORA_TLS_DOMAIN"
        success "HTTPS is now active at https://$RETICORA_TLS_DOMAIN"
    else
        warn "Let's Encrypt certificate issuance failed."
        warn "Common causes: DNS does not point at this host, or ports 80/443"
        warn "are not reachable from the internet. The stack keeps running with"
        warn "the self-signed bootstrap certificate; re-run this installer after"
        warn "fixing connectivity, or check the logs:"
        warn "  ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs certbot"
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
# discovery document at the configured issuer URL, otherwise sign-in fails
# later in the browser with a hard-to-diagnose error.
validate_keycloak_bootstrap() {
    info "Validating the Keycloak bootstrap …"

    # The issuer URL must not point at localhost when the UI is served from a
    # different host — the browser resolves it, not the server.
    case "$RETICORA_OIDC_ISSUER_URL" in
        http://localhost:*|http://localhost/*|https://localhost:*|https://localhost/*|http://127.*|https://127.*)
            case "$RETICORA_PUBLIC_BASE_URL" in
                http://localhost*|https://localhost*|http://127.*|https://127.*) : ;;
                *) warn "RETICORA_OIDC_ISSUER_URL ($RETICORA_OIDC_ISSUER_URL) points at localhost but the public base URL is $RETICORA_PUBLIC_BASE_URL."
                   warn "Remote browsers cannot reach a localhost issuer — sign-in will fail for every user not on this machine." ;;
            esac
            ;;
    esac

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

# ─── Images: build / pull ─────────────────────────────────────────────────────
provide_images() {
    if [ -n "$RETICORA_IMAGE_REGISTRY" ]; then
        # Prebuilt images: disable the source-build override so compose uses
        # the registry images referenced by RETICORA_SERVER_IMAGE /
        # RETICORA_FRONTEND_IMAGE.
        if [ -f "$OVERRIDE_FILE" ]; then
            mv "$OVERRIDE_FILE" "$OVERRIDE_FILE.disabled"
        fi
        info "Using prebuilt images from registry: $RETICORA_IMAGE_REGISTRY (tag $RETICORA_IMAGE_TAG)"
        RETICORA_SERVER_IMAGE="$RETICORA_IMAGE_REGISTRY/reticora-server:$RETICORA_IMAGE_TAG" \
        RETICORA_FRONTEND_IMAGE="$RETICORA_IMAGE_REGISTRY/reticora-frontend:$RETICORA_IMAGE_TAG" \
            compose_cmd pull server frontend || die "Pulling the images failed."
    else
        # Local source build: make sure the build override is active.
        if [ -f "$OVERRIDE_FILE.disabled" ]; then
            mv "$OVERRIDE_FILE.disabled" "$OVERRIDE_FILE"
        fi
        info "Building the Reticora images (this can take a few minutes) …"
        compose_cmd build server frontend || die "Image build failed."
    fi
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        compose_cmd pull certbot || die "Pulling the certbot image failed."
    fi
    success "Server and frontend images are available"
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

# db_volume_name — prints the name of the Docker volume holding the main
# PostgreSQL data directory (<project>_pgdata), resolved from the running
# postgres container's mounts (falling back to the compose project name).
db_volume_name() {
    local cid project name
    cid="$(compose_cmd ps -q postgres 2>/dev/null || true)"
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
    printf '%s_pgdata' "${project:-docker-compose}"
}

# db_image — prints the container image configured for the postgres service,
# so the temporary password-reset instance runs exactly the same PostgreSQL
# version as the real stack (an older binary refuses a newer data directory).
db_image() {
    local img
    img="$("${COMPOSE[@]}" --env-file "$ENV_FILE" -f "$COMPOSE_FILE" config 2>/dev/null \
        | awk '/^[[:space:]]*postgres:/{f=1} f && /^[[:space:]]*image:/{print $2; exit}')"
    printf '%s' "${img:-timescale/timescaledb:latest-pg16}"
}

# sync_db_password — the postgres image applies POSTGRES_PASSWORD only when it
# initializes an *empty* data directory. When the installer is re-run with a
# changed RETICORA_DB_PASSWORD, the persistent pgdata volume still carries the
# previous password, so every connection fails with SQLSTATE 28P01
# (password authentication failed) and the server container never becomes
# healthy ("dependency failed to start: container docker-compose-server-1 is
# unhealthy"). Probe the authentication here and, on mismatch, resync the
# stored password so the database matches .env again.
sync_db_password() {
    info "Checking database authentication …"
    if compose_cmd exec -T -e PGPASSWORD="$RETICORA_DB_PASSWORD" postgres \
        psql -U reticora -d reticora -tAc 'SELECT 1' >/dev/null 2>&1; then
        success "Database password matches the configuration"
        return 0
    fi

    warn "The existing PostgreSQL volume still uses a different password (SQLSTATE 28P01);"
    warn "PostgreSQL ignores POSTGRES_PASSWORD once its data directory is initialized."
    info "Updating the password of database role 'reticora' to the configured value …"

    # Start a temporary PostgreSQL instance on the same data volume, but with
    # 'trust' authentication for local connections (via a throwaway hba config
    # outside the volume) so the role password can be changed without knowing
    # the old one. The new password is passed through the PGPASSWORD
    # environment variable and a psql variable, never on a command line.
    local hba_vol image
    hba_vol="$(db_volume_name)"
    image="$(db_image)"
    compose_cmd rm -sf postgres >/dev/null 2>&1 || true
    $DOCKER run -d --rm --name reticora-db-pwreset \
        --entrypoint sh \
        -v "$hba_vol:/var/lib/postgresql/data" \
        -e POSTGRES_PASSWORD="$RETICORA_DB_PASSWORD" \
        -e RESET_HBA='local all all trust
host all all all scram-sha-256' \
        "$image" \
        -c 'mkdir -p /etc/reticora-pwreset && printf "%s\n" "$RESET_HBA" > /etc/reticora-pwreset/pg_hba.conf && chmod 644 /etc/reticora-pwreset/pg_hba.conf && exec docker-entrypoint.sh postgres -c hba_file=/etc/reticora-pwreset/pg_hba.conf' \
        >/dev/null 2>&1 \
        || die "Cannot start the temporary PostgreSQL instance on volume '$hba_vol' — is another postgres container still running?"

    local ok=1
    for _ in $(seq 1 30); do
        # The password travels only via stdin: the \set meta-command reads it
        # from the PGPASSWORD environment variable inside psql, so it never
        # appears in a process list or on a command line.
        if printf '%s\n' '\set pw `printenv PGPASSWORD`' "ALTER ROLE reticora WITH PASSWORD :'pw'" | \
            $DOCKER exec -i -e PGPASSWORD="$RETICORA_DB_PASSWORD" reticora-db-pwreset \
                psql -U reticora -d reticora -v ON_ERROR_STOP=1 -q >/dev/null 2>&1; then
            ok=0
            break
        fi
        sleep 2
    done
    $DOCKER rm -f reticora-db-pwreset >/dev/null 2>&1 || true
    if [ "$ok" -ne 0 ]; then
        die "Could not reset the database password. Remove the stale volume with '$DOCKER volume rm $hba_vol' (all CMDB data is lost) and re-run this installer."
    fi
    success "Database password updated"

    info "Restarting PostgreSQL with normal authentication …"
    compose_cmd up -d postgres
    wait_for_service postgres || die "PostgreSQL did not become healthy."
    if compose_cmd exec -T -e PGPASSWORD="$RETICORA_DB_PASSWORD" postgres \
        psql -U reticora -d reticora -tAc 'SELECT 1' >/dev/null 2>&1; then
        success "Database authentication verified"
    else
        die "Database authentication still fails after the password reset. Remove the stale volume with '$DOCKER volume rm $hba_vol' (all CMDB data is lost) and re-run this installer."
    fi
}

start_stack() {
    info "Starting the infrastructure services …"
    compose_cmd up -d postgres nats redis minio keycloak-db
    wait_for_service postgres || die "PostgreSQL did not become healthy."
    wait_for_service keycloak-db || die "Keycloak PostgreSQL did not become healthy."
    success "Database and infrastructure services are running"

    sync_db_password

    run_migrations
    seed_default_organization

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

    info "Starting the Reticora server and frontend …"
    compose_cmd up -d server frontend
    wait_for_service server || {
        show_service_logs server
        die "Reticora server did not become healthy — inspect the logs: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs server"
    }
    wait_for_service frontend || {
        show_service_logs frontend
        die "Frontend did not become healthy — inspect the logs: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs frontend"
    }
    # Start the certbot renewal daemon only when TLS is configured; without a
    # domain it has nothing to do.
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        compose_cmd up -d certbot
    fi
    success "All services are up"
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
            compose_cmd exec -T postgres psql -U reticora -d reticora -v ON_ERROR_STOP=1 -q \
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
seed_default_organization() {
    info "Ensuring the default organization exists …"
    compose_cmd exec -T postgres psql -U reticora -d reticora -v ON_ERROR_STOP=1 -q \
        -c "INSERT INTO organization (id, name, slug) VALUES ('00000000-0000-0000-0000-000000000001', 'Reticora Demo', 'reticora-demo') ON CONFLICT (id) DO NOTHING" \
        || die "Could not seed the default organization."
    success "Default organization is present"
}

# ─── Health verification ──────────────────────────────────────────────────────
verify_stack() {
    info "Verifying the installation …"
    local url="http://localhost:${RETICORA_SERVER_PORT}/healthz" attempts=30
    while [ "$attempts" -gt 0 ]; do
        if curl -fsS "$url" >/dev/null 2>&1; then
            success "Backend health check passed ($url)"
            return 0
        fi
        sleep 2
        attempts=$((attempts - 1))
    done
    die "Backend health check failed at $url — inspect the logs: ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs server"
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
print_summary() {
    echo
    printf '%s%sInstallation complete%s\n' "$C_BOLD" "$C_GREEN" "$C_RESET"
    local ui_url="http://localhost:$RETICORA_FRONTEND_PORT"
    local tls_note=""
    if [ -n "$RETICORA_TLS_DOMAIN" ]; then
        if tls_cert_is_selfsigned; then
            ui_url="https://$RETICORA_TLS_DOMAIN"
            tls_note="  ⚠ TLS:             self-signed bootstrap certificate (issuance failed — see warnings above)"
        else
            ui_url="https://$RETICORA_TLS_DOMAIN"
            tls_note="  TLS certificate:   Let's Encrypt (auto-renewed by the certbot service)"
        fi
    fi
    cat <<EOF

  Web UI:            $ui_url  (host port $RETICORA_FRONTEND_PORT)
  REST API:          http://localhost:$RETICORA_SERVER_PORT  (health: /healthz)
  Keycloak console:  http://localhost:$RETICORA_KEYCLOAK_PORT  (user: $KEYCLOAK_ADMIN)
  Initial login:     admin@reticora.local / admin123  (change immediately!)
${tls_note:+$tls_note
}
  Configuration:     $ENV_FILE
  Session key:       $COMPOSE_DIR/secrets/session-private.pem

  Useful commands:
    ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE ps
    ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE logs -f server
    ${COMPOSE[*]} --env-file $ENV_FILE -f $COMPOSE_FILE down

  Next step: install a collector in the customer network with ./install-vm.sh
EOF
}

# ─── Main ─────────────────────────────────────────────────────────────────────
main() {
    preflight
    collect_config
    echo
    info "Configuration:"
    cat <<EOF
  Public base URL:   $RETICORA_PUBLIC_BASE_URL
  OIDC issuer:       $RETICORA_OIDC_ISSUER_URL
  OIDC redirect:     $RETICORA_OIDC_REDIRECT_URL
  HTTPS (TLS):       ${RETICORA_TLS_DOMAIN:-<disabled — plain HTTP>}
  Ports:             UI=$RETICORA_FRONTEND_PORT API=$RETICORA_SERVER_PORT Keycloak=$RETICORA_KEYCLOAK_PORT
  Image source:      ${RETICORA_IMAGE_REGISTRY:-<local build>} (tag $RETICORA_IMAGE_TAG)
EOF
    if is_tty; then
        confirm "Start the installation with these settings?" "yes" || die "Aborted by user."
    fi

    write_env
    success "Environment written to $ENV_FILE"
    ensure_session_key
    configure_realm
    bootstrap_tls_cert
    provide_images
    start_stack
    reload_frontend_after_bootstrap
    issue_tls_certificate
    verify_stack
    gc_images
    print_summary
}

main "$@"
