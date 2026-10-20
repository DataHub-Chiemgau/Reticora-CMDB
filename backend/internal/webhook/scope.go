package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// ObjectScope is the client and the site of one object an event is about;
// "" is NULL. Like RLS (USING of migrations 000058 and 000063) a row without
// client or site is readable by client- and site-scoped readers.
type ObjectScope struct {
	ClientID string
	SiteID   string
}

// ObjectKind is the table of an object an event references by ID.
type ObjectKind string

// The object kinds scope resolution reads.
const (
	ObjectCI           ObjectKind = "ci"
	ObjectAsset        ObjectKind = "asset"
	ObjectQuantityItem ObjectKind = "quantity_item"
)

// ObjectRef is an object an event payload references by ID.
type ObjectRef struct {
	Kind ObjectKind
	ID   string
}

// ObjectResolver reads the client and site of the referenced objects of an
// organization, one ObjectScope per reference. A reference it cannot read
// fails the whole resolution (ErrObjectUnresolved).
type ObjectResolver interface {
	ResolveObjects(ctx context.Context, orgID string, refs []ObjectRef) ([]ObjectScope, error)
}

// ErrObjectUnresolved is returned for a reference that names no object of
// the organization.
var ErrObjectUnresolved = errors.New("event object not found")

// refField is a payload field holding the ID of an event object.
type refField struct {
	field    string
	kind     ObjectKind
	optional bool
}

// scopeRule is the source of the object scope of an event type (RBA-08).
// The zero rule marks a type whose scope cannot be resolved: it reaches only
// unbound subscriptions and accounts that read org-wide.
type scopeRule struct {
	// payload: the payload's top-level client_id/site_id, when it carries
	// either, is the scope of the event's object (a missing one is NULL).
	payload bool
	// refs are the objects of the event; every present ID must resolve and
	// a field that is not optional must be present.
	refs []refField
	// orgWide: without any of refs the event is about metadata of the
	// organization (CI types, attribute definitions, source policies),
	// which every reader of the permission may read. The payload must name
	// the object (id or name); another payload under such a type, e.g. of
	// a workflow's send_webhook action, is not resolvable.
	orgWide bool
}

var (
	ciByID          = scopeRule{payload: true, refs: []refField{{field: "id", kind: ObjectCI}}}
	ciRelationship  = scopeRule{refs: []refField{{field: "source_ci_id", kind: ObjectCI}, {field: "target_ci_id", kind: ObjectCI}}}
	organizational  = scopeRule{orgWide: true}
	inventoryObject = scopeRule{refs: []refField{
		{field: "asset_id", kind: ObjectAsset, optional: true},
		{field: "quantity_item_id", kind: ObjectQuantityItem, optional: true},
	}}
	notResolvable = scopeRule{}
)

// scopeRules holds the scope source of every subscribable event type that
// EventReadPermission assigns a read permission (TestEventScopeResolution).
var scopeRules = map[string]scopeRule{
	"ci.created":        ciByID,
	"ci.updated":        ciByID,
	"ci.changed":        ciByID,
	"ci.deleted":        ciByID,
	"ci.restored":       ciByID,
	"ci.status_changed": ciByID,
	"ci.offline":        ciByID,
	"ci.online":         ciByID,
	// Survivor and victim; the victim is soft-deleted (ci/merge.go).
	"ci.merged": {refs: []refField{{field: "survivor_id", kind: ObjectCI}, {field: "victim_id", kind: ObjectCI}}},

	"relationship.created": ciRelationship,
	"relationship.removed": ciRelationship,
	"relationship.deleted": ciRelationship,

	"ci_type.created":     organizational,
	"ci_type.updated":     organizational,
	"ci_type.deactivated": organizational,
	// Type and global fields and attribute definitions belong to the
	// organization; an instance field carries the ID of its CI.
	"ci_attribute.changed":          {refs: []refField{{field: "ci_id", kind: ObjectCI, optional: true}}, orgWide: true},
	"override.changed":              {refs: []refField{{field: "ci_id", kind: ObjectCI}}},
	"reconciliation.policy_changed": organizational,

	"inventory.movement":   inventoryObject,
	"reservation.created":  inventoryObject,
	"reservation.released": inventoryObject,
	// composition.deleted carries only the ID of the deleted link and is
	// therefore not resolvable.
	"composition.created": {refs: []refField{
		{field: "parent_asset_id", kind: ObjectAsset},
		{field: "child_ci_id", kind: ObjectCI, optional: true},
		{field: "child_asset_id", kind: ObjectAsset, optional: true},
	}},
	"composition.updated": {refs: []refField{
		{field: "parent_asset_id", kind: ObjectAsset},
		{field: "child_ci_id", kind: ObjectCI, optional: true},
		{field: "child_asset_id", kind: ObjectAsset, optional: true},
	}},
	"composition.deleted": notResolvable,

	// No emitter defines their payload: not resolvable.
	"lifecycle.transitioned": notResolvable,
	"asset.movement":         notResolvable,
	"discovery.completed":    notResolvable,
}

// eventObjects returns the scope of every object of the event, nil when it
// cannot be resolved (unknown type or payload form, missing object, read
// error).
func (d *Dispatcher) eventObjects(ctx context.Context, orgID, event string, body []byte) []ObjectScope {
	objects, refs := payloadObjects(event, body)
	if refs == nil {
		return objects
	}
	if d.opts.Objects == nil {
		return nil
	}
	resolved, err := d.opts.Objects.ResolveObjects(ctx, orgID, refs)
	if err != nil {
		slog.Warn("resolve webhook event scope failed", "error", err, "organization_id", orgID, "event", event)
		return nil
	}
	return resolved
}

// payloadObjects applies the scope rule of event to the payload: either the
// object scopes it determines without a lookup, or the references to
// resolve. Both nil: the scope is unknown.
func payloadObjects(event string, body []byte) (objects []ObjectScope, refs []ObjectRef) {
	rule, ok := scopeRules[event]
	if !ok {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return nil, nil
	}
	if rule.payload {
		_, hasClient := fields["client_id"]
		_, hasSite := fields["site_id"]
		if hasClient || hasSite {
			clientID, okClient := optionalString(fields, "client_id")
			siteID, okSite := optionalString(fields, "site_id")
			if !okClient || !okSite {
				return nil, nil
			}
			return []ObjectScope{{ClientID: clientID, SiteID: siteID}}, nil
		}
	}
	for _, ref := range rule.refs {
		raw, present := fields[ref.field]
		if !present {
			if ref.optional {
				continue
			}
			return nil, nil
		}
		var id string
		if json.Unmarshal(raw, &id) != nil || id == "" {
			return nil, nil
		}
		refs = append(refs, ObjectRef{Kind: ref.kind, ID: id})
	}
	if len(refs) == 0 {
		if rule.orgWide && (nonEmptyString(fields, "id") || nonEmptyString(fields, "name")) {
			return []ObjectScope{{}}, nil
		}
		return nil, nil
	}
	return nil, refs
}

// nonEmptyString reports whether the field is a non-empty string.
func nonEmptyString(fields map[string]json.RawMessage, name string) bool {
	var s string
	return json.Unmarshal(fields[name], &s) == nil && s != ""
}

// optionalString reads a string or null field; a missing field is "".
func optionalString(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := fields[name]
	if !ok || string(raw) == "null" {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// objectQueries read id, client and site of the rows of a kind, deleted_at
// ignored: a deleted CI keeps the scope its events were about.
var objectQueries = map[ObjectKind]string{
	ObjectCI: `SELECT id::text, COALESCE(client_id::text, ''), COALESCE(site_id::text, '')
		FROM ci WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
	ObjectAsset: `SELECT id::text, COALESCE(client_id::text, ''), ''
		FROM asset WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
	ObjectQuantityItem: `SELECT id::text, COALESCE(client_id::text, ''), ''
		FROM quantity_item WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
}

// mergeVictimQuery reads the scope of merged CIs that left table ci from the
// snapshot of their last merge.
const mergeVictimQuery = `SELECT DISTINCT ON (victim_id) victim_id::text,
		COALESCE(snapshot->'victim'->>'client_id', ''), COALESCE(snapshot->'victim'->>'site_id', '')
	FROM ci_merge WHERE organization_id = $1 AND victim_id = ANY($2::uuid[]) AND jsonb_typeof(snapshot->'victim') = 'object'
	ORDER BY victim_id, merged_at DESC`

// PGObjectResolver resolves event objects in PostgreSQL, in an org-wide
// tenant transaction of the organization (database.WithTenant).
type PGObjectResolver struct {
	pool *pgxpool.Pool
}

// NewPGObjectResolver creates the resolver.
func NewPGObjectResolver(pool *pgxpool.Pool) *PGObjectResolver {
	return &PGObjectResolver{pool: pool}
}

// ResolveObjects implements ObjectResolver.
func (r *PGObjectResolver) ResolveObjects(ctx context.Context, orgID string, refs []ObjectRef) ([]ObjectScope, error) {
	ids := map[ObjectKind][]string{}
	for _, ref := range refs {
		if _, ok := objectQueries[ref.Kind]; !ok || !uuidPattern.MatchString(ref.ID) {
			return nil, fmt.Errorf("%w: %s %q", ErrObjectUnresolved, ref.Kind, ref.ID)
		}
		ids[ref.Kind] = append(ids[ref.Kind], strings.ToLower(ref.ID))
	}
	found := map[ObjectRef]ObjectScope{}
	read := func(ctx context.Context, tx pgx.Tx, kind ObjectKind, query string, list []string) error {
		rows, err := tx.Query(ctx, query, orgID, list)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var s ObjectScope
			if err := rows.Scan(&id, &s.ClientID, &s.SiteID); err != nil {
				return err
			}
			found[ObjectRef{Kind: kind, ID: id}] = s
		}
		return rows.Err()
	}
	// An own transaction: a failed read never aborts the caller's (the
	// outbox publisher's) transaction.
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(database.WithoutTx(ctx), r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		for kind, list := range ids {
			if err := read(ctx, tx, kind, objectQueries[kind], list); err != nil {
				return err
			}
		}
		var victims []string
		for _, id := range ids[ObjectCI] {
			if _, ok := found[ObjectRef{Kind: ObjectCI, ID: id}]; !ok {
				victims = append(victims, id)
			}
		}
		if len(victims) == 0 {
			return nil
		}
		return read(ctx, tx, ObjectCI, mergeVictimQuery, victims)
	})
	if err != nil {
		return nil, fmt.Errorf("resolve event objects: %w", err)
	}
	out := make([]ObjectScope, 0, len(refs))
	for _, ref := range refs {
		s, ok := found[ObjectRef{Kind: ref.Kind, ID: strings.ToLower(ref.ID)}]
		if !ok {
			return nil, fmt.Errorf("%w: %s %s", ErrObjectUnresolved, ref.Kind, ref.ID)
		}
		out = append(out, s)
	}
	return out, nil
}
