//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect test of the agent's
// direct channel.
//
//   - WP-051 (AGT-03), finding P2 "Agent follows https->http redirects and
//     sends its Bearer credential in cleartext". AGT-03 / WP-051: agent
//     traffic only over TLS, no HTTP fallback. httpClientFor sets no
//     CheckRedirect, so the default policy follows a redirect of the
//     verified HTTPS endpoint to plain http on the same host and keeps the
//     Authorization header; sendDirect checks only the initial URL.
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./cmd/agent/
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// TestAuditT2AgentRefusesRedirectToPlainHTTP: a redirect from the verified
// HTTPS server to http:// must end the send with an error, and the plain
// HTTP server must see neither the credential nor the telemetry.
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
