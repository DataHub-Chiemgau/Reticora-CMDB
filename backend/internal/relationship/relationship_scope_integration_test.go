package relationship_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestRelationshipRepositoryClientScope runs the relationship repository with
// the principal's tenant scope (TEN-06, WP-010). An edge is visible only when
// both endpoint CIs are visible: a principal restricted to client 1 neither
// sees, creates, changes nor deletes an edge that touches a CI of client 2.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "11")
	own1 := f.CI(t, f.OrgA, f.Client1, "rel-c1-a")
	own2 := f.CI(t, f.OrgA, f.Client1, "rel-c1-b")
	foreign := f.CI(t, f.OrgA, f.Client2, "rel-c2")

	repo := relationship.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	ownEdge := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: own1, TargetCIID: own2, RelType: "depends_on"}
	crossEdge := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: own1, TargetCIID: foreign, RelType: "depends_on"}
	for _, rel := range []*relationship.Relationship{ownEdge, crossEdge} {
		if err := repo.Create(orgCtx, rel); err != nil {
			t.Fatalf("org-wide create %s->%s: %v", rel.SourceCIID, rel.TargetCIID, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	rels, _, err := repo.List(ctx, f.OrgA, own1, api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rels) != 1 || rels[0].ID != ownEdge.ID {
		t.Fatalf("client-1 list of %s: got %+v, want only the edge between client-1 CIs", own1, rels)
	}

	if err = repo.Create(ctx, &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: own2, TargetCIID: foreign, RelType: "connected_to"}); err == nil {
		t.Fatal("client-1 principal created an edge to a CI of client 2")
	}
	notes := "changed"
	if _, err = repo.Update(ctx, f.OrgA, crossEdge.ID, relationship.UpdateRequest{Notes: &notes}); err == nil {
		t.Fatal("client-1 principal updated an edge that touches client 2")
	}
	if err = repo.Delete(ctx, f.OrgA, crossEdge.ID); err == nil {
		t.Fatal("client-1 principal deleted an edge that touches client 2")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM ci_relationship WHERE id = $1 AND notes IS NULL`, crossEdge.ID).Scan(&count); err != nil {
		t.Fatalf("read cross edge: %v", err)
	}
	if count != 1 {
		t.Fatal("edge touching client 2 was changed or deleted")
	}

	// Within its own client the principal keeps full access.
	if _, err = repo.Update(ctx, f.OrgA, ownEdge.ID, relationship.UpdateRequest{Notes: &notes}); err != nil {
		t.Fatalf("client-1 update of own edge: %v", err)
	}
	if err = repo.Delete(ctx, f.OrgA, ownEdge.ID); err != nil {
		t.Fatalf("client-1 delete of own edge: %v", err)
	}

	if _, _, err = repo.List(context.Background(), f.OrgA, "", api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestCreateRelationshipInvisibleCIIs422 is the retest of F-145 (REL-02,
// REL-04): POST /relationships with a source or target CI the caller cannot
// see — a CI of another organization, of a client outside the scope, or an
// unknown id — answers 422 with violations on source_ci_id or target_ci_id,
// the same answer for a foreign and an unknown CI, and stores neither an
// edge nor an event. Edges between own CIs are created (201).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCreateRelationshipInvisibleCIIs422(t *testing.T) {
	f := scopetest.Seed(t, "fe")
	bg := context.Background()
	a1, a2 := f.CI(t, f.OrgA, "", "rel422-a1"), f.CI(t, f.OrgA, "", "rel422-a2")
	c1, c1b := f.CI(t, f.OrgA, f.Client1, "rel422-c1"), f.CI(t, f.OrgA, f.Client1, "rel422-c1b")
	c2 := f.CI(t, f.OrgA, f.Client2, "rel422-c2")
	b1, b2 := f.CI(t, f.OrgB, "", "rel422-b1"), f.CI(t, f.OrgB, "", "rel422-b2")
	unknown := f.ID()

	mux := chi.NewRouter()
	relationship.NewHandler(relationship.NewPGRepository(f.App).WithEvents(events.NewOutbox())).RegisterRoutes(mux)
	orgA := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	orgB := tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB, UserID: f.User})
	client1 := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	post := func(ctx context.Context, source, target string) *httptest.ResponseRecorder {
		body := `{"source_ci_id":"` + source + `","target_ci_id":"` + target + `","rel_type":"depends_on","source":"manual"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/relationships", strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	edges := func() int {
		return count(`SELECT count(*) FROM ci_relationship WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB)
	}
	created := func() int {
		return count(`SELECT count(*) FROM event_outbox WHERE organization_id IN ($1, $2) AND type = 'relationship.created'`, f.OrgA, f.OrgB)
	}
	type violation struct{ Field, Detail string }
	type problem struct {
		Type, Title, Detail string
		Status              int
		Violations          []violation
	}

	// Negative: each refusal is a 422 naming the invisible endpoint(s).
	answers := map[string]problem{}
	for _, tc := range []struct {
		name           string
		ctx            context.Context
		source, target string
		fields         []string
	}{
		{"B: own CI -> CI of A", orgB, b1, a1, []string{"target_ci_id"}},
		{"B: CI of A -> own CI", orgB, a1, b1, []string{"source_ci_id"}},
		{"B: CI of A -> CI of A", orgB, a1, a2, []string{"source_ci_id", "target_ci_id"}},
		{"B: own CI -> unknown", orgB, b1, unknown, []string{"target_ci_id"}},
		{"B: unknown -> own CI", orgB, unknown, b1, []string{"source_ci_id"}},
		{"A: own CI -> unknown", orgA, a1, unknown, []string{"target_ci_id"}},
		{"A: unknown -> own CI", orgA, unknown, a1, []string{"source_ci_id"}},
		{"A: CI of B -> own CI", orgA, b1, a1, []string{"source_ci_id"}},
		{"client 1: own CI -> CI of client 2", client1, c1, c2, []string{"target_ci_id"}},
		{"client 1: CI of client 2 -> own CI", client1, c2, c1, []string{"source_ci_id"}},
		{"client 1: own CI -> unknown", client1, c1, unknown, []string{"target_ci_id"}},
	} {
		w := post(tc.ctx, tc.source, tc.target)
		var p problem
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatalf("%s: body %s: %v", tc.name, w.Body.String(), err)
		}
		var fields []string
		for _, v := range p.Violations {
			fields = append(fields, v.Field)
		}
		if w.Code != http.StatusUnprocessableEntity || p.Status != http.StatusUnprocessableEntity || !slices.Equal(fields, tc.fields) {
			t.Errorf("%s: %d %s, want 422 with violations on %v", tc.name, w.Code, w.Body.String(), tc.fields)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s: Content-Type %q", tc.name, ct)
		}
		answers[tc.name] = p
	}
	// A foreign CI and an unknown id get the same answer: the response
	// does not tell whether the CI exists elsewhere.
	for _, pair := range [][2]string{
		{"B: own CI -> CI of A", "B: own CI -> unknown"},
		{"B: CI of A -> own CI", "B: unknown -> own CI"},
		{"A: CI of B -> own CI", "A: unknown -> own CI"},
		{"client 1: own CI -> CI of client 2", "client 1: own CI -> unknown"},
	} {
		if foreign, unknown := answers[pair[0]], answers[pair[1]]; !reflect.DeepEqual(foreign, unknown) {
			t.Errorf("%s answers %+v, %s answers %+v", pair[0], foreign, pair[1], unknown)
		}
	}
	if n := edges(); n != 0 {
		t.Errorf("%d edges stored by refused creates", n)
	}
	if n := created(); n != 0 {
		t.Errorf("%d relationship.created events written by refused creates", n)
	}

	// Positive: edges between visible CIs are created, also from a client
	// principal to an org-wide CI (E-62).
	for _, tc := range []struct {
		name           string
		ctx            context.Context
		source, target string
	}{
		{"A: own CIs", orgA, a1, a2},
		{"B: own CIs", orgB, b1, b2},
		{"client 1: own CIs", client1, c1, c1b},
		{"client 1: own CI -> org-wide CI", client1, c1, a1},
	} {
		w := post(tc.ctx, tc.source, tc.target)
		var rel relationship.Relationship
		_ = json.Unmarshal(w.Body.Bytes(), &rel)
		if w.Code != http.StatusCreated || rel.ID == "" || rel.SourceCIID != tc.source || rel.TargetCIID != tc.target {
			t.Errorf("%s: %d %s, want 201", tc.name, w.Code, w.Body.String())
		}
	}
	if n := edges(); n != 4 {
		t.Errorf("%d edges stored, want the 4 created ones", n)
	}
	if n := created(); n != 4 {
		t.Errorf("%d relationship.created events, want 4", n)
	}
	if n := count(`SELECT count(*) FROM ci_relationship WHERE organization_id = $1 AND (source_ci_id IN ($2, $3) OR target_ci_id IN ($2, $3))`,
		f.OrgB, a1, a2); n != 0 {
		t.Errorf("%d edges of organization B touch a CI of organization A", n)
	}
}
