package rls_test

// Metric ingest of a CI the caller does not see (WP-665, k7n-metric-ingest-
// errors, F-394; MON-02, API-03, TEN-06). POST /api/v1/monitoring/metrics
// through the complete PostgreSQL router answers a sample whose ci_id names
// a CI of another client, of another organization or no CI at all like
// GET /api/v1/cis/{id}/metrics: 404 problem+json naming the field ci_id and
// the ci_id sent, never 500, and nothing is stored. A body that mixes a
// visible and an invisible CI is refused whole (all or nothing, documented
// in api/openapi.yaml): the visible sample is not stored either. Controls:
// the own CI 202, a malformed or NUL-carrying ci_id 400. The collectors'
// edge subject .metrics (discovery.Handler.EdgeMetrics) answers the same
// 404 for a CI outside the collector's scope instead of 500.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//     TEST_APP_DATABASE_URL=... go test -count=1 -v \
//     -run '^(TestMetricIngestForInvisibleCIIsNotFound|TestEdgeMetricIngestForInvisibleCIIsNotFound)$' ./internal/tenant/rls/

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
)

// metricSample is one sample of an ingest body.
type metricSample struct {
	CIID  string  `json:"ci_id"`
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// metricIngestFixture seeds a CI of client 1 (own), one of client 2 and one
// of organization B, and removes the samples of both organizations at the
// end (metric_sample has no foreign key to the organization).
func metricIngestFixture(t *testing.T) (f *scopetest.Fixture, own, otherClient, otherOrg string, count func(name string) int) {
	t.Helper()
	f = scopetest.Seed(t, "c9")
	bg := context.Background()
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(bg, `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`, []string{f.OrgA, f.OrgB})
	})
	own = f.CI(t, f.OrgA, f.Client1, "k7n-metric-own")
	otherClient = f.CI(t, f.OrgA, f.Client2, "k7n-metric-c2")
	otherOrg = f.CI(t, f.OrgB, "", "k7n-metric-orgb")
	count = func(name string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM metric_sample WHERE metric_name = $1`, name).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		return n
	}
	return f, own, otherClient, otherOrg, count
}

func TestMetricIngestForInvisibleCIIsNotFound(t *testing.T) {
	f, own, otherClient, otherOrg, count := metricIngestFixture(t)
	env := newChannelEnv(t, f)
	user := f.AppUser(t, f.OrgA, "k7n-metric-user")
	client1 := &channelPrincipal{kind: "client1", org: f.OrgA, user: user,
		scope: identity.Scope{Clients: only(f.Client1), Sites: all(), Teams: all()}}
	orgWide := &channelPrincipal{kind: "orgwide", org: f.OrgA, user: user,
		scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}
	const unknown = "00000000-0000-4000-8000-0000000000c9"

	cases := []struct {
		label     string
		p         *channelPrincipal
		body      []metricSample
		status    int
		offending string // the ci_id the 404 names
	}{
		{"control: own CI", client1, []metricSample{{own, "k7n_own", 1}}, http.StatusAccepted, ""},
		{"client 1 -> CI of client 2", client1, []metricSample{{otherClient, "k7n_c2", 1}}, http.StatusNotFound, otherClient},
		{"client 1 -> CI of organization B", client1, []metricSample{{otherOrg, "k7n_orgb", 1}}, http.StatusNotFound, otherOrg},
		{"org-wide -> CI of organization B", orgWide, []metricSample{{otherOrg, "k7n_orgb2", 1}}, http.StatusNotFound, otherOrg},
		{"org-wide -> unknown CI", orgWide, []metricSample{{unknown, "k7n_unknown", 1}}, http.StatusNotFound, unknown},
		{"client 1 mixed: own + client 2", client1, []metricSample{{own, "k7n_mixed_own", 1}, {otherClient, "k7n_mixed_c2", 1}}, http.StatusNotFound, otherClient},
		{"org-wide mixed: unknown + own", orgWide, []metricSample{{unknown, "k7n_mixed_unknown", 1}, {own, "k7n_mixed_own2", 1}}, http.StatusNotFound, unknown},
		{"control: malformed ci_id", orgWide, []metricSample{{"not-a-uuid", "k7n_malformed", 1}}, http.StatusBadRequest, ""},
		{"control: ci_id with NUL", orgWide, []metricSample{{"0000\x00", "k7n_nul", 1}}, http.StatusBadRequest, ""},
	}
	for _, c := range cases {
		code, body := env.call(c.p, http.MethodPost, "/api/v1/monitoring/metrics", map[string]any{"metrics": c.body})
		if code != c.status {
			t.Errorf("%s: POST /monitoring/metrics = %d %s; want %d", c.label, code, trim(body), c.status)
		}
		stored := 0
		for _, s := range c.body {
			stored += count(s.Name)
		}
		want := 0
		if c.status == http.StatusAccepted {
			want = len(c.body)
		}
		if stored != want {
			t.Errorf("%s: %d sample(s) stored, want %d (all or nothing)", c.label, stored, want)
		}
		if c.offending == "" || code != http.StatusNotFound {
			continue
		}
		var problem struct {
			Type   string `json:"type"`
			Status int    `json:"status"`
			Detail string `json:"detail"`
			Field  string `json:"field"`
		}
		if err := json.Unmarshal([]byte(body), &problem); err != nil || problem.Status != http.StatusNotFound ||
			problem.Field != "ci_id" || !strings.Contains(problem.Detail, c.offending) {
			t.Errorf("%s: problem %s; want status 404, field ci_id and the ci_id %s in detail", c.label, trim(body), c.offending)
		}
		// TEN-06: the answer names nothing of the invisible CI but the id sent.
		for _, leak := range []string{"k7n-metric", "metric_sample", "SQLSTATE"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: the answer discloses %q: %s", c.label, leak, trim(body))
			}
		}
	}
	// The read path the ingest now matches.
	for _, id := range []string{otherClient, otherOrg, unknown} {
		if code, body := env.call(client1, http.MethodGet, "/api/v1/cis/"+id+"/metrics", nil); code != http.StatusNotFound {
			t.Errorf("control GET /cis/%s/metrics as client 1 = %d %s; want 404", id, code, trim(body))
		}
	}
}

// TestEdgeMetricIngestForInvisibleCIIsNotFound covers the collectors' edge
// subject .metrics with the production chain (discovery.PGRepository, the
// monitoring ingest of POST /monitoring/metrics with the PostgreSQL store
// and CI resolver): a collector of client 1 that names the CI of client 2,
// of organization B or an unknown CI by ci_id gets 404, not 500, and nothing
// of the batch is stored; its own CI gets 202.
func TestEdgeMetricIngestForInvisibleCIIsNotFound(t *testing.T) {
	f, own, otherClient, otherOrg, count := metricIngestFixture(t)
	repo := discovery.NewPGRepository(f.App)
	ciRepo := ci.NewPGRepository(f.App)
	metrics := monitoring.NewHandler(monitoring.NewPGMetricStore(f.App)).WithCIResolver(monitoring.CIAddressResolver{CIs: ciRepo})
	h := discovery.NewHandler(repo, ciRepo).WithEdgeMetrics(metrics, nil)
	col := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "k7n-metric-edge",
		Status: discovery.CollectorActive, Config: map[string]any{}}
	if err := repo.RegisterCollector(f.OrgCtx(f.OrgA), col); err != nil {
		t.Fatal(err)
	}
	// As ServeCollectorEdge: the organization-wide system scope; the handler
	// narrows it to the collector's client.
	scope := database.OrgWideScope(f.OrgA, "")
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	const unknown = "00000000-0000-4000-8000-0000000001c9"

	cases := []struct {
		label     string
		body      []metricSample
		status    int
		offending string
	}{
		{"control: own CI", []metricSample{{own, "k7n_edge_own", 1}}, http.StatusAccepted, ""},
		{"CI of client 2", []metricSample{{otherClient, "k7n_edge_c2", 1}}, http.StatusNotFound, otherClient},
		{"CI of organization B", []metricSample{{otherOrg, "k7n_edge_orgb", 1}}, http.StatusNotFound, otherOrg},
		{"unknown CI", []metricSample{{unknown, "k7n_edge_unknown", 1}}, http.StatusNotFound, unknown},
		{"mixed: own + client 2", []metricSample{{own, "k7n_edge_mixed_own", 1}, {otherClient, "k7n_edge_mixed_c2", 1}}, http.StatusNotFound, otherClient},
	}
	for _, c := range cases {
		raw, err := json.Marshal(c.body)
		if err != nil {
			t.Fatal(err)
		}
		reply := h.EdgeMetrics(ctx, f.OrgA, col.ID, raw)
		if reply.Status != c.status {
			t.Errorf("%s: .metrics = %d %q; want %d", c.label, reply.Status, reply.Error, c.status)
		}
		if c.offending != "" && !strings.Contains(reply.Error, c.offending) {
			t.Errorf("%s: reply error %q does not name the ci_id %s", c.label, reply.Error, c.offending)
		}
		stored := 0
		for _, s := range c.body {
			stored += count(s.Name)
		}
		want := 0
		if c.status == http.StatusAccepted {
			want = len(c.body)
		}
		if stored != want {
			t.Errorf("%s: %d sample(s) stored, want %d (all or nothing)", c.label, stored, want)
		}
	}
}
