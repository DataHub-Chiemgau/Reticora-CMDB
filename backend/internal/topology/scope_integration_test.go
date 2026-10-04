package topology_test

import (
	"context"
	"sort"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// TestTraversalStaysInsideScope covers WP-031 (IMP-07, CI-05): the recursive
// traversal runs under the caller's full scope and only passes through CIs
// that are visible and not deleted. An invisible or deleted intermediate
// node never connects two visible ones, and the PostgreSQL result equals the
// shared selection of relationship.SelectTraversal.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTraversalStaysInsideScope(t *testing.T) {
	f := scopetest.Seed(t, "4c")
	bg := context.Background()
	repo := relationship.NewPGRepository(f.App)

	site1, site2 := f.ID(), f.ID()
	for _, s := range []string{site1, site2} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
			s, f.OrgA, f.Client1, "topo "+s); err != nil {
			t.Fatalf("seed site: %v", err)
		}
	}
	ciAt := func(name, client, site string) string {
		id := f.CI(t, f.OrgA, client, name)
		if site != "" {
			if _, err := f.Admin.Exec(bg, `UPDATE ci SET site_id = $2 WHERE id = $1`, id, site); err != nil {
				t.Fatalf("place %s: %v", name, err)
			}
		}
		return id
	}
	// root ─ foreign (client 2) ─ hiddenTarget
	// root ─ deleted             ─ deletedTarget
	// root ─ otherSite (site 2)  ─ siteTarget
	// root ─ direct ─ second
	root := ciAt("root", f.Client1, site1)
	foreign := ciAt("foreign", f.Client2, "")
	hiddenTarget := ciAt("behind-foreign", f.Client1, site1)
	deleted := ciAt("deleted", f.Client1, site1)
	deletedTarget := ciAt("behind-deleted", f.Client1, site1)
	otherSite := ciAt("other-site", f.Client1, site2)
	siteTarget := ciAt("behind-other-site", f.Client1, site1)
	direct := ciAt("direct", f.Client1, site1)
	second := ciAt("second", f.Client1, site1)
	for _, e := range [][2]string{
		{root, foreign}, {foreign, hiddenTarget},
		{root, deleted}, {deleted, deletedTarget},
		{root, otherSite}, {otherSite, siteTarget},
		{root, direct}, {direct, second},
	} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on')`,
			f.OrgA, e[0], e[1]); err != nil {
			t.Fatalf("seed edge: %v", err)
		}
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatalf("delete ci: %v", err)
	}

	reached := func(ctx context.Context, maxNodes int) ([]relationship.Relationship, map[string]bool) {
		t.Helper()
		rels, err := repo.TraverseFrom(ctx, f.OrgA, root, 5, maxNodes)
		if err != nil {
			t.Fatalf("traverse: %v", err)
		}
		nodes := map[string]bool{}
		for _, r := range rels {
			nodes[r.SourceCIID], nodes[r.TargetCIID] = true, true
		}
		return rels, nodes
	}

	// Org-wide: the deleted node blocks the walk, everything else is reached.
	_, nodes := reached(f.OrgCtx(f.OrgA), relationship.MaxTraversalNodes)
	for id, want := range map[string]bool{
		foreign: true, hiddenTarget: true, otherSite: true, siteTarget: true, direct: true, second: true,
		deleted: false, deletedTarget: false,
	} {
		if nodes[id] != want {
			t.Errorf("org-wide: node %s reached=%v, want %v", id, nodes[id], want)
		}
	}

	// Client 1: the CI of client 2 does not connect root and hiddenTarget.
	_, nodes = reached(f.ClientCtx(f.Client1), relationship.MaxTraversalNodes)
	if nodes[foreign] || nodes[hiddenTarget] {
		t.Errorf("client 1 reached through a CI of client 2: foreign=%v behind=%v", nodes[foreign], nodes[hiddenTarget])
	}
	if !nodes[second] {
		t.Error("client 1 did not reach the visible chain root-direct-second")
	}

	// Site 1: the CI at site 2 does not connect root and siteTarget.
	scope := database.OrgWideScope(f.OrgA, f.User)
	scope.Sites = database.ScopeIDs(site1)
	_, nodes = reached(database.ContextWithTenantScope(bg, &scope), relationship.MaxTraversalNodes)
	if nodes[otherSite] || nodes[siteTarget] {
		t.Errorf("site 1 reached through a CI of site 2: other=%v behind=%v", nodes[otherSite], nodes[siteTarget])
	}

	// Organization B sees nothing of the graph.
	if rels, err := repo.TraverseFrom(f.OrgCtx(f.OrgB), f.OrgB, root, 5, relationship.MaxTraversalNodes); err != nil || len(rels) != 0 {
		t.Errorf("org B: %d relationships, %v", len(rels), err)
	}

	// The node limit selects the same edges as the memory semantics.
	var visible []relationship.Relationship
	rows, err := f.Admin.Query(bg, `SELECT id::text, source_ci_id::text, target_ci_id::text FROM ci_relationship
		WHERE organization_id = $1 AND source_ci_id <> $2 AND target_ci_id <> $2`, f.OrgA, deleted)
	if err != nil {
		t.Fatalf("list relationships: %v", err)
	}
	for rows.Next() {
		var r relationship.Relationship
		if err := rows.Scan(&r.ID, &r.SourceCIID, &r.TargetCIID); err != nil {
			t.Fatalf("scan relationship: %v", err)
		}
		visible = append(visible, r)
	}
	rows.Close()
	for _, maxNodes := range []int{1, 2, 4, 6} {
		got, _ := reached(f.OrgCtx(f.OrgA), maxNodes)
		want := relationship.SelectTraversal(root, visible, 5, maxNodes)
		if ids(got) != ids(want) {
			t.Errorf("maxNodes %d: PostgreSQL %s, memory semantics %s", maxNodes, ids(got), ids(want))
		}
	}
}

func ids(rels []relationship.Relationship) string {
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	s := ""
	for _, id := range out {
		s += id + " "
	}
	return s
}
