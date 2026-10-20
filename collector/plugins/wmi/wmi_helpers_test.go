package wmi

// Helpers of the WMI tests. The fake WinRM endpoints listen on a random
// 127.x.y.z loopback address on the fixed WinRM ports so parallel runs do not
// collide.

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	auditUser     = `CORP\audit-admin`
	auditPassword = "S3cret!-audit-wmi"
	// plainWinRMPort is WinRM over plain HTTP; the plugin never uses it.
	plainWinRMPort = 5985
)

// auditListen binds port on a random 127.x.y.z address.
func auditListen(t *testing.T, port int) (net.Listener, string) {
	t.Helper()
	var lastErr error
	for range 20 {
		ip := fmt.Sprintf("127.%d.%d.%d", 1+rand.IntN(250), rand.IntN(250), 1+rand.IntN(250))
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
		if err == nil {
			return ln, ip
		}
		lastErr = err
	}
	t.Fatalf("cannot bind a loopback address on port %d: %v", port, lastErr)
	return nil, ""
}

// serveWinRM serves handler on ln, over TLS with the self-signed httptest
// certificate when useTLS is set.
func serveWinRM(t *testing.T, ln net.Listener, handler http.Handler, useTLS bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	_ = srv.Listener.Close()
	srv.Listener = ln
	// Rejected handshakes are expected; keep them out of the test output.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if useTLS {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

// pinScope is the TLS scope of ip that pins the SPKI of cert.
func pinScope(t *testing.T, ip string, cert *x509.Certificate) plugins.TLSScope {
	t.Helper()
	_, network, err := net.ParseCIDR(ip + "/32")
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return plugins.TLSScope{Network: network, PinsSHA256: [][]byte{pin[:]}}
}

// auditRecorder records every request a fake WinRM endpoint receives.
type auditRecorder struct {
	mu   sync.Mutex
	dump []string
}

func (r *auditRecorder) add(req *http.Request) {
	raw, _ := httputil.DumpRequest(req, true)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dump = append(r.dump, string(raw))
}

// count is the number of requests received.
func (r *auditRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.dump)
}

// leaked reports the requests that carry the password readable (plain text
// or as HTTP Basic credentials).
func (r *auditRecorder) leaked() []string {
	basic := base64.StdEncoding.EncodeToString([]byte(auditUser + ":" + auditPassword))
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, d := range r.dump {
		if strings.Contains(d, auditPassword) || strings.Contains(d, basic) {
			first, _, _ := strings.Cut(d, "\r\n")
			out = append(out, first)
		}
	}
	return out
}
