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
// opens exactly one alarm per organization (second run: no new alarm,
// WP-984), without worker errors. The system flag only reads: a write under
// app.system fails or changes no row.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1AlertEvaluatorPerOrganization' ./internal/monitoring/

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
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
		if err := f.Admin.QueryRow(bg, `INSERT INTO alert_rule (organization_id, name, metric_name, condition, threshold, severity, duration,
			                        warning_condition)
			VALUES ($1, 't1-alert', 't1_alert_metric', 'gt', 50, 'warning', '0 seconds',
			        '{"op":"gt","field":"value","value":50}') RETURNING id::text`, org).Scan(&id); err != nil {
			t.Fatalf("seed rule: %v", err)
		}
		rules[org] = id
		ciID := f.CI(t, org, "", "t1-alert-ci-"+org[len(org)-2:])
		if err := store.Ingest(f.OrgCtx(org), []monitoring.Metric{{OrgID: org, CIID: ciID, Name: "t1_alert_metric", Value: 99,
			Timestamp: time.Now().UTC().Add(-time.Second)}}); err != nil {
			t.Fatalf("ingest %s: %v", org, err)
		}
	}

	// The state of a series is its alarm (WP-984): each organization gets
	// exactly one open alarm, and the second run adds none.
	ev := monitoring.NewEvaluator(store, store.AlertStore(), alarm.NewRuleSink(alarm.NewPGService(f.App)))
	errorsBefore := testutil.ToFloat64(observability.WorkerErrors.WithLabelValues("monitoring"))
	ev.Evaluate(bg)
	ev.Evaluate(bg)
	if after := testutil.ToFloat64(observability.WorkerErrors.WithLabelValues("monitoring")); after != errorsBefore {
		t.Errorf("worker_errors_total{monitoring} rose from %v to %v during the evaluation", errorsBefore, after)
	}
	for org, id := range rules {
		var open, all int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FILTER (WHERE state = 'open'), count(*) FROM alarm
			WHERE organization_id = $1 AND source_kind = 'rule' AND source_id = $2`, org, id).Scan(&open, &all); err != nil {
			t.Fatal(err)
		}
		if open != 1 || all != 1 {
			t.Errorf("rule of organization %s: %d open of %d alarms over two runs, want exactly one (written in the organization's transaction)",
				org, open, all)
		}
		var opened int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM alarm_transition tr JOIN alarm a ON a.id = tr.alarm_id
			WHERE a.organization_id = $1 AND a.source_id = $2 AND tr.to_state = 'open'`, org, id).Scan(&opened); err != nil {
			t.Fatal(err)
		}
		if opened != 1 {
			t.Errorf("rule of organization %s opened %d times, want once", org, opened)
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
