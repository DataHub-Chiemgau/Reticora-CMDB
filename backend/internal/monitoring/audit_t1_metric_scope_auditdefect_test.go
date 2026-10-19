//go:build auditdefect

package monitoring_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT tests (they fail until
// the product is fixed).
//
// WP-040 (MON-01, TEC-06; TEN-06) P2 "Metric series names of a
// foreign-client CI are visible via GET /api/v1/cis/{id}/metrics
// (MetricNames without scope predicate)": pg_store.go MetricNames reads
// metric_sample_v with organization and CI only; the views of migration
// 000071 are org-only, so a client-1 or site-1 principal lists the series of
// a client-2 CI.
//
// WP-025 (TEN-02, TEN-05) / WP-039 (TEC-06) P2 "review_item and metric_sample
// carry no client_id/site_id; DB-level scope is org-only (AC1)": WP-025 AC1
// names metric_sample among the child tables that get client_id/site_id and
// client predicates; WP-039 AC1 requires the access to metric_sample_1h to be
// org-/scope-limited. A client-1 or site-1 principal selecting the views
// directly (any new read path) still gets the client-2 samples.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT1MetricViewsHonourScope' ./internal/monitoring/

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestAuditT1MetricViewsHonourScope(t *testing.T) {
	m := seedT1Metrics(t, "da")
	for kind, ctx := range m.scopes() {
		if kind == "org" {
			continue
		}
		scope, _ := database.TenantScopeFromContext(ctx)
		for _, view := range []struct{ name, sql string }{
			{"metric_sample_v", `SELECT count(*) FROM metric_sample_v WHERE organization_id = $1 AND ci_id = $2`},
			{"metric_sample_1h_v", `SELECT count(*) FROM metric_sample_1h_v WHERE organization_id = $1 AND ci_id = $2`},
		} {
			var n int
			if err := database.WithTenant(context.Background(), m.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, view.sql, m.f.OrgA, m.foreign).Scan(&n)
			}); err != nil {
				t.Fatalf("%s %s: %v", kind, view.name, err)
			}
			if n != 0 {
				t.Errorf("TEN-05/TEN-06: %s selects %d rows of the client-2 CI from %s, want 0", kind, n, view.name)
			}
		}
	}
	// Positive control: the org-wide principal reads them.
	orgWide := database.OrgWideScope(m.f.OrgA, m.f.User)
	var n int
	if err := database.WithTenant(context.Background(), m.f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM metric_sample_v WHERE organization_id = $1 AND ci_id = $2`, m.f.OrgA, m.foreign).Scan(&n)
	}); err != nil || n != 2 {
		t.Errorf("org-wide samples of the client-2 CI: %d, %v; want 2", n, err)
	}
}
