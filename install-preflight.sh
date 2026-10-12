#!/usr/bin/env bash
#
# install-preflight.sh — prerequisite checks shared by install-cloud.sh and
# install-vm.sh (INS-07, TEC-09, CH22). Sourcing it defines functions only.
#
#   check_platform           Ubuntu 22.04/24.04 LTS or Debian 12, else abort
#   check_docker_engine      Docker Engine >= 24
#   check_compose_v2         Docker Compose v2 (plugin or v2 binary)
#   check_sizing <profile>   warns below the minimum sizing (docs/ops/sizing.md)
#
# The functions use info/warn/die of the sourcing installer.

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
