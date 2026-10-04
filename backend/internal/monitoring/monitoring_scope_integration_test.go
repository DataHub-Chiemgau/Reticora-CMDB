package monitoring_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
)

// TestMonitoringStoreScope runs the metric and alert rule stores with the
// principal's tenant scope (TEN-06, WP-016): a principal restricted to client
// 1 neither reads nor writes samples of a client-2 CI, and alert rules of
// another organization stay invisible.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMonitoringStoreScope(t *testing.T) {
	f := scopetest.Seed(t, "28")
	own := f.CI(t, f.OrgA, f.Client1, "metrics-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "metrics-c2")
	bg := context.Background()
	t.Cleanup(func() {
		// metric_sample has no foreign key to the organization.
		if _, err := f.Admin.Exec(bg, `DELETE FROM metric_sample WHERE organization_id = $1`, f.OrgA); err != nil {
			t.Errorf("cleanup samples: %v", err)
		}
	})

	store := monitoring.NewPGMetricStore(f.App)
	now := time.Now().UTC()
	sample := func(ciID string) monitoring.Metric {
		return monitoring.Metric{OrgID: f.OrgA, CIID: ciID, Name: "cpu_usage", Value: 42, Timestamp: now}
	}
	if err := store.Ingest(f.OrgCtx(f.OrgA), []monitoring.Metric{sample(own), sample(foreign)}); err != nil {
		t.Fatalf("org-wide ingest: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	query := func(ciID string) []monitoring.MetricPoint {
		t.Helper()
		points, err := store.Query(ctx, monitoring.MetricQuery{OrgID: f.OrgA, CIID: ciID, Name: "cpu_usage", From: now.Add(-time.Hour)})
		if err != nil {
			t.Fatalf("query %s: %v", ciID, err)
		}
		return points
	}
	if got := query(own); len(got) != 1 {
		t.Fatalf("client-1 samples of own CI: %d, want 1", len(got))
	}
	if got := query(foreign); len(got) != 0 {
		t.Fatalf("client-1 samples of client-2 CI: %d, want 0", len(got))
	}
	if got := query(""); len(got) != 1 {
		t.Fatalf("client-1 samples of all CIs: %d, want only the own one", len(got))
	}
	if err := store.Ingest(ctx, []monitoring.Metric{sample(foreign)}); err == nil {
		t.Fatal("client-1 principal wrote a sample for a client-2 CI")
	}

	alerts := store.AlertStore()
	ruleB, err := alerts.CreateRule(f.OrgCtx(f.OrgB), monitoring.AlertRule{OrgID: f.OrgB, Name: "b", MetricName: "cpu_usage", Condition: "gt", Threshold: 90, Severity: "warning", Enabled: true})
	if err != nil {
		t.Fatalf("create organization B rule: %v", err)
	}
	if _, err = alerts.CreateRule(ctx, monitoring.AlertRule{OrgID: f.OrgA, Name: "a", MetricName: "cpu_usage", Condition: "gt", Threshold: 90, Severity: "warning", Enabled: true}); err != nil {
		t.Fatalf("create organization A rule: %v", err)
	}
	rules, err := alerts.ListRules(ctx, f.OrgA)
	if err != nil || len(rules) != 1 || rules[0].OrgID != f.OrgA {
		t.Fatalf("organization A rules: %+v err=%v, want only the A rule", rules, err)
	}
	if _, err = alerts.DeleteRule(ctx, f.OrgB, ruleB.ID); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("delete organization B rule: got %v, want ErrTenantMismatch", err)
	}
	if _, err = store.Query(bg, monitoring.MetricQuery{OrgID: f.OrgA}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("query without scope: got %v, want ErrNoTenantScope", err)
	}
}
