package probe_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Raw probe results (PRB-06, PRB-07, MON-01; WP-945, migration
// 000325_probe_result, ADR 0001, ADR 0002): the hypertable probe_result
// without RLS behind the security-barrier view probe_result_v with the
// organization, client and site predicates, the CASCADED CHECK OPTION, no
// direct access of the application role, the idempotency key also on
// compressed chunks, the status encoding and the TimescaleDB jobs.
//
// Retest:
//
//	cd backend && go test -count=1 -p 1 -run 'TestProbeResult' ./internal/probe/

// resultEnv seeds probes in both organizations and removes its result rows
// (probe_result has no foreign key that cascades them).
type resultEnv struct {
	*schemaEnv
	collector string
}

func newResultEnv(t *testing.T) *resultEnv {
	t.Helper()
	e := &resultEnv{schemaEnv: newSchemaEnv(t), collector: "c0de93c0-0000-4000-8000-0000000000c1"}
	clean := func() {
		if _, err := e.f.Admin.Exec(context.Background(), `DELETE FROM probe_result WHERE organization_id IN ($1, $2)`,
			e.f.OrgA, e.f.OrgB); err != nil {
			t.Errorf("clean probe_result: %v", err)
		}
	}
	clean()
	t.Cleanup(clean)
	return e
}

// seed inserts results of the probe directly into the hypertable (the
// maintenance pool is no application role).
func (e *resultEnv) seed(orgID, probeID string, at time.Time, n int) {
	e.t.Helper()
	e.mustExec(`INSERT INTO probe_result (time, organization_id, probe_id, collector_id, seq, epoch, status, rtt_ms)
		SELECT $3::timestamptz + make_interval(secs => i), $1, $2, $4, i, 1, 0, 1.5 FROM generate_series(1, $5) i`,
		orgID, probeID, at, e.collector, n)
}

// visibleResults counts the rows of probe_result_v in the scope of ctx.
func (e *resultEnv) visibleResults(ctx context.Context, orgID string) int {
	e.t.Helper()
	return e.count(ctx, orgID, "probe_result_v")
}

// insertAs inserts one result through the view in the scope of ctx.
func (e *resultEnv) insertAs(ctx context.Context, orgID, rowOrg, probeID string, seq int, at time.Time) error {
	return e.as(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO probe_result_v (time, organization_id, probe_id, collector_id, seq, epoch, status)
			VALUES ($1, $2, $3, $4, $5, 1, 0) ON CONFLICT DO NOTHING`, at, rowOrg, probeID, e.collector, seq)
		return err
	})
}

// TestProbeResultViewIsolatesOrganizations: the view shows the rows of the
// transaction's organization only.
func TestProbeResultViewIsolatesOrganizations(t *testing.T) {
	e := newResultEnv(t)
	f := e.f
	clientB := e.scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'wp945 b', 'wp945-b') RETURNING id::text`, f.OrgB)
	pa := e.probeOfClient(f.OrgA, f.Client1, "10.45.0.1:22")
	pb := e.probeOfClient(f.OrgB, clientB, "10.45.0.2:22")
	now := time.Now().UTC().Truncate(time.Second)
	e.seed(f.OrgA, pa, now.Add(-time.Hour), 5)
	e.seed(f.OrgB, pb, now.Add(-time.Hour), 3)
	if n := e.visibleResults(f.OrgCtx(f.OrgA), f.OrgA); n != 5 {
		t.Errorf("organization A sees %d results, want 5", n)
	}
	if n := e.visibleResults(f.OrgCtx(f.OrgB), f.OrgB); n != 3 {
		t.Errorf("organization B sees %d results, want 3", n)
	}
	// Asked for the probe of organization B by id, organization A gets
	// nothing.
	var n int
	if err := e.as(f.OrgCtx(f.OrgA), f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM probe_result_v WHERE probe_id = $1`, pb).Scan(&n)
	}); err != nil || n != 0 {
		t.Fatalf("results of the probe of organization B seen by A: %d, %v", n, err)
	}
}

// TestProbeResultViewHonoursClientAndSiteScope: a client- or
// site-restricted principal sees the results of the probes of its scope
// and of probes without client or site; an org-wide one sees all.
func TestProbeResultViewHonoursClientAndSiteScope(t *testing.T) {
	e := newResultEnv(t)
	f := e.f
	site1 := e.site(f.OrgA, f.Client1, "wp945-site-1")
	p1 := e.probeOfClient(f.OrgA, f.Client1, "10.45.1.1:22")
	p2 := e.probeOfClient(f.OrgA, f.Client2, "10.45.1.2:22")
	pSite := e.scalar(`INSERT INTO probe (organization_id, collector_group_id, client_id, site_id, type, target)
		VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1),
		$2, $3, 'tcp', '10.45.1.3:22') RETURNING id::text`, f.OrgA, f.Client1, site1)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	e.seed(f.OrgA, p1, at, 2)
	e.seed(f.OrgA, p2, at, 3)
	e.seed(f.OrgA, pSite, at, 4)
	if n := e.visibleResults(f.OrgCtx(f.OrgA), f.OrgA); n != 9 {
		t.Errorf("org-wide principal sees %d results, want 9", n)
	}
	if n := e.visibleResults(f.ClientCtx(f.Client1), f.OrgA); n != 6 {
		t.Errorf("client 1 sees %d results, want 6 (its probes, not client 2's)", n)
	}
	if n := e.visibleResults(f.ClientCtx(f.Client2), f.OrgA); n != 3 {
		t.Errorf("client 2 sees %d results, want 3", n)
	}
	scope := database.OrgWideScope(f.OrgA, f.User)
	scope.Sites = database.ScopeIDs(site1)
	siteCtx := database.ContextWithTenantScope(context.Background(), &scope)
	if n := e.visibleResults(siteCtx, f.OrgA); n != 9 {
		t.Errorf("site 1 sees %d results, want 9 (its site and the probes without site)", n)
	}
	other := e.site(f.OrgA, f.Client1, "wp945-site-2")
	scope.Sites = database.ScopeIDs(other)
	otherCtx := database.ContextWithTenantScope(context.Background(), &scope)
	if n := e.visibleResults(otherCtx, f.OrgA); n != 5 {
		t.Errorf("site 2 sees %d results, want 5 (not the probe of site 1)", n)
	}
	// A probe moved to client 2 takes its results along at once.
	e.mustExec(`UPDATE probe SET client_id = $2, site_id = NULL WHERE id = $1`, pSite, f.Client2)
	if n := e.visibleResults(f.ClientCtx(f.Client1), f.OrgA); n != 2 {
		t.Errorf("client 1 sees %d results after the move, want 2", n)
	}
}

// TestProbeResultCheckOptionRefusesForeignRows: an insert through the view
// for another organization or a probe outside the client scope fails on
// the CASCADED CHECK OPTION; inside the scope it succeeds.
func TestProbeResultCheckOptionRefusesForeignRows(t *testing.T) {
	e := newResultEnv(t)
	f := e.f
	p1 := e.probeOfClient(f.OrgA, f.Client1, "10.45.2.1:22")
	p2 := e.probeOfClient(f.OrgA, f.Client2, "10.45.2.2:22")
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if err := e.insertAs(f.OrgCtx(f.OrgA), f.OrgA, f.OrgA, p1, 1, at); err != nil {
		t.Fatalf("insert in the scope: %v", err)
	}
	if err := e.insertAs(f.OrgCtx(f.OrgA), f.OrgA, f.OrgB, p1, 2, at); sqlState(err) != "44000" {
		t.Errorf("insert for organization B: %v, want the check option (44000)", err)
	}
	if err := e.insertAs(f.ClientCtx(f.Client1), f.OrgA, f.OrgA, p2, 3, at); sqlState(err) != "44000" {
		t.Errorf("insert for a probe of client 2 as client 1: %v, want the check option (44000)", err)
	}
	if err := e.insertAs(f.ClientCtx(f.Client1), f.OrgA, f.OrgA, p1, 4, at); err != nil {
		t.Errorf("insert for a probe of client 1 as client 1: %v", err)
	}
	if got := e.scalar(`SELECT count(*)::text FROM probe_result WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB); got != "2" {
		t.Errorf("%s rows stored, want 2", got)
	}
	// An org-wide transaction of organization B skips the probe lookup: a
	// row of B with the probe of A is stored under B, but it does not take
	// the idempotency key of A's real delivery (organization_id is part of
	// probe_result_key).
	if err := e.insertAs(f.OrgCtx(f.OrgB), f.OrgB, f.OrgB, p1, 5, at); err != nil {
		t.Fatalf("org-wide insert of organization B: %v", err)
	}
	if err := e.insertAs(f.OrgCtx(f.OrgA), f.OrgA, f.OrgA, p1, 5, at); err != nil {
		t.Fatalf("delivery of organization A: %v", err)
	}
	if got := e.scalar(`SELECT count(*)::text FROM probe_result WHERE organization_id = $1 AND probe_id = $2 AND seq = 5`,
		f.OrgA, p1); got != "1" {
		t.Errorf("delivery of organization A after B's row with its probe: %s rows, want 1", got)
	}
}

// compressOld compresses every chunk of probe_result older than a day.
func (e *resultEnv) compressOld() {
	e.t.Helper()
	e.mustExec(`SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('probe_result', older_than => INTERVAL '1 day') c`)
}

// TestProbeResultAppRoleHasNoDirectAccess: the application role has no
// privilege on the hypertable, its chunks and the compressed chunk
// relations (has_table_privilege, ADR 0002 decision 7); a read ends with
// permission denied, the view still answers from the compressed chunk.
func TestProbeResultAppRoleHasNoDirectAccess(t *testing.T) {
	e := newResultEnv(t)
	f := e.f
	p := e.probeOfClient(f.OrgA, f.Client1, "10.45.3.1:22")
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	e.seed(f.OrgA, p, old, 10)
	e.seed(f.OrgA, p, time.Now().UTC().Add(-time.Hour).Truncate(time.Second), 10)
	e.compressOld()
	rows, err := f.Admin.Query(context.Background(), `
		WITH chunk AS (SELECT i.inhrelid::regclass AS rel FROM pg_inherits i WHERE i.inhparent = 'probe_result'::regclass)
		SELECT 'probe_result', 'hypertable'
		UNION ALL SELECT rel::text, 'chunk' FROM chunk
		UNION ALL SELECT to_regclass(rel::text || '_compressed')::text, 'compressed chunk' FROM chunk
		           WHERE to_regclass(rel::text || '_compressed') IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	type relation struct{ name, kind string }
	var rels []relation
	for rows.Next() {
		var r relation
		if err = rows.Scan(&r.name, &r.kind); err != nil {
			t.Fatal(err)
		}
		rels = append(rels, r)
	}
	rows.Close()
	kinds := map[string]int{}
	for _, r := range rels {
		kinds[r.kind]++
		var privileged bool
		if err = f.Admin.QueryRow(context.Background(), `SELECT has_table_privilege('reticora_app', $1::regclass, 'SELECT')
			OR has_table_privilege('reticora_app', $1::regclass, 'INSERT') OR has_table_privilege('reticora_app', $1::regclass, 'DELETE')`,
			r.name).Scan(&privileged); err != nil {
			t.Fatal(err)
		}
		if privileged {
			t.Errorf("reticora_app has a privilege on %s %s", r.kind, r.name)
		}
		err = e.as(f.OrgCtx(f.OrgA), f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			var n int
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+r.name).Scan(&n)
		})
		if sqlState(err) != "42501" {
			t.Errorf("read of %s %s as the application: %v, want permission denied", r.kind, r.name, err)
		}
	}
	if kinds["chunk"] < 2 || kinds["compressed chunk"] < 1 {
		t.Fatalf("relations %v: want at least two chunks and one compressed chunk", kinds)
	}
	if n := e.visibleResults(f.OrgCtx(f.OrgA), f.OrgA); n != 20 {
		t.Errorf("the view shows %d results, want 20 (also from the compressed chunk)", n)
	}
}

// TestProbeResultInsertIsIdempotent: a repeated delivery (INSERT … ON
// CONFLICT DO NOTHING through the view) stores no second row, also when
// the chunk is compressed.
func TestProbeResultInsertIsIdempotent(t *testing.T) {
	e := newResultEnv(t)
	f := e.f
	p := e.probeOfClient(f.OrgA, f.Client1, "10.45.4.1:22")
	recent := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	old := time.Now().UTC().Add(-50 * time.Hour).Truncate(time.Second)
	ctx := f.OrgCtx(f.OrgA)
	for range 2 {
		for i, at := range []time.Time{recent, old} {
			if err := e.insertAs(ctx, f.OrgA, f.OrgA, p, i+1, at); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}
	}
	if n := e.visibleResults(ctx, f.OrgA); n != 2 {
		t.Fatalf("%d rows after the double delivery, want 2", n)
	}
	e.compressOld()
	compressed := e.scalar(`SELECT count(*)::text FROM timescaledb_information.chunks
		WHERE hypertable_name = 'probe_result' AND is_compressed AND range_end > $1::timestamptz AND range_start <= $1::timestamptz`, old)
	if compressed != "1" {
		t.Fatalf("the chunk of the old result is not compressed (%s)", compressed)
	}
	for range 2 {
		if err := e.insertAs(ctx, f.OrgA, f.OrgA, p, 2, old); err != nil {
			t.Fatalf("insert into the compressed chunk: %v", err)
		}
	}
	if n := e.visibleResults(ctx, f.OrgA); n != 2 {
		t.Fatalf("%d rows after the delivery into the compressed chunk, want 2", n)
	}
	// A new sequence number in the compressed chunk is a new row.
	if err := e.insertAs(ctx, f.OrgA, f.OrgA, p, 3, old.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if n := e.visibleResults(ctx, f.OrgA); n != 3 {
		t.Fatalf("%d rows, want 3", n)
	}
}

// TestProbeResultStatusEncoding: status 0, 1 and 2 are accepted, 3 is
// refused by the CHECK; the constants encode ok, fail and error of
// probewire.
func TestProbeResultStatusEncoding(t *testing.T) {
	e := newResultEnv(t)
	f := e.f
	p := e.probeOfClient(f.OrgA, f.Client1, "10.45.5.1:22")
	for want, s := range map[int16]probewire.Status{0: probewire.StatusOK, 1: probewire.StatusFail, 2: probewire.StatusError} {
		code, ok := probe.ResultStatusCode(s)
		if !ok || code != want {
			t.Errorf("ResultStatusCode(%s) = %d, %v; want %d", s, code, ok, want)
		}
	}
	if probe.ResultStatusOK != 0 || probe.ResultStatusFail != 1 || probe.ResultStatusError != 2 {
		t.Error("status constants are not 0, 1, 2")
	}
	if _, ok := probe.ResultStatusCode("unknown"); ok {
		t.Error("an unknown status has a code")
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	insert := func(seq int, status int16) error {
		return e.as(f.OrgCtx(f.OrgA), f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO probe_result_v (time, organization_id, probe_id, collector_id, seq, epoch, status)
				VALUES ($1, $2, $3, $4, $5, 1, $6)`, at, f.OrgA, p, e.collector, seq, status)
			return err
		})
	}
	for _, status := range []int16{probe.ResultStatusOK, probe.ResultStatusFail, probe.ResultStatusError} {
		if err := insert(int(status)+1, status); err != nil {
			t.Errorf("status %d: %v", status, err)
		}
	}
	if err := insert(9, 3); sqlState(err) != "23514" {
		t.Errorf("status 3: %v, want the CHECK (23514)", err)
	}
	comment := e.scalar(`SELECT col_description('probe_result'::regclass, (SELECT attnum FROM pg_attribute
		WHERE attrelid = 'probe_result'::regclass AND attname = 'status'))`)
	if !strings.Contains(comment, "0 = ok") || !strings.Contains(comment, "2 = error") {
		t.Errorf("status comment %q", comment)
	}
}

// TestProbeResultPoliciesExist: compression after 1 day, retention 14 days
// as TimescaleDB jobs, 3-hour chunks and the compression settings of ADR
// 0002.
func TestProbeResultPoliciesExist(t *testing.T) {
	e := newResultEnv(t)
	compress := e.scalar(`SELECT config->>'compress_after' FROM timescaledb_information.jobs
		WHERE hypertable_name = 'probe_result' AND proc_name = 'policy_compression'`)
	retention := e.scalar(`SELECT config->>'drop_after' FROM timescaledb_information.jobs
		WHERE hypertable_name = 'probe_result' AND proc_name = 'policy_retention'`)
	if compress != "1 day" || retention != "14 days" {
		t.Errorf("compress_after %q, drop_after %q; want 1 day and 14 days", compress, retention)
	}
	if got := e.scalar(`SELECT time_interval::text FROM timescaledb_information.dimensions
		WHERE hypertable_name = 'probe_result' AND column_name = 'time'`); got != "03:00:00" {
		t.Errorf("chunk interval %s, want 3 hours (ADR 0002)", got)
	}
	settings := e.scalar(`SELECT string_agg(attname || ':' || COALESCE(segmentby_column_index::text, '') || ':' ||
			COALESCE(orderby_column_index::text, '') || ':' || COALESCE(orderby_asc::text, ''), ',' ORDER BY attname)
		FROM timescaledb_information.compression_settings WHERE hypertable_name = 'probe_result'`)
	for _, want := range []string{"organization_id:1::", "probe_id:2::", "time::1:false", "collector_id::2:true", "seq::3:true"} {
		if !strings.Contains(settings, want) {
			t.Errorf("compression settings %s lack %s", settings, want)
		}
	}
	if got := e.scalar(`SELECT string_agg(indexname, ',' ORDER BY indexname) FROM pg_indexes WHERE tablename = 'probe_result'`); got !=
		"idx_probe_result_org_time,idx_probe_result_probe_time,probe_result_key" {
		t.Errorf("indexes %s", got)
	}
}
