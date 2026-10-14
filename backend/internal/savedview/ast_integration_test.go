package savedview_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
)

// TestFilterASTAgainstPostgres runs the filter AST of SRC-03 (WP-187)
// against PostgreSQL: or, not, a numeric attribute comparison and an asset
// field of the linked asset select exactly the expected CIs.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestFilterASTAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "bc")
	bg := context.Background()
	big := f.CI(t, f.OrgA, f.Client1, "ast-big")
	small := f.CI(t, f.OrgA, f.Client1, "ast-small")
	lab := f.CI(t, f.OrgA, f.Client1, "ast-lab")
	asset := f.Asset(t, f.OrgA, f.Client1, "AST-1")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE ci SET attributes = '{"ram_gb": 64}' WHERE id = $1`, []any{big}},
		{`UPDATE ci SET attributes = '{"ram_gb": 8}', status = 'maintenance' WHERE id = $1`, []any{small}},
		{`UPDATE ci SET attributes = '{"ram_gb": 128}' WHERE id = $1`, []any{lab}},
		{`UPDATE asset SET warranty_end = '2026-12-31', ci_id = $2 WHERE id = $1`, []any{asset, big}},
		{`UPDATE ci SET asset_id = $2 WHERE id = $1`, []any{big, asset}},
	} {
		if _, err := f.Admin.Exec(bg, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	engine := savedview.NewPGQueryEngine(f.App)
	run := func(raw string) map[string]bool {
		t.Helper()
		var where savedview.Expr
		if err := json.Unmarshal([]byte(raw), &where); err != nil {
			t.Fatal(err)
		}
		results, _, err := engine.Query(f.OrgCtx(f.OrgA), f.OrgA, savedview.FilterSpec{EntityKind: "ci", Where: &where}, api.PaginationParams{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, r := range results {
			out[r.ID] = true
		}
		return out
	}
	// RAM of at least 16 GB and not the lab machine: big only.
	got := run(`{"and": [{"field": "attributes.ram_gb", "op": "gte", "value": 16}, {"not": {"field": "name", "op": "like", "value": "*lab"}}]}`)
	if len(got) != 1 || !got[big] {
		t.Errorf("and/not: %v", got)
	}
	// In maintenance or with a warranty ending in 2026: small and big.
	got = run(`{"or": [{"field": "status", "op": "eq", "value": "maintenance"}, {"field": "asset.warranty_end", "op": "lt", "value": "2027-01-01"}]}`)
	if len(got) != 2 || !got[small] || !got[big] {
		t.Errorf("or with an asset field: %v", got)
	}
}
