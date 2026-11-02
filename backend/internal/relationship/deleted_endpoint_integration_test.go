package relationship_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestCreateRefusesDeletedEndpoints covers WP-695 (REL-01, CI-05; F-424): a
// soft-deleted CI is no endpoint of a new edge. POST /api/v1/relationships
// to a deleted target or from a deleted source answers 422 with the
// violation on target_ci_id or source_ci_id, as for an unknown CI (F-145),
// and stores nothing; the repository refuses the deleted end with
// ErrSourceCINotFound or ErrTargetCINotFound also on the discovery path
// (without the type list check). The control between live CIs is 201.
// Before the fix the edge to the deleted CI was stored (201).
//
// Retest: cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… go test -count=1 -run '^TestCreateRefusesDeletedEndpoints$' ./internal/relationship/
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCreateRefusesDeletedEndpoints(t *testing.T) {
	f := scopetest.Seed(t, "6f")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wp695-admin")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	live, other := f.CI(t, f.OrgA, "", "wp695-live"), f.CI(t, f.OrgA, "", "wp695-other")
	gone := f.CI(t, f.OrgA, "", "wp695-gone")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now(), status = 'decommissioned' WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	repo := relationship.NewPGRepository(f.App)
	mux := chi.NewRouter()
	relationship.NewHandler(repo, relationshiptype.NewPGRepository(f.App)).RegisterRoutes(mux)
	edges := func(id string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 OR target_ci_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for _, c := range []struct {
		name, source, target string
		status               int
		fields               []string
	}{
		{"deleted target", live, gone, http.StatusUnprocessableEntity, []string{"target_ci_id"}},
		{"deleted source", gone, live, http.StatusUnprocessableEntity, []string{"source_ci_id"}},
		{"between live CIs (control)", live, other, http.StatusCreated, nil},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/relationships", bytes.NewBufferString(
			`{"source_ci_id":"`+c.source+`","target_ci_id":"`+c.target+`","rel_type":"connected_to"}`)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var problem struct {
			Violations []struct {
				Field string `json:"field"`
			} `json:"violations"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		var fields []string
		for _, v := range problem.Violations {
			fields = append(fields, v.Field)
		}
		if w.Code != c.status || !slices.Equal(fields, c.fields) {
			t.Errorf("POST /relationships, %s: %d %s, want %d with violations %v", c.name, w.Code, w.Body.String(), c.status, c.fields)
		}
	}

	// The discovery path (no type list check) refuses the deleted end too.
	err := repo.Create(ctx, &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: other, TargetCIID: gone,
		RelType: "connected_to", Source: "discovery"})
	if !errors.Is(err, relationship.ErrTargetCINotFound) {
		t.Errorf("discovery edge to the deleted CI: %v, want ErrTargetCINotFound", err)
	}
	if n := edges(gone); n != 0 {
		t.Errorf("edges of the deleted CI: %d, want 0", n)
	}
	if n := edges(live); n != 1 {
		t.Errorf("edges of the live CI: %d, want the control edge only", n)
	}
}
