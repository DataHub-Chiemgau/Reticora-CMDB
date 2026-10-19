// Phase-1 acceptance audit, group T2-auth-authz: regression tests of the
// agent transport (both channels TLS only, credential on every report).
//
//   - WP-051 (AGT-03), gap (high) "Agent-side transport unit tests (probe
//     only today)": sendDirect with http:// is refused before any network
//     call; sendViaRelay to a plaintext listener fails the TLS handshake
//     without transmitting the token; without a token nothing is sent;
//     HTTPS with the enrolled CA carries the Bearer credential and a body in
//     the backend contract (agent.TelemetryPayload, POST
//     /api/v1/agents/telemetry); an unknown server CA is refused. The
//     redirect case (https -> http) is a defect: see
//     t2_redirect_auditdefect_test.go.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestT2Agent' ./cmd/agent/
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
)

const t2AgentToken = "t2-secret-agent-token"

func caPEM(srv *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

// foreignCAPEM is a self-signed CA that signed none of the test servers.
func foreignCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "t2 foreign CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestT2AgentDirectChannelIsTLSOnly: the direct channel never talks plain
// HTTP and verifies the server against the enrolled CA.
func TestT2AgentDirectChannelIsTLSOnly(t *testing.T) {
	t.Run("http server URL refused without a network call", func(t *testing.T) {
		var hits atomic.Int32
		plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
		defer plain.Close()
		cfg := agentConfig{ServerURL: plain.URL, AgentID: "t2-agent", Hostname: "h", Token: t2AgentToken}
		if err := sendDirect(context.Background(), cfg, buildTelemetry(cfg)); err == nil {
			t.Error("sendDirect to an http:// server URL returned no error")
		}
		if n := hits.Load(); n != 0 {
			t.Errorf("plain HTTP server received %d request(s), want 0", n)
		}
	})

	t.Run("https with the enrolled CA carries the credential and the contract body", func(t *testing.T) {
		var (
			mu      sync.Mutex
			auth    string
			path    string
			payload agent.TelemetryPayload
			decErr  error
		)
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			auth, path = r.Header.Get("Authorization"), r.URL.Path
			decErr = json.NewDecoder(r.Body).Decode(&payload)
			w.WriteHeader(http.StatusAccepted)
		}))
		defer srv.Close()
		cfg := agentConfig{ServerURL: srv.URL, AgentID: "t2-agent", Hostname: "t2-host", Token: t2AgentToken, CAPEM: caPEM(srv)}
		if err := sendDirect(context.Background(), cfg, buildTelemetry(cfg)); err != nil {
			t.Fatalf("sendDirect over verified TLS: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if auth != "Bearer "+t2AgentToken {
			t.Errorf("Authorization %q, want the agent Bearer credential", auth)
		}
		if path != "/api/v1/agents/telemetry" {
			t.Errorf("path %q, want /api/v1/agents/telemetry", path)
		}
		if decErr != nil || payload.AgentID != "t2-agent" || payload.Hostname != "t2-host" || payload.CollectedAt.IsZero() {
			t.Errorf("body does not decode as agent.TelemetryPayload: %+v (err %v)", payload, decErr)
		}
	})

	t.Run("unknown server CA refused", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
		defer srv.Close()
		// httptest servers share one certificate: the foreign CA is a
		// freshly generated self-signed one.
		cfg := agentConfig{ServerURL: srv.URL, AgentID: "t2-agent", Hostname: "h", Token: t2AgentToken, CAPEM: foreignCAPEM(t)}
		if err := sendDirect(context.Background(), cfg, buildTelemetry(cfg)); err == nil {
			t.Error("sendDirect to a server with an unknown CA returned no error")
		}
		if n := hits.Load(); n != 0 {
			t.Errorf("server with an unknown CA received %d request(s), want 0", n)
		}
	})

	t.Run("no token: nothing is sent", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
		defer srv.Close()
		collectAndSend(context.Background(), agentConfig{ServerURL: srv.URL, AgentID: "t2-agent", Hostname: "h", CAPEM: caPEM(srv)})
		if n := hits.Load(); n != 0 {
			t.Errorf("agent without credential sent %d request(s), want 0", n)
		}
	})
}

// TestT2AgentRelayChannelIsTLSOnly: the relay channel refuses a plaintext
// peer; the token never appears on the wire.
func TestT2AgentRelayChannelIsTLSOnly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	received := make(chan []byte, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			received <- nil
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		var buf bytes.Buffer
		chunk := make([]byte, 4096)
		n, _ := c.Read(chunk)
		buf.Write(chunk[:n])
		// Answer like a plaintext relay would.
		_, _ = c.Write([]byte("{\"status\":\"ok\"}\n"))
		for {
			n, rerr := c.Read(chunk)
			buf.Write(chunk[:n])
			if rerr != nil {
				break
			}
		}
		received <- buf.Bytes()
	}()
	cfg := agentConfig{CollectorRelay: ln.Addr().String(), AgentID: "t2-agent", Hostname: "h", Token: t2AgentToken}
	if err := sendViaRelay(context.Background(), cfg, buildTelemetry(cfg)); err == nil {
		t.Error("sendViaRelay to a plaintext listener returned no error")
	}
	select {
	case wire := <-received:
		if bytes.Contains(wire, []byte(t2AgentToken)) {
			t.Errorf("the plaintext listener received the agent token")
		}
		if len(wire) == 0 || wire[0] != 0x16 {
			t.Errorf("first byte on the wire %x, want a TLS handshake record (0x16)", wire)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("plaintext listener saw no connection")
	}
}
