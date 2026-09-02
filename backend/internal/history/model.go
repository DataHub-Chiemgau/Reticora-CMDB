// Package history implements the unified entity change history and
// point-in-time state reconstruction (spec §16). Every mutation of assets,
// CIs, relationships, lifecycle states, assignments, locations, movements and
// overrides is recorded as an append-only change row; the state of an entity
// at a historical point in time is reconstructed by replaying the trail.
package history

import "time"

// Change is one recorded entity mutation.
type Change struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	EntityType     string    `json:"entity_type"` // ci | asset | relationship | location_node | reservation | composition
	EntityID       string    `json:"entity_id"`
	ActorID        string    `json:"actor_id,omitempty"`
	ChangeType     string    `json:"change_type"`
	FieldName      string    `json:"field_name,omitempty"`
	OldValue       any       `json:"old_value,omitempty"`
	NewValue       any       `json:"new_value,omitempty"`
	Comment        string    `json:"comment,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// StateSnapshot is the reconstructed state of an entity at a point in time.
type StateSnapshot struct {
	EntityType string         `json:"entity_type"`
	EntityID   string         `json:"entity_id"`
	At         time.Time      `json:"at"`
	// Fields holds the last known value per field up to the snapshot time.
	Fields map[string]any `json:"fields"`
	// Deleted is true when a delete change precedes the snapshot time.
	Deleted bool `json:"deleted"`
}

// Change types (kept as data, not code; the CHECK constraint lists the
// current vocabulary but new classes are additive).
var ChangeTypes = map[string]bool{
	"create": true, "update": true, "delete": true, "status_change": true,
	"attribute_change": true, "type_change": true, "lifecycle_transition": true,
	"location_change": true, "movement": true, "override": true,
	"reconciliation_decision": true, "parent_child_change": true,
	"assignment": true, "relationship_change": true,
}

// Replay reconstructs the field state from an ordered change trail (oldest
// first). A "create" change seeds the state with its new_value object;
// subsequent changes overwrite individual fields; "delete" marks the entity
// as deleted.
func Replay(changes []Change) StateSnapshot {
	snapshot := StateSnapshot{Fields: map[string]any{}}
	for _, change := range changes {
		switch change.ChangeType {
		case "create":
			if obj, ok := change.NewValue.(map[string]any); ok {
				for k, v := range obj {
					snapshot.Fields[k] = v
				}
			}
			snapshot.Deleted = false
		case "delete":
			snapshot.Deleted = true
		default:
			if change.FieldName != "" {
				if change.NewValue == nil {
					delete(snapshot.Fields, change.FieldName)
				} else {
					snapshot.Fields[change.FieldName] = change.NewValue
				}
			}
		}
	}
	return snapshot
}
