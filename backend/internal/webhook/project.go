package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// Field permissions of webhook payloads (RBA-07, E-43, WP-730): the
// dispatcher projects the payload of an event per subscription with the
// field policy of its service account, an unbound subscription with the
// default policy without role (SubscriberAccess.FieldPolicy). The rules
// depend on the event type (projectionRules); a type without rule and a
// payload that does not have the form of its type reach a subscription
// only while its policy restricts nothing the payload could carry
// (fail-closed). The projection reads the database only for
// override.changed (the CI of the field, DispatcherOptions.CIs) and for
// relationship.* (the scope of both ends, eventObjects).

// CIFieldScope is the type, scope and instance attribute keys of a CI, the
// facts the projection of override.changed needs.
type CIFieldScope struct {
	TypeID       string
	ClientID     string
	SiteID       string
	InstanceKeys []string
}

// CIFieldScopes reads the CIFieldScope of a CI of the organization (the
// dispatcher calls it with the org-wide tenant scope); nil without error
// when the CI is unknown or deleted.
type CIFieldScopes interface {
	CIFieldScope(ctx context.Context, orgID, ciID string) (*CIFieldScope, error)
}

// unknownScope is the client and site of an object whose scope the
// projection does not know (an end of an edge without client or site,
// embedded asset facts): only role assignments without client and site
// scope cover it, so only their grants count there.
const unknownScope = "\x00unknown"

// projectionRule projects the decoded payload of one event for a policy;
// false withholds the event from the subscription.
type projectionRule func(p *projector, body map[string]json.RawMessage) (map[string]json.RawMessage, bool)

// passThrough is the rule of the events without field values of an object
// kind with field permissions: metamodel (ci_type.*, ci_attribute.*,
// lifecycle.*, metamodel.published), discovery and reconciliation (like
// the review payloads, WP-265), the CI events that carry only structural
// fields and the operational events.
func passThrough(_ *projector, body map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	return body, true
}

// projectionRules are the rules by event type (the canonical name the
// dispatcher stores; the direct dispatches of the CI handler use the former
// name ci.updated).
var projectionRules = map[string]projectionRule{
	"ci.created":        (*projector).ciSnapshot,
	"ci.deleted":        (*projector).ciSnapshot,
	"ci.restored":       (*projector).ciSnapshot,
	"ci.changed":        (*projector).ciChange,
	"ci.updated":        (*projector).ciChange,
	"ci.status_changed": (*projector).ciChange,
	// IDs, name, status and timestamps only.
	"ci.merged":  passThrough,
	"ci.offline": passThrough,
	"ci.online":  passThrough,

	"override.changed": (*projector).override,

	"relationship.created": (*projector).relationship,
	"relationship.removed": (*projector).relationship,
	"relationship.deleted": (*projector).relationship,
	"relationship.changed": (*projector).relationship,

	// The asset events of WP-740: the asset; asset.changed also names
	// the changed fields.
	"asset.created": (*projector).asset,
	"asset.deleted": (*projector).asset,
	"asset.changed": (*projector).assetChange,

	"metamodel.published": passThrough,
	"import.completed":    passThrough,
	"stock.below_minimum": passThrough,
	"stock.movement":      (*projector).embeddedAsset,
}

// prefixRules are the rules of the event types by prefix.
var prefixRules = map[string]projectionRule{
	"asset":          (*projector).asset,
	"inventory":      (*projector).embeddedAsset,
	"reservation":    (*projector).embeddedAsset,
	"composition":    (*projector).embeddedAsset,
	"ci_type":        passThrough,
	"ci_attribute":   passThrough,
	"lifecycle":      passThrough,
	"discovery":      passThrough,
	"reconciliation": passThrough,
	"review":         passThrough,
	"collector":      passThrough,
	"export":         passThrough,
	"job":            passThrough,
	"entitlement":    passThrough,
	"invitation":     passThrough,
	"certificate":    passThrough,
	"webhook":        passThrough,
	"apikey":         passThrough,
}

// unruledKinds are the object kinds of the event types without rule whose
// objects have field permissions: such an event is withheld from a policy
// that restricts the kind.
var unruledKinds = map[string]permission.ObjectKind{
	"contract": permission.KindContract,
	"ticket":   permission.KindTicket,
	"document": permission.KindDocument,
}

// ruleOf returns the projection rule of the event type.
func ruleOf(event string) (projectionRule, bool) {
	if rule, ok := projectionRules[event]; ok {
		return rule, true
	}
	prefix, _, _ := strings.Cut(event, ".")
	rule, ok := prefixRules[prefix]
	return rule, ok
}

// restrictsAny reports whether the policy may hide a field of any object
// kind; a nil policy restricts everything.
func restrictsAny(policy *permission.FieldPolicy) bool {
	for _, kind := range permission.ObjectKinds {
		if policy.Restricted(kind) {
			return true
		}
	}
	return false
}

// unruled reports whether an event without (applicable) rule may reach a
// subscription with the policy: only when the policy restricts no field
// the payload could carry.
func unruled(policy *permission.FieldPolicy, event string) bool {
	prefix, _, _ := strings.Cut(event, ".")
	if kind, ok := unruledKinds[prefix]; ok {
		return !policy.Restricted(kind)
	}
	return !restrictsAny(policy)
}

// isWorkflowFrame reports whether the payload is the run frame of the
// workflow action send_webhook ({run_id, context}) under a freely chosen
// event name: it has the form of no event type.
func isWorkflowFrame(body map[string]json.RawMessage) bool {
	_, run := body["run_id"]
	_, wfContext := body["context"]
	return run && wfContext && len(body) == 2
}

// projector projects the payload of one event for one policy.
type projector struct {
	ctx     context.Context
	d       *Dispatcher
	orgID   string
	event   string
	policy  *permission.FieldPolicy
	objects func() []ObjectScope
}

// projectPayload returns the payload of the event for a subscription with
// the policy: the body itself when nothing is projected, otherwise the
// projected object; false withholds the event (RBA-07, WP-730).
func (p *projector) projectPayload(body []byte) ([]byte, bool) {
	rule, ok := ruleOf(p.event)
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil || isWorkflowFrame(fields) {
		// No event object: only a policy without any restriction reads it.
		return body, !restrictsAny(p.policy)
	}
	if !ok {
		return body, unruled(p.policy, p.event)
	}
	out, deliver := rule(p, fields)
	if !deliver {
		return nil, false
	}
	if out == nil {
		return body, true
	}
	raw, err := json.Marshal(out)
	if err != nil {
		slog.Error("encode projected webhook payload failed", "error", err, "organization_id", p.orgID, "event", p.event)
		return nil, false
	}
	return raw, true
}

// stringMember reads a string member: present reports whether it is set
// (not missing and not null), ok whether it is a string where set.
func stringMember(body map[string]json.RawMessage, name string) (value string, present, ok bool) {
	raw, exists := body[name]
	if !exists || string(raw) == "null" {
		return "", false, true
	}
	if json.Unmarshal(raw, &value) != nil {
		return "", true, false
	}
	return value, true, true
}

// requiredString reads a non-empty string member.
func requiredString(body map[string]json.RawMessage, name string) (string, bool) {
	value, present, ok := stringMember(body, name)
	return value, present && ok && value != ""
}

// objectMember decodes an object member; present is false for a missing
// or null member, ok false for one that is no object.
func objectMember(body map[string]json.RawMessage, name string) (obj map[string]json.RawMessage, present, ok bool) {
	raw, exists := body[name]
	if !exists || string(raw) == "null" {
		return nil, false, true
	}
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, true, false
	}
	return obj, true, true
}

// setMember encodes value into body[name].
func setMember(body map[string]json.RawMessage, name string, value any) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	body[name] = raw
	return true
}

// ciReadable reports whether the member path of the CI is readable: a CI
// attribute may also be an instance attribute of the same name, whose key
// the payload does not name, so it must be readable as both.
func (p *projector) ciReadable(typeID string, ref *permission.ObjectRef, path string) bool {
	if !permission.Readable(p.policy, permission.KindCI, typeID, *ref, path) {
		return false
	}
	name, isAttr := strings.CutPrefix(path, "attributes.")
	return !isAttr || permission.Readable(p.policy, permission.KindCI, typeID, *ref, path, permission.WithInstanceKeys([]string{name}))
}

// ciObject projects a CI snapshot (ci.Item): structural members stay,
// typed columns and attributes follow the CI policy, the embedded asset
// facts the asset policy; a member without mapping is left out. keep
// names members of the event frame the caller handles (changed_fields,
// from, to). The snapshot must name id and ci_type_id.
func (p *projector) ciObject(body map[string]json.RawMessage, keep map[string]bool) (map[string]json.RawMessage, bool) {
	id, okID := requiredString(body, "id")
	typeID, okType := requiredString(body, "ci_type_id")
	client, _, okClient := stringMember(body, "client_id")
	site, _, okSite := stringMember(body, "site_id")
	if !okID || !okType || !okClient || !okSite {
		return nil, false
	}
	ref := permission.ObjectRef{ID: id, ClientID: client, SiteID: site}
	ciRestricted := p.policy.Restricted(permission.KindCI)
	for member := range body {
		switch {
		case keep[member]:
		case member == "asset":
			facts, present, ok := objectMember(body, member)
			if !ok {
				return nil, false
			}
			if present && !setMember(body, member, p.assetFacts(facts)) {
				return nil, false
			}
		case !ciRestricted:
		case member == "attributes":
			attrs, present, ok := objectMember(body, member)
			if !ok {
				return nil, false
			}
			if !present {
				continue
			}
			for k := range attrs {
				if !p.ciReadable(typeID, &ref, "attributes."+k) {
					delete(attrs, k)
				}
			}
			if !setMember(body, member, attrs) {
				return nil, false
			}
		case !p.ciReadable(typeID, &ref, member):
			delete(body, member)
		}
	}
	return body, true
}

// ciSnapshot is the rule of ci.created, ci.deleted and ci.restored.
func (p *projector) ciSnapshot(body map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	if !permission.NeedsProjection(p.policy, permission.KindCI) {
		return nil, true
	}
	return p.ciObject(body, nil)
}

// ciChangeFrame are the members of ci.changed and ci.status_changed beside
// the CI snapshot.
var ciChangeFrame = map[string]bool{"changed_fields": true, "from": true, "to": true}

// ciChangeMarkers are change names of ci.changed without a CI field: the
// template list, the tags and a merge.
var ciChangeMarkers = map[string]bool{"templates": true, "tags": true, "merged_ci": true}

// ciChange is the rule of ci.changed (also as ci.updated) and
// ci.status_changed: the snapshot as ciSnapshot, changed_fields only with
// readable fields; an event whose changed fields are all unreadable is
// withheld. attributes counts as readable while the projected snapshot
// keeps an attribute (the change names no single key).
func (p *projector) ciChange(body map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	if !permission.NeedsProjection(p.policy, permission.KindCI) {
		return nil, true
	}
	var changed []string
	if raw, ok := body["changed_fields"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &changed) != nil {
			return nil, false
		}
	}
	out, ok := p.ciObject(body, ciChangeFrame)
	if !ok || changed == nil {
		return out, ok
	}
	typeID, _ := requiredString(out, "ci_type_id")
	id, _ := requiredString(out, "id")
	client, _, _ := stringMember(out, "client_id")
	site, _, _ := stringMember(out, "site_id")
	ref := permission.ObjectRef{ID: id, ClientID: client, SiteID: site}
	attrs, _, _ := objectMember(out, "attributes")
	kept := make([]string, 0, len(changed))
	for _, name := range changed {
		readable := false
		switch {
		case ciChangeMarkers[name]:
			readable = true
		case name == "attributes":
			readable = len(attrs) > 0
		default:
			_, structural, mapped := permission.FieldKeyOf(permission.KindCI, name)
			readable = structural || (mapped && p.ciReadable(typeID, &ref, name))
		}
		if readable {
			kept = append(kept, name)
		}
	}
	if len(changed) > 0 && len(kept) == 0 {
		return nil, false
	}
	if !setMember(out, "changed_fields", kept) {
		return nil, false
	}
	return out, true
}

// override is the rule of override.changed: the event names one field of
// a CI with its discovered and override value; it reaches the subscription
// only when the field is readable for the type and scope of the CI.
func (p *projector) override(body map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	if !p.policy.Restricted(permission.KindCI) {
		return nil, true
	}
	ciID, okCI := requiredString(body, "ci_id")
	name, okName := requiredString(body, "field_name")
	if !okCI || !okName || p.d.opts.CIs == nil {
		return nil, false
	}
	scope, err := p.d.opts.CIs.CIFieldScope(orgScoped(database.WithoutTx(p.ctx), p.orgID), p.orgID, ciID)
	if err != nil {
		slog.Error("read CI of webhook event failed; not delivered", "error", err, "organization_id", p.orgID, "event", p.event)
		return nil, false
	}
	if scope == nil {
		return nil, false
	}
	ref := permission.ObjectRef{ID: ciID, ClientID: scope.ClientID, SiteID: scope.SiteID}
	return nil, permission.Readable(p.policy, permission.KindCI, scope.TypeID, ref, permission.CIFieldPath(name),
		permission.WithInstanceKeys(scope.InstanceKeys))
}

// relationshipEnds returns the object references of both ends of an edge:
// the scope of its source and target CI (eventObjects), an end without
// client or site, or of unknown scope, as unknownScope.
func (p *projector) relationshipEnds(id string) [2]permission.ObjectRef {
	ends := [2]permission.ObjectRef{
		{ID: id, ClientID: unknownScope, SiteID: unknownScope},
		{ID: id, ClientID: unknownScope, SiteID: unknownScope},
	}
	objects := p.objects()
	if len(objects) != len(ends) {
		return ends
	}
	for i, o := range objects {
		if o.ClientID != "" {
			ends[i].ClientID = o.ClientID
		}
		if o.SiteID != "" {
			ends[i].SiteID = o.SiteID
		}
	}
	return ends
}

// relationshipReadable reports whether the field of the edge (empty: the
// edge as a whole) is readable at both ends (E-140).
func (p *projector) relationshipReadable(relType, fieldKey string, ends *[2]permission.ObjectRef) bool {
	for i := range ends {
		if !p.policy.Level(permission.KindRelationship, relType, fieldKey, ends[i]).Readable() {
			return false
		}
	}
	return true
}

// relationship is the rule of relationship.*: the event reaches the
// subscription only when the relationship type is readable at both ends;
// attributes, notes, confidence and the other grantable columns of the
// edge follow its field grants.
func (p *projector) relationship(body map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	if !p.policy.Restricted(permission.KindRelationship) {
		return nil, true
	}
	id, okID := requiredString(body, "id")
	relType, okType := requiredString(body, "rel_type")
	if !okID || !okType {
		return nil, false
	}
	ends := p.relationshipEnds(id)
	if !p.relationshipReadable(relType, "", &ends) {
		return nil, false
	}
	for member := range body {
		if member == "attributes" {
			attrs, present, ok := objectMember(body, member)
			if !ok {
				return nil, false
			}
			if !present {
				continue
			}
			for k := range attrs {
				if !p.relationshipReadable(relType, permission.NamespaceAttributes+"."+k, &ends) {
					delete(attrs, k)
				}
			}
			if !setMember(body, member, attrs) {
				return nil, false
			}
			continue
		}
		key := permission.NamespaceCore + "." + member
		if _, err := permission.ParseFieldKey(permission.KindRelationship, key); err == nil && !p.relationshipReadable(relType, key, &ends) {
			delete(body, member)
		}
	}
	return body, true
}

// assetReadable reports whether the asset field is readable: for every
// asset type (the payload names none) and for the object.
func (p *projector) assetReadable(key string, ref *permission.ObjectRef) bool {
	return len(permission.TypeUnreadableKeys(p.policy, permission.KindAsset, nil, []string{key})) == 0 &&
		p.policy.Level(permission.KindAsset, "", key, *ref).Readable()
}

// assetMembers are members of asset payloads without a column of the
// asset DTO: the site is structural ("" ), expires_at of
// asset.warranty_expiring is the warranty end.
var assetMembers = map[string]string{"site_id": "", "expires_at": "core.warranty_end"}

// assetObject projects asset fields (an asset payload or embedded asset
// facts) with the asset policy: structural members stay, columns and
// custom_fields keys follow the policy, a member without mapping is left
// out.
func (p *projector) assetObject(body map[string]json.RawMessage, ref *permission.ObjectRef) map[string]json.RawMessage {
	for member := range body {
		if member == "custom_fields" {
			fields, present, ok := objectMember(body, member)
			if !ok || !present {
				delete(body, member)
				continue
			}
			for k := range fields {
				if !p.assetReadable(permission.NamespaceAttributes+"."+k, ref) {
					delete(fields, k)
				}
			}
			if !setMember(body, member, fields) {
				delete(body, member)
			}
			continue
		}
		key, structural, ok := permission.FieldKeyOf(permission.KindAsset, member)
		if !ok {
			key, ok = assetMembers[member]
			structural = ok && key == ""
		}
		if !structural && (!ok || !p.assetReadable(key, ref)) {
			delete(body, member)
		}
	}
	return body
}

// assetFacts projects embedded asset facts ({id, …}) of unknown scope.
func (p *projector) assetFacts(facts map[string]json.RawMessage) map[string]json.RawMessage {
	if !p.policy.Restricted(permission.KindAsset) {
		return facts
	}
	id, _, _ := stringMember(facts, "id")
	ref := permission.ObjectRef{ID: id, ClientID: unknownScope, SiteID: unknownScope}
	return p.assetObject(facts, &ref)
}

// asset is the rule of asset.*: the payload is the asset (id, client_id,
// site_id and its fields).
func (p *projector) asset(body map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	if !p.policy.Restricted(permission.KindAsset) {
		return nil, true
	}
	id, okID := requiredString(body, "id")
	client, _, okClient := stringMember(body, "client_id")
	site, _, okSite := stringMember(body, "site_id")
	if !okID || !okClient || !okSite {
		return nil, false
	}
	ref := permission.ObjectRef{ID: id, ClientID: client, SiteID: site}
	return p.assetObject(body, &ref), true
}

// assetChange is the rule of asset.changed (WP-740): the asset as asset,
// changed_fields only with readable fields; an event whose changed fields
// are all unreadable is withheld. custom_fields counts as readable while
// the projected asset keeps a custom field (the change names no single
// key).
func (p *projector) assetChange(body map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	if !p.policy.Restricted(permission.KindAsset) {
		return nil, true
	}
	var changed []string
	if raw, ok := body["changed_fields"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &changed) != nil {
			return nil, false
		}
	}
	delete(body, "changed_fields")
	out, ok := p.asset(body)
	if !ok || changed == nil {
		return out, ok
	}
	id, _ := requiredString(out, "id")
	client, _, _ := stringMember(out, "client_id")
	site, _, _ := stringMember(out, "site_id")
	ref := permission.ObjectRef{ID: id, ClientID: client, SiteID: site}
	custom, _, _ := objectMember(out, "custom_fields")
	kept := make([]string, 0, len(changed))
	for _, name := range changed {
		readable := false
		if name == "custom_fields" {
			readable = len(custom) > 0
		} else {
			key, structural, mapped := permission.FieldKeyOf(permission.KindAsset, name)
			readable = structural || (mapped && p.assetReadable(key, &ref))
		}
		if readable {
			kept = append(kept, name)
		}
	}
	if len(changed) > 0 && len(kept) == 0 {
		return nil, false
	}
	if !setMember(out, "changed_fields", kept) {
		return nil, false
	}
	return out, true
}

// embeddedAsset is the rule of inventory.*, stock.movement, reservation.*
// and composition.*: their payloads name assets by id; embedded asset
// facts (asset) follow the asset policy.
func (p *projector) embeddedAsset(body map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	facts, present, ok := objectMember(body, "asset")
	if !ok {
		return nil, false
	}
	if !present || !p.policy.Restricted(permission.KindAsset) {
		return nil, true
	}
	return body, setMember(body, "asset", p.assetFacts(facts))
}
