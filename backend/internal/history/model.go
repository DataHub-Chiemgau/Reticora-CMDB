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
	EntityType     string    `json:"entity_type"` // ci | asset | relationship | location | reservation | composition
	EntityID       string    `json:"entity_id"`
	ActorID        string    `json:"actor_id,omitempty"`
	ChangeType     string    `json:"change_type"`
	FieldName      string    `json:"field_name,omitempty"`
	OldValue       any       `json:"old_value,omitempty"`
	NewValue       any       `json:"new_value,omitempty"`
	Comment        string    `json:"comment,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	// Source names the trail of the entry in the unified CI and location
	// history (CI-06, LOC-11): ci_change, entity_change, audit or
	// location_change.
	Source string `json:"source,omitempty"`
	// SiteID is the site of the CI when the change was made (ci_change),
	// or the site of the moved location (location_change).
	SiteID string `json:"site_id,omitempty"`

	// The fields below describe a change of a CI after AUD-03 (E-82 option
	// 2); only ci_change entries carry them. One entry is one change set:
	// the field rows of one write. A change set of one row also keeps
	// field_name, old_value and new_value; one of several rows leaves them
	// empty. A row written before 000167 has no change set and is an entry
	// of its own.

	// ChangeKind is the kind of the change (ChangeKinds).
	ChangeKind string `json:"change_kind,omitempty"`
	// ChangeSetID groups the field rows of one write; empty for a row
	// written before 000167.
	ChangeSetID string `json:"change_set_id,omitempty"`
	// FieldChanges are the changed fields of the change set; a row without
	// a field (create, delete) is none.
	FieldChanges []FieldChange `json:"field_changes,omitempty"`
	// ChangeSource is the source of the change (ui, api, collector, import,
	// system, as in the audit log); the JSON name keeps source for the trail.
	ChangeSource  string     `json:"change_source,omitempty"`
	CollectorID   string     `json:"collector_id,omitempty"`
	RequestID     string     `json:"request_id,omitempty"`
	CorrelationID string     `json:"correlation_id,omitempty"`
	ObservedAt    *time.Time `json:"observed_at,omitempty"`
	// ActorUserID is the user who made the change (not an API key).
	ActorUserID string `json:"actor_user_id,omitempty"`
}

// FieldChange is one changed field of a change set (AUD-03).
type FieldChange struct {
	Field string `json:"field"`
	Old   any    `json:"old"`
	New   any    `json:"new"`
}

// ListOptions narrow the history of an entity. IncludeAudit adds the audit
// entries of a CI (E-78); ChangeKind keeps only the CI changes of that kind
// (AUD-03), entries without a kind drop out.
type ListOptions struct {
	IncludeAudit bool
	ChangeKind   string
}

// Sources of the unified CI and location history.
const (
	SourceCIChange       = "ci_change"
	SourceEntityChange   = "entity_change"
	SourceAudit          = "audit"
	SourceLocationChange = "location_change"
)

// StateSnapshot is the reconstructed state of an entity at a point in time.
type StateSnapshot struct {
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
	At         time.Time `json:"at"`
	// Fields holds the last known value per field up to the snapshot time.
	Fields map[string]any `json:"fields"`
	// Deleted is true when a delete change precedes the snapshot time.
	Deleted bool `json:"deleted"`
}

// Change types (kept as data, not code; the CHECK constraint lists the
// current vocabulary but new classes are additive). The type change is
// type_changed (CI-11, 000167).
var ChangeTypes = map[string]bool{
	"create": true, "update": true, "delete": true, "status_change": true,
	"attribute_change": true, "type_changed": true, "lifecycle_transition": true,
	"location_change": true, "movement": true, "override": true,
	"reconciliation_decision": true, "parent_child_change": true,
	"assignment": true, "relationship_change": true,
}

// ChangeKinds are the kinds of a CI change (AUD-03, ci_change.change_kind
// of 000167): the kind holds for the change set of one write.
var ChangeKinds = map[string]bool{
	"created": true, "updated": true, "deleted": true, "restored": true,
	"merged": true, "type_changed": true, "relationship_added": true,
	"relationship_removed": true, "status_changed": true,
}

// NoRecordedStateError is the answer of a state snapshot whose entity has
// no recorded change up to the snapshot time: before its creation or
// before the first recorded change (JOB-04, F-164). The snapshot job fails
// with it; GET /cis/{id}/state and /assets/{id}/state answer 404.
type NoRecordedStateError struct {
	At time.Time
}

func (e *NoRecordedStateError) Error() string {
	return "no recorded state before " + e.At.UTC().Format(time.RFC3339Nano)
}

// Replay reconstructs the field state from an ordered change trail (oldest
// first). A "create" change seeds the state with its new_value object;
// subsequent changes overwrite individual fields; "delete" marks the entity
// as deleted and a change of kind restored (CI-05) clears the mark again.
// Overrides and reconciliation decisions (merge, unmerge) are no field
// changes of the entity and are not replayed (AUD-08).
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
		case "override", "reconciliation_decision":
		default:
			if change.FieldName != "" {
				if change.NewValue == nil {
					delete(snapshot.Fields, change.FieldName)
				} else {
					snapshot.Fields[change.FieldName] = change.NewValue
				}
			}
		}
		if change.ChangeKind == "restored" {
			snapshot.Deleted = false
		}
	}
	return snapshot
}
