package monitoring

// TEC-06 spike (WP-039): can metric_sample get RLS + FORCE RLS while keeping
// compression and the continuous aggregate (MON-01)? The tests run against the
// TimescaleDB of TEST_DATABASE_URL in their own schema and pin the result that
// docs/decisions/0001-timescale-rls.md records:
//
//   - TimescaleDB refuses RLS together with compression and continuous
//     aggregates in both orders (TestTimescaleRefusesRLSWithCompression).
//   - Security-barrier views with an org predicate isolate raw samples
//     including compressed chunks and the continuous aggregate, and reject
//     foreign rows on write (TestSecurityBarrierViewsIsolateMetrics).
//
// If a TimescaleDB upgrade lifts the restrictions, the first test fails and
// the ADR has to be revisited.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

const (
	spikeSchema = "tec06_spike"
	spikeOrgA   = "7ec06000-0000-4000-8000-00000000000a"
	spikeOrgB   = "7ec06000-0000-4000-8000-00000000000b"
)

var errSpikeRollback = errors.New("rollback")

func spikeAdmin(t *testing.T) (context.Context, string, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)
	drop := func() {
		if _, dropErr := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+spikeSchema+` CASCADE`); dropErr != nil {
			t.Errorf("drop spike schema: %v", dropErr)
		}
	}
	drop()
	t.Cleanup(drop)
	mustExec(ctx, t, admin,
		`CREATE SCHEMA `+spikeSchema,
		`CREATE TABLE `+spikeSchema+`.m (
			time TIMESTAMPTZ NOT NULL, organization_id UUID NOT NULL, ci_id UUID NOT NULL,
			metric_name TEXT NOT NULL, value DOUBLE PRECISION NOT NULL)`,
		`SELECT create_hypertable('`+spikeSchema+`.m', 'time', chunk_time_interval => INTERVAL '1 day')`,
	)
	return ctx, dsn, admin
}

func mustExec(ctx context.Context, t *testing.T, pool *pgxpool.Pool, statements ...string) {
	t.Helper()
	for _, sql := range statements {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
}

func expectError(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sql, contains string) {
	t.Helper()
	_, err := pool.Exec(ctx, sql)
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("%s: got error %v, want %q", sql, err, contains)
	}
}

func TestTimescaleRefusesRLSWithCompression(t *testing.T) {
	ctx, _, admin := spikeAdmin(t)
	m := spikeSchema + ".m"

	// RLS first: compression and continuous aggregates are refused.
	mustExec(ctx, t, admin,
		`ALTER TABLE `+m+` ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE `+m+` FORCE ROW LEVEL SECURITY`,
	)
	expectError(ctx, t, admin,
		`ALTER TABLE `+m+` SET (timescaledb.compress, timescaledb.compress_segmentby = 'organization_id')`,
		"compression cannot be used on table with row security")
	expectError(ctx, t, admin,
		`CREATE MATERIALIZED VIEW `+spikeSchema+`.m_1h WITH (timescaledb.continuous) AS
		 SELECT time_bucket('1 hour', time) AS bucket, organization_id, avg(value) AS avg_value
		 FROM `+m+` GROUP BY 1, 2 WITH NO DATA`,
		"cannot create continuous aggregate on hypertable with row security")

	// Compression first: RLS can no longer be enabled.
	mustExec(ctx, t, admin,
		`ALTER TABLE `+m+` NO FORCE ROW LEVEL SECURITY`,
		`ALTER TABLE `+m+` DISABLE ROW LEVEL SECURITY`,
		`ALTER TABLE `+m+` SET (timescaledb.compress, timescaledb.compress_segmentby = 'organization_id')`,
	)
	expectError(ctx, t, admin, `ALTER TABLE `+m+` ENABLE ROW LEVEL SECURITY`,
		"operation not supported on hypertables that have compression enabled")
}

func TestSecurityBarrierViewsIsolateMetrics(t *testing.T) {
	ctx, dsn, admin := spikeAdmin(t)
	m := spikeSchema + ".m"
	mustExec(ctx, t, admin,
		`ALTER TABLE `+m+` SET (timescaledb.compress, timescaledb.compress_segmentby = 'organization_id, ci_id, metric_name')`,
		// 20 days of hourly samples for two organizations.
		`INSERT INTO `+m+` SELECT now() - make_interval(hours => i),
			CASE WHEN i % 2 = 0 THEN '`+spikeOrgA+`'::uuid ELSE '`+spikeOrgB+`'::uuid END,
			gen_random_uuid(), 'cpu', i FROM generate_series(1, 480) AS i`,
		`SELECT compress_chunk(c) FROM show_chunks('`+m+`', older_than => INTERVAL '7 days') AS c`,
		`CREATE MATERIALIZED VIEW `+spikeSchema+`.m_1h WITH (timescaledb.continuous) AS
		 SELECT time_bucket('1 hour', time) AS bucket, organization_id, avg(value) AS avg_value
		 FROM `+m+` GROUP BY 1, 2 WITH NO DATA`,
		`CALL refresh_continuous_aggregate('`+spikeSchema+`.m_1h', NULL, NULL)`,
		// The org predicate is an InitPlan (sub-select), so it is evaluated
		// once and chunk/segment pruning works like a constant filter.
		`CREATE VIEW `+spikeSchema+`.m_v WITH (security_barrier) AS
		 SELECT * FROM `+m+` WHERE organization_id = (SELECT current_setting('app.org_id')::uuid)
		 WITH CASCADED CHECK OPTION`,
		`CREATE VIEW `+spikeSchema+`.m_1h_v WITH (security_barrier) AS
		 SELECT * FROM `+spikeSchema+`.m_1h WHERE organization_id = (SELECT current_setting('app.org_id')::uuid)`,
		`GRANT USAGE ON SCHEMA `+spikeSchema+` TO reticora_app`,
		`GRANT SELECT, INSERT ON `+spikeSchema+`.m_v TO reticora_app`,
		`GRANT SELECT ON `+spikeSchema+`.m_1h_v TO reticora_app`,
		// A cheap user function for the leak check below. The application
		// role may not create functions itself (only index DDL, migration
		// 000078), so it is prepared here and only called by that role.
		`CREATE FUNCTION `+spikeSchema+`.guard(org uuid) RETURNS boolean
			LANGUAGE plpgsql COST 0.0000001 AS $$
			BEGIN
				IF org <> '`+spikeOrgA+`'::uuid THEN RAISE EXCEPTION 'leaked %', org; END IF;
				RETURN true;
			END $$`,
		`GRANT EXECUTE ON FUNCTION `+spikeSchema+`.guard(uuid) TO reticora_app`,
	)
	var compressed int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM timescaledb_information.chunks
		WHERE hypertable_schema = $1 AND hypertable_name = 'm' AND is_compressed`, spikeSchema).Scan(&compressed); err != nil || compressed == 0 {
		t.Fatalf("spike needs compressed chunks, got %d (%v)", compressed, err)
	}

	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(pool.Close)
	scope := database.OrgWideScope(spikeOrgA, "")
	inTenant := func(fn func(context.Context, pgx.Tx) error) error {
		return database.WithTenant(ctx, pool, &scope, fn)
	}
	type counts struct{ rows, orgs int }
	query := func(t *testing.T, sql string) counts {
		t.Helper()
		var c counts
		start := time.Now()
		if err := inTenant(func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, sql).Scan(&c.rows, &c.orgs)
		}); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		t.Logf("%s: %d rows in %s", sql, c.rows, time.Since(start))
		return c
	}

	t.Run("raw samples including compressed chunks", func(t *testing.T) {
		if c := query(t, `SELECT count(*), count(DISTINCT organization_id) FROM `+spikeSchema+`.m_v`); c.rows != 240 || c.orgs != 1 {
			t.Fatalf("view returns %+v, want 240 rows of one organization", c)
		}
		if c := query(t, `SELECT count(*), count(DISTINCT organization_id) FROM `+spikeSchema+`.m_v
			WHERE time < now() - INTERVAL '8 days'`); c.rows == 0 || c.orgs != 1 {
			t.Fatalf("compressed range returns %+v, want rows of one organization", c)
		}
	})
	t.Run("continuous aggregate", func(t *testing.T) {
		if c := query(t, `SELECT count(*), count(DISTINCT organization_id) FROM `+spikeSchema+`.m_1h_v`); c.rows == 0 || c.orgs != 1 {
			t.Fatalf("aggregate view returns %+v, want rows of one organization", c)
		}
	})
	t.Run("no direct access", func(t *testing.T) {
		for _, rel := range []string{"m", "m_1h"} {
			err := inTenant(func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `SELECT 1 FROM `+spikeSchema+`.`+rel+` LIMIT 1`)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("direct access to %s: %v, want permission denied", rel, err)
			}
		}
	})
	t.Run("leaky function sees no foreign rows", func(t *testing.T) {
		// A cheap user function in the WHERE clause runs after the barrier
		// predicate; otherwise it would raise on a row of organization B.
		err := inTenant(func(ctx context.Context, tx pgx.Tx) error {
			var n int
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+spikeSchema+`.m_v WHERE `+spikeSchema+`.guard(organization_id)`).Scan(&n)
		})
		if err != nil {
			t.Fatalf("security barrier leaked: %v", err)
		}
	})
	t.Run("writes through the view", func(t *testing.T) {
		insert := func(org string, age time.Duration) error {
			return inTenant(func(ctx context.Context, tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO `+spikeSchema+`.m_v VALUES (now() - $2::interval, $1, gen_random_uuid(), 'cpu', 1)`,
					org, age.String()); err != nil {
					return err
				}
				return errSpikeRollback
			})
		}
		if err := insert(spikeOrgB, 0); err == nil || errors.Is(err, errSpikeRollback) {
			t.Fatalf("insert of a foreign organization must violate the check option, got %v", err)
		}
		for _, age := range []time.Duration{0, 10 * 24 * time.Hour} {
			if err := insert(spikeOrgA, age); !errors.Is(err, errSpikeRollback) {
				t.Fatalf("own insert (age %s): %v", age, err)
			}
		}
	})
}
