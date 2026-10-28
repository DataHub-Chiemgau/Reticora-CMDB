//go:build auditdefect

// Defect test found by WP-538 (t1-ev-module-isolation, MGT-03, TEN-06; F-366):
// POST /desks/{id}/bookings accepts a user_id of another organization. The
// foreign key desk_booking.user_id → app_user(id) does not include the
// organization, and the repository does not check it: an organization-A
// principal books an organization-A desk for an organization-B user (201),
// and tells an existing user of another organization apart from an unknown
// id (201 versus 404).
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditDeskBookingStaysInOrganization' ./internal/desk/
package desk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/desk"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func TestAuditDeskBookingStaysInOrganization(t *testing.T) {
	f := scopetest.Seed(t, "35")
	room := f.Room(t, f.Client1, "desk-x-room-c1")
	user := f.AppUser(t, f.OrgA, "desk-x-booker")
	userB := f.AppUser(t, f.OrgB, "desk-x-booker-b")
	repo := desk.NewPGRepository(f.App)
	d := &desk.Desk{OrganizationID: f.OrgA, RoomID: room, Name: "desk-x-c1", Status: "available", Attributes: map[string]any{}}
	if err := repo.Create(f.OrgCtx(f.OrgA), d); err != nil {
		t.Fatalf("desk: %v", err)
	}

	mux := chi.NewRouter()
	desk.NewHandler(repo).RegisterRoutes(mux)
	client1 := database.OrgWideScope(f.OrgA, user)
	client1.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, user)
	start := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	for i, c := range []struct {
		name  string
		scope *database.TenantScope
	}{{"client 1", &client1}, {"org-wide", &orgWide}} {
		s := start.Add(time.Duration(2*i) * time.Hour)
		body := `{"user_id":"` + userB + `","starts_at":"` + s.Format(time.RFC3339) + `","ends_at":"` + s.Add(time.Hour).Format(time.RFC3339) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/desks/"+d.ID+"/bookings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, c.scope)))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: booking for a user of organization B: status %d, want 404: %s", c.name, w.Code, w.Body.String())
		}
	}
	var n int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM desk_booking WHERE user_id = $1`, userB).Scan(&n); err != nil || n != 0 {
		t.Errorf("organization-A bookings for the organization-B user: %d err=%v, want none", n, err)
	}
}
