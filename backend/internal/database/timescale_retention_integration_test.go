package database_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

// metricRollupRetentionDown is the down migration of the rollup retention
// (WP-548), seen from this package.
const metricRollupRetentionDown = "000174_metric_rollup_retention.down.sql"

// TestMetricRollupRetention covers NFR-06 for the hourly metric rollups
// (WP-548, F-317): the continuous aggregate metric_sample_1h has a
// TimescaleDB retention job on its materialization hypertable that drops
// buckets after 400 days, like the raw samples (metric_sample, migration
// 000020); the down migration removes exactly that job again and keeps the
// retention of the raw samples.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL) at the latest
// migration.
func TestMetricRollupRetention(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var matID int
	if err = conn.QueryRow(ctx, `SELECT mat_hypertable_id FROM _timescaledb_catalog.continuous_agg
		WHERE user_view_schema = 'public' AND user_view_name = 'metric_sample_1h'`).Scan(&matID); err != nil {
		t.Fatalf("materialization hypertable of metric_sample_1h: %v", err)
	}
	// retentionJobs lists the drop_after of every retention job on the
	// hypertable with id hypertableID.
	retentionJobs := func(q interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	}, hypertableID int) []string {
		t.Helper()
		rows, qerr := q.Query(ctx, `SELECT config->>'drop_after' FROM timescaledb_information.jobs
			WHERE proc_name = 'policy_retention' AND (config->>'hypertable_id')::int = $1 ORDER BY job_id`, hypertableID)
		if qerr != nil {
			t.Fatal(qerr)
		}
		out, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil {
			t.Fatal(qerr)
		}
		return out
	}
	var rawID int
	if err = conn.QueryRow(ctx, `SELECT id FROM _timescaledb_catalog.hypertable
		WHERE schema_name = 'public' AND table_name = 'metric_sample'`).Scan(&rawID); err != nil {
		t.Fatalf("hypertable metric_sample: %v", err)
	}

	if got := retentionJobs(conn, matID); len(got) != 1 || got[0] != "400 days" {
		t.Fatalf("retention jobs of metric_sample_1h (hypertable %d): %v, want one with drop_after 400 days", matID, got)
	}
	// The job is the policy of the aggregate, scheduled and owned like the
	// other policies, not a disabled leftover.
	var scheduled bool
	var hypertableName string
	if err = conn.QueryRow(ctx, `SELECT scheduled, hypertable_name FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_retention' AND (config->>'hypertable_id')::int = $1`, matID).Scan(&scheduled, &hypertableName); err != nil {
		t.Fatal(err)
	}
	if !scheduled {
		t.Error("the retention job of metric_sample_1h is not scheduled")
	}
	t.Logf("retention job of metric_sample_1h on hypertable %s (id %d)", hypertableName, matID)
	if got := retentionJobs(conn, rawID); len(got) != 1 || got[0] != "400 days" {
		t.Errorf("retention jobs of metric_sample: %v, want one with drop_after 400 days", got)
	}

	// Down removes the policy of the aggregate, not the one of the raw
	// samples. TimescaleDB keeps its jobs in catalog tables, so the down
	// file runs in a transaction that is rolled back afterwards.
	down, err := os.ReadFile(filepath.Join(migrationsDir, metricRollupRetentionDown))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down %s: %v", metricRollupRetentionDown, err)
	}
	if got := retentionJobs(tx, matID); len(got) != 0 {
		t.Errorf("retention jobs of metric_sample_1h after down: %v, want none", got)
	}
	if got := retentionJobs(tx, rawID); len(got) != 1 || got[0] != "400 days" {
		t.Errorf("retention jobs of metric_sample after down: %v, want the one of migration 000020", got)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if got := retentionJobs(conn, matID); len(got) != 1 {
		t.Errorf("retention jobs of metric_sample_1h after the rollback: %v", got)
	}
}
