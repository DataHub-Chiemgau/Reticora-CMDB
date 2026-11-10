package permission

import (
	"context"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// Field rights of computed CI attributes over asset fields (SR-58,
// WP-871): an expression may read whitelisted fields of the linked asset
// (rules.AssetFields), so its result inherits the strictest rule of its
// sources. The attribute is readable only when its own field is readable
// and every source field is readable at the linked asset; a sensitive
// source makes the attribute sensitive, an unknown asset closes it while
// the asset policy is restricted, and without the feature assets_basic it
// is closed for every reader. The rule sits in projectConfig.readable, so
// every channel that projects through this package follows it.

// FeatureAssetsBasic is the entitlement key the asset sources need
// (entitlement.FeatureAssetsBasic; the package does not import entitlement).
const FeatureAssetsBasic = "assets_basic"

// FeatureCheck reports whether a feature is enabled for the organization
// (entitlement.Service.IsEnabled).
type FeatureCheck func(ctx context.Context, orgID, featureKey string) bool

// ComputedAssetFields returns the asset fields (without rules.AssetPrefix)
// an expression of attribute_definition.computed reads; every field of
// rules.AssetFields for an expression that does not parse (fail-closed).
func ComputedAssetFields(raw []byte) []string {
	expr, err := rules.ParseExpr(raw)
	if err != nil {
		return slices.Clone(rules.AssetFields)
	}
	var out []string
	for _, f := range expr.Fields() {
		if name, ok := strings.CutPrefix(f, rules.AssetPrefix); ok && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// hasComputedSources reports whether the organization has a computed
// attribute with asset sources.
func (p *FieldPolicy) hasComputedSources() bool {
	return p != nil && p.idx != nil && len(p.idx.computed) > 0
}

// computedHidden reports whether a computed attribute with asset sources
// can be closed for this principal: the feature assets_basic is off or the
// asset policy is restricted.
func (p *FieldPolicy) computedHidden() bool {
	return p.hasComputedSources() && (p.assetsOff || p.Restricted(KindAsset))
}

// assetSources returns the asset fields the computed attribute reads at a
// CI of type typeID: the union of its global and set-scoped definitions and
// of those of the type or an ancestor; nil for an attribute without asset
// sources.
func (p *FieldPolicy) assetSources(typeID, attribute string) []string {
	if !p.hasComputedSources() {
		return nil
	}
	sources := p.idx.computed[attribute]
	if len(sources) == 0 {
		return nil
	}
	chain := p.chain(KindCI, typeID)
	var out []string
	for _, s := range sources {
		if s.CITypeID != nil && !slices.Contains(chain, *s.CITypeID) {
			continue
		}
		for _, f := range s.AssetFields {
			if !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	return out
}

// computedReadable reports whether the computed attribute with field key
// key and the asset sources is readable at the CI (SR-58).
func (c *projectConfig) computedReadable(policy *FieldPolicy, typeID string, ref *ObjectRef, key string, sources []string) bool {
	if policy.assetsOff {
		return false
	}
	assetRestricted := policy.Restricted(KindAsset)
	if assetRestricted && c.asset == nil {
		return false
	}
	var assetChain []string
	if c.asset != nil {
		assetChain = policy.chain(KindAsset, c.asset.typeID)
	}
	sensitive := false
	for _, name := range sources {
		assetKey, structural, ok := c.fieldKey(KindAsset, name)
		if !ok {
			return false
		}
		if structural {
			continue
		}
		if !policy.unrestricted && policy.sensitive(KindAsset, assetChain, assetKey) {
			sensitive = true
		}
		if assetRestricted && !policy.Level(KindAsset, c.asset.typeID, assetKey, c.asset.ref).Readable() {
			return false
		}
	}
	return policy.level(KindCI, typeID, key, ref, sensitive).Readable()
}

// listReadable is Level(...).Readable() for the checks without a single
// object (list filters, export manifests, structured query columns): a
// computed attribute with asset sources is readable only when the feature
// is on, every source is readable at every asset type the policy knows,
// and the attribute itself is readable, as sensitive when a source is.
func (p *FieldPolicy) listReadable(kind ObjectKind, typeID, key string, ref ObjectRef) bool { //nolint:gocritic // ObjectRef by value like Level
	if kind == KindCI {
		if name, ok := strings.CutPrefix(key, NamespaceAttributes+"."); ok {
			if sources := p.assetSources(typeID, name); len(sources) > 0 {
				return p.computedListReadable(typeID, key, &ref, sources)
			}
		}
	}
	return p.Level(kind, typeID, key, ref).Readable()
}

func (p *FieldPolicy) computedListReadable(typeID, key string, ref *ObjectRef, sources []string) bool {
	if p.assetsOff {
		return false
	}
	cfg := &projectConfig{}
	assetTypes := p.listTypes(KindAsset, nil)
	restricted := p.Restricted(KindAsset)
	sensitive := false
	for _, name := range sources {
		assetKey, structural, ok := cfg.fieldKey(KindAsset, name)
		if !ok {
			return false
		}
		if structural {
			continue
		}
		if !p.unrestricted {
			for _, t := range assetTypes {
				if p.sensitive(KindAsset, p.chain(KindAsset, t), assetKey) {
					sensitive = true
				}
			}
		}
		if restricted && !p.readableForTypes(KindAsset, assetTypes, []string{assetKey}) {
			return false
		}
	}
	return p.level(KindCI, typeID, key, ref, sensitive).Readable()
}

// WithFeatureCheck makes the resolver close computed attributes with asset
// sources for an organization without the feature assets_basic (AK3 of
// WP-871). Without it the feature counts as enabled.
func (r *FieldResolver) WithFeatureCheck(check FeatureCheck) *FieldResolver {
	r.features = check
	return r
}

// assetsOff reports whether the organization lacks assets_basic.
func (r *FieldResolver) assetsOff(ctx context.Context, orgID string) bool {
	return r.features != nil && !r.features(ctx, orgID, FeatureAssetsBasic)
}
