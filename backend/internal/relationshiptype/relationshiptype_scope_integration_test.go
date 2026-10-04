package relationshiptype_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
)

// TestRelationshipTypeRepositoryScope runs the relationship type repository
// with the principal's tenant scope (TEN-06, WP-010). Relationship types
// belong to the organization: a client-scoped principal of organization A
// sees A's types and the global catalogue, but neither sees nor changes a
// type of organization B, also not by naming B's organization.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipTypeRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "13")
	repo := relationshiptype.NewPGRepository(f.App)

	typA := &relationshiptype.Type{OrganizationID: f.OrgA, Key: "scopetest_feeds", ForwardLabel: "feeds", ReverseLabel: "fed by"}
	if err := repo.Create(f.OrgCtx(f.OrgA), typA); err != nil {
		t.Fatalf("create org A type: %v", err)
	}
	typB := &relationshiptype.Type{OrganizationID: f.OrgB, Key: "scopetest_mirrors", ForwardLabel: "mirrors", ReverseLabel: "mirrored by"}
	if err := repo.Create(f.OrgCtx(f.OrgB), typB); err != nil {
		t.Fatalf("create org B type: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	types, _, err := repo.List(ctx, f.OrgA, api.PaginationParams{Limit: 500})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	keys := map[string]bool{}
	for _, typ := range types {
		keys[typ.Key] = true
	}
	if !keys[typA.Key] || keys[typB.Key] || !keys["depends_on"] {
		t.Fatalf("client-1 list: own=%v foreign=%v global=%v, want true/false/true", keys[typA.Key], keys[typB.Key], keys["depends_on"])
	}
	ok, err := repo.Exists(ctx, f.OrgA, typB.Key)
	if err != nil || ok {
		t.Fatalf("exists of organization B type: ok=%v err=%v, want false/nil", ok, err)
	}
	if err = repo.Delete(ctx, f.OrgA, typB.Key); err == nil {
		t.Fatal("organization A principal deleted a type of organization B")
	}
	if _, err = repo.GetByKey(ctx, f.OrgB, typB.Key); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("read with organization B: got %v, want ErrTenantMismatch", err)
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM relationship_type WHERE organization_id = $1 AND key = $2`, f.OrgB, typB.Key).Scan(&count); err != nil {
		t.Fatalf("count organization B type: %v", err)
	}
	if count != 1 {
		t.Fatal("type of organization B was deleted")
	}

	if _, err = repo.Exists(context.Background(), f.OrgA, typA.Key); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("exists without scope: got %v, want ErrNoTenantScope", err)
	}
}
