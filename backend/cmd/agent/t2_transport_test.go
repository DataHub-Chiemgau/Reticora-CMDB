// Phase-1 acceptance audit, group T2-auth-authz: regression tests of the
// agent transport (both channels TLS only, credential on every report).
//
//   - WP-051 (AGT-03), gap (high) "Agent-side transport unit tests (probe
//     only today)": sendDirect with http:// is refused before any network
//     call; sendViaRelay to a plaintext listener fails the TLS handshake
//     without transmitting the token; without a token nothing is sent;
//     HTTPS with the enrolled CA carries the Bearer credential and a body in
//     the backend contract (agent.TelemetryPayload, POST
//     /api/v1/agents/telemetry); an unknown server CA is refused.
//   - WP-365 (AGT-03), finding F-064 "Agent follows https->http redirects
//     and sends its Bearer credential in cleartext": on both direct
//     channels (TLS and mTLS) a redirect to any scheme but https is refused,
//     at most 3 redirects are followed, the Authorization header is dropped
//     once a redirect leaves the host; the failed report is spooled and
//     delivered with the next successful send.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestT2Agent|TestAuditT2Agent' ./cmd/agent/
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
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

// TestAuditT2AgentRefusesRedirectToPlainHTTP: a redirect from the verified
// HTTPS server to http:// must end the send with an error, and the plain
// HTTP server must see neither the credential nor the telemetry (F-064).
func TestAuditT2AgentRefusesRedirectToPlainHTTP(t *testing.T) {
	for _, code := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var (
				mu    sync.Mutex
				hits  int
				auth  string
				bytes int64
			)
			plain := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				hits++
				auth, bytes = r.Header.Get("Authorization"), r.ContentLength
			}))
			defer plain.Close()
			tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, plain.URL+"/api/v1/agents/telemetry", code)
			}))
			defer tlsSrv.Close()
			cfg := agentConfig{ServerURL: tlsSrv.URL, AgentID: "t2-agent", Hostname: "h", Token: t2AgentToken, CAPEM: caPEM(tlsSrv)}
			err := sendDirect(context.Background(), cfg, buildTelemetry(cfg))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				t.Errorf("redirect %d https -> http: sendDirect returned no error", code)
			}
			if hits != 0 {
				t.Errorf("redirect %d https -> http: the plain HTTP server received %d request(s) (Authorization %q, body %d bytes); want none (AGT-03: TLS only)",
					code, hits, auth, bytes)
			}
		})
	}
}

// clientCertPEM is a self-signed client certificate for the mTLS channel.
func clientCertPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(365), Subject: pkix.Name{CommonName: "t2-agent"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// recorder is a test server that counts requests and keeps the
// Authorization header of the last one.
type recorder struct {
	mu   sync.Mutex
	hits int
	auth string
}

func (rec *recorder) record(r *http.Request) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.hits++
	rec.auth = r.Header.Get("Authorization")
}

func (rec *recorder) get() (int, string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.hits, rec.auth
}

// TestT2AgentDirectChannelRedirectPolicy covers WP-365 (AGT-03, F-064) on
// both direct channels: https -> http is refused before the plain server is
// contacted, the credential stays on the host it was sent to, a redirect on
// the same host keeps it, and a fourth redirect is refused.
func TestT2AgentDirectChannelRedirectPolicy(t *testing.T) {
	certPEM, keyPEM := clientCertPEM(t)
	channels := []struct {
		name  string
		start func(h http.Handler) *httptest.Server
		cfg   func(srv *httptest.Server) agentConfig
	}{
		{"tls", httptest.NewTLSServer, func(srv *httptest.Server) agentConfig {
			return agentConfig{ServerURL: srv.URL, AgentID: "t2-agent", Hostname: "h", Token: t2AgentToken, CAPEM: caPEM(srv)}
		}},
		{"mtls", func(h http.Handler) *httptest.Server {
			srv := httptest.NewUnstartedServer(h)
			srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
			srv.StartTLS()
			return srv
		}, func(srv *httptest.Server) agentConfig {
			return agentConfig{ServerURL: srv.URL, AgentID: "t2-agent", Hostname: "h", Token: t2AgentToken,
				CAPEM: caPEM(srv), CertPEM: certPEM, KeyPEM: keyPEM}
		}},
	}
	for _, ch := range channels {
		t.Run(ch.name, func(t *testing.T) {
			send := func(srv *httptest.Server) error {
				cfg := ch.cfg(srv)
				return sendDirect(context.Background(), cfg, buildTelemetry(cfg))
			}

			t.Run("https to http refused", func(t *testing.T) {
				var plain recorder
				plainSrv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { plain.record(r) }))
				defer plainSrv.Close()
				srv := ch.start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, plainSrv.URL+r.URL.Path, http.StatusTemporaryRedirect)
				}))
				defer srv.Close()
				err := send(srv)
				if err == nil || !strings.Contains(err.Error(), "https redirects only") {
					t.Errorf("redirect to http: error %v, want the refusal of the redirect", err)
				}
				if n, _ := plain.get(); n != 0 {
					t.Errorf("plain HTTP server received %d request(s), want 0", n)
				}
			})

			t.Run("credential dropped on a host change", func(t *testing.T) {
				var other recorder
				otherSrv := ch.start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					other.record(r)
					w.WriteHeader(http.StatusAccepted)
				}))
				defer otherSrv.Close()
				srv := ch.start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, otherSrv.URL+r.URL.Path, http.StatusTemporaryRedirect)
				}))
				defer srv.Close()
				if err := send(srv); err != nil {
					t.Fatalf("https redirect to another host: %v", err)
				}
				if n, auth := other.get(); n != 1 || auth != "" {
					t.Errorf("other host: %d request(s), Authorization %q; want 1 request without credential", n, auth)
				}
			})

			t.Run("credential kept on the same host", func(t *testing.T) {
				var final recorder
				srv := ch.start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/moved" {
						http.Redirect(w, r, "/moved", http.StatusTemporaryRedirect)
						return
					}
					final.record(r)
					w.WriteHeader(http.StatusAccepted)
				}))
				defer srv.Close()
				if err := send(srv); err != nil {
					t.Fatalf("https redirect on the same host: %v", err)
				}
				if n, auth := final.get(); n != 1 || auth != "Bearer "+t2AgentToken {
					t.Errorf("same host: %d request(s), Authorization %q; want the credential", n, auth)
				}
			})

			t.Run("3 redirects followed", func(t *testing.T) {
				var hops atomic.Int32
				srv := ch.start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := hops.Add(1)
					if n > 3 {
						w.WriteHeader(http.StatusAccepted)
						return
					}
					http.Redirect(w, r, "/hop"+strconv.Itoa(int(n)), http.StatusTemporaryRedirect)
				}))
				defer srv.Close()
				if err := send(srv); err != nil || hops.Load() != 4 {
					t.Errorf("3 redirects: error %v after %d request(s), want success after 4", err, hops.Load())
				}
			})

			t.Run("a fourth redirect refused", func(t *testing.T) {
				var hops atomic.Int32
				srv := ch.start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := hops.Add(1)
					if n > 4 {
						w.WriteHeader(http.StatusAccepted)
						return
					}
					http.Redirect(w, r, "/hop"+strconv.Itoa(int(n)), http.StatusTemporaryRedirect)
				}))
				defer srv.Close()
				if err := send(srv); err == nil || !strings.Contains(err.Error(), "stopped after 3 redirects") {
					t.Errorf("4 redirects: error %v, want the redirect limit", err)
				}
				if n := hops.Load(); n != 4 {
					t.Errorf("server saw %d request(s), want 4 (the request and 3 redirects)", n)
				}
			})
		})
	}
}

// TestT2AgentSpoolsTelemetryAfterARefusedRedirect covers WP-365 (AGT-03): a
// report that cannot be sent (here: redirect to http) is logged and spooled;
// the next successful send delivers it first and empties the spool. A
// report the backend rejects as invalid does not block the spool.
func TestT2AgentSpoolsTelemetryAfterARefusedRedirect(t *testing.T) {
	var (
		mu       sync.Mutex
		redirect = true
		reject   = false
		received []agent.TelemetryPayload
	)
	var plain recorder
	plainSrv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { plain.record(r) }))
	defer plainSrv.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if redirect {
			http.Redirect(w, r, plainSrv.URL+r.URL.Path, http.StatusFound)
			return
		}
		var p agent.TelemetryPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		if reject && p.Hostname == "first" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received = append(received, p)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	cfg := agentConfig{ServerURL: srv.URL, AgentID: "t2-agent", Token: t2AgentToken, CAPEM: caPEM(srv), SpoolDir: t.TempDir()}
	spooled := func() int {
		t.Helper()
		n, err := spoolFor(cfg).Len(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	cfg.Hostname = "first"
	collectAndSend(context.Background(), cfg)
	if n := spooled(); n != 1 {
		t.Fatalf("after the refused redirect: %d spooled report(s), want 1", n)
	}
	if n, _ := plain.get(); n != 0 {
		t.Fatalf("plain HTTP server received %d request(s), want 0", n)
	}

	mu.Lock()
	redirect = false
	mu.Unlock()
	cfg.Hostname = "second"
	collectAndSend(context.Background(), cfg)
	mu.Lock()
	got := append([]agent.TelemetryPayload(nil), received...)
	mu.Unlock()
	if len(got) != 2 || got[0].Hostname != "second" || got[1].Hostname != "first" || got[1].CollectedAt.IsZero() {
		t.Fatalf("delivered %+v, want the current report and then the spooled one", got)
	}
	if n := spooled(); n != 0 {
		t.Errorf("after the successful send: %d spooled report(s), want 0", n)
	}

	// A spooled report the backend rejects as invalid is dropped; a
	// transient failure keeps it.
	cfg.Hostname = "first"
	spoolTelemetry(context.Background(), cfg, buildTelemetry(cfg))
	mu.Lock()
	redirect = true
	mu.Unlock()
	flushSpool(context.Background(), cfg)
	if n := spooled(); n != 1 {
		t.Errorf("after a failed flush: %d spooled report(s), want 1", n)
	}
	mu.Lock()
	redirect, reject = false, true
	mu.Unlock()
	flushSpool(context.Background(), cfg)
	if n := spooled(); n != 0 {
		t.Errorf("after a 400 for the spooled report: %d spooled report(s), want 0", n)
	}
	if entries, err := os.ReadDir(cfg.SpoolDir); err != nil || len(entries) != 0 {
		t.Errorf("spool directory not empty: %v (err %v)", entries, err)
	}
}
