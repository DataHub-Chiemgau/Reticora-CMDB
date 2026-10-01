package user_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// TestUserRepositoryScope runs the user repository with the principal's
// tenant scope (TEN-06, WP-012). Users, teams and roles belong to the
// organization: a client-scoped principal of organization A sees A's users
// but none of organization B, and naming B fails. First-login provisioning
// runs before a principal exists and acts org-wide for the organization the
// IdP names.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestUserRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "1a")
	repo := user.NewPGRepository(f.App)

	// Login provisioning: no scope in the context.
	idA, err := repo.EnsureUser(context.Background(), f.OrgA, "scopetest-1a-a", "a@scopetest.invalid", "User A")
	if err != nil {
		t.Fatalf("provision org A user: %v", err)
	}
	if err = repo.EnsureRole(context.Background(), f.OrgA, idA, "viewer"); err != nil {
		t.Fatalf("provision org A role: %v", err)
	}
	idB, err := repo.EnsureUser(context.Background(), f.OrgB, "scopetest-1a-b", "b@scopetest.invalid", "User B")
	if err != nil {
		t.Fatalf("provision org B user: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	users, _, err := repo.ListUsers(ctx, f.OrgA, "", api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, u := range users {
		seen[u.ID] = true
	}
	if !seen[idA] || seen[idB] {
		t.Fatalf("org A list: own=%v org B=%v, want true/false", seen[idA], seen[idB])
	}
	if _, err = repo.GetUser(ctx, f.OrgA, idB); err == nil {
		t.Fatal("organization A principal read a user of organization B")
	}
	if err = repo.DeleteUser(ctx, f.OrgA, idB); err == nil {
		t.Fatal("organization A principal deleted a user of organization B")
	}
	if _, err = repo.GetUser(ctx, f.OrgB, idB); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("read with organization B: got %v, want ErrTenantMismatch", err)
	}
	roles, err := repo.ListUserRoles(ctx, f.OrgA, idA)
	if err != nil || len(roles) == 0 {
		t.Fatalf("roles of provisioned user: %v err=%v, want the viewer role", roles, err)
	}

	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM app_user WHERE id = $1`, idB).Scan(&count); err != nil {
		t.Fatalf("count org B user: %v", err)
	}
	if count != 1 {
		t.Fatal("user of organization B was deleted")
	}
	if _, _, err = repo.ListUsers(context.Background(), f.OrgA, "", api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
