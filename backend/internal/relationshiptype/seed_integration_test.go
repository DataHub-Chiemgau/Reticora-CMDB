package relationshiptype_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
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
		"uplink_to":         {true, "network", relationshiptype.ImpactSourceDependsOnTarget, true}, // redundant since WP-1026 (E-199)
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

// TestRelationshipTypesREL06 covers REL-06 (WP-294, E-125) against
// PostgreSQL: the five seeds of REL-06 with their impact direction in the
// database, in Catalogue() and in relationship.ValidRelTypes; the
// cardinality and undirected-only-in-network contract over the API (400)
// and the CHECKs of migration 000202; 409 for the key of a system type in
// PG and memory; an organization row with a system key from before WP-294
// resolved once, before the system row, in List (with total), GetByKey and
// ListAll; the delete protection (409 for a type an edge or a suppression
// uses, 204 unused, 404 system or unknown); ListAll without a page bound
// at 501 organization types.
//
// Retest: cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… go test -count=1 -run '^TestRelationshipTypesREL06$' ./internal/relationshiptype/
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipTypesREL06(t *testing.T) {
	f := scopetest.Seed(t, "90")
	bg := context.Background()
	repo := relationshiptype.NewPGRepository(f.App)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	// The seeds of REL-06.
	all, err := repo.ListAll(ctx, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]relationshiptype.Type{}
	for _, typ := range all {
		byKey[typ.Key] = typ
	}
	catalogue := map[string]relationshiptype.Type{}
	for _, c := range relationshiptype.Catalogue() {
		catalogue[c.Key] = c
	}
	for key, impact := range map[string]string{
		"managed_by":   relationshiptype.ImpactNone,
		"backed_up_by": relationshiptype.ImpactNone,
		"monitored_by": relationshiptype.ImpactNone,
		"uses":         relationshiptype.ImpactSourceDependsOnTarget,
		"belongs_to":   relationshiptype.ImpactSourceDependsOnTarget,
	} {
		got, ok := byKey[key]
		if !ok || !got.IsSystem || got.OrganizationID != "" || !got.Directed || got.Category != "logical" ||
			got.ImpactDirection != impact || got.Cardinality != relationshiptype.CardinalityManyToMany {
			t.Errorf("seed %s = %+v, want a directed logical system type with %s", key, got, impact)
		}
		if c, inCatalogue := catalogue[key]; !inCatalogue || c.ImpactDirection != impact || c.ReverseName != got.ReverseName ||
			c.DisplayName != got.DisplayName {
			t.Errorf("catalogue %s = %+v, database %+v", key, c, got)
		}
	}
	if m, b := byKey["monitored_by"], byKey["belongs_to"]; m.DisplayName != "monitored by" || m.ReverseName != "monitors" ||
		b.DisplayName != "belongs to" || b.ReverseName != "has member" {
		t.Errorf("names of the new seeds: %+v, %+v", m, b)
	}
	for key := range catalogue {
		if !relationship.ValidRelTypes[key] {
			t.Errorf("%s is in Catalogue() but not in relationship.ValidRelTypes", key)
		}
	}
	for key := range relationship.ValidRelTypes {
		if _, inCatalogue := catalogue[key]; !inCatalogue {
			t.Errorf("%s is in relationship.ValidRelTypes but not in Catalogue()", key)
		}
	}

	// The contract over the API and in the database.
	mux := chi.NewRouter()
	relationshiptype.NewHandler(repo).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"unknown cardinality", http.MethodPost, "/api/v1/relationship-types", `{"key":"rel06_a","display_name":"a","reverse_name":"a","cardinality":"n:m"}`, http.StatusBadRequest},
		{"undirected without network", http.MethodPost, "/api/v1/relationship-types", `{"key":"rel06_b","display_name":"b","reverse_name":"b","directed":false}`, http.StatusBadRequest},
		{"undirected in power", http.MethodPost, "/api/v1/relationship-types", `{"key":"rel06_c","display_name":"c","reverse_name":"c","directed":false,"category":"power"}`, http.StatusBadRequest},
		{"key of a system type", http.MethodPost, "/api/v1/relationship-types", `{"key":"depends_on","display_name":"d","reverse_name":"d"}`, http.StatusConflict},
		{"key of a new seed", http.MethodPost, "/api/v1/relationship-types", `{"key":"monitored_by","display_name":"m","reverse_name":"m","impact_direction":"none"}`, http.StatusConflict},
		{"one_to_one", http.MethodPost, "/api/v1/relationship-types", `{"key":"rel06_pair","display_name":"pairs","reverse_name":"paired","cardinality":"one_to_one"}`, http.StatusCreated},
		{"change to a bad cardinality", http.MethodPatch, "/api/v1/relationship-types/rel06_pair", `{"cardinality":"1:1"}`, http.StatusBadRequest},
		{"change a system type", http.MethodPatch, "/api/v1/relationship-types/monitored_by", `{"description":"x"}`, http.StatusNotFound},
	} {
		if got := call(tc.method, tc.path, tc.body); got.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.name, got.Code, got.Body.String(), tc.status)
		}
	}
	for name, stmt := range map[string]string{
		"relationship_type_cardinality_check": `INSERT INTO relationship_type (organization_id, key, display_name, reverse_name, cardinality)
			VALUES ($1, 'rel06_x1', 'x', 'x', 'n:m')`,
		"relationship_type_undirected_network_check": `INSERT INTO relationship_type (organization_id, key, display_name, reverse_name, directed, impact_direction, category)
			VALUES ($1, 'rel06_x2', 'x', 'x', false, 'undirected', 'logical')`,
	} {
		_, err = f.Admin.Exec(bg, stmt, f.OrgA)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.ConstraintName != name {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Memory refuses the key of a system type as well.
	memory := relationshiptype.NewMemoryRepository()
	if err = memory.Create(bg, &relationshiptype.Type{OrganizationID: f.OrgA, Key: "belongs_to", DisplayName: "b", ReverseName: "b",
		Directed: true, Category: "logical", ImpactDirection: relationshiptype.ImpactNone}); !errors.Is(err, relationshiptype.ErrConflict) {
		t.Errorf("memory create with a system key: %v, want ErrConflict", err)
	}

	// An organization row with a system key from before WP-294 resolves
	// once, before the system row.
	if _, err = f.Admin.Exec(bg, `INSERT INTO relationship_type (organization_id, key, display_name, reverse_name, impact_direction)
		VALUES ($1, 'uses', 'uses (own)', 'used by (own)', 'none')`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	var distinct int
	if err = f.Admin.QueryRow(bg, `SELECT count(DISTINCT key) FROM relationship_type WHERE organization_id IS NULL OR organization_id = $1`,
		f.OrgA).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	listed, total, err := repo.List(ctx, f.OrgA, api.PaginationParams{Limit: 1000})
	if err != nil || total != distinct || len(listed) != distinct {
		t.Fatalf("list: %d of total %d, %d keys, %v", len(listed), total, distinct, err)
	}
	ownUses := func(where string, types []relationshiptype.Type) {
		t.Helper()
		var found []relationshiptype.Type
		for _, typ := range types {
			if typ.Key == "uses" {
				found = append(found, typ)
			}
		}
		if len(found) != 1 || found[0].OrganizationID != f.OrgA || found[0].ImpactDirection != relationshiptype.ImpactNone {
			t.Errorf("%s: uses = %+v, want the organization row once", where, found)
		}
	}
	ownUses("List", listed)
	all, err = repo.ListAll(ctx, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	ownUses("ListAll", all)
	if got, gerr := repo.GetByKey(ctx, f.OrgA, "uses"); gerr != nil {
		t.Error(gerr)
	} else {
		ownUses("GetByKey", []relationshiptype.Type{*got})
	}
	// Paging runs over the resolved keys.
	if page, _, perr := repo.List(ctx, f.OrgA, api.PaginationParams{Limit: 5, Offset: distinct - 2}); perr != nil || len(page) != 2 {
		t.Errorf("last page: %d types, %v", len(page), perr)
	}

	// Delete protection.
	for _, key := range []string{"rel06_edge", "rel06_suppressed", "rel06_free"} {
		if w := call(http.MethodPost, "/api/v1/relationship-types", `{"key":"`+key+`","display_name":"x","reverse_name":"x"}`); w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", key, w.Code, w.Body.String())
		}
	}
	src, dst := f.CI(t, f.OrgA, "", "rel06-a"), f.CI(t, f.OrgA, "", "rel06-b")
	if _, err = f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
		VALUES ($1, $2, $3, 'rel06_edge')`, f.OrgA, src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id)
		SELECT $1, $2, $3, id FROM relationship_type WHERE organization_id = $1 AND key = 'rel06_suppressed'`, f.OrgA, src, dst); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		key    string
		status int
	}{
		{"rel06_edge", http.StatusConflict},
		{"rel06_suppressed", http.StatusConflict},
		{"rel06_free", http.StatusNoContent},
		{"depends_on", http.StatusNotFound},
		{"rel06_unknown", http.StatusNotFound},
	} {
		if w := call(http.MethodDelete, "/api/v1/relationship-types/"+c.key, ""); w.Code != c.status {
			t.Errorf("delete %s: %d %s, want %d", c.key, w.Code, w.Body.String(), c.status)
		}
	}
	for _, key := range []string{"rel06_edge", "rel06_suppressed"} {
		if _, gerr := repo.GetByKey(ctx, f.OrgA, key); gerr != nil {
			t.Errorf("%s after the refused delete: %v", key, gerr)
		}
	}
	if err = memory.Delete(bg, f.OrgA, "depends_on"); !errors.Is(err, relationshiptype.ErrNotFound) {
		t.Errorf("memory delete of a system type: %v, want ErrNotFound", err)
	}

	// ListAll has no page bound: 501 organization types are all read.
	if _, err = f.Admin.Exec(bg, `INSERT INTO relationship_type (organization_id, key, display_name, reverse_name)
		SELECT $1, 'rel06_bulk_' || i, 'bulk', 'bulk' FROM generate_series(1, 501) AS i`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	all, err = repo.ListAll(ctx, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	bulk := 0
	for _, typ := range all {
		if strings.HasPrefix(typ.Key, "rel06_bulk_") {
			bulk++
		}
	}
	// distinct keys before, rel06_edge and rel06_suppressed, the bulk.
	if bulk != 501 || len(all) != distinct+2+501 {
		t.Errorf("ListAll: %d bulk types of 501, %d types in all, want %d", bulk, len(all), distinct+2+501)
	}
}
