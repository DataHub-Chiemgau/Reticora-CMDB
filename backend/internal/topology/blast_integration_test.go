package topology

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestServiceChainPostgres covers IMP-08 (WP-297) against PostgreSQL: the
// system types business_service and application of migration 000104 and
// a subtype of the organization are services, read through
// citype.PGRepository; a type of another organization stays invisible. In
// the client and site scope (IMP-07) a service outside the scope appears
// neither in affected_services nor in service_spofs, nor does a CI outside
// it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestServiceChainPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b8")
	bg := context.Background()
	typeOf := func(key string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatalf("system type %s: %v", key, err)
		}
		return id
	}
	application := typeOf("application")
	orgType := func(org, key string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_type (organization_id, key, name, parent_type_id) VALUES ($1, $2, $2, $3)
			RETURNING id::text`, org, key, application).Scan(&id); err != nil {
			t.Fatalf("org type %s: %v", key, err)
		}
		return id
	}
	webapp := orgType(f.OrgA, "wp297-webapp")
	foreignApp := orgType(f.OrgB, "wp297-foreign-app")

	site1, site2 := f.ID(), f.ID()
	for site, client := range map[string]string{site1: f.Client1, site2: f.Client2} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
			site, f.OrgA, client, "wp297 "+site); err != nil {
			t.Fatal(err)
		}
	}
	add := func(name, client, site, typeID string) string {
		t.Helper()
		id := f.CI(t, f.OrgA, client, name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $2, location_id = $3 WHERE id = $1`, id, typeID, site); err != nil {
			t.Fatalf("type %s: %v", name, err)
		}
		return id
	}
	sw := add("wp297-switch", f.Client1, site1, typeOf("switch"))
	vm := add("wp297-vm", f.Client1, site1, typeOf("vm"))
	app1 := add("wp297-app-1", f.Client1, site1, application)
	bs1 := add("wp297-service-1", f.Client1, site1, typeOf("business_service"))
	web := add("wp297-web", f.Client1, site1, webapp)
	app2 := add("wp297-app-2", f.Client2, site2, application)
	bs2 := add("wp297-service-2", f.Client2, site2, typeOf("business_service"))
	for _, e := range [][3]string{
		{vm, "hosted_on", sw}, {app1, "runs_on", vm}, {bs1, "depends_on", app1}, {web, "runs_on", vm},
		{app2, "runs_on", vm}, {bs2, "depends_on", app2},
	} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, $4)`,
			f.OrgA, e[0], e[2], e[1]); err != nil {
			t.Fatal(err)
		}
	}

	h := NewHandler(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).WithCITypes(citype.NewPGRepository(f.App))
	services, err := h.serviceTypes(f.OrgCtx(f.OrgA), f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	if !services[application] || !services[typeOf("business_service")] || !services[webapp] || services[foreignApp] ||
		services[typeOf("vm")] {
		t.Errorf("service types of organization A: %v", services)
	}
	if services, err = h.serviceTypes(f.OrgCtx(f.OrgB), f.OrgB); err != nil || !services[foreignApp] || services[webapp] {
		t.Errorf("service types of organization B: %v, %v", services, err)
	}

	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(site1)
	scopes := map[string]context.Context{
		"organization": f.OrgCtx(f.OrgA),
		"client 1":     f.ClientCtx(f.Client1),
		"site 1":       database.ContextWithTenantScope(bg, &siteScope),
	}
	type answer struct {
		AffectedServices []AffectedService `json:"affected_services"`
		ServiceSPOFs     []ServiceSPOF     `json:"service_spofs"`
	}
	for name, scope := range scopes {
		ctx := tenant.WithTenant(scope, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		wantServices := []string{app1, bs1, web}
		wantSPOF := []string{sw, vm, app1}
		if name == "organization" {
			wantServices = append(wantServices, app2, bs2)
			wantSPOF = append(wantSPOF, app2)
		}
		for _, path := range []string{"/api/v1/cis/" + sw + "/blast-radius", "/api/v1/impact/" + sw + "?include=services,spof"} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
			var a answer
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &a) != nil {
				t.Fatalf("%s %s: %d %s", name, path, w.Code, w.Body.String())
			}
			var gotServices, gotSPOF []string
			for _, s := range a.AffectedServices {
				gotServices = append(gotServices, s.ID)
			}
			for _, s := range a.ServiceSPOFs {
				gotSPOF = append(gotSPOF, s.ID)
				for _, id := range s.ServiceIDs {
					if !slices.Contains(wantServices, id) {
						t.Errorf("%s %s: SPOF %s names service %s outside the scope", name, path, s.Name, id)
					}
				}
			}
			slices.Sort(gotServices)
			slices.Sort(gotSPOF)
			if !slices.Equal(gotServices, slices.Sorted(slices.Values(wantServices))) ||
				!slices.Equal(gotSPOF, slices.Sorted(slices.Values(wantSPOF))) {
				t.Errorf("%s %s: services %v (want %v), SPOF %v (want %v)", name, path, gotServices, wantServices, gotSPOF, wantSPOF)
			}
		}
	}
}
