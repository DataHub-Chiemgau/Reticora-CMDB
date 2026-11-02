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
//   - The production views metric_sample_v and metric_sample_1h_v add the
//     client and site scope as a sub-select over ci (ADR point 4, migration
//     000143): client- and site-scoped principals read only samples of
//     visible CIs from both views and cannot insert for another CI
//     (TestSecurityBarrierViewsIsolateMetrics, "client and site scope").
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

const (
	spikeSchema = "tec06_spike"
	spikeOrgA   = "7ec06000-0000-4000-8000-00000000000a"
	spikeOrgB   = "7ec06000-0000-4000-8000-00000000000b"
)

var errSpikeRollback = errors.New("rollback")

func spikeAdmin(t *testing.T) (context.Context, *pgxpool.Pool) {
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
	return ctx, admin
}

func mustExec(ctx context.Context, t *testing.T, pool *pgxpool.Pool, statements ...string) {
	t.Helper()
	for _, sql := range statements {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
}

// expectRefused: sql fails with SQLSTATE 0A000 (feature_not_supported) and
// one of the messages. TimescaleDB 2.18 renamed compression to columnstore
// in its messages; the database image moved from 2.17.2 to 2.31.0 (E-96,
// WP-389), so both wordings of the same refusal are accepted.
func expectRefused(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sql string, messages ...string) {
	t.Helper()
	_, err := pool.Exec(ctx, sql)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "0A000" {
		t.Fatalf("%s: got error %v, want SQLSTATE 0A000 with one of %q", sql, err, messages)
	}
	for _, m := range messages {
		if pgErr.Message == m {
			return
		}
	}
	t.Fatalf("%s: got %q, want one of %q", sql, pgErr.Message, messages)
}

func TestTimescaleRefusesRLSWithCompression(t *testing.T) {
	ctx, admin := spikeAdmin(t)
	m := spikeSchema + ".m"

	// RLS first: compression and continuous aggregates are refused.
	mustExec(ctx, t, admin,
		`ALTER TABLE `+m+` ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE `+m+` FORCE ROW LEVEL SECURITY`,
	)
	expectRefused(ctx, t, admin,
		`ALTER TABLE `+m+` SET (timescaledb.compress, timescaledb.compress_segmentby = 'organization_id')`,
		"compression cannot be used on table with row security",
		"columnstore cannot be used on table with row security")
	expectRefused(ctx, t, admin,
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
	expectRefused(ctx, t, admin, `ALTER TABLE `+m+` ENABLE ROW LEVEL SECURITY`,
		"operation not supported on hypertables that have compression enabled",
		"operation not supported on hypertables that have columnstore enabled")
}

func TestSecurityBarrierViewsIsolateMetrics(t *testing.T) {
	ctx, admin := spikeAdmin(t)
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

	pool, err := database.NewPool(ctx, scopetest.AppDSN(t))
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
	t.Run("client and site scope", func(t *testing.T) {
		spikeScopedViews(ctx, t, admin)
	})
}

// spikeScopedViews checks ADR point 4 on the production views of migration
// 000143: CIs of client 1 at site 1 (own), of client 1 at a second site
// (sibling), of client 2 (foreign) and without client (org), one sample each
// two days back, the hourly aggregate refreshed for that hour.
func spikeScopedViews(ctx context.Context, t *testing.T, admin *pgxpool.Pool) {
	f := scopetest.Seed(t, "6c")
	t.Cleanup(func() {
		// metric_sample has no foreign key to the organization.
		if _, err := admin.Exec(ctx, `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`, []string{f.OrgA, f.OrgB}); err != nil {
			t.Errorf("cleanup metric samples: %v", err)
		}
	})
	site := func(client, name string) string {
		t.Helper()
		var id string
		if err := admin.QueryRow(ctx, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`,
			f.Room(t, client, name)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ci := func(client, siteID, name string) string {
		t.Helper()
		id := f.CI(t, f.OrgA, client, name)
		if siteID != "" {
			mustExec(ctx, t, admin, `UPDATE ci SET location_id = '`+siteID+`' WHERE id = '`+id+`'`)
		}
		return id
	}
	site1, site1b, site2 := site(f.Client1, "spike-s1"), site(f.Client1, "spike-s1b"), site(f.Client2, "spike-s2")
	own, sibling := ci(f.Client1, site1, "spike-own"), ci(f.Client1, site1b, "spike-sibling")
	foreign, orgCI := ci(f.Client2, site2, "spike-foreign"), ci("", "", "spike-org")
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	for i, id := range []string{own, sibling, foreign, orgCI} {
		if _, err := admin.Exec(ctx, `INSERT INTO metric_sample (time, organization_id, ci_id, metric_name, value) VALUES ($1, $2, $3, 'spike_cpu', $4)`,
			at.Add(time.Duration(i)*time.Minute), f.OrgA, id, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `CALL refresh_continuous_aggregate('metric_sample_1h', $1::timestamptz, $2::timestamptz)`,
		at.Add(-time.Hour), at.Add(2*time.Hour)); err != nil {
		t.Fatalf("refresh aggregate: %v", err)
	}
	// A cheap function that raises on the foreign CI: the barrier must
	// evaluate the scope predicate before it.
	mustExec(ctx, t, admin,
		`CREATE FUNCTION `+spikeSchema+`.guard_ci(ci uuid) RETURNS boolean
			LANGUAGE plpgsql COST 0.0000001 AS $$
			BEGIN
				IF ci = '`+foreign+`'::uuid THEN RAISE EXCEPTION 'leaked %', ci; END IF;
				RETURN true;
			END $$`,
		`GRANT EXECUTE ON FUNCTION `+spikeSchema+`.guard_ci(uuid) TO reticora_app`)

	clientScope := database.OrgWideScope(f.OrgA, f.User)
	clientScope.Clients = database.ScopeIDs(f.Client1)
	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(site1)
	orgScope := database.OrgWideScope(f.OrgA, f.User)
	ids := []string{own, sibling, foreign, orgCI}
	for _, p := range []struct {
		name    string
		scope   *database.TenantScope
		visible map[string]bool
	}{
		{"org", &orgScope, map[string]bool{own: true, sibling: true, foreign: true, orgCI: true}},
		// TEN-05: a CI without client (or site) is org-wide and visible.
		{"client1", &clientScope, map[string]bool{own: true, sibling: true, orgCI: true}},
		{"site1", &siteScope, map[string]bool{own: true, orgCI: true}},
	} {
		for _, view := range []string{"metric_sample_v", "metric_sample_1h_v"} {
			// The org-wide principal sees the foreign CI, so only the scoped
			// ones call the raising function.
			guard := "true"
			if !p.visible[foreign] {
				guard = spikeSchema + ".guard_ci(ci_id)"
			}
			var got []string
			err := database.WithTenant(ctx, f.App, p.scope, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT COALESCE(array_agg(ci_id::text ORDER BY ci_id), '{}')
					FROM `+view+` WHERE metric_name = 'spike_cpu' AND `+guard).Scan(&got)
			})
			if err != nil {
				t.Fatalf("%s %s: %v", p.name, view, err)
			}
			seen := map[string]bool{}
			for _, id := range got {
				seen[id] = true
			}
			for _, id := range ids {
				if seen[id] != p.visible[id] {
					t.Errorf("TEC-06/TEN-05: %s %s: CI %s visible %v, want %v", p.name, view, id, seen[id], p.visible[id])
				}
			}
		}
	}

	insert := func(scope *database.TenantScope, ciID string) error {
		return database.WithTenant(ctx, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO metric_sample_v (time, organization_id, ci_id, metric_name, value) VALUES (now(), $1, $2, 'spike_cpu', 1)`,
				f.OrgA, ciID); err != nil {
				return err
			}
			return errSpikeRollback
		})
	}
	for _, c := range []struct {
		name  string
		scope *database.TenantScope
		ci    string
	}{
		{"client1 -> client-2 CI", &clientScope, foreign},
		{"site1 -> sibling-site CI", &siteScope, sibling},
		{"site1 -> client-2 CI", &siteScope, foreign},
	} {
		if err := insert(c.scope, c.ci); err == nil || errors.Is(err, errSpikeRollback) || !strings.Contains(err.Error(), "check option") {
			t.Errorf("%s: insert through metric_sample_v: %v, want a check option violation", c.name, err)
		}
	}
	for _, c := range []struct {
		name  string
		scope *database.TenantScope
		ci    string
	}{
		{"client1 -> own CI", &clientScope, own},
		{"client1 -> org-wide CI", &clientScope, orgCI},
		{"site1 -> own CI", &siteScope, own},
		{"site1 -> org-wide CI", &siteScope, orgCI},
	} {
		if err := insert(c.scope, c.ci); !errors.Is(err, errSpikeRollback) {
			t.Errorf("%s: insert through metric_sample_v: %v, want accepted", c.name, err)
		}
	}
}
