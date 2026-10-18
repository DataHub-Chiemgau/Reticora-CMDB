//go:build auditdefect

package collectorcmd

// Phase-1 acceptance audit, group T7-ui: DEFECT test (fails until the
// product is fixed).
//
// WP-190 (MON-02) P1 "Collector metric uploads (sweep reachability, UPS
// values, SNMP poll) are rejected with 401 and never reach the server":
// uploadMetricSamples -> postMetricsPayload (collectorcmd.go) POSTs
// /api/v1/monitoring/metrics with only Content-Type, X-Organization-ID and
// X-Collector-ID. The backend HTTP API requires a bearer token
// (middleware/auth.go "missing bearer token"; no client-certificate
// authentication on the HTTP API), and the edge channel has no metrics
// subject (edge.go knows only ".ingest" and ".heartbeat"). Records and
// heartbeats use the authenticated edge; metric samples do not, so they are
// dropped on every real installation.
//
// Requirement encoded: with the collector's authenticated edge channel up
// (as after enrollment), polled metric samples reach the server through an
// authenticated path: either over the edge, or over HTTP with a credential
// the backend accepts (Authorization: Bearer ...). The fake backend below
// answers 401 without a bearer, exactly like the real middleware.
//
// Retest:
//   cd collector && go test -tags auditdefect -count=1 -v -run 'TestAuditT7MetricSamplesReachTheServerAuthenticated' ./collectorcmd/

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

func TestAuditT7MetricSamplesReachTheServerAuthenticated(t *testing.T) {
	var (
		mu        sync.Mutex
		accepted  int
		refused   int
		httpPaths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		httpPaths = append(httpPaths, r.URL.Path)
		// The backend's HTTP API authenticates every /api/v1 request with a
		// bearer token; X-Organization-ID / X-Collector-ID are not credentials.
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || len(r.Header.Get("Authorization")) <= len("Bearer ") {
			refused++
			http.Error(w, `{"title":"Unauthorized","detail":"missing bearer token"}`, http.StatusUnauthorized)
			return
		}
		accepted++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	up, err := newUploader(context.Background(), testConfig(srv.URL, ""))
	if err != nil {
		t.Fatal(err)
	}

	// The enrolled collector holds its mutually authenticated edge channel:
	// the server answers on the collector's subject tree.
	var edgeSuffixes []string
	withEdge(t, func(_ context.Context, suffix string, _ []byte) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		edgeSuffixes = append(edgeSuffixes, suffix)
		return []byte(`{"status":202,"license_status":"active"}`), nil
	})

	samples := []plugins.MetricSample{
		{Name: "sweep_reachable", Value: 1, Labels: map[string]string{"target": "192.0.2.10"}},
		{Name: "ups_battery_charge_percent", Value: 97, Labels: map[string]string{"target": "192.0.2.20"}},
	}
	uploadErr := up.uploadMetricSamples(context.Background(), samples)

	mu.Lock()
	defer mu.Unlock()
	viaEdge := 0
	for _, s := range edgeSuffixes {
		if s != edgeHeartbeatSuffix {
			viaEdge++
		}
	}
	if viaEdge == 0 && accepted == 0 {
		t.Errorf("metric samples did not reach the server over an authenticated path: edge requests %v, HTTP %v accepted=%d refused(401)=%d, uploadMetricSamples error=%v "+
			"(MON-02: collector baseline metrics must be stored; the HTTP upload carries no credential and there is no edge metrics subject)",
			edgeSuffixes, httpPaths, accepted, refused, uploadErr)
	}
	if uploadErr != nil && (viaEdge > 0 || accepted > 0) {
		t.Errorf("samples were delivered but uploadMetricSamples reported %v", uploadErr)
	}
}
