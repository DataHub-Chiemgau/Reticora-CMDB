// Package metamodel holds the metamodel drafts (WP-260, MET-53, API-06):
// the not yet published working state of metamodel changes of an
// organization as a list of typed items. Capturing an item records the
// state of its target (base) in the same transaction; testing (WP-721) and
// publishing (WP-261) follow. Without a draft the resource routes of the
// package citype stay productive (MET-04). citype does not import this
// package.
package metamodel

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Status of a draft. published and discarded are final.
const (
	StatusDraft     = "draft"
	StatusTested    = "tested"
	StatusPublished = "published"
	StatusDiscarded = "discarded"
)

// Kinds of draft items: the metamodel parts of MET-53.
const (
	KindCIType              = "ci_type"
	KindCITypeField         = "ci_type_field"
	KindGlobalField         = "global_field"
	KindAttributeDefinition = "attribute_definition"
	KindAttributeSet        = "attribute_set"
	KindTemplate            = "template"
	KindTemplateVersion     = "template_version"
	KindRule                = "rule"
	KindLayout              = "layout"
)

// Operations of draft items.
const (
	OpCreate     = "create"
	OpUpdate     = "update"
	OpUpsert     = "upsert"
	OpDelete     = "delete"
	OpClone      = "clone"
	OpActivate   = "activate"
	OpDeactivate = "deactivate"
	OpAssign     = "assign"
	OpPublish    = "publish"
)

// Draft is a metamodel draft (table metamodel_draft).
type Draft struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	CreatedBy      *string         `json:"created_by"`
	Items          []Item          `json:"items,omitempty"`
	ItemCount      int             `json:"item_count"`
	TestReport     json.RawMessage `json:"test_report"`
	Version        int64           `json:"version"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	PublishedAt    *time.Time      `json:"published_at"`
}

// Item is one typed operation of a draft. target_id is null for a new
// part (and for global fields, which target_key names); base is the state
// of the target at capture (null for a new part); schema_versions holds
// ci_type.schema_version of every directly affected type. route is
// informative only: WP-261 maps kind and op to repository calls.
type Item struct {
	ID             string           `json:"id"`
	Kind           string           `json:"kind"`
	Op             string           `json:"op"`
	TargetID       *string          `json:"target_id"`
	TargetKey      *string          `json:"target_key"`
	Payload        json.RawMessage  `json:"payload"`
	Base           json.RawMessage  `json:"base"`
	SchemaVersions map[string]int64 `json:"schema_versions"`
	Route          string           `json:"route"`
	CreatedBy      *string          `json:"created_by"`
	CreatedAt      time.Time        `json:"created_at"`
}

// DraftInput is the request of POST /api/v1/metamodel/drafts.
type DraftInput struct {
	Name string `json:"name"`
}

// DraftPatch is the request of PATCH /api/v1/metamodel/drafts/{id}.
type DraftPatch struct {
	Name *string `json:"name,omitempty"`
}

// ItemInput is the request of POST /api/v1/metamodel/drafts/{id}/items.
type ItemInput struct {
	Kind      string          `json:"kind"`
	Op        string          `json:"op"`
	TargetID  string          `json:"target_id,omitempty"`
	TargetKey string          `json:"target_key,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Payload types of the resource routes whose request is an anonymous
// struct in citype; the fields are those of the route.
type (
	// AttributeDefinitionCreate is the request of POST
	// /api/v1/attribute-definitions.
	AttributeDefinitionCreate struct {
		citype.AttributeDefinitionPatch
		CITypeID       string `json:"ci_type_id,omitempty"`
		AttributeSetID string `json:"attribute_set_id,omitempty"`
		Key            string `json:"key"`
	}
	// AttributeSetCreate is the request of POST /api/v1/attribute-sets.
	AttributeSetCreate struct {
		Key         string               `json:"key"`
		DisplayName citype.LocalizedText `json:"display_name,omitempty"`
		Description string               `json:"description,omitempty"`
		IsActive    *bool                `json:"is_active,omitempty"`
	}
	// TypeAttributeSetsInput is the request of PUT
	// /api/v1/ci-types/{id}/attribute-sets.
	TypeAttributeSetsInput struct {
		CITypeID      string                          `json:"ci_type_id,omitempty"`
		AttributeSets []citype.AttributeSetAssignment `json:"attribute_sets"`
	}
	// TemplateCreate is the request of POST /api/v1/templates.
	TemplateCreate struct {
		Key         string               `json:"key"`
		CITypeID    string               `json:"ci_type_id"`
		DisplayName citype.LocalizedText `json:"display_name,omitempty"`
		Description string               `json:"description,omitempty"`
		IsActive    *bool                `json:"is_active,omitempty"`
	}
	// TemplateVersionItemsInput is the request of PUT
	// /api/v1/templates/{id}/versions/{version}.
	TemplateVersionItemsInput struct {
		Items []citype.TemplateItemInput `json:"items"`
	}
	// noPayload is the empty request of the routes without a body
	// (activate, deactivate, delete, new template version, publish).
	noPayload struct{}
)

// ItemSpec describes one pair of kind and op: the resource route it
// stands for (method and path with {id}, {name} or {version}) and the
// request type its payload is decoded into.
type ItemSpec struct {
	Route   string
	payload func() any
}

func spec(route string, payload func() any) ItemSpec { return ItemSpec{Route: route, payload: payload} }

func none() any { return &noPayload{} }

// ItemKinds are the permitted pairs of kind and op.
var ItemKinds = map[string]map[string]ItemSpec{
	KindCIType: {
		OpCreate:     spec("POST /api/v1/ci-types", func() any { return &citype.CreateTypeRequest{} }),
		OpUpdate:     spec("PATCH /api/v1/ci-types/{id}", func() any { return &citype.UpdateTypeRequest{} }),
		OpClone:      spec("POST /api/v1/ci-types/{id}/clone", func() any { return &citype.CloneRequest{} }),
		OpActivate:   spec("POST /api/v1/ci-types/{id}/activate", none),
		OpDeactivate: spec("POST /api/v1/ci-types/{id}/deactivate", none),
		OpAssign:     spec("PUT /api/v1/ci-types/{id}/attribute-sets", func() any { return &TypeAttributeSetsInput{} }),
	},
	KindCITypeField: {
		OpUpsert: spec("PUT /api/v1/ci-types/{id}/fields", func() any { return &citype.UpsertFieldRequest{} }),
		OpDelete: spec("DELETE /api/v1/ci-types/{id}/fields/{name}", none),
	},
	KindGlobalField: {
		OpUpsert: spec("PUT /api/v1/ci-fields/global", func() any { return &citype.UpsertFieldRequest{} }),
		OpDelete: spec("DELETE /api/v1/ci-fields/global/{name}", none),
	},
	KindAttributeDefinition: {
		OpCreate: spec("POST /api/v1/attribute-definitions", func() any { return &AttributeDefinitionCreate{} }),
		OpUpdate: spec("PATCH /api/v1/attribute-definitions/{id}", func() any { return &citype.AttributeDefinitionPatch{} }),
		OpDelete: spec("DELETE /api/v1/attribute-definitions/{id}", none),
	},
	KindAttributeSet: {
		OpCreate: spec("POST /api/v1/attribute-sets", func() any { return &AttributeSetCreate{} }),
		OpUpdate: spec("PATCH /api/v1/attribute-sets/{id}", func() any { return &citype.AttributeSetPatch{} }),
		OpDelete: spec("DELETE /api/v1/attribute-sets/{id}", none),
	},
	KindTemplate: {
		OpCreate: spec("POST /api/v1/templates", func() any { return &TemplateCreate{} }),
		OpUpdate: spec("PATCH /api/v1/templates/{id}", func() any { return &citype.TemplatePatch{} }),
		OpDelete: spec("DELETE /api/v1/templates/{id}", none),
		OpClone:  spec("POST /api/v1/templates/{id}/clone", func() any { return &citype.TemplateClone{} }),
	},
	KindTemplateVersion: {
		OpCreate:  spec("POST /api/v1/templates/{id}/versions", none),
		OpUpdate:  spec("PUT /api/v1/templates/{id}/versions/{version}", func() any { return &TemplateVersionItemsInput{} }),
		OpPublish: spec("POST /api/v1/templates/{id}/versions/{version}/publish", none),
	},
	KindRule: {
		OpCreate: spec("POST /api/v1/rules", func() any { return &citype.RuleInput{} }),
		OpUpdate: spec("PATCH /api/v1/rules/{id}", func() any { return &citype.RulePatch{} }),
		OpDelete: spec("DELETE /api/v1/rules/{id}", none),
	},
	KindLayout: {
		OpCreate: spec("POST /api/v1/layouts", func() any { return &citype.LayoutInput{} }),
		OpUpdate: spec("PATCH /api/v1/layouts/{id}", func() any { return &citype.LayoutPatch{} }),
		OpDelete: spec("DELETE /api/v1/layouts/{id}", none),
	},
}

// ItemPermission is the write permission of the resource route of an
// item (testdata/route_permissions.txt): global fields and attribute
// definitions need ci_attribute:manage, publishing a template version
// metamodel:publish, everything else citype:manage (E-106, WP-363).
func ItemPermission(kind, op string) identity.Permission {
	switch {
	case kind == KindGlobalField || kind == KindAttributeDefinition:
		return identity.PermCIAttributeManage
	case kind == KindTemplateVersion && op == OpPublish:
		return identity.PermMetamodelPublish
	}
	return identity.PermCITypeManage
}

// Errors of the draft routes.
var (
	ErrDraftNotFound = errors.New("metamodel draft not found")
	ErrItemNotFound  = errors.New("draft item not found")
	// ErrDraftImmutable: published and discarded drafts cannot change (409).
	ErrDraftImmutable = errors.New("published and discarded drafts are immutable")
	// ErrVersionMismatch: If-Match names another version (412).
	ErrVersionMismatch = errors.New("the draft was changed since the If-Match version")
	// ErrTargetNotFound: the target of an item does not exist or is not
	// one the resource route changes (404).
	ErrTargetNotFound = errors.New("the target of the item was not found")
	// ErrTargetReadOnly: the target is a read-only system row (403).
	ErrTargetReadOnly = errors.New("the target of the item is a read-only system row")
	// ErrItemReferenced: another item of the draft references the item
	// (409).
	ErrItemReferenced = errors.New("another item of the draft references this item")
)

func fieldError(field, message string) error {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// checkName checks the name of a draft.
// validation: fixed texts.
func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 {
		return "", fieldError("name", "name must have 1 to 200 characters")
	}
	return name, nil
}

// capture is a checked item request: the spec of its pair, the target,
// the compact payload and the item references it carries.
type capture struct {
	Kind, Op  string
	Spec      ItemSpec
	TargetID  string
	TargetKey string
	Payload   json.RawMessage
	// Refs are the item ids of {"$item": "<id>"} in the payload.
	Refs []string
	// CITypeID is the ci_type_id of the payload when it names a type
	// directly (no item reference).
	CITypeID string
}

// decodeStrict decodes raw into v like api.ReadJSON: an unknown field or a
// value of the wrong type is an error.
// validation: the texts of encoding/json about the request payload, like
// api.ReadJSON.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("payload must be a single JSON value")
	}
	return nil
}

// checkItem checks kind, op, target and payload of an item request without
// the database: the pair must be in ItemKinds, the payload decodes
// strictly into the request type of the route (400 otherwise), item
// references {"$item": "<id>"} are collected and stand in for an id while
// decoding.
// validation: fixed texts and the decoding texts of decodeStrict.
func checkItem(in *ItemInput) (*capture, error) {
	ops, ok := ItemKinds[in.Kind]
	if !ok {
		return nil, fieldError("kind", "kind must be one of "+strings.Join(sortedKeys(ItemKinds), ", "))
	}
	sp, ok := ops[in.Op]
	if !ok {
		return nil, fieldError("op", "op of "+in.Kind+" must be one of "+strings.Join(sortedKeys(ops), ", "))
	}
	c := &capture{Kind: in.Kind, Op: in.Op, Spec: sp, TargetID: strings.TrimSpace(in.TargetID),
		TargetKey: strings.TrimSpace(in.TargetKey)}
	raw := bytes.TrimSpace(in.Payload)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, &api.FieldError{Field: "payload", Message: "payload must be a JSON object"}
	}
	if _, isObject := generic.(map[string]any); !isObject {
		return nil, &api.FieldError{Field: "payload", Message: "payload must be a JSON object"}
	}
	substituted, refs, err := substituteRefs(generic)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(substituted)
	if err != nil {
		return nil, &api.FieldError{Field: "payload", Message: "payload must be a JSON object"}
	}
	if derr := decodeStrict(plain, sp.payload()); derr != nil {
		return nil, &api.FieldError{Field: "payload", Message: derr.Error()}
	}
	var compact bytes.Buffer
	if err = json.Compact(&compact, raw); err != nil {
		return nil, &api.FieldError{Field: "payload", Message: "payload must be a JSON object"}
	}
	c.Payload, c.Refs = compact.Bytes(), refs
	if m, ok := generic.(map[string]any); ok {
		if id, isString := m["ci_type_id"].(string); isString && api.IsUUID(id) {
			c.CITypeID = id
		}
	}
	return c, checkTarget(c, substituted.(map[string]any))
}

// checkTarget checks target_id and target_key of an item: a new part has
// no target_id (a new template version names its template), every other
// operation names the target by id; fields are named by target_key (on
// upsert the name of the payload).
// validation: fixed texts.
func checkTarget(c *capture, payload map[string]any) error {
	needsID := c.Op != OpCreate || c.Kind == KindTemplateVersion
	if c.Kind == KindGlobalField {
		needsID = false
	}
	switch {
	case needsID && c.TargetID == "":
		return fieldError("target_id", "target_id is required for "+c.Kind+"/"+c.Op)
	case !needsID && c.TargetID != "":
		return fieldError("target_id", "target_id must be empty for "+c.Kind+"/"+c.Op)
	}
	if c.Kind != KindCITypeField && c.Kind != KindGlobalField {
		if c.TargetKey != "" {
			return fieldError("target_key", "target_key names fields only")
		}
		return nil
	}
	if c.Op == OpUpsert {
		name, _ := payload["name"].(string)
		name = strings.TrimSpace(name)
		switch {
		case name == "":
			return fieldError("payload.name", "name is required")
		case c.TargetKey != "" && c.TargetKey != name:
			return fieldError("target_key", "target_key differs from the name of the payload")
		}
		c.TargetKey = name
	}
	if c.TargetKey == "" {
		return fieldError("target_key", "target_key is required for "+c.Kind+"/"+c.Op)
	}
	return nil
}

// substituteRefs returns a copy of v in which every item reference
// {"$item": "<id>"} is replaced by its id, and the referenced ids.
// validation: fixed texts.
func substituteRefs(v any) (out any, refs []string, err error) {
	var walk func(v any) (any, error)
	walk = func(v any) (any, error) {
		switch t := v.(type) {
		case map[string]any:
			if ref, isRef := t["$item"]; isRef {
				id, isString := ref.(string)
				if len(t) != 1 || !isString || !api.IsUUID(id) {
					return nil, fieldError("payload", `an item reference is an object {"$item": "<item id>"} with no other key`)
				}
				refs = append(refs, id)
				return id, nil
			}
			out := make(map[string]any, len(t))
			for k, child := range t {
				w, walkErr := walk(child)
				if walkErr != nil {
					return nil, walkErr
				}
				out[k] = w
			}
			return out, nil
		case []any:
			out := make([]any, len(t))
			for i, child := range t {
				w, walkErr := walk(child)
				if walkErr != nil {
					return nil, walkErr
				}
				out[i] = w
			}
			return out, nil
		}
		return v, nil
	}
	out, err = walk(v)
	return out, refs, err
}

// itemRefs returns the item ids an item's payload references.
func itemRefs(payload json.RawMessage) []string {
	if len(payload) == 0 {
		return nil
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if dec.Decode(&generic) != nil {
		return nil
	}
	_, refs, _ := substituteRefs(generic)
	return refs
}

// checkRefs checks the item references of a new item against the items of
// its draft (E-112): each names an item of the draft that creates a part
// (create or clone), and the references form no cycle.
// validation: fixed texts.
func checkRefs(items []Item, newID string, refs []string) error {
	byID := make(map[string]*Item, len(items))
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	for _, ref := range refs {
		target, ok := byID[ref]
		switch {
		case ref == newID:
			return fieldError("payload", "an item cannot reference itself")
		case !ok:
			return fieldError("payload", "an item reference must name an item of the same draft")
		case target.Op != OpCreate && target.Op != OpClone:
			return fieldError("payload", "an item reference must name an item that creates a part (create or clone)")
		}
	}
	edges := map[string][]string{newID: refs}
	for i := range items {
		edges[items[i].ID] = itemRefs(items[i].Payload)
	}
	const (
		active = iota + 1
		done
	)
	state := map[string]int{}
	var visit func(id string) bool
	visit = func(id string) bool {
		switch state[id] {
		case active:
			return false
		case done:
			return true
		}
		state[id] = active
		for _, next := range edges[id] {
			if !visit(next) {
				return false
			}
		}
		state[id] = done
		return true
	}
	if !visit(newID) {
		return fieldError("payload", "the item references form a cycle")
	}
	return nil
}

// route fills the path of a spec route.
func route(template string, values ...string) string {
	out := template
	for i := 0; i+1 < len(values); i += 2 {
		out = strings.Replace(out, values[i], values[i+1], 1)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
