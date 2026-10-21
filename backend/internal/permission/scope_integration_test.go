package permission_test

import (
	"context"
	"os"
	"sort"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
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

// Fixture ids of the site assignments of WP-390: site 2 of client 2, an
// unknown site, organization B with a site, and users with a standard role at
// site 1, assignments naming the site of organization B, an unknown site, and
// site 1 combined with client 2.
const (
	rbaSite2         = "9ba03000-0000-4000-8000-0000000000d2"
	rbaUnknownSite   = "9ba03000-0000-4000-8000-0000000000d9"
	rbaOrgB          = "9ba03000-0000-4000-8000-000000000002"
	rbaClientB       = "9ba03000-0000-4000-8000-0000000000b1"
	rbaSiteB         = "9ba03000-0000-4000-8000-0000000000b2"
	rbaUserSiteRA    = "9ba03000-0000-4000-8000-0000000000f7"
	rbaUserSiteB     = "9ba03000-0000-4000-8000-0000000000f8"
	rbaUserSiteGone  = "9ba03000-0000-4000-8000-0000000000f9"
	rbaUserSiteOther = "9ba03000-0000-4000-8000-0000000000fa"
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
			`DELETE FROM organization WHERE id = '` + rbaOrgB + `'`,
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
		`INSERT INTO site (id, organization_id, client_id, name) VALUES ('` + rbaSite2 + `', '` + rbaOrg + `', '` + rbaClient2 + `', 'Site 2')`,
		`INSERT INTO organization (id, name, slug) VALUES ('` + rbaOrgB + `', 'RBA-03 Org B', 'rba03-org-b')`,
		`INSERT INTO client (id, organization_id, name, slug) VALUES ('` + rbaClientB + `', '` + rbaOrgB + `', 'Client B', 'rba03-client-b')`,
		`INSERT INTO site (id, organization_id, client_id, name) VALUES ('` + rbaSiteB + `', '` + rbaOrgB + `', '` + rbaClientB + `', 'Site B')`,
		// Role permissions come from role.permissions and role_permission.
		`INSERT INTO role (id, organization_id, name, permissions) VALUES ('` + rbaRoleTech + `', '` + rbaOrg + `', 'rba03-tech', '["ci:read"]')`,
		`INSERT INTO role_permission (organization_id, role_id, permission_key) VALUES ('` + rbaOrg + `', '` + rbaRoleTech + `', 'ci:write')`,
		`INSERT INTO role (id, organization_id, name, permissions) VALUES ('` + rbaRoleAdmin + `', '` + rbaOrg + `', 'rba03-admin', '["ci:read","ci:delete","no:such-permission"]')`,
		`INSERT INTO custom_role (id, organization_id, name, permissions) VALUES ('` + rbaCustomRole + `', '` + rbaOrg + `', 'rba03-custom', '["asset:read"]')`,
	}
	for _, u := range []string{rbaUserClient, rbaUserTwo, rbaUserOrg, rbaUserSite, rbaUserBroken, rbaUserNone,
		rbaUserSiteRA, rbaUserSiteB, rbaUserSiteGone, rbaUserSiteOther} {
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
		// Standard role at site 1 (client 1).
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_site_id) VALUES ('`+rbaOrg+`', '`+rbaUserSiteRA+`', '`+rbaRoleTech+`', '`+rbaSite1+`')`,
		// Sites of organization B and an unknown site: grant nothing.
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_site_id) VALUES ('`+rbaOrg+`', '`+rbaUserSiteB+`', '`+rbaRoleTech+`', '`+rbaSiteB+`')`,
		`INSERT INTO user_custom_role (user_id, custom_role_id, scope_type, scope_id) VALUES ('`+rbaUserSiteB+`', '`+rbaCustomRole+`', 'site', '`+rbaSiteB+`')`,
		`INSERT INTO user_custom_role (user_id, custom_role_id, scope_type, scope_id) VALUES ('`+rbaUserSiteGone+`', '`+rbaCustomRole+`', 'site', '`+rbaUnknownSite+`')`,
		// Site 1 (client 1) combined with client 2: grants nothing.
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id, scope_site_id) VALUES ('`+rbaOrg+`', '`+rbaUserSiteOther+`', '`+rbaRoleTech+`', '`+rbaClient2+`', '`+rbaSite1+`')`,
	)
	for _, sql := range stmts {
		if _, seedErr := admin.Exec(ctx, sql); seedErr != nil {
			t.Fatalf("seed %q: %v", sql, seedErr)
		}
	}

	pool, err := database.NewPool(ctx, scopetest.AppDSN(t))
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

	// WP-390 (RBA-03, TEN-09, F-014): a site assignment carries the client of
	// its site, so the client dimension is never left unrestricted.
	t.Run("site assignment restricts clients to the client of the site", func(t *testing.T) {
		for _, user := range []string{rbaUserSite, rbaUserSiteRA} {
			grants, err := repo.AccessGrants(ctx, rbaOrg, user)
			if err != nil {
				t.Fatalf("AccessGrants(%s): %v", user, err)
			}
			if len(grants) != 1 || len(grants[0].Clients) != 1 || grants[0].Clients[0] != rbaClient1 ||
				len(grants[0].Sites) != 1 || grants[0].Sites[0] != rbaSite1 {
				t.Fatalf("user %s: grants %+v, want one grant in client 1 at site 1", user, grants)
			}
			access := identity.ResolveAccess(grants)
			assertIDs(t, "clients", access.Scope.Clients, rbaClient1)
			assertIDs(t, "sites", access.Scope.Sites, rbaSite1)
		}
		assertPermissions(t, resolve(ctx, t, repo, rbaUserSiteRA).Permissions, "ci:read", "ci:write")
	})

	t.Run("site of another organization, unknown site or site of another client grants nothing", func(t *testing.T) {
		for _, user := range []string{rbaUserSiteB, rbaUserSiteGone, rbaUserSiteOther} {
			grants, err := repo.AccessGrants(ctx, rbaOrg, user)
			if err != nil {
				t.Fatalf("AccessGrants(%s): %v", user, err)
			}
			if len(grants) != 0 {
				t.Fatalf("user %s must not get any grant: %+v", user, grants)
			}
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
	// WP-390: a site user sees the client of its site only.
	for _, user := range []string{rbaUserSite, rbaUserSiteRA} {
		if got := visible(user); len(got) != 1 || got[0] != rbaClient1 {
			t.Fatalf("site user %s sees clients %v, want only %s", user, got, rbaClient1)
		}
	}
}

// TestMemoryAccessGrantsConfineSitesToTheirClient covers the memory
// repository of WP-390: like the PostgreSQL repository it gives a site
// assignment the client of its site (RegisterSite); a site of unknown client,
// of another organization or outside the assignment's clients grants nothing.
func TestMemoryAccessGrantsConfineSitesToTheirClient(t *testing.T) {
	ctx := context.Background()
	repo := permission.NewMemoryRepository()
	if _, err := repo.ReplaceRolePermissions(ctx, "org-1", "viewer", "", []string{"ci:read"}); err != nil {
		t.Fatal(err)
	}
	repo.RegisterSite("org-1", "site-1", "client-1")
	repo.RegisterSite("org-1", "site-2", "client-2")
	repo.RegisterSite("org-2", "site-b", "client-b")
	repo.AssignScopedRoleToUser("org-1", "site-user", "viewer", nil, []string{"site-1"})
	repo.AssignScopedRoleToUser("org-1", "two-sites", "viewer", nil, []string{"site-1", "site-2"})
	repo.AssignScopedRoleToUser("org-1", "client-and-site", "viewer", []string{"client-1"}, []string{"site-1"})
	repo.AssignScopedRoleToUser("org-1", "other-client", "viewer", []string{"client-2"}, []string{"site-1"})
	repo.AssignScopedRoleToUser("org-1", "foreign-site", "viewer", nil, []string{"site-b"})
	repo.AssignScopedRoleToUser("org-1", "unknown-site", "viewer", nil, []string{"site-9"})

	for _, tc := range []struct {
		user           string
		clients, sites []string
	}{
		{"site-user", []string{"client-1"}, []string{"site-1"}},
		{"two-sites", []string{"client-1", "client-2"}, []string{"site-1", "site-2"}},
		{"client-and-site", []string{"client-1"}, []string{"site-1"}},
	} {
		grants, err := repo.AccessGrants(ctx, "org-1", tc.user)
		if err != nil {
			t.Fatal(err)
		}
		access := identity.ResolveAccess(grants)
		assertIDs(t, tc.user+" clients", access.Scope.Clients, tc.clients...)
		assertIDs(t, tc.user+" sites", access.Scope.Sites, tc.sites...)
		assertPermissions(t, access.Permissions, "ci:read")
	}
	for _, user := range []string{"other-client", "foreign-site", "unknown-site"} {
		if grants, err := repo.AccessGrants(ctx, "org-1", user); err != nil || len(grants) != 0 {
			t.Errorf("user %s: grants %+v, %v; want none", user, grants, err)
		}
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
