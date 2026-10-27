#!/usr/bin/env bash
#
# collector-selfupdate.test.sh — COL-06 (WP-546, open evidence of WP-148):
# the signed self-update of the running collector, end to end with two real
# builds of collector/cmd/collector (collectorcmd.Version v0.0.1 and v0.0.2).
#
# A local HTTP server (python3) serves the channel manifests below
# RETICORA_UPDATE_URL, the artifacts and their bundles, and answers the
# collector's heartbeats (status switchable per case). A cosign stub on the
# collector's PATH accepts only the fixture bundle of the v0.0.2 artifact,
# and only with the pinned release identity of edgecore/update
# (ReleaseIdentityRegexp) and the GitHub Actions issuer; the real cosign
# check runs in the ci.yml job cosign-verify. This script acts as the
# service manager (Restart=always): it starts the installed binary again
# after it exits.
#
#   a) v0.0.1 installs the signed v0.0.2 after its first heartbeat, exits;
#      the restarted binary is v0.0.2, reports it (start log, "version") and
#      confirms the update with its first heartbeat;
#   b) wrong signature (bundle of another artifact) and no signature: no
#      swap, log "collector update refused", the collector keeps running;
#   c) beta release while subscribed to stable: no swap, cosign not run;
#   d) the new version does not start (its heartbeats are refused, every
#      start ends unconfirmed): after three starts the fourth rolls back to
#      v0.0.1, which runs and does not install v0.0.2 again.
#
# Tools: go, python3, sha256sum, cmp. Runs in the job installer-script-tests
# of install-smoke.yml.
#
# Usage: ./tests/collector-selfupdate.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
TMP_ROOT="$(mktemp -d)"
COLLECTOR_PID=""
SERVER_PID=""
cleanup() {
    [ -z "$COLLECTOR_PID" ] || kill "$COLLECTOR_PID" 2>/dev/null
    [ -z "$SERVER_PID" ] || kill "$SERVER_PID" 2>/dev/null
    wait 2>/dev/null
    rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | tail -n 12 | sed 's/^/     /'; return 0; }
check() { # check <name> <command...>: ok when the command succeeds
    local name="$1"; shift
    if "$@"; then ok "$name"; else fail "$name" "${DETAIL:-}"; fi
    DETAIL=""
}
die() { printf 'FAIL %s\n' "$1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | tail -n 20 | sed 's/^/     /'; exit 1; }

sha() { sha256sum "$1" | cut -d' ' -f1; }

# ─── Two builds of the collector ─────────────────────────────────────────────
BIN="$TMP_ROOT/bin"
mkdir -p "$BIN"
GOOS_HOST="$(cd collector && go env GOOS)" || die "go env GOOS"
GOARCH_HOST="$(cd collector && go env GOARCH)" || die "go env GOARCH"
for v in v0.0.1 v0.0.2; do
    out="$( (cd collector && CGO_ENABLED=0 go build -trimpath \
        -ldflags "-X github.com/DataHub-Chiemgau/Reticora-CMDB/collector/collectorcmd.Version=$v" \
        -o "$BIN/reticora-collector-$v" ./cmd/collector) 2>&1)" || die "build collector $v" "$out"
    got="$("$BIN/reticora-collector-$v" version 2>&1)"
    [ "$got" = "reticora-collector $v" ] || die "build collector $v reports its version" "$got"
done
V1_SHA="$(sha "$BIN/reticora-collector-v0.0.1")"
V2_SHA="$(sha "$BIN/reticora-collector-v0.0.2")"
[ "$V1_SHA" != "$V2_SHA" ] || die "the two builds differ"
ok "two builds: reticora-collector v0.0.1 and v0.0.2"

# ─── Release server, fixture bundles, cosign stub ────────────────────────────
WWW="$TMP_ROOT/www"
mkdir -p "$WWW/artifacts" "$WWW/updates/stable" "$WWW/updates/beta"
cp "$BIN/reticora-collector-v0.0.2" "$WWW/artifacts/reticora-collector-v0.0.2"
# shellcheck disable=SC2016 # the backquotes delimit the Go raw string
IDENTITY="$(sed -n 's/^[[:space:]]*ReleaseIdentityRegexp[[:space:]]*=[[:space:]]*`\(.*\)`$/\1/p' edgecore/update/update.go)"
[ -n "$IDENTITY" ] || die "ReleaseIdentityRegexp found in edgecore/update/update.go"
ISSUER="https://token.actions.githubusercontent.com"
# The fixture bundle of the v0.0.2 artifact and the bundle of another
# artifact (v0.0.1): the stub knows only the first.
printf '{"fixture":"reticora-collector v0.0.2","sha256":"%s"}\n' "$V2_SHA" > "$WWW/artifacts/reticora-collector-v0.0.2.sigstore.json"
printf '{"fixture":"reticora-collector v0.0.1","sha256":"%s"}\n' "$V1_SHA" > "$WWW/artifacts/reticora-collector-v0.0.1.sigstore.json"
FIXTURE_BUNDLE="$TMP_ROOT/fixture.sigstore.json"
cp "$WWW/artifacts/reticora-collector-v0.0.2.sigstore.json" "$FIXTURE_BUNDLE"

STUBS="$TMP_ROOT/stubs"
COSIGN_LOG="$TMP_ROOT/cosign.log"
mkdir -p "$STUBS"
: > "$COSIGN_LOG"
cat > "$STUBS/cosign" <<STUB
#!/usr/bin/env bash
# cosign stub: verify-blob accepts only the fixture bundle, for the artifact
# it was made for, with the pinned release identity and issuer.
printf '%s\n' "\$*" >> '$COSIGN_LOG'
[ "\${1:-}" = verify-blob ] || { echo "cosign stub: unsupported command \${1:-}" >&2; exit 2; }
shift
bundle="" identity="" issuer="" artifact=""
while [ "\$#" -gt 0 ]; do
    case "\$1" in
        --bundle) bundle="\$2"; shift ;;
        --certificate-identity-regexp) identity="\$2"; shift ;;
        --certificate-oidc-issuer) issuer="\$2"; shift ;;
        -*) echo "cosign stub: unknown flag \$1" >&2; exit 2 ;;
        *) artifact="\$1" ;;
    esac
    shift
done
if ! cmp -s "\$bundle" '$FIXTURE_BUNDLE'; then
    echo "Error: bundle does not verify: invalid signature" >&2; exit 1
fi
if [ "\$identity" != '$IDENTITY' ] || [ "\$issuer" != '$ISSUER' ]; then
    echo "Error: none of the expected identities matched what was in the certificate" >&2; exit 1
fi
if [ "\$(sha256sum "\$artifact" | cut -d' ' -f1)" != '$V2_SHA' ]; then
    echo "Error: invalid signature when validating ASN.1 encoded signature" >&2; exit 1
fi
echo "Verified OK"
STUB
chmod +x "$STUBS/cosign"

cat > "$TMP_ROOT/server.py" <<'PY'
# Release server and heartbeat endpoint of the self-update test.
import http.server, os, sys
root, state = sys.argv[1], sys.argv[2]

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _send(self, code, body=b"{}", ctype="application/json"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = os.path.normpath(os.path.join(root, self.path.split("?")[0].lstrip("/")))
        if not path.startswith(root + os.sep) or not os.path.isfile(path):
            return self._send(404, b"not found", "text/plain")
        with open(path, "rb") as f:
            self._send(200, f.read(), "application/octet-stream")

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length") or 0))
        if self.path.endswith("/heartbeat"):
            with open(os.path.join(state, "heartbeat-status")) as f:
                code = int(f.read().strip())
            with open(os.path.join(state, "heartbeats.log"), "a") as f:
                f.write("%d\n" % code)
            return self._send(code)
        self._send(200)

srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(os.path.join(state, "port.tmp"), "w") as f:
    f.write(str(srv.server_address[1]))
os.rename(os.path.join(state, "port.tmp"), os.path.join(state, "port"))
srv.serve_forever()
PY
STATE="$TMP_ROOT/state"
mkdir -p "$STATE"
echo 200 > "$STATE/heartbeat-status"
python3 -I "$TMP_ROOT/server.py" "$WWW" "$STATE" &
SERVER_PID=$!
for _ in $(seq 1 100); do [ -s "$STATE/port" ] && break; sleep 0.1; done
[ -s "$STATE/port" ] || die "release server started"
BASE="http://127.0.0.1:$(cat "$STATE/port")"

MANIFEST="reticora-collector-$GOOS_HOST-$GOARCH_HOST.release.json"
# write_manifest <channel dir> <release channel> <bundle file or "">
write_manifest() {
    local bundle_field=""
    [ -z "$3" ] || bundle_field=",\"signatureBundleUrl\":\"$BASE/artifacts/$3\""
    rm -f "$WWW"/updates/*/"$MANIFEST"
    printf '{"version":"v0.0.2","channel":"%s","downloadUrl":"%s/artifacts/reticora-collector-v0.0.2","checksums":{"%s/%s":"%s"}%s}\n' \
        "$2" "$BASE" "$GOOS_HOST" "$GOARCH_HOST" "$V2_SHA" "$bundle_field" > "$WWW/updates/$1/$MANIFEST"
}

# ─── The collector as the service manager runs it ───────────────────────────
INSTALL=""
install_v1() { # fresh installation directory with v0.0.1
    INSTALL="$TMP_ROOT/install-$1"
    mkdir -p "$INSTALL/spool"
    cp "$BIN/reticora-collector-v0.0.1" "$INSTALL/reticora-collector"
}
start_collector() { # start_collector <log>
    env -i HOME="$TMP_ROOT" PATH="$STUBS:/usr/local/bin:/usr/bin:/bin" \
        RETICORA_ENVIRONMENT=test \
        RETICORA_SERVER_URL="$BASE" \
        RETICORA_ORGANIZATION_ID=00000000-0000-4000-8000-000000000546 \
        RETICORA_COLLECTOR_ID=00000000-0000-4000-8000-000000000547 \
        RETICORA_CREDENTIALS_PATH="$INSTALL/credentials.json" \
        RETICORA_SPOOL_DIR="$INSTALL/spool" \
        RETICORA_SCAN_SUBNETS=127.0.0.1/32 RETICORA_DISCOVERY_PROTOCOLS=snmp \
        RETICORA_HEARTBEAT_INTERVAL=1s RETICORA_METRICS_INTERVAL=0s \
        RETICORA_AGENT_RELAY_ADDR=127.0.0.1:0 \
        RETICORA_UPDATE_URL="$BASE/updates" RETICORA_UPDATE_CHANNEL=stable RETICORA_UPDATE_INTERVAL=1s \
        "$INSTALL/reticora-collector" > "$1" 2>&1 &
    COLLECTOR_PID=$!
}
wait_log() { # wait_log <log> <pattern> [seconds]: the pattern appears
    local _
    for _ in $(seq 1 $(( ${3:-60} * 10 ))); do
        grep -q -- "$2" "$1" 2>/dev/null && return 0
        sleep 0.1
    done
    return 1
}
wait_exit() { # wait_exit [seconds]: the collector exits; sets EXIT_CODE
    local _
    for _ in $(seq 1 $(( ${1:-60} * 10 ))); do
        if ! kill -0 "$COLLECTOR_PID" 2>/dev/null; then
            wait "$COLLECTOR_PID"; EXIT_CODE=$?; COLLECTOR_PID=""; return 0
        fi
        sleep 0.1
    done
    return 1
}
stop_collector() {
    [ -n "$COLLECTOR_PID" ] || return 0
    kill "$COLLECTOR_PID" 2>/dev/null
    wait "$COLLECTOR_PID" 2>/dev/null
    COLLECTOR_PID=""
}
running() { [ -n "$COLLECTOR_PID" ] && kill -0 "$COLLECTOR_PID" 2>/dev/null; }
installed_is() { [ "$(sha "$INSTALL/reticora-collector")" = "$1" ]; }
file_is() { [ -f "$1" ] && [ "$(sha "$1")" = "$2" ]; }
not_logged() { ! grep -q -- "$2" "$1"; }
swapped_to_v2() { installed_is "$V2_SHA" && file_is "$INSTALL/reticora-collector.old" "$V1_SHA"; }
rolled_back_and_exited() {
    [ "$EXIT_CODE" -ne 0 ] && grep -q '"msg":"collector update rolled back after failed starts","version":"v0.0.2"' "$LOG"
}
restored_v1() {
    installed_is "$V1_SHA" && file_is "$INSTALL/reticora-collector.failed" "$V2_SHA" \
        && [ "$(cat "$INSTALL/.update-rolledback")" = v0.0.2 ] && [ ! -e "$INSTALL/.update-pending" ]
}
v1_runs() { grep -q '"msg":"starting collector".*"version":"v0.0.1"' "$LOG" && running; }
untouched() { # the v0.0.1 installation without traces of an update
    installed_is "$V1_SHA" && [ ! -e "$INSTALL/reticora-collector.old" ] \
        && [ ! -e "$INSTALL/reticora-collector.update" ] && [ ! -e "$INSTALL/.update-pending" ]
}

# ─── a) v0.0.1 updates itself to v0.0.2 ──────────────────────────────────────
install_v1 a
write_manifest stable stable reticora-collector-v0.0.2.sigstore.json
: > "$COSIGN_LOG"
LOG="$TMP_ROOT/a-1.log"
start_collector "$LOG"
if wait_exit 90; then
    check "a: v0.0.1 installs v0.0.2 and exits for the restart (exit $EXIT_CODE)" \
        grep -q '"msg":"collector restarting into the update","version":"v0.0.2"' "$LOG"
    DETAIL="$(cat "$LOG")"
    check "a: the update follows the first accepted heartbeat" \
        grep -q '"msg":"installing collector update","from":"v0.0.1","to":"v0.0.2","channel":"stable"' "$LOG"
else
    fail "a: v0.0.1 installs v0.0.2 and exits for the restart" "$(cat "$LOG")"
    stop_collector
fi
DETAIL="$(cat "$COSIGN_LOG")"
check "a: cosign verified with the pinned release identity and issuer" \
    grep -qF -- "--certificate-identity-regexp $IDENTITY --certificate-oidc-issuer $ISSUER" "$COSIGN_LOG"
check "a: the installed binary is v0.0.2, v0.0.1 kept as .old" \
    swapped_to_v2
got="$("$INSTALL/reticora-collector" version 2>&1)"
DETAIL="$got"
check "a: the installed binary reports v0.0.2" test "$got" = "reticora-collector v0.0.2"
LOG="$TMP_ROOT/a-2.log"
start_collector "$LOG"
if wait_log "$LOG" '"msg":"collector update confirmed","version":"v0.0.2"' 60; then
    ok "a: the restarted collector confirms the update with its first heartbeat"
else
    fail "a: the restarted collector confirms the update with its first heartbeat" "$(cat "$LOG")"
fi
DETAIL="$(cat "$LOG")"
check "a: the running collector reports version v0.0.2" \
    grep -q '"msg":"starting collector".*"version":"v0.0.2"' "$LOG"
check "a: no update pending after the confirmation" test ! -e "$INSTALL/.update-pending"
stop_collector

# ─── b) wrong signature and no signature: no swap ────────────────────────────
for variant in wrong no; do
    install_v1 "b-$variant"
    if [ "$variant" = wrong ]; then
        write_manifest stable stable reticora-collector-v0.0.1.sigstore.json
    else
        write_manifest stable stable ""
    fi
    : > "$COSIGN_LOG"
    LOG="$TMP_ROOT/b-$variant.log"
    start_collector "$LOG"
    if wait_log "$LOG" '"msg":"collector update refused","version":"v0.0.2"' 60; then
        ok "b ($variant signature): log 'collector update refused'"
    else
        fail "b ($variant signature): log 'collector update refused'" "$(cat "$LOG")"
    fi
    DETAIL="$(cat "$LOG")"
    if [ "$variant" = wrong ]; then
        check "b (wrong signature): refused by the signature check" \
            grep -q 'signature verification failed, update refused' "$LOG"
        DETAIL="$(cat "$COSIGN_LOG")"
        check "b (wrong signature): cosign got the foreign bundle" grep -q 'verify-blob --bundle' "$COSIGN_LOG"
    else
        check "b (no signature): refused as unsigned" grep -q 'unsigned updates are refused' "$LOG"
    fi
    sleep 1.5
    DETAIL="$(cat "$LOG")"
    check "b ($variant signature): the collector keeps running" running
    DETAIL="$(ls -la "$INSTALL")"
    check "b ($variant signature): no swap, v0.0.1 still installed" untouched
    got="$("$INSTALL/reticora-collector" version 2>&1)"
    DETAIL="$got"
    check "b ($variant signature): the installed binary reports v0.0.1" test "$got" = "reticora-collector v0.0.1"
    stop_collector
done

# ─── c) beta release while subscribed to stable: no swap ─────────────────────
install_v1 c
write_manifest stable beta reticora-collector-v0.0.2.sigstore.json
: > "$COSIGN_LOG"
LOG="$TMP_ROOT/c.log"
start_collector "$LOG"
if wait_log "$LOG" '"msg":"collector update check failed".*release is not on the subscribed channel' 60; then
    ok "c: the beta release is refused on the channel stable"
else
    fail "c: the beta release is refused on the channel stable" "$(cat "$LOG")"
fi
sleep 1.5
DETAIL="$(cat "$LOG")"
check "c: nothing installed" not_logged "$LOG" "installing collector update"
DETAIL="$(cat "$COSIGN_LOG")"
check "c: cosign not run" test ! -s "$COSIGN_LOG"
DETAIL="$(ls -la "$INSTALL")"
check "c: no swap, v0.0.1 still installed" untouched
check "c: the collector keeps running" running
stop_collector

# ─── d) the new version does not start: rollback to v0.0.1 ──────────────────
install_v1 d
write_manifest stable stable reticora-collector-v0.0.2.sigstore.json
echo 200 > "$STATE/heartbeat-status"
LOG="$TMP_ROOT/d-0.log"
start_collector "$LOG"
if wait_exit 90 && installed_is "$V2_SHA"; then
    ok "d: v0.0.2 installed (pending confirmation)"
else
    fail "d: v0.0.2 installed (pending confirmation)" "$(cat "$LOG")"
    stop_collector
fi
# Every heartbeat of the new binary is refused: it never works; each start
# ends unconfirmed and the service manager starts it again.
echo 503 > "$STATE/heartbeat-status"
for attempt in 1 2 3; do
    LOG="$TMP_ROOT/d-$attempt.log"
    start_collector "$LOG"
    if wait_log "$LOG" '"msg":"heartbeat completed","status":503' 60; then
        DETAIL="$(cat "$LOG")"
        check "d: start $attempt of v0.0.2 stays unconfirmed" \
            grep -q '"msg":"collector update awaiting confirmation","version":"v0.0.2"' "$LOG"
    else
        fail "d: start $attempt of v0.0.2 sends a heartbeat" "$(cat "$LOG")"
    fi
    stop_collector
    DETAIL="$(cat "$INSTALL/.update-pending" 2>&1)"
    check "d: start $attempt counted" grep -q "\"attempts\":$attempt" "$INSTALL/.update-pending"
done
LOG="$TMP_ROOT/d-4.log"
start_collector "$LOG"
if wait_exit 60; then
    DETAIL="$(cat "$LOG")"
    check "d: the fourth start rolls back and exits non-zero (exit $EXIT_CODE)" \
        rolled_back_and_exited
else
    fail "d: the fourth start rolls back and exits" "$(cat "$LOG")"
    stop_collector
fi
DETAIL="$(ls -la "$INSTALL")"
check "d: v0.0.1 restored, v0.0.2 kept as .failed, rollback remembered" \
    restored_v1
got="$("$INSTALL/reticora-collector" version 2>&1)"
DETAIL="$got"
check "d: the restored binary reports v0.0.1" test "$got" = "reticora-collector v0.0.1"
echo 200 > "$STATE/heartbeat-status"
LOG="$TMP_ROOT/d-5.log"
start_collector "$LOG"
if wait_log "$LOG" '"msg":"heartbeat completed","status":200' 60; then
    sleep 3 # three update intervals after the confirmed start
    DETAIL="$(cat "$LOG")"
    check "d: the restored v0.0.1 runs" v1_runs
    check "d: the rolled-back v0.0.2 is not installed again" not_logged "$LOG" "installing collector update"
    check "d: v0.0.1 stays installed" installed_is "$V1_SHA"
else
    fail "d: the restored v0.0.1 sends a heartbeat" "$(cat "$LOG")"
fi
stop_collector

echo
echo "collector-selfupdate: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
