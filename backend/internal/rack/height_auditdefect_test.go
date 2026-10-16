//go:build auditdefect

// Phase-1 acceptance audit, group T6-epic-b – DEFECT test (must fail until fixed).
//
// WP-108 (RCK-01, LOC-*)
// Finding P2 "PATCH height_units accepts a height below existing mounts (mounts
// end up outside the rack)": rack/pg_repository.go UpdateRack writes
// height_units without comparing it to max(position_u + height_u - 1) of the
// rack's mounts. RCK-01: the server keeps position_u + height_u − 1 ≤
// rack.height_units (409).
// Gap (high): rack height reduction must not orphan mounts.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -run 'TestAuditDefectRackHeight' ./internal/rack/
package rack_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditDefectRackHeightNotBelowMounts(t *testing.T) {
	f := scopetest.Seed(t, "db")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := rack.NewPGRepository(f.App)
	room := f.Room(t, f.Client1, "audit-db-room")

	mounted := &rack.Rack{OrganizationID: f.OrgA, RoomID: room, Name: "audit-db-mounted", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}
	if err := repo.CreateRack(ctx, mounted); err != nil {
		t.Fatal(err)
	}
	ci := f.CI(t, f.OrgA, f.Client1, "audit-db-ci")
	if err := repo.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: mounted.ID, CIID: ci, PositionU: 40, HeightU: 2, Face: "front"}); err != nil {
		t.Fatal(err)
	}
	empty := &rack.Rack{OrganizationID: f.OrgA, RoomID: room, Name: "audit-db-empty", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}
	if err := repo.CreateRack(ctx, empty); err != nil {
		t.Fatal(err)
	}

	mux := chi.NewRouter()
	rack.NewHandler(repo).RegisterRoutes(mux)
	patch := func(id string, height int) *httptest.ResponseRecorder {
		body := `{"height_units":` + strconv.Itoa(height) + `}`
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/racks/"+id, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	stored := func(id string) int {
		var h int
		if err := f.Admin.QueryRow(bg, `SELECT height_units FROM rack WHERE id = $1`, id).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}

	// Ordered: the mount occupies U40–41 of the 42U rack.
	for _, tc := range []struct {
		name       string
		rack       string
		height     int
		wantStatus int
		wantStored int
	}{
		{"shrink below the highest mount is refused", mounted.ID, 39, http.StatusConflict, 42},
		{"shrink far below the mount is refused", mounted.ID, 10, http.StatusConflict, 42},
		{"shrink to exactly the highest occupied unit is allowed", mounted.ID, 41, http.StatusOK, 41},
		{"rack without mounts may shrink to 1U", empty.ID, 1, http.StatusOK, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := patch(tc.rack, tc.height)
			got := stored(tc.rack)
			if w.Code != tc.wantStatus || got != tc.wantStored {
				t.Errorf("PATCH height_units=%d = %d (stored %d), want %d (stored %d): RCK-01 keeps position_u + height_u - 1 <= height_units; body %.200s",
					tc.height, w.Code, got, tc.wantStatus, tc.wantStored, w.Body.String())
			}
			if tc.wantStatus == http.StatusConflict && !strings.Contains(w.Body.String(), "height_units") {
				t.Errorf("409 should name the field height_units: %.200s", w.Body.String())
			}
		})
	}
}
