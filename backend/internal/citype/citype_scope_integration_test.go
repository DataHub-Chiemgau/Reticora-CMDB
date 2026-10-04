package citype_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestCITypeRepositoryScope runs the CI type repository with the principal's
// tenant scope (TEN-06, WP-010). Types belong to the organization, so a
// client-scoped principal sees its organization's types but none of another
// organization; instance fields follow their CI, so the principal neither
// sees nor changes the instance fields of a client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCITypeRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "12")
	own := f.CI(t, f.OrgA, f.Client1, "type-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "type-c2")

	repo := citype.NewPGRepository(f.App)
	orgA := &citype.Type{OrganizationID: f.OrgA, Key: "scopetest_a", Name: "Scopetest A"}
	if err := repo.Create(f.OrgCtx(f.OrgA), orgA); err != nil {
		t.Fatalf("create org A type: %v", err)
	}
	orgB := &citype.Type{OrganizationID: f.OrgB, Key: "scopetest_b", Name: "Scopetest B"}
	if err := repo.Create(f.OrgCtx(f.OrgB), orgB); err != nil {
		t.Fatalf("create org B type: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	types, _, err := repo.List(ctx, f.OrgA, citype.FilterParams{}, api.PaginationParams{Limit: 500})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, typ := range types {
		seen[typ.ID] = true
	}
	if !seen[orgA.ID] || seen[orgB.ID] {
		t.Fatalf("client-1 list: org A type=%v org B type=%v, want true/false", seen[orgA.ID], seen[orgB.ID])
	}
	if _, err = repo.GetByID(ctx, f.OrgA, orgB.ID); err == nil {
		t.Fatal("organization A principal read a type of organization B")
	}

	field := citype.UpsertInstanceFieldRequest{Name: "rack_note", DataType: "string"}
	if _, err = repo.UpsertInstanceField(f.OrgCtx(f.OrgA), f.OrgA, foreign, field); err != nil {
		t.Fatalf("org-wide upsert on client-2 CI: %v", err)
	}
	if _, err = repo.UpsertInstanceField(ctx, f.OrgA, own, field); err != nil {
		t.Fatalf("client-1 upsert on own CI: %v", err)
	}

	fields, err := repo.ListInstanceFields(ctx, f.OrgA, foreign)
	if err != nil {
		t.Fatalf("list instance fields: %v", err)
	}
	if len(fields) != 0 {
		t.Fatalf("client-1 principal sees %d instance fields of a client-2 CI", len(fields))
	}
	if _, err = repo.UpsertInstanceField(ctx, f.OrgA, foreign, citype.UpsertInstanceFieldRequest{Name: "intruder", DataType: "string"}); err == nil {
		t.Fatal("client-1 principal added an instance field to a client-2 CI")
	}
	if err = repo.DeleteInstanceField(ctx, f.OrgA, foreign, "rack_note"); err == nil {
		t.Fatal("client-1 principal deleted an instance field of a client-2 CI")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM ci_instance_field_definition WHERE ci_id = $1`, foreign).Scan(&count); err != nil {
		t.Fatalf("count foreign fields: %v", err)
	}
	if count != 1 {
		t.Fatalf("client-2 CI has %d instance fields, want the one created org-wide", count)
	}

	if _, _, err = repo.List(context.Background(), f.OrgA, citype.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
