package monitoring_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/jackc/pgx/v5"
)

// TestMetricStoreUsesBarrierViews covers WP-040 (MON-01, TEC-06) as decided
// in docs/decisions/0001-timescale-rls.md: the application role reaches
// metric_sample and metric_sample_1h only through the security-barrier views
// of migration 000071, which keep organizations apart; the store writes and
// reads through them, hourly steps come from metric_sample_1h with correctly
// weighted averages, and new chunks span one day.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMetricStoreUsesBarrierViews(t *testing.T) {
	f := scopetest.Seed(t, "52")
	bg := context.Background()
	t.Cleanup(func() {
		// metric_sample has no foreign key to the organization.
		if _, err := f.Admin.Exec(bg, `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`,
			[]string{f.OrgA, f.OrgB}); err != nil {
			t.Errorf("cleanup metric samples: %v", err)
		}
	})
	own := f.CI(t, f.OrgA, f.Client1, "metric-own")
	foreign := f.CI(t, f.OrgA, f.Client2, "metric-foreign")
	other := f.CI(t, f.OrgB, "", "metric-org-b")
	store := monitoring.NewPGMetricStore(f.App)

	// Two hours, two days ago: hour 1 has the samples 10 and 20, hour 2 has 40.
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	samples := []monitoring.Metric{
		{OrgID: f.OrgA, CIID: own, Name: "cpu", Value: 10, Timestamp: base.Add(10 * time.Minute)},
		{OrgID: f.OrgA, CIID: own, Name: "cpu", Value: 20, Timestamp: base.Add(20 * time.Minute)},
		{OrgID: f.OrgA, CIID: own, Name: "cpu", Value: 40, Timestamp: base.Add(70 * time.Minute)},
		{OrgID: f.OrgA, CIID: foreign, Name: "cpu", Value: 99, Timestamp: base.Add(15 * time.Minute)},
	}
	if err := store.Ingest(f.OrgCtx(f.OrgA), samples); err != nil {
		t.Fatalf("ingest org A: %v", err)
	}
	if err := store.Ingest(f.OrgCtx(f.OrgB), []monitoring.Metric{{OrgID: f.OrgB, CIID: other, Name: "cpu", Value: 77, Timestamp: base.Add(5 * time.Minute)}}); err != nil {
		t.Fatalf("ingest org B: %v", err)
	}

	raw := func(ctx context.Context, orgID, ciID string) []float64 {
		t.Helper()
		points, err := store.Query(ctx, monitoring.MetricQuery{OrgID: orgID, CIID: ciID, Name: "cpu", From: base, To: base.Add(2 * time.Hour)})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		out := make([]float64, 0, len(points))
		for _, p := range points {
			out = append(out, p.Value)
		}
		return out
	}
	if got := raw(f.OrgCtx(f.OrgA), f.OrgA, ""); len(got) != 4 {
		t.Errorf("org A raw samples: %v, want 4", got)
	}
	if got := raw(f.ClientCtx(f.Client1), f.OrgA, ""); len(got) != 3 {
		t.Errorf("client 1 raw samples: %v, want the 3 of its CI", got)
	}
	if got := raw(f.OrgCtx(f.OrgB), f.OrgB, ""); len(got) != 1 || got[0] != 77 {
		t.Errorf("org B raw samples: %v, want only its own", got)
	}
	if got := raw(f.OrgCtx(f.OrgB), f.OrgB, own); len(got) != 0 {
		t.Errorf("org B reads samples of an org A CI: %v", got)
	}

	// The application role has no direct privilege; the view rejects rows
	// of another organization.
	orgA := database.OrgWideScope(f.OrgA, f.User)
	for _, c := range []struct{ name, sql string }{
		{"select hypertable", `SELECT count(*) FROM metric_sample`},
		{"select aggregate", `SELECT count(*) FROM metric_sample_1h`},
		{"delete through view", `DELETE FROM metric_sample_v`},
	} {
		err := database.WithTenant(bg, f.App, &orgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, c.sql)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s as application role: %v, want permission denied", c.name, err)
		}
	}
	err := database.WithTenant(bg, f.App, &orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO metric_sample_v (time, organization_id, ci_id, metric_name, value) VALUES (now(), $1, $2, 'cpu', 1)`, f.OrgB, other)
		return err
	})
	if err == nil {
		t.Error("insert of an org B sample through the org A view was accepted")
	}

	// Hourly steps read metric_sample_1h: hour 1 averages 15 over two
	// samples, hour 2 is 40; the two-hour bucket weights by sample count.
	if _, err = f.Admin.Exec(bg, `CALL refresh_continuous_aggregate('metric_sample_1h', $1::timestamptz, $2::timestamptz)`,
		base.Add(-time.Hour), base.Add(3*time.Hour)); err != nil {
		t.Fatalf("refresh aggregate: %v", err)
	}
	hourly, err := store.Query(f.OrgCtx(f.OrgA), monitoring.MetricQuery{OrgID: f.OrgA, CIID: own, Name: "cpu", From: base, To: base.Add(2 * time.Hour), Step: time.Hour})
	if err != nil {
		t.Fatalf("hourly query: %v", err)
	}
	if len(hourly) != 2 || hourly[0].Value != 15 || hourly[1].Value != 40 {
		t.Errorf("hourly points %+v, want 15 and 40", hourly)
	}
	twoHours, err := store.Query(f.OrgCtx(f.OrgA), monitoring.MetricQuery{OrgID: f.OrgA, CIID: own, Name: "cpu", From: base, To: base.Add(2 * time.Hour), Step: 2 * time.Hour})
	if err != nil {
		t.Fatalf("two-hour query: %v", err)
	}
	if len(twoHours) != 1 || math.Abs(twoHours[0].Value-70.0/3.0) > 1e-9 {
		t.Errorf("two-hour bucket %+v, want the weighted average %.4f", twoHours, 70.0/3.0)
	}
	if points, qErr := store.Query(f.OrgCtx(f.OrgB), monitoring.MetricQuery{OrgID: f.OrgB, CIID: own, Name: "cpu", From: base, To: base.Add(2 * time.Hour), Step: time.Hour}); qErr != nil || len(points) != 0 {
		t.Errorf("org B reads the org A aggregate: %+v, %v", points, qErr)
	}

	// New chunks span one day (MON-01).
	var interval time.Duration
	if err = f.Admin.QueryRow(bg, `SELECT extract(epoch FROM time_interval)::bigint * 1000000000 FROM timescaledb_information.dimensions WHERE hypertable_name = 'metric_sample'`).Scan(&interval); err != nil {
		t.Fatalf("read chunk interval: %v", err)
	}
	if interval != 24*time.Hour {
		t.Errorf("chunk interval %s, want 24h", interval)
	}
}
