package history

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// Field permissions of the asset history (RBA-07, E-43, Konvention 11,
// WP-794): GET /history/asset/{id}, GET /assets/{id}/state and the result
// of an asset snapshot job carry no value of a field the principal may not
// read. The trail names the fields by their DTO paths (the trigger
// asset_history and the custom fields of asset/pg_repository.go), so the
// projection follows the asset DTO (permission.Readable): structural
// members stay, a typed field needs read access, the custom fields are
// projected per key (custom_fields.<key>), a field without mapping is left
// out. The type and scope come from the asset (AssetLookup); an asset it
// does not find leaves only the structural fields under a restricted
// policy (fail-closed).

// AssetFacts are the type and the scope of an asset.
type AssetFacts struct {
	TypeID   string
	ClientID string
	SiteID   string
}

// AssetLookup reads the facts of an asset the caller sees; nil without
// error for one it does not find. PGRepository implements it.
type AssetLookup interface {
	AssetFacts(ctx context.Context, orgID, assetID string) (*AssetFacts, error)
}

// memberCustomFields is the asset DTO member and trail field of the custom
// fields; custom_fields.<key> is the DTO path of one.
const memberCustomFields = "custom_fields"

// assetProjection projects the history values of one asset.
type assetProjection struct {
	// policy nil reads only the structural fields.
	policy *permission.FieldPolicy
	typeID string
	ref    permission.ObjectRef
}

// assetProjection returns the projection of the asset's history for
// policy; nil when the policy restricts no asset field.
func (h *Handler) assetProjection(ctx context.Context, orgID, assetID string, policy *permission.FieldPolicy) (*assetProjection, error) {
	if !permission.NeedsProjection(policy, permission.KindAsset) {
		return nil, nil
	}
	p := &assetProjection{ref: permission.ObjectRef{ID: assetID}}
	lookup, ok := h.repo.(AssetLookup)
	if !ok {
		return p, nil
	}
	facts, err := lookup.AssetFacts(ctx, orgID, assetID)
	if err != nil || facts == nil {
		return p, err
	}
	p.typeID = facts.TypeID
	p.ref.ClientID, p.ref.SiteID = facts.ClientID, facts.SiteID
	if policy.Level(permission.KindAsset, facts.TypeID, "", p.ref).Readable() {
		p.policy = policy
	}
	return p, nil
}

// readable reports whether the DTO path of the asset is readable.
func (p *assetProjection) readable(path string) bool {
	return permission.Readable(p.policy, permission.KindAsset, p.typeID, p.ref, path)
}

// customFields keeps the readable keys of a custom fields value; a value
// that is no object is left out.
func (p *assetProjection) customFields(value any) any {
	fields, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		if p.readable(memberCustomFields + "." + k) {
			out[k] = v
		}
	}
	return out
}

// object projects an asset as the history keeps it (the value of a create
// or delete, the fields of a state): the custom fields per key, every
// other member by its DTO path; a value that is no object is left out.
func (p *assetProjection) object(value any) any {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]any, len(obj))
	for member, v := range obj {
		switch {
		case member == memberCustomFields:
			if v != nil {
				v = p.customFields(v)
			}
			out[member] = v
		case p.readable(member):
			out[member] = v
		}
	}
	return out
}

// changes projects the history entries of the asset: a create or delete
// (the whole asset, or its custom fields as {custom_fields: …}) as an
// object, the custom fields per key, an entry of an unreadable field is
// left out.
func (p *assetProjection) changes(items []Change) []Change {
	out := make([]Change, 0, len(items))
	for i := range items {
		c := items[i]
		switch {
		case c.ChangeType == "create" || c.ChangeType == "delete" || c.FieldName == "":
			c.OldValue, c.NewValue = p.object(c.OldValue), p.object(c.NewValue)
		case c.FieldName == memberCustomFields:
			c.OldValue, c.NewValue = p.customFields(c.OldValue), p.customFields(c.NewValue)
		case !p.readable(c.FieldName):
			continue
		}
		out = append(out, c)
	}
	return out
}

// state projects the fields of a state snapshot.
func (p *assetProjection) state(s *StateSnapshot) {
	if fields, ok := p.object(s.Fields).(map[string]any); ok {
		s.Fields = fields
	}
}

// projectAssetList projects the history of the asset assetID with the
// policy of the request.
func (h *Handler) projectAssetList(ctx context.Context, orgID, assetID string, items []Change) ([]Change, error) {
	policy, err := permission.PolicyFrom(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.assetProjection(ctx, orgID, assetID, policy)
	if err != nil || p == nil {
		return items, err
	}
	return p.changes(items), nil
}
