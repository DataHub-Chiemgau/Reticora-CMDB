//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT test (must fail
// until the defect is fixed).
//
// WP-152 (DIS-04) / WP-147 (COL-02).
//
// Finding: P0 "WMI plugin sends credentials in cleartext" – the audit
// recommendation and the critical test gap extend it to the NAS plugin:
// Synology logs in with account and password in the query string over
// http://target:5000, and the HTTPS variant (5001) uses InsecureSkipVerify.
// COL-02: device credentials are never sent unencrypted or to unverified
// endpoints.
//
// The fake NAS endpoints listen on a random 127.x.y.z loopback address on the
// fixed vendor ports so parallel runs do not collide.
//
// Retest:
//
//	cd collector && go test -tags auditdefect -count=1 -v -run 'TestAuditNASCredentialTransport' ./plugins/nas/
package nas

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	auditNASUser     = "audit-nas-admin"
	auditNASPassword = "S3cret-audit-nas"
)

func auditNASListen(t *testing.T, port int) (net.Listener, string) {
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

func TestAuditNASCredentialTransport(t *testing.T) {
	cases := []struct {
		name string
		port int
		tls  bool
	}{
		{name: "Synology DSM over http://target:5000", port: 5000},
		{name: "Synology DSM over https://target:5001 with an untrusted certificate", port: 5001, tls: true},
	}
	basic := base64.StdEncoding.EncodeToString([]byte(auditNASUser + ":" + auditNASPassword))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, ip := auditNASListen(t, tc.port)
			var mu sync.Mutex
			var leaked []string
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := httputil.DumpRequest(r, true)
				dump, _ := url.QueryUnescape(string(raw))
				if strings.Contains(dump, auditNASPassword) || strings.Contains(string(raw), basic) {
					first, _, _ := strings.Cut(string(raw), "\r\n")
					mu.Lock()
					leaked = append(leaked, first)
					mu.Unlock()
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"success":true,"data":{"sid":"audit-sid","model":"DS920+","serial":"NAS-AUDIT-1"}}`)
			}))
			_ = srv.Listener.Close()
			srv.Listener = ln
			if tc.tls {
				srv.StartTLS()
			} else {
				srv.Start()
			}
			defer srv.Close()

			p := New()
			p.Timeout = 2 * time.Second
			_, _ = p.Collect(context.Background(), ip, map[string]string{"username": auditNASUser, "password": auditNASPassword})

			mu.Lock()
			defer mu.Unlock()
			if len(leaked) > 0 {
				t.Errorf("COL-02 violated: the NAS endpoint %s (tls=%v) received the password readable in %d request(s), e.g. %q",
					net.JoinHostPort(ip, fmt.Sprint(tc.port)), tc.tls, len(leaked), leaked[0])
			}
		})
	}
}
