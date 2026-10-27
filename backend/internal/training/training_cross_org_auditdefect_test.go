//go:build auditdefect

// Defect test found by WP-538 (t1-ev-module-isolation, MGT-03, TEN-06):
// POST /trainings/{id}/assignments accepts a course and a user of another
// organization. The foreign keys training_assignment.training_id →
// training(id) and user_id → app_user(id) do not include the organization,
// and the repository does not check them: an organization-A principal
// creates an organization-A assignment to an organization-B course, or for
// an organization-B user (201 each), and tells existing ids of another
// organization apart from unknown ones (201 versus 404). The assignment to
// the organization-B course is also deleted by organization B's course
// deletion (ON DELETE CASCADE).
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditTrainingAssignmentStaysInOrganization' ./internal/training/
package training_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/training"
	"github.com/go-chi/chi/v5"
)

func TestAuditTrainingAssignmentStaysInOrganization(t *testing.T) {
	f := scopetest.Seed(t, "37")
	userA := f.AppUser(t, f.OrgA, "training-x-a")
	userB := f.AppUser(t, f.OrgB, "training-x-b")
	repo := training.NewPGRepository(f.App)
	courseA := &training.Course{OrganizationID: f.OrgA, Title: "A"}
	if err := repo.Create(f.OrgCtx(f.OrgA), courseA); err != nil {
		t.Fatalf("organization A course: %v", err)
	}
	courseB := &training.Course{OrganizationID: f.OrgB, Title: "B"}
	if err := repo.Create(f.OrgCtx(f.OrgB), courseB); err != nil {
		t.Fatalf("organization B course: %v", err)
	}

	mux := chi.NewRouter()
	training.NewHandler(repo).RegisterRoutes(mux)
	scope := database.OrgWideScope(f.OrgA, userA)
	for _, c := range []struct{ name, course, user string }{
		{"assignment to the organization-B course", courseB.ID, userA},
		{"assignment of the organization-B user", courseA.ID, userB},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/trainings/"+c.course+"/assignments", strings.NewReader(`{"user_id":"`+c.user+`"}`))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userA})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404: %s", c.name, w.Code, w.Body.String())
		}
	}
	var n int
	if err := f.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM training_assignment WHERE organization_id = $1 AND (training_id = $2 OR user_id = $3)`,
		f.OrgA, courseB.ID, userB).Scan(&n); err != nil || n != 0 {
		t.Errorf("organization-A assignments referencing organization B: %d err=%v, want none", n, err)
	}
}
