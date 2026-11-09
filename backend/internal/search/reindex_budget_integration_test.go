package search_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// errExplained rolls back the transaction of an explained write.
var errExplained = errors.New("explained")

// TestReindexFieldRowsQueryBudget covers the field rows of a rebuild batch
// (WP-786, WP-266) at 2,000 CIs (two batches of search.ReindexBatchSize):
// the statement that upserts the fresh rows of a batch and deletes the
// stale ones reads the fresh rows once, not once per field row of the
// batch (explained as reticora_app in a rolled back transaction, without
// hash and merge joins). With NOT EXISTS the planner estimated fresh at a
// few rows under the policies of the seed of the load test (CIs with
// client and site) and chose a nested loop anti join over it, seconds per
// batch of 1,000 CIs. A value cleared
// since the last rebuild loses its field row; the others stay.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -run 'TestReindexFieldRowsQueryBudget' ./internal/search/
func TestReindexFieldRowsQueryBudget(t *testing.T) {
	f := scopetest.Seed(t, "5c")
	bg := context.Background()
	const objects = 2000
	server := `(SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1)`
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, hostname, manufacturer, model, serial_number)
		SELECT $1, `+server+`, 'rfb-' || lpad(i::text, 5, '0'), 'active', 'rfb-host-' || i, 'Vendor ' || (i % 50),
			'model-' || (i % 7), 'rfb-serial-' || i
		FROM generate_series(1, $2::int) AS i`, f.OrgA, objects); err != nil {
		t.Fatalf("seed CIs: %v", err)
	}
	cfg := f.App.Config()
	log := &sentLog{}
	cfg.ConnConfig.Tracer = log
	cfg.MinConns = 0
	traced, perr := pgxpool.NewWithConfig(bg, cfg)
	if perr != nil {
		t.Fatal(perr)
	}
	t.Cleanup(traced.Close)
	repo := search.NewPGRepository(traced)
	if _, err := repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	// The field rows of the last CI batch, explained on the rebuilt index
	// with the join the planner takes for an estimate of a few fresh rows
	// (no hash or merge join): the stale rows must not depend on it.
	var stmt sentStatement
	log.mu.Lock()
	for i := len(log.stmt) - 1; i >= 0; i-- {
		if s := log.stmt[i]; strings.HasPrefix(strings.TrimSpace(s.sql), "WITH fresh AS (") && strings.Contains(s.sql, "JOIN ci e ON") {
			stmt = s
			break
		}
	}
	log.mu.Unlock()
	if stmt.sql == "" {
		t.Fatal("the rebuild sent no statement of the field rows of a CI batch")
	}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	var plan []byte
	if err := database.WithTenant(bg, f.App, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		if _, qerr := tx.Exec(ctx, `SET LOCAL enable_hashjoin = off; SET LOCAL enable_mergejoin = off`); qerr != nil {
			return qerr
		}
		if qerr := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+stmt.sql, stmt.args...).Scan(&plan); qerr != nil {
			return qerr
		}
		return errExplained
	}); !errors.Is(err, errExplained) {
		t.Fatalf("explain the field rows of a batch: %v", err)
	}
	nodes := searchPlanNodes(t, plan)
	for _, n := range nodes {
		loops, _ := n["Actual Loops"].(float64)
		if n["CTE Name"] == "fresh" && loops > 1 {
			t.Errorf("the batch reads its fresh field rows %d times, want once: %s", int(loops), describeSearchPlan(nodes))
		}
	}
	t.Logf("field rows of a batch: %s", describeSearchPlan(nodes))

	// A cleared value loses its field row with the next rebuild.
	var id, doc string
	if err := f.Admin.QueryRow(bg, `UPDATE ci SET model = NULL WHERE organization_id = $1 AND name = 'rfb-00042' RETURNING id::text`, f.OrgA).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("second reindex: %v", err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM search_document WHERE entity_id = $1`, id).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	rows, err := f.Admin.Query(bg, `SELECT field FROM search_document_field WHERE document_id = $1`, doc)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var field string
		if serr := rows.Scan(&field); serr != nil {
			t.Fatal(serr)
		}
		fields[field] = true
	}
	rows.Close()
	if fields["core.model"] || !fields["core.hostname"] || !fields["core.serial_number"] {
		t.Errorf("field rows after the model was cleared and the index rebuilt: %v", fields)
	}
}
