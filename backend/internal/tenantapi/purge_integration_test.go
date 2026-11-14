package tenantapi_test

// WP-1154 (TLC-06 part): the database purge of an organization after the
// grace period of its deletion, on PostgreSQL with the application login.
// org.purge deletes every row of the organization (the append-only tables
// included) table by table, children before parents, through
// org_purge_batch of the role reticora_org_purger, keeps the vault and the
// tombstone, empties the hypertables also in compressed chunks and the
// materializations of the continuous aggregates, leaves the neighbouring
// organization unchanged, continues
// after a crash with the same counts and hashes, writes purge_started and
// db_purged to operator_audit and reminds the org_admins 7 and 1 day
// before the end of the grace period, once per threshold.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestOrgPurge|TestOrgDeletionNotices' ./internal/tenantapi/

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

// purgeTables is the table catalog of the server (cmd/server).
func purgeTables(ctx context.Context, tx pgx.Tx) ([]string, error) {
	tables, err := export.OrgPurgeTables(ctx, tx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tables))
	for i := range tables {
		names = append(names, tables[i].Name)
	}
	return names, nil
}

type purgeEnv struct {
	f    *scopetest.Fixture
	user string
}

func (e *purgeEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *purgeEnv) id(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

// seedPurge seeds organization A with P2 and P3 data of the append-only
// tables (audit entries, a movement with legacy signature, a completed
// stocktake, a disposal, an alarm and an incident with their transitions,
// younger than 400 days) and organization B with some of them.
func seedPurge(t *testing.T) *purgeEnv {
	t.Helper()
	f := scopetest.Seed(t, "21")
	e := &purgeEnv{f: f, user: f.AppUser(t, f.OrgA, "wp1154-admin")}
	clear := func() {
		orgs := []string{f.OrgA, f.OrgB}
		for _, rel := range append([]string{"metric_sample", "probe_result"}, materializations(t, f)...) {
			if _, err := f.Admin.Exec(context.Background(), `DELETE FROM `+rel+` WHERE organization_id = ANY($1::uuid[])`, orgs); err != nil {
				t.Errorf("remove the rows of %s: %v", rel, err)
			}
		}
	}
	clear()
	t.Cleanup(clear)
	// The legacy signatures are kept by their guard; the fixture removes
	// the organizations only after they are gone (a run that failed before
	// the purge leaves them).
	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := f.Admin.Acquire(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Release()
		for _, stmt := range []string{`SET session_replication_role = replica`,
			`DELETE FROM assignment_signature_legacy WHERE organization_id IN ('` + f.OrgA + `', '` + f.OrgB + `')`,
			`SET session_replication_role = origin`} {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				t.Error(err)
			}
		}
	})
	for _, org := range []string{f.OrgA, f.OrgB} {
		client := f.Client1
		if org == f.OrgB {
			client = ""
		}
		ci := f.CI(t, org, client, "wp1154-ci")
		e.exec(t, `INSERT INTO audit_log (organization_id, action, resource_type, resource_id) VALUES ($1, 'ci.created', 'ci', $2)`, org, ci)
		e.exec(t, `INSERT INTO metric_sample (time, organization_id, ci_id, metric_name, value) VALUES (now(), $1, $2, 'cpu', 0.5)`, org, ci)
		// Samples and probe results older than the compression of their
		// hypertables (metric_sample 7 days, probe_result 1 day) and
		// outside every refresh window of the continuous aggregates.
		e.exec(t, `INSERT INTO metric_sample (time, organization_id, ci_id, metric_name, value)
			VALUES (now() - interval '10 days', $1, $2, 'cpu', 0.7)`, org, ci)
		e.exec(t, `INSERT INTO probe_result (time, organization_id, probe_id, collector_id, seq, epoch, status, rtt_ms)
			VALUES (now() - interval '10 days', $1, gen_random_uuid(), gen_random_uuid(), 1, 1, 0, 1.5)`, org)
	}
	// The continuous aggregates materialize the old buckets; then the old
	// chunks of the hypertables and of the materializations are compressed,
	// as the columnstore policies do in operation.
	for _, agg := range []string{"metric_sample_1h", "probe_result_5m", "probe_result_1h", "probe_result_1d"} {
		e.exec(t, `CALL refresh_continuous_aggregate('`+agg+`', now() - interval '12 days', now() - interval '8 days')`)
	}
	for _, rel := range []string{"metric_sample", "probe_result", "probe_result_5m", "probe_result_1h", "probe_result_1d"} {
		e.exec(t, `SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('`+rel+`', older_than => now() - interval '8 days') c`)
	}
	org := f.OrgA
	ci := f.CI(t, org, f.Client1, "wp1154-ci-2")
	asset := f.Asset(t, org, f.Client1, "WP1154-A1")
	e.exec(t, `INSERT INTO audit_log (organization_id, action, resource_type, resource_id) VALUES ($1, 'asset.created', 'assets', $2)`, org, asset)
	movement := e.id(t, `INSERT INTO stock_movement (organization_id, item_kind, asset_id, movement_type)
		VALUES ($1, 'asset', $2, 'assignment') RETURNING id::text`, org, asset)
	e.exec(t, `INSERT INTO assignment_signature_legacy (organization_id, movement_id, kind, payload, payload_sha256)
		VALUES ($1, $2, 'checkout', '{}'::jsonb, repeat('0', 64))`, org, movement)
	site := e.id(t, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'wp1154-site') RETURNING id::text`, org, f.Client1)
	warehouse := e.id(t, `INSERT INTO location (organization_id, kind, parent_id, name) VALUES ($1, 'warehouse', $2, 'wp1154-wh') RETURNING id::text`,
		org, site)
	stocktake := e.id(t, `INSERT INTO stocktake (organization_id, title, location_id) VALUES ($1, 'wp1154', $2) RETURNING id::text`, org, warehouse)
	e.exec(t, `INSERT INTO stocktake_item (organization_id, stocktake_id, item_kind, asset_id, location_id, expected_quantity)
		VALUES ($1, $2, 'asset', $3, $4, 1)`, org, stocktake, asset, warehouse)
	e.exec(t, `INSERT INTO stock_scan (organization_id, stocktake_id, ci_id, scanned_by, scan_result) VALUES ($1, $2, $3, $4, 'found')`,
		org, stocktake, ci, e.user)
	e.exec(t, `UPDATE stocktake SET status = 'completed' WHERE id = $1`, stocktake)
	e.exec(t, `INSERT INTO disposal_record (organization_id, asset_id, method, performed_at, client_id) VALUES ($1, $2, 'recycling', now(), $3)`,
		org, asset, f.Client1)
	alarm := e.id(t, `INSERT INTO alarm (organization_id, client_id, source_kind, source_id, severity, state, opened_at)
		VALUES ($1, $2, 'collector', gen_random_uuid(), 'warning', 'open', now()) RETURNING id::text`, org, f.Client1)
	e.exec(t, `INSERT INTO alarm_transition (organization_id, alarm_id, from_state, to_state, at, reason) VALUES ($1, $2, 'open', 'acknowledged', now(), 'wp1154')`,
		org, alarm)
	incident := e.id(t, `INSERT INTO incident (organization_id, client_id, cause_class, opened_at) VALUES ($1, $2, 'topology', now()) RETURNING id::text`,
		org, f.Client1)
	e.exec(t, `INSERT INTO incident_transition (organization_id, incident_id, from_state, to_state, at, reason, details)
		VALUES ($1, $2, 'open', 'acknowledged', now(), 'wp1154', '{}'::jsonb)`, org, incident)
	return e
}

// requestDue puts the deletion of organization A in requested with a grace
// period that ended yesterday.
func (e *purgeEnv) requestDue(t *testing.T) {
	t.Helper()
	e.exec(t, `UPDATE organization SET deletion_state = 'requested', deletion_requested_at = now() - interval '31 days',
		deletion_due_at = now() - interval '1 day', deletion_requested_by = $2 WHERE id = $1`, e.f.OrgA, e.user)
}

// materializations are the materialization hypertables of the continuous
// aggregates with organization_id (org_purge_target of their views).
func materializations(t *testing.T, f *scopetest.Fixture) []string {
	t.Helper()
	rows, err := f.Admin.Query(context.Background(), `SELECT org_purge_target(format('%I.%I', view_schema, view_name)::regclass)::text
		FROM timescaledb_information.continuous_aggregates WHERE view_schema = 'public' ORDER BY view_name`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// counts returns the rows of org per table with organization_id and per
// continuous aggregate (the rows of its materialization hypertable, which
// the view does not show beyond its materialization).
func (e *purgeEnv) counts(t *testing.T, org string) map[string]int64 {
	t.Helper()
	bg := context.Background()
	rows, err := e.f.Admin.Query(bg, `SELECT c.relname::text, org_purge_target(c.oid)::text FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace AND NOT c.relispartition
		  AND (c.relkind IN ('r', 'p') OR (c.relkind = 'v' AND org_purge_target(c.oid) <> c.oid))
		  AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped)
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	type rel struct{ name, target string }
	tables, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rel, error) {
		var v rel
		return v, r.Scan(&v.name, &v.target)
	})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, table := range tables {
		var n int64
		if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM `+table.target+` WHERE organization_id = $1`, org).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table.name, err)
		}
		out[table.name] = n
	}
	return out
}

func TestOrgPurgeRemovesAllTenantRows(t *testing.T) {
	e := seedPurge(t)
	bg := context.Background()
	before := e.counts(t, e.f.OrgA)
	for _, table := range []string{"audit_log", "stock_movement", "assignment_signature_legacy", "stocktake", "stocktake_item",
		"stock_scan", "disposal_record", "alarm", "alarm_transition", "incident", "incident_transition", "ci", "metric_sample",
		"probe_result", "metric_sample_1h", "probe_result_5m", "probe_result_1h", "probe_result_1d"} {
		if before[table] == 0 {
			t.Fatalf("seed: no row of %s", table)
		}
	}
	// The old samples lie in compressed chunks (TimescaleDB refuses ctid
	// there: the purge deletes a hypertable by organization).
	var compressed int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM timescaledb_information.chunks
		WHERE hypertable_name IN ('metric_sample', 'probe_result') AND is_compressed`).Scan(&compressed); err != nil || compressed < 2 {
		t.Fatalf("seed: %d compressed chunks %v, want those of both hypertables", compressed, err)
	}
	neighbour := e.counts(t, e.f.OrgB)
	e.requestDue(t)

	results, err := tenantapi.NewPurger(e.f.App, purgeTables).PurgeOrganization(bg, e.f.OrgA)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	after := e.counts(t, e.f.OrgA)
	for table, n := range after {
		if n != 0 && table != "org_dek" && table != "pii_subject" {
			t.Errorf("%s: %d rows of the purged organization left", table, n)
		}
	}
	for _, r := range results {
		if r.RowsAfter != 0 || r.RowsBefore != before[r.Table] || len(r.SHA256) != 64 {
			t.Errorf("result %+v, %d rows before", r, before[r.Table])
		}
	}
	if got := e.counts(t, e.f.OrgB); !mapsEqual(got, neighbour) {
		for table, n := range neighbour {
			if got[table] != n {
				t.Errorf("neighbour %s: %d rows, before %d", table, got[table], n)
			}
		}
	}
	var name, slug, state, settings string
	if err := e.f.Admin.QueryRow(bg, `SELECT name, slug, deletion_state, settings::text FROM organization WHERE id = $1`, e.f.OrgA).
		Scan(&name, &slug, &state, &settings); err != nil {
		t.Fatal(err)
	}
	if name != e.f.OrgA || slug != "purged-"+e.f.OrgA || state != tenantapi.DeletionPurged || settings != "{}" {
		t.Errorf("tombstone %q %q %q %s", name, slug, state, settings)
	}
	// A second run finds nothing to do.
	if r, err := tenantapi.NewPurger(e.f.App, purgeTables).PurgeOrganization(bg, e.f.OrgA); err != nil || r != nil {
		t.Errorf("purge of a purged organization: %v %v", r, err)
	}
}

func mapsEqual(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestOrgPurgeOrderAndCycles(t *testing.T) {
	e := seedPurge(t)
	e.requestDue(t)
	e.exec(t, `UPDATE organization SET deletion_state = 'purging' WHERE id = $1`, e.f.OrgA)
	order, edges, err := tenantapi.NewPurger(e.f.App, purgeTables).Order(context.Background(), e.f.OrgA)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	pos := map[string]int{}
	for i, table := range order {
		pos[table] = i
	}
	for _, kept := range []string{"organization", "org_dek", "pii_subject"} {
		if _, ok := pos[kept]; ok {
			t.Errorf("%s is purged by the database step", kept)
		}
	}
	for _, edge := range edges {
		c, okC := pos[edge.Child]
		p, okP := pos[edge.Parent]
		if !okC || !okP || edge.Child == edge.Parent || (edge.OnDelete != "a" && edge.OnDelete != "r") {
			continue
		}
		// A cycle of restricting keys cannot follow every key; its tables
		// are deleted row by row (org_purge_rows).
		if restrictingCycle[edge.Child] && restrictingCycle[edge.Parent] {
			continue
		}
		if c > p {
			t.Errorf("%s (%s) is deleted after its parent %s", edge.Child, edge.OnDelete, edge.Parent)
		}
	}
	_, cycles, err := tenantapi.PurgeOrder(order, edges)
	if err != nil {
		t.Fatal(err)
	}
	// The cycles of foreign keys between the tables of the purge, each
	// resolved by its cascading or clearing keys.
	got := make([]string, 0, len(cycles))
	for _, c := range cycles {
		got = append(got, strings.Join(c, ","))
	}
	if want := wantPurgeCycles; !slices.Equal(got, want) {
		t.Errorf("cycles %q, want %q", got, want)
	}

	// The rules on a small graph: a cycle of a cascading and a restricting
	// key resolves, one of restricting keys does not.
	tables := []string{"a", "b", "c"}
	order2, cyc, err := tenantapi.PurgeOrder(tables, []tenantapi.FKEdge{
		{Child: "a", Parent: "b", OnDelete: "c"}, {Child: "b", Parent: "a", OnDelete: "a"}, {Child: "c", Parent: "a", OnDelete: "r"}})
	if err != nil || !slices.Equal(order2, []string{"c", "b", "a"}) || len(cyc) != 1 {
		t.Errorf("order %v, cycles %v, %v", order2, cyc, err)
	}
	// A cycle of restricting keys (credential and vault_connection) is
	// ordered by name; the purge deletes its tables row by row.
	if order3, _, err := tenantapi.PurgeOrder(tables, []tenantapi.FKEdge{
		{Child: "a", Parent: "b", OnDelete: "r"}, {Child: "b", Parent: "a", OnDelete: "a"}}); err != nil ||
		!slices.Equal(order3, []string{"c", "a", "b"}) {
		t.Errorf("restricting cycle: %v %v", order3, err)
	}
}

// wantPurgeCycles are the cycles of foreign keys between the tables of the
// purge (the tables of each, sorted). All but credential and
// vault_connection are resolved by a cascading or clearing key.
var wantPurgeCycles = []string{"alarm,incident", "asset,ci", "collector_group,probe", "credential,vault_connection",
	"form_def,form_def_version", "workflow_def,workflow_def_version"}

// restrictingCycle are the tables of the cycle of restricting keys: a
// vault connection authenticates with a credential, a credential may refer
// to a vault connection (WP-893, WP-894).
var restrictingCycle = map[string]bool{"credential": true, "vault_connection": true}

func TestOrgPurgeResumes(t *testing.T) {
	e := seedPurge(t)
	bg := context.Background()
	e.requestDue(t)
	e.exec(t, `UPDATE organization SET deletion_state = 'purging' WHERE id = $1`, e.f.OrgA)
	// The digests of an uninterrupted run: rows and hash per table.
	purger := tenantapi.NewPurger(e.f.App, purgeTables)
	order, _, err := purger.Order(bg, e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]tenantapi.PurgeTableResult{}
	for _, table := range order {
		r, derr := purger.Digest(bg, e.f.OrgA, table)
		if derr != nil {
			t.Fatal(derr)
		}
		want[table] = r
	}

	crash := errors.New("crash")
	tablesDone := 0
	_, err = tenantapi.NewPurger(e.f.App, purgeTables).WithBatchSize(1).WithTableHook(func(context.Context, string) error {
		tablesDone++
		if tablesDone == 3 {
			return crash
		}
		return nil
	}).PurgeOrganization(bg, e.f.OrgA)
	if !errors.Is(err, crash) {
		t.Fatalf("first run: %v, want the crash after the third table", err)
	}
	var state string
	if serr := e.f.Admin.QueryRow(bg, `SELECT deletion_state FROM organization WHERE id = $1`, e.f.OrgA).Scan(&state); serr != nil || state != "purging" {
		t.Fatalf("state after the crash %q %v", state, serr)
	}
	results, err := tenantapi.NewPurger(e.f.App, purgeTables).PurgeOrganization(bg, e.f.OrgA)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(results) != len(order) {
		t.Fatalf("%d results, %d tables", len(results), len(order))
	}
	for i, r := range results {
		w := want[r.Table]
		if r.Table != order[i] || r.RowsBefore != w.RowsBefore || r.SHA256 != w.SHA256 || r.RowsAfter != 0 {
			t.Errorf("result %+v, want %+v at %d", r, w, i)
		}
	}
}

func TestOrgPurgeFunctionRefuses(t *testing.T) {
	e := seedPurge(t)
	bg := context.Background()
	e.requestDue(t)
	call := func(scopeOrg, org, table string) error {
		scope := database.OrgWideScope(scopeOrg, "")
		return database.WithTenant(bg, e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			var n int64
			return tx.QueryRow(ctx, `SELECT org_purge_batch($1, $2::regclass, 10)`, org, table).Scan(&n)
		})
	}
	refused := func(what string, err error) {
		t.Helper()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s: %v, want 42501", what, err)
		}
	}
	refused("organization in requested", call(e.f.OrgA, e.f.OrgA, "audit_log"))
	e.exec(t, `UPDATE organization SET deletion_state = 'purging' WHERE id = $1`, e.f.OrgA)
	refused("foreign app.org_id", call(e.f.OrgB, e.f.OrgA, "audit_log"))
	refused("operator_audit", call(e.f.OrgA, e.f.OrgA, "operator_audit"))
	// A view is refused unless it is a continuous aggregate.
	refused("plain view probe_result_v", call(e.f.OrgA, e.f.OrgA, "probe_result_v"))
	// The runtime role itself still cannot delete append-only rows.
	for _, table := range []string{"audit_log", "alarm_transition", "incident_transition", "stock_movement"} {
		scope := database.OrgWideScope(e.f.OrgA, "")
		err := database.WithTenant(bg, e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE organization_id = $1`, e.f.OrgA)
			return err
		})
		if err == nil {
			t.Errorf("DELETE FROM %s as reticora_app passed", table)
		}
	}
	// The organization in purging: the function deletes.
	if err := call(e.f.OrgA, e.f.OrgA, "audit_log"); err != nil {
		t.Errorf("purge batch in purging: %v", err)
	}
}

func TestOrgPurgeOperatorAudit(t *testing.T) {
	e := seedPurge(t)
	bg := context.Background()
	e.requestDue(t)
	// The fixture reuses the ids of the organizations: only the entries of
	// this run count.
	var last int64
	if err := e.f.Admin.QueryRow(bg, `SELECT COALESCE(max(id), 0) FROM operator_audit`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if _, err := tenantapi.NewPurger(e.f.App, purgeTables).PurgeOrganization(bg, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	rows, err := e.f.Admin.Query(bg, `SELECT action, operator_kind, operator_id, details::text FROM operator_audit
		WHERE target_org = $1 AND id > $2 ORDER BY id`, e.f.OrgA, last)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for rows.Next() {
		var action, kind, operatorID, details string
		if serr := rows.Scan(&action, &kind, &operatorID, &details); serr != nil {
			t.Fatal(serr)
		}
		actions = append(actions, action)
		if kind != operator.KindSystem || operatorID != tenantapi.OrgPurgeKind {
			t.Errorf("%s: kind %s, operator %s", action, kind, operatorID)
		}
		if action == "org.deletion.db_purged" && (!strings.Contains(details, `"sha256"`) || !strings.Contains(details, `"audit_log"`) ||
			strings.Contains(details, "wp1154-ci")) {
			t.Errorf("db_purged details %s", details)
		}
		if action == "org.deletion.purge_started" && (!strings.Contains(details, "requested_at") || !strings.Contains(details, e.user)) {
			t.Errorf("purge_started details %s", details)
		}
	}
	// The deletion protocol org.purged is the last entry before purged (WP-1156).
	if !slices.Equal(actions, []string{"org.deletion.purge_started", tenantapi.ActionOrgPurged, "org.deletion.db_purged"}) {
		t.Fatalf("operator_audit actions %v", actions)
	}
	res, err := operator.NewAuditor(e.f.App).Verify(bg)
	if err != nil || !res.Valid {
		t.Fatalf("operator audit chain: %+v %v", res, err)
	}
}

// fakeNotifier records the reminders; failures fails the next calls whose
// key starts with failPrefix.
type fakeNotifier struct {
	mu         sync.Mutex
	keys       []string
	failures   int
	failPrefix string
}

func (n *fakeNotifier) Notify(_ context.Context, _ string, req *notify.Request) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.failures > 0 && strings.HasPrefix(req.DedupKey, n.failPrefix) {
		n.failures--
		return 0, errors.New("notification queue unavailable")
	}
	n.keys = append(n.keys, req.DedupKey)
	return len(req.Recipients), nil
}

type fakeResolver struct{ roles [][]string }

func (r *fakeResolver) Resolve(_ context.Context, _ string, a *notify.Audience) ([]notify.Recipient, error) {
	r.roles = append(r.roles, a.Roles)
	return []notify.Recipient{{UserID: "u-1"}}, nil
}

func TestOrgDeletionNotices(t *testing.T) {
	// Request and cancel reach every org_admin, critical and personal.
	for _, typ := range []string{events.OrgDeletionRequested, events.OrgDeletionCancelled} {
		sub, ok := notify.Subscriptions[typ]
		if !ok || !sub.Critical || !sub.Personal || sub.Audience == nil {
			t.Fatalf("%s subscription %+v", typ, sub)
		}
		if a := sub.Audience(map[string]any{}); a == nil || !slices.Equal(a.Roles, []string{"org_admin"}) {
			t.Errorf("%s audience %+v", typ, a)
		}
		for _, locale := range []string{notify.LocaleDE, notify.LocaleEN} {
			render, ok := notify.Templates[typ][locale]
			if !ok || render(map[string]any{"due_at": "2026-12-13T22:00:00Z"}).Subject == "" {
				t.Errorf("%s: no %s text", typ, locale)
			}
		}
	}

	e := seedPurge(t)
	bg := context.Background()

	// A deletion request reaches exactly the org_admins through the event
	// consumer of the notifications (in-app, personal), no other user.
	admin, engineer := e.f.AppUser(t, e.f.OrgA, "wp1154-org-admin"), e.f.AppUser(t, e.f.OrgA, "wp1154-engineer")
	for user, role := range map[string]string{admin: "org_admin", engineer: "engineer"} {
		var roleID string
		if err := e.f.Admin.QueryRow(bg, `SELECT id::text FROM role WHERE organization_id = $1 AND name = $2`, e.f.OrgA, role).Scan(&roleID); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal(err)
			}
			roleID = e.id(t, `INSERT INTO role (organization_id, name, permissions) VALUES ($1, $2, '["location:read"]') RETURNING id::text`, e.f.OrgA, role)
		}
		e.exec(t, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, e.f.OrgA, user, roleID)
	}
	center := notify.NewMemoryCenter()
	if err := center.Channels.Create(bg, &notify.Channel{OrganizationID: e.f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	inbox := center.Inbox.(*notify.MemoryInbox)
	realNotifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences,
		notify.NewSender(map[string]notify.Provider{notify.ProviderInApp: notify.NewInAppProvider(inbox)}))
	consumer := notify.NewEventConsumer(notify.NewPGResolver(e.f.App, permission.NewPGRepository(e.f.App)), realNotifier)
	data, _ := json.Marshal(events.OrgDeletionData{ID: e.f.OrgA, RequestedBy: admin, DueAt: time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)})
	if err := consumer.Consume(bg, &events.Envelope{ID: e.f.ID(), Type: events.OrgDeletionRequested, OrgID: e.f.OrgA, OccurredAt: time.Now(),
		EntityID: e.f.OrgA, Data: data}); err != nil {
		t.Fatalf("consume org.deletion_requested: %v", err)
	}
	for range 3 {
		if _, err := realNotifier.ProcessDue(bg); err != nil {
			t.Fatal(err)
		}
	}
	notices := func(user string) int {
		n := 0
		for _, item := range inbox.For(e.f.OrgA, user) {
			if item.Category == "org_deletion" && item.Data["event_type"] == events.OrgDeletionRequested {
				n++
			}
		}
		return n
	}
	if got := notices(admin); got != 1 {
		t.Errorf("org_admin got %d notices of the deletion request, want 1", got)
	}
	if got := notices(engineer); got != 0 {
		t.Errorf("engineer got %d notices of the deletion request, want none", got)
	}

	due := time.Now().UTC().Add(6 * 24 * time.Hour).Truncate(time.Second)
	e.exec(t, `UPDATE organization SET deletion_state = 'requested', deletion_requested_at = $2::timestamptz - interval '30 days',
		deletion_due_at = $2, deletion_requested_by = $3 WHERE id = $1`, e.f.OrgA, due, e.user)
	notifier, resolver := &fakeNotifier{}, &fakeResolver{}
	now := time.Now().UTC()
	purger := tenantapi.NewPurger(e.f.App, purgeTables).WithNotices(notifier, resolver).WithClock(func() time.Time { return now })
	prefix := "org.deletion:" + e.f.OrgA + ":" + due.Format(time.RFC3339) + ":"
	mine := func() []string {
		var out []string
		for _, k := range notifier.keys {
			if strings.HasPrefix(k, "org.deletion:"+e.f.OrgA) {
				out = append(out, k)
			}
		}
		return out
	}
	// A failing notification queue keeps the threshold open: the run
	// reports the error, the next run sends the reminder.
	notifier.failures, notifier.failPrefix = 1, "org.deletion:"+e.f.OrgA
	if err := purger.Run(bg); err == nil || notifier.failures != 0 {
		t.Fatal("run with a failing notifier: no error")
	}
	if got := mine(); len(got) != 0 {
		t.Fatalf("reminders after a failure %v, want none yet", got)
	}
	// Two runs on the same day: the reminder of 7 days goes once.
	for range 2 {
		if err := purger.Run(bg); err != nil {
			t.Fatal(err)
		}
	}
	if got := mine(); !slices.Equal(got, []string{prefix + "7"}) {
		t.Fatalf("reminders %v, want one of 7 days", got)
	}
	if len(resolver.roles) == 0 || !slices.Equal(resolver.roles[0], []string{"org_admin"}) {
		t.Errorf("audience roles %v", resolver.roles)
	}
	// Twelve hours before the end: the reminder of one day, once.
	now = due.Add(-12 * time.Hour)
	for range 2 {
		if err := purger.Run(bg); err != nil {
			t.Fatal(err)
		}
	}
	if got := mine(); !slices.Equal(got, []string{prefix + "7", prefix + "1"}) {
		t.Fatalf("reminders %v, want 7 and 1 day once each", got)
	}
	var state string
	if err := e.f.Admin.QueryRow(bg, `SELECT deletion_state FROM organization WHERE id = $1`, e.f.OrgA).Scan(&state); err != nil || state != "requested" {
		t.Fatalf("state %q %v: the reminder must not purge", state, err)
	}
}
