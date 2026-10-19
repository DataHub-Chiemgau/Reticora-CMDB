#!/usr/bin/env bash
#
# collector-install-cli.test.sh — Phase-1 acceptance audit, group
# T4-collector-load, DEFECT test (fails until the defect is fixed; not wired
# into CI).
#
# WP-156 (INS-06, COL-04). Finding P2 "Installed collector does not match the
# UI enroll command (binary name and credentials path differ)": the Discovery
# page shows `reticora-collector enroll --server <origin> --code <code>`, but
#   - install-vm.sh installs the binary as /opt/reticora-collector/collector
#     and puts no `reticora-collector` on PATH; the image ships
#     /usr/local/bin/collector;
#   - install-vm.sh writes RETICORA_CREDENTIALS_PATH=/opt/reticora-collector/
#     credentials.json into collector.env (the unit's EnvironmentFile), while
#     the CLI run from a shell (without that file) stores the identity at
#     enrollment.DefaultCredentialsPath (/var/lib/reticora-collector/...).
#     The service never sees the identity the UI command created.
#
# Requirement: one configuration path and a working UI command on an
# installed collector (systemd and Docker).
#
# The test runs the installer's own functions (collect_config, write_env,
# install_systemd_unit) non-interactively against a temporary env file and
# unit path with a systemctl stub; nothing is installed on the host. The
# binary name and the image are checked in install-vm.sh and
# collector/Dockerfile.
#
# Retest: ./tests/audit/collector-install-cli.test.sh
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
ROOT="$(pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

# The command the UI shows and the CLI's default identity path.
ui_bin="$(grep -oE '`[a-z-]+ enroll --server' "$ROOT/frontend/src/pages/DiscoveryPage.tsx" | head -1 | sed -E 's/^`([a-z-]+) .*/\1/')"
cli_default="$(sed -nE 's/^const DefaultCredentialsPath = "(.*)"$/\1/p' "$ROOT/edgecore/enrollment/enrollment.go")"
if [ -z "$ui_bin" ] || [ -z "$cli_default" ]; then
    echo "FAIL cannot read the UI enroll command ($ui_bin) or the CLI default path ($cli_default)"
    exit 1
fi
ok "UI enroll command uses '$ui_bin'; CLI default identity path $cli_default"

# The installer's functions without its main call.
sed -e '/^main "\$@"$/d' "$ROOT/install-vm.sh" > "$TMP/install-vm-lib.sh"
cp "$ROOT/install-preflight.sh" "$TMP/install-preflight.sh"
mkdir -p "$TMP/bin" "$TMP/opt"
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/systemctl"
chmod +x "$TMP/bin/systemctl"

(
    export PATH="$TMP/bin:$PATH"
    # shellcheck disable=SC1091
    . "$TMP/install-vm-lib.sh" --non-interactive
    set +e
    ENV_FILE="$TMP/opt/collector.env"     # no earlier configuration
    collect_config >/dev/null
    INSTALL_DIR="$TMP/opt"                # write below the temp directory
    SYSTEMD_UNIT="$TMP/reticora-collector.service"
    write_env >/dev/null
    install_systemd_unit >/dev/null
) > "$TMP/installer.log" 2>&1
if [ ! -f "$TMP/opt/collector.env" ] || [ ! -f "$TMP/reticora-collector.service" ]; then
    fail "control: the installer functions wrote no collector.env/unit: $(tail -3 "$TMP/installer.log")"
else
    ok "installer functions wrote collector.env and the systemd unit"
    if grep -q "^EnvironmentFile=$TMP/opt/collector.env$" "$TMP/reticora-collector.service"; then
        ok "the unit reads collector.env"
    else
        fail "control: the unit does not read collector.env"
    fi
    env_path="$(sed -nE 's/^RETICORA_CREDENTIALS_PATH=(.*)$/\1/p' "$TMP/opt/collector.env" | tail -1)"
    if [ -z "$env_path" ] || [ "$env_path" = "$cli_default" ]; then
        ok "collector.env credentials path matches the CLI default ($cli_default)"
    else
        fail "INS-06 violated: collector.env sets RETICORA_CREDENTIALS_PATH=$env_path, but '$ui_bin enroll' run from a shell stores the identity at $cli_default; the service never sees it"
    fi
fi

# The UI command must resolve on an installed collector.
if grep -Eq "(/usr/local/bin|/usr/bin)/$ui_bin([^-a-z]|$)" "$ROOT/install-vm.sh"; then
    ok "install-vm.sh puts $ui_bin on PATH"
else
    fail "INS-06 violated: install-vm.sh installs the binary as /opt/reticora-collector/collector and puts no '$ui_bin' on PATH; the UI command fails with 'command not found'"
fi
if grep -Eq "(/usr/local/bin|/usr/bin)/$ui_bin([^-a-z]|$)" "$ROOT/collector/Dockerfile"; then
    ok "the collector image provides $ui_bin on PATH"
else
    fail "INS-06 violated: the collector image ships $(sed -nE 's/^COPY --from=builder [^ ]+ (.*)$/\1/p' "$ROOT/collector/Dockerfile" | head -1), not '$ui_bin'"
fi

echo
echo "collector-install-cli: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
