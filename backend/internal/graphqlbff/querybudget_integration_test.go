package graphqlbff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAssetAggregateQueryBudget covers ABN-02 for the asset detail
// aggregate of the BFF (GQL-03, WP-311; WP-824) against PostgreSQL in a
// database of its own (scopetest.BudgetDatabase): asset(id) { cis children
// movements documents } with every field, sent through Handler.ServeHTTP
// with a session principal (identity.PrincipalTypeUser) and the PostgreSQL
// repositories of CIs, relationships, assets, movements and documents at a
// traced copy of the application pool (row level security of an org-wide
// principal). The asset with 100 linked CIs, each with a child CI (maxPage
// of cis and children), costs the same statements as the asset with 10,
// apart from the count of each full list (a page shorter than its limit
// gives the count itself, WP-839): the CIs and the children are read once
// per request (dataloader), not per CI, and the request stays within
// aggregateBudget. The fake counters of
// TestAssetAggregateReadsEachListOnce (WP-311) are not repeated. With
// RETICORA_PERF_BUDGET=1 the aggregate of 100 CIs must stay below the
// 300 ms of the detail budget of NFR-02 (best of five).
//
// Retest (locally without TimescaleDB with scopetest.MigrationsDirEnv):
//
//	cd backend && go test -count=1 -run TestAssetAggregateQueryBudget ./internal/graphqlbff/
func TestAssetAggregateQueryBudget(t *testing.T) {
	scopetest.BudgetDatabase(t)
	f := scopetest.Seed(t, "31")
	bg := context.Background()
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	uploader := f.AppUser(t, f.OrgA, "wp824-uploader")

	// seed inserts an asset with n linked CIs, each with a standalone
	// child CI, three movements and two linked documents, and returns its
	// id.
	seed := func(name string, n int) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO asset (organization_id, client_id, inventory_no, name, manufacturer)
			VALUES ($1, $2, $3, $3, 'ACME') RETURNING id::text`, f.OrgA, f.Client1, name).Scan(&id); err != nil {
			t.Fatalf("seed asset %s: %v", name, err)
		}
		for _, stmt := range []struct {
			what string
			sql  string
			args []any
		}{
			{"CIs", `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, asset_id)
				SELECT $1, $2, $3, $4 || '-ci-' || i, 'active', $5 FROM generate_series(1, $6::int) AS i`,
				[]any{f.OrgA, f.Client1, serverType, name, id, n}},
			{"children", `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, parent_ci_id, child_mode)
				SELECT $1, $2, $3, p.name || '-child', 'active', p.id, 'standalone' FROM ci p WHERE p.organization_id = $1 AND p.asset_id = $4`,
				[]any{f.OrgA, f.Client1, serverType, id}},
			{"movements", `INSERT INTO stock_movement (organization_id, item_kind, asset_id, movement_type, occurred_at)
				SELECT $1, 'asset', $2, 'receipt', now() - make_interval(hours => i) FROM generate_series(1, 3) AS i`,
				[]any{f.OrgA, id}},
			{"documents", `WITH d AS (INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
					SELECT $1, $2 || '-doc-' || i, $2 || '-doc-' || i || '.pdf', '', $3 FROM generate_series(1, 2) AS i RETURNING id)
				INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) SELECT $1, d.id, 'asset', $4 FROM d`,
				[]any{f.OrgA, name, uploader, id}},
		} {
			if _, err := f.Admin.Exec(bg, stmt.sql, stmt.args...); err != nil {
				t.Fatalf("seed %s of %s: %v", stmt.what, name, err)
			}
		}
		return id
	}
	small, large := seed("wp824-small", 10), seed("wp824-large", maxPage)
	if _, err := f.Admin.Exec(bg, `ANALYZE ci, asset, stock_movement, document, document_link`); err != nil {
		t.Fatal(err)
	}

	trace := scopetest.NewQueryTrace(t, f.App)
	h := NewHandler(ci.NewPGRepository(trace.Pool), relationship.NewPGRepository(trace.Pool)).
		WithEntitlements(entitled{FeatureCMDBCore: true, entitlement.FeatureAssetsBasic: true, entitlement.FeatureDocuments: true}).
		WithSources(&Sources{Assets: asset.NewPGRepository(trace.Pool), Movements: movement.NewPGRepository(trace.Pool),
			Documents: document.NewPGRepository(trace.Pool)}).
		WithModules(modules{ModuleInventory: true, ModuleDocuments: true})

	// aggregate sends the detail of the asset with maxPage CIs and children
	// as a session of organization A and returns the answer. The selection
	// of the lists is narrow: with every field two lists of maxPage entries
	// exceed MaxComplexity.
	aggregate := func(id string) (assetResponse, error) {
		query := `{ asset(id: "` + id + `") { ` + assetFields + ` cis(first: ` + itoa(maxPage) + `) { id name assetId status }` +
			` children(first: ` + itoa(maxPage) + `) { id name parentCiId childMode }` +
			` movements(first: 20) { nodes { ` + movementFields + ` } totalCount } documents(first: 20) { ` + documentFields + ` } } }`
		req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(gql(query)))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Permissions: assetRead,
			Type: identity.PrincipalTypeUser})
		ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(assetRead))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req.WithContext(ctx))
		var out assetResponse
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s: %d %s", Path, w.Code, w.Body.String())
		}
		err := json.Unmarshal(w.Body.Bytes(), &out)
		return out, err
	}
	// statements returns the statements of the aggregate of the asset with
	// cis CIs and children and, of them, the counts of the CI lists: a page
	// shorter than its limit (maxPage) gives the count itself (WP-839), a
	// full one counts once more, per list, never per CI.
	statements := func(id string, cis int) (all, counts int) {
		t.Helper()
		var resp assetResponse
		stmts := trace.Run(t, func() error {
			var err error
			resp, err = aggregate(id)
			return err
		})
		if len(resp.Errors) != 0 {
			t.Fatalf("asset %s: errors %+v", id, resp.Errors)
		}
		a, _ := resp.Data["asset"].(map[string]any)
		movements, _ := a["movements"].(map[string]any)
		docs, _ := a["documents"].([]any)
		if got, kids := len(names(t, a["cis"])), len(names(t, a["children"])); got != cis || kids != cis || movements["totalCount"] != 3.0 || len(docs) != 2 {
			t.Fatalf("asset %s: %d CIs, %d children, movements %v, %d documents; want %d, %d, 3 and 2", id, got, kids, movements["totalCount"], len(docs), cis, cis)
		}
		for _, s := range stmts {
			if strings.HasPrefix(s.SQL, "SELECT COUNT(*) FROM ci WHERE") {
				counts++
			}
		}
		return len(stmts), counts
	}
	// Warm the pool: the first connection switches its role.
	statements(small, 10)
	ten, tenCounts := statements(small, 10)
	hundred, hundredCounts := statements(large, maxPage)
	if tenCounts != 0 || hundredCounts != 2 {
		t.Errorf("asset aggregate: %d and %d counts of the CI lists for 10 and %d CIs and children, want 0 and 2 (one per full list)",
			tenCounts, hundredCounts, maxPage)
	}
	if ten != hundred-hundredCounts {
		t.Errorf("asset aggregate: %d statements for 10 CIs and children, %d for %d of which %d count full lists (N+1)",
			ten, hundred, maxPage, hundredCounts)
	}
	// Five reads, each in a transaction of its own (begin with the tenant
	// context, reads, commit): the asset; the CIs of the asset (page, the
	// count of a full page, overrides and asset facts of the page, one
	// statement each); their children (page, count, overrides); the
	// movements (count, page); the documents (count, page).
	const aggregateBudget = 3 + 6 + 5 + 4 + 4
	if hundred > aggregateBudget {
		t.Errorf("asset aggregate: %d statements, budget %d", hundred, aggregateBudget)
	}
	t.Logf("asset aggregate: %d statements for 10 CIs and children, %d for %d (%d counts of full lists)", ten, hundred, maxPage, hundredCounts)
	scopetest.TimeBudget(t, "asset aggregate of 100 CIs and children", 5, 300*time.Millisecond, func() error {
		_, err := aggregate(large)
		return err
	})
}
