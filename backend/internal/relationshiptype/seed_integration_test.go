package relationshiptype_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestRelationshipTypeSeedsFollowREL01 covers REL-01 and CH27 (WP-127,
// E-18) against PostgreSQL: the system seeds carry the REL-01 fields with
// the impact semantics of the catalogue (the same in the database and in
// Go); connected_to is undirected; the database refuses an invalid
// combination; custom types follow the same contract over the API; the
// structural keys are not storable.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipTypeSeedsFollowREL01(t *testing.T) {
	f := scopetest.Seed(t, "90")
	bg := context.Background()
	repo := relationshiptype.NewPGRepository(f.App)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	types, _, err := repo.List(ctx, f.OrgA, api.PaginationParams{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]relationshiptype.Type{}
	for _, typ := range types {
		byKey[typ.Key] = typ
	}
	// The REL-01 seeds with their impact semantics.
	for key, want := range map[string]struct {
		directed  bool
		category  string
		impact    string
		redundant bool
	}{
		"connected_to":      {false, "network", relationshiptype.ImpactUndirected, false},
		"powered_by":        {true, "power", relationshiptype.ImpactSourceDependsOnTarget, true},
		"hosted_on":         {true, "hosting", relationshiptype.ImpactSourceDependsOnTarget, false},
		"runs_on":           {true, "hosting", relationshiptype.ImpactSourceDependsOnTarget, false},
		"member_of_cluster": {true, "logical", relationshiptype.ImpactSourceDependsOnTarget, true},
		"depends_on":        {true, "logical", relationshiptype.ImpactSourceDependsOnTarget, false},
		"uplink_to":         {true, "network", relationshiptype.ImpactSourceDependsOnTarget, false},
	} {
		got, ok := byKey[key]
		if !ok || !got.IsSystem || got.Directed != want.directed || got.Category != want.category ||
			got.ImpactDirection != want.impact || got.Redundant != want.redundant {
			t.Errorf("%s = %+v, want %+v", key, got, want)
		}
	}
	// Database seeds and the Go catalogue agree, and every seed is valid.
	for _, c := range relationshiptype.Catalogue() {
		got := byKey[c.Key]
		if got.DisplayName != c.DisplayName || got.ReverseName != c.ReverseName || got.Directed != c.Directed ||
			got.Category != c.Category || got.ImpactDirection != c.ImpactDirection || got.Redundant != c.Redundant {
			t.Errorf("seed %s: database %+v, catalogue %+v", c.Key, got, c)
		}
		if verr := c.Validate(); verr != nil {
			t.Errorf("catalogue %s: %v", c.Key, verr)
		}
	}

	// The database holds the contract on its own.
	for name, stmt := range map[string]string{
		"unknown category":            `INSERT INTO relationship_type (organization_id, key, display_name, reverse_name, category) VALUES ($1, 'rel01_x1', 'x', 'x', 'grouping')`,
		"unknown impact direction":    `INSERT INTO relationship_type (organization_id, key, display_name, reverse_name, impact_direction) VALUES ($1, 'rel01_x2', 'x', 'x', 'both')`,
		"directed but undirected":     `INSERT INTO relationship_type (organization_id, key, display_name, reverse_name, impact_direction) VALUES ($1, 'rel01_x3', 'x', 'x', 'undirected')`,
		"undirected with a direction": `INSERT INTO relationship_type (organization_id, key, display_name, reverse_name, directed) VALUES ($1, 'rel01_x4', 'x', 'x', false)`,
	} {
		if _, err = f.Admin.Exec(bg, stmt, f.OrgA); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	// Custom types over the API.
	mux := chi.NewRouter()
	relationshiptype.NewHandler(repo).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	w := call(http.MethodPost, "/api/v1/relationship-types", `{"key":"rel01_feeds","display_name":"feeds","reverse_name":"fed by"}`)
	var created relationshiptype.Type
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if w.Code != http.StatusCreated || !created.Directed || created.Category != "logical" ||
		created.ImpactDirection != relationshiptype.ImpactSourceDependsOnTarget || created.IsSystem {
		t.Errorf("custom type with defaults: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodPost, "/api/v1/relationship-types", `{"key":"rel01_peers","display_name":"peers with","reverse_name":"peers with","directed":false,"category":"network"}`)
	if w.Code != http.StatusCreated || !bytes.Contains(w.Body.Bytes(), []byte(`"impact_direction":"undirected"`)) {
		t.Errorf("undirected custom type: %d %s", w.Code, w.Body.String())
	}
	for name, tc := range map[string]struct {
		method, path, body string
		status             int
	}{
		"unknown category":     {http.MethodPost, "/api/v1/relationship-types", `{"key":"rel01_a","display_name":"a","reverse_name":"a","category":"grouping"}`, http.StatusBadRequest},
		"undirected impact":    {http.MethodPost, "/api/v1/relationship-types", `{"key":"rel01_b","display_name":"b","reverse_name":"b","impact_direction":"undirected"}`, http.StatusBadRequest},
		"structural key":       {http.MethodPost, "/api/v1/relationship-types", `{"key":"mounted_in","display_name":"m","reverse_name":"m"}`, http.StatusBadRequest},
		"taken key":            {http.MethodPost, "/api/v1/relationship-types", `{"key":"rel01_feeds","display_name":"f","reverse_name":"f"}`, http.StatusConflict},
		"former field names":   {http.MethodPost, "/api/v1/relationship-types", `{"key":"rel01_c","forward_label":"c","reverse_label":"c"}`, http.StatusBadRequest},
		"change impact":        {http.MethodPatch, "/api/v1/relationship-types/rel01_feeds", `{"impact_direction":"target_depends_on_source","redundant":true}`, http.StatusOK},
		"invalid change":       {http.MethodPatch, "/api/v1/relationship-types/rel01_feeds", `{"directed":false}`, http.StatusBadRequest},
		"change a system type": {http.MethodPatch, "/api/v1/relationship-types/powered_by", `{"redundant":false}`, http.StatusNotFound},
	} {
		if got := call(tc.method, tc.path, tc.body); got.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", name, got.Code, got.Body.String(), tc.status)
		}
	}
	feeds, err := repo.GetByKey(ctx, f.OrgA, "rel01_feeds")
	if err != nil || feeds.ImpactDirection != relationshiptype.ImpactTargetDependsOnSource || !feeds.Redundant || !feeds.Directed {
		t.Errorf("after the changes: %+v %v", feeds, err)
	}

	// Structural relationships are projected, never stored (CH27, WP-079).
	src, dst := f.CI(t, f.OrgA, "", "rel01-a"), f.CI(t, f.OrgA, "", "rel01-b")
	for _, key := range relationshiptype.StructuralKeys {
		if _, err = f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			VALUES ($1, $2, $3, $4)`, f.OrgA, src, dst, key); err == nil {
			t.Errorf("stored a %s relationship", key)
		}
	}
}
