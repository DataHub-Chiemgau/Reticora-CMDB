package ci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// TestAssetFreeCIAgainstPostgres covers CI-09 (WP-178) against PostgreSQL:
// a logical CI without an asset takes part in relationships, search,
// topology, impact and history like any other CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssetFreeCIAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b7")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	cis := ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()), search.NewCIIndexer(search.NewPGRepository(f.App)))
	rels := relationship.NewPGRepository(f.App)

	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	app := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: typeID, Name: "assetfree-billing-b7", Status: "active", IsManual: true}
	if err := cis.Create(ctx, app); err != nil {
		t.Fatal(err)
	}
	host := f.CI(t, f.OrgA, f.Client1, "assetfree-host-b7")
	if err := rels.Create(ctx, &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: app.ID, TargetCIID: host, RelType: "runs_on", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	stored, err := cis.GetByID(ctx, f.OrgA, app.ID)
	if err != nil || stored.AssetID != "" {
		t.Fatalf("asset-free CI: %+v %v", stored, err)
	}

	// Relationships.
	if list, _, lerr := rels.List(ctx, f.OrgA, app.ID, api.PaginationParams{Limit: 10}); lerr != nil || len(list) != 1 || list[0].TargetCIID != host {
		t.Errorf("relationships: %+v %v", list, lerr)
	}
	// Search.
	found, err := search.NewPGRepository(f.App).Query(ctx, search.Query{OrganizationID: f.OrgA, UserID: f.User, Text: "assetfree-billing-b7", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	for i := range found.Data {
		hit = hit || found.Data[i].EntityID == app.ID
	}
	if !hit {
		t.Errorf("search: %+v", found.Data)
	}
	// Topology and impact.
	mux := chi.NewRouter()
	topology.NewHandler(cis, rels).WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).RegisterRoutes(mux)
	get := func(path string, out any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), out) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	var graph topology.Graph
	get("/api/v1/topology?root_ci_id="+app.ID, &graph)
	inGraph := map[string]bool{}
	for _, n := range graph.Nodes {
		inGraph[n.ID] = true
	}
	if !inGraph[app.ID] || !inGraph[host] || len(graph.Edges) != 1 {
		t.Errorf("topology: %+v", graph)
	}
	var impact struct {
		Impacted []topology.ImpactNode `json:"impacted"`
	}
	get("/api/v1/impact/"+host, &impact)
	if len(impact.Impacted) != 1 || impact.Impacted[0].ID != app.ID || impact.Impacted[0].Heuristic {
		t.Errorf("impact of the host: %+v", impact.Impacted)
	}
	// History.
	name := "assetfree-billing-b7-renamed"
	if _, err = cis.Update(ctx, f.OrgA, app.ID, ci.UpdateRequest{Name: &name}); err != nil {
		t.Fatal(err)
	}
	changes, total, err := history.NewPGRepository(f.App).List(ctx, f.OrgA, "ci", app.ID, api.PaginationParams{Limit: 20})
	if err != nil || total == 0 || len(changes) == 0 {
		t.Errorf("history: %d entries, %v", total, err)
	}
}
