package permission

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Unit tests of the field permission model (RBA-07, E-43, WP-264) on the
// memory repository: resolution stages, union over roles, caps, sensitivity,
// actions, the scope of each assignment, Restricted, the cache and
// ParseFieldKey.

const (
	tOrg        = "00000000-0000-4000-8000-0000000000a1"
	tUser       = "00000000-0000-4000-8000-0000000000b1"
	tRoleA      = "00000000-0000-4000-8000-0000000000c1"
	tRoleB      = "00000000-0000-4000-8000-0000000000c2"
	tCustom     = "00000000-0000-4000-8000-0000000000c3"
	tParentType = "00000000-0000-4000-8000-0000000000d1"
	tChildType  = "00000000-0000-4000-8000-0000000000d2"
	tOtherType  = "00000000-0000-4000-8000-0000000000d3"
	tObject     = "00000000-0000-4000-8000-0000000000e1"
	tTeam       = "00000000-0000-4000-8000-0000000000f1"
	tOtherTeam  = "00000000-0000-4000-8000-0000000000f2"
	tClient1    = "00000000-0000-4000-8000-000000000101"
	tClient2    = "00000000-0000-4000-8000-000000000102"
	tSA         = "00000000-0000-4000-8000-000000000201"
)

// fakeAccess is an identity.AccessResolver and
// identity.ServiceAccountAccessResolver that counts its calls.
type fakeAccess struct {
	users    map[string][]identity.Grant
	roles    map[string]identity.Grant
	accounts map[string][]identity.Grant
	calls    int
	asked    []string
}

func (f *fakeAccess) AccessGrants(_ context.Context, _, userID string) ([]identity.Grant, error) {
	f.calls++
	return f.users[userID], nil
}

func (f *fakeAccess) RoleGrants(_ context.Context, _ string, names []string) ([]identity.Grant, error) {
	f.calls++
	f.asked = append(f.asked, names...)
	var out []identity.Grant
	for _, n := range names {
		if g, ok := f.roles[n]; ok {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeAccess) ServiceAccountGrants(_ context.Context, _, id string) ([]identity.Grant, error) {
	f.calls++
	return f.accounts[id], nil
}

func ptr(s string) *string { return &s }

func perms(keys ...string) []identity.Permission {
	out := make([]identity.Permission, len(keys))
	for i, k := range keys {
		out[i] = identity.Permission(k)
	}
	return out
}

func roleGrant(roleID string, keys ...string) identity.Grant {
	return identity.Grant{RoleID: roleID, Permissions: perms(keys...)}
}

// fixture is a memory repository with the type chain child -> parent.
func fixture(t *testing.T) (*MemoryFieldGrants, context.Context) {
	t.Helper()
	repo := NewMemoryFieldGrants()
	repo.SetTypeParent(tOrg, tChildType, tParentType)
	return repo, context.Background()
}

func addField(t *testing.T, repo *MemoryFieldGrants, g FieldGrant) {
	t.Helper()
	if _, err := repo.CreateFieldGrant(context.Background(), tOrg, &g); err != nil {
		t.Fatalf("create field grant: %v", err)
	}
}

func addObject(t *testing.T, repo *MemoryFieldGrants, g ObjectGrant) {
	t.Helper()
	if _, err := repo.CreateObjectGrant(context.Background(), tOrg, &g); err != nil {
		t.Fatalf("create object grant: %v", err)
	}
}

func addSensitive(t *testing.T, repo *MemoryFieldGrants, s SensitiveField) {
	t.Helper()
	if _, err := repo.CreateSensitiveField(context.Background(), tOrg, &s); err != nil {
		t.Fatalf("create sensitive field: %v", err)
	}
}

func policyFor(t *testing.T, repo *MemoryFieldGrants, grants []identity.Grant, teams []string, caps ...string) *FieldPolicy {
	t.Helper()
	access := &fakeAccess{users: map[string][]identity.Grant{tUser: grants}}
	for _, team := range teams {
		repo.AddTeamMember(tOrg, team, tUser)
	}
	var all []identity.Permission
	if caps != nil {
		all = perms(caps...)
	} else {
		for i := range grants {
			all = append(all, grants[i].Permissions...)
		}
	}
	p, err := NewFieldResolver(access, access, repo, nil).Resolve(context.Background(),
		identity.Principal{Subject: tUser, OrganizationID: tOrg, Type: identity.PrincipalTypeUser, Permissions: all})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return p
}

const hostname = "core.hostname"

var obj = ObjectRef{ID: tObject}

// TestFieldResolutionStages covers the order of E-43 for one role: object
// grant > field grant with type (nearest type of the chain) > field grant
// without type > type grant > kind grant > object right; a tie within a
// stage resolves to none.
func TestFieldResolutionStages(t *testing.T) {
	role := ptr(tRoleA)
	cases := []struct {
		name   string
		fields []FieldGrant
		object []ObjectGrant
		want   Level
	}{
		{name: "object right without grants", want: LevelWrite,
			fields: []FieldGrant{{RoleID: ptr(tRoleB), ObjectKind: KindCI, Level: LevelNone}}},
		{name: "kind grant", want: LevelRead,
			fields: []FieldGrant{{RoleID: role, ObjectKind: KindCI, Level: LevelRead}}},
		{name: "type grant beats kind grant", want: LevelNone,
			fields: []FieldGrant{
				{RoleID: role, ObjectKind: KindCI, Level: LevelWrite},
				{RoleID: role, ObjectKind: KindCI, CITypeID: ptr(tParentType), Level: LevelNone}}},
		{name: "nearest type grant wins", want: LevelWrite,
			fields: []FieldGrant{
				{RoleID: role, ObjectKind: KindCI, CITypeID: ptr(tParentType), Level: LevelNone},
				{RoleID: role, ObjectKind: KindCI, CITypeID: ptr(tChildType), Level: LevelWrite}}},
		{name: "field grant without type beats type grant", want: LevelRead,
			fields: []FieldGrant{
				{RoleID: role, ObjectKind: KindCI, CITypeID: ptr(tChildType), Level: LevelNone},
				{RoleID: role, ObjectKind: KindCI, FieldKey: ptr(hostname), Level: LevelRead}}},
		{name: "field grant with type beats field grant without", want: LevelNone,
			fields: []FieldGrant{
				{RoleID: role, ObjectKind: KindCI, FieldKey: ptr(hostname), Level: LevelWrite},
				{RoleID: role, ObjectKind: KindCI, CITypeID: ptr(tParentType), FieldKey: ptr(hostname), Level: LevelNone}}},
		{name: "nearest type of the chain wins for fields", want: LevelRead,
			fields: []FieldGrant{
				{RoleID: role, ObjectKind: KindCI, CITypeID: ptr(tParentType), FieldKey: ptr(hostname), Level: LevelNone},
				{RoleID: role, ObjectKind: KindCI, CITypeID: ptr(tChildType), FieldKey: ptr(hostname), Level: LevelRead}}},
		{name: "grant of another type does not apply", want: LevelWrite,
			fields: []FieldGrant{{RoleID: role, ObjectKind: KindCI, CITypeID: ptr(tOtherType), FieldKey: ptr(hostname), Level: LevelNone}}},
		{name: "grant of another field does not apply", want: LevelWrite,
			fields: []FieldGrant{{RoleID: role, ObjectKind: KindCI, FieldKey: ptr("core.fqdn"), Level: LevelNone}}},
		{name: "object grant of the role beats field grants", want: LevelWrite,
			fields: []FieldGrant{{RoleID: role, ObjectKind: KindCI, CITypeID: ptr(tChildType), FieldKey: ptr(hostname), Level: LevelNone}},
			object: []ObjectGrant{{GranteeRoleID: role, ObjectKind: KindCI, ObjectID: tObject, Level: LevelWrite}}},
		{name: "object grant of a team of the principal", want: LevelRead,
			fields: []FieldGrant{{RoleID: role, ObjectKind: KindCI, FieldKey: ptr(hostname), Level: LevelWrite}},
			object: []ObjectGrant{{GranteeTeamID: ptr(tTeam), ObjectKind: KindCI, ObjectID: tObject, Level: LevelRead}}},
		{name: "object grant of a foreign team does not apply", want: LevelWrite,
			object: []ObjectGrant{{GranteeTeamID: ptr(tOtherTeam), ObjectKind: KindCI, ObjectID: tObject, Level: LevelNone}}},
		{name: "object grant of another object does not apply", want: LevelWrite,
			object: []ObjectGrant{{GranteeRoleID: role, ObjectKind: KindCI, ObjectID: tParentType, Level: LevelNone}}},
		{name: "tie within the object stage resolves to none", want: LevelNone,
			object: []ObjectGrant{
				{GranteeRoleID: role, ObjectKind: KindCI, ObjectID: tObject, Level: LevelWrite},
				{GranteeTeamID: ptr(tTeam), ObjectKind: KindCI, ObjectID: tObject, Level: LevelNone}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, _ := fixture(t)
			for _, g := range tc.fields {
				addField(t, repo, g)
			}
			for _, g := range tc.object {
				addObject(t, repo, g)
			}
			p := policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:read", "ci:write")}, []string{tTeam})
			if got := p.Level(KindCI, tChildType, hostname, obj); got != tc.want {
				t.Errorf("Level = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFieldResolutionUnionAndCaps: the highest level of all roles counts
// (RBA-03), so a none grant closes a field only when no other role reads
// it; each role is capped by its object right and every role by
// Principal.Permissions (API key intersection, AUT-04).
func TestFieldResolutionUnionAndCaps(t *testing.T) {
	t.Run("none closes only without another reading role", func(t *testing.T) {
		repo, _ := fixture(t)
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr(hostname), Level: LevelNone})
		one := policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:read")}, nil)
		if got := one.Level(KindCI, tChildType, hostname, obj); got != LevelNone {
			t.Errorf("one role: %q, want none", got)
		}
		two := policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:read"), roleGrant(tRoleB, "ci:read")}, nil)
		if got := two.Level(KindCI, tChildType, hostname, obj); got != LevelRead {
			t.Errorf("two roles: %q, want read", got)
		}
	})
	t.Run("custom role", func(t *testing.T) {
		repo, _ := fixture(t)
		addField(t, repo, FieldGrant{CustomRoleID: ptr(tCustom), ObjectKind: KindCI, FieldKey: ptr(hostname), Level: LevelNone})
		p := policyFor(t, repo, []identity.Grant{{CustomRoleID: tCustom, Permissions: perms("ci:read")}}, nil)
		if got := p.Level(KindCI, "", hostname, obj); got != LevelNone {
			t.Errorf("custom role: %q, want none", got)
		}
	})
	t.Run("object right caps the grant", func(t *testing.T) {
		repo, _ := fixture(t)
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr(hostname), Level: LevelWrite})
		p := policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:read")}, nil)
		if got := p.Level(KindCI, "", hostname, obj); got != LevelRead {
			t.Errorf("read role with write grant: %q, want read", got)
		}
		if got := p.Level(KindAsset, "", "core.serial_number", obj); got != LevelNone {
			t.Errorf("kind without right: %q, want none", got)
		}
	})
	t.Run("principal permissions cap the roles", func(t *testing.T) {
		repo, _ := fixture(t)
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr(hostname), Level: LevelWrite})
		p := policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:read", "ci:write")}, nil, "ci:read")
		if got := p.Level(KindCI, "", hostname, obj); got != LevelRead {
			t.Errorf("API key with ci:read: %q, want read", got)
		}
	})
	t.Run("principal without roles reads nothing", func(t *testing.T) {
		repo, _ := fixture(t)
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, Level: LevelRead})
		p := policyFor(t, repo, nil, nil, "ci:read", "ci:write")
		if got := p.Level(KindCI, "", hostname, obj); got != LevelNone {
			t.Errorf("no roles: %q, want none", got)
		}
	})
}

// TestFieldSensitivity: a sensitive field (at the type, an ancestor or
// without type) is none unless a role has a field grant with exactly its
// key; object and type grants do not lift it.
func TestFieldSensitivity(t *testing.T) {
	serial := "core.serial_number"
	full := []identity.Grant{roleGrant(tRoleA, "ci:read", "ci:write")}
	t.Run("marked at the parent type", func(t *testing.T) {
		repo, _ := fixture(t)
		addSensitive(t, repo, SensitiveField{ObjectKind: KindCI, CITypeID: ptr(tParentType), FieldKey: serial})
		addObject(t, repo, ObjectGrant{GranteeRoleID: ptr(tRoleA), ObjectKind: KindCI, ObjectID: tObject, Level: LevelWrite})
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, CITypeID: ptr(tChildType), Level: LevelWrite})
		p := policyFor(t, repo, full, nil)
		if got := p.Level(KindCI, tChildType, serial, obj); got != LevelNone {
			t.Errorf("sensitive at the parent: %q, want none", got)
		}
		if got := p.Level(KindCI, tOtherType, serial, obj); got != LevelWrite {
			t.Errorf("other type: %q, want write (object grant)", got)
		}
		if got := p.Level(KindCI, tChildType, hostname, obj); got != LevelWrite {
			t.Errorf("other field: %q, want write", got)
		}
	})
	t.Run("marked without type, opened by a field grant", func(t *testing.T) {
		repo, _ := fixture(t)
		addSensitive(t, repo, SensitiveField{ObjectKind: KindCI, FieldKey: serial})
		if got := policyFor(t, repo, full, nil).Level(KindCI, tChildType, serial, obj); got != LevelNone {
			t.Errorf("without grant: %q, want none", got)
		}
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr(serial), Level: LevelRead})
		p := policyFor(t, repo, full, nil)
		if got := p.Level(KindCI, tChildType, serial, obj); got != LevelRead {
			t.Errorf("with field grant: %q, want read", got)
		}
		other := policyFor(t, repo, []identity.Grant{roleGrant(tRoleB, "ci:read", "ci:write")}, nil)
		if got := other.Level(KindCI, tChildType, serial, obj); got != LevelNone {
			t.Errorf("role without the field grant: %q, want none", got)
		}
	})
	t.Run("asset kind", func(t *testing.T) {
		repo, _ := fixture(t)
		addSensitive(t, repo, SensitiveField{ObjectKind: KindAsset, FieldKey: "core.purchase_cost"})
		p := policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "asset:read", "ci:read")}, nil)
		if got := p.Level(KindAsset, "", "core.purchase_cost", obj); got != LevelNone {
			t.Errorf("asset purchase_cost: %q, want none", got)
		}
		if got := p.Level(KindCI, "", "core.purchase_cost", obj); got != LevelRead {
			t.Errorf("mark of asset applies to ci: %q", got)
		}
	})
}

// TestFieldActions: an action needs its permission; an action grant none
// closes it, write keeps it; an object grant none closes the actions of the
// object.
func TestFieldActions(t *testing.T) {
	const action = "lifecycle:transition"
	holder := []identity.Grant{roleGrant(tRoleA, "ci:read", "ci:write", action)}
	t.Run("without grant", func(t *testing.T) {
		repo, _ := fixture(t)
		addSensitive(t, repo, SensitiveField{ObjectKind: KindCI, FieldKey: hostname})
		if !policyFor(t, repo, holder, nil).ActionAllowed(KindCI, tChildType, action, obj) {
			t.Error("holder without grant: refused")
		}
		if policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:write")}, nil).ActionAllowed(KindCI, tChildType, action, obj) {
			t.Error("role without the permission: allowed")
		}
	})
	t.Run("none and write", func(t *testing.T) {
		repo, _ := fixture(t)
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, CITypeID: ptr(tParentType), Action: ptr(action), Level: LevelNone})
		p := policyFor(t, repo, holder, nil)
		if p.ActionAllowed(KindCI, tChildType, action, obj) {
			t.Error("action grant none at the parent type: allowed")
		}
		if !p.ActionAllowed(KindCI, tOtherType, action, obj) {
			t.Error("other type: refused")
		}
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, CITypeID: ptr(tChildType), Action: ptr(action), Level: LevelWrite})
		if !policyFor(t, repo, holder, nil).ActionAllowed(KindCI, tChildType, action, obj) {
			t.Error("nearer action grant write: refused")
		}
	})
	t.Run("object grant none", func(t *testing.T) {
		repo, _ := fixture(t)
		addObject(t, repo, ObjectGrant{GranteeRoleID: ptr(tRoleA), ObjectKind: KindCI, ObjectID: tObject, Level: LevelNone})
		p := policyFor(t, repo, holder, nil)
		if p.ActionAllowed(KindCI, tChildType, action, obj) {
			t.Error("object grant none: allowed")
		}
		if !p.ActionAllowed(KindCI, tChildType, action, ObjectRef{ID: tClient1}) {
			t.Error("other object: refused")
		}
		if got := p.Level(KindCI, tChildType, hostname, obj); got != LevelNone {
			t.Errorf("fields of the object: %q, want none", got)
		}
	})
	t.Run("read level refused for actions", func(t *testing.T) {
		repo, _ := fixture(t)
		g := FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, Action: ptr(action), Level: LevelRead}
		if _, err := repo.CreateFieldGrant(context.Background(), tOrg, &g); err == nil {
			t.Error("action grant read accepted")
		}
	})
}

// TestFieldScopePerAssignment: a grant acts only on objects in the scope of
// its assignment; a team assignment does not restrict objects without team.
func TestFieldScopePerAssignment(t *testing.T) {
	t.Run("client", func(t *testing.T) {
		repo, _ := fixture(t)
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr(hostname), Level: LevelNone})
		clientRole := identity.Grant{RoleID: tRoleA, Clients: []string{tClient1}, Permissions: perms("ci:read", "ci:write")}
		orgRole := roleGrant(tRoleB, "ci:read")
		p := policyFor(t, repo, []identity.Grant{clientRole, orgRole}, nil)
		if got := p.Level(KindCI, "", hostname, ObjectRef{ID: tObject, ClientID: tClient1}); got != LevelRead {
			t.Errorf("client 1: %q, want read (none of the client role, read of the org role)", got)
		}
		if got := p.Level(KindCI, "", "core.fqdn", ObjectRef{ID: tObject, ClientID: tClient1}); got != LevelWrite {
			t.Errorf("client 1, other field: %q, want write", got)
		}
		if got := p.Level(KindCI, "", "core.fqdn", ObjectRef{ID: tObject, ClientID: tClient2}); got != LevelRead {
			t.Errorf("client 2: %q, want read (the client role does not reach it)", got)
		}
		only := policyFor(t, repo, []identity.Grant{clientRole}, nil)
		if got := only.Level(KindCI, "", "core.fqdn", ObjectRef{ID: tObject, ClientID: tClient2}); got != LevelNone {
			t.Errorf("client role only, client 2: %q, want none", got)
		}
		if got := only.Level(KindCI, "", "core.fqdn", ObjectRef{ID: tObject}); got != LevelWrite {
			t.Errorf("client role only, shared object: %q, want write", got)
		}
	})
	t.Run("team", func(t *testing.T) {
		repo, _ := fixture(t)
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindTicket, FieldKey: ptr("core.description"), Level: LevelRead})
		teamRole := identity.Grant{RoleID: tRoleA, Teams: []string{tTeam}, Permissions: perms("ticket:read", "ticket:write")}
		p := policyFor(t, repo, []identity.Grant{teamRole}, nil)
		if got := p.Level(KindTicket, "", "core.description", ObjectRef{ID: tObject, TeamID: tTeam}); got != LevelRead {
			t.Errorf("own team: %q, want read", got)
		}
		if got := p.Level(KindTicket, "", "core.description", ObjectRef{ID: tObject, TeamID: tOtherTeam}); got != LevelNone {
			t.Errorf("other team: %q, want none", got)
		}
		if got := p.Level(KindTicket, "", "core.description", ObjectRef{ID: tObject}); got != LevelRead {
			t.Errorf("ticket without team: %q, want read", got)
		}
		if p.ActionAllowed(KindTicket, "", "ticket:write", ObjectRef{ID: tObject, TeamID: tOtherTeam}) {
			t.Error("action on a ticket of another team: allowed")
		}
	})
}

// TestFieldRelationships: a grant of a relationship type applies to the
// relationships of that type (rel_type) and, at kind ci, to the relations
// of that type at CIs of the type.
func TestFieldRelationships(t *testing.T) {
	repo, _ := fixture(t)
	const relTypeID = "00000000-0000-4000-8000-000000000301"
	repo.SetRelationshipType(relTypeID, "runs_on")
	addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindRelationship, RelationshipTypeID: ptr(relTypeID), Level: LevelRead})
	addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, CITypeID: ptr(tParentType), RelationshipTypeID: ptr(relTypeID), Level: LevelNone})
	p := policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:read", "ci:write", "relationship:read", "relationship:write")}, nil)
	if got := p.Level(KindRelationship, "runs_on", "core.notes", obj); got != LevelRead {
		t.Errorf("relationship of the type: %q, want read", got)
	}
	if got := p.Level(KindRelationship, "connects_to", "core.notes", obj); got != LevelWrite {
		t.Errorf("relationship of another type: %q, want write", got)
	}
	if got := p.RelationLevel(tChildType, "runs_on", obj); got != LevelNone {
		t.Errorf("relations at a CI of the subtype: %q, want none", got)
	}
	if got := p.RelationLevel(tOtherType, "runs_on", obj); got != LevelWrite {
		t.Errorf("relations at a CI of another type: %q, want write", got)
	}
}

// TestFieldRestrictedAndEmptySnapshot: without grants and sensitivity the
// policy is unrestricted and Resolve reads no roles; Restricted names the
// kinds with a restriction.
func TestFieldRestrictedAndEmptySnapshot(t *testing.T) {
	repo, ctx := fixture(t)
	access := &fakeAccess{users: map[string][]identity.Grant{tUser: {roleGrant(tRoleA, "ci:read")}}}
	resolver := NewFieldResolver(access, access, repo, nil)
	principal := identity.Principal{Subject: tUser, OrganizationID: tOrg, Permissions: perms("ci:read", "lifecycle:transition")}
	p, err := resolver.Resolve(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if access.calls != 0 {
		t.Errorf("empty snapshot: %d role lookups, want none", access.calls)
	}
	if p.Restricted(KindCI) {
		t.Error("Restricted without grants")
	}
	if got := p.Level(KindCI, tChildType, hostname, obj); got != LevelRead {
		t.Errorf("unrestricted level: %q, want read (object right)", got)
	}
	if !p.ActionAllowed(KindCI, "", "lifecycle:transition", obj) || p.ActionAllowed(KindCI, "", "asset:move", obj) {
		t.Error("unrestricted actions do not follow the permissions")
	}

	addSensitive(t, repo, SensitiveField{ObjectKind: KindCI, FieldKey: hostname})
	addField(t, repo, FieldGrant{RoleID: ptr(tRoleB), ObjectKind: KindAsset, Level: LevelNone})
	resolver.Invalidate(ctx, tOrg)
	if p, err = resolver.Resolve(ctx, principal); err != nil {
		t.Fatal(err)
	}
	if access.calls == 0 {
		t.Error("restricted snapshot without role lookup")
	}
	if !p.Restricted(KindCI) {
		t.Error("ci with a sensitive field is not restricted")
	}
	if p.Restricted(KindAsset) {
		t.Error("asset grant of a foreign role restricts the principal")
	}

	var nilPolicy *FieldPolicy
	if nilPolicy.Level(KindCI, "", hostname, obj) != LevelNone || nilPolicy.ActionAllowed(KindCI, "", "ci:read", obj) || !nilPolicy.Restricted(KindCI) {
		t.Error("nil policy is not fail-closed")
	}
}

// TestFieldResolvePrincipals: a session holds its assignments and the
// standard roles of its IdP groups and its teams; a service account key
// the account's roles; a key without account its owner's roles; a subject
// that is no id holds nothing.
func TestFieldResolvePrincipals(t *testing.T) {
	repo, ctx := fixture(t)
	addField(t, repo, FieldGrant{RoleID: ptr(tRoleB), ObjectKind: KindCI, FieldKey: ptr(hostname), Level: LevelRead})
	addObject(t, repo, ObjectGrant{GranteeTeamID: ptr(tTeam), ObjectKind: KindCI, ObjectID: tObject, Level: LevelNone})
	repo.AddTeamMember(tOrg, tTeam, tUser)
	access := &fakeAccess{
		users:    map[string][]identity.Grant{tUser: {roleGrant(tRoleA, "ci:read", "ci:write")}},
		roles:    map[string]identity.Grant{"engineer": roleGrant(tRoleB, "ci:read", "ci:write")},
		accounts: map[string][]identity.Grant{tSA: {roleGrant(tRoleB, "ci:read", "ci:write")}},
	}
	resolver := NewFieldResolver(access, access, repo, nil)
	all := perms("ci:read", "ci:write")

	session, err := resolver.Resolve(ctx, identity.Principal{Subject: tUser, OrganizationID: tOrg, Type: identity.PrincipalTypeUser,
		Permissions: all, Groups: []string{"reticora-engineer"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(access.asked, []string{"engineer"}) {
		t.Errorf("RoleGrants asked for %v, want [engineer]", access.asked)
	}
	if got := session.Level(KindCI, "", "core.fqdn", ObjectRef{ID: tClient1}); got != LevelWrite {
		t.Errorf("session: %q, want write", got)
	}
	if got := session.Level(KindCI, "", hostname, obj); got != LevelNone {
		t.Errorf("session, object closed for its team: %q, want none", got)
	}

	account, err := resolver.Resolve(ctx, identity.Principal{Subject: tSA, OrganizationID: tOrg, Type: identity.PrincipalTypeAPIKey,
		ServiceAccountID: tSA, Permissions: all})
	if err != nil {
		t.Fatal(err)
	}
	if got := account.Level(KindCI, "", hostname, obj); got != LevelRead {
		t.Errorf("service account: %q, want read (role B, no team)", got)
	}

	owner, err := resolver.Resolve(ctx, identity.Principal{Subject: tUser, OrganizationID: tOrg, Type: identity.PrincipalTypeAPIKey,
		Permissions: perms("ci:read"), Groups: []string{"org_admin"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := owner.Level(KindCI, "", "core.fqdn", ObjectRef{ID: tClient1}); got != LevelRead {
		t.Errorf("owner key capped to ci:read: %q, want read", got)
	}

	legacy, err := resolver.Resolve(ctx, identity.Principal{Subject: "rk_live_prefix", OrganizationID: tOrg, Type: identity.PrincipalTypeAPIKey, Permissions: all})
	if err != nil {
		t.Fatal(err)
	}
	if got := legacy.Level(KindCI, "", "core.fqdn", ObjectRef{}); got != LevelNone {
		t.Errorf("key without owner: %q, want none", got)
	}

	jobs, err := resolver.PolicyForGrants(ctx, tOrg, []identity.Grant{roleGrant(tRoleB, "ci:read", "ci:write")}, []string{tTeam})
	if err != nil {
		t.Fatal(err)
	}
	if got := jobs.Level(KindCI, "", hostname, ObjectRef{ID: tClient1}); got != LevelRead {
		t.Errorf("PolicyForGrants: %q, want read", got)
	}
	if got := jobs.Level(KindCI, "", hostname, obj); got != LevelNone {
		t.Errorf("PolicyForGrants with team: %q, want none", got)
	}
}

// TestFieldResolverCache: the snapshot is read once per TTL from the
// repository; Invalidate drops it.
func TestFieldResolverCache(t *testing.T) {
	repo, ctx := fixture(t)
	counting := &countingRepo{MemoryFieldGrants: repo}
	first, err := repo.CreateFieldGrant(ctx, tOrg, &FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr(hostname), Level: LevelRead})
	if err != nil {
		t.Fatal(err)
	}
	access := &fakeAccess{users: map[string][]identity.Grant{tUser: {roleGrant(tRoleA, "ci:read", "ci:write")}}}
	store := cache.NewMemoryStore()
	resolver := NewFieldResolver(access, access, counting, store)
	principal := identity.Principal{Subject: tUser, OrganizationID: tOrg, Permissions: perms("ci:read", "ci:write")}
	level := func() Level {
		t.Helper()
		p, resolveErr := resolver.Resolve(ctx, principal)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		return p.Level(KindCI, "", hostname, obj)
	}
	if got := level(); got != LevelRead {
		t.Fatalf("first: %q", got)
	}
	addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, CITypeID: ptr(tChildType), FieldKey: ptr(hostname), Level: LevelNone})
	if err = repo.DeleteFieldGrant(ctx, tOrg, first.ID); err != nil {
		t.Fatal(err)
	}
	if got := level(); got != LevelRead || counting.snapshots != 1 {
		t.Errorf("second: %q after %d snapshot reads, want read from the cache", got, counting.snapshots)
	}
	resolver.Invalidate(ctx, tOrg)
	if got := level(); got != LevelWrite || counting.snapshots != 2 {
		t.Errorf("after Invalidate: %q after %d snapshot reads, want write", got, counting.snapshots)
	}
}

type countingRepo struct {
	*MemoryFieldGrants
	snapshots int
}

func (c *countingRepo) Snapshot(ctx context.Context, orgID string) (Snapshot, error) {
	c.snapshots++
	return c.MemoryFieldGrants.Snapshot(ctx, orgID)
}

// TestParseFieldKey covers the three forms and the refused keys.
func TestParseFieldKey(t *testing.T) {
	ok := []struct {
		kind ObjectKind
		key  string
	}{
		{KindCI, "core.serial_number"}, {KindCI, "core.management_ip"}, {KindCI, "attributes.rack_unit"},
		{KindCI, "_instance.owner_note"}, {KindAsset, "core.purchase_cost"}, {KindAsset, "attributes.cost-center"},
		{KindRelationship, "core.confidence"}, {KindRelationship, "attributes.bandwidth"}, {KindTicket, "core.description"},
		{KindContract, "core.value"}, {KindContract, "core.supplier_id"},
	}
	for _, c := range ok {
		if _, err := ParseFieldKey(c.kind, c.key); err != nil {
			t.Errorf("%s %s: %v", c.kind, c.key, err)
		}
	}
	refused := []struct {
		kind ObjectKind
		key  string
	}{
		{KindCI, "core.id"}, {KindCI, "core.name"}, {KindCI, "core.client_id"}, {KindCI, "core.site_id"},
		{KindCI, "core.version"}, {KindCI, "core.ci_type_id"}, {KindCI, "core.created_at"}, {KindTicket, "core.title"},
		{KindAsset, "_instance.x"}, {KindTicket, "attributes.x"}, {KindCI, "attributes."}, {KindCI, "attributes.a.b"},
		{KindCI, "hostname"}, {KindCI, "other.hostname"}, {KindContract, "core.number"}, {KindContract, "core.client_id"},
		{KindContract, "attributes.x"}, {"widget", "core.hostname"},
	}
	for _, c := range refused {
		if _, err := ParseFieldKey(c.kind, c.key); !errors.Is(err, ErrUnknownFieldKey) {
			t.Errorf("%s %s: %v, want ErrUnknownFieldKey", c.kind, c.key, err)
		}
	}
	for kind, fields := range coreFields {
		if !slices.IsSorted(fields) {
			t.Errorf("core fields of %s are not sorted", kind)
		}
	}
}

// TestMemoryFieldGrantsValidationAndPaging: the checks shared with the
// PostgreSQL repository and the keyset paging.
func TestMemoryFieldGrantsValidationAndPaging(t *testing.T) {
	repo, ctx := fixture(t)
	bad := []FieldGrant{
		{ObjectKind: KindCI, Level: LevelRead},
		{RoleID: ptr(tRoleA), CustomRoleID: ptr(tCustom), ObjectKind: KindCI, Level: LevelRead},
		{RoleID: ptr(tRoleA), ObjectKind: "widget", Level: LevelRead},
		{RoleID: ptr(tRoleA), ObjectKind: KindCI, Level: "admin"},
		{RoleID: ptr(tRoleA), ObjectKind: KindTicket, CITypeID: ptr(tChildType), Level: LevelRead},
		{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr(hostname), Action: ptr("ci:write"), Level: LevelNone},
		{RoleID: ptr(tRoleA), ObjectKind: KindAsset, RelationshipTypeID: ptr(tObject), Level: LevelNone},
		{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr("core.name"), Level: LevelNone},
	}
	for i := range bad {
		if _, err := repo.CreateFieldGrant(ctx, tOrg, &bad[i]); err == nil {
			t.Errorf("bad grant %d accepted", i)
		}
	}
	unknown := FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, Action: ptr("ci:fly"), Level: LevelNone}
	if _, err := repo.CreateFieldGrant(ctx, tOrg, &unknown); !errors.Is(err, ErrUnknownReference) {
		t.Errorf("unknown action: %v", err)
	}
	keys := []string{"core.fqdn", "core.hostname", "core.model", "core.os_name", "core.status"}
	for _, k := range keys {
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr(k), Level: LevelRead})
		time.Sleep(time.Millisecond)
	}
	dup := FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr("core.fqdn"), Level: LevelNone}
	if _, err := repo.CreateFieldGrant(ctx, tOrg, &dup); err == nil {
		t.Error("duplicate accepted")
	}
	var seen []string
	page := api.PaginationParams{Limit: 2}
	for range 4 {
		items, next, err := repo.ListFieldGrants(ctx, tOrg, &FieldGrantFilter{RoleID: tRoleA}, page)
		if err != nil {
			t.Fatal(err)
		}
		for i := range items {
			seen = append(seen, *items[i].FieldKey)
		}
		if next == "" {
			break
		}
		c, err := api.DecodeCursor(next)
		if err != nil {
			t.Fatal(err)
		}
		page = api.PaginationParams{Limit: 2, Cursor: &c}
	}
	if !slices.Equal(seen, keys) {
		t.Errorf("paged %v, want %v", seen, keys)
	}
	if items, _, _ := repo.ListFieldGrants(ctx, "00000000-0000-4000-8000-0000000000a2", &FieldGrantFilter{}, api.PaginationParams{}); len(items) != 0 {
		t.Errorf("other organization lists %d grants", len(items))
	}
}
