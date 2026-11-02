#!/usr/bin/env bash
# shellcheck disable=SC2016,SC2034,SC2153 # the scripts in single quotes and the variables set for them belong to the sourced installer
#
# collector-install-cli.test.sh — INS-06 (WP-156): the installed collector
# matches the enroll command of the UI (retest F-110, WP-432). Formerly a
# defect test of the Phase-1 acceptance audit (group T4-collector-load); runs
# in the CI job installer-script-tests (install-smoke.yml).
#
# The Discovery page shows `reticora-collector enroll --server <origin> --code
# <code>`. On an installed collector (systemd and Docker)
#   - the command is on PATH: install-vm.sh links /usr/local/bin/
#     reticora-collector to the installed binary and the systemd unit starts
#     that command; the image ships /usr/local/bin/reticora-collector as its
#     entrypoint ("collector" stays as a link);
#   - the identity the command stores when run from a shell (without
#     collector.env: enrollment.DefaultCredentialsPath) is the one the service
#     reads: collector.env, the unit's EnvironmentFile, names the same path —
#     also on a re-run over the configuration of an older installer, which
#     moves an identity from /opt/reticora-collector/credentials.json; in the
#     Docker mode the compose project mounts its directory, which belongs to
#     the image user.
#
# The test runs the installer's own functions (collect_config, write_env,
# provide_collector_systemd, install_systemd_unit, install_docker_setup)
# non-interactively below a temporary directory with stubs for systemctl and
# docker; nothing is installed on the host. The enrollment over PATH against
# an installed stack runs in install-smoke.yml (install-cloud-smoke).
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
mkdir -p "$TMP/bin"
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/systemctl"
printf '#!/bin/sh\necho "docker $*" >> "$CASE/docker.log"\nexit 0\n' > "$TMP/bin/docker"
chmod +x "$TMP/bin/systemctl" "$TMP/bin/docker"

# installer <case> <script> — sources the installer functions (systemd mode,
# non-interactive, no RETICORA_* of the environment) and runs <script> with
# collector.env, INSTALL_DIR, the unit, the command link, the spool and the
# former identity path below $TMP/<case> (CASE); the identity path keeps its
# default unless <script> redirects it. The defaults are written to
# $CASE/defaults, the output to $CASE.log.
installer() {
    local dir="$TMP/$1"
    mkdir -p "$dir"
    (
        unset "${!RETICORA_@}"
        export PATH="$TMP/bin:$PATH" CASE="$dir"
        # shellcheck disable=SC1091
        . "$TMP/install-vm-lib.sh" --non-interactive
        set +e
        printf 'CREDENTIALS_PATH=%s\nLEGACY_CREDENTIALS_PATH=%s\nCOLLECTOR_COMMAND=%s\nIMAGE_USER=%s\n' \
            "${CREDENTIALS_PATH:-}" "${LEGACY_CREDENTIALS_PATH:-}" "${COLLECTOR_COMMAND:-}" "${IMAGE_USER:-}" > "$CASE/defaults"
        INSTALL_DIR="$CASE/opt"
        ENV_FILE="$INSTALL_DIR/collector.env"
        LEGACY_CREDENTIALS_PATH="$INSTALL_DIR/credentials.json"
        SYSTEMD_UNIT="$CASE/reticora-collector.service"
        COLLECTOR_COMMAND="$CASE/usr-local-bin/reticora-collector"
        SPOOL_DIR="$CASE/spool"
        eval "$2"
    ) > "$dir.log" 2>&1
}
env_value() { sed -nE "s/^$1=(.*)\$/\1/p" "$2" | tail -1; }
default_of() { env_value "$1" "$TMP/$2/defaults"; }
log_tail() { tail -3 "$TMP/$1.log" | tr '\n' ' '; }

# ─── Fresh installation (systemd) ─────────────────────────────────────────────
# A prebuilt binary (--build-from-source) that reports its arguments.
mkdir -p "$TMP/fresh/src/bin"
printf '#!/bin/sh\necho "installed collector $*"\n' > "$TMP/fresh/src/bin/collector"
chmod +x "$TMP/fresh/src/bin/collector"
installer fresh 'collect_config && write_env || exit 1
    BUILD_FROM_SOURCE=1; SOURCE_DIR="$CASE/src"
    provide_collector_systemd && install_systemd_unit'
env_file="$TMP/fresh/opt/collector.env"
unit="$TMP/fresh/reticora-collector.service"
link="$TMP/fresh/usr-local-bin/reticora-collector"
if [ ! -f "$env_file" ] || [ ! -f "$unit" ]; then
    fail "control: the installer functions wrote no collector.env/unit: $(log_tail fresh)"
else
    ok "installer functions wrote collector.env and the systemd unit"
    if grep -qx "EnvironmentFile=$env_file" "$unit"; then
        ok "the unit reads collector.env"
    else
        fail "control: the unit does not read collector.env"
    fi
    env_path="$(env_value RETICORA_CREDENTIALS_PATH "$env_file")"
    if [ -z "$env_path" ] || [ "$env_path" = "$cli_default" ]; then
        ok "collector.env credentials path matches the CLI default ($cli_default)"
    else
        fail "INS-06 violated: collector.env sets RETICORA_CREDENTIALS_PATH=$env_path, but '$ui_bin enroll' run from a shell stores the identity at $cli_default; the service never sees it"
    fi
    if grep -qx "ExecStart=$link" "$unit"; then
        ok "the unit starts the command of the UI ($ui_bin)"
    else
        fail "INS-06 violated: the unit starts $(sed -n 's/^ExecStart=//p' "$unit"), not the '$ui_bin' of the UI command"
    fi
fi

# The UI command must resolve on an installed collector.
command_default="$(default_of COLLECTOR_COMMAND fresh)"
if [[ "$command_default" =~ ^(/usr/local/bin|/usr/bin)/$ui_bin$ ]] && [ -L "$link" ] \
    && [ "$(readlink -f "$link")" = "$(readlink -f "$TMP/fresh/opt/collector")" ] \
    && [ "$("$link" enroll --server https://cmdb.example.test --code C0DE 2>&1)" = "installed collector enroll --server https://cmdb.example.test --code C0DE" ]; then
    ok "install-vm.sh puts $ui_bin on PATH ($command_default → the installed binary)"
else
    fail "INS-06 violated: install-vm.sh puts no '$ui_bin' on PATH that runs the installed binary (command ${command_default:-<none>}, link $(readlink "$link" 2>/dev/null || echo '<none>')); the UI command fails with 'command not found'"
fi

# ─── Re-run over an older installation ───────────────────────────────────────
# collector.env of an installer before WP-432 names the former path, where the
# service kept its identity.
legacy_default="$(default_of LEGACY_CREDENTIALS_PATH fresh)"
for c in upgrade upgrade-both; do
    mkdir -p "$TMP/$c/opt" "$TMP/$c/var-lib"
    printf 'RETICORA_SERVER_URL=https://cmdb.example.test\nRETICORA_CREDENTIALS_PATH=%s\n' "$TMP/$c/opt/credentials.json" > "$TMP/$c/opt/collector.env"
    printf '{"device_id":"legacy"}\n' > "$TMP/$c/opt/credentials.json"
done
printf '{"device_id":"enrolled"}\n' > "$TMP/upgrade-both/var-lib/credentials.json"
for c in upgrade upgrade-both; do
    installer "$c" 'CREDENTIALS_PATH="$CASE/var-lib/credentials.json"; collect_config && write_env'
done
if [ "$legacy_default" = /opt/reticora-collector/credentials.json ] \
    && [ "$(env_value RETICORA_CREDENTIALS_PATH "$TMP/upgrade/opt/collector.env")" = "$TMP/upgrade/var-lib/credentials.json" ] \
    && [ "$(env_value RETICORA_SERVER_URL "$TMP/upgrade/opt/collector.env")" = https://cmdb.example.test ]; then
    ok "re-run over an older installation: collector.env names the default identity path"
else
    fail "INS-06 violated: a re-run keeps RETICORA_CREDENTIALS_PATH=$(env_value RETICORA_CREDENTIALS_PATH "$TMP/upgrade/opt/collector.env") of the older installation (former path ${legacy_default:-<unknown>}): $(log_tail upgrade)"
fi
if [ "$(cat "$TMP/upgrade/var-lib/credentials.json" 2>/dev/null)" = '{"device_id":"legacy"}' ] && [ ! -e "$TMP/upgrade/opt/credentials.json" ] \
    && grep -q "Collector identity moved from $TMP/upgrade/opt/credentials.json" "$TMP/upgrade.log"; then
    ok "re-run over an older installation: its identity is moved to the default path"
else
    fail "INS-06 violated: the identity of the older installation was not moved to the default path: $(log_tail upgrade)"
fi
if [ "$(cat "$TMP/upgrade-both/var-lib/credentials.json")" = '{"device_id":"enrolled"}' ] && [ -f "$TMP/upgrade-both/opt/credentials.json" ] \
    && grep -q 'of an older installation is no longer read' "$TMP/upgrade-both.log"; then
    ok "re-run with an identity at both paths: the default one stays, a warning names the former one"
else
    fail "INS-06 violated: with an identity at both paths the default one was replaced or no warning was given: $(log_tail upgrade-both)"
fi

# Another path of the operator stays, with the --credentials hint.
mkdir -p "$TMP/custom/opt"
printf 'RETICORA_CREDENTIALS_PATH=/srv/reticora/identity.json\n' > "$TMP/custom/opt/collector.env"
installer custom 'collect_config && write_env'
if [ "$(env_value RETICORA_CREDENTIALS_PATH "$TMP/custom/opt/collector.env")" = /srv/reticora/identity.json ] \
    && grep -q -- "--credentials /srv/reticora/identity.json" "$TMP/custom.log"; then
    ok "an identity path of the operator stays, the installer names the enroll option for it"
else
    fail "an identity path of the operator stays, the installer names the enroll option for it: $(log_tail custom)"
fi

# ─── Docker mode ──────────────────────────────────────────────────────────────
mkdir -p "$TMP/docker/src/collector"
: > "$TMP/docker/src/collector/Dockerfile"
installer docker 'MODE=docker; CREDENTIALS_PATH="$CASE/var-lib/credentials.json"
    collect_config && write_env || exit 1
    BUILD_FROM_SOURCE=1; SOURCE_DIR="$CASE/src"; install_docker_setup'
compose="$TMP/docker/opt/docker-compose.yml"
if [ "$(env_value RETICORA_CREDENTIALS_PATH "$TMP/docker/opt/collector.env")" = "$TMP/docker/var-lib/credentials.json" ] \
    && grep -qx "      - $TMP/docker/var-lib:$TMP/docker/var-lib" "$compose" && grep -qx "      - $TMP/docker/opt/collector.env" "$compose"; then
    ok "Docker mode: the container gets the default identity path and its directory"
else
    fail "INS-06 violated: in the Docker mode the compose project does not mount the identity directory of collector.env: $(log_tail docker)"
fi
image_user="$(default_of IMAGE_USER docker)"
if ! grep -qx 'USER nobody:nobody' "$ROOT/collector/Dockerfile" || [ "$image_user" != 65534:65534 ]; then
    fail "INS-06 violated: the installer hands the identity directory to $image_user, the image runs as $(sed -n 's/^USER //p' "$ROOT/collector/Dockerfile") (nobody of alpine is 65534:65534)"
elif [ "$(stat -c %u:%g:%a "$TMP/docker/var-lib")" = 65534:65534:700 ]; then
    ok "Docker mode: the identity directory belongs to the image user (65534:65534, mode 700)"
elif [ "$(id -u)" -ne 0 ] && grep -q "Could not hand $TMP/docker/var-lib to the user of the collector image (65534:65534)" "$TMP/docker.log"; then
    ok "Docker mode: the identity directory belongs to the image user (not root: the installer warns that it cannot hand it over)"
else
    fail "INS-06 violated: in the Docker mode the identity directory does not belong to the image user (owner and mode $(stat -c %u:%g:%a "$TMP/docker/var-lib" 2>/dev/null)): $(log_tail docker)"
fi
# The directory of an identity path of the operator is never handed over.
mkdir -p "$TMP/docker-custom/opt" "$TMP/docker-custom/srv"
printf 'RETICORA_CREDENTIALS_PATH=%s\n' "$TMP/docker-custom/srv/identity.json" > "$TMP/docker-custom/opt/collector.env"
owner_before="$(stat -c %u:%g:%a "$TMP/docker-custom/srv")"
installer docker-custom 'MODE=docker; CREDENTIALS_PATH="$CASE/var-lib/credentials.json"; collect_config && write_env'
if [ "$(stat -c %u:%g:%a "$TMP/docker-custom/srv")" = "$owner_before" ] && [ ! -e "$TMP/docker-custom/var-lib" ] \
    && grep -q "make $TMP/docker-custom/srv writable for it" "$TMP/docker-custom.log"; then
    ok "Docker mode: the directory of an operator's identity path is not handed to the image user"
else
    fail "Docker mode: the directory of an operator's identity path is not handed to the image user (before $owner_before, after $(stat -c %u:%g:%a "$TMP/docker-custom/srv")): $(log_tail docker-custom)"
fi

# ─── Image ────────────────────────────────────────────────────────────────────
dockerfile="$ROOT/collector/Dockerfile"
if grep -Eq "^COPY --from=builder [^ ]+ /usr/local/bin/$ui_bin\$" "$dockerfile" \
    && grep -qx "ENTRYPOINT \[\"$ui_bin\"\]" "$dockerfile"; then
    ok "the collector image provides $ui_bin on PATH as its entrypoint"
else
    fail "INS-06 violated: the collector image ships $(sed -nE 's/^COPY --from=builder [^ ]+ (.*)$/\1/p' "$dockerfile" | head -1) with $(grep '^ENTRYPOINT' "$dockerfile"), not '$ui_bin'"
fi
if grep -qx "RUN ln -s $ui_bin /usr/local/bin/collector" "$dockerfile"; then
    ok "the collector image keeps 'collector' as a link for existing calls"
else
    fail "INS-06 violated: the collector image has no 'collector' link to $ui_bin for existing calls"
fi

echo
echo "collector-install-cli: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
