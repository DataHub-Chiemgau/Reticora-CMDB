package topology_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	single, batch, nodes int
}

func (c *pgCountingCIs) GetByID(ctx context.Context, orgID, id string) (*ci.Item, error) {
	c.single++
	return c.PGRepository.GetByID(ctx, orgID, id)
}

func (c *pgCountingCIs) GetByIDs(ctx context.Context, orgID string, ids []string) ([]ci.Item, error) {
	c.batch++
	return c.PGRepository.GetByIDs(ctx, orgID, ids)
}

func (c *pgCountingCIs) ListNodes(ctx context.Context, orgID string, ids []string) ([]ci.Node, error) {
	c.nodes++
	return c.PGRepository.ListNodes(ctx, orgID, ids)
}

// TestImpactPathsAgainstPostgres covers IMP-02 and NFR-02 (WP-177) against
// PostgreSQL: a chain of 21 hosted_on hops is followed for 20 hops with the
// shortest path and its type sequence; the containment projected from
// parent_ci_id is in the category physical; the nodes are read in one
// read of the node projection (WP-335).
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
	if cis.nodes != 1 || cis.batch != 0 || cis.single > 2 {
		t.Errorf("CI reads: %d single, %d batch, %d node reads", cis.single, cis.batch, cis.nodes)
	}
	physical := impact("?categories=physical")
	if len(physical) != 1 || physical[0].ID != blade || physical[0].Path[0].RelType != "contains" {
		t.Errorf("physical: %+v", physical)
	}
}

// TestAuditT3BlastRadiusMarksHeuristicCIs covers IMP-03 for the blast
// radius against PostgreSQL (WP-490, F-098; formerly an auditdefect test of
// WP-176): a CI reached only over connected_to is listed with
// heuristic=true and counted in heuristic_count, not in affected_count,
// as GET /impact/{ci_id} marks it.
//
// Seed: switch connected_to server, vm hosted_on server, server powered_by
// pdu (all client 1), PostgreSQL repositories.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
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
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
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

	type blastRadius struct {
		AffectedCIs    []map[string]any `json:"affected_cis"`
		AffectedCount  int              `json:"affected_count"`
		HeuristicCount int              `json:"heuristic_count"`
		ClientIDs      []string         `json:"client_ids"`
	}
	var blast blastRadius
	get("/api/v1/cis/"+sw+"/blast-radius", &blast)
	for _, n := range blast.AffectedCIs {
		id, _ := n["id"].(string)
		if flag, _ := n["heuristic"].(bool); flag != heuristic[id] {
			t.Errorf("blast radius of the switch lists %v with heuristic=%v, /impact says %v (IMP-03)", n["name"], flag, heuristic[id])
		}
	}
	if len(blast.AffectedCIs) != 2 || blast.AffectedCount != 0 || blast.HeuristicCount != 2 || len(blast.ClientIDs) != 0 {
		t.Errorf("blast radius of the switch: %d CIs, affected_count %d, heuristic_count %d, clients %v; want 2 heuristic hits, none affected",
			len(blast.AffectedCIs), blast.AffectedCount, blast.HeuristicCount, blast.ClientIDs)
	}

	// The pdu fails: server and vm are certain, the switch a heuristic hit.
	var fromPDU blastRadius
	get("/api/v1/cis/"+pdu+"/blast-radius", &fromPDU)
	if fromPDU.AffectedCount != 2 || fromPDU.HeuristicCount != 1 || len(fromPDU.AffectedCIs) != 3 {
		t.Fatalf("blast radius of the pdu: %+v", fromPDU)
	}
	for i, want := range []bool{false, false, true} {
		if flag, _ := fromPDU.AffectedCIs[i]["heuristic"].(bool); flag != want {
			t.Errorf("blast radius of the pdu, CI %d (%v): heuristic %v, want %v (certain first)", i, fromPDU.AffectedCIs[i]["name"], flag, want)
		}
	}
}

// TestDependenciesDirectionAgainstPostgres covers IMP-01 and IMP-07 for GET
// /cis/{id}/dependencies against PostgreSQL (WP-490, retest of F-099):
// upstream and downstream follow the impact_direction, not the raw edge
// direction. vm hosted_on server puts the server upstream of the vm and the
// vm downstream of the server; powered_by and the containment projected
// from parent_ci_id alike. A deleted CI and a CI of another client stay
// out for a principal of client 1, and connected_to is no dependency.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDependenciesDirectionAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b5")
	bg := context.Background()
	pdu := f.CI(t, f.OrgA, f.Client1, "dep-pdu")
	server := f.CI(t, f.OrgA, f.Client1, "dep-server")
	vm := f.CI(t, f.OrgA, f.Client1, "dep-vm")
	chassis := f.CI(t, f.OrgA, f.Client1, "dep-chassis")
	blade := f.CI(t, f.OrgA, f.Client1, "dep-blade")
	sw := f.CI(t, f.OrgA, f.Client1, "dep-switch")
	gone := f.CI(t, f.OrgA, f.Client1, "dep-deleted")
	foreign := f.CI(t, f.OrgA, f.Client2, "dep-foreign")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
			($1, $2, $3, 'hosted_on'), ($1, $3, $4, 'powered_by'), ($1, $5, $3, 'connected_to'),
			($1, $6, $3, 'hosted_on'), ($1, $7, $3, 'hosted_on')`, []any{f.OrgA, vm, server, pdu, sw, gone, foreign}},
		{`UPDATE ci SET parent_ci_id = $2 WHERE id = $1`, []any{blade, chassis}},
		{`UPDATE ci SET deleted_at = now() WHERE id = $1`, []any{gone}},
	} {
		if _, err := f.Admin.Exec(bg, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	mux := chi.NewRouter()
	topology.NewHandler(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).RegisterRoutes(mux)
	serve := func(scope context.Context, path string) *httptest.ResponseRecorder {
		ctx := tenant.WithTenant(scope, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		return w
	}
	client1 := f.ClientCtx(f.Client1)
	for _, c := range []struct {
		name  string
		scope context.Context
		id    string
		query string
		want  []string
	}{
		{"upstream of the vm", client1, vm, "?direction=upstream", []string{server, pdu}},
		{"upstream of the vm, depth 1", client1, vm, "?direction=upstream&depth=1", []string{server}},
		{"downstream of the server", client1, server, "?direction=downstream", []string{vm}},
		{"downstream of the server, org-wide", f.OrgCtx(f.OrgA), server, "", []string{vm, foreign}},
		{"downstream of the pdu", client1, pdu, "", []string{server, vm}},
		{"upstream of the server", client1, server, "?direction=upstream", []string{pdu}},
		{"downstream of the chassis", client1, chassis, "?direction=downstream", []string{blade}},
		{"upstream of the blade", client1, blade, "?direction=upstream", []string{chassis}},
		{"upstream of the switch", client1, sw, "?direction=upstream", nil},
		{"downstream of the switch", client1, sw, "?direction=downstream", nil},
	} {
		w := serve(c.scope, "/api/v1/cis/"+c.id+"/dependencies"+c.query)
		var body topology.DependencyResult
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", c.name, w.Code, w.Body.String())
		}
		got := map[string]bool{}
		for _, n := range body.Nodes {
			got[n.ID] = true
		}
		ok := len(got) == len(c.want) && body.Count == len(c.want)
		for _, id := range c.want {
			ok = ok && got[id]
		}
		if !ok {
			t.Errorf("%s: %v (count %d), want %v", c.name, got, body.Count, c.want)
		}
	}
	// An invalid depth is a 400 on the field depth (F-255).
	if w := serve(client1, "/api/v1/cis/"+vm+"/dependencies?depth=abc"); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), `"field":"depth"`) {
		t.Errorf("depth=abc: %d %s", w.Code, w.Body.String())
	}
}
