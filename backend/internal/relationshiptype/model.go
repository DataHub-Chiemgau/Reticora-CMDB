// Package relationshiptype implements administrator-managed relationship type
// metadata (REL-01, CH27): display and reverse names, allowed source/target
// CI types, direction, cardinality, category and the impact semantics
// (impact_direction, redundant). Types are metadata rows seeded with a
// system catalogue; custom types need no code or schema changes.
package relationshiptype

import (
	"fmt"
	"time"
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

// StructuralKeys are the relationships projected from the structure tables
// (CH27): they are listed but never stored.
var StructuralKeys = []string{"contains", "contained_by", "parent_of", "child_of", "mounted_in", "located_in"}

// Type is a relationship type definition (REL-01). AllowedSourceTypes and
// AllowedTargetTypes are CI type keys: POST /relationships takes a source
// and a target CI of these types only, an empty list allows every type
// (E-95, F-313). Cardinality is informative until REL-06 [P2].
type Type struct {
	ID                 string    `json:"id"`
	OrganizationID     string    `json:"organization_id,omitempty"`
	Key                string    `json:"key"`
	DisplayName        string    `json:"display_name"`
	ReverseName        string    `json:"reverse_name"`
	Directed           bool      `json:"directed"`
	Category           string    `json:"category"`
	ImpactDirection    string    `json:"impact_direction"`
	Redundant          bool      `json:"redundant"`
	AllowedSourceTypes []string  `json:"allowed_source_types"`
	AllowedTargetTypes []string  `json:"allowed_target_types"`
	Cardinality        string    `json:"cardinality"`
	IsSystem           bool      `json:"is_system"`
	Description        string    `json:"description,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
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

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Validate checks the REL-01 contract of a type: a known category and
// impact direction; an undirected type has the impact direction
// undirected, a directed type any other.
func (t *Type) Validate() error {
	if !contains(Categories, t.Category) {
		return fmt.Errorf("category must be one of %v", Categories)
	}
	if !contains(ImpactDirections, t.ImpactDirection) {
		return fmt.Errorf("impact_direction must be one of %v", ImpactDirections)
	}
	if t.Directed == (t.ImpactDirection == ImpactUndirected) {
		return fmt.Errorf("impact_direction undirected belongs to undirected types (directed false) only")
	}
	if contains(StructuralKeys, t.Key) && !t.IsSystem {
		return fmt.Errorf("%s is projected from the structure tables and cannot be defined", t.Key)
	}
	return nil
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
}

// Catalogue returns the built-in relationship type catalogue with the
// impact semantics of CH27 (E-18). Migration 000113 gives the seeded rows
// the same values.
func Catalogue() []Type {
	def := func(key, name, reverse, category, impact string, redundant bool) Type {
		return Type{
			Key: key, DisplayName: name, ReverseName: reverse,
			Directed: impact != ImpactUndirected, Category: category, ImpactDirection: impact, Redundant: redundant,
			Cardinality: "many_to_many", IsSystem: true,
			AllowedSourceTypes: []string{}, AllowedTargetTypes: []string{},
		}
	}
	return []Type{
		def("connected_to", "connected to", "connected to", "network", ImpactUndirected, false),
		def("uplink_to", "uplink to", "uplink from", "network", ImpactSourceDependsOnTarget, false),
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
