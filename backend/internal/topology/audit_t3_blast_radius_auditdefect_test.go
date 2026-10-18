//go:build auditdefect

package topology_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT test (it fails until
// the product is fixed).
//
// WP-176 (IMP-03: connected_to is a heuristic, second-class impact path) P2
// "Blast radius lists connected_to-only (heuristic) CIs as affected without a
// heuristic marker": GetBlastRadius (impact.go) copies the impacted graph
// nodes into AffectedCIs ([]Node) and drops impactHit.heuristic; BlastRadius
// has no heuristic field. GET /api/v1/impact/{id} marks the same CIs as
// heuristic (control). The CI detail impact section uses blast-radius.
// Requirement: a CI reached only over connected_to is marked heuristic in
// the blast radius, or it is not counted as affected.
//
// Seed: switch connected_to server, vm hosted_on server, server powered_by
// pdu (all client 1), PostgreSQL repositories.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3BlastRadiusMarksHeuristicCIs' ./internal/topology/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
	"github.com/go-chi/chi/v5"
)

func TestAuditT3BlastRadiusMarksHeuristicCIs(t *testing.T) {
	f := scopetest.Seed(t, "d7")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	sw := f.CI(t, f.OrgA, f.Client1, "audit-t3-switch")
	server := f.CI(t, f.OrgA, f.Client1, "audit-t3-server")
	vm := f.CI(t, f.OrgA, f.Client1, "audit-t3-vm")
	pdu := f.CI(t, f.OrgA, f.Client1, "audit-t3-pdu")
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'connected_to'), ($1, $4, $3, 'hosted_on'), ($1, $3, $5, 'powered_by')`, f.OrgA, sw, server, vm, pdu); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	topology.NewHandler(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).RegisterRoutes(mux)
	get := func(path string, out any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}

	// Control: the impact endpoint classifies server and vm as heuristic.
	var imp struct {
		Impacted []topology.ImpactNode `json:"impacted"`
	}
	get("/api/v1/impact/"+sw, &imp)
	heuristic := map[string]bool{}
	for _, n := range imp.Impacted {
		heuristic[n.ID] = n.Heuristic
	}
	if !heuristic[server] || !heuristic[vm] {
		t.Fatalf("control: /impact/{switch} heuristic flags = %v, want server and vm heuristic", heuristic)
	}

	var blast struct {
		AffectedCIs []map[string]any `json:"affected_cis"`
	}
	get("/api/v1/cis/"+sw+"/blast-radius", &blast)
	for _, n := range blast.AffectedCIs {
		id, _ := n["id"].(string)
		if !heuristic[id] {
			continue
		}
		if flag, _ := n["heuristic"].(bool); !flag {
			t.Errorf("blast radius of the switch lists %v (reached only over connected_to) as affected without heuristic=true (IMP-03)", n["name"])
		}
	}
}
