// Package citype implements the flexible, metadata-driven CI type system:
// extended CI type CRUD (clone/deactivate/version), field definitions at the
// type and global scope, CI-instance field definitions, and the built-in
// type templates. Validation of field definitions and values delegates to the
// shared fieldmeta registry so all three attribute scopes behave identically.
package citype

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// Type is a CI type definition including its extension metadata (spec §1).
type Type struct {
	ID             string        `json:"id"`
	OrganizationID string        `json:"organization_id,omitempty"`
	Key            string        `json:"key"`
	Name           string        `json:"name"`
	DisplayName    LocalizedText `json:"display_name"`
	Icon           string        `json:"icon,omitempty"`
	Description    string        `json:"description,omitempty"`
	Category       string        `json:"category,omitempty"`
	IsBuiltin      bool          `json:"is_builtin"`
	IsSystem       bool          `json:"is_system"`
	IsActive       bool          `json:"is_active"`
	IsLogical      bool          `json:"is_logical"`
	// IsObservable: the offline detector watches CIs of the type (LCY-05);
	// true for physical, false for logical types unless set (MET-01).
	IsObservable bool `json:"is_observable"`
	// AttributeSchema (JSON Schema 2020-12) and RequiredFields are generated
	// from the type's attribute definitions; they are read-only (DB-03).
	AttributeSchema map[string]any `json:"attribute_schema,omitempty"`
	RequiredFields  []string       `json:"required_fields"`
	// SchemaVersion is the version of the generated attribute schema.
	SchemaVersion int    `json:"schema_version"`
	Version       int    `json:"version"`
	ClonedFromID  string `json:"cloned_from_id,omitempty"`
	// ParentTypeID is the parent in the type chain (MET-06): a type of the
	// organization or a system type; empty for a root.
	ParentTypeID string `json:"parent_type_id,omitempty"`
	TemplateKey  string `json:"template_key,omitempty"`
	// LifecycleModelID is the lifecycle model of the type (MET-01); a
	// subtype without one inherits it along the type chain (MET-06).
	LifecycleModelID string `json:"lifecycle_model_id,omitempty"`
	// LifecycleDefinitionID mirrors LifecycleModelID (API-01, deprecated).
	LifecycleDefinitionID    string         `json:"lifecycle_definition_id,omitempty"`
	Capabilities             map[string]any `json:"capabilities"`
	AllowedRelationshipTypes []string       `json:"allowed_relationship_types"`
	UISchema                 map[string]any `json:"ui_schema"`
	ComplianceRules          []any          `json:"compliance_rules"`
	DiscoveryMappings        []any          `json:"discovery_mappings"`
	Fields                   []Field        `json:"fields,omitempty"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
}

// Field is a field definition on a CI type (scope type) or globally.
type Field struct {
	ID              string                      `json:"id,omitempty"`
	CITypeID        string                      `json:"ci_type_id,omitempty"`
	Scope           string                      `json:"scope"` // global | type
	Name            string                      `json:"name"`
	Label           string                      `json:"label,omitempty"`
	Description     string                      `json:"description,omitempty"`
	DataType        string                      `json:"data_type"`
	Required        bool                        `json:"required"`
	DefaultValue    string                      `json:"default_value,omitempty"`
	EnumValues      []string                    `json:"enum_values,omitempty"`
	UIGroup         string                      `json:"ui_group,omitempty"`
	SortOrder       int                         `json:"sort_order"`
	Validation      *fieldmeta.ValidationRules  `json:"validation,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string                      `json:"reference_target,omitempty"`
	// IsSystem marks a system definition (no organization), the base
	// layer of the composition (MET-16).
	IsSystem bool `json:"is_system,omitempty"`
	// MultiValue is attribute_definition.multi_value; nil when unknown. The
	// legacy field views do not show it.
	MultiValue *bool `json:"-"`
	// Inheritable: subtypes inherit the definition (MET-06); the type's own
	// definitions apply to it either way.
	Inheritable bool `json:"inheritable"`
	// AppliesTo and its lists are the applicability of a global field
	// (MET-13, WP-255; empty mode: all), read-only in the legacy view and
	// absent for type fields. ResolveFields takes an organization's global
	// field only for the types it applies to.
	AppliesTo           string   `json:"applies_to,omitempty"`
	AppliesToCategories []string `json:"applies_to_categories,omitempty"`
	AppliesToTypeIDs    []string `json:"applies_to_type_ids,omitempty"`
}

// InstanceField is a field definition scoped to a single CI instance (spec §2).
type InstanceField struct {
	ID              string                      `json:"id,omitempty"`
	OrganizationID  string                      `json:"organization_id,omitempty"`
	CIID            string                      `json:"ci_id"`
	Name            string                      `json:"name"`
	Label           string                      `json:"label,omitempty"`
	Description     string                      `json:"description,omitempty"`
	DataType        string                      `json:"data_type"`
	Required        bool                        `json:"required"`
	DefaultValue    string                      `json:"default_value,omitempty"`
	EnumValues      []string                    `json:"enum_values,omitempty"`
	UIGroup         string                      `json:"ui_group,omitempty"`
	SortOrder       int                         `json:"sort_order"`
	Validation      *fieldmeta.ValidationRules  `json:"validation,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string                      `json:"reference_target,omitempty"`
	CreatedAt       time.Time                   `json:"created_at"`
	UpdatedAt       time.Time                   `json:"updated_at"`
}

// Definition converts a type/global field into the shared fieldmeta shape.
func (f Field) Definition() fieldmeta.FieldDefinition {
	return fieldmeta.FieldDefinition{
		Name:            f.Name,
		Label:           f.Label,
		Description:     f.Description,
		DataType:        f.DataType,
		Required:        f.Required,
		DefaultValue:    f.DefaultValue,
		EnumValues:      f.EnumValues,
		UIGroup:         f.UIGroup,
		SortOrder:       f.SortOrder,
		Validation:      f.Validation,
		Conditional:     f.Conditional,
		ReferenceTarget: f.ReferenceTarget,
		MultiValue:      f.MultiValue,
	}
}

// Definition converts an instance field into the shared fieldmeta shape;
// MultiValue stays nil (ci_instance_field_definition has no multi_value),
// the composition takes it from the layer below.
func (f InstanceField) Definition() fieldmeta.FieldDefinition {
	return fieldmeta.FieldDefinition{
		Name:            f.Name,
		Label:           f.Label,
		Description:     f.Description,
		DataType:        f.DataType,
		Required:        f.Required,
		DefaultValue:    f.DefaultValue,
		EnumValues:      f.EnumValues,
		UIGroup:         f.UIGroup,
		SortOrder:       f.SortOrder,
		Validation:      f.Validation,
		Conditional:     f.Conditional,
		ReferenceTarget: f.ReferenceTarget,
	}
}

// CreateTypeRequest is the payload for creating a CI type.
type CreateTypeRequest struct {
	Key         string        `json:"key"`
	Name        string        `json:"name"`
	DisplayName LocalizedText `json:"display_name,omitempty"`
	Icon        string        `json:"icon,omitempty"`
	Description string        `json:"description,omitempty"`
	Category    string        `json:"category,omitempty"`
	IsLogical   bool          `json:"is_logical,omitempty"`
	// IsObservable defaults to !IsLogical.
	IsObservable *bool `json:"is_observable,omitempty"`
	// ParentTypeID makes the type a subtype (MET-06).
	ParentTypeID     string `json:"parent_type_id,omitempty"`
	TemplateKey      string `json:"template_key,omitempty"`
	LifecycleModelID string `json:"lifecycle_model_id,omitempty"`
	// LifecycleDefinitionID is the deprecated name of LifecycleModelID
	// (API-01); a different value of both is 422.
	LifecycleDefinitionID    string         `json:"lifecycle_definition_id,omitempty"`
	Capabilities             map[string]any `json:"capabilities,omitempty"`
	AllowedRelationshipTypes []string       `json:"allowed_relationship_types,omitempty"`
	UISchema                 map[string]any `json:"ui_schema,omitempty"`
	ComplianceRules          []any          `json:"compliance_rules,omitempty"`
	DiscoveryMappings        []any          `json:"discovery_mappings,omitempty"`
	Fields                   []Field        `json:"fields,omitempty"`
}

// UpdateTypeRequest is the payload for updating a CI type. Setting
// BreaksExistingValidation requests a new version instead of an in-place edit
// when existing CIs would fail the tightened validation.
type UpdateTypeRequest struct {
	Name         *string       `json:"name,omitempty"`
	DisplayName  LocalizedText `json:"display_name,omitempty"`
	Icon         *string       `json:"icon,omitempty"`
	Description  *string       `json:"description,omitempty"`
	Category     *string       `json:"category,omitempty"`
	IsLogical    *bool         `json:"is_logical,omitempty"`
	IsObservable *bool         `json:"is_observable,omitempty"`
	// ParentTypeID moves the type below another one; "" makes it a root
	// (MET-06).
	ParentTypeID     *string `json:"parent_type_id,omitempty"`
	LifecycleModelID *string `json:"lifecycle_model_id,omitempty"`
	// LifecycleDefinitionID is the deprecated name of LifecycleModelID
	// (API-01); a different value of both is 422.
	LifecycleDefinitionID    *string        `json:"lifecycle_definition_id,omitempty"`
	Capabilities             map[string]any `json:"capabilities,omitempty"`
	AllowedRelationshipTypes []string       `json:"allowed_relationship_types,omitempty"`
	UISchema                 map[string]any `json:"ui_schema,omitempty"`
	ComplianceRules          []any          `json:"compliance_rules,omitempty"`
	DiscoveryMappings        []any          `json:"discovery_mappings,omitempty"`
}

// CloneRequest is the payload for cloning a CI type.
type CloneRequest struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// UpsertFieldRequest is the payload for creating or updating a field
// definition on a type (or globally when Scope is "global").
type UpsertFieldRequest struct {
	Scope           string                      `json:"scope,omitempty"`
	Name            string                      `json:"name"`
	Label           string                      `json:"label,omitempty"`
	Description     string                      `json:"description,omitempty"`
	DataType        string                      `json:"data_type"`
	Required        bool                        `json:"required,omitempty"`
	DefaultValue    string                      `json:"default_value,omitempty"`
	EnumValues      []string                    `json:"enum_values,omitempty"`
	UIGroup         string                      `json:"ui_group,omitempty"`
	SortOrder       int                         `json:"sort_order,omitempty"`
	Validation      *fieldmeta.ValidationRules  `json:"validation,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string                      `json:"reference_target,omitempty"`
}

// UpsertInstanceFieldRequest is the payload for creating or updating a field
// definition on a single CI instance.
type UpsertInstanceFieldRequest struct {
	Name            string                      `json:"name"`
	Label           string                      `json:"label,omitempty"`
	Description     string                      `json:"description,omitempty"`
	DataType        string                      `json:"data_type"`
	Required        bool                        `json:"required,omitempty"`
	DefaultValue    string                      `json:"default_value,omitempty"`
	EnumValues      []string                    `json:"enum_values,omitempty"`
	UIGroup         string                      `json:"ui_group,omitempty"`
	SortOrder       int                         `json:"sort_order,omitempty"`
	Validation      *fieldmeta.ValidationRules  `json:"validation,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string                      `json:"reference_target,omitempty"`
}

// FilterParams holds query filter parameters for listing CI types.
type FilterParams struct {
	IncludeInactive bool
	Category        string
	Search          string
}

// Languages of a LocalizedText (MET-01).
var Languages = []string{"de", "en"}

// LocalizedText is a label per language, {"de": …, "en": …} (MET-01). A
// plain JSON string is accepted for compatibility and used for every
// language.
type LocalizedText map[string]string

// UnmarshalJSON accepts an object of languages or a plain string.
func (l *LocalizedText) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			*l = nil
			return nil
		}
		*l = LocalizedText{"de": s, "en": s}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return errors.New("display_name must be an object of languages (de, en) or a string")
	}
	*l = m
	return nil
}

// Validate checks the languages and lengths; an empty text is valid (the
// name is used).
// validation: fixed texts naming the language.
func (l LocalizedText) Validate() error {
	for lang, text := range l {
		if lang != "de" && lang != "en" {
			return errors.New("display_name supports the languages " + strings.Join(Languages, ", "))
		}
		if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 200 {
			return errors.New("display_name." + lang + " must have 1 to 200 characters")
		}
	}
	return nil
}

// WithFallback fills missing languages from the other language or name.
func (l LocalizedText) WithFallback(name string) LocalizedText {
	out := LocalizedText{}
	for _, lang := range Languages {
		out[lang] = strings.TrimSpace(l[lang])
	}
	for _, lang := range Languages {
		if out[lang] != "" {
			continue
		}
		for _, other := range Languages {
			if out[other] != "" {
				out[lang] = out[other]
				break
			}
		}
		if out[lang] == "" {
			out[lang] = name
		}
	}
	return out
}

// Contains reports whether any language contains sub (case-insensitive).
func (l LocalizedText) Contains(sub string) bool {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.Contains(strings.ToLower(l[k]), strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// MaxTypeChainDepth is the number of types a chain root → leaf holds at
// most; the root is level 1 (MET-06).
const MaxTypeChainDepth = 6

// Errors of the parent of a type (MET-06), reported on parent_type_id: an
// unknown parent and one of another organization alike (422), a cycle
// (409), a chain deeper than MaxTypeChainDepth (422).
// validation: fixed texts; the composition text names the attribute key.
func errParentUnknown() error {
	return &api.FieldError{Field: "parent_type_id", Message: "parent ci type not found", Status: http.StatusUnprocessableEntity}
}

func errParentSystem() error {
	return &api.FieldError{Field: "parent_type_id", Message: "a system ci type has only system ci types as parent",
		Status: http.StatusUnprocessableEntity}
}

func errParentCycle() error {
	return &api.ConflictError{Field: "parent_type_id", Message: "the parent ci type would make the type its own ancestor"}
}

func errParentDepth() error {
	return &api.FieldError{Field: "parent_type_id",
		Message: fmt.Sprintf("a ci type chain has at most %d levels", MaxTypeChainDepth), Status: http.StatusUnprocessableEntity}
}

// errTypeComposition reports definitions of a type's subtree that cannot
// be composed (MET-16) after a change of field: parent_type_id (the moved
// chain), or category, which changes the organization's global attributes
// that apply (WP-255); the message names the attribute key.
func errTypeComposition(field, msg, code string) error {
	return &api.FieldError{Field: field, Message: msg, Status: http.StatusUnprocessableEntity, Code: code}
}

// Errors of the lifecycle model of a type (MET-01), reported on
// lifecycle_model_id (422): the deprecated field lifecycle_definition_id
// with another value, and a model that is neither one of the organization
// nor a system model.
// validation: fixed texts.
func errLifecycleModelMismatch() error {
	return &api.FieldError{Field: "lifecycle_model_id",
		Message: "lifecycle_model_id and lifecycle_definition_id name different lifecycle models",
		Status:  http.StatusUnprocessableEntity}
}

func errLifecycleModelUnknown() error {
	return &api.FieldError{Field: "lifecycle_model_id", Message: "lifecycle model not found",
		Status: http.StatusUnprocessableEntity}
}

// LifecycleModel returns the requested lifecycle model: lifecycle_model_id
// or its deprecated name lifecycle_definition_id (API-01); both with
// different values are a 422 on lifecycle_model_id.
func (r *CreateTypeRequest) LifecycleModel() (string, error) {
	switch {
	case r.LifecycleModelID == "":
		return r.LifecycleDefinitionID, nil
	case r.LifecycleDefinitionID != "" && r.LifecycleDefinitionID != r.LifecycleModelID:
		return "", errLifecycleModelMismatch()
	}
	return r.LifecycleModelID, nil
}

// LifecycleModel returns the requested change of the lifecycle model (nil:
// unchanged, "": none), as CreateTypeRequest.LifecycleModel.
func (r *UpdateTypeRequest) LifecycleModel() (*string, error) {
	switch {
	case r.LifecycleModelID == nil:
		return r.LifecycleDefinitionID, nil
	case r.LifecycleDefinitionID != nil && *r.LifecycleDefinitionID != *r.LifecycleModelID:
		return nil, errLifecycleModelMismatch()
	}
	return r.LifecycleModelID, nil
}

// mirrorLifecycle fills the deprecated lifecycle_definition_id with
// lifecycle_model_id (API-01).
func (t *Type) mirrorLifecycle() {
	t.LifecycleDefinitionID = t.LifecycleModelID
}

// LifecycleStateCount is the number of CIs in one lifecycle state.
type LifecycleStateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

// LifecycleStatesConflict refuses a change of lifecycle_model_id or
// parent_type_id after which CIs of the type or of its subtypes carry
// lifecycle states the new effective ci model does not contain (409 with
// the number of CIs per state, MET-06, LCY-02).
type LifecycleStatesConflict struct {
	Field  string
	Counts []LifecycleStateCount
}

// lifecycleStatesConflictDetail is the fixed text of LifecycleStatesConflict.
const lifecycleStatesConflictDetail = "CIs of the type or of its subtypes have lifecycle states the new lifecycle model does not contain; " +
	"move them to a state of the new model first"

// validation: the field and a fixed text.
func (e *LifecycleStatesConflict) Error() string {
	return e.Field + ": " + lifecycleStatesConflictDetail
}

// observableDefault is the MET-01 default of is_observable.
func observableDefault(explicit *bool, logical bool) bool {
	if explicit != nil {
		return *explicit
	}
	return !logical
}
