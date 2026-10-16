// Phase-1 acceptance audit, group T6-epic-b – REGRESSION test (must pass).
//
// WP-108 (RCK-01, TEN-05, TEN-09)
// Gap (high) "Rack height reduction must not orphan mounts", negative tenant
// part: a client-scoped principal PATCHing the rack of another client gets 404
// and the rack stays unchanged; the owning client's principal may change it.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -count=1 -v -run 'TestAuditT6RackForeignClient' ./internal/rack/
package rack_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditT6RackForeignClientPatchIsNotFound(t *testing.T) {
	f := scopetest.Seed(t, "dc")
	bg := context.Background()
	repo := rack.NewPGRepository(f.App)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	room := f.Room(t, f.Client1, "audit-dc-room")
	rk := &rack.Rack{OrganizationID: f.OrgA, RoomID: room, Name: "audit-dc-rack", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}
	if err := repo.CreateRack(orgCtx, rk); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	rack.NewHandler(repo).RegisterRoutes(mux)
	patch := func(ctx context.Context, body string) int {
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/racks/"+rk.ID, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	stored := func() (int, string) {
		var h int
		var name string
		if err := f.Admin.QueryRow(bg, `SELECT height_units, name FROM rack WHERE id = $1`, rk.ID).Scan(&h, &name); err != nil {
			t.Fatal(err)
		}
		return h, name
	}
	client := func(id string) context.Context {
		return tenant.WithTenant(f.ClientCtx(id), tenant.TenantInfo{OrganizationID: f.OrgA, ClientID: id, UserID: f.User})
	}

	if code := patch(client(f.Client2), `{"height_units":10,"name":"hijacked"}`); code != http.StatusNotFound {
		t.Errorf("client-2 principal PATCH of a client-1 rack = %d, want 404", code)
	}
	if h, name := stored(); h != 42 || name != "audit-dc-rack" {
		t.Errorf("rack after foreign PATCH: height %d name %q, want 42 audit-dc-rack", h, name)
	}
	if code := patch(client(f.Client1), `{"height_units":40}`); code != http.StatusOK {
		t.Errorf("client-1 principal PATCH of its rack = %d, want 200", code)
	}
	if h, _ := stored(); h != 40 {
		t.Errorf("rack height after own PATCH = %d, want 40", h)
	}
}
