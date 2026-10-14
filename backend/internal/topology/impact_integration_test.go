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

// TestImpactDirectionAgainstPostgres covers IMP-01 and IMP-03 (WP-176)
// against PostgreSQL: GET /impact/{ci_id} follows the impact_direction of
// the seeded and the organization's own relationship types; connected_to
// reaches its neighbor only as a marked heuristic.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestImpactDirectionAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b5")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	pdu := f.CI(t, f.OrgA, f.Client1, "imp-pdu")
	server := f.CI(t, f.OrgA, f.Client1, "imp-server")
	vm := f.CI(t, f.OrgA, f.Client1, "imp-vm")
	sw := f.CI(t, f.OrgA, f.Client1, "imp-switch")
	report := f.CI(t, f.OrgA, f.Client1, "imp-report")
	doc := f.CI(t, f.OrgA, f.Client1, "imp-doc")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO relationship_type (organization_id, key, display_name, reverse_name, impact_direction)
			VALUES ($1, 'imp_feeds', 'feeds', 'fed by', 'target_depends_on_source'),
			       ($1, 'imp_documents', 'documents', 'documented by', 'none')`, []any{f.OrgA}},
		{`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
			($1, $2, $3, 'powered_by'), ($1, $4, $2, 'hosted_on'), ($1, $5, $2, 'connected_to'),
			($1, $2, $6, 'imp_feeds'), ($1, $2, $7, 'imp_documents')`, []any{f.OrgA, server, pdu, vm, sw, report, doc}},
	} {
		if _, err := f.Admin.Exec(bg, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	mux := chi.NewRouter()
	topology.NewHandler(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).RegisterRoutes(mux)
	impact := func(id string) map[string]bool {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/impact/"+id, http.NoBody).WithContext(ctx))
		var body struct {
			Impacted []topology.ImpactNode `json:"impacted"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("impact %s: %d %s", id, w.Code, w.Body.String())
		}
		out := map[string]bool{}
		for i := range body.Impacted {
			out[body.Impacted[i].ID] = body.Impacted[i].Heuristic
		}
		return out
	}
	// pdu fails: server (powered_by), vm (hosted_on on the server), report
	// (fed by the server); switch only heuristically; doc never.
	got := impact(pdu)
	want := map[string]bool{server: false, vm: false, report: false, sw: true}
	if len(got) != len(want) {
		t.Fatalf("impact of the pdu: %v, want %v", got, want)
	}
	for id, heuristic := range want {
		if h, ok := got[id]; !ok || h != heuristic {
			t.Errorf("%s: impacted %v heuristic %v, want %v", id, ok, h, heuristic)
		}
	}
	// The vm fails: nothing it depends on is impacted.
	if got = impact(vm); len(got) != 0 {
		t.Errorf("impact of the vm: %v", got)
	}
}

// pgCountingCIs counts the CI reads of an impact walk against PostgreSQL.
type pgCountingCIs struct {
	*ci.PGRepository
	single, batch int
}

func (c *pgCountingCIs) GetByID(ctx context.Context, orgID, id string) (*ci.Item, error) {
	c.single++
	return c.PGRepository.GetByID(ctx, orgID, id)
}

func (c *pgCountingCIs) GetByIDs(ctx context.Context, orgID string, ids []string) ([]ci.Item, error) {
	c.batch++
	return c.PGRepository.GetByIDs(ctx, orgID, ids)
}

// TestImpactPathsAgainstPostgres covers IMP-02 and NFR-02 (WP-177) against
// PostgreSQL: a chain of 21 hosted_on hops is followed for 20 hops with the
// shortest path and its type sequence; the containment projected from
// parent_ci_id is in the category physical; the nodes are read in one
// batch.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestImpactPathsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b6")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	chain := []string{f.CI(t, f.OrgA, f.Client1, "chain-00")}
	for i := 1; i <= 21; i++ {
		chain = append(chain, f.CI(t, f.OrgA, f.Client1, "chain-"+string(rune('a'+i))))
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			VALUES ($1, $2, $3, 'hosted_on')`, f.OrgA, chain[i], chain[i-1]); err != nil {
			t.Fatal(err)
		}
	}
	blade := f.CI(t, f.OrgA, f.Client1, "chain-blade")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET parent_ci_id = $2 WHERE id = $1`, blade, chain[0]); err != nil {
		t.Fatal(err)
	}
	cis := &pgCountingCIs{PGRepository: ci.NewPGRepository(f.App)}
	mux := chi.NewRouter()
	topology.NewHandler(cis, relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).RegisterRoutes(mux)
	impact := func(query string) []topology.ImpactNode {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/impact/"+chain[0]+query, http.NoBody).WithContext(ctx))
		var body struct {
			Impacted []topology.ImpactNode `json:"impacted"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("impact%s: %d %s", query, w.Code, w.Body.String())
		}
		return body.Impacted
	}
	all := impact("")
	hops := map[string]int{}
	for i := range all {
		hops[all[i].ID] = all[i].Hops
	}
	if len(all) != 21 || hops[chain[20]] != 20 || hops[blade] != 1 {
		t.Errorf("impact: %d CIs, hops to chain 20 %d, blade %d", len(all), hops[chain[20]], hops[blade])
	}
	if _, ok := hops[chain[21]]; ok {
		t.Error("the 21st hop was reached")
	}
	for i := range all {
		if all[i].ID != chain[20] {
			continue
		}
		if len(all[i].Path) != 20 || all[i].Path[0].FromCIID != chain[0] || all[i].Path[19].RelType != "hosted_on" {
			t.Errorf("path to chain 20: %+v", all[i].Path)
		}
	}
	if cis.batch != 1 || cis.single > 2 {
		t.Errorf("CI reads: %d single, %d batch", cis.single, cis.batch)
	}
	physical := impact("?categories=physical")
	if len(physical) != 1 || physical[0].ID != blade || physical[0].Path[0].RelType != "contains" {
		t.Errorf("physical: %+v", physical)
	}
}
