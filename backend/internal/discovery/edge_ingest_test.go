package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
)

type fakeLicense struct{ state, problem string }

func (f fakeLicense) DiscoveryLicense(context.Context, string) (state, problemType string, err error) {
	return f.state, f.problem, nil
}

// TestEdgeIngest covers the collectors' edge data path (COL-08): records
// sent over the mTLS NATS channel are processed like the HTTP ingest for the
// organization of the subject; an expired or missing discovery license is
// refused with the problem type the collector pauses on; invalid bodies are
// refused like on the API.
func TestEdgeIngest(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	h := NewHandler(repo, cis)
	ctx := context.Background()
	col := &Collector{OrganizationID: "org-1", Name: "edge"}
	if err := repo.RegisterCollector(ctx, col); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(ingestBody{Records: []DeviceRecord{consumerRecord("m1", time.Now().UTC().Add(-time.Hour))}})
	if err != nil {
		t.Fatal(err)
	}

	reply := h.EdgeIngest(ctx, "org-1", col.ID, body)
	if reply.Status != http.StatusAccepted || reply.Result == nil || reply.Result.Created != 1 || reply.LicenseStatus != LicenseActive {
		t.Fatalf("ingest: %+v", reply)
	}
	items, _, err := cis.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("CIs of org-1: %d, %v", len(items), err)
	}
	if other, _, _ := cis.List(ctx, "org-2", ci.FilterParams{}, api.PaginationParams{Limit: 10}); len(other) != 0 {
		t.Errorf("records reached another organization: %d", len(other))
	}

	if reply = h.EdgeIngest(ctx, "org-1", col.ID, []byte(`{"records":[]}`)); reply.Status != http.StatusBadRequest {
		t.Errorf("empty batch: %+v", reply)
	}
	if reply = h.EdgeIngest(ctx, "org-1", col.ID, []byte(`not json`)); reply.Status != http.StatusBadRequest {
		t.Errorf("invalid body: %+v", reply)
	}

	h.WithEdgeLicense(fakeLicense{state: LicenseExpired, problem: api.ProblemLicenseExpired})
	if reply = h.EdgeIngest(ctx, "org-1", col.ID, body); reply.Status != http.StatusForbidden ||
		reply.Type != api.ProblemLicenseExpired || reply.LicenseStatus != LicenseExpired {
		t.Errorf("expired license: %+v", reply)
	}
	h.WithEdgeLicense(fakeLicense{state: LicenseNotEntitled, problem: api.ProblemFeatureNotEntitled})
	if reply = h.EdgeIngest(ctx, "org-1", col.ID, body); reply.Status != http.StatusForbidden || reply.Type != api.ProblemFeatureNotEntitled {
		t.Errorf("not entitled: %+v", reply)
	}
}

// TestEdgeHeartbeat: a heartbeat over the edge activates the collector and
// reports its status and the license state; an unknown collector is 404.
func TestEdgeHeartbeat(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, ci.NewMemoryRepository()).WithEdgeLicense(fakeLicense{state: LicenseActive})
	ctx := context.Background()
	c := &Collector{OrganizationID: "org-1", Name: "edge"}
	if err := repo.RegisterCollector(ctx, c); err != nil {
		t.Fatal(err)
	}
	reply := h.EdgeHeartbeat(ctx, "org-1", c.ID, []byte(`{"spool":{"messages":1}}`))
	if reply.Status != http.StatusNoContent || reply.CollectorStatus != CollectorActive || reply.LicenseStatus != LicenseActive {
		t.Fatalf("heartbeat: %+v", reply)
	}
	if reply = h.EdgeHeartbeat(ctx, "org-1", "unknown", nil); reply.Status != http.StatusNotFound {
		t.Errorf("unknown collector: %+v", reply)
	}
	if reply = h.EdgeHeartbeat(ctx, "org-1", c.ID, []byte(`{`)); reply.Status != http.StatusBadRequest {
		t.Errorf("invalid body: %+v", reply)
	}
}

// scopeRecorder resolves every target to ci-<target> and records the
// tenant scope the samples were assigned in.
type scopeRecorder struct {
	mu     sync.Mutex
	scopes []database.TenantScope
}

func (s *scopeRecorder) ResolveCI(ctx context.Context, _, address string) (string, bool) {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return "", false
	}
	s.mu.Lock()
	s.scopes = append(s.scopes, scope)
	s.mu.Unlock()
	if address == "192.0.2.99" {
		return "", false
	}
	return "ci-" + address, true
}

func (s *scopeRecorder) last() database.TenantScope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scopes[len(s.scopes)-1]
}

// metricsReply is the answer to .metrics as the collector reads it.
type metricsReply struct {
	Status     int    `json:"status"`
	Type       string `json:"type"`
	Error      string `json:"error"`
	Ingested   *int   `json:"ingested"`
	Unassigned *int   `json:"unassigned"`
}

// accepted reports whether the reply is 202 with the counts.
func (r metricsReply) accepted(ingested, unassigned int) bool {
	return r.Status == http.StatusAccepted && r.Ingested != nil && *r.Ingested == ingested &&
		r.Unassigned != nil && *r.Unassigned == unassigned
}

// asMetricsReply returns reply as it travels on the edge.
func asMetricsReply(t *testing.T, reply EdgeReply) (metricsReply, string) {
	t.Helper()
	raw, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	var out metricsReply
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out, string(raw)
}

type fakeFeatures map[string]entitlement.Status

func (f fakeFeatures) Status(_ context.Context, orgID, _ string) (entitlement.Entitlement, entitlement.Status, error) {
	status, ok := f[orgID]
	if !ok {
		status = entitlement.StatusNotEntitled
	}
	return entitlement.Entitlement{}, status, nil
}

// TestEdgeMetrics covers the collector side of F-046 on the server: the
// samples a collector sends on .metrics run through the ingest of
// POST /monitoring/metrics for the organization of the subject (202 with
// ingested and unassigned), in the scope of the collector: its client and
// site, org-wide for a collector without them. Without the monitoring
// entitlement the answer is 403 with the problem type of the entitlement
// middleware; an invalid body is 400; an unknown or disabled collector is
// refused and nothing is stored.
func TestEdgeMetrics(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	register := func(name, client, site string) *Collector {
		c := &Collector{OrganizationID: "org-1", ClientID: client, SiteID: site, Name: name, Status: CollectorActive}
		if err := repo.RegisterCollector(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	orgWide := register("org-wide", "", "")
	client1 := register("client-1", "client-1", "")
	site1 := register("site-1", "client-1", "site-1")
	store := monitoring.NewMemoryMetricStore()
	resolver := &scopeRecorder{}
	h := NewHandler(repo, ci.NewMemoryRepository())
	if _, ok := h.edgeHandlers()[MetricsSuffix]; ok {
		t.Fatal(".metrics is served without a metrics ingest")
	}
	h.WithEdgeMetrics(monitoring.NewHandler(store).WithCIResolver(resolver), fakeFeatures{"org-1": entitlement.StatusActive})
	if _, ok := h.edgeHandlers()[MetricsSuffix]; !ok {
		t.Fatal(".metrics is not served")
	}
	body := []byte(`[{"name":"sweep_reachable","value":1,"labels":{"target":"192.0.2.10","kind":"sweep"}},` +
		`{"name":"sweep_reachable","value":0,"labels":{"target":"192.0.2.99","kind":"sweep"}}]`)
	stored := func(orgID string) int {
		points, err := store.Query(ctx, monitoring.MetricQuery{OrgID: orgID, CIID: "ci-192.0.2.10", Name: "sweep_reachable"})
		if err != nil {
			t.Fatal(err)
		}
		return len(points)
	}

	scopes := []struct {
		col           *Collector
		client, sites string
	}{
		{orgWide, "all", "all"},
		{client1, "client-1", "all"},
		{site1, "client-1", "site-1"},
	}
	describe := func(s database.ScopeSet) string {
		if s.IsAll() {
			return "all"
		}
		return strings.Join(s.IDs(), ",")
	}
	for i, tc := range scopes {
		reply, raw := asMetricsReply(t, h.EdgeMetrics(systemContext(ctx, "org-1"), "org-1", tc.col.ID, body))
		if !reply.accepted(1, 1) {
			t.Fatalf("%s: reply %s, want 202 with ingested 1 and unassigned 1", tc.col.Name, raw)
		}
		scope := resolver.last()
		if scope.OrgID != "org-1" || describe(scope.Clients) != tc.client || describe(scope.Sites) != tc.sites {
			t.Errorf("%s: assigned in org %s, clients %s, sites %s; want the collector's scope (clients %s, sites %s)",
				tc.col.Name, scope.OrgID, describe(scope.Clients), describe(scope.Sites), tc.client, tc.sites)
		}
		if n := stored("org-1"); n != i+1 {
			t.Errorf("%s: %d stored samples of org-1, want %d", tc.col.Name, n, i+1)
		}
	}
	if n := stored("org-2"); n != 0 {
		t.Errorf("samples reached another organization: %d", n)
	}

	// Only unassigned samples: 202 with both counts in the reply.
	reply, raw := asMetricsReply(t, h.EdgeMetrics(ctx, "org-1", orgWide.ID, []byte(`[{"name":"sweep_reachable","value":1,"labels":{"target":"192.0.2.99"}}]`)))
	if !reply.accepted(0, 1) || !strings.Contains(raw, `"ingested":0`) {
		t.Errorf("unassigned only: %s", raw)
	}
	for _, bad := range []string{`not json`, `[]`, `[{"value":1}]`} {
		if reply, raw = asMetricsReply(t, h.EdgeMetrics(ctx, "org-1", orgWide.ID, []byte(bad))); reply.Status != http.StatusBadRequest {
			t.Errorf("body %s: %s, want 400", bad, raw)
		}
	}

	// The monitoring entitlement as on POST /monitoring/metrics.
	h.WithEdgeMetrics(monitoring.NewHandler(store).WithCIResolver(resolver), fakeFeatures{})
	before := stored("org-1")
	if reply, raw = asMetricsReply(t, h.EdgeMetrics(ctx, "org-1", orgWide.ID, body)); reply.Status != http.StatusForbidden || reply.Type != api.ProblemFeatureNotEntitled {
		t.Errorf("not entitled: %s, want 403 with %s", raw, api.ProblemFeatureNotEntitled)
	}
	if n := stored("org-1"); n != before {
		t.Errorf("not entitled: %d samples stored", n-before)
	}

	// Unknown and disabled collectors (COL-04).
	h.WithEdgeMetrics(monitoring.NewHandler(store).WithCIResolver(resolver), nil)
	disabled := CollectorDisabled
	if _, err := repo.UpdateCollector(ctx, "org-1", client1.ID, &CollectorPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if reply, raw = asMetricsReply(t, h.EdgeMetrics(ctx, "org-1", client1.ID, body)); reply.Status != http.StatusForbidden {
		t.Errorf("disabled collector: %s, want 403", raw)
	}
	if reply, raw = asMetricsReply(t, h.EdgeMetrics(ctx, "org-2", orgWide.ID, body)); reply.Status != http.StatusNotFound {
		t.Errorf("collector of another organization: %s, want 404", raw)
	}
	if n := stored("org-1"); n != before {
		t.Errorf("refused collectors stored %d samples", n-before)
	}
}

func TestEdgeSubjects(t *testing.T) {
	org, col := "6f0b6f0e-0000-4000-8000-000000000001", "6f0b6f0e-0000-4000-8000-000000000002"
	if o, c, ok := subjectCollector("collector."+org+"."+col+IngestSuffix, IngestSuffix); !ok || o != org || c != col {
		t.Errorf("ingest subject: %s %s %v", o, c, ok)
	}
	if o, c, ok := subjectCollector("collector."+org+"."+col+MetricsSuffix, MetricsSuffix); !ok || o != org || c != col {
		t.Errorf("metrics subject: %s %s %v", o, c, ok)
	}
	if _, _, ok := subjectCollector("collector.x.y"+IngestSuffix, IngestSuffix); ok {
		t.Error("non-UUID subject accepted")
	}
}
