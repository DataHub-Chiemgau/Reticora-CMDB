package permission_test

import (
	"context"
	"os"
	"sort"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixture ids of this file; distinct from the other integration tests that
// share the database.
const (
	rbaOrg        = "9ba03000-0000-4000-8000-000000000001"
	rbaClient1    = "9ba03000-0000-4000-8000-0000000000c1"
	rbaClient2    = "9ba03000-0000-4000-8000-0000000000c2"
	rbaSite1      = "9ba03000-0000-4000-8000-0000000000d1"
	rbaRoleTech   = "9ba03000-0000-4000-8000-0000000000a1"
	rbaRoleAdmin  = "9ba03000-0000-4000-8000-0000000000a2"
	rbaCustomRole = "9ba03000-0000-4000-8000-0000000000a3"
	rbaUserClient = "9ba03000-0000-4000-8000-0000000000f1"
	rbaUserTwo    = "9ba03000-0000-4000-8000-0000000000f2"
	rbaUserOrg    = "9ba03000-0000-4000-8000-0000000000f3"
	rbaUserSite   = "9ba03000-0000-4000-8000-0000000000f4"
	rbaUserBroken = "9ba03000-0000-4000-8000-0000000000f5"
	rbaUserNone   = "9ba03000-0000-4000-8000-0000000000f6"
)

func seedRBA03(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)
	cleanup := func() {
		// custom_role has no ON DELETE CASCADE to organization.
		for _, sql := range []string{
			`DELETE FROM user_custom_role WHERE custom_role_id = '` + rbaCustomRole + `'`,
			`DELETE FROM custom_role WHERE organization_id = '` + rbaOrg + `'`,
			`DELETE FROM organization WHERE id = '` + rbaOrg + `'`,
		} {
			if _, delErr := admin.Exec(ctx, sql); delErr != nil {
				t.Errorf("cleanup %q: %v", sql, delErr)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	stmts := []string{
		`INSERT INTO organization (id, name, slug) VALUES ('` + rbaOrg + `', 'RBA-03 Org', 'rba03-org')`,
		`INSERT INTO client (id, organization_id, name, slug) VALUES ('` + rbaClient1 + `', '` + rbaOrg + `', 'Client 1', 'rba03-client-1')`,
		`INSERT INTO client (id, organization_id, name, slug) VALUES ('` + rbaClient2 + `', '` + rbaOrg + `', 'Client 2', 'rba03-client-2')`,
		`INSERT INTO site (id, organization_id, client_id, name) VALUES ('` + rbaSite1 + `', '` + rbaOrg + `', '` + rbaClient1 + `', 'Site 1')`,
		// Role permissions come from role.permissions and role_permission.
		`INSERT INTO role (id, organization_id, name, permissions) VALUES ('` + rbaRoleTech + `', '` + rbaOrg + `', 'rba03-tech', '["ci:read"]')`,
		`INSERT INTO role_permission (organization_id, role_id, permission_key) VALUES ('` + rbaOrg + `', '` + rbaRoleTech + `', 'ci:write')`,
		`INSERT INTO role (id, organization_id, name, permissions) VALUES ('` + rbaRoleAdmin + `', '` + rbaOrg + `', 'rba03-admin', '["ci:read","ci:delete","no:such-permission"]')`,
		`INSERT INTO custom_role (id, organization_id, name, permissions) VALUES ('` + rbaCustomRole + `', '` + rbaOrg + `', 'rba03-custom', '["asset:read"]')`,
	}
	for _, u := range []string{rbaUserClient, rbaUserTwo, rbaUserOrg, rbaUserSite, rbaUserBroken, rbaUserNone} {
		stmts = append(stmts, `INSERT INTO app_user (id, organization_id, email, display_name) VALUES ('`+u+`', '`+rbaOrg+`', '`+u+`@example.test', 'RBA-03')`)
	}
	stmts = append(stmts,
		// Client-only user: one assignment in client 1.
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ('`+rbaOrg+`', '`+rbaUserClient+`', '`+rbaRoleTech+`', '`+rbaClient1+`')`,
		// Two client assignments: union of both clients.
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ('`+rbaOrg+`', '`+rbaUserTwo+`', '`+rbaRoleTech+`', '`+rbaClient1+`')`,
		`INSERT INTO user_custom_role (user_id, custom_role_id, scope_type, scope_id) VALUES ('`+rbaUserTwo+`', '`+rbaCustomRole+`', 'client', '`+rbaClient2+`')`,
		// Client assignment plus org-wide role: the org-wide role sets the scope to all.
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ('`+rbaOrg+`', '`+rbaUserOrg+`', '`+rbaRoleTech+`', '`+rbaClient1+`')`,
		`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ('`+rbaOrg+`', '`+rbaUserOrg+`', '`+rbaRoleAdmin+`')`,
		// Site-only custom role.
		`INSERT INTO user_custom_role (user_id, custom_role_id, scope_type, scope_id) VALUES ('`+rbaUserSite+`', '`+rbaCustomRole+`', 'site', '`+rbaSite1+`')`,
		// Client scope without id: invalid, grants nothing.
		`INSERT INTO user_custom_role (user_id, custom_role_id, scope_type) VALUES ('`+rbaUserBroken+`', '`+rbaCustomRole+`', 'client')`,
	)
	for _, sql := range stmts {
		if _, seedErr := admin.Exec(ctx, sql); seedErr != nil {
			t.Fatalf("seed %q: %v", sql, seedErr)
		}
	}

	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func resolve(ctx context.Context, t *testing.T, repo *permission.PGRepository, userID string) identity.Access {
	t.Helper()
	grants, err := repo.AccessGrants(ctx, rbaOrg, userID)
	if err != nil {
		t.Fatalf("AccessGrants(%s): %v", userID, err)
	}
	return identity.ResolveAccess(grants)
}

func TestAccessGrantsResolveScopesFromRoleAssignments(t *testing.T) {
	ctx, pool := seedRBA03(t)
	repo := permission.NewPGRepository(pool)

	t.Run("client-only user is not org-wide", func(t *testing.T) {
		access := resolve(ctx, t, repo, rbaUserClient)
		if access.Scope.OrgWide() || access.Scope.Clients.All {
			t.Fatalf("client-only user got org-wide client scope: %+v", access.Scope)
		}
		assertIDs(t, "clients", access.Scope.Clients, rbaClient1)
		if !access.Scope.Sites.All || !access.Scope.Teams.All {
			t.Fatalf("sites and teams are not restricted by a client assignment: %+v", access.Scope)
		}
		assertPermissions(t, access.Permissions, "ci:read", "ci:write")
	})

	t.Run("client sets are united", func(t *testing.T) {
		access := resolve(ctx, t, repo, rbaUserTwo)
		assertIDs(t, "clients", access.Scope.Clients, rbaClient1, rbaClient2)
		// asset:read is only granted in client 2.
		assertIDs(t, "asset:read clients", access.PermissionScopes["asset:read"].Clients, rbaClient2)
		assertIDs(t, "ci:read clients", mustScope(t, access, "ci:read").Clients, rbaClient1)
	})

	t.Run("org-wide role sets the scope to all", func(t *testing.T) {
		access := resolve(ctx, t, repo, rbaUserOrg)
		if !access.Scope.OrgWide() {
			t.Fatalf("org-wide role must make the scope org-wide: %+v", access.Scope)
		}
		// ci:write comes only from the client assignment.
		assertIDs(t, "ci:write clients", access.PermissionScopes["ci:write"].Clients, rbaClient1)
		// Unknown permission keys of a role are dropped.
		assertPermissions(t, access.Permissions, "ci:delete", "ci:read", "ci:write")
	})

	t.Run("site assignment restricts sites", func(t *testing.T) {
		access := resolve(ctx, t, repo, rbaUserSite)
		assertIDs(t, "sites", access.Scope.Sites, rbaSite1)
		if access.Scope.OrgWide() {
			t.Fatalf("site-only user got org-wide scope")
		}
	})

	t.Run("scoped assignment without scope id grants nothing", func(t *testing.T) {
		for _, user := range []string{rbaUserBroken, rbaUserNone} {
			access := resolve(ctx, t, repo, user)
			if len(access.Permissions) != 0 || access.Scope.Clients.All || len(access.Scope.Clients.IDs) != 0 {
				t.Fatalf("user %s must not get any access: %+v", user, access)
			}
		}
	})
}

// TestClientOnlyUserSeesOnlyAssignedClient runs the whole chain: role
// assignment → AccessGrants → principal scope → TenantScopeFor → WithTenant →
// client policy (migration 000033).
func TestClientOnlyUserSeesOnlyAssignedClient(t *testing.T) {
	ctx, pool := seedRBA03(t)
	repo := permission.NewPGRepository(pool)

	visible := func(userID string) []string {
		t.Helper()
		access := resolve(ctx, t, repo, userID)
		principal := identity.Principal{
			Subject: userID, OrganizationID: rbaOrg, Scope: &access.Scope,
			PermissionScopes: access.PermissionScopes, Permissions: access.Permissions,
		}
		scope := middleware.TenantScopeFor(&principal)
		var ids []string
		err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT id::text FROM client ORDER BY id`)
			if err != nil {
				return err
			}
			ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		if err != nil {
			t.Fatalf("WithTenant(%s): %v", userID, err)
		}
		return ids
	}

	if got := visible(rbaUserClient); len(got) != 1 || got[0] != rbaClient1 {
		t.Fatalf("client-only user sees clients %v, want only %s", got, rbaClient1)
	}
	if got := visible(rbaUserOrg); len(got) != 2 {
		t.Fatalf("org-wide user sees clients %v, want both", got)
	}
	if got := visible(rbaUserNone); len(got) != 0 {
		t.Fatalf("user without assignments sees clients %v, want none", got)
	}
}

func mustScope(t *testing.T, access identity.Access, p identity.Permission) identity.Scope {
	t.Helper()
	principal := identity.Principal{Scope: &access.Scope, PermissionScopes: access.PermissionScopes, Permissions: access.Permissions}
	s, ok := principal.ScopeFor(p)
	if !ok {
		t.Fatalf("permission %s not granted", p)
	}
	return s
}

func assertIDs(t *testing.T, name string, got identity.ScopeSet, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got.All || len(got.IDs) != len(want) {
		t.Fatalf("%s = %+v, want %v", name, got, want)
	}
	for i := range want {
		if got.IDs[i] != want[i] {
			t.Fatalf("%s = %+v, want %v", name, got, want)
		}
	}
}

func assertPermissions(t *testing.T, got []identity.Permission, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("permissions = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("permissions = %v, want %v", got, want)
		}
	}
}
