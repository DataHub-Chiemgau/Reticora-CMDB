package permission

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Unit tests of SR-58 (WP-871): a computed CI attribute over asset fields
// inherits the strictest field rule of its sources.

// computedCI is a CI DTO with the computed attribute price_band (reads
// asset.purchase_price) and the entered attribute owner.
func computedCI() map[string]any {
	ci := sampleCI()
	ci["attributes"] = map[string]any{"price_band": "high", "owner": "ops"}
	return ci
}

// computedPolicy resolves the policy of tUser with the grants against repo;
// assets reports assets_basic (nil: no feature check).
func computedPolicy(t *testing.T, repo *MemoryFieldGrants, assets *bool, grants ...identity.Grant) *FieldPolicy {
	t.Helper()
	access := &fakeAccess{users: map[string][]identity.Grant{tUser: grants}}
	var all []identity.Permission
	for i := range grants {
		all = append(all, grants[i].Permissions...)
	}
	resolver := NewFieldResolver(access, access, repo, nil)
	if assets != nil {
		resolver.WithFeatureCheck(func(_ context.Context, orgID, key string) bool {
			if orgID != tOrg || key != FeatureAssetsBasic {
				t.Errorf("feature check %s %s", orgID, key)
			}
			return *assets
		})
	}
	p, err := resolver.Resolve(context.Background(),
		identity.Principal{Subject: tUser, OrganizationID: tOrg, Type: identity.PrincipalTypeUser, Permissions: all})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return p
}

// computedAttrs projects computedCI and returns its attributes.
func computedAttrs(t *testing.T, policy *FieldPolicy, typeID string, withAsset bool) map[string]any {
	t.Helper()
	opts := []ProjectOption{}
	if withAsset {
		opts = append(opts, WithAsset(tAssetType, ObjectRef{ID: tAsset, ClientID: tClient1}))
	}
	got, err := Project(policy, KindCI, typeID, ObjectRef{ID: tObject, ClientID: tClient1}, computedCI(), opts...)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	return decode(t, got["attributes"]).(map[string]any)
}

var (
	readerGrant = roleGrant(tRoleA, "ci:read", "asset:read")
	both        = map[string]any{"price_band": "high", "owner": "ops"}
	ownerOnly   = map[string]any{"owner": "ops"}
	yes, no     = true, false
)

func priceBand(typeID *string) ComputedSource {
	return ComputedSource{CITypeID: typeID, Attribute: "price_band", AssetFields: []string{"purchase_price"}}
}

// TestComputedAttributeAssetSources covers AK1: the source field none at
// the asset closes the attribute, a readable source keeps it, an unknown
// asset closes it while the asset policy is restricted, a sensitive source
// needs a field grant on the attribute itself.
func TestComputedAttributeAssetSources(t *testing.T) {
	t.Run("source none", func(t *testing.T) {
		repo, _ := fixture(t)
		repo.SetComputed(tOrg, []ComputedSource{priceBand(nil)})
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindAsset, FieldKey: ptr("core.purchase_cost"), Level: LevelNone})
		policy := computedPolicy(t, repo, nil, readerGrant)
		if got := computedAttrs(t, policy, tChildType, true); !reflect.DeepEqual(got, ownerOnly) {
			t.Errorf("attributes = %v, want %v", got, ownerOnly)
		}
		if Readable(policy, KindCI, tChildType, ObjectRef{ID: tObject}, "attributes.price_band",
			WithAsset(tAssetType, ObjectRef{ID: tAsset})) {
			t.Errorf("Readable of price_band with an unreadable source")
		}
		if !NeedsProjection(policy, KindCI) {
			t.Errorf("NeedsProjection must hold with computed sources and a restricted asset policy")
		}
	})
	t.Run("source readable", func(t *testing.T) {
		repo, _ := fixture(t)
		repo.SetComputed(tOrg, []ComputedSource{priceBand(nil)})
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindAsset, FieldKey: ptr("core.supplier"), Level: LevelNone})
		policy := computedPolicy(t, repo, nil, readerGrant)
		if got := computedAttrs(t, policy, tChildType, true); !reflect.DeepEqual(got, both) {
			t.Errorf("attributes = %v, want %v", got, both)
		}
		// Without the asset the attribute is unreadable while the asset
		// policy is restricted.
		if got := computedAttrs(t, policy, tChildType, false); !reflect.DeepEqual(got, ownerOnly) {
			t.Errorf("attributes without asset = %v, want %v", got, ownerOnly)
		}
	})
	t.Run("attribute none", func(t *testing.T) {
		repo, _ := fixture(t)
		repo.SetComputed(tOrg, []ComputedSource{priceBand(nil)})
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr("attributes.price_band"), Level: LevelNone})
		policy := computedPolicy(t, repo, nil, readerGrant)
		if got := computedAttrs(t, policy, tChildType, true); !reflect.DeepEqual(got, ownerOnly) {
			t.Errorf("attributes = %v, want %v", got, ownerOnly)
		}
	})
	t.Run("sensitive source", func(t *testing.T) {
		repo, _ := fixture(t)
		repo.SetComputed(tOrg, []ComputedSource{priceBand(nil)})
		addSensitive(t, repo, SensitiveField{ObjectKind: KindAsset, FieldKey: "core.purchase_cost"})
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindAsset, FieldKey: ptr("core.purchase_cost"), Level: LevelRead})
		policy := computedPolicy(t, repo, nil, readerGrant)
		if got := computedAttrs(t, policy, tChildType, true); !reflect.DeepEqual(got, ownerOnly) {
			t.Errorf("sensitive source without a grant on the attribute: %v, want %v", got, ownerOnly)
		}
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr("attributes.price_band"), Level: LevelRead})
		policy = computedPolicy(t, repo, nil, readerGrant)
		if got := computedAttrs(t, policy, tChildType, true); !reflect.DeepEqual(got, both) {
			t.Errorf("sensitive source with a grant on the attribute: %v, want %v", got, both)
		}
	})
	t.Run("type scope", func(t *testing.T) {
		repo, _ := fixture(t)
		// Defined at the parent type: the child inherits it; another type
		// does not see the definition.
		repo.SetComputed(tOrg, []ComputedSource{priceBand(ptr(tParentType))})
		addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindAsset, FieldKey: ptr("core.purchase_cost"), Level: LevelNone})
		policy := computedPolicy(t, repo, nil, readerGrant)
		if got := computedAttrs(t, policy, tChildType, true); !reflect.DeepEqual(got, ownerOnly) {
			t.Errorf("child type: %v, want %v", got, ownerOnly)
		}
		if got := computedAttrs(t, policy, tOtherType, true); !reflect.DeepEqual(got, both) {
			t.Errorf("other type: %v, want %v", got, both)
		}
	})
}

// TestComputedAttributeAssetsFeature covers AK3: without assets_basic a
// computed attribute with asset sources is closed for every reader, also
// in an organization without field permissions; with it nothing changes.
func TestComputedAttributeAssetsFeature(t *testing.T) {
	repo, _ := fixture(t)
	repo.SetComputed(tOrg, []ComputedSource{priceBand(nil)})
	off := computedPolicy(t, repo, &no, readerGrant)
	if !NeedsProjection(off, KindCI) {
		t.Fatalf("NeedsProjection without assets_basic")
	}
	if got := computedAttrs(t, off, tChildType, true); !reflect.DeepEqual(got, ownerOnly) {
		t.Errorf("without assets_basic: %v, want %v", got, ownerOnly)
	}
	if got := ProjectAttributes(off, tChildType, ObjectRef{ID: tObject}, map[string]any{"price_band": 1, "owner": 2}); !reflect.DeepEqual(got, map[string]any{"owner": 2}) {
		t.Errorf("ProjectAttributes without assets_basic: %v", got)
	}
	on := computedPolicy(t, repo, &yes, readerGrant)
	if NeedsProjection(on, KindCI) {
		t.Errorf("an organization without field permissions and with assets_basic needs no projection")
	}
	if got := computedAttrs(t, on, tChildType, true); !reflect.DeepEqual(got, both) {
		t.Errorf("with assets_basic: %v, want %v", got, both)
	}
	// Without computed sources the feature check changes nothing.
	plain, _ := fixture(t)
	if NeedsProjection(computedPolicy(t, plain, &no, readerGrant), KindCI) {
		t.Errorf("no computed sources: NeedsProjection without assets_basic")
	}
}

// TestComputedAssetFields covers the extraction of the asset sources from
// the stored expression.
func TestComputedAssetFields(t *testing.T) {
	expr := []byte(`{"op":"add","args":[{"op":"field","field":"asset.purchase_price"},{"op":"field","field":"cost"},{"op":"field","field":"asset.purchase_price"}]}`)
	if got := ComputedAssetFields(expr); !reflect.DeepEqual(got, []string{"purchase_price"}) {
		t.Errorf("fields = %v", got)
	}
	if got := ComputedAssetFields([]byte(`{"op":"field","field":"cost"}`)); len(got) != 0 {
		t.Errorf("no asset field: %v", got)
	}
	if got := ComputedAssetFields([]byte(`{"op":"bogus"}`)); !slices.Contains(got, "purchase_price") || !slices.Contains(got, "serial_number") {
		t.Errorf("unparsable expression must read every asset field: %v", got)
	}
}
