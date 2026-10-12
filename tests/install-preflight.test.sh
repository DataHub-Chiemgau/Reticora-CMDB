#!/usr/bin/env bash
#
# install-preflight.test.sh — unit tests of the installer preflight (WP-093:
# INS-07, TEC-09, CH22): supported platforms, Docker Engine >= 24, Compose v2,
# sizing warnings and the apt-only package installation. Runs without Docker
# or any other dependency; docker/compose are stubbed.
#
# Usage: ./tests/install-preflight.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

# A shell with the preflight functions and the installers' output helpers.
preflight() {
    bash -c '
        info() { printf "INFO %s\n" "$*"; }
        warn() { printf "WARN %s\n" "$*" >&2; }
        die()  { printf "DIE %s\n" "$*" >&2; exit 1; }
        . ./install-preflight.sh
        '"$1"
}

expect() {
    local name="$1" want_status="$2" pattern="$3" script="$4" out status
    out="$(preflight "$script" 2>&1)"
    status=$?
    if [ "$status" -eq "$want_status" ] && printf '%s' "$out" | grep -q -- "$pattern"; then
        ok "$name"
    else
        fail "$name (status $status, output: $out)"
    fi
}

os_release() {
    local file="$TMP_ROOT/os-release-$1-$2"
    printf 'ID=%s\nVERSION_ID="%s"\n' "$1" "$2" > "$file"
    printf '%s' "$file"
}

# ─── Platforms (CH22) ─────────────────────────────────────────────────────────
for platform in ubuntu:22.04 ubuntu:24.04 debian:12; do
    file="$(os_release "${platform%%:*}" "${platform#*:}")"
    expect "platform $platform is supported" 0 "is supported" "RETICORA_OS_RELEASE=$file check_platform"
done
for platform in ubuntu:20.04 debian:11 centos:9 rhel:9; do
    file="$(os_release "${platform%%:*}" "${platform#*:}")"
    expect "platform $platform is refused" 1 "Reticora supports Ubuntu 22.04 LTS, Ubuntu 24.04 LTS and Debian 12" \
        "RETICORA_OS_RELEASE=$file check_platform"
done
file="$(os_release centos 9)"
expect "override continues with a warning" 0 "not supported" "RETICORA_OS_RELEASE=$file RETICORA_ALLOW_UNSUPPORTED_PLATFORM=1 check_platform"
expect "missing os-release is refused" 1 "unknown is not supported" "RETICORA_OS_RELEASE=$TMP_ROOT/none check_platform"

# install.sh refuses an unsupported platform before downloading anything.
out="$(RETICORA_OS_RELEASE="$(os_release centos 9)" RETICORA_SOURCE_DIR="$TMP_ROOT/src" ./install.sh --cloud --non-interactive 2>&1)"
status=$?
if [ "$status" -ne 0 ] && printf '%s' "$out" | grep -q "centos:9 is not supported" && [ ! -e "$TMP_ROOT/src" ]; then
    ok "install.sh refuses an unsupported platform"
else
    fail "install.sh refuses an unsupported platform (status $status, output: $out)"
fi
if [ "$(grep -h '^SUPPORTED_PLATFORMS=' install.sh install-preflight.sh | sort -u | wc -l)" -eq 1 ]; then
    ok "install.sh and install-preflight.sh list the same platforms"
else
    fail "install.sh and install-preflight.sh list different platforms"
fi

# ─── Docker Engine >= 24 and Compose v2 ──────────────────────────────────────
STUBS="$TMP_ROOT/bin"
mkdir -p "$STUBS"
cat > "$STUBS/docker" <<'EOF'
#!/usr/bin/env bash
case "$1 $2" in
    "version --format") printf '%s\n' "${STUB_DOCKER_VERSION}" ;;
    "compose version") printf '%s\n' "${STUB_COMPOSE_VERSION}" ;;
    *) exit 1 ;;
esac
EOF
cat > "$STUBS/docker-compose" <<'EOF'
#!/usr/bin/env bash
[ "$1 $2" = "version --short" ] && printf '%s\n' "${STUB_LEGACY_COMPOSE_VERSION}"
EOF
chmod +x "$STUBS/docker" "$STUBS/docker-compose"
WITH_STUBS="PATH=$STUBS:\$PATH"

expect "docker 23 is refused" 1 "requires Docker Engine >= 24" "$WITH_STUBS STUB_DOCKER_VERSION=23.0.6 check_docker_engine docker"
expect "docker 24 is accepted" 0 "Docker Engine 24.0.7" "$WITH_STUBS STUB_DOCKER_VERSION=24.0.7 check_docker_engine docker"
expect "docker 27 is accepted" 0 "Docker Engine 27.3.1" "$WITH_STUBS STUB_DOCKER_VERSION=27.3.1 check_docker_engine docker"
expect "unknown docker version is refused" 1 "Cannot determine the Docker Engine version" "$WITH_STUBS STUB_DOCKER_VERSION= check_docker_engine docker"
expect "compose plugin v2 is accepted" 0 "Docker Compose 2.29.1" "$WITH_STUBS STUB_COMPOSE_VERSION=2.29.1 check_compose_v2 docker compose"
expect "compose v-prefixed v2 is accepted" 0 "Docker Compose v2.20.2" "$WITH_STUBS STUB_COMPOSE_VERSION=v2.20.2 check_compose_v2 docker compose"
expect "docker-compose v1 is refused" 1 "requires Compose v2" "$WITH_STUBS STUB_LEGACY_COMPOSE_VERSION=1.29.2 check_compose_v2 docker-compose"
expect "docker-compose v2 binary is accepted" 0 "Docker Compose 2.24.0" "$WITH_STUBS STUB_LEGACY_COMPOSE_VERSION=2.24.0 check_compose_v2 docker-compose"

# ─── Sizing (INS-07, docs/ops/sizing.md) ─────────────────────────────────────
expect "cloud sizing met" 0 "meets the cloud minimum" \
    "RETICORA_HOST_CPUS=4 RETICORA_HOST_MEMORY_GIB=8 RETICORA_HOST_DISK_GIB=100 check_sizing cloud"
expect "cloud below minimum warns about vCPU" 0 "Only 2 vCPU available; the cloud minimum is 4" \
    "RETICORA_HOST_CPUS=2 RETICORA_HOST_MEMORY_GIB=16 RETICORA_HOST_DISK_GIB=200 check_sizing cloud"
expect "cloud below minimum warns about RAM" 0 "Only 4 GiB RAM available; the cloud minimum is 8 GiB" \
    "RETICORA_HOST_CPUS=8 RETICORA_HOST_MEMORY_GIB=4 RETICORA_HOST_DISK_GIB=200 check_sizing cloud"
expect "cloud below minimum warns about disk" 0 "Only 50 GiB free disk" \
    "RETICORA_HOST_CPUS=8 RETICORA_HOST_MEMORY_GIB=16 RETICORA_HOST_DISK_GIB=50 check_sizing cloud"
expect "collector sizing met" 0 "meets the collector minimum" \
    "RETICORA_HOST_CPUS=2 RETICORA_HOST_MEMORY_GIB=2 RETICORA_HOST_DISK_GIB=20 check_sizing collector"
expect "real host resources are read" 0 "GiB" "check_sizing cloud /"
if grep -q '10.000' docs/ops/sizing.md && grep -q '50.000' docs/ops/sizing.md && grep -q '4 vCPU' docs/ops/sizing.md; then
    ok "docs/ops/sizing.md documents the sizing up to 50,000 objects"
else
    fail "docs/ops/sizing.md documents the sizing up to 50,000 objects"
fi

# ─── Package installation: apt only ──────────────────────────────────────────
if grep -nE '\b(dnf|yum|zypper|pacman)\b|apk add' install.sh install-cloud.sh install-vm.sh >/dev/null; then
    fail "installers only use apt-get"
else
    ok "installers only use apt-get"
fi
for script in install-cloud.sh install-vm.sh; do
    # shellcheck disable=SC2016 # the literal line in the installers
    if grep -q '^\. "\$SCRIPT_DIR/install-preflight.sh"' "$script" && grep -q 'check_platform' "$script"; then
        ok "$script runs the preflight"
    else
        fail "$script runs the preflight"
    fi
done

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
