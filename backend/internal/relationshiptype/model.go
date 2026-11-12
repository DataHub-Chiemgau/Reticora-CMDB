// Package relationshiptype implements administrator-managed relationship type
// metadata (REL-01, CH27): display and reverse names, allowed source/target
// CI types, direction, cardinality, category and the impact semantics
// (impact_direction, redundant). Types are metadata rows seeded with a
// system catalogue; custom types need no code or schema changes.
package relationshiptype

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// Categories of REL-01.
var Categories = []string{"network", "power", "hosting", "logical", "physical"}

// Impact directions of REL-01/CH27: which end of an edge depends on the
// other; none takes no part in impact, undirected edges (connected_to) are
// a heuristic in both directions.
const (
	ImpactSourceDependsOnTarget = "source_depends_on_target"
	ImpactTargetDependsOnSource = "target_depends_on_source"
	ImpactNone                  = "none"
	ImpactUndirected            = "undirected"
)

// ImpactDirections lists the impact directions.
var ImpactDirections = []string{ImpactSourceDependsOnTarget, ImpactTargetDependsOnSource, ImpactNone, ImpactUndirected}

// Cardinalities of REL-01, enforced since REL-06 (WP-294): one_to_one
// allows a source one outgoing and a target one incoming edge of the type,
// one_to_many a target one incoming, many_to_one a source one outgoing
// edge; many_to_many has no bound.
const (
	CardinalityOneToOne   = "one_to_one"
	CardinalityOneToMany  = "one_to_many"
	CardinalityManyToOne  = "many_to_one"
	CardinalityManyToMany = "many_to_many"
)

// Cardinalities lists the cardinalities.
var Cardinalities = []string{CardinalityOneToOne, CardinalityOneToMany, CardinalityManyToOne, CardinalityManyToMany}

// StructuralKeys are the relationships projected from the structure tables
// (CH27): they are listed but never stored.
var StructuralKeys = []string{"contains", "contained_by", "parent_of", "child_of", "mounted_in", "located_in"}

// ProjectedKeys are the edges the topology and the impact project from the
// IPAM tables to subnets and VLANs (IMP-11, WP-298): in_subnet from a CI to
// a subnet, in_vlan from a CI or a subnet to a VLAN. They are neither
// stored nor listed and no type may define them.
var ProjectedKeys = []string{"in_subnet", "in_vlan"}

// Type is a relationship type definition (REL-01). AllowedSourceTypes and
// AllowedTargetTypes are CI type keys: POST /relationships takes a source
// and a target CI of these types only, an empty list allows every type
// (E-95, F-313), subtypes of a listed type included (MET-06, WP-294).
// Cardinality bounds the edges of the type per end (REL-06, WP-294).
// MetadataSchema describes the attributes of its edges (REL-07, WP-295);
// a system type has none.
type Type struct {
	ID                 string   `json:"id"`
	OrganizationID     string   `json:"organization_id,omitempty"`
	Key                string   `json:"key"`
	DisplayName        string   `json:"display_name"`
	ReverseName        string   `json:"reverse_name"`
	Directed           bool     `json:"directed"`
	Category           string   `json:"category"`
	ImpactDirection    string   `json:"impact_direction"`
	Redundant          bool     `json:"redundant"`
	AllowedSourceTypes []string `json:"allowed_source_types"`
	AllowedTargetTypes []string `json:"allowed_target_types"`
	Cardinality        string   `json:"cardinality"`
	IsSystem           bool     `json:"is_system"`
	Description        string   `json:"description,omitempty"`
	// MetadataSchema is never nil: [] without definitions.
	MetadataSchema []AttributeDefinition `json:"metadata_schema"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

// UpsertRequest is the payload for creating or updating a relationship type.
type UpsertRequest struct {
	Key                string   `json:"key"`
	DisplayName        string   `json:"display_name"`
	ReverseName        string   `json:"reverse_name"`
	Directed           *bool    `json:"directed,omitempty"`
	Category           string   `json:"category,omitempty"`
	ImpactDirection    string   `json:"impact_direction,omitempty"`
	Redundant          *bool    `json:"redundant,omitempty"`
	AllowedSourceTypes []string `json:"allowed_source_types,omitempty"`
	AllowedTargetTypes []string `json:"allowed_target_types,omitempty"`
	Cardinality        string   `json:"cardinality,omitempty"`
	Description        string   `json:"description,omitempty"`
	// MetadataSchema replaces the schema; nil (absent) keeps the stored
	// one, [] clears it (REL-07).
	MetadataSchema *[]AttributeDefinition `json:"metadata_schema,omitempty"`
}

// ciTypeList is a request field that names CI type keys (REL-01, E-95).
type ciTypeList struct {
	field string
	keys  []string
}

// ciTypeLists returns the CI type lists of a type; a nil list is not set
// (a change without it keeps the stored list).
func ciTypeLists(source, target []string) []ciTypeList {
	return []ciTypeList{{"allowed_source_types", source}, {"allowed_target_types", target}}
}

// Validate checks the REL-01 contract of a type: a known category,
// impact direction and cardinality; an undirected type has the impact
// direction undirected, a directed type any other; undirected belongs to
// the category network (REL-06, IMP-03). The database holds the same rules
// (CHECKs of migrations 000113 and 000202).
func (t *Type) Validate() error {
	if !slices.Contains(Categories, t.Category) {
		return fmt.Errorf("category must be one of %v", Categories)
	}
	if !slices.Contains(ImpactDirections, t.ImpactDirection) {
		return fmt.Errorf("impact_direction must be one of %v", ImpactDirections)
	}
	if !slices.Contains(Cardinalities, t.Cardinality) {
		return fmt.Errorf("cardinality must be one of %v", Cardinalities)
	}
	if t.Directed == (t.ImpactDirection == ImpactUndirected) {
		return fmt.Errorf("impact_direction undirected belongs to undirected types (directed false) only")
	}
	if t.ImpactDirection == ImpactUndirected && t.Category != "network" {
		return fmt.Errorf("an undirected type belongs to the category network")
	}
	if slices.Contains(StructuralKeys, t.Key) && !t.IsSystem {
		return fmt.Errorf("%s is projected from the structure tables and cannot be defined", t.Key)
	}
	if slices.Contains(ProjectedKeys, t.Key) {
		return fmt.Errorf("%s is projected from the IPAM tables and cannot be defined", t.Key)
	}
	if t.IsSystem && len(t.MetadataSchema) > 0 {
		return fmt.Errorf("a system type has no metadata_schema")
	}
	return validateSchema(t.MetadataSchema)
}

// apply sets the fields of req on t.
func (req *UpsertRequest) apply(t *Type) {
	if req.DisplayName != "" {
		t.DisplayName = req.DisplayName
	}
	if req.ReverseName != "" {
		t.ReverseName = req.ReverseName
	}
	if req.Directed != nil {
		t.Directed = *req.Directed
	}
	if req.Category != "" {
		t.Category = req.Category
	}
	if req.ImpactDirection != "" {
		t.ImpactDirection = req.ImpactDirection
	}
	if req.Redundant != nil {
		t.Redundant = *req.Redundant
	}
	if req.AllowedSourceTypes != nil {
		t.AllowedSourceTypes = req.AllowedSourceTypes
	}
	if req.AllowedTargetTypes != nil {
		t.AllowedTargetTypes = req.AllowedTargetTypes
	}
	if req.Cardinality != "" {
		t.Cardinality = req.Cardinality
	}
	if req.Description != "" {
		t.Description = req.Description
	}
	if req.MetadataSchema != nil {
		t.MetadataSchema = *req.MetadataSchema
	}
}

// Catalogue returns the built-in relationship type catalogue with the
// impact semantics of CH27 (E-18). Migrations 000113 and 000202 (REL-06:
// monitored_by, belongs_to) give the seeded rows the same values.
func Catalogue() []Type {
	def := func(key, name, reverse, category, impact string, redundant bool) Type {
		return Type{
			Key: key, DisplayName: name, ReverseName: reverse,
			Directed: impact != ImpactUndirected, Category: category, ImpactDirection: impact, Redundant: redundant,
			Cardinality: CardinalityManyToMany, IsSystem: true,
			AllowedSourceTypes: []string{}, AllowedTargetTypes: []string{}, MetadataSchema: []AttributeDefinition{},
		}
	}
	return []Type{
		def("connected_to", "connected to", "connected to", "network", ImpactUndirected, false),
		def("uplink_to", "uplink to", "uplink from", "network", ImpactSourceDependsOnTarget, true),
		def("powered_by", "powered by", "powers", "power", ImpactSourceDependsOnTarget, true),
		def("powers", "powers", "powered by", "power", ImpactTargetDependsOnSource, true),
		def("hosted_on", "hosted on", "hosts", "hosting", ImpactSourceDependsOnTarget, false),
		def("runs_on", "runs on", "hosts", "hosting", ImpactSourceDependsOnTarget, false),
		def("stores", "stores", "stored on", "hosting", ImpactTargetDependsOnSource, false),
		def("member_of_cluster", "member of cluster", "has cluster member", "logical", ImpactSourceDependsOnTarget, true),
		def("depends_on", "depends on", "required by", "logical", ImpactSourceDependsOnTarget, false),
		def("uses", "uses", "used by", "logical", ImpactSourceDependsOnTarget, false),
		def("used_by", "used by", "uses", "logical", ImpactTargetDependsOnSource, false),
		def("member_of", "member of", "has member", "logical", ImpactNone, false),
		def("monitors", "monitors", "monitored by", "logical", ImpactNone, false),
		// The source is the monitored CI; the failure of the monitoring CI
		// reaches none (REL-06).
		def("monitored_by", "monitored by", "monitors", "logical", ImpactNone, false),
		def("belongs_to", "belongs to", "has member", "logical", ImpactSourceDependsOnTarget, false),
		def("backs_up", "backs up", "backed up by", "logical", ImpactNone, false),
		def("backed_up_by", "backed up by", "backs up", "logical", ImpactNone, false),
		def("managed_by", "managed by", "manages", "logical", ImpactNone, false),
		def("manages", "manages", "managed by", "logical", ImpactNone, false),
		def("assigned_to", "assigned to", "assigned", "logical", ImpactNone, false),
		def("contains", "contains", "contained by", "physical", ImpactTargetDependsOnSource, false),
		def("contained_by", "contained by", "contains", "physical", ImpactSourceDependsOnTarget, false),
		def("parent_of", "parent of", "child of", "physical", ImpactTargetDependsOnSource, false),
		def("child_of", "child of", "parent of", "physical", ImpactSourceDependsOnTarget, false),
		def("mounted_in", "mounted in", "mounts", "physical", ImpactSourceDependsOnTarget, false),
		def("located_in", "located in", "contains", "physical", ImpactNone, false),
	}
}

// ReservedAttributeKeys are the keys no attribute of an edge may have
// (REL-07, WP-295): provenance, which only the server records (REL-08),
// and every JSON field name of an edge (relationship.Relationship). A
// definition with such a key is 400; POST and PATCH /relationships refuse
// such an attribute with 422 on attributes.<key>.
var ReservedAttributeKeys = []string{
	"provenance",
	"id", "organization_id", "source_ci_id", "target_ci_id", "rel_type", "attributes", "source",
	"confidence", "first_seen_at", "last_seen_at", "verification_state", "source_system", "notes",
	"source_interface_id", "target_interface_id", "last_confirmed_at", "verified_by", "verified_at",
	"created_at", "updated_at", "projected", "target_location_id",
}

// maxAttributeKeyLength is the longest key of a definition, as of
// attribute_definition.
const maxAttributeKeyLength = 100

// AttributeDefinition is one definition of the attribute schema of a
// relationship type (REL-07, WP-295) in the format of attribute_definition
// (citype.AttributeDefinition), limited to key, display_name, data_type,
// required, default, enum_values, validation and conditional. data_type is
// a phase 1 type of MET-11 (fieldmeta.Phase1Types). validation is a rule
// AST of MET-45; a request may send the legacy form, which is translated
// (fieldmeta.ParseValidation). default only prefills a form; the server
// does not set it.
type AttributeDefinition struct {
	Key         string                      `json:"key"`
	DisplayName citype.LocalizedText        `json:"display_name,omitempty"`
	DataType    string                      `json:"data_type"`
	Required    bool                        `json:"required"`
	Default     json.RawMessage             `json:"default,omitempty"`
	EnumValues  []string                    `json:"enum_values,omitempty"`
	Validation  *rules.Rule                 `json:"validation,omitempty"`
	Conditional *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	// validationErr is the error of reading the validation of a request;
	// Validate reports it (400).
	validationErr error
}

// attributeDefinitionJSON is the wire form of an AttributeDefinition with
// the validation not yet read.
type attributeDefinitionJSON struct {
	Key         string                      `json:"key"`
	DisplayName citype.LocalizedText        `json:"display_name,omitempty"`
	DataType    string                      `json:"data_type"`
	Required    bool                        `json:"required"`
	Default     json.RawMessage             `json:"default,omitempty"`
	EnumValues  []string                    `json:"enum_values,omitempty"`
	Validation  json.RawMessage             `json:"validation,omitempty"`
	Conditional *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
}

func (w *attributeDefinitionJSON) definition() AttributeDefinition {
	return AttributeDefinition{
		Key: w.Key, DisplayName: w.DisplayName, DataType: w.DataType, Required: w.Required,
		Default: w.Default, EnumValues: w.EnumValues, Conditional: w.Conditional,
	}
}

// UnmarshalJSON reads a definition of a request strictly: an unknown member
// is an error (as api.ReadJSON), the validation is read with
// fieldmeta.ParseValidation (rule AST or legacy form).
func (d *AttributeDefinition) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var w attributeDefinitionJSON
	if err := dec.Decode(&w); err != nil {
		return fmt.Errorf("metadata_schema: %w", err)
	}
	*d = w.definition()
	d.Validation, d.validationErr = fieldmeta.ParseValidation(w.Validation)
	return nil
}

// decodeSchema reads a stored metadata_schema; a stored validation is read
// like fieldmeta.StoredValidation, without the checks of a request.
func decodeSchema(raw []byte) ([]AttributeDefinition, error) {
	out := []AttributeDefinition{}
	if len(raw) == 0 {
		return out, nil
	}
	var stored []attributeDefinitionJSON
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("read metadata_schema: %w", err)
	}
	for i := range stored {
		def := stored[i].definition()
		if len(stored[i].Validation) > 0 {
			def.Validation = fieldmeta.StoredValidation(stored[i].Validation)
		}
		out = append(out, def)
	}
	return out, nil
}

// Field returns the definition in the shared form of the field metadata
// (fieldmeta.FieldDefinition): key as name, the validation as the
// structure of fieldmeta.ValidationFromRule.
func (d *AttributeDefinition) Field() fieldmeta.FieldDefinition {
	validation, _, _ := fieldmeta.ValidationFromRule(d.Validation)
	return fieldmeta.FieldDefinition{
		Name: d.Key, Label: d.DisplayName.WithFallback(d.Key)["en"], DataType: d.DataType,
		Required: d.Required, EnumValues: d.EnumValues, Validation: validation, Conditional: d.Conditional,
	}
}

// Fields returns the definitions of a schema as fieldmeta definitions.
func Fields(schema []AttributeDefinition) []fieldmeta.FieldDefinition {
	out := make([]fieldmeta.FieldDefinition, len(schema))
	for i := range schema {
		out[i] = schema[i].Field()
	}
	return out
}

// validate checks a definition as citype.AttributeDefinition.Validate does
// and normalizes it: the key trimmed, display_name with both languages,
// required set by a required rule of the validation. It returns the member
// a rejection concerns.
// validation: fixed texts naming the member and the rule.
func (d *AttributeDefinition) validate() (string, error) {
	d.Key = strings.TrimSpace(d.Key)
	if d.Key == "" || utf8.RuneCountInString(d.Key) > maxAttributeKeyLength {
		return "key", fmt.Errorf("key is required (at most %d characters)", maxAttributeKeyLength)
	}
	if slices.Contains(ReservedAttributeKeys, d.Key) {
		return "key", fmt.Errorf("%q is reserved for the relationship itself", d.Key)
	}
	if err := d.DisplayName.Validate(); err != nil {
		return "display_name", err
	}
	d.DisplayName = d.DisplayName.WithFallback(d.Key)
	// Exactly the phase 1 types: json is not checked by
	// fieldmeta.ValidateValue, file and the references only as an id.
	if !slices.Contains(fieldmeta.Phase1Types, d.DataType) {
		return "data_type", fmt.Errorf("data_type must be one of %v", fieldmeta.Phase1Types)
	}
	if d.validationErr != nil {
		return "validation", d.validationErr
	}
	if d.Validation != nil {
		if err := d.Validation.Validate(); err != nil {
			return "validation", err
		}
		_, required, err := fieldmeta.ValidationFromRule(d.Validation)
		if err != nil {
			return "validation", err
		}
		d.Required = d.Required || required
	}
	field := d.Field()
	if err := fieldmeta.ValidateDefinition(&field); err != nil {
		attribute := "data_type"
		var de *fieldmeta.DefinitionError
		if errors.As(err, &de) {
			attribute = de.Attribute
		}
		return attribute, err
	}
	if len(d.Default) > 0 && string(d.Default) != "null" {
		var value any
		if err := json.Unmarshal(d.Default, &value); err != nil {
			return "default", fmt.Errorf("default must be a JSON value")
		}
		if err := fieldmeta.ValidateValue(&field, value); err != nil {
			return "default", err
		}
	}
	return "", nil
}

// validateSchema checks the definitions of a schema; the keys are unique.
func validateSchema(schema []AttributeDefinition) error {
	seen := make(map[string]bool, len(schema))
	for i := range schema {
		member, err := schema[i].validate()
		if err != nil {
			return fmt.Errorf("metadata_schema[%d].%s: %v", i, member, err)
		}
		if seen[schema[i].Key] {
			return fmt.Errorf("metadata_schema[%d].key: %q is defined twice", i, schema[i].Key)
		}
		seen[schema[i].Key] = true
	}
	return nil
}
