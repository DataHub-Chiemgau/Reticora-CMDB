package permission_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
// a user for site 1 combined with client 2, which no assignment can name since
// WP-391 (role_assignment_single_scope).
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

// Fixture ids of WP-391: site 3 of client 1, teams 1 and 2 of the
// organization and a team of organization B, users with a client role beside
// a site role (E-70) at site 1 (client 1) or site 2 (client 2) and beside a
// team role, users of the team assignments, and the CIs of the E-70 matrix.
const (
	rbaSite3           = "9ba03000-0000-4000-8000-0000000000d3"
	rbaTeam1           = "9ba03000-0000-4000-8000-0000000000e1"
	rbaTeam2           = "9ba03000-0000-4000-8000-0000000000e2"
	rbaTeamB           = "9ba03000-0000-4000-8000-0000000000b3"
	rbaUserMixed       = "9ba03000-0000-4000-8000-0000000000fb"
	rbaUserMixedOther  = "9ba03000-0000-4000-8000-0000000000fc"
	rbaUserClientTeam  = "9ba03000-0000-4000-8000-0000000000fd"
	rbaUserTeam        = "9ba03000-0000-4000-8000-0000000000fe"
	rbaUserTeamForeign = "9ba03000-0000-4000-8000-0000000000ff"
	rbaUserDuplicate   = "9ba03000-0000-4000-8000-0000000000ef"
	rbaCIClient1Site1  = "9ba03000-0000-4000-8000-000000000101"
	rbaCIClient1Site3  = "9ba03000-0000-4000-8000-000000000102"
	rbaCIClient1       = "9ba03000-0000-4000-8000-000000000103"
	rbaCIClient2Site2  = "9ba03000-0000-4000-8000-000000000104"
	rbaCIClient2       = "9ba03000-0000-4000-8000-000000000105"
	rbaCIOrg           = "9ba03000-0000-4000-8000-000000000106"
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
		// custom_role, team and ticket have no ON DELETE CASCADE to
		// organization.
		for _, sql := range []string{
			`DELETE FROM user_custom_role WHERE custom_role_id = '` + rbaCustomRole + `'`,
			`DELETE FROM custom_role WHERE organization_id = '` + rbaOrg + `'`,
			`DELETE FROM ticket WHERE organization_id = '` + rbaOrg + `'`,
			`DELETE FROM team WHERE organization_id IN ('` + rbaOrg + `', '` + rbaOrgB + `')`,
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
		`INSERT INTO site (id, organization_id, client_id, name) VALUES ('` + rbaSite3 + `', '` + rbaOrg + `', '` + rbaClient1 + `', 'Site 3')`,
		`INSERT INTO team (id, organization_id, name) VALUES ('` + rbaTeam1 + `', '` + rbaOrg + `', 'Team 1')`,
		`INSERT INTO team (id, organization_id, name) VALUES ('` + rbaTeam2 + `', '` + rbaOrg + `', 'Team 2')`,
		`INSERT INTO organization (id, name, slug) VALUES ('` + rbaOrgB + `', 'RBA-03 Org B', 'rba03-org-b')`,
		`INSERT INTO client (id, organization_id, name, slug) VALUES ('` + rbaClientB + `', '` + rbaOrgB + `', 'Client B', 'rba03-client-b')`,
		`INSERT INTO site (id, organization_id, client_id, name) VALUES ('` + rbaSiteB + `', '` + rbaOrgB + `', '` + rbaClientB + `', 'Site B')`,
		`INSERT INTO team (id, organization_id, name) VALUES ('` + rbaTeamB + `', '` + rbaOrgB + `', 'Team B')`,
		// Role permissions come from role.permissions and role_permission.
		`INSERT INTO role (id, organization_id, name, permissions) VALUES ('` + rbaRoleTech + `', '` + rbaOrg + `', 'rba03-tech', '["ci:read"]')`,
		`INSERT INTO role_permission (organization_id, role_id, permission_key) VALUES ('` + rbaOrg + `', '` + rbaRoleTech + `', 'ci:write')`,
		`INSERT INTO role (id, organization_id, name, permissions) VALUES ('` + rbaRoleAdmin + `', '` + rbaOrg + `', 'rba03-admin', '["ci:read","ci:delete","no:such-permission"]')`,
		`INSERT INTO custom_role (id, organization_id, name, permissions) VALUES ('` + rbaCustomRole + `', '` + rbaOrg + `', 'rba03-custom', '["asset:read"]')`,
	}
	for _, u := range []string{rbaUserClient, rbaUserTwo, rbaUserOrg, rbaUserSite, rbaUserBroken, rbaUserNone,
		rbaUserSiteRA, rbaUserSiteB, rbaUserSiteGone, rbaUserSiteOther,
		rbaUserMixed, rbaUserMixedOther, rbaUserClientTeam, rbaUserTeam, rbaUserTeamForeign, rbaUserDuplicate} {
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
		// E-70: client role in client 1 beside a site role at site 1 (client
		// 1), resp. at site 2 (client 2), and beside a team role.
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ('`+rbaOrg+`', '`+rbaUserMixed+`', '`+rbaRoleTech+`', '`+rbaClient1+`')`,
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_site_id) VALUES ('`+rbaOrg+`', '`+rbaUserMixed+`', '`+rbaRoleTech+`', '`+rbaSite1+`')`,
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ('`+rbaOrg+`', '`+rbaUserMixedOther+`', '`+rbaRoleTech+`', '`+rbaClient1+`')`,
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_site_id) VALUES ('`+rbaOrg+`', '`+rbaUserMixedOther+`', '`+rbaRoleTech+`', '`+rbaSite2+`')`,
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ('`+rbaOrg+`', '`+rbaUserClientTeam+`', '`+rbaRoleTech+`', '`+rbaClient1+`')`,
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_team_id) VALUES ('`+rbaOrg+`', '`+rbaUserClientTeam+`', '`+rbaRoleTech+`', '`+rbaTeam1+`')`,
	)
	// CIs of the E-70 matrix, placed at the location of their site.
	for _, ci := range []struct{ id, client, site string }{
		{rbaCIClient1Site1, rbaClient1, rbaSite1}, {rbaCIClient1Site3, rbaClient1, rbaSite3}, {rbaCIClient1, rbaClient1, ""},
		{rbaCIClient2Site2, rbaClient2, rbaSite2}, {rbaCIClient2, rbaClient2, ""}, {rbaCIOrg, "", ""},
	} {
		stmts = append(stmts, `INSERT INTO ci (id, organization_id, client_id, location_id, ci_type_id, name, status)
			SELECT '`+ci.id+`', '`+rbaOrg+`', NULLIF('`+ci.client+`', '')::uuid, NULLIF('`+ci.site+`', '')::uuid, id, 'rba03-`+ci.id[len(ci.id)-3:]+`', 'active'
			FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`)
	}
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

	t.Run("site of another organization or unknown site grants nothing", func(t *testing.T) {
		for _, user := range []string{rbaUserSiteB, rbaUserSiteGone} {
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

// TestRoleAssignmentScopeColumns covers the schema of WP-391 (migration
// 000150, RBA-03, F-049) on the runtime login: an assignment names at most one
// scope, so a site cannot be combined with a client (a site assignment has the
// client of its site, WP-390) nor a team with a client; the team must belong
// to the organization; the same role in the same scope is stored once, also
// org-wide (NULLS NOT DISTINCT).
func TestRoleAssignmentScopeColumns(t *testing.T) {
	ctx, pool := seedRBA03(t)
	scope := database.OrgWideScope(rbaOrg, "")
	insert := func(user, client, site, team string) error {
		return database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id, scope_site_id, scope_team_id)
				VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid)`, rbaOrg, user, rbaRoleTech, client, site, team)
			return err
		})
	}
	refusedBy := func(err error) string {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return pgErr.ConstraintName
		}
		return ""
	}
	for _, tc := range []struct {
		name, client, site, team, constraint string
	}{
		{"site with another client", rbaClient2, rbaSite1, "", "role_assignment_single_scope"},
		{"site with its client", rbaClient1, rbaSite1, "", "role_assignment_single_scope"},
		{"team with a client", rbaClient1, "", rbaTeam1, "role_assignment_single_scope"},
		{"team with a site", "", rbaSite1, rbaTeam1, "role_assignment_single_scope"},
		{"team of another organization", "", "", rbaTeamB, "role_assignment_scope_team_id_fkey"},
	} {
		if got := refusedBy(insert(rbaUserSiteOther, tc.client, tc.site, tc.team)); got != tc.constraint {
			t.Errorf("%s: refused by %q, want %s", tc.name, got, tc.constraint)
		}
	}
	for _, tc := range []struct{ name, client, site, team string }{
		{"organization", "", "", ""}, {"client", rbaClient1, "", ""}, {"site", "", rbaSite1, ""}, {"team", "", "", rbaTeam1},
	} {
		if err := insert(rbaUserSiteOther, tc.client, tc.site, tc.team); err != nil {
			t.Fatalf("%s assignment: %v", tc.name, err)
		}
		if got := refusedBy(insert(rbaUserSiteOther, tc.client, tc.site, tc.team)); got != "role_assignment_scope_key" {
			t.Errorf("repeated %s assignment refused by %q, want role_assignment_scope_key", tc.name, got)
		}
	}
}

// TestRoleAssignmentsTeamScope covers POST /api/v1/role-assignments of
// WP-391 (RBA-03, F-049) on PostgreSQL: a standard or custom role is assigned
// in a team of the organization (201) and AccessGrants returns it as
// Grant.Teams = [team], so the resolved scope is restricted to the teams and
// leaves clients and sites open (E-11); a team of another organization or an
// unknown team is 404 without a row; a repeated assignment of the same role in
// the same scope, org-wide as well, stores no second row. A custom role of a
// team outside the organization (scope_id has no foreign key) and an
// assignment of a deleted team grant nothing.
func TestRoleAssignmentsTeamScope(t *testing.T) {
	ctx, pool := seedRBA03(t)
	repo := permission.NewPGRepository(pool)
	mux := chi.NewRouter()
	user.NewHandler(user.NewPGRepository(pool)).RegisterRoutes(mux)
	scope := database.OrgWideScope(rbaOrg, rbaUserOrg)
	reqCtx := tenant.WithTenant(database.ContextWithTenantScope(ctx, &scope), tenant.TenantInfo{OrganizationID: rbaOrg, UserID: rbaUserOrg})
	call := func(method, path string, body any) (int, string) {
		t.Helper()
		raw := ""
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			raw = string(b)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(raw)).WithContext(reqCtx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	assign := func(body map[string]any) (int, user.UserRoleAssignment) {
		t.Helper()
		code, resp := call(http.MethodPost, "/api/v1/role-assignments", body)
		var a user.UserRoleAssignment
		if code == http.StatusCreated {
			if err := json.Unmarshal([]byte(resp), &a); err != nil {
				t.Fatalf("decode %s: %v", resp, err)
			}
		}
		return code, a
	}
	rows := func(userID string) int {
		t.Helper()
		var n int
		if err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM role_assignment WHERE user_id = $1)
				+ (SELECT count(*) FROM user_custom_role WHERE user_id = $1)`, userID).Scan(&n)
		}); err != nil {
			t.Fatalf("count assignments of %s: %v", userID, err)
		}
		return n
	}
	grantsOf := func(userID string) []identity.Grant {
		t.Helper()
		grants, err := repo.AccessGrants(ctx, rbaOrg, userID)
		if err != nil {
			t.Fatalf("AccessGrants(%s): %v", userID, err)
		}
		return grants
	}

	t.Run("a repeated org-wide assignment stores one row", func(t *testing.T) {
		body := map[string]any{"user_id": rbaUserDuplicate, "role_id": rbaRoleTech}
		code1, first := assign(body)
		code2, second := assign(body)
		if code1 != http.StatusCreated || code2 != http.StatusCreated || first.ID == "" || second.ID != first.ID {
			t.Fatalf("POST twice: %d %q, %d %q; want 201 with the same assignment", code1, first.ID, code2, second.ID)
		}
		if n := rows(rbaUserDuplicate); n != 1 {
			t.Fatalf("assignments after a repeated org-wide POST: %d, want 1", n)
		}
	})

	t.Run("standard and custom role in a team of the organization", func(t *testing.T) {
		body := map[string]any{"user_id": rbaUserTeam, "role_id": rbaRoleTech, "scope_type": "team", "scope_id": rbaTeam1}
		code, standard := assign(body)
		if code != http.StatusCreated || standard.ScopeType != "team" || standard.ScopeID != rbaTeam1 {
			t.Fatalf("POST team assignment: %d %+v, want 201 in team 1", code, standard)
		}
		if codeAgain, again := assign(body); codeAgain != http.StatusCreated || again.ID != standard.ID {
			t.Fatalf("repeated team assignment: %d %q, want 201 with %q", codeAgain, again.ID, standard.ID)
		}
		code, custom := assign(map[string]any{"user_id": rbaUserTeam, "custom_role_id": rbaCustomRole, "scope_type": "team", "scope_id": rbaTeam2})
		if code != http.StatusCreated || custom.ScopeType != "team" {
			t.Fatalf("POST custom team assignment: %d %+v, want 201", code, custom)
		}
		if n := rows(rbaUserTeam); n != 2 {
			t.Fatalf("assignments of the team user: %d, want 2", n)
		}

		grants := grantsOf(rbaUserTeam)
		teams := map[string][]identity.Permission{}
		for _, g := range grants {
			if len(g.Clients) != 0 || len(g.Sites) != 0 || len(g.Teams) != 1 {
				t.Fatalf("team grant %+v must name its team only", g)
			}
			teams[g.Teams[0]] = g.Permissions
		}
		if len(grants) != 2 || len(teams[rbaTeam1]) != 2 || len(teams[rbaTeam2]) != 1 || teams[rbaTeam2][0] != "asset:read" {
			t.Fatalf("grants %+v, want ci:read/ci:write in team 1 and asset:read in team 2", grants)
		}
		access := identity.ResolveAccess(grants)
		assertIDs(t, "teams", access.Scope.Teams, rbaTeam1, rbaTeam2)
		if !access.Scope.Clients.All || !access.Scope.Sites.All || access.Scope.OrgWide() {
			t.Fatalf("a team scope leaves clients and sites open and is not org-wide: %+v", access.Scope)
		}
		assertIDs(t, "ci:write teams", mustScope(t, access, "ci:write").Teams, rbaTeam1)
		assertIDs(t, "asset:read teams", mustScope(t, access, "asset:read").Teams, rbaTeam2)

		code, list := call(http.MethodGet, "/api/v1/role-assignments?user_id="+rbaUserTeam, nil)
		var listed struct {
			Data []user.UserRoleAssignment `json:"data"`
		}
		if err := json.Unmarshal([]byte(list), &listed); code != http.StatusOK || err != nil || len(listed.Data) != 2 {
			t.Fatalf("GET role-assignments: %d %s (%v)", code, list, err)
		}
		for _, a := range listed.Data {
			if a.ScopeType != "team" || (a.ScopeID != rbaTeam1 && a.ScopeID != rbaTeam2) {
				t.Errorf("listed assignment %+v, want scope_type team", a)
			}
		}
	})

	t.Run("team of another organization or unknown team is 404 without a row", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"role_id": rbaRoleTech, "scope_id": rbaTeamB},
			{"custom_role_id": rbaCustomRole, "scope_id": rbaTeamB},
			{"role_id": rbaRoleTech, "scope_id": rbaUnknownSite},
			{"custom_role_id": rbaCustomRole, "scope_id": rbaUnknownSite},
			{"role_id": rbaRoleTech, "scope_id": "not-a-team"},
			{"role_id": rbaRoleTech},
		} {
			body["user_id"], body["scope_type"] = rbaUserTeamForeign, "team"
			if code, _ := assign(body); code != http.StatusNotFound {
				t.Errorf("POST %v: %d, want 404", body, code)
			}
		}
		if n := rows(rbaUserTeamForeign); n != 0 {
			t.Fatalf("refused team assignments stored %d rows", n)
		}
	})

	t.Run("custom role of a foreign team or of a deleted team grants nothing", func(t *testing.T) {
		if err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO user_custom_role (user_id, custom_role_id, scope_type, scope_id) VALUES ($1, $2, 'team', $3)`,
				rbaUserTeamForeign, rbaCustomRole, rbaTeamB)
			return err
		}); err != nil {
			t.Fatalf("insert custom role of a foreign team: %v", err)
		}
		if grants := grantsOf(rbaUserTeamForeign); len(grants) != 0 {
			t.Fatalf("custom role of a team of organization B granted %+v", grants)
		}
		// Deleting a team removes its standard assignments (ON DELETE
		// CASCADE); its custom assignments stay but grant nothing.
		for _, team := range []string{rbaTeam1, rbaTeam2} {
			if err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `DELETE FROM team WHERE id = $1`, team)
				return err
			}); err != nil {
				t.Fatalf("delete team: %v", err)
			}
		}
		if grants := grantsOf(rbaUserTeam); len(grants) != 0 {
			t.Fatalf("assignments of deleted teams granted %+v", grants)
		}
		if n := rows(rbaUserTeam); n != 1 {
			t.Fatalf("assignments of the team user after deleting the teams: %d, want the custom one", n)
		}
	})
}

// TestMixedGrantsFollowE70 fixes the visibility of mixed client and site
// assignments after E-70 option 1 (transition rule, RBA-03, TEN-04, F-181):
// the scope sets are united per dimension and the dimensions intersect in the
// RLS. The cells of the retest (client role C1, site role S1; CIs in C1/S1,
// C1/S2 and C2/S1) are realized in both layouts, since a CI has the client of
// its site (ci_derive_location):
//   - S1 a site of C1: the CI of C1 at S1 is visible, the CI of C1 at another
//     site of C1 (C1/S2) is not, the client role alone saw it;
//   - S1 a site of C2: the site role adds C2, its CI at S1 (C2/S1) is visible,
//     the CI of C1 at a site of C1 is not.
//
// CIs without site of the united clients and org-wide CIs stay visible. A
// team role beside a client role narrows the tickets of the client to the
// team and tickets without team, which are read-only (E-11, TEN-05), and adds
// no client. Every principal is resolved through role_assignment →
// AccessGrants → ResolveAccess → TenantScopeFor → WithTenant.
func TestMixedGrantsFollowE70(t *testing.T) {
	ctx, pool := seedRBA03(t)
	repo := permission.NewPGRepository(pool)
	tenantOf := func(userID string) database.TenantScope {
		t.Helper()
		access := resolve(ctx, t, repo, userID)
		return middleware.TenantScopeFor(&identity.Principal{
			Subject: userID, OrganizationID: rbaOrg, Scope: &access.Scope,
			PermissionScopes: access.PermissionScopes, Permissions: access.Permissions,
		})
	}
	visible := func(userID, table string, ids ...string) map[string]bool {
		t.Helper()
		scope := tenantOf(userID)
		seen := map[string]bool{}
		err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT id::text FROM `+table+` WHERE id = ANY($1::uuid[])`, ids)
			if err != nil {
				return err
			}
			found, err := pgx.CollectRows(rows, pgx.RowTo[string])
			for _, id := range found {
				seen[id] = true
			}
			return err
		})
		if err != nil {
			t.Fatalf("read %s as %s: %v", table, userID, err)
		}
		return seen
	}

	mixed := resolve(ctx, t, repo, rbaUserMixed)
	assertIDs(t, "client 1 + site 1: clients", mixed.Scope.Clients, rbaClient1)
	assertIDs(t, "client 1 + site 1: sites", mixed.Scope.Sites, rbaSite1)
	other := resolve(ctx, t, repo, rbaUserMixedOther)
	assertIDs(t, "client 1 + site 2: clients", other.Scope.Clients, rbaClient1, rbaClient2)
	assertIDs(t, "client 1 + site 2: sites", other.Scope.Sites, rbaSite2)

	cis := []string{rbaCIClient1Site1, rbaCIClient1Site3, rbaCIClient1, rbaCIClient2Site2, rbaCIClient2, rbaCIOrg}
	names := map[string]string{
		rbaCIClient1Site1: "C1 at site 1", rbaCIClient1Site3: "C1 at site 3", rbaCIClient1: "C1 without site",
		rbaCIClient2Site2: "C2 at site 2", rbaCIClient2: "C2 without site", rbaCIOrg: "org-wide",
	}
	for _, tc := range []struct {
		name, user string
		want       []bool // in the order of cis
	}{
		{"client role 1 alone", rbaUserClient, []bool{true, true, true, false, false, true}},
		{"client role 1 + site role at site 1 of client 1", rbaUserMixed, []bool{true, false, true, false, false, true}},
		{"client role 1 + site role at site 2 of client 2", rbaUserMixedOther, []bool{false, false, true, true, true, true}},
		{"client role 1 + team role", rbaUserClientTeam, []bool{true, true, true, false, false, true}},
	} {
		seen := visible(tc.user, "ci", cis...)
		for i, id := range cis {
			if seen[id] != tc.want[i] {
				t.Errorf("%s: CI %s visible = %v, want %v", tc.name, names[id], seen[id], tc.want[i])
			}
		}
	}

	// Tickets of the client and team user (E-11 intersection).
	orgScope := database.OrgWideScope(rbaOrg, rbaUserOrg)
	tickets := map[string]string{}
	for _, tk := range []struct{ key, ci, team string }{
		{"C1 team 1", rbaCIClient1, rbaTeam1}, {"C1 team 2", rbaCIClient1, rbaTeam2}, {"C1 without team", rbaCIClient1, ""},
		{"C2 team 1", rbaCIClient2, rbaTeam1}, {"org-wide team 1", "", rbaTeam1}, {"org-wide team 2", "", rbaTeam2},
	} {
		if err := database.WithTenant(ctx, pool, &orgScope, func(ctx context.Context, tx pgx.Tx) error {
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO ticket (organization_id, title, reporter_id, related_ci_id, team_id)
				VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid) RETURNING id::text`,
				rbaOrg, "rba03 "+tk.key, rbaUserOrg, tk.ci, tk.team).Scan(&id)
			tickets[tk.key] = id
			return err
		}); err != nil {
			t.Fatalf("insert ticket %s: %v", tk.key, err)
		}
	}
	ticketIDs := make([]string, 0, len(tickets))
	for _, id := range tickets {
		ticketIDs = append(ticketIDs, id)
	}
	seen := visible(rbaUserClientTeam, "ticket", ticketIDs...)
	for key, want := range map[string]bool{
		"C1 team 1": true, "C1 team 2": false, "C1 without team": true,
		"C2 team 1": false, "org-wide team 1": true, "org-wide team 2": false,
	} {
		if seen[tickets[key]] != want {
			t.Errorf("client role 1 + team role: ticket %s visible = %v, want %v", key, seen[tickets[key]], want)
		}
	}
	teamScope := tenantOf(rbaUserClientTeam)
	for key, writable := range map[string]bool{"C1 team 1": true, "C1 without team": false} {
		var updated int64
		err := database.WithTenant(ctx, pool, &teamScope, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE ticket SET title = title || ' (seen)' WHERE id = $1`, tickets[key])
			updated = tag.RowsAffected()
			return err
		})
		if (err == nil && updated == 1) != writable {
			t.Errorf("client role 1 + team role: update ticket %s: %d rows, %v; want writable %v", key, updated, err, writable)
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
