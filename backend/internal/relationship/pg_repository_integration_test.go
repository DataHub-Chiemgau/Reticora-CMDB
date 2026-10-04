package relationship_test

import (
	"context"
	"os"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// These tests run against the migrated CI PostgreSQL (see the migrations CI
// job). They are skipped locally unless TEST_DATABASE_URL is set.
func testPool(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	return dsn
}

// TestTraverseFromProjectsRelTypePG is the regression test for the verified
// defect: the recursive walk CTE did not project rel_type/source, so every
// neighbors/impact query failed with SQLSTATE 42703. It builds a 3-node chain
// with a cycle and asserts a multi-hop traversal returns typed edges.
func TestTraverseFromProjectsRelTypePG(t *testing.T) {
	dsn := testPool(t)
	ctx := context.Background()
	// The fixture creates its own organization, which the tenant-scoped
	// application role cannot do before app.org_id exists; the repository under
	// test sets the tenant itself on every call.
	pool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	orgID := "11111111-1111-1111-1111-111111111111"
	if _, err := pool.Exec(ctx, `INSERT INTO organization (id, name, slug) VALUES ($1, 'traverse-org', 'traverse-org') ON CONFLICT (id) DO NOTHING`, orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM organization WHERE id = $1`, orgID)
	})
	// Repeat runs share the org and its CIs; clear the graph first.
	if _, err := pool.Exec(ctx, `DELETE FROM ci_relationship WHERE organization_id = $1`, orgID); err != nil {
		t.Fatalf("reset relationships: %v", err)
	}

	ciIDs := []string{
		"22222222-2222-2222-2222-222222222221",
		"22222222-2222-2222-2222-222222222222",
		"22222222-2222-2222-2222-222222222223",
	}
	var typeID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatalf("lookup server ci_type: %v", err)
	}
	for _, id := range ciIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO ci (id, organization_id, ci_type_id, name, status) VALUES ($1, $2, $3, $4, 'active') ON CONFLICT (id) DO NOTHING`, id, orgID, typeID, "ci-"+id[len(id)-2:]); err != nil {
			t.Fatalf("insert ci: %v", err)
		}
	}

	// The repository takes the tenant scope from the request context.
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	repo := relationship.NewPGRepository(pool)
	mk := func(src, dst, typ string) {
		r := &relationship.Relationship{
			OrganizationID: orgID,
			SourceCIID:     src,
			TargetCIID:     dst,
			RelType:        typ,
		}
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("create relationship %s->%s: %v", src, dst, err)
		}
	}
	mk(ciIDs[0], ciIDs[1], "connected_to")
	mk(ciIDs[1], ciIDs[2], "connected_to")
	mk(ciIDs[2], ciIDs[0], "depends_on") // cycle back to the root

	rels, err := repo.TraverseFrom(ctx, orgID, ciIDs[0], 3, 100)
	if err != nil {
		t.Fatalf("traverse: %v", err)
	}
	if len(rels) != 3 {
		t.Fatalf("expected 3 relationships within depth 3 incl. cycle, got %d: %+v", len(rels), rels)
	}
	types := map[string]bool{}
	for _, rel := range rels {
		if rel.RelType == "" {
			t.Errorf("relationship %s has empty rel_type", rel.ID)
		}
		types[rel.RelType] = true
	}
	if !types["connected_to"] || !types["depends_on"] {
		t.Errorf("expected both rel_types projected, got %v", types)
	}

	// Depth 1 from the root must only return the directly attached edges.
	relsD1, err := repo.TraverseFrom(ctx, orgID, ciIDs[0], 1, 100)
	if err != nil {
		t.Fatalf("traverse depth 1: %v", err)
	}
	if len(relsD1) != 2 {
		t.Fatalf("expected 2 edges at depth 1 (chain start + cycle edge), got %d", len(relsD1))
	}
}
