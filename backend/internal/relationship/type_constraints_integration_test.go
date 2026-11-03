package relationship_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestRelationshipCardinalityEnforced covers REL-06 (WP-294, E-125) for
// POST /relationships against PostgreSQL: the type lists of a relationship
// type include the subtypes of a listed CI type (parent_type_id, MET-06);
// one_to_one, one_to_many and many_to_one refuse an edge beyond the
// cardinality with 422 on rel_type and store nothing; a request waits for
// a transaction that holds the bounded end (SELECT … FOR NO KEY UPDATE)
// and then sees its edge; of two parallel requests for the same bounded
// end exactly one is 201, the other 422; the
// edges of the discovery system types (many_to_many, empty lists) stay
// possible. The remaining cases of the type lists are in
// TestRelationshipTypeConstraintsEnforced.
//
// Retest: cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… go test -count=1 -run '^TestRelationshipCardinalityEnforced$' ./internal/relationship/
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipCardinalityEnforced(t *testing.T) {
	f := scopetest.Seed(t, "92")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "rel06-admin")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})

	// rel06-blade is an organization subtype of the system type server.
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_type (organization_id, name, key, parent_type_id)
		SELECT $1, 'rel06-blade', 'rel06-blade', id FROM ci_type WHERE key = 'server' AND organization_id IS NULL`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	ciOfType := func(typeKey, name string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status)
			SELECT $1, id, $3, 'active' FROM ci_type WHERE key = $2 AND (organization_id IS NULL OR organization_id = $1)
			RETURNING id::text`, f.OrgA, typeKey, name).Scan(&id); err != nil {
			t.Fatalf("CI %s of type %s: %v", name, typeKey, err)
		}
		return id
	}

	types := relationshiptype.NewPGRepository(f.App)
	repo := relationship.NewPGRepository(f.App)
	mux := chi.NewRouter()
	relationshiptype.NewHandler(types).RegisterRoutes(mux)
	relationship.NewHandler(repo, types).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	edge := func(relType, source, target string) *httptest.ResponseRecorder {
		return call(http.MethodPost, "/api/v1/relationships",
			`{"source_ci_id":"`+source+`","target_ci_id":"`+target+`","rel_type":"`+relType+`"}`)
	}
	violations := func(w *httptest.ResponseRecorder) []string {
		var problem struct {
			Violations []struct {
				Field string `json:"field"`
			} `json:"violations"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		var fields []string
		for _, v := range problem.Violations {
			fields = append(fields, v.Field)
		}
		return fields
	}
	for _, body := range []string{
		`{"key":"rel06-runs","display_name":"runs","reverse_name":"run by","allowed_source_types":["server"]}`,
		`{"key":"rel06-pairs","display_name":"pairs","reverse_name":"paired","cardinality":"one_to_one"}`,
		`{"key":"rel06-owns","display_name":"owns","reverse_name":"owned by","cardinality":"one_to_many"}`,
		`{"key":"rel06-in","display_name":"in","reverse_name":"holds","cardinality":"many_to_one"}`,
	} {
		if w := call(http.MethodPost, "/api/v1/relationship-types", body); w.Code != http.StatusCreated {
			t.Fatalf("relationship type %s: %d %s", body, w.Code, w.Body.String())
		}
	}

	// A subtype of a listed CI type is allowed, another type is not.
	blade, sw, app := ciOfType("rel06-blade", "rel06-blade-1"), ciOfType("switch", "rel06-switch"), ciOfType("server", "rel06-app")
	if w := edge("rel06-runs", blade, app); w.Code != http.StatusCreated {
		t.Errorf("subtype of server as source: %d %s", w.Code, w.Body.String())
	}
	if w := edge("rel06-runs", sw, app); w.Code != http.StatusUnprocessableEntity || !slices.Equal(violations(w), []string{"source_ci_id"}) {
		t.Errorf("switch as source: %d %s", w.Code, w.Body.String())
	}

	// The cardinalities.
	n := map[string]string{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		n[name] = ciOfType("server", "rel06-"+name)
	}
	for _, c := range []struct {
		name, relType, source, target string
		status                        int
	}{
		{"one_to_one, first", "rel06-pairs", "a", "b", http.StatusCreated},
		{"one_to_one, second outgoing of the source", "rel06-pairs", "a", "c", http.StatusUnprocessableEntity},
		{"one_to_one, second incoming of the target", "rel06-pairs", "d", "b", http.StatusUnprocessableEntity},
		{"one_to_one, other ends", "rel06-pairs", "c", "d", http.StatusCreated},
		{"one_to_many, first", "rel06-owns", "a", "b", http.StatusCreated},
		{"one_to_many, second outgoing of the source", "rel06-owns", "a", "c", http.StatusCreated},
		{"one_to_many, second incoming of the target", "rel06-owns", "d", "b", http.StatusUnprocessableEntity},
		{"many_to_one, first", "rel06-in", "a", "b", http.StatusCreated},
		{"many_to_one, second incoming of the target", "rel06-in", "c", "b", http.StatusCreated},
		{"many_to_one, second outgoing of the source", "rel06-in", "a", "e", http.StatusUnprocessableEntity},
	} {
		w := edge(c.relType, n[c.source], n[c.target])
		if w.Code != c.status {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body.String(), c.status)
		} else if c.status == http.StatusUnprocessableEntity && !slices.Equal(violations(w), []string{"rel_type"}) {
			t.Errorf("%s: violations %v, want rel_type", c.name, violations(w))
		}
	}
	for relType, want := range map[string]int{"rel06-pairs": 2, "rel06-owns": 2, "rel06-in": 2} {
		var stored int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE organization_id = $1 AND rel_type = $2`,
			f.OrgA, relType).Scan(&stored); err != nil || stored != want {
			t.Errorf("%s: %d edges stored, want %d (%v)", relType, stored, want, err)
		}
	}

	// The bounded end is locked: a request waits for a transaction that
	// holds the target and then sees the edge it committed.
	held := ciOfType("server", "rel06-held")
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err = tx.Exec(bg, `SELECT id FROM ci WHERE id = $1 FOR NO KEY UPDATE`, held); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- edge("rel06-owns", n["e"], held).Code }()
	select {
	case code := <-done:
		t.Fatalf("POST while the target is locked returned %d without waiting", code)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err = tx.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
		VALUES ($1, $2, $3, 'rel06-owns')`, f.OrgA, n["d"], held); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != http.StatusUnprocessableEntity {
		t.Errorf("POST after the committed edge to the locked target: %d, want 422", code)
	}

	// Parallel requests for the same bounded end: one wins.
	for round := 0; round < 5; round++ {
		target := ciOfType("server", fmt.Sprintf("rel06-race-target-%d", round))
		sources := [2]string{ciOfType("server", fmt.Sprintf("rel06-race-a-%d", round)), ciOfType("server", fmt.Sprintf("rel06-race-b-%d", round))}
		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for i := range sources {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				codes[i] = edge("rel06-owns", sources[i], target).Code
			}(i)
		}
		close(start)
		wg.Wait()
		slices.Sort(codes)
		if !slices.Equal(codes, []int{http.StatusCreated, http.StatusUnprocessableEntity}) {
			t.Errorf("round %d: parallel one_to_many to the same target: %v, want one 201 and one 422", round, codes)
		}
	}

	// Discovery edges of the system types are not bounded.
	pdu, srv := ciOfType("server", "rel06-pdu"), ciOfType("server", "rel06-srv")
	for i, end := range []string{pdu, ciOfType("server", "rel06-pdu-2")} {
		rel := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: srv, TargetCIID: end, RelType: "powered_by", Source: "discovery"}
		if err := repo.Create(ctx, rel); err != nil {
			t.Errorf("discovery powered_by %d: %v", i, err)
		}
	}
	for i, end := range []string{pdu, sw} {
		if w := edge("connected_to", srv, end); w.Code != http.StatusCreated {
			t.Errorf("connected_to %d: %d %s", i, w.Code, w.Body.String())
		}
	}
}
