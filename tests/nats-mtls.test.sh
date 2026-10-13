#!/usr/bin/env bash
#
# nats-mtls.test.sh — the collector NATS endpoint accepts only TLS clients
# with a certificate of the collector CA (WP-146: COL-08, PRI-07). Starts
# nats-server with deploy/docker-compose/nats-edge.conf (TLS handshake first,
# client verification) and probes it with openssl: no client certificate and
# a certificate of a foreign CA are refused, a collector certificate is
# accepted. The authorization callout is covered by
# backend/internal/discovery/natsedge_integration_test.go; here the generated
# pki/edge.conf carries the same TLS block without the callout.
#
# Needs openssl and either nats-server (NATS_SERVER_BIN or PATH) or Docker.
# Usage: ./tests/nats-mtls.test.sh
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
ROOT="$(pwd)"
TMP="$(mktemp -d)"
PORT="${NATS_MTLS_TEST_PORT:-14228}"
SERVER_PID=""
CONTAINER=""
cleanup() {
    [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
    [ -n "$CONTAINER" ] && docker rm -f "$CONTAINER" >/dev/null 2>&1
    rm -rf "$TMP"
}
trap cleanup EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; }

mkdir -p "$TMP/pki"
cd "$TMP" || exit 1

# A CA like the collector CA, a foreign CA, the endpoint's server
# certificate and two client certificates.
gen_ca() { # name
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 \
        -subj "/CN=$1" -keyout "$1.key" -out "$1.pem" >/dev/null 2>&1
}
gen_cert() { # name ca usage [san]
    openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=$1/O=wp146" \
        -keyout "$1.key" -out "$1.csr" >/dev/null 2>&1
    {
        echo "extendedKeyUsage=$3"
        [ -n "${4:-}" ] && echo "subjectAltName=$4"
    } > "$1.ext"
    openssl x509 -req -in "$1.csr" -CA "$2.pem" -CAkey "$2.key" -CAcreateserial -days 1 \
        -extfile "$1.ext" -out "$1.pem" >/dev/null 2>&1
}
gen_ca collector-ca
gen_ca foreign-ca
gen_cert server collector-ca serverAuth "DNS:localhost,IP:127.0.0.1"
gen_cert collector collector-ca clientAuth
gen_cert intruder foreign-ca clientAuth

cp collector-ca.pem pki/ca.pem
cp server.pem pki/server.pem
cp server.key pki/server-key.pem
chmod 0644 pki/*
cat > pki/edge.conf <<'CONF'
tls {
  cert_file: "pki/server.pem"
  key_file: "pki/server-key.pem"
  ca_file: "pki/ca.pem"
  verify: true
  handshake_first: true
  timeout: 5
}
accounts { COLLECTORS {} }
CONF
cp "$ROOT/deploy/docker-compose/nats-edge.conf" nats-edge.conf

BIN="${NATS_SERVER_BIN:-$(command -v nats-server || true)}"
if [ -n "$BIN" ]; then
    RETICORA_NATS_EDGE_LEAF_URL=nats://127.0.0.1:1 "$BIN" -c nats-edge.conf -p "$PORT" >server.log 2>&1 &
    SERVER_PID=$!
elif command -v docker >/dev/null 2>&1; then
    CONTAINER="reticora-nats-mtls-test-$$"
    docker run -d --name "$CONTAINER" -p "127.0.0.1:$PORT:4222" -w /etc/nats \
        -e RETICORA_NATS_EDGE_LEAF_URL=nats://127.0.0.1:1 \
        -v "$TMP/nats-edge.conf:/etc/nats/nats-edge.conf:ro" -v "$TMP/pki:/etc/nats/pki:ro" \
        nats:2.10-alpine -c nats-edge.conf >/dev/null || { echo "docker run failed"; exit 1; }
else
    echo "neither nats-server nor docker available"
    exit 1
fi

# Wait until the endpoint accepts TCP connections.
for _ in $(seq 1 50); do
    if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then break; fi
    sleep 0.2
done

probe() { # [cert key]
    local args=(-connect "127.0.0.1:$PORT" -servername localhost -CAfile collector-ca.pem -verify_return_error -tls1_2)
    [ $# -eq 2 ] && args+=(-cert "$1" -key "$2")
    timeout 10 openssl s_client "${args[@]}" </dev/null >probe.log 2>&1
}

if probe; then
    fail "a client without certificate completed the TLS handshake"
else
    ok "no client certificate: refused"
fi
if probe intruder.pem intruder.key; then
    fail "a certificate of a foreign CA completed the TLS handshake"
else
    ok "certificate of a foreign CA: refused"
fi
if probe collector.pem collector.key && grep -q "Verify return code: 0 (ok)" probe.log; then
    ok "collector certificate: TLS established, server certificate trusted via the collector CA"
else
    fail "collector certificate refused: $(tail -5 probe.log)"
    [ -f server.log ] && tail -20 server.log
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
