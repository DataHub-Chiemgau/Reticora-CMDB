#!/usr/bin/env bash
# shellcheck disable=SC2016,SC2034,SC2163,SC2329 # the scripts in single quotes, the variables and functions set for them (VAR=value exported by name) belong to the sourced installer
#
# install-vm-enroll.test.sh — INS-06, COL-08: the collector installed by
# install-vm.sh is enrolled and delivers over its mTLS edge (the collector
# side of the "collector sends traffic, the cloud gets nothing" fix).
#
#   - defaults: collector.env leaves organization, collector ID and the
#     cloud URL empty (set by the enrollment); the placeholders of older
#     installers (https://cmdb.example.com, reticora-demo, the host name)
#     are not reused, a real value stays;
#   - the one-time enrollment code (prompt with hidden input, or
#     RETICORA_ENROLL_CODE) enrolls the collector with
#     'reticora-collector enroll --server <url>' before the service is
#     (re)started; the code reaches the command in RETICORA_ENROLL_TOKEN,
#     never on its command line, and is never written to collector.env or
#     printed; a failed enrollment is reported and the installation goes
#     on; an enrolled collector is not enrolled again; without a cloud URL
#     the code is not used;
#   - Docker mode: the enrollment runs in the image before the container
#     starts, then the container is restarted; a private cloud CA (and a
#     client certificate) outside the mounted directories is mounted
#     read-only; the spool belongs to the image user (65534:65534, mode 700);
#   - verify: the NATS endpoint <host of the cloud URL>:4222 (or
#     RETICORA_NATS_URL, or the server URL of the enrollment) is probed over
#     TCP with a timeout; an unreachable endpoint is a clear warning;
#   - the summary names the enrollment state, the NATS endpoint and the
#     scope confirmation.
#
# The installer's functions run sourced (without main, or with main and the
# platform steps stubbed) below a temporary directory with stubs for
# systemctl, docker and the collector binary; nothing is installed on the
# host.
#
# Usage: ./tests/install-vm-enroll.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
ROOT="$(pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"; [ -z "${LISTENER:-}" ] || kill "$LISTENER" 2>/dev/null' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | tail -n 12 | sed 's/^/     /'; return 0; }

sed -e '/^main "\$@"$/d' "$ROOT/install-vm.sh" > "$TMP/install-vm-lib.sh"
cp "$ROOT/install-preflight.sh" "$TMP/install-preflight.sh"
mkdir -p "$TMP/bin"
printf '#!/bin/sh\necho "systemctl $*" >> "$CASE/calls"\nexit 0\n' > "$TMP/bin/systemctl"
# docker: records its arguments and the enrollment token of its environment.
printf '#!/bin/sh\necho "docker $* token=${RETICORA_ENROLL_TOKEN:-}" >> "$CASE/calls"\nexit 0\n' > "$TMP/bin/docker"
chmod +x "$TMP/bin/systemctl" "$TMP/bin/docker"

# The collector binary the installer provides: records its arguments and the
# token of its environment; 'enroll' stores an identity at --credentials
# (exit FAKE_ENROLL_RC, default 0).
FAKE_COLLECTOR="$TMP/fake-collector"
cat > "$FAKE_COLLECTOR" <<'STUB'
#!/bin/sh
echo "collector $* token=${RETICORA_ENROLL_TOKEN:-}" >> "$CASE/calls"
creds="" server=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        --credentials) creds="$2"; shift ;;
        --server) server="$2"; shift ;;
    esac
    shift
done
rc="${FAKE_ENROLL_RC:-0}"
if [ "$rc" -eq 0 ] && [ -n "$creds" ]; then
    mkdir -p "$(dirname "$creds")"
    printf '{\n  "device_id": "c1",\n  "metadata": {\n    "server_url": "%s"\n  }\n}\n' "$server" > "$creds"
fi
exit "$rc"
STUB
chmod +x "$FAKE_COLLECTOR"

# installer <case> <script> [VAR=value …] — sources the installer functions
# (systemd mode, non-interactive unless INTERACTIVE=1, no RETICORA_* of the
# environment) and runs <script> with collector.env, INSTALL_DIR, the unit,
# the command link, the spool and the identity below $TMP/<case> (CASE).
# The output goes to $CASE.log, the calls of the stubs to $CASE/calls.
installer() {
    local name="$1" script="$2"; shift 2
    local dir="$TMP/$name"
    mkdir -p "$dir"
    : > "$dir/calls"
    (
        unset "${!RETICORA_@}"
        export PATH="$TMP/bin:$PATH" CASE="$dir"
        [ "$#" -eq 0 ] || export "$@"
        if [ "${INTERACTIVE:-0}" = 1 ]; then
            # shellcheck disable=SC1091
            . "$TMP/install-vm-lib.sh" --systemd
            is_tty() { true; }
        else
            # shellcheck disable=SC1091
            . "$TMP/install-vm-lib.sh" --non-interactive
        fi
        set +e
        INSTALL_DIR="$CASE/opt"
        ENV_FILE="$INSTALL_DIR/collector.env"
        LEGACY_CREDENTIALS_PATH="$INSTALL_DIR/credentials.json"
        CREDENTIALS_PATH="$CASE/var-lib/credentials.json"
        SYSTEMD_UNIT="$CASE/reticora-collector.service"
        COLLECTOR_COMMAND="$CASE/usr-local-bin/reticora-collector"
        SPOOL_DIR="$CASE/var-lib/spool"
        eval "$script"
    ) > "$dir.log" 2>&1 < "${STDIN:-/dev/null}"
}
env_value() { sed -nE "s/^$1=(.*)\$/\1/p" "$TMP/$2/opt/collector.env" | tail -n 1; }
calls() { cat "$TMP/$1/calls"; }
log() { cat "$TMP/$1.log"; }

# The whole run (main): preflight, release and sources stubbed; the
# collector "binary" is the fake; verify probes nothing.
MAIN='preflight() { :; }; select_release() { :; }; ensure_sources() { :; }; install_update_cosign() { :; }
    provide_collector_systemd() { mkdir -p "$INSTALL_DIR"; cp "$FAKE_COLLECTOR" "$INSTALL_DIR/collector"; echo "provide_collector_systemd" >> "$CASE/calls"; }
    install_systemd_unit() { echo "install_systemd_unit (restart)" >> "$CASE/calls"; }
    probe_tcp() { return 0; }; sleep() { :; }
    main'
export FAKE_COLLECTOR

# ─── Defaults ─────────────────────────────────────────────────────────────────
installer defaults 'collect_config && write_env'
if [ -f "$TMP/defaults/opt/collector.env" ] && [ -z "$(env_value RETICORA_ORGANIZATION_ID defaults)" ] \
    && [ -z "$(env_value RETICORA_COLLECTOR_ID defaults)" ] && [ -z "$(env_value RETICORA_SERVER_URL defaults)" ] \
    && ! grep -qE 'cmdb\.example\.com|reticora-demo' "$TMP/defaults/opt/collector.env"; then
    ok "defaults: organization, collector ID and cloud URL are empty (set by the enrollment), no placeholders"
else
    fail "defaults: organization, collector ID and cloud URL are empty (set by the enrollment), no placeholders" \
        "$(cat "$TMP/defaults/opt/collector.env" 2>&1; log defaults)"
fi

# A re-run over the placeholders of an older installer drops them; a real
# value stays.
mkdir -p "$TMP/placeholders/opt" "$TMP/real/opt"
printf 'RETICORA_SERVER_URL=https://cmdb.example.com\nRETICORA_ORGANIZATION_ID=reticora-demo\nRETICORA_COLLECTOR_ID=%s\n' \
    "$(hostname -s 2>/dev/null || hostname)" > "$TMP/placeholders/opt/collector.env"
printf 'RETICORA_SERVER_URL=https://cloud.example.test\nRETICORA_ORGANIZATION_ID=6f0b6f0e-0000-4000-8000-000000000001\nRETICORA_COLLECTOR_ID=edge-7\n' \
    > "$TMP/real/opt/collector.env"
installer placeholders 'collect_config && write_env'
installer real 'collect_config && write_env'
if [ -z "$(env_value RETICORA_SERVER_URL placeholders)" ] && [ -z "$(env_value RETICORA_ORGANIZATION_ID placeholders)" ] \
    && [ -z "$(env_value RETICORA_COLLECTOR_ID placeholders)" ] && grep -q 'placeholder of an older installer' "$TMP/placeholders.log" \
    && [ "$(env_value RETICORA_SERVER_URL real)" = https://cloud.example.test ] && [ "$(env_value RETICORA_COLLECTOR_ID real)" = edge-7 ] \
    && [ "$(env_value RETICORA_ORGANIZATION_ID real)" = 6f0b6f0e-0000-4000-8000-000000000001 ]; then
    ok "re-run: the placeholders of an older installer are dropped, real values stay"
else
    fail "re-run: the placeholders of an older installer are dropped, real values stay" \
        "$(cat "$TMP/placeholders/opt/collector.env" "$TMP/real/opt/collector.env"; log placeholders)"
fi

# ─── Interactive: the prompts and the hidden code ────────────────────────────
# Answers: cloud URL, organization, collector ID, subnets, protocols,
# intervals, SNMP community, SSH user, spool, update URL, client
# certificate, CA, TLS server name, enrollment code.
CODE="K7QP-2MXD-9VRT"
printf '%s\n' "https://cloud.example.test" "" "" "10.20.0.0/16" "" "" "" "" "" "" "" "" "" "" "$CODE" > "$TMP/answers"
STDIN="$TMP/answers" installer interactive 'collect_config && write_env && echo "ENROLL_CODE=${ENROLL_CODE:+set}"' INTERACTIVE=1
# read -p shows a prompt only on a terminal: the prompts are checked in the
# installer, the answers in the result.
if grep -q '"Organization ID (leave empty: set by enrollment' install-vm.sh \
    && grep -q '"Collector ID (leave empty: set by enrollment' install-vm.sh \
    && grep -q 'ask_optional_secret ENROLL_CODE "One-time enrollment code (optional, input hidden)"' install-vm.sh \
    && grep -q 'Enter it to enroll the collector now' "$TMP/interactive.log" \
    && grep -qx 'ENROLL_CODE=set' "$TMP/interactive.log" && [ -z "$(env_value RETICORA_ORGANIZATION_ID interactive)" ] \
    && [ -z "$(env_value RETICORA_COLLECTOR_ID interactive)" ] && [ "$(env_value RETICORA_SCAN_SUBNETS interactive)" = 10.20.0.0/16 ] \
    && [ "$(env_value RETICORA_SERVER_URL interactive)" = https://cloud.example.test ] \
    && ! grep -qF "$CODE" "$TMP/interactive/opt/collector.env" "$TMP/interactive.log"; then
    ok "interactive: IDs default to empty (set by enrollment), the code is asked hidden and not written or echoed"
else
    fail "interactive: IDs default to empty (set by enrollment), the code is asked hidden and not written or echoed" "$(log interactive)"
fi

# ─── Enrollment in the run ───────────────────────────────────────────────────
installer enroll "$MAIN" RETICORA_ENROLL_CODE="$CODE" RETICORA_SERVER_URL=https://cloud.example.test
order="$(grep -E '^(provide_collector_systemd|collector enroll|install_systemd_unit)' "$TMP/enroll/calls" | awk '{ print $1 }' | tr '\n' '|')"
enroll_line="$(grep '^collector enroll' "$TMP/enroll/calls")"
if [ "$order" = "provide_collector_systemd|collector|install_systemd_unit|" ] \
    && [[ "$enroll_line" == "collector enroll --server https://cloud.example.test --credentials $TMP/enroll/var-lib/credentials.json token=$CODE" ]] \
    && [ -f "$TMP/enroll/var-lib/credentials.json" ]; then
    ok "with the code: 'reticora-collector enroll --server <url>' runs before the service is (re)started"
else
    fail "with the code: 'reticora-collector enroll --server <url>' runs before the service is (re)started" "order=$order
$(calls enroll)
$(log enroll)"
fi
argv="${enroll_line% token=*}"
if [[ "$argv" != *"$CODE"* ]] && ! grep -qF "$CODE" "$TMP/enroll/opt/collector.env" "$TMP/enroll.log" \
    && ! grep -q 'RETICORA_ENROLL' "$TMP/enroll/opt/collector.env"; then
    ok "the code reaches the command in RETICORA_ENROLL_TOKEN only: not on its command line, not in collector.env, not printed"
else
    fail "the code reaches the command in RETICORA_ENROLL_TOKEN only: not on its command line, not in collector.env, not printed" \
        "argv=$argv
$(cat "$TMP/enroll/opt/collector.env")"
fi
if grep -q 'Collector enrolled; its identity is stored in' "$TMP/enroll.log" && grep -q 'Enrollment:        enrolled (identity:' "$TMP/enroll.log" \
    && grep -q 'NATS endpoint:     tls://cloud.example.test:4222' "$TMP/enroll.log" \
    && grep -q 'confirm them on the Discovery page' "$TMP/enroll.log" \
    && grep -q 'Collector NATS endpoint cloud.example.test:4222 is reachable' "$TMP/enroll.log"; then
    ok "summary: enrolled, the NATS endpoint tls://<cloud host>:4222 and the scope confirmation"
else
    fail "summary: enrolled, the NATS endpoint tls://<cloud host>:4222 and the scope confirmation" "$(log enroll)"
fi

# A failed enrollment is reported; the installation goes on.
installer enroll-fails "$MAIN" RETICORA_ENROLL_CODE="$CODE" RETICORA_SERVER_URL=https://cloud.example.test FAKE_ENROLL_RC=1
if grep -q '^install_systemd_unit' "$TMP/enroll-fails/calls" && grep -q 'The enrollment failed (exit 1)' "$TMP/enroll-fails.log" \
    && grep -q 'Enrollment:        NOT enrolled' "$TMP/enroll-fails.log" \
    && grep -q 'reticora-collector enroll --server https://cloud.example.test --code <code>' "$TMP/enroll-fails.log" \
    && grep -q 'The collector is not enrolled yet' "$TMP/enroll-fails.log" && ! grep -qF "$CODE" "$TMP/enroll-fails.log"; then
    ok "a failed enrollment is a warning; the service is installed and the summary names the enroll command"
else
    fail "a failed enrollment is a warning; the service is installed and the summary names the enroll command" "$(log enroll-fails)"
fi

# An enrolled collector is not enrolled again; without a URL the code waits.
mkdir -p "$TMP/enrolled/var-lib"
printf '{"device_id":"c1","metadata":{"server_url":"https://cloud.example.test"}}\n' > "$TMP/enrolled/var-lib/credentials.json"
installer enrolled "$MAIN" RETICORA_ENROLL_CODE="$CODE"
installer no-url "$MAIN" RETICORA_ENROLL_CODE="$CODE"
if ! grep -q '^collector enroll' "$TMP/enrolled/calls" && grep -q 'enrolled already; the enrollment code is not used\|is enrolled already' "$TMP/enrolled.log" \
    && grep -q 'Collector NATS endpoint cloud.example.test:4222' "$TMP/enrolled.log" \
    && ! grep -q '^collector enroll' "$TMP/no-url/calls" && grep -q 'RETICORA_ENROLL_CODE is set but no cloud URL' "$TMP/no-url.log"; then
    ok "an enrolled collector is not enrolled again (the NATS host comes from its identity); without a cloud URL the code is not used"
else
    fail "an enrolled collector is not enrolled again (the NATS host comes from its identity); without a cloud URL the code is not used" \
        "$(calls enrolled; log enrolled; log no-url)"
fi

# ─── Docker mode ──────────────────────────────────────────────────────────────
mkdir -p "$TMP/docker/src/collector"
: > "$TMP/docker/src/collector/Dockerfile"
installer docker 'MODE=docker; collect_config && write_env || exit 1
    BUILD_FROM_SOURCE=1; SOURCE_DIR="$CASE/src"; install_docker_setup' RETICORA_ENROLL_CODE="$CODE" RETICORA_SERVER_URL=https://cloud.example.test
docker_calls="$(grep -E '^docker compose (build|run|up|restart)' "$TMP/docker/calls" | sed -E 's/ --credentials [^ ]+//')"
expected="docker compose build token=
docker compose run --rm -e RETICORA_ENROLL_TOKEN collector enroll --server https://cloud.example.test token=$CODE
docker compose up -d token=
docker compose restart collector token="
if [ "$docker_calls" = "$expected" ]; then
    ok "Docker mode: the enrollment runs in the image (code in the environment) before the container starts, then it restarts"
else
    fail "Docker mode: the enrollment runs in the image (code in the environment) before the container starts, then it restarts" \
        "$docker_calls
$(log docker)"
fi
if [ "$(stat -c %u:%g:%a "$TMP/docker/var-lib/spool")" = 65534:65534:700 ]; then
    ok "Docker mode: the spool belongs to the image user (65534:65534, mode 700)"
elif [ "$(id -u)" -ne 0 ] && grep -q "Could not hand $TMP/docker/var-lib/spool to the user of the collector image" "$TMP/docker.log"; then
    ok "Docker mode: the spool belongs to the image user (not root: the installer warns that it cannot hand it over)"
else
    fail "Docker mode: the spool belongs to the image user (65534:65534, mode 700)" \
        "$(stat -c %u:%g:%a "$TMP/docker/var-lib/spool" 2>&1); $(log docker)"
fi
# A private cloud CA outside the mounted directories is mounted read-only at
# its path, so the enrollment in the image (--ca-file) and the container can
# read it; files below a mounted directory are not mounted twice.
mkdir -p "$TMP/docker-ca/opt" "$TMP/docker-ca/pki" "$TMP/docker-ca/src/collector"
: > "$TMP/docker-ca/src/collector/Dockerfile"
CA="$TMP/docker-ca/pki/cloud-ca.pem"
printf 'CA\n' > "$CA"
printf 'RETICORA_TLS_CA_FILE=%s\n' "$CA" > "$TMP/docker-ca/opt/collector.env"
installer docker-ca 'MODE=docker; collect_config && write_env || exit 1
    BUILD_FROM_SOURCE=1; SOURCE_DIR="$CASE/src"; install_docker_setup' RETICORA_ENROLL_CODE="$CODE" RETICORA_SERVER_URL=https://cloud.example.test
compose_file="$TMP/docker-ca/opt/docker-compose.yml"
if grep -qxF "      - $CA:$CA:ro" "$compose_file" \
    && grep -q "^docker compose run --rm -e RETICORA_ENROLL_TOKEN collector enroll --server https://cloud.example.test .*--ca-file $CA token=$CODE\$" "$TMP/docker-ca/calls"; then
    ok "Docker mode: a private cloud CA is mounted read-only, so the enrollment in the image can read --ca-file"
else
    fail "Docker mode: a private cloud CA is mounted read-only, so the enrollment in the image can read --ca-file" \
        "$(cat "$compose_file" 2>&1; calls docker-ca; log docker-ca)"
fi
installer docker-mounts '
    RETICORA_SPOOL_DIR="$CASE/spool"; mkdir -p "$CASE/pki"; printf "CA\n" > "$CASE/pki/ca.pem"; chmod 644 "$CASE/pki/ca.pem"
    printf "KEY\n" > "$CASE/pki/client.key"; chmod 600 "$CASE/pki/client.key"
    RETICORA_TLS_CA_FILE="$CASE/pki/ca.pem"; RETICORA_TLS_CLIENT_CERT_FILE="$INSTALL_DIR/client.pem"
    RETICORA_TLS_CLIENT_KEY_FILE="$CASE/pki/ca.pem"; docker_file_mounts "$CASE/var-lib"
    RETICORA_TLS_CA_FILE="$CASE/var-lib/ca.pem"; RETICORA_TLS_CLIENT_CERT_FILE="relative/client.pem"
    RETICORA_TLS_CLIENT_KEY_FILE="$CASE/pki/client.key"; docker_file_mounts "$CASE/var-lib"'
if [ "$(grep -c -- '^      - ' "$TMP/docker-mounts.log")" = 2 ] \
    && grep -qxF "      - $TMP/docker-mounts/pki/ca.pem:$TMP/docker-mounts/pki/ca.pem:ro" "$TMP/docker-mounts.log" \
    && grep -qxF "      - $TMP/docker-mounts/pki/client.key:$TMP/docker-mounts/pki/client.key:ro" "$TMP/docker-mounts.log" \
    && grep -q 'relative/client.pem is not an absolute path: the collector container cannot read it' "$TMP/docker-mounts.log" \
    && grep -q "make $TMP/docker-mounts/pki/client.key readable for it" "$TMP/docker-mounts.log" \
    && ! grep -q "make $TMP/docker-mounts/pki/ca.pem readable" "$TMP/docker-mounts.log"; then
    ok "Docker mode: files below a mounted directory or named twice are mounted once; a relative path or an unreadable file is a warning"
else
    fail "Docker mode: files below a mounted directory or named twice are mounted once; a relative path or an unreadable file is a warning" \
        "$(log docker-mounts)"
fi

# Another existing spool directory of the operator is not handed over.
mkdir -p "$TMP/docker-spool/opt" "$TMP/docker-spool/srv-spool" "$TMP/docker-spool/src/collector"
: > "$TMP/docker-spool/src/collector/Dockerfile"
printf 'RETICORA_SPOOL_DIR=%s\n' "$TMP/docker-spool/srv-spool" > "$TMP/docker-spool/opt/collector.env"
before="$(stat -c %u:%g:%a "$TMP/docker-spool/srv-spool")"
installer docker-spool 'MODE=docker; collect_config && write_env || exit 1
    BUILD_FROM_SOURCE=1; SOURCE_DIR="$CASE/src"; install_docker_setup'
if [ "$(stat -c %u:%g:%a "$TMP/docker-spool/srv-spool")" = "$before" ] \
    && grep -q "make $TMP/docker-spool/srv-spool writable for it" "$TMP/docker-spool.log"; then
    ok "Docker mode: an existing spool directory of the operator is not handed over; the installer says what to do"
else
    fail "Docker mode: an existing spool directory of the operator is not handed over; the installer says what to do" "$(log docker-spool)"
fi

# ─── The NATS endpoint check ─────────────────────────────────────────────────
installer endpoints '
    RETICORA_CREDENTIALS_PATH="$CASE/none.json"
    for url in https://cloud.example.test:8443/ui http://203.0.113.10:3000 "http://[2001:db8::1]:3000" ""; do
        RETICORA_SERVER_URL="$url"; printf "[%s] -> [%s]\n" "$url" "$(nats_endpoint)"
    done
    mkdir -p "$INSTALL_DIR"; printf "RETICORA_NATS_URL=tls://nats.example.test:14222\n" > "$ENV_FILE"
    RETICORA_SERVER_URL=https://cloud.example.test; printf "[nats-url] -> [%s]\n" "$(nats_endpoint)"
    rm -f "$ENV_FILE"; RETICORA_SERVER_URL=""
    printf "{\n  \"metadata\": {\n    \"server_url\": \"https://enrolled.example.test\"\n  }\n}\n" > "$CASE/c.json"
    RETICORA_CREDENTIALS_PATH="$CASE/c.json"; printf "[enrolled] -> [%s]\n" "$(nats_endpoint)"'
expected='[https://cloud.example.test:8443/ui] -> [cloud.example.test 4222]
[http://203.0.113.10:3000] -> [203.0.113.10 4222]
[http://[2001:db8::1]:3000] -> [2001:db8::1 4222]
[] -> []
[nats-url] -> [nats.example.test 14222]
[enrolled] -> [enrolled.example.test 4222]'
if [ "$(grep '^\[' "$TMP/endpoints.log")" = "$expected" ]; then
    ok "NATS endpoint: the host of the cloud URL (IPv6 too) on 4222, RETICORA_NATS_URL, or the URL of the enrollment"
else
    fail "NATS endpoint: the host of the cloud URL (IPv6 too) on 4222, RETICORA_NATS_URL, or the URL of the enrollment" "$(log endpoints)"
fi

# A real TCP probe: a closed port is a clear warning, a listening one passes.
closed="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
open_port_file="$TMP/open-port"
python3 -c '
import socket, sys
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("127.0.0.1", 0)); s.listen(8)
open(sys.argv[1], "w").write(str(s.getsockname()[1]))
while True:
    c, _ = s.accept(); c.close()
' "$open_port_file" &
LISTENER=$!
for _ in $(seq 50); do [ -s "$open_port_file" ] && break; sleep 0.1; done
open_port="$(cat "$open_port_file" 2>/dev/null)"
installer probe '
    mkdir -p "$INSTALL_DIR"
    printf "RETICORA_NATS_URL=tls://127.0.0.1:%s\n" "$CLOSED" > "$ENV_FILE"; verify_nats_endpoint
    printf "RETICORA_NATS_URL=tls://127.0.0.1:%s\n" "$OPEN" > "$ENV_FILE"; verify_nats_endpoint
    RETICORA_SERVER_URL=""; RETICORA_CREDENTIALS_PATH="$CASE/none.json"; rm -f "$ENV_FILE"; verify_nats_endpoint' \
    CLOSED="$closed" OPEN="$open_port"
if [ -n "$open_port" ] && grep -q "Collector NATS endpoint 127.0.0.1:$closed is NOT reachable from this VM" "$TMP/probe.log" \
    && grep -q "allow $closed/TCP outbound here and inbound on the cloud host" "$TMP/probe.log" \
    && grep -q "Collector NATS endpoint 127.0.0.1:$open_port is reachable (TCP)" "$TMP/probe.log" \
    && grep -q 'No cloud URL known yet' "$TMP/probe.log"; then
    ok "verify: a TCP probe of the NATS endpoint; unreachable is a warning with what to open"
else
    fail "verify: a TCP probe of the NATS endpoint; unreachable is a warning with what to open" "open=$open_port closed=$closed
$(log probe)"
fi

echo
echo "install-vm-enroll: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
