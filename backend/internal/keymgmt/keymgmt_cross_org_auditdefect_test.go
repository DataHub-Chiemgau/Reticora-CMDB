//go:build auditdefect

// Defect test found by WP-538 (t1-ev-module-isolation, MGT-03, TEN-06; F-366):
// POST /keys/{id}/issue accepts an assigned_to of another organization. The
// foreign key key_assignment.assigned_to → app_user(id) does not include the
// organization, and the repository does not check it: an organization-A
// principal hands an organization-A key to an organization-B user (201), and
// tells an existing user of another organization apart from an unknown id
// (201 versus 404).
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditKeyIssueStaysInOrganization' ./internal/keymgmt/
package keymgmt_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/keymgmt"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func TestAuditKeyIssueStaysInOrganization(t *testing.T) {
	f := scopetest.Seed(t, "36")
	holder := f.AppUser(t, f.OrgA, "key-x-holder")
	holderB := f.AppUser(t, f.OrgB, "key-x-holder-b")
	repo := keymgmt.NewPGRepository(f.App)
	keys := map[string]*keymgmt.Item{}
	for _, name := range []string{"client 1", "org-wide"} {
		k := &keymgmt.Item{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "key-x " + name, KeyType: "physical", Status: "available"}
		if err := repo.Create(f.OrgCtx(f.OrgA), k); err != nil {
			t.Fatalf("key: %v", err)
		}
		keys[name] = k
	}

	mux := chi.NewRouter()
	keymgmt.NewHandler(repo).RegisterRoutes(mux)
	client1 := database.OrgWideScope(f.OrgA, holder)
	client1.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, holder)
	for name, scope := range map[string]*database.TenantScope{"client 1": &client1, "org-wide": &orgWide} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/"+keys[name].ID+"/issue", strings.NewReader(`{"assigned_to":"`+holderB+`"}`))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: holder})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: issue to a user of organization B: status %d, want 404: %s", name, w.Code, w.Body.String())
		}
	}
	var n int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM key_assignment WHERE assigned_to = $1`, holderB).Scan(&n); err != nil || n != 0 {
		t.Errorf("organization-A handovers to the organization-B user: %d err=%v, want none", n, err)
	}
}
