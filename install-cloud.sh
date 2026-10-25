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
ENV_FILE="$COMPOSE_DIR/.env"
NON_INTERACTIVE=0
# Release whose signed images are installed (empty: chosen in select_release).
RELEASE="${RETICORA_RELEASE:-}"
# Registry of the signed images (empty: .env, otherwise RELEASE_REGISTRY).
IMAGE_REGISTRY_OVERRIDE="${RETICORA_IMAGE_REGISTRY:-}"
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
# a re-run merges instead of dropping additional configuration (INS-05).
merge_env_extras() {
    local new="$1" line key extras=()
    [ -f "$ENV_FILE" ] || return 0
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in ''|'#'*) continue ;; esac
        key="${line%%=*}"
        [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
        grep -q "^${key}=" "$new" && continue
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
    local cert_pub key_pub
    cert_pub="$(openssl x509 -in "$cert" -noout -pubkey 2>/dev/null | openssl sha256)"
    key_pub="$(openssl pkey -in "$key" -pubout 2>/dev/null | openssl sha256)"
    [ -n "$cert_pub" ] && [ "$cert_pub" = "$key_pub" ] \
        || die "TLS certificate $cert and private key $key do not match."
    if [ "$chain" = "1" ]; then
        tls_cert_is_trusted "$cert" \
            || die "TLS certificate chain $cert does not verify against the system trust store (incomplete chain or untrusted issuer)."
        success "TLS material for $domain is consistent (parses, SAN present, key matches, chain verifies)"
        return 0
    fi
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
    # directory over plain HTTP on port 80. The service's entrypoint is the
    # renewal loop shell, so the one-off run selects the certbot binary.
    if compose_cmd run --rm --no-deps --entrypoint certbot certbot certonly \
        --webroot -w /var/www/certbot \
        -d "$RETICORA_TLS_DOMAIN" \
        --non-interactive --agree-tos \
        "${email_args[@]}"; then
        validate_tls_material "$RETICORA_TLS_DOMAIN" 1
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
        *) fallback="postgres:16-alpine" ;;
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
# client's redirect URIs and web origins).
KC_ROTATED_SETTINGS=(KEYCLOAK_ADMIN_PASSWORD RETICORA_OIDC_CLIENT_SECRET RETICORA_OIDC_REDIRECT_URL RETICORA_OIDC_ISSUER_URL)

# report_rotated_settings — names (never values) the settings of
# KC_ROTATED_SETTINGS whose value differs from the one in .env.previous.
report_rotated_settings() {
    local key previous names=()
    for key in "${KC_ROTATED_SETTINGS[@]}"; do
        previous="$(env_get "$key" "$ENV_FILE.previous")" || continue
        [ "$previous" = "${!key}" ] || names+=("$key")
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

# kc_client_drift <realm file> <clientId> — reads the client of the running
# realm (JSON) on stdin and prints each of secret, redirectUris and webOrigins
# whose value differs from the rendered realm; lists compare as sets, the way
# Keycloak stores them.
kc_client_drift() {
    jq -r --arg id "$2" --slurpfile realm "$1" '
        def norm: if type == "array" then unique else . end;
        . as $have
        | ($realm[0].clients[] | select(.clientId == $id)) as $want
        | ("secret", "redirectUris", "webOrigins")
        | select(($want[.] | norm) != ($have[.] | norm))'
}

# sync_realm_client — applies secret, redirectUris and webOrigins of each
# client of the rendered realm that has a secret to the running realm: one
# update per differing field; nothing else of the realm is changed.
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
            jq -c --arg id "$client_id" --arg f "$field" '.clients[] | select(.clientId == $id) | {($f): .[$f]}' "$realm_file" \
                | kcadm update "clients/$uuid" -r reticora -f - >/dev/null \
                || die "Could not apply $field of the rendered realm to the Keycloak client $client_id ($KC_ROTATION_TROUBLESHOOTING)."
            if [ "$field" = secret ]; then
                success "Keycloak rotation: client $client_id secret set to RETICORA_OIDC_CLIENT_SECRET"
            else
                success "Keycloak rotation: client $client_id $field set to $(jq -r --arg id "$client_id" --arg f "$field" \
                    '.clients[] | select(.clientId == $id) | .[$f] | join(", ")' "$realm_file")"
            fi
            changed=1
        done
    done
    [ "$changed" -eq 1 ] || success "Keycloak client secret, redirect URIs and web origins match the configuration"
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

# create_initial_admin — applies a rotation (apply_keycloak_rotation), removes
# the demo administrator of earlier releases and creates the individual
# administrator once: org_admin (MFA required), member of reticora-admin,
# organization attribute of the default organization, random temporary
# password and mandatory password change and OTP setup at first login.
# Re-runs leave an existing administrator alone.
create_initial_admin() {
    INITIAL_ADMIN_PASSWORD=""
    apply_keycloak_rotation
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
$(initial_login_line)
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
    select_release
    local image_source="<local build from this checkout (--build-from-source)>"
    if [ "$BUILD_FROM_SOURCE" != 1 ]; then
        image_source="$RETICORA_IMAGE_REGISTRY (signed release $RETICORA_IMAGE_TAG)"
    fi
    echo
    info "Configuration:"
    cat <<EOF
  Public base URL:   $RETICORA_PUBLIC_BASE_URL
  OIDC issuer:       $RETICORA_OIDC_ISSUER_URL
  OIDC redirect:     $RETICORA_OIDC_REDIRECT_URL
  HTTPS (TLS):       ${RETICORA_TLS_DOMAIN:-<disabled — plain HTTP>}
  Ports:             UI=$RETICORA_FRONTEND_PORT API=$RETICORA_SERVER_PORT Keycloak=$RETICORA_KEYCLOAK_PORT
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
    start_stack
    issue_tls_certificate
    verify_stack
    gc_images
    print_summary
}

main "$@"
