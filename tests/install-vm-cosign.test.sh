#!/usr/bin/env bash
# shellcheck disable=SC2016 # the scripts in single quotes run inside the sandbox
#
# install-vm-cosign.test.sh — COL-06 (WP-148): cosign for the signed
# auto-updates of a collector installed by install-vm.sh (retest F-260,
# WP-432). The auto-updater (collector/collectorcmd/update.go,
# edgecore/update CosignVerifier) verifies every release with
# 'cosign verify-blob' from the PATH of the service; without cosign every
# update is refused.
#
#   - with RETICORA_UPDATE_URL (environment or collector.env) the installer
#     writes it to collector.env and, before the collector, installs the
#     pinned cosign of the signature block (COSIGN_VERSION, sha256 checked
#     before it is installed) as /usr/local/bin/cosign, in the default PATH
#     of systemd services; a re-run downloads nothing, another file there is
#     replaced, the pinned cosign that verified the release is reused;
#   - without RETICORA_UPDATE_URL nothing is installed or downloaded;
#   - a download with another checksum, or none, installs nothing and stops
#     before the collector is installed;
#   - the Docker mode (updated through the signed image) installs no cosign
#     and writes no update URL.
#
# The installer's main runs non-interactively in a clean environment; curl
# (the cosign download), preflight and the collector and unit installation
# are stubbed, all paths lie below a temporary directory, nothing is
# installed on the host. The sandbox pins the checksum of a fake cosign;
# the real pin is compared with install-smoke.yml, refuses the fake, and
# with RETICORA_TEST_COSIGN=<cosign v2.4.1 binary> (CI: install-smoke.yml)
# installs the real release binary.
#
# Usage: ./tests/install-vm-cosign.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | tail -n 8 | sed 's/^/     /'; return 0; }

case "$(uname -m)" in
    aarch64|arm64) ARCH="arm64"; PIN_VAR="COSIGN_SHA256_ARM64" ;;
    *) ARCH="amd64"; PIN_VAR="COSIGN_SHA256_AMD64" ;;
esac
UPDATE_URL="https://updates.example.test/collector"

# ─── Sandbox: stubs and a clean PATH ─────────────────────────────────────────
SYS="$TMP_ROOT/sys"; STUBS="$TMP_ROOT/stubs"
mkdir -p "$SYS" "$STUBS"
for c in bash sh env cat cp mv rm mkdir chmod chown ln mktemp sed awk grep tr cut head tail wc sort \
    sha256sum uname id dirname basename date ls hostname touch readlink true false stat; do
    p="$(type -P "$c" 2>/dev/null)" && ln -sf "$p" "$SYS/$c"
done
cat > "$STUBS/curl" <<'STUB'
#!/usr/bin/env bash
# Fake curl: serves the cosign download from FAKE_COSIGN_DOWNLOAD.
out="" url=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) out="$2"; shift ;;
        -*) ;;
        *) url="$1" ;;
    esac
    shift
done
printf 'curl %s\n' "$url" >> "$FAKE_LOG"
src=""
case "$url" in
    https://github.com/sigstore/cosign/releases/download/*) src="${FAKE_COSIGN_DOWNLOAD:-}" ;;
esac
if [ -z "$src" ] || [ ! -f "$src" ]; then
    echo "curl: (22) The requested URL returned error: 404" >&2
    exit 22
fi
cp "$src" "$out"
STUB
chmod +x "$STUBS/curl"

# A fake cosign whose checksum the sandbox pins, and another file.
FAKE_COSIGN="$TMP_ROOT/fake-cosign"; OTHER_COSIGN="$TMP_ROOT/other-cosign"
printf '#!/bin/sh\necho "fake cosign $*"\n' > "$FAKE_COSIGN"
printf '#!/bin/sh\necho "other cosign $*"\n' > "$OTHER_COSIGN"
chmod +x "$FAKE_COSIGN" "$OTHER_COSIGN"
FAKE_SHA="$(sha256sum "$FAKE_COSIGN" | cut -d' ' -f1)"
sha_of() { sha256sum "$1" 2>/dev/null | cut -d' ' -f1; }

sed -e '/^main "\$@"$/d' install-vm.sh > "$TMP_ROOT/install-vm-lib.sh"
cp install-preflight.sh "$TMP_ROOT/"

# vm <case> [VAR=value …] -- <script> — the installer functions (sourced
# without main, non-interactive) in a clean environment: collector.env,
# INSTALL_DIR, the spool, the identity paths, the cosign cache and
# UPDATE_COSIGN below the case directory, which a later call reuses; the
# fake cosign pinned unless PIN=real. Calls of curl are logged to
# <case dir>/calls (reset per call).
vm() {
    local dir="$TMP_ROOT/case-$1"; shift
    local vars=()
    while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do vars+=("$1"); shift; done
    shift
    mkdir -p "$dir/tmp"
    : > "$dir/calls"
    env -i PATH="$STUBS:$SYS" HOME="$dir" TMPDIR="$dir/tmp" CASE="$dir" FAKE_LOG="$dir/calls" \
        FAKE_COSIGN_DOWNLOAD="$FAKE_COSIGN" FAKE_SHA="$FAKE_SHA" PIN_VAR="$PIN_VAR" "${vars[@]}" \
        bash -c 'set -euo pipefail; . "$0" --non-interactive
            INSTALL_DIR="$CASE/opt"; ENV_FILE="$INSTALL_DIR/collector.env"; SPOOL_DIR="$CASE/spool"
            CREDENTIALS_PATH="$CASE/var-lib/credentials.json"; LEGACY_CREDENTIALS_PATH="$INSTALL_DIR/credentials.json"
            UPDATE_COSIGN="$CASE/usr-local-bin/cosign"; RETICORA_TOOLS_DIR="$CASE/tools"
            [ "${PIN:-}" = real ] || printf -v "$PIN_VAR" "%s" "$FAKE_SHA"
            '"$1" "$TMP_ROOT/install-vm-lib.sh" 2>&1
}
case_dir() { printf '%s' "$TMP_ROOT/case-$1"; }
cosign_of() { printf '%s/usr-local-bin/cosign' "$(case_dir "$1")"; }
env_value() { sed -nE "s/^$1=(.*)\$/\1/p" "$(case_dir "$2")/opt/collector.env" | tail -n 1; }
downloads() { grep -c '^curl https://github.com/sigstore/cosign/' "$(case_dir "$1")/calls"; }

# The whole run (main, systemd mode, --build-from-source): preflight and the
# collector and unit installation stubbed; the collector stub reports
# whether cosign is in place.
MAIN='preflight() { :; }; ensure_sources() { :; }; verify() { :; }; print_summary() { :; }
    provide_collector_systemd() { echo "STUB provide_collector_systemd (cosign: $( [ -x "$UPDATE_COSIGN" ] && echo present || echo missing))"; }
    install_systemd_unit() { echo "STUB install_systemd_unit"; }
    install_docker_setup() { echo "STUB install_docker_setup"; }
    BUILD_FROM_SOURCE=1; main'

# ─── With and without RETICORA_UPDATE_URL ────────────────────────────────────
OUT="$(vm upd RETICORA_UPDATE_URL="$UPDATE_URL" RETICORA_UPDATE_CHANNEL=beta -- "$MAIN")"; RC=$?
if [ "$RC" -eq 0 ] && [ -x "$(cosign_of upd)" ] && [ "$(sha_of "$(cosign_of upd)")" = "$FAKE_SHA" ] \
    && grep -qx "curl https://github.com/sigstore/cosign/releases/download/v2.4.1/cosign-linux-$ARCH" "$(case_dir upd)/calls" \
    && grep -q 'STUB provide_collector_systemd (cosign: present)' <<<"$OUT" \
    && grep -q "cosign v2.4.1 for the signed auto-updates: $(cosign_of upd) (sha256 checked)" <<<"$OUT"; then
    ok "with RETICORA_UPDATE_URL: the pinned cosign v2.4.1 (sha256 checked) is installed before the collector"
else
    fail "with RETICORA_UPDATE_URL: the pinned cosign v2.4.1 (sha256 checked) is installed before the collector" \
        "rc=$RC calls: $(cat "$(case_dir upd)/calls")
$OUT"
fi
if [ "$(env_value RETICORA_UPDATE_URL upd)" = "$UPDATE_URL" ] && [ "$(env_value RETICORA_UPDATE_CHANNEL upd)" = beta ]; then
    ok "with RETICORA_UPDATE_URL: collector.env enables the auto-updates of the service"
else
    fail "with RETICORA_UPDATE_URL: collector.env enables the auto-updates of the service" "$(cat "$(case_dir upd)/opt/collector.env" 2>&1)"
fi

# A re-run takes the URL from collector.env and finds the pinned cosign.
OUT="$(vm upd -- "$MAIN")"; RC=$?
if [ "$RC" -eq 0 ] && [ "$(env_value RETICORA_UPDATE_URL upd)" = "$UPDATE_URL" ] && [ "$(downloads upd)" -eq 0 ] \
    && [ "$(sha_of "$(cosign_of upd)")" = "$FAKE_SHA" ]; then
    ok "re-run: the update URL of collector.env stays, the installed cosign is kept without a download"
else
    fail "re-run: the update URL of collector.env stays, the installed cosign is kept without a download" "rc=$RC $(cat "$(case_dir upd)/calls")
$OUT"
fi

OUT="$(vm none -- "$MAIN")"; RC=$?
if [ "$RC" -eq 0 ] && [ ! -e "$(cosign_of none)" ] && [ ! -e "$(case_dir none)/usr-local-bin" ] && [ ! -s "$(case_dir none)/calls" ] \
    && [ -z "$(env_value RETICORA_UPDATE_URL none)" ] && grep -q 'STUB provide_collector_systemd' <<<"$OUT"; then
    ok "without RETICORA_UPDATE_URL: no cosign is installed or downloaded"
else
    fail "without RETICORA_UPDATE_URL: no cosign is installed or downloaded" "rc=$RC $(cat "$(case_dir none)/calls")
$OUT"
fi

# ─── Fail-closed ─────────────────────────────────────────────────────────────
OUT="$(vm bad RETICORA_UPDATE_URL="$UPDATE_URL" FAKE_COSIGN_DOWNLOAD="$OTHER_COSIGN" -- "$MAIN")"; RC=$?
if [ "$RC" -ne 0 ] && [ ! -e "$(cosign_of bad)" ] && [ ! -e "$(cosign_of bad).part" ] \
    && grep -q 'The downloaded cosign v2.4.1 does not match its published sha256 checksum; it was not installed' <<<"$OUT" \
    && ! grep -q 'STUB provide_collector_systemd' <<<"$OUT"; then
    ok "a downloaded cosign with another checksum is not installed, the installation stops before the collector"
else
    fail "a downloaded cosign with another checksum is not installed, the installation stops before the collector" "rc=$RC
$OUT"
fi

OUT="$(vm nodl RETICORA_UPDATE_URL="$UPDATE_URL" FAKE_COSIGN_DOWNLOAD=/nonexistent -- "$MAIN")"; RC=$?
if [ "$RC" -ne 0 ] && [ ! -e "$(cosign_of nodl)" ] && [ ! -e "$(cosign_of nodl).part" ] \
    && grep -q 'cosign v2.4.1 for the auto-updates could not be downloaded' <<<"$OUT" \
    && ! grep -q 'STUB provide_collector_systemd' <<<"$OUT"; then
    ok "a failed cosign download installs nothing, the installation stops before the collector"
else
    fail "a failed cosign download installs nothing, the installation stops before the collector" "rc=$RC
$OUT"
fi

OUT="$(vm badurl RETICORA_UPDATE_URL=updates.example.test -- "$MAIN")"; RC=$?
if [ "$RC" -ne 0 ] && [ ! -e "$(cosign_of badurl)" ] && grep -q 'Invalid value for Base URL of the signed collector releases' <<<"$OUT"; then
    ok "an update URL that is no http(s) URL is refused"
else
    fail "an update URL that is no http(s) URL is refused" "rc=$RC
$OUT"
fi

# ─── Replace, reuse ──────────────────────────────────────────────────────────
mkdir -p "$(case_dir replace)/usr-local-bin"
cp "$OTHER_COSIGN" "$(cosign_of replace)"
OUT="$(vm replace RETICORA_UPDATE_URL="$UPDATE_URL" -- "$MAIN")"; RC=$?
if [ "$RC" -eq 0 ] && [ "$(sha_of "$(cosign_of replace)")" = "$FAKE_SHA" ] && [ "$(downloads replace)" -eq 1 ]; then
    ok "another cosign at the path is replaced by the pinned one"
else
    fail "another cosign at the path is replaced by the pinned one" "rc=$RC
$OUT"
fi

# The pinned cosign that verified the collector release (ensure_cosign,
# COSIGN_BIN) is copied; a download would fail.
OUT="$(vm reuse RETICORA_UPDATE_URL="$UPDATE_URL" FAKE_COSIGN_DOWNLOAD=/nonexistent FAKE_PINNED="$FAKE_COSIGN" -- \
    'mkdir -p "$CASE/tools"; cp "$FAKE_PINNED" "$CASE/tools/cosign"; COSIGN_BIN="$CASE/tools/cosign"; '"$MAIN")"; RC=$?
if [ "$RC" -eq 0 ] && [ "$(sha_of "$(cosign_of reuse)")" = "$FAKE_SHA" ] && [ "$(downloads reuse)" -eq 0 ]; then
    ok "the pinned cosign that verified the release is reused without a second download"
else
    fail "the pinned cosign that verified the release is reused without a second download" "rc=$RC
$OUT"
fi

# ─── Docker mode ─────────────────────────────────────────────────────────────
OUT="$(vm docker RETICORA_UPDATE_URL="$UPDATE_URL" -- 'MODE=docker; '"$MAIN")"; RC=$?
if [ "$RC" -eq 0 ] && [ ! -e "$(cosign_of docker)" ] && [ ! -s "$(case_dir docker)/calls" ] \
    && [ -z "$(env_value RETICORA_UPDATE_URL docker)" ] && grep -q 'RETICORA_UPDATE_URL is not used in the Docker mode' <<<"$OUT" \
    && grep -q 'STUB install_docker_setup' <<<"$OUT"; then
    ok "Docker mode: no cosign and no update URL, the image is updated by the installer"
else
    fail "Docker mode: no cosign and no update URL, the image is updated by the installer" "rc=$RC
$OUT"
fi

# ─── The pin ─────────────────────────────────────────────────────────────────
# Defaults of the installer: the cosign pin of the signature block, the same
# as the one install-smoke.yml checks, and the path in the default PATH of
# systemd services, which the unit does not replace.
defaults="$(env -i PATH="$SYS" bash -c 'set -euo pipefail; . "$0" --non-interactive
    printf "%s %s %s\n" "$UPDATE_COSIGN" "$COSIGN_VERSION" "$COSIGN_SHA256_AMD64"' "$TMP_ROOT/install-vm-lib.sh" 2>&1)"
wf=".github/workflows/install-smoke.yml"
wf_pin="$(sed -nE 's/^ +COSIGN_VERSION: (v[0-9.]+)$/\1/p' "$wf" | sort -u) $(sed -nE 's/^ +COSIGN_SHA256: ([0-9a-f]{64})$/\1/p' "$wf" | sort -u)"
if [ "$defaults" = "/usr/local/bin/cosign $wf_pin" ] && ! grep -q '^Environment=.*PATH=' install-vm.sh; then
    ok "pin: cosign $wf_pin as /usr/local/bin/cosign, the same as install-smoke.yml, in the PATH of the service"
else
    fail "pin: cosign $wf_pin as /usr/local/bin/cosign, the same as install-smoke.yml, in the PATH of the service" \
        "installer: $defaults; install-smoke.yml: $wf_pin"
fi

OUT="$(vm realpin PIN=real RETICORA_UPDATE_URL="$UPDATE_URL" -- "$MAIN")"; RC=$?
if [ "$RC" -ne 0 ] && [ ! -e "$(cosign_of realpin)" ] && grep -q 'does not match its published sha256 checksum' <<<"$OUT"; then
    ok "pin: the published checksum refuses a file other than the cosign release"
else
    fail "pin: the published checksum refuses a file other than the cosign release" "rc=$RC
$OUT"
fi

if [ -z "${RETICORA_TEST_COSIGN:-}" ]; then
    printf 'skip real cosign case (RETICORA_TEST_COSIGN not set)\n'
elif [ "$ARCH" != amd64 ]; then
    printf 'skip real cosign case (RETICORA_TEST_COSIGN is the linux-amd64 release, this is %s)\n' "$ARCH"
else
    OUT="$(vm real PIN=real RETICORA_UPDATE_URL="$UPDATE_URL" FAKE_COSIGN_DOWNLOAD="$RETICORA_TEST_COSIGN" -- \
        "$MAIN"'; "$UPDATE_COSIGN" version 2>&1 | grep -E "GitVersion"')"; RC=$?
    if [ "$RC" -eq 0 ] && grep -qE 'GitVersion: +v2\.4\.1' <<<"$OUT"; then
        ok "real cosign: the release binary v2.4.1 linux-amd64 matches the pin and is installed for the updater"
    else
        fail "real cosign: the release binary v2.4.1 linux-amd64 matches the pin and is installed for the updater" "rc=$RC
$OUT"
    fi
fi

echo
printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
