package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestGrantCeilingTargets covers the target grants of WP-394 (RBA-03,
// F-015): handing out a role in a scope needs the route permission and every
// permission of the role in a scope covering the target scope; the coverage
// itself is identity.GrantCeiling (TestGrantCeiling). The organization, a
// client, a site with the client of the site (site.client_id) or without a
// known client, and a team are the targets; a scope without ID or of an
// unknown type counts as organization-wide (fail-closed). The route
// permission is checked in the target scope as well: a principal with the
// role's permissions org-wide but user:manage only in client 1 invites into
// client 1 only.
func TestGrantCeilingTargets(t *testing.T) {
	const c1, c2, s1, s2, t1 = "c-1", "c-2", "s-1", "s-2", "t-1"
	resolved := func(grants ...identity.Grant) *identity.Principal {
		access := identity.ResolveAccess(grants)
		scope := access.Scope
		return &identity.Principal{Type: identity.PrincipalTypeUser, Permissions: access.Permissions, Scope: &scope,
			PermissionScopes: access.PermissionScopes, ClientScope: scope.LegacyClientScope()}
	}
	perms := func(p ...identity.Permission) []identity.Permission { return p }
	admin := perms(identity.PermCIRead, identity.PermCIWrite, identity.PermUserManage, identity.PermRoleManage)
	orgAdmin := resolved(identity.OrgWideGrant(admin))
	delegate := resolved(identity.Grant{Permissions: admin, Clients: []string{c1}})
	siteAdmin := resolved(identity.Grant{Permissions: admin, Clients: []string{c1}, Sites: []string{s1}})
	// Role permissions org-wide, user:manage only in client 1.
	narrowRoute := resolved(identity.OrgWideGrant(perms(identity.PermCIRead)),
		identity.Grant{Permissions: perms(identity.PermUserManage), Clients: []string{c1}})
	viewer := perms(identity.PermCIRead)

	for _, tc := range []struct {
		name                           string
		caller                         *identity.Principal
		route                          identity.Permission
		role                           []identity.Permission
		scopeType, scopeID, siteClient string
		want                           []identity.Permission
	}{
		{"org-wide covers org-wide", orgAdmin, identity.PermRoleManage, admin, "organization", "", "", nil},
		{"org-wide covers org (invitation spelling)", orgAdmin, identity.PermUserManage, admin, "org", "", "", nil},
		{"org-wide covers a client", orgAdmin, identity.PermRoleManage, admin, "client", c2, "", nil},
		{"org-wide covers a site without known client", orgAdmin, identity.PermRoleManage, admin, "site", s2, "", nil},
		{"org-wide covers a team", orgAdmin, identity.PermRoleManage, admin, "team", t1, "", nil},
		{"delegate: org-wide org_admin", delegate, identity.PermRoleManage, admin, "organization", "", "", admin},
		{"delegate: org-wide viewer", delegate, identity.PermUserManage, viewer, "org", "", "", perms(identity.PermCIRead, identity.PermUserManage)},
		{"delegate: own client", delegate, identity.PermRoleManage, admin, "client", c1, "", nil},
		{"delegate: other client", delegate, identity.PermUserManage, viewer, "client", c2, "", perms(identity.PermCIRead, identity.PermUserManage)},
		{"delegate: site of own client", delegate, identity.PermUserManage, viewer, "site", s1, c1, nil},
		{"delegate: site of other client", delegate, identity.PermUserManage, viewer, "site", s2, c2, perms(identity.PermCIRead, identity.PermUserManage)},
		{"delegate: site without known client", delegate, identity.PermUserManage, viewer, "site", s1, "", perms(identity.PermCIRead, identity.PermUserManage)},
		{"delegate: team", delegate, identity.PermRoleManage, admin, "team", t1, "", admin},
		{"delegate: client without id is org-wide", delegate, identity.PermRoleManage, viewer, "client", "", "", perms(identity.PermCIRead, identity.PermRoleManage)},
		{"delegate: unknown scope type is org-wide", delegate, identity.PermRoleManage, viewer, "region", c1, "", perms(identity.PermCIRead, identity.PermRoleManage)},
		{"site admin: own site", siteAdmin, identity.PermRoleManage, viewer, "site", s1, c1, nil},
		{"site admin: its client", siteAdmin, identity.PermRoleManage, viewer, "client", c1, "", perms(identity.PermCIRead, identity.PermRoleManage)},
		{"site admin: other site of its client", siteAdmin, identity.PermRoleManage, viewer, "site", s2, c1, perms(identity.PermCIRead, identity.PermRoleManage)},
		{"route permission in a narrower scope: client 1", narrowRoute, identity.PermUserManage, viewer, "client", c1, "", nil},
		{"route permission in a narrower scope: org-wide", narrowRoute, identity.PermUserManage, viewer, "org", "", "", perms(identity.PermUserManage)},
		{"route permission missing", orgAdmin, identity.PermAPIKeyManage, viewer, "organization", "", "", perms(identity.PermAPIKeyManage)},
		{"role permission missing", delegate, identity.PermRoleManage, perms(identity.PermCIRead, identity.PermAuditRead), "client", c1, "", perms(identity.PermAuditRead)},
		{"role without permissions needs the route permission", delegate, identity.PermRoleManage, nil, "organization", "", "", perms(identity.PermRoleManage)},
		{"nil caller holds nothing", nil, identity.PermRoleManage, viewer, "client", c1, "", perms(identity.PermCIRead, identity.PermRoleManage)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := grantCeiling(tc.caller, tc.route, tc.role, tc.scopeType, tc.scopeID, tc.siteClient)
			var ceiling *GrantCeilingError
			var got []identity.Permission
			if errors.As(err, &ceiling) {
				got = ceiling.Missing
			} else if err != nil {
				t.Fatalf("grantCeiling: %v", err)
			}
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("missing %v, want %v", got, want)
			}
		})
	}
	if slices.Contains(viewer, identity.PermUserManage) {
		t.Fatal("grantCeiling changed the role's permissions")
	}
}

// TestCheckRoleScope covers the role/scope match of WP-394 (F-305): a client
// role only with scope_type client, a site role only with scope_type site, an
// organization role with every scope; otherwise 422 field scope_type.
func TestCheckRoleScope(t *testing.T) {
	for _, tc := range []struct {
		role, scope string
		ok          bool
	}{
		{"client", "client", true}, {"client", "org", false}, {"client", "organization", false},
		{"client", "site", false}, {"client", "team", false},
		{"site", "site", true}, {"site", "client", false}, {"site", "org", false}, {"site", "team", false},
		{"org", "org", true}, {"org", "organization", true}, {"org", "client", true}, {"org", "site", true}, {"org", "team", true},
	} {
		err := checkRoleScope(tc.role, tc.scope)
		var fe *api.FieldError
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s role with scope_type %s: %v, want accepted", tc.role, tc.scope, err)
		case !tc.ok && (!errors.As(err, &fe) || fe.Field != "scope_type" || fe.Status != http.StatusUnprocessableEntity):
			t.Errorf("%s role with scope_type %s: %v, want 422 scope_type", tc.role, tc.scope, err)
		}
	}
}

// TestValidateAssignmentScope covers the scope fields of a role assignment
// (WP-394): a client or site scope names a client or site, an org-wide one
// none, other scope types are 422; a team is checked against the
// organization later (404).
func TestValidateAssignmentScope(t *testing.T) {
	const id = "c0de0000-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		scopeType, scopeID, field string
	}{
		{"organization", "", ""}, {"organization", id, "scope_id"},
		{"client", id, ""}, {"client", "", "scope_id"}, {"client", "not-a-client", "scope_id"},
		{"site", id, ""}, {"site", "", "scope_id"},
		{"team", id, ""}, {"team", "", ""},
		{"region", id, "scope_type"}, {"org", "", "scope_type"},
	} {
		err := validateAssignmentScope(&UserRoleAssignment{ScopeType: tc.scopeType, ScopeID: tc.scopeID})
		var fe *api.FieldError
		if tc.field == "" {
			if err != nil {
				t.Errorf("%s %q: %v, want accepted", tc.scopeType, tc.scopeID, err)
			}
			continue
		}
		if !errors.As(err, &fe) || fe.Field != tc.field || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s %q: %v, want 422 %s", tc.scopeType, tc.scopeID, err, tc.field)
		}
	}
}

// TestCheckRoleWidening covers the role definitions of WP-394 (F-015): every
// permission a change adds must be held organization-wide; removing or
// keeping permissions is not limited.
func TestCheckRoleWidening(t *testing.T) {
	admin := []identity.Permission{identity.PermCIRead, identity.PermRoleManage}
	orgAdmin := &identity.Principal{Permissions: admin}
	scope := identity.Scope{Clients: identity.ScopeSet{IDs: []string{"c-1"}}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	delegate := &identity.Principal{Permissions: admin, Scope: &scope}
	for _, tc := range []struct {
		name          string
		caller        *identity.Principal
		current, next []string
		missing       []identity.Permission
	}{
		{"org-wide adds a held permission", orgAdmin, []string{"ci:read"}, []string{"ci:read", "role:manage"}, nil},
		{"org-wide adds a permission it lacks", orgAdmin, nil, []string{"user:manage"}, []identity.Permission{identity.PermUserManage}},
		{"client-scoped adds a permission it holds in its client", delegate, []string{"ci:read"}, []string{"ci:read", "role:manage"},
			[]identity.Permission{identity.PermRoleManage}},
		{"client-scoped keeps the permissions", delegate, []string{"ci:read", "user:manage"}, []string{"user:manage", "ci:read"}, nil},
		{"client-scoped removes a permission", delegate, []string{"ci:read", "user:manage"}, []string{"ci:read"}, nil},
		{"nil caller adds", nil, nil, []string{"ci:read"}, []identity.Permission{identity.PermCIRead}},
		{"nil caller removes", nil, []string{"ci:read"}, []string{}, nil},
	} {
		err := checkRoleWidening(tc.caller, tc.current, tc.next)
		var widening *RoleWideningError
		var got []identity.Permission
		if errors.As(err, &widening) {
			got = widening.Missing
		} else if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !slices.Equal(got, tc.missing) {
			t.Errorf("%s: missing %v, want %v", tc.name, got, tc.missing)
		}
	}
}

// TestRoleAssignmentGrantCeilingTeamScope covers the team target of WP-394
// (after WP-391): a team scope restricts only the team dimension, so a
// client-1 delegate (role:manage with scope_client_id = client 1) gets 403
// (RFC 7807) for org_admin in a team on POST /role-assignments and /roles/assign,
// without row and without audit; the org-wide org_admin gets 201. A client or
// site role in a team is 422 scope_type (F-305).
func TestRoleAssignmentGrantCeilingTeamScope(t *testing.T) {
	c := newCeilingFixture(t, "f8")
	adminRole := auditRoleID(t, c.Fixture, "org_admin")
	techRole := auditRoleID(t, c.Fixture, "client_technician")
	siteRole := c.ID()
	c.exec(`INSERT INTO role (id, organization_id, name, permissions, scope) VALUES ($1, $2, 'team-ceiling-site-reader', '["ci:read"]', 'site')`, siteRole, c.OrgA)
	var team string
	if err := c.Admin.QueryRow(context.Background(), `INSERT INTO team (organization_id, name) VALUES ($1, 'ceiling team') RETURNING id::text`, c.OrgA).Scan(&team); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// team has no ON DELETE CASCADE to organization; its assignments go with it.
		_, _ = c.Admin.Exec(context.Background(), `DELETE FROM team WHERE id = $1`, team)
	})
	mux := chi.NewRouter()
	NewHandler(NewPGRepository(c.App)).RegisterRoutes(mux)
	body := func(user, role string) string {
		return `{"user_id":"` + user + `","role_id":"` + role + `","scope_type":"team","scope_id":"` + team + `"}`
	}

	for _, path := range []string{"/api/v1/role-assignments", "/api/v1/roles/assign"} {
		target := c.AppUser(t, c.OrgA, "team-target"+path[len(path)-6:])
		audits := c.audits()
		w := auditPost(mux, c.delegateCtx, path, body(target, adminRole))
		if !forbidden(w) || c.assignments(target) != 0 || c.audits() != audits {
			t.Errorf("%s: delegate assigns org_admin in a team: %d, %d assignments, %d new audit entries; want 403 and nothing; body %.200s",
				path, w.Code, c.assignments(target), c.audits()-audits, w.Body.String())
		}
		if w = auditPost(mux, c.orgAdminCtx, path, body(target, adminRole)); w.Code != http.StatusCreated || c.assignments(target) != 1 {
			t.Errorf("%s: org-wide org_admin assigns org_admin in a team: %d %.200s, want 201", path, w.Code, w.Body.String())
		}
		var a UserRoleAssignment
		if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil || a.ScopeType != "team" || a.ScopeID != team {
			t.Errorf("%s: assignment %+v (%v), want scope_type team", path, a, err)
		}
	}
	for name, role := range map[string]string{"client role": techRole, "site role": siteRole} {
		target := c.AppUser(t, c.OrgA, "team-mismatch-"+name[:4])
		w := auditPost(mux, c.orgAdminCtx, "/api/v1/role-assignments", body(target, role))
		var problem struct {
			Field string `json:"field"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusUnprocessableEntity || problem.Field != "scope_type" || c.assignments(target) != 0 {
			t.Errorf("%s in a team: %d field %q, %d assignments; want 422 scope_type and none; body %.200s",
				name, w.Code, problem.Field, c.assignments(target), w.Body.String())
		}
	}
}
