package override

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// TestRecordDiscoveredBatchReadsCIsByKey covers the batch provenance of the
// ingest (WP-333, WP-334, NFR-03) against PostgreSQL: the visibility check
// reads the CIs of the batch once per statement. As a semi join of the
// values with the CIs it took seconds per batch in the load test once the
// planner's statistics were older than a large ingest: the organization's
// CIs were read and compared for every value. The statement still writes
// the values of the visible CIs and nothing for a CI the scope does not
// show.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL); its organization
// has ids of its own.
func TestRecordDiscoveredBatchReadsCIsOnce(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	const org = "0334b0a7-0000-4000-8000-000000000001"
	cleanup := func() {
		if _, cerr := admin.Exec(ctx, `DELETE FROM organization WHERE id = $1`, org); cerr != nil {
			t.Errorf("cleanup: %v", cerr)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err = admin.Exec(ctx, `INSERT INTO organization (id, name, slug) VALUES ($1, 'WP-334 batch plan', 'wp334-batch-plan')`, org); err != nil {
		t.Fatal(err)
	}
	var cis []string
	if _, err = admin.Exec(ctx, `INSERT INTO ci (organization_id, ci_type_id, name, status)
		SELECT $1, t.id, 'plan-' || n, 'active' FROM ci_type t, generate_series(1, 300) n
		WHERE t.key = 'server' AND t.organization_id IS NULL`, org); err != nil {
		t.Fatal(err)
	}
	rows, err := admin.Query(ctx, `SELECT id::text FROM ci WHERE organization_id = $1 ORDER BY id LIMIT 100`, org)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		cis = append(cis, id)
	}
	rows.Close()
	var ids, names []string
	var raw, sources []*string
	var ranks []int32
	var observed []time.Time
	source := "snmp"
	for _, id := range cis {
		for _, field := range []string{"fqdn", "hostname", "manufacturer", "model", "name", "serial_number", "management_ip"} {
			v := fmt.Sprintf("%q", field)
			ids, names, raw = append(ids, id), append(names, field), append(raw, &v)
			sources, ranks, observed = append(sources, &source), append(ranks, 3), append(observed, time.Now().UTC())
		}
	}

	app, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	scope := database.OrgWideScope(org, "")
	var plan []map[string]any
	err = database.WithTenant(ctx, app, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var out string
		if qerr := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+recordDiscoveredBatchSQL,
			org, ids, names, raw, sources, ranks, observed, cis).Scan(&out); qerr != nil {
			return qerr
		}
		return json.Unmarshal([]byte(out), &plan)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Every read of ci happens once per statement, in the CTE of the
	// visible CIs (an InitPlan): whatever index the planner picks for it,
	// it never runs once per value.
	var reads []string
	var walk func(node map[string]any, once bool)
	walk = func(node map[string]any, once bool) {
		once = once || node["Parent Relationship"] == "InitPlan"
		if node["Relation Name"] == "ci" {
			reads = append(reads, fmt.Sprintf("%v %v (once: %v)", node["Node Type"], node["Index Name"], once))
			if !once {
				t.Errorf("ci is read for every value: %v", node)
			}
		}
		children, _ := node["Plans"].([]any)
		for _, c := range children {
			if child, ok := c.(map[string]any); ok {
				walk(child, once)
			}
		}
	}
	walk(plan[0]["Plan"].(map[string]any), false)
	if len(reads) != 1 {
		t.Errorf("the batch provenance reads ci %d times: %v; want once per statement", len(reads), reads)
	}
	t.Logf("ci read: %v", reads)

	// The statement writes every value of the visible CIs.
	repo := NewPGRepository(app)
	batch := make([]DiscoveredValues, len(cis))
	for i, id := range cis {
		batch[i] = DiscoveredValues{CIID: id, Values: map[string]any{"model": "M", "name": fmt.Sprintf("n-%d", i)}, Source: "snmp",
			ObservedAt: time.Now()}
	}
	if err = repo.RecordDiscoveredBatch(database.ContextWithTenantScope(ctx, &scope), org, batch); err != nil {
		t.Fatal(err)
	}
	var written int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM ci_field_value WHERE organization_id = $1 AND discovered_at IS NOT NULL`,
		org).Scan(&written); err != nil || written != 200 {
		t.Errorf("%d discovered values written, want 200 (%v)", written, err)
	}
	// A CI the scope does not show: nothing of the batch is written.
	other := database.OrgWideScope("0334b0a7-0000-4000-8000-000000000002", "")
	if err = repo.RecordDiscoveredBatch(database.ContextWithTenantScope(ctx, &other), other.OrgID, batch[:1]); err == nil {
		t.Error("a CI of another organization was written")
	}
}
