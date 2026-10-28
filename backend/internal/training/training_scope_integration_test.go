package training_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/training"
	"github.com/go-chi/chi/v5"
)

// TestTrainingRepositoryScope runs the training repository with the
// principal's tenant scope (TEN-06, WP-019). Trainings belong to the
// organization (team and person scope follow with WP-029): a principal of
// organization A manages A's courses only, courses of organization B stay
// invisible, and a call without scope fails closed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTrainingRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "37")
	repo := training.NewPGRepository(f.App)
	courseB := &training.Course{OrganizationID: f.OrgB, Title: "B"}
	if err := repo.Create(f.OrgCtx(f.OrgB), courseB); err != nil {
		t.Fatalf("organization B course: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	courseA := &training.Course{OrganizationID: f.OrgA, Title: "A"}
	if err := repo.Create(ctx, courseA); err != nil {
		t.Fatalf("organization A course: %v", err)
	}
	list, _, err := repo.List(ctx, f.OrgA, training.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || len(list) != 1 || list[0].ID != courseA.ID {
		t.Fatalf("organization A courses: %+v err=%v, want only the A course", list, err)
	}
	if err = repo.Delete(ctx, f.OrgB, courseB.ID); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("delete organization B course: got %v, want ErrTenantMismatch", err)
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM training WHERE id = $1`, courseB.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("organization B course: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, training.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// dbLeak matches database internals in a response body: SQLSTATE codes,
// PostgreSQL error texts, constraint, policy, relation and type names.
var dbLeak = regexp.MustCompile(`(?i)sqlstate|constraint|policy|violat|row-level|relation|_fkey|_pkey|_key\b|duplicate key|foreign key|syntax for type|invalid input|pgx|pgconn|error:|training_assignment`)

// TestTrainingHandlerErrorsAreSanitized carries the probe
// TestVerifyB03training (WP-019, MGT-03) as a regression test through the
// training handler with PostgreSQL. Trainings belong to the organization: for
// a principal of organization A (restricted to client 1) a course or
// assignment of organization B answers 404, an invalid reference (unknown
// user, unknown course) 404 and an invalid id 400 or 404, never with an
// SQLSTATE, constraint or policy text; the organization-B rows stay
// unchanged. The own course answers 2xx. Assigning to a course or a user of
// organization B is accepted today: TestAuditTrainingAssignmentStaysInOrganization
// (training_cross_org_auditdefect_test.go).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTrainingHandlerErrorsAreSanitized(t *testing.T) {
	f := scopetest.Seed(t, "37")
	userA := f.AppUser(t, f.OrgA, "training-h-a")
	userB := f.AppUser(t, f.OrgB, "training-h-b")
	const unknown = "c0de37a0-0000-4000-8000-00000000dead"
	repo := training.NewPGRepository(f.App)
	courseA := &training.Course{OrganizationID: f.OrgA, Title: "A"}
	if err := repo.Create(f.OrgCtx(f.OrgA), courseA); err != nil {
		t.Fatalf("organization A course: %v", err)
	}
	courseB := &training.Course{OrganizationID: f.OrgB, Title: "B"}
	if err := repo.Create(f.OrgCtx(f.OrgB), courseB); err != nil {
		t.Fatalf("organization B course: %v", err)
	}
	assignmentB, err := repo.Assign(f.OrgCtx(f.OrgB), &training.Assignment{OrganizationID: f.OrgB, TrainingID: courseB.ID, UserID: userB})
	if err != nil {
		t.Fatalf("organization B assignment: %v", err)
	}

	mux := chi.NewRouter()
	training.NewHandler(repo).RegisterRoutes(mux)
	scope := database.OrgWideScope(f.OrgA, userA)
	scope.Clients = database.ScopeIDs(f.Client1)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userA})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		return w
	}

	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		// Foreign row: organization B's course and assignment.
		{"get organization-B course", http.MethodGet, "/api/v1/trainings/" + courseB.ID, "", http.StatusNotFound},
		{"patch organization-B course", http.MethodPatch, "/api/v1/trainings/" + courseB.ID, `{"title":"hijacked"}`, http.StatusNotFound},
		{"delete organization-B course", http.MethodDelete, "/api/v1/trainings/" + courseB.ID, "", http.StatusNotFound},
		{"complete organization-B assignment", http.MethodPost, "/api/v1/training-assignments/" + assignmentB.ID + "/complete", `{}`, http.StatusNotFound},
		// Invalid reference.
		{"assign an unknown user", http.MethodPost, "/api/v1/trainings/" + courseA.ID + "/assignments", `{"user_id":"` + unknown + `"}`, http.StatusNotFound},
		{"assign to an unknown course", http.MethodPost, "/api/v1/trainings/" + unknown + "/assignments", `{"user_id":"` + userA + `"}`, http.StatusNotFound},
		// Invalid id.
		{"get invalid id", http.MethodGet, "/api/v1/trainings/not-a-uuid", "", http.StatusNotFound},
		{"patch invalid id", http.MethodPatch, "/api/v1/trainings/not-a-uuid", `{"title":"x"}`, http.StatusNotFound},
		{"assignments of an invalid id", http.MethodGet, "/api/v1/trainings/not-a-uuid/assignments", "", http.StatusBadRequest},
		{"assign with an invalid user id", http.MethodPost, "/api/v1/trainings/" + courseA.ID + "/assignments", `{"user_id":"not-a-uuid"}`, http.StatusNotFound},
		{"complete invalid id", http.MethodPost, "/api/v1/training-assignments/not-a-uuid/complete", `{}`, http.StatusNotFound},
	} {
		w := call(c.method, c.path, c.body)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
		if dbLeak.MatchString(w.Body.String()) {
			t.Errorf("%s: body discloses database internals: %s", c.name, w.Body.String())
		}
	}

	// Organization B's rows are unchanged; no assignment crossed over.
	bg := context.Background()
	var title, status string
	if err = f.Admin.QueryRow(bg, `SELECT title FROM training WHERE id = $1`, courseB.ID).Scan(&title); err != nil || title != "B" {
		t.Errorf("organization B course: title=%q err=%v, want unchanged", title, err)
	}
	if err = f.Admin.QueryRow(bg, `SELECT status FROM training_assignment WHERE id = $1`, assignmentB.ID).Scan(&status); err != nil || status != "assigned" {
		t.Errorf("organization B assignment: status=%q err=%v, want assigned", status, err)
	}
	var n int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM training_assignment WHERE training_id IN ($1, $2)`, courseA.ID, courseB.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("assignments: %d err=%v, want only the organization-B one", n, err)
	}
	if w := call(http.MethodGet, "/api/v1/trainings/"+courseB.ID+"/assignments", ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), assignmentB.ID) {
		t.Errorf("assignments of the organization-B course: %d %s, want 200 without the assignment", w.Code, w.Body.String())
	}

	// Positive control: the own course.
	if w := call(http.MethodGet, "/api/v1/trainings/"+courseA.ID, ""); w.Code != http.StatusOK {
		t.Errorf("get own course: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPatch, "/api/v1/trainings/"+courseA.ID, `{"title":"A2"}`); w.Code != http.StatusOK {
		t.Errorf("patch own course: %d %s", w.Code, w.Body.String())
	}
	w := call(http.MethodPost, "/api/v1/trainings/"+courseA.ID+"/assignments", `{"user_id":"`+userA+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("assign own course: %d %s", w.Code, w.Body.String())
	}
	var own training.Assignment
	if err = json.Unmarshal(w.Body.Bytes(), &own); err != nil {
		t.Fatalf("decode assignment: %v", err)
	}
	if w = call(http.MethodPost, "/api/v1/training-assignments/"+own.ID+"/complete", `{}`); w.Code != http.StatusOK {
		t.Errorf("complete own assignment: %d %s", w.Code, w.Body.String())
	}
}
