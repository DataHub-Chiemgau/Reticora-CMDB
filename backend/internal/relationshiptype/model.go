// Package relationshiptype implements administrator-managed relationship type
// metadata (spec §11): forward/reverse labels, allowed source/target CI
// types, direction, cardinality, category, and impact-analysis participation.
// Types are metadata rows seeded with a system catalogue; custom types need
// no code or schema changes.
package relationshiptype

import "time"

// Type is a relationship type definition.
type Type struct {
	ID                  string    `json:"id"`
	OrganizationID      string    `json:"organization_id,omitempty"`
	Key                 string    `json:"key"`
	ForwardLabel        string    `json:"forward_label"`
	ReverseLabel        string    `json:"reverse_label"`
	SourceCITypes       []string  `json:"source_ci_types"`
	TargetCITypes       []string  `json:"target_ci_types"`
	Direction           string    `json:"direction"` // directed | undirected
	Cardinality         string    `json:"cardinality"`
	Category            string    `json:"category,omitempty"`
	ImpactParticipation bool      `json:"impact_participation"`
	IsSystem            bool      `json:"is_system"`
	Description         string    `json:"description,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// UpsertRequest is the payload for creating or updating a relationship type.
type UpsertRequest struct {
	Key                 string   `json:"key"`
	ForwardLabel        string   `json:"forward_label"`
	ReverseLabel        string   `json:"reverse_label"`
	SourceCITypes       []string `json:"source_ci_types,omitempty"`
	TargetCITypes       []string `json:"target_ci_types,omitempty"`
	Direction           string   `json:"direction,omitempty"`
	Cardinality         string   `json:"cardinality,omitempty"`
	Category            string   `json:"category,omitempty"`
	ImpactParticipation *bool    `json:"impact_participation,omitempty"`
	Description         string   `json:"description,omitempty"`
}

// Catalogue returns the built-in relationship type catalogue (spec §11). The
// same set is seeded by migration 000055 for database-backed deployments.
func Catalogue() []Type {
	def := func(key, fwd, rev, category string) Type {
		return Type{
			Key: key, ForwardLabel: fwd, ReverseLabel: rev,
			Direction: "directed", Cardinality: "many_to_many",
			Category: category, ImpactParticipation: true, IsSystem: true,
			SourceCITypes: []string{}, TargetCITypes: []string{},
		}
	}
	return []Type{
		def("connected_to", "connected to", "connected to", "network"),
		def("hosted_on", "hosted on", "hosts", "infrastructure"),
		def("runs_on", "runs on", "hosts", "infrastructure"),
		def("depends_on", "depends on", "required by", "dependency"),
		def("member_of", "member of", "has member", "grouping"),
		def("member_of_cluster", "member of cluster", "has cluster member", "grouping"),
		def("powers", "powers", "powered by", "power"),
		def("powered_by", "powered by", "powers", "power"),
		def("mounted_in", "mounted in", "mounts", "infrastructure"),
		def("uplink_to", "uplink to", "uplink from", "network"),
		def("stores", "stores", "stored on", "storage"),
		def("monitors", "monitors", "monitored by", "monitoring"),
		def("backs_up", "backs up", "backed up by", "backup"),
		def("backed_up_by", "backed up by", "backs up", "backup"),
		def("managed_by", "managed by", "manages", "management"),
		def("manages", "manages", "managed by", "management"),
		def("assigned_to", "assigned to", "assigned", "inventory"),
		def("contains", "contains", "contained by", "composition"),
		def("contained_by", "contained by", "contains", "composition"),
		def("parent_of", "parent of", "child of", "composition"),
		def("child_of", "child of", "parent of", "composition"),
		def("located_in", "located in", "contains", "location"),
		def("uses", "uses", "used by", "dependency"),
		def("used_by", "used by", "uses", "dependency"),
	}
}
