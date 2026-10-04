package collectorcmd

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
)

// relayBackend records the agent telemetry requests it receives.
type relayBackend struct {
	mu       sync.Mutex
	status   int
	requests []string
}

func (b *relayBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := io.Reader(r.Body)
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err == nil {
			body = gz
		}
	}
	data, _ := io.ReadAll(body)
	b.mu.Lock()
	b.requests = append(b.requests, r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(data))
	status := b.status
	b.mu.Unlock()
	w.WriteHeader(status)
}

// TestAgentRelayIsTLSOnlyAndForwardsTheAgentCredential covers WP-051
// (AGT-03): the relay speaks TLS only; it forwards telemetry with the
// agent's own bearer credential to the agent telemetry endpoint; a backend
// outage spools the message under the agent topic, and the replay goes to the
// agent telemetry endpoint again, never to discovery; a rejected credential
// is not spooled.
func TestAgentRelayIsTLSOnlyAndForwardsTheAgentCredential(t *testing.T) {
	backend := &relayBackend{status: http.StatusAccepted}
	srv := httptest.NewServer(backend)
	defer srv.Close()

	tlsSrv := httptest.NewUnstartedServer(nil)
	tlsSrv.StartTLS() // only for its certificate
	cert := tlsSrv.TLS.Certificates[0]
	pool := tlsSrv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	tlsSrv.Close()

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := &uploader{cfg: collectorConfig{ServerURL: srv.URL}, client: srv.Client(), spool: &buffer.DiskBuffer{Dir: t.TempDir()}}
	go serveAgentRelay(ctx, listener, up)
	addr := listener.Addr().String()

	send := func(line string) string {
		t.Helper()
		conn, dialErr := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool, ServerName: "example.com"})
		if dialErr != nil {
			t.Fatalf("tls dial: %v", dialErr)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, writeErr := conn.Write([]byte(line + "\n")); writeErr != nil {
			t.Fatal(writeErr)
		}
		ack, _ := bufio.NewReader(conn).ReadString('\n')
		return ack
	}
	message := `{"token":"agent-token-1","telemetry":{"agent_id":"laptop-1","hostname":"laptop-1"}}`

	// Plaintext is never accepted.
	plain, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = plain.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = plain.Write([]byte(message + "\n"))
	reply, _ := io.ReadAll(plain)
	plain.Close()
	if strings.Contains(string(reply), `"ok"`) {
		t.Fatal("the relay accepted a plaintext message")
	}

	if ack := send(message); !strings.Contains(ack, `"ok"`) {
		t.Fatalf("TLS message: ack %q", ack)
	}
	backend.mu.Lock()
	got := strings.Join(backend.requests, "|")
	backend.mu.Unlock()
	if !strings.HasPrefix(got, "/api/v1/agents/telemetry Bearer agent-token-1 ") || !strings.Contains(got, `"agent_id":"laptop-1"`) {
		t.Fatalf("forwarded request %q", got)
	}
	if ack := send(`{"telemetry":{"agent_id":"x"}}`); !strings.Contains(ack, "error") {
		t.Errorf("message without credential: ack %q", ack)
	}

	// Backend outage: spooled under the agent topic, replayed to telemetry.
	backend.mu.Lock()
	backend.status, backend.requests = http.StatusServiceUnavailable, nil
	backend.mu.Unlock()
	if ack := send(message); !strings.Contains(ack, `"ok"`) {
		t.Fatalf("spooled message: ack %q", ack)
	}
	msgs, err := up.spool.PeekBatch(ctx, 10)
	if err != nil || len(msgs) != 1 || msgs[0].Topic != agentTelemetryTopic {
		t.Fatalf("spool %+v, %v; want one agent telemetry message", msgs, err)
	}
	backend.mu.Lock()
	backend.status, backend.requests = http.StatusAccepted, nil
	backend.mu.Unlock()
	up.flushSpool(ctx)
	backend.mu.Lock()
	got = strings.Join(backend.requests, "|")
	backend.mu.Unlock()
	if !strings.HasPrefix(got, "/api/v1/agents/telemetry Bearer agent-token-1 ") {
		t.Errorf("replay went to %q, want the agent telemetry endpoint", got)
	}
	if n, _ := up.spool.Len(ctx); n != 0 {
		t.Errorf("spool still holds %d messages", n)
	}

	// A rejected credential is answered with an error and not spooled.
	backend.mu.Lock()
	backend.status = http.StatusUnauthorized
	backend.mu.Unlock()
	if ack := send(message); !strings.Contains(ack, "rejected") {
		t.Errorf("rejected credential: ack %q", ack)
	}
	if n, _ := up.spool.Len(ctx); n != 0 {
		t.Errorf("rejected telemetry was spooled (%d)", n)
	}
}

// TestAgentRelayNeedsACertificate: without a server certificate the relay
// does not start.
func TestAgentRelayNeedsACertificate(t *testing.T) {
	if _, err := agentRelayTLS(collectorConfig{}); err == nil {
		t.Error("relay TLS configured without certificate")
	}
}
