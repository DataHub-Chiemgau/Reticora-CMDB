package database_test

// WP-985 (ALR-05, MET-45, AK4/AK6): migration alert_rule_ast (000348) moves
// every stored rule of the old form (condition, threshold, severity) into
// the stage of its severity as {"op": condition, "field": "value", "value":
// threshold}, the same stage the application builds (monitoring.
// LegacyCondition via normalizeRule). Its down moves the rules the old
// columns cannot express (several stages, a clear condition) to
// migration_quarantine and restores NOT NULL. This test migrates a database
// of its own to the version before 000348, stores old rules of every
// condition and severity, applies 000348 and compares every stage with the
// application's; then it adds a rule with several stages and one with a
// clear condition, applies the down and expects both quarantined and the old
// rules unchanged. The shared database of TEST_DATABASE_URL is not changed;
// the scratch database is dropped.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestMigration000860LegacyRules' ./internal/database/

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
)

func TestMigration000860LegacyRules(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	files := loadMigrations(t)
	version := 0
	for _, f := range files {
		if f.name == "alert_rule_ast" {
			version = f.version
		}
	}
	if version == 0 {
		t.Fatal("migration alert_rule_ast not found")
	}
	conn := scratchDatabase(ctx, t, dsn, "alertast860")
	m := &migrator{t: t, conn: conn, files: files}
	m.upTo(ctx, version-1)

	const org = "c0de0860-0000-4000-8000-00000000000a"
	execAll(ctx, t, conn,
		`INSERT INTO organization (id, name, slug) VALUES ('`+org+`', 'alert-ast-860', 'alert-ast-860')`)

	type legacy struct {
		id, condition, severity string
		threshold               float64
	}
	var rules []legacy
	thresholds := []float64{90.25, 1e9, -3}
	n := 0
	for _, condition := range []string{"gt", "lt", "eq"} {
		for i, severity := range []string{"critical", "warning", "info"} {
			n++
			r := legacy{id: fmt.Sprintf("c0de0860-0000-4000-8000-%012d", n), condition: condition,
				severity: severity, threshold: thresholds[i]}
			rules = append(rules, r)
			if _, err := conn.Exec(ctx, `INSERT INTO alert_rule (id, organization_id, name, metric_name, condition, threshold, severity)
				VALUES ($1, $2, $3, 'cpu', $4, $5, $6)`, r.id, org, "legacy "+r.id, r.condition, r.threshold, r.severity); err != nil {
				t.Fatalf("store old rule %s/%s: %v", condition, severity, err)
			}
		}
	}

	m.up(ctx)

	stageColumn := map[string]string{"critical": "critical_condition", "warning": "warning_condition", "info": "info_condition"}
	checkLegacy := func(when string) {
		t.Helper()
		for _, r := range rules {
			want := string(monitoring.LegacyCondition(r.condition, r.threshold))
			var (
				condition, severity string
				threshold           float64
				matches             bool
				others              int
			)
			err := conn.QueryRow(ctx, `SELECT condition, threshold, severity,
					`+stageColumn[r.severity]+` = $2::jsonb,
					(critical_condition IS NOT NULL)::int + (warning_condition IS NOT NULL)::int + (info_condition IS NOT NULL)::int
				FROM alert_rule WHERE id = $1`, r.id, want).Scan(&condition, &threshold, &severity, &matches, &others)
			if err != nil {
				t.Fatalf("%s: read rule %s: %v", when, r.id, err)
			}
			if !matches {
				t.Errorf("%s: rule %s/%s/%v: %s differs from the application's stage %s", when, r.condition, r.severity, r.threshold, stageColumn[r.severity], want)
			}
			if others != 1 {
				t.Errorf("%s: rule %s/%s: %d stages set, want exactly the one of its severity", when, r.condition, r.severity, others)
			}
			if condition != r.condition || threshold != r.threshold || severity != r.severity {
				t.Errorf("%s: rule %s: old form %s/%v/%s, want %s/%v/%s unchanged", when, r.id,
					condition, threshold, severity, r.condition, r.threshold, r.severity)
			}
		}
	}
	checkLegacy("after up")

	const (
		multi = "c0de0860-0000-4000-8000-0000000001a1"
		clear = "c0de0860-0000-4000-8000-0000000001a2"
	)
	execAll(ctx, t, conn,
		`INSERT INTO alert_rule (id, organization_id, name, metric_name, critical_condition, warning_condition)
		 VALUES ('`+multi+`', '`+org+`', 'stages', 'cpu', '{"op":"gt","field":"value","value":95}', '{"op":"gt","field":"value","value":80}')`,
		`INSERT INTO alert_rule (id, organization_id, name, metric_name, critical_condition, clear_condition)
		 VALUES ('`+clear+`', '`+org+`', 'hysteresis', 'cpu', '{"op":"gt","field":"value","value":90}', '{"op":"lt","field":"value","value":85}')`)

	m.down(ctx)

	var quarantined int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM migration_quarantine
		WHERE migration = '000348' AND source_table = 'alert_rule' AND row_data->>'id' IN ($1, $2)`, multi, clear).Scan(&quarantined); err != nil {
		t.Fatal(err)
	}
	if quarantined != 2 {
		t.Errorf("down: %d rules quarantined, want the rule with stages and the one with a clear condition", quarantined)
	}
	var left, kept int
	if err := conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE id IN ($1, $2)), count(*) FROM alert_rule`, multi, clear).Scan(&left, &kept); err != nil {
		t.Fatal(err)
	}
	if left != 0 || kept != len(rules) {
		t.Errorf("down: %d quarantined rules left, %d rules kept; want 0 and %d", left, kept, len(rules))
	}
	var nullable int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_attribute
		WHERE attrelid = 'alert_rule'::regclass AND attname IN ('condition', 'threshold', 'severity') AND NOT attnotnull`).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != 0 {
		t.Errorf("down: %d of condition, threshold, severity nullable, want NOT NULL again", nullable)
	}

	// Up again: the old rules are migrated as the first time.
	m.up(ctx)
	checkLegacy("after down and up")
}
