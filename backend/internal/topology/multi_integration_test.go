package topology_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// TestMultiImpactAgainstPostgres covers IMP-05 and IMP-06 (WP-180) against
// PostgreSQL: GET /impact?ci_ids= analyses several failing CIs together
// (one of two supplies keeps the server up, both take it down) with the
// failed CIs, the impacted CIs with paths and a summary; /impact/{ci_id}
// stays the short form; GET /topology without a root over 2,000 CIs is
// 422.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMultiImpactAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b8")
	bg := context.Background()
	ctxOf := func(org string) context.Context {
		return tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
	}
	pduA := f.CI(t, f.OrgA, f.Client1, "multi-pdu-a")
	pduB := f.CI(t, f.OrgA, f.Client1, "multi-pdu-b")
	server := f.CI(t, f.OrgA, f.Client1, "multi-server")
	vm := f.CI(t, f.OrgA, f.Client1, "multi-vm")
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'powered_by'), ($1, $2, $4, 'powered_by'), ($1, $5, $2, 'hosted_on')`, f.OrgA, server, pduA, pduB, vm); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	topology.NewHandler(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).RegisterRoutes(mux)
	call := func(org, path string) (int, []byte) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctxOf(org)))
		return w.Code, w.Body.Bytes()
	}
	type answer struct {
		FailedCIs []topology.Node        `json:"failed_cis"`
		Impacted  []topology.ImpactNode  `json:"impacted"`
		Summary   topology.ImpactSummary `json:"summary"`
	}
	impact := func(path string) answer {
		t.Helper()
		code, body := call(f.OrgA, path)
		var a answer
		if code != http.StatusOK || json.Unmarshal(body, &a) != nil {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		return a
	}
	if one := impact("/api/v1/impact?ci_ids=" + pduA); len(one.Impacted) != 0 || one.Summary.Failed != 1 {
		t.Errorf("one supply: %+v", one)
	}
	both := impact("/api/v1/impact?ci_ids=" + pduA + "," + pduB)
	if len(both.FailedCIs) != 2 || len(both.Impacted) != 2 || both.Summary.Impacted != 2 || both.Summary.Certain != 2 ||
		both.Summary.ByCategory["power"] != 1 || both.Summary.ByCategory["hosting"] != 1 {
		t.Errorf("both supplies: %+v", both)
	}
	for i := range both.Impacted {
		if both.Impacted[i].ID == vm && (both.Impacted[i].Hops != 2 || both.Impacted[i].Path[1].RelType != "hosted_on") {
			t.Errorf("path to the vm: %+v", both.Impacted[i])
		}
	}
	if short := impact("/api/v1/impact/" + server); len(short.Impacted) != 1 || short.Impacted[0].ID != vm || len(short.FailedCIs) != 1 {
		t.Errorf("short form: %+v", short)
	}
	for path, want := range map[string]int{
		"/api/v1/impact":                                  http.StatusBadRequest,
		"/api/v1/impact?ci_ids=" + f.ID():                 http.StatusNotFound,
		"/api/v1/impact?ci_ids=" + pduA + "&categories=x": http.StatusUnprocessableEntity,
	} {
		if code, body := call(f.OrgA, path); code != want {
			t.Errorf("%s: %d, want %d (%s)", path, code, want, body)
		}
	}

	// Over 2,000 CIs the topology without a root is 422; a type filter
	// that narrows below the limit answers.
	var serverType, switchType string
	if err := f.Admin.QueryRow(bg, `SELECT (SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL),
		(SELECT id::text FROM ci_type WHERE key = 'switch' AND organization_id IS NULL)`).Scan(&serverType, &switchType); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status)
		SELECT $1, $2, 'bulk-' || g, 'active' FROM generate_series(1, 2001) g`, f.OrgB, serverType); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status) VALUES ($1, $2, 'bulk-switch', 'active')`,
		f.OrgB, switchType); err != nil {
		t.Fatal(err)
	}
	if code, body := call(f.OrgB, "/api/v1/topology"); code != http.StatusUnprocessableEntity {
		t.Errorf("topology over 2000 CIs: %d %s", code, body)
	}
	code, body := call(f.OrgB, "/api/v1/topology?ci_type="+switchType)
	var g topology.Graph
	if code != http.StatusOK || json.Unmarshal(body, &g) != nil || len(g.Nodes) != 1 {
		t.Errorf("topology by type id: %d %s", code, body)
	}
}
