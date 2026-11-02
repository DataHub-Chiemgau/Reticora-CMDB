package identity

import (
	"context"
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

type grantsFunc func(orgID, id string) ([]Grant, error)

func (f grantsFunc) AccessGrants(_ context.Context, orgID, id string) ([]Grant, error) {
	return f(orgID, id)
}

func (f grantsFunc) ServiceAccountGrants(_ context.Context, orgID, id string) ([]Grant, error) {
	return f(orgID, id)
}

// TestBoundKeyCreatorCeiling covers the creator scope of a key bound to a
// service account (WP-668, F-397): per permission the key acts in the
// intersection of the account's current scope and the scope its creator held
// at binding, its tenant scope lies within the union of the creator's
// scopes, and permissions outside the snapshot are dropped. A key bound
// before migration 000185 follows its creator's current rights, and grants
// nothing without an owner resolver (fail-closed).
func TestBoundKeyCreatorCeiling(t *testing.T) {
	const c1, c2, s1 = "c-1", "c-2", "s-1"
	creator := func(grants ...Grant) *Principal {
		access := ResolveAccess(grants)
		return &Principal{Permissions: access.Permissions, Scope: &access.Scope, PermissionScopes: access.PermissionScopes}
	}
	all := ScopeSet{All: true}
	only := func(ids ...string) ScopeSet { return ScopeSet{IDs: ids} }

	// Creator: ci:read org-wide, ci:write in client 1; the account holds
	// both and location:read org-wide.
	ceiling := creatorScopeOf(creator(OrgWideGrant([]Permission{PermCIRead}),
		Grant{Permissions: []Permission{PermCIWrite}, Clients: []string{c1}}), []Permission{PermCIRead, PermCIWrite})
	account := ResolveAccess([]Grant{OrgWideGrant([]Permission{PermCIRead, PermCIWrite, PermLocationRead})})
	got := withinCeiling(&account, ceiling)
	if !slices.Equal(got.Permissions, []Permission{PermCIRead, PermCIWrite}) {
		t.Errorf("permissions %v, want ci:read and ci:write only", got.Permissions)
	}
	if !got.Scope.OrgWide() {
		t.Errorf("tenant scope %+v, want org-wide (ci:read)", got.Scope)
	}
	if w := got.PermissionScopes[PermCIWrite]; w.Clients.All || !slices.Equal(w.Clients.IDs, []string{c1}) || !w.Sites.All {
		t.Errorf("ci:write scope %+v, want client 1", w)
	}

	// Creator: ci:read at site 1 of client 1; the account reads client 2.
	ceiling = creatorScopeOf(creator(Grant{Permissions: []Permission{PermCIRead}, Clients: []string{c1}, Sites: []string{s1}}), []Permission{PermCIRead})
	account = ResolveAccess([]Grant{{Permissions: []Permission{PermCIRead}, Clients: []string{c2}}})
	got = withinCeiling(&account, ceiling)
	want := Scope{Clients: only(), Sites: only(s1), Teams: all}
	if !got.Scope.equal(&want) {
		t.Errorf("site-1 creator, client-2 account: scope %+v, want no client and site 1", got.Scope)
	}

	// Through Validate: a recorded scope, a legacy key and no owner resolver.
	sa := "sa-1"
	store := NewMemoryAPIKeyStore()
	generated, err := GenerateAPIKey(APIKeyEnvironmentLive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Create(context.Background(), &StoredAPIKey{OrganizationID: "org", Name: "k", KeyHash: generated.KeyHash,
		KeyPrefix: generated.KeyPrefix, Permissions: []Permission{PermCIRead}, CreatedBy: "creator", ServiceAccountID: &sa,
		CreatorScope: map[Permission]Scope{PermCIRead: {Clients: only(c1), Sites: all, Teams: all}}}); err != nil {
		t.Fatal(err)
	}
	orgWideAccount := grantsFunc(func(string, string) ([]Grant, error) { return []Grant{OrgWideGrant([]Permission{PermCIRead})}, nil })
	creatorClient2 := grantsFunc(func(string, string) ([]Grant, error) {
		return []Grant{{Permissions: []Permission{PermCIRead}, Clients: []string{c2}}}, nil
	})
	service := NewAPIKeyServiceWithStore(store).WithOwnerAccess(creatorClient2).WithServiceAccountAccess(orgWideAccount)
	clients := func() ScopeSet {
		t.Helper()
		info, vErr := service.Validate(context.Background(), generated.Plaintext)
		if vErr != nil {
			t.Fatalf("validate: %v", vErr)
		}
		if len(info.Scopes) == 0 {
			return ScopeSet{}
		}
		return info.Scope.Clients
	}
	if got := clients(); got.All || !slices.Equal(got.IDs, []string{c1}) {
		t.Errorf("recorded creator scope client 1, org-wide account: clients %+v, want client 1", got)
	}
	store.keys[0].CreatorScope = nil
	if got := clients(); got.All || !slices.Equal(got.IDs, []string{c2}) {
		t.Errorf("legacy key, creator now in client 2: clients %+v, want client 2", got)
	}
	service.owners = nil
	if info, vErr := service.Validate(context.Background(), generated.Plaintext); vErr != nil || len(info.Scopes) != 0 {
		t.Errorf("legacy key without owner resolver: %+v, %v; want no permissions", info, vErr)
	}
}
