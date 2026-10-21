package identity

import (
	"slices"
	"testing"
)

// TestGrantCeiling covers WP-354 (RBA-03, RBA-08): a principal covers a
// target grant set only if it holds every permission in a scope containing
// the target's scope of that permission (org-wide ⊇ client ⊇ site, per
// permission through PermissionScopes); a principal without a resolved scope
// is org-wide, restricted only by its client scope. A site grant names the
// client of its site (WP-390); one without its client needs all clients. A
// team grant restricts only the teams (WP-391), so it needs all clients and
// sites and its team; a team-restricted principal covers no grant of all
// teams.
func TestGrantCeiling(t *testing.T) {
	const c1, c2, s1, s2, t1, t2 = "c-1", "c-2", "s-1", "s-2", "t-1", "t-2"
	resolved := func(grants ...Grant) *Principal {
		access := ResolveAccess(grants)
		scope := access.Scope
		return &Principal{Type: PrincipalTypeUser, Permissions: access.Permissions, Scope: &scope,
			PermissionScopes: access.PermissionScopes, ClientScope: scope.LegacyClientScope()}
	}
	perms := func(p ...Permission) []Permission { return p }
	clientGrant := func(client string, p ...Permission) Grant { return Grant{Permissions: p, Clients: []string{client}} }
	// siteGrant is a site grant as permission.AccessGrants resolves it, with
	// the client of the site; unmappedSite lacks the client.
	siteGrant := func(client, site string, p ...Permission) Grant {
		return Grant{Permissions: p, Clients: []string{client}, Sites: []string{site}}
	}
	unmappedSite := func(site string, p ...Permission) Grant { return Grant{Permissions: p, Sites: []string{site}} }
	teamGrant := func(team string, p ...Permission) Grant { return Grant{Permissions: p, Teams: []string{team}} }

	orgAdmin := resolved(OrgWideGrant(perms(PermCIRead, PermCIDelete, PermUserManage, PermRoleManage)))
	client1 := resolved(clientGrant(c1, PermCIRead, PermAPIKeyManage))
	site1 := resolved(siteGrant(c1, s1, PermCIRead))
	team1 := resolved(teamGrant(t1, PermCIRead))
	// ci:read org-wide, ci:write only in client 1 (narrower PermissionScopes).
	mixed := resolved(OrgWideGrant(perms(PermCIRead)), clientGrant(c1, PermCIWrite))

	for _, tc := range []struct {
		name   string
		p      *Principal
		target []Grant
		want   []Permission
	}{
		{"org-wide covers org-wide", orgAdmin, []Grant{OrgWideGrant(perms(PermUserManage, PermRoleManage))}, nil},
		{"org-wide covers a client", orgAdmin, []Grant{clientGrant(c1, PermCIRead)}, nil},
		{"org-wide covers a site", orgAdmin, []Grant{siteGrant(c1, s1, PermCIDelete)}, nil},
		{"org-wide covers a site without its client", orgAdmin, []Grant{unmappedSite(s1, PermCIDelete)}, nil},
		{"missing permission", orgAdmin, []Grant{OrgWideGrant(perms(PermCIRead, PermWebhookManage))}, perms(PermWebhookManage)},
		{"client does not cover org-wide", client1, []Grant{OrgWideGrant(perms(PermCIRead))}, perms(PermCIRead)},
		{"client covers the same client", client1, []Grant{clientGrant(c1, PermCIRead)}, nil},
		{"client does not cover another client", client1, []Grant{clientGrant(c2, PermCIRead)}, perms(PermCIRead)},
		{"client does not cover two clients", client1, []Grant{clientGrant(c1, PermCIRead), clientGrant(c2, PermCIRead)}, perms(PermCIRead)},
		{"client covers a site of that client", client1, []Grant{siteGrant(c1, s1, PermCIRead)}, nil},
		{"client does not cover a site of another client", client1, []Grant{siteGrant(c2, s2, PermCIRead)}, perms(PermCIRead)},
		{"client does not cover a site without its client", client1, []Grant{unmappedSite(s1, PermCIRead)}, perms(PermCIRead)},
		{"site covers the same site", site1, []Grant{siteGrant(c1, s1, PermCIRead)}, nil},
		{"site does not cover another site of its client", site1, []Grant{siteGrant(c1, s2, PermCIRead)}, perms(PermCIRead)},
		{"site does not cover the same site without its client", site1, []Grant{unmappedSite(s1, PermCIRead)}, perms(PermCIRead)},
		{"site does not cover its client", site1, []Grant{clientGrant(c1, PermCIRead)}, perms(PermCIRead)},
		{"org-wide covers a team", orgAdmin, []Grant{teamGrant(t1, PermCIDelete)}, nil},
		{"client does not cover a team", client1, []Grant{teamGrant(t1, PermCIRead)}, perms(PermCIRead)},
		{"team covers the same team", team1, []Grant{teamGrant(t1, PermCIRead)}, nil},
		{"team does not cover another team", team1, []Grant{teamGrant(t2, PermCIRead)}, perms(PermCIRead)},
		{"team does not cover a client of all teams", team1, []Grant{clientGrant(c1, PermCIRead)}, perms(PermCIRead)},
		{"team does not cover org-wide", team1, []Grant{OrgWideGrant(perms(PermCIRead))}, perms(PermCIRead)},
		{"per permission: org-wide ci:read covers org-wide", mixed, []Grant{OrgWideGrant(perms(PermCIRead))}, nil},
		{"per permission: client ci:write does not cover org-wide", mixed, []Grant{OrgWideGrant(perms(PermCIRead, PermCIWrite))}, perms(PermCIWrite)},
		{"per permission: client ci:write covers client", mixed, []Grant{clientGrant(c1, PermCIWrite)}, nil},
		{"empty target", client1, nil, nil},
		{"nil principal", nil, []Grant{clientGrant(c1, PermCIRead)}, perms(PermCIRead)},
		{"scope nil without client scope is org-wide",
			&Principal{Permissions: perms(PermCIRead)}, []Grant{OrgWideGrant(perms(PermCIRead))}, nil},
		{"scope nil restricted by client scope covers that client",
			&Principal{Permissions: perms(PermCIRead), ClientScope: c1 + "," + c2}, []Grant{clientGrant(c2, PermCIRead)}, nil},
		{"scope nil restricted by client scope does not cover org-wide",
			&Principal{Permissions: perms(PermCIRead), ClientScope: c1}, []Grant{OrgWideGrant(perms(PermCIRead))}, perms(PermCIRead)},
		{"scope nil restricted to no client covers no client",
			&Principal{Permissions: perms(PermCIRead), ClientScope: NoScopeID}, []Grant{clientGrant(c1, PermCIRead)}, perms(PermCIRead)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := GrantCeiling(tc.p, tc.target); !slices.Equal(got, tc.want) {
				t.Errorf("GrantCeiling = %v, want %v", got, tc.want)
			}
		})
	}
}
