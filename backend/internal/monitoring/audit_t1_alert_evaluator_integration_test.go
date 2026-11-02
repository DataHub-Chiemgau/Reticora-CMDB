package monitoring_test

// Phase-1 acceptance audit, group T1-tenancy: REGRESSION test (passes).
//
// WP-022 (MON-04, TEN-06; AC1 "Systemworker laufen mandantenweise", AC2
// "app.system nur lesend und nur für die Allowlist") gap "The alert
// evaluator's per-org path on PostgreSQL is only probe-proven; the committed
// tests do not cover ListEnabled under app.system or MarkPending/MarkFired
// per org across two orgs".
//
// Two organizations each have an enabled rule (gt 50, duration 0) and a
// sample of 99. The evaluator on PostgreSQL lists the rules under
// app.system, evaluates each in its organization's tenant transaction and
// fires each rule exactly once (second run: no new event), without worker
// errors. The system flag only reads: a write under app.system fails or
// changes no row.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1AlertEvaluatorPerOrganization' ./internal/monitoring/

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestAuditT1AlertEvaluatorPerOrganization(t *testing.T) {
	f := scopetest.Seed(t, "ef")
	bg := context.Background()
	t.Cleanup(func() {
		// metric_sample has no foreign key to the organization.
		if _, err := f.Admin.Exec(bg, `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`, []string{f.OrgA, f.OrgB}); err != nil {
			t.Errorf("cleanup metric samples: %v", err)
		}
	})
	store := monitoring.NewPGMetricStore(f.App)
	rules := map[string]string{}
	for _, org := range []string{f.OrgA, f.OrgB} {
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO alert_rule (organization_id, name, metric_name, condition, threshold, duration)
			VALUES ($1, 't1-alert', 't1_alert_metric', 'gt', 50, '0 seconds') RETURNING id::text`, org).Scan(&id); err != nil {
			t.Fatalf("seed rule: %v", err)
		}
		rules[org] = id
		ciID := f.CI(t, org, "", "t1-alert-ci-"+org[len(org)-2:])
		if err := store.Ingest(f.OrgCtx(org), []monitoring.Metric{{OrgID: org, CIID: ciID, Name: "t1_alert_metric", Value: 99,
			Timestamp: time.Now().UTC().Add(-time.Second)}}); err != nil {
			t.Fatalf("ingest %s: %v", org, err)
		}
	}

	var mu sync.Mutex
	events := map[string]int{}
	ev := monitoring.NewEvaluator(store, store.AlertStore(), monitoring.NotifierFunc(func(_ context.Context, e monitoring.AlertEvent) {
		mu.Lock()
		defer mu.Unlock()
		if rules[e.Rule.OrgID] == e.Rule.ID {
			events[e.Rule.OrgID]++
		}
	}))
	errorsBefore := testutil.ToFloat64(observability.WorkerErrors.WithLabelValues("monitoring"))
	ev.Evaluate(bg)
	ev.Evaluate(bg)
	if after := testutil.ToFloat64(observability.WorkerErrors.WithLabelValues("monitoring")); after != errorsBefore {
		t.Errorf("worker_errors_total{monitoring} rose from %v to %v during the evaluation", errorsBefore, after)
	}
	for org, id := range rules {
		var fired *time.Time
		if err := f.Admin.QueryRow(bg, `SELECT last_fired_at FROM alert_rule WHERE id = $1`, id).Scan(&fired); err != nil {
			t.Fatal(err)
		}
		if fired == nil {
			t.Errorf("rule of organization %s: last_fired_at not set by the evaluator (MarkFired in the organization's transaction)", org)
		}
		mu.Lock()
		n := events[org]
		mu.Unlock()
		if n != 1 {
			t.Errorf("rule of organization %s fired %d times over two runs, want exactly once", org, n)
		}
	}

	// app.system only reads.
	t.Run("WithSystem cannot write", func(t *testing.T) {
		err := database.WithSystem(bg, f.App, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE alert_rule SET name = 't1-alert-system' WHERE id = $1`, rules[f.OrgA])
			return err
		})
		if err == nil {
			t.Errorf("UPDATE alert_rule inside WithSystem succeeded, want a refused write")
		}
	})
	t.Run("the system flag grants no write in a read-write transaction", func(t *testing.T) {
		var affected int64
		tx, err := f.App.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if _, err = tx.Exec(bg, `SELECT set_config('app.system', 'on', true), set_config('app.org_id', '', true)`); err != nil {
			t.Fatal(err)
		}
		var visible int
		if err = tx.QueryRow(bg, `SELECT count(*) FROM alert_rule WHERE id = ANY($1::uuid[])`, []string{rules[f.OrgA], rules[f.OrgB]}).Scan(&visible); err != nil {
			t.Fatal(err)
		}
		if visible != 2 {
			t.Errorf("app.system reads %d of the two rules, want 2 (ListEnabled needs the system-wide view)", visible)
		}
		tag, err := tx.Exec(bg, `UPDATE alert_rule SET name = 't1-alert-system' WHERE id = ANY($1::uuid[])`, []string{rules[f.OrgA], rules[f.OrgB]})
		if err == nil {
			affected = tag.RowsAffected()
		}
		if affected != 0 {
			t.Errorf("UPDATE alert_rule under app.system changed %d rows, want 0", affected)
		}
	})
	if got := func() string {
		var name string
		_ = f.Admin.QueryRow(bg, `SELECT name FROM alert_rule WHERE id = $1`, rules[f.OrgA]).Scan(&name)
		return name
	}(); got != "t1-alert" {
		t.Errorf("rule name after the system writes: %q, want unchanged", got)
	}
}
