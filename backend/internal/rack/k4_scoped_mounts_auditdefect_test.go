//go:build auditdefect

// Defect test of a finding from the implementation of K4 (WP-500, F-360,
// audit/g1-reaudit/FINDINGS.md): the height check of PATCH /racks/{id}
// (checkHeightCoversMounts) and the overlap list of CreateMount and
// UpdateMount (mountsForRackTx) read rack_mount under the caller's row-level
// security. A mount of a CI of another client in a rack of the caller's
// client is invisible to a client-bound principal, so the principal shrinks
// the rack below it (200) and the mount lies outside the rack (RCK-01).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -v -run 'TestAuditK4RackHeightCoversInvisibleMounts' ./internal/rack/
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

func TestAuditK4RackHeightCoversInvisibleMounts(t *testing.T) {
	f := scopetest.Seed(t, "db")
	bg := context.Background()
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	clientCtx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := rack.NewPGRepository(f.App)

	// A 42U rack in a room of client 1; an org-wide principal mounts a CI of
	// client 2 at U40-41 (shared colocation rack).
	room := f.Room(t, f.Client1, "k4-scoped-room")
	rk := &rack.Rack{OrganizationID: f.OrgA, RoomID: room, Name: "k4-scoped-rack", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}
	if err := repo.CreateRack(orgCtx, rk); err != nil {
		t.Fatal(err)
	}
	foreignCI := f.CI(t, f.OrgA, f.Client2, "k4-scoped-ci-c2")
	if err := repo.CreateMount(orgCtx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rk.ID, CIID: foreignCI, PositionU: 40, HeightU: 2, Face: "front"}); err != nil {
		t.Fatalf("org-wide mount of the client-2 CI: %v", err)
	}
	if _, err := repo.GetRack(clientCtx, f.OrgA, rk.ID); err != nil {
		t.Fatalf("the rack of client 1 is not visible to the client-1 principal: %v", err)
	}

	// Mounting into the occupied units stays refused: the exclusion
	// constraint rack_mount_no_overlap is not subject to row-level security.
	ownCI := f.CI(t, f.OrgA, f.Client1, "k4-scoped-ci-c1")
	if err := repo.CreateMount(clientCtx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rk.ID, CIID: ownCI, PositionU: 40, HeightU: 1, Face: "front"}); err == nil {
		t.Error("client-1 mount into U40, occupied by the client-2 mount: accepted; want a conflict")
	}

	mux := chi.NewRouter()
	rack.NewHandler(repo).RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodPatch, "/api/v1/racks/"+rk.ID, strings.NewReader(`{"height_units":39}`)).WithContext(clientCtx)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	var height, top int
	if err := f.Admin.QueryRow(bg, `SELECT r.height_units, COALESCE(MAX(m.position_u + m.height_u - 1), 0)
		FROM rack r LEFT JOIN rack_mount m ON m.rack_id = r.id WHERE r.id = $1 GROUP BY r.height_units`, rk.ID).Scan(&height, &top); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusConflict || height != 42 {
		t.Errorf("client-1 PATCH height_units=39 of a rack with a client-2 mount at U40-41: %d, stored height %d, highest mounted unit %d; "+
			"want 409 on height_units and height 42 (RCK-01 holds for every mount, also those the caller cannot see); body %.200s",
			w.Code, height, top, w.Body.String())
	}
}
