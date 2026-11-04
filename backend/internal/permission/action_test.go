package permission

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Unit tests of the action grants (RBA-07, E-43, WP-728): CheckAction on
// type grants none and write, without grant with and without the role's
// permission, an object grant none for every action of the object;
// WithAction and its default ci:write; CheckActionOn without principal,
// with an unrestricted and a restricted policy; the policy of a job's
// creator narrowed to the scope snapshot.

// wantActionRefused checks the 403 error of a refused action.
func wantActionRefused(t *testing.T, what string, err error, action identity.Permission) {
	t.Helper()
	var ff *api.FieldForbiddenError
	if !errors.As(err, &ff) {
		t.Fatalf("%s: %v, want *api.FieldForbiddenError", what, err)
	}
	if ff.Action != string(action) || len(ff.Fields) != 0 {
		t.Fatalf("%s: action %q fields %v, want %q without fields", what, ff.Action, ff.Fields, action)
	}
}

func TestCheckAction(t *testing.T) {
	const del = identity.PermCIDelete
	holder := []identity.Grant{roleGrant(tRoleA, "ci:read", "ci:write", string(del))}

	t.Run("type grant none and write", func(t *testing.T) {
		repo, _ := fixture(t)
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, CITypeID: ptr(tParentType), Action: ptr(string(del)), Level: LevelNone})
		p := policyFor(t, repo, holder, nil)
		wantActionRefused(t, "none at the parent type", CheckAction(p, KindCI, tChildType, obj, del), del)
		if err := CheckAction(p, KindCI, tOtherType, obj, del); err != nil {
			t.Fatalf("other type: %v", err)
		}
		if err := CheckAction(p, KindCI, tChildType, obj, identity.PermCIWrite); err != nil {
			t.Fatalf("another action of the type: %v", err)
		}
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, CITypeID: ptr(tChildType), Action: ptr(string(del)), Level: LevelWrite})
		if err := CheckAction(policyFor(t, repo, holder, nil), KindCI, tChildType, obj, del); err != nil {
			t.Fatalf("write at the nearer type: %v", err)
		}
	})

	t.Run("without grant: the role's permission decides", func(t *testing.T) {
		repo, _ := fixture(t)
		addSensitive(t, repo, SensitiveField{ObjectKind: KindCI, FieldKey: hostname})
		if err := CheckAction(policyFor(t, repo, holder, nil), KindCI, tChildType, obj, del); err != nil {
			t.Fatalf("holder: %v", err)
		}
		writer := []identity.Grant{roleGrant(tRoleA, "ci:read", "ci:write")}
		wantActionRefused(t, "role without ci:delete", CheckAction(policyFor(t, repo, writer, nil), KindCI, tChildType, obj, del), del)
	})

	t.Run("object grant none closes every action", func(t *testing.T) {
		repo, _ := fixture(t)
		addObject(t, repo, ObjectGrant{GranteeRoleID: ptr(tRoleA), ObjectKind: KindCI, ObjectID: tObject, Level: LevelNone})
		p := policyFor(t, repo, holder, nil)
		for _, action := range []identity.Permission{del, identity.PermCIWrite} {
			wantActionRefused(t, string(action), CheckAction(p, KindCI, tChildType, obj, action), action)
		}
		if err := CheckAction(p, KindCI, tChildType, ObjectRef{ID: tClient1}, del); err != nil {
			t.Fatalf("other object: %v", err)
		}
	})

	t.Run("organization without grants and nil policy", func(t *testing.T) {
		p := UnrestrictedPolicy(perms("ci:write"))
		if err := CheckAction(p, KindCI, tChildType, obj, identity.PermCIWrite); err != nil {
			t.Fatalf("unrestricted with the permission: %v", err)
		}
		wantActionRefused(t, "unrestricted without the permission", CheckAction(p, KindCI, tChildType, obj, del), del)
		wantActionRefused(t, "nil policy", CheckAction(nil, KindCI, tChildType, obj, del), del)
		if p.NeedsActionTarget() || !(*FieldPolicy)(nil).NeedsActionTarget() {
			t.Fatal("NeedsActionTarget: unrestricted needs no object, nil does")
		}
	})
}

func TestWithAction(t *testing.T) {
	ctx := context.Background()
	if got := ActionFrom(ctx); got != identity.PermCIWrite {
		t.Fatalf("default action %q, want ci:write", got)
	}
	ctx = WithAction(ctx, identity.PermCIInstanceAttributeManage)
	if got := ActionFrom(ctx); got != identity.PermCIInstanceAttributeManage {
		t.Fatalf("action %q, want ci_instance_attribute:manage", got)
	}
}

func TestCheckActionOn(t *testing.T) {
	const del = identity.PermCIDelete
	target := []ActionTarget{{TypeID: tChildType, Ref: obj}}
	loads := 0
	load := func(context.Context) ([]ActionTarget, error) {
		loads++
		return target, nil
	}
	principal := identity.Principal{Subject: tUser, OrganizationID: tOrg, Type: identity.PrincipalTypeUser}

	t.Run("without principal nothing is checked", func(t *testing.T) {
		if err := CheckActionOn(context.Background(), KindCI, del, nil); err != nil {
			t.Fatalf("system write: %v", err)
		}
	})
	t.Run("principal without policy source fails closed", func(t *testing.T) {
		ctx := identity.WithPrincipal(context.Background(), principal)
		if err := CheckActionOn(ctx, KindCI, del, load); !errors.Is(err, ErrNoPolicySource) {
			t.Fatalf("err %v, want ErrNoPolicySource", err)
		}
	})
	t.Run("request of an organization without grants: the route permission governs", func(t *testing.T) {
		loads = 0
		ctx := identity.WithPrincipal(context.Background(), principal)
		ctx = WithPolicySource(ctx, unrestrictedResolver{})
		if err := CheckActionOn(ctx, KindCI, del, load); err != nil || loads != 0 {
			t.Fatalf("err %v, loads %d", err, loads)
		}
	})
	t.Run("job policy without grants reads no object and needs the permission", func(t *testing.T) {
		loads = 0
		ctx := WithPolicy(context.Background(), UnrestrictedPolicy(perms(string(del))))
		if err := CheckActionOn(ctx, KindCI, del, load); err != nil || loads != 0 {
			t.Fatalf("err %v, loads %d", err, loads)
		}
		wantActionRefused(t, "unrestricted without the permission", CheckActionOn(ctx, KindCI, identity.PermCIWrite, load), identity.PermCIWrite)
	})
	t.Run("restricted policy checks each object", func(t *testing.T) {
		repo, _ := fixture(t)
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, CITypeID: ptr(tChildType), Action: ptr(string(del)), Level: LevelNone})
		p := policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:read", string(del))}, nil)
		ctx := WithPolicy(context.Background(), p)
		loads = 0
		wantActionRefused(t, "grant none", CheckActionOn(ctx, KindCI, del, load), del)
		if loads != 1 {
			t.Fatalf("loads %d, want 1", loads)
		}
		if err := CheckActionOn(ctx, KindCI, del, nil); !errors.Is(err, ErrNoActionTarget) {
			t.Fatalf("without loader: %v, want ErrNoActionTarget", err)
		}
		other := func(context.Context) ([]ActionTarget, error) {
			return []ActionTarget{{TypeID: tOtherType, Ref: obj}}, nil
		}
		if err := CheckActionOn(ctx, KindCI, del, other); err != nil {
			t.Fatalf("other type: %v", err)
		}
		failing := func(context.Context) ([]ActionTarget, error) { return nil, errors.New("read failed") }
		if err := CheckActionOn(ctx, KindCI, del, failing); err == nil || err.Error() != "read failed" {
			t.Fatalf("load error: %v", err)
		}
	})
}

// unrestrictedResolver resolves every principal to the policy without
// grants of its permissions.
type unrestrictedResolver struct{}

func (unrestrictedResolver) Resolve(_ context.Context, p identity.Principal) (*FieldPolicy, error) { //nolint:gocritic // interface of PolicyResolver
	return UnrestrictedPolicy(p.Permissions), nil
}

// fakeStatus is a CreatorStatus.
type fakeStatus map[string]bool

func (f fakeStatus) UserActive(_ context.Context, _, userID string) (bool, error) {
	return f[userID], nil
}

func TestPolicyForCreator(t *testing.T) {
	const del = identity.PermCIDelete
	scoped := identity.Grant{RoleID: tRoleA, Permissions: perms("ci:read", "ci:write"), Clients: []string{tClient1, tClient2}}
	orgWide := identity.Grant{RoleID: tRoleB, Permissions: perms(string(del))}
	resolve := func(t *testing.T, active bool, scope *CreatorScope) (*FieldPolicy, error) {
		t.Helper()
		repo, _ := fixture(t)
		// A grant of another role makes the organization restricted.
		addField(t, repo, FieldGrant{RoleID: ptr(tCustom), ObjectKind: KindCI, Action: ptr(string(del)), Level: LevelNone})
		access := &fakeAccess{users: map[string][]identity.Grant{tUser: {scoped, orgWide}}}
		return NewFieldResolver(access, access, repo, nil).PolicyForCreator(context.Background(), fakeStatus{tUser: active}, tOrg, tUser, scope)
	}
	in1 := ObjectRef{ID: tObject, ClientID: tClient1}
	in2 := ObjectRef{ID: tObject, ClientID: tClient2}

	p, err := resolve(t, true, &CreatorScope{Clients: []string{tClient1}})
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckAction(p, KindCI, tChildType, in1, identity.PermCIWrite); err != nil {
		t.Fatalf("client of the snapshot: %v", err)
	}
	wantActionRefused(t, "client outside the snapshot", CheckAction(p, KindCI, tChildType, in2, identity.PermCIWrite), identity.PermCIWrite)
	wantActionRefused(t, "org-wide grant narrowed to the snapshot", CheckAction(p, KindCI, tChildType, in2, del), del)
	if err = CheckAction(p, KindCI, tChildType, in1, del); err != nil {
		t.Fatalf("org-wide grant in the snapshot: %v", err)
	}

	p, err = resolve(t, true, &CreatorScope{})
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckAction(p, KindCI, tChildType, in2, identity.PermCIWrite); err != nil {
		t.Fatalf("snapshot without restriction: %v", err)
	}

	p, err = resolve(t, true, &CreatorScope{Clients: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	wantActionRefused(t, "empty snapshot dimension", CheckAction(p, KindCI, tChildType, in1, identity.PermCIWrite), identity.PermCIWrite)

	p, err = resolve(t, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantActionRefused(t, "job without snapshot", CheckAction(p, KindCI, tChildType, in1, identity.PermCIWrite), identity.PermCIWrite)

	if _, err = resolve(t, false, &CreatorScope{}); !errors.Is(err, ErrCreatorInactive) {
		t.Fatalf("inactive creator: %v, want ErrCreatorInactive", err)
	}
	repo, _ := fixture(t)
	if _, err = NewFieldResolver(&fakeAccess{}, nil, repo, nil).PolicyForCreator(context.Background(), nil, tOrg, tUser, &CreatorScope{}); !errors.Is(err, ErrCreatorInactive) {
		t.Fatalf("without user status: %v, want ErrCreatorInactive", err)
	}
	if _, err = NewFieldResolver(&fakeAccess{}, nil, repo, nil).PolicyForCreator(context.Background(), fakeStatus{}, tOrg, "system", &CreatorScope{}); !errors.Is(err, ErrCreatorInactive) {
		t.Fatalf("no user id: %v, want ErrCreatorInactive", err)
	}
}
