package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
)

// TestLifecycleRepositoryScope runs the lifecycle repository and state store
// with the principal's tenant scope (TEN-06, WP-011). Definitions belong to
// the organization: a client-scoped principal sees its organization's
// definitions but none of another organization. Lifecycle states live on the
// CI, so the principal neither reads nor sets the state of a client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLifecycleRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "18")
	own := f.CI(t, f.OrgA, f.Client1, "lifecycle-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "lifecycle-c2")
	// Only CI types with a lifecycle model have lifecycle states (WP-080).
	if _, err := f.Admin.Exec(context.Background(), `
		WITH t AS (INSERT INTO ci_type (organization_id, key, name, lifecycle_definition_id)
		           VALUES ($1, 'lifecycle-scope-18', 'Lifecycle scope', '00000000-0000-0000-0000-000000000101') RETURNING id)
		UPDATE ci SET ci_type_id = (SELECT id FROM t) WHERE id IN ($2, $3)`, f.OrgA, own, foreign); err != nil {
		t.Fatalf("lifecycle type: %v", err)
	}

	repo := lifecycle.NewPGRepository(f.App)
	defA := &lifecycle.Definition{OrganizationID: f.OrgA, Key: "scopetest_a", Name: "Scopetest A", AppliesTo: "ci"}
	if err := repo.Create(f.OrgCtx(f.OrgA), defA); err != nil {
		t.Fatalf("create org A definition: %v", err)
	}
	defB := &lifecycle.Definition{OrganizationID: f.OrgB, Key: "scopetest_b", Name: "Scopetest B", AppliesTo: "ci"}
	if err := repo.Create(f.OrgCtx(f.OrgB), defB); err != nil {
		t.Fatalf("create org B definition: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	defs, _, err := repo.List(ctx, f.OrgA, api.PaginationParams{Limit: 500})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, d := range defs {
		seen[d.ID] = true
	}
	if !seen[defA.ID] || seen[defB.ID] {
		t.Fatalf("client-1 list: org A=%v org B=%v, want true/false", seen[defA.ID], seen[defB.ID])
	}
	if _, err = repo.GetByID(ctx, f.OrgA, defB.ID); err == nil {
		t.Fatal("organization A principal read a definition of organization B")
	}

	states := lifecycle.NewPGStateStore(f.App)
	if err = states.SetState(ctx, f.OrgA, "ci", own, "deployed"); err != nil {
		t.Fatalf("client-1 set state of own CI: %v", err)
	}
	if err = states.SetState(ctx, f.OrgA, "ci", foreign, "retired"); err == nil {
		t.Fatal("client-1 principal set the state of a client-2 CI")
	}
	if _, err = states.CurrentState(ctx, f.OrgA, "ci", foreign); err == nil {
		t.Fatal("client-1 principal read the state of a client-2 CI")
	}
	var state *string
	if err = f.Admin.QueryRow(context.Background(), `SELECT lifecycle_state FROM ci WHERE id = $1`, foreign).Scan(&state); err != nil {
		t.Fatalf("read foreign state: %v", err)
	}
	if state != nil {
		t.Fatalf("client-2 CI state changed to %q", *state)
	}

	if _, err = states.CurrentState(context.Background(), f.OrgA, "ci", own); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("state without scope: got %v, want ErrNoTenantScope", err)
	}
}
