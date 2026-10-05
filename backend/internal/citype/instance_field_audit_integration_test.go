package citype_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestInstanceFieldChangesAreAudited covers WP-056 (MET-14): creating,
// changing and deleting an instance attribute definition through the
// handler writes an audit entry on the CI with the acting user, and the
// definition names are what discovery leaves alone.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInstanceFieldChangesAreAudited(t *testing.T) {
	f := scopetest.Seed(t, "5b")
	bg := context.Background()
	repo := citype.NewPGRepository(f.App)
	ciID := f.CI(t, f.OrgA, f.Client1, "inst-audit")
	user := f.AppUser(t, f.OrgA, "inst-admin")

	mux := chi.NewRouter()
	citype.NewHandler(repo).RegisterRoutes(mux)
	call := func(method, path, body string) int {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		scope := database.OrgWideScope(f.OrgA, user)
		ctx := tenant.WithTenant(database.ContextWithTenantScope(req.Context(), &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w.Code
	}
	path := "/api/v1/cis/" + ciID + "/field-definitions"
	if code := call(http.MethodPut, path, `{"name":"rack_label","data_type":"string","label":"Rack"}`); code != http.StatusOK {
		t.Fatalf("create instance field: status %d", code)
	}
	if code := call(http.MethodPut, path, `{"name":"rack_label","data_type":"string","label":"Rack label"}`); code != http.StatusOK {
		t.Fatalf("change instance field: status %d", code)
	}
	names, err := repo.InstanceFieldNames(f.OrgCtx(f.OrgA), f.OrgA, ciID)
	if err != nil || len(names) != 1 || names[0] != "rack_label" {
		t.Errorf("instance field names: %v, %v", names, err)
	}
	if code := call(http.MethodDelete, path+"/rack_label", ""); code != http.StatusNoContent {
		t.Fatalf("delete instance field: status %d", code)
	}
	if code := call(http.MethodDelete, path+"/rack_label", ""); code != http.StatusNotFound {
		t.Errorf("delete a missing instance field: status %d, want 404", code)
	}

	rows, err := f.Admin.Query(bg, `SELECT action, changes->>'field' FROM audit_log
		WHERE organization_id = $1 AND resource_type = 'ci' AND resource_id = $2 AND actor_id = $3 AND action LIKE 'ci_instance_field.%'
		ORDER BY timestamp`, f.OrgA, ciID, user)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var action, field string
		if err = rows.Scan(&action, &field); err != nil {
			t.Fatal(err)
		}
		got = append(got, action+":"+field)
	}
	want := []string{"ci_instance_field.created:rack_label", "ci_instance_field.updated:rack_label", "ci_instance_field.deleted:rack_label"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("audit entries %v, want %v", got, want)
	}
}
