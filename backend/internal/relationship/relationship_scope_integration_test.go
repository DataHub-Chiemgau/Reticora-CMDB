package relationship_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
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
