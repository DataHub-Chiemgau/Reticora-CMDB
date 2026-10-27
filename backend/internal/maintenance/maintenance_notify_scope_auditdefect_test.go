//go:build auditdefect

// Defect test found by WP-538 (t1-ev-module-isolation, MGT-04, TEN-06):
// POST /maintenance-windows/{id}/notify and GET
// /maintenance-windows/{id}/notifications answer 200 for a window the
// caller cannot see (a window of only client-2 CIs for a client-1
// principal, a window of another organization) instead of 404 like GET,
// PATCH and DELETE of the window. Nothing is queued or listed; the response
// still claims a successful announcement ({"queued":0}).
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditMaintenanceNotifyHiddenWindowIsNotFound' ./internal/maintenance/
package maintenance_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/maintenance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func TestAuditMaintenanceNotifyHiddenWindowIsNotFound(t *testing.T) {
	f := scopetest.Seed(t, "3a")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "maint-x-c2")
	ciB := f.CI(t, f.OrgB, "", "maint-x-b")
	user := f.AppUser(t, f.OrgA, "maint-x-user")
	repo := maintenance.NewPGRepository(f.App)
	start := time.Now().UTC().Add(24 * time.Hour)
	window := func(orgID, ciID string) string {
		w := &maintenance.Window{OrganizationID: orgID, Title: "Hidden", StartsAt: start, EndsAt: start.Add(time.Hour), CIIDs: []string{ciID}}
		if err := repo.Create(f.OrgCtx(orgID), w); err != nil {
			t.Fatalf("window: %v", err)
		}
		return w.ID
	}
	client2Only, orgB := window(f.OrgA, foreignCI), window(f.OrgB, ciB)

	mux := chi.NewRouter()
	maintenance.NewHandler(repo).RegisterRoutes(mux)
	client1 := database.OrgWideScope(f.OrgA, user)
	client1.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, user)
	for _, c := range []struct {
		name, window string
		scope        *database.TenantScope
	}{
		{"client-2 window as client 1", client2Only, &client1},
		{"organization-B window", orgB, &orgWide},
	} {
		for _, r := range []struct{ method, suffix string }{{http.MethodPost, "/notify"}, {http.MethodGet, "/notifications"}} {
			req := httptest.NewRequest(r.method, "/api/v1/maintenance-windows/"+c.window+r.suffix, nil)
			ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, c.scope)))
			if w.Code != http.StatusNotFound {
				t.Errorf("%s: %s %s: status %d, want 404: %s", c.name, r.method, r.suffix, w.Code, w.Body.String())
			}
		}
	}
	var n int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM maintenance_notification WHERE maintenance_window_id IN ($1, $2)`, client2Only, orgB).Scan(&n); err != nil || n != 0 {
		t.Errorf("notifications of the hidden windows: %d err=%v, want none", n, err)
	}
}
