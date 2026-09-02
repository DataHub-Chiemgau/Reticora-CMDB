// Package citype implements the flexible, metadata-driven CI type system:
// extended CI type CRUD (clone/deactivate/version), field definitions at the
// type and global scope, CI-instance field definitions, and the built-in
// type templates. Validation of field definitions and values delegates to the
// shared fieldmeta registry so all three attribute scopes behave identically.
package citype

import (
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// Type is a CI type definition including its extension metadata (spec §1).
type Type struct {
	ID                       string         `json:"id"`
	OrganizationID           string         `json:"organization_id,omitempty"`
	Key                      string         `json:"key"`
	Name                     string         `json:"name"`
	DisplayName              string         `json:"display_name,omitempty"`
	Icon                     string         `json:"icon,omitempty"`
	Description              string         `json:"description,omitempty"`
	Category                 string         `json:"category,omitempty"`
	IsBuiltin                bool           `json:"is_builtin"`
	IsSystem                 bool           `json:"is_system"`
	IsActive                 bool           `json:"is_active"`
	IsLogical                bool           `json:"is_logical"`
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
	ID              string                    `json:"id,omitempty"`
	CITypeID        string                    `json:"ci_type_id,omitempty"`
	Scope           string                    `json:"scope"` // global | type
	Name            string                    `json:"name"`
	Label           string                    `json:"label,omitempty"`
	Description     string                    `json:"description,omitempty"`
	DataType        string                    `json:"data_type"`
	Required        bool                      `json:"required"`
	DefaultValue    string                    `json:"default_value,omitempty"`
	EnumValues      []string                  `json:"enum_values,omitempty"`
	UIGroup         string                    `json:"ui_group,omitempty"`
	SortOrder       int                       `json:"sort_order"`
	Validation      *fieldmeta.ValidationRules  `json:"validation,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string                    `json:"reference_target,omitempty"`
}

// InstanceField is a field definition scoped to a single CI instance (spec §2).
type InstanceField struct {
	ID              string                    `json:"id,omitempty"`
	OrganizationID  string                    `json:"organization_id,omitempty"`
	CIID            string                    `json:"ci_id"`
	Name            string                    `json:"name"`
	Label           string                    `json:"label,omitempty"`
	Description     string                    `json:"description,omitempty"`
	DataType        string                    `json:"data_type"`
	Required        bool                      `json:"required"`
	DefaultValue    string                    `json:"default_value,omitempty"`
	EnumValues      []string                  `json:"enum_values,omitempty"`
	UIGroup         string                    `json:"ui_group,omitempty"`
	SortOrder       int                       `json:"sort_order"`
	Validation      *fieldmeta.ValidationRules  `json:"validation,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string                    `json:"reference_target,omitempty"`
	CreatedAt       time.Time                 `json:"created_at"`
	UpdatedAt       time.Time                 `json:"updated_at"`
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
	Key                      string         `json:"key"`
	Name                     string         `json:"name"`
	DisplayName              string         `json:"display_name,omitempty"`
	Icon                     string         `json:"icon,omitempty"`
	Description              string         `json:"description,omitempty"`
	Category                 string         `json:"category,omitempty"`
	IsLogical                bool           `json:"is_logical,omitempty"`
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
	DisplayName              *string        `json:"display_name,omitempty"`
	Icon                     *string        `json:"icon,omitempty"`
	Description              *string        `json:"description,omitempty"`
	Category                 *string        `json:"category,omitempty"`
	IsLogical                *bool          `json:"is_logical,omitempty"`
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
	Scope           string                    `json:"scope,omitempty"`
	Name            string                    `json:"name"`
	Label           string                    `json:"label,omitempty"`
	Description     string                    `json:"description,omitempty"`
	DataType        string                    `json:"data_type"`
	Required        bool                      `json:"required,omitempty"`
	DefaultValue    string                    `json:"default_value,omitempty"`
	EnumValues      []string                  `json:"enum_values,omitempty"`
	UIGroup         string                    `json:"ui_group,omitempty"`
	SortOrder       int                       `json:"sort_order,omitempty"`
	Validation      *fieldmeta.ValidationRules  `json:"validation,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string                    `json:"reference_target,omitempty"`
}

// UpsertInstanceFieldRequest is the payload for creating or updating a field
// definition on a single CI instance.
type UpsertInstanceFieldRequest struct {
	Name            string                    `json:"name"`
	Label           string                    `json:"label,omitempty"`
	Description     string                    `json:"description,omitempty"`
	DataType        string                    `json:"data_type"`
	Required        bool                      `json:"required,omitempty"`
	DefaultValue    string                    `json:"default_value,omitempty"`
	EnumValues      []string                  `json:"enum_values,omitempty"`
	UIGroup         string                    `json:"ui_group,omitempty"`
	SortOrder       int                       `json:"sort_order,omitempty"`
	Validation      *fieldmeta.ValidationRules  `json:"validation,omitempty"`
	Conditional     *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string                    `json:"reference_target,omitempty"`
}

// FilterParams holds query filter parameters for listing CI types.
type FilterParams struct {
	IncludeInactive bool
	Category        string
	Search          string
}
