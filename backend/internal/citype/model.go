// Package citype implements the flexible, metadata-driven CI type system:
// extended CI type CRUD (clone/deactivate/version), field definitions at the
// type and global scope, CI-instance field definitions, and the built-in
// type templates. Validation of field definitions and values delegates to the
// shared fieldmeta registry so all three attribute scopes behave identically.
package citype

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

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
	SchemaVersion            int            `json:"schema_version"`
	Version                  int            `json:"version"`
	ClonedFromID             string         `json:"cloned_from_id,omitempty"`
	TemplateKey              string         `json:"template_key,omitempty"`
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
	}
}

// Definition converts an instance field into the shared fieldmeta shape.
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
	IsObservable             *bool          `json:"is_observable,omitempty"`
	TemplateKey              string         `json:"template_key,omitempty"`
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
	Name                     *string        `json:"name,omitempty"`
	DisplayName              LocalizedText  `json:"display_name,omitempty"`
	Icon                     *string        `json:"icon,omitempty"`
	Description              *string        `json:"description,omitempty"`
	Category                 *string        `json:"category,omitempty"`
	IsLogical                *bool          `json:"is_logical,omitempty"`
	IsObservable             *bool          `json:"is_observable,omitempty"`
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

// observableDefault is the MET-01 default of is_observable.
func observableDefault(explicit *bool, logical bool) bool {
	if explicit != nil {
		return *explicit
	}
	return !logical
}
