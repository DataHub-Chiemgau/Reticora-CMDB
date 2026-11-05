// Package history implements the unified entity change history and
// point-in-time state reconstruction (spec §16). Every mutation of assets,
// CIs, relationships, lifecycle states, assignments, locations, movements and
// overrides is recorded as an append-only change row; the state of an entity
// at a historical point in time is reconstructed by replaying the trail.
package history

import (
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

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

	// ciVersion is the version of the CI after a ci_change row or at a
	// ci_snapshot of its trail (WP-312).
	ciVersion *int64
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

// Sources of the unified CI and location history. SourceRelationshipChange
// is the trail of the history of a relationship (relationship_change,
// WP-296) and of the edges of a CI in its timeline (CITimeline, WP-792).
const (
	SourceCIChange           = "ci_change"
	SourceEntityChange       = "entity_change"
	SourceAudit              = "audit"
	SourceLocationChange     = "location_change"
	SourceRelationshipChange = "relationship_change"
)

// TimelineOptions narrow the timeline of a CI (CITimeline, WP-792). From
// and To bound the time of recording, both inclusive (zero: open):
// created_at, timestamp of an audit entry, changed_at of a location move,
// observed_at of a relationship change. IncludeAudit adds the audit entries
// (audit:read, E-78), IncludeRelationships the entries of relationship_change
// of the edges whose source or target is the CI, removed edges included
// (relationship:read).
type TimelineOptions struct {
	From, To             time.Time
	IncludeAudit         bool
	IncludeRelationships bool
}

// ProjectCITimeline projects the timeline entries of the CI ciID with the
// field permissions of policy (RBA-07, E-43) for the GraphQL BFF (WP-792):
// like the REST history (projectList), except that an entry of a field the
// principal may not read stays, without old and new value; in its field
// changes the unreadable fields are left out. facts are the type, scope
// and instance keys of the CI (nil: only the structural fields). The
// entries of relationship_change are projected by the relationship grant
// already (CITimeline) and stay as they are.
func ProjectCITimeline(policy *permission.FieldPolicy, ciID string, facts *CIFacts, items []Change) []Change {
	if !permission.NeedsProjection(policy, permission.KindCI) {
		return items
	}
	p := &ciProjection{ref: permission.ObjectRef{ID: ciID}}
	if facts != nil {
		p.typeID, p.keys = facts.TypeID, facts.InstanceKeys
		p.ref.ClientID, p.ref.SiteID = facts.ClientID, facts.SiteID
		if policy.Level(permission.KindCI, facts.TypeID, "", p.ref).Readable() {
			p.policy = policy
		}
	}
	out := make([]Change, 0, len(items))
	for i := range items {
		c := items[i]
		switch {
		case c.Source == SourceRelationshipChange:
		case c.FieldName != "" && c.FieldName != memberAttributes && c.Source != SourceAudit &&
			c.Source != SourceLocationChange && !p.field(c.FieldName):
			c.OldValue, c.NewValue = nil, nil
			c.FieldChanges = p.fieldChanges(c.FieldChanges)
		default:
			projected := p.changes(ciID, []Change{c})
			if len(projected) == 1 {
				c = projected[0]
			}
		}
		out = append(out, c)
	}
	return out
}

// StateSnapshot is the reconstructed state of an entity at a point in time.
type StateSnapshot struct {
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
	At         time.Time `json:"at"`
	// Fields holds the last known value per field up to the snapshot time.
	Fields map[string]any `json:"fields"`
	// Deleted is true when a delete change precedes the snapshot time.
	Deleted bool `json:"deleted"`
	// MergedIntoID is the survivor a merged CI was merged into (CI-12);
	// empty for a CI that was not merged.
	MergedIntoID string `json:"merged_into_id,omitempty"`
	// BasedOnSnapshotAt is taken_at of the ci_snapshot the replay of a CI
	// started from (WP-793); nil when it replayed the whole trail.
	BasedOnSnapshotAt *time.Time `json:"based_on_snapshot_at,omitempty"`
	// Relationships are the edges of the CI at the point in time (WP-793):
	// nil (left out) without relationship:read or for the snapshot job,
	// an empty list for a CI without edges.
	Relationships *[]EdgeState `json:"relationships,omitempty"`
	// NotHistorized names the fields of a CI that are not historized
	// (AUD-03) and so are not part of its state at a point in time.
	NotHistorized []string `json:"not_historized,omitempty"`
	// restore is what a restore of the deleted CI brings back (CI-05):
	// the status before the deletion and the released asset link.
	restore restoreState
}

// restoreState is the status and the asset link of a CI before its last
// deletion (old_value.status and new_value.unlinked_asset_id of the delete
// row), as ci.PGRepository.restore reads them.
type restoreState struct {
	Status  string `json:"status,omitempty"`
	AssetID string `json:"asset_id,omitempty"`
}

// Change types (kept as data, not code; the CHECK constraint lists the
// current vocabulary but new classes are additive). The type change is
// type_changed (CI-11, 000167), the template migration of a CI
// template_migration (WP-253, 000210); Replay treats it as the update of
// the field attributes.
var ChangeTypes = map[string]bool{
	"create": true, "update": true, "delete": true, "status_change": true,
	"attribute_change": true, "type_changed": true, "lifecycle_transition": true,
	"location_change": true, "movement": true, "override": true,
	"reconciliation_decision": true, "parent_child_change": true,
	"assignment": true, "relationship_change": true, "template_migration": true,
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

// SourceCISnapshot is the trail element of a ci_snapshot (WP-312): the
// replay of a CI starts with the last snapshot before the point in time.
const SourceCISnapshot = "ci_snapshot"

// Replay reconstructs the field state from an ordered change trail (oldest
// first), as the writers of the CI changed the row (AUD-08, WP-312):
//
//   - an element of the source ci_snapshot sets the whole state (its new
//     value is the stored document, snapshotDocument);
//   - "create" seeds the state with its new_value object;
//   - "delete" marks the entity as deleted; the deletion of a CI
//     (ci.PGRepository.Delete) takes the status of its new_value and
//     releases the asset link, the deletion by a merge (field
//     merged_into_id) names the survivor;
//   - the restore (field deleted_at, change kind restored; update of
//     RestoreDeleted and reconciliation_decision of Restore) clears the mark
//     and brings back the status before the deletion (active for none or
//     decommissioned) and the released asset link, like the restore of the
//     row; another change of kind restored (the unmerge) clears the mark;
//   - overrides and the other reconciliation decisions (merge, unmerge)
//     are no field changes of the entity and are not replayed;
//   - every other change with a field name overwrites that field.
func Replay(changes []Change) StateSnapshot {
	snapshot := StateSnapshot{Fields: map[string]any{}}
	for i := range changes {
		change := &changes[i]
		if change.Source == SourceCISnapshot {
			snapshot.setDocument(change.NewValue)
			continue
		}
		switch {
		case change.ChangeType == "create":
			if obj, ok := change.NewValue.(map[string]any); ok {
				for k, v := range obj {
					snapshot.Fields[k] = v
				}
			}
			snapshot.Deleted = false
		case change.ChangeType == "delete":
			snapshot.replayDelete(change)
		case change.ChangeKind == "restored" && change.FieldName == "deleted_at":
			snapshot.replayRestore()
		case change.ChangeType == "override", change.ChangeType == "reconciliation_decision":
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
			snapshot.MergedIntoID = ""
		}
	}
	return snapshot
}

// replayDelete marks the entity as deleted. The merge names the survivor;
// the deletion of a CI decommissions it and releases its asset link and
// keeps what a restore brings back.
func (s *StateSnapshot) replayDelete(change *Change) {
	s.Deleted = true
	newValue, _ := change.NewValue.(map[string]any)
	if change.FieldName == "merged_into_id" {
		s.MergedIntoID, _ = newValue["merged_into_id"].(string)
		return
	}
	if change.EntityType != "ci" || newValue == nil {
		return
	}
	oldValue, _ := change.OldValue.(map[string]any)
	s.restore.Status, _ = oldValue["status"].(string)
	s.restore.AssetID, _ = newValue["unlinked_asset_id"].(string)
	if status, ok := newValue["status"]; ok {
		s.Fields["status"] = status
	}
	delete(s.Fields, "asset_id")
}

// replayRestore brings a deleted CI back as ci.PGRepository.restore does:
// a decommissioned status becomes the one before the deletion (active for
// none or decommissioned), a released asset link comes back.
func (s *StateSnapshot) replayRestore() {
	s.Deleted = false
	delete(s.Fields, "deleted_at")
	if s.Fields["status"] == "decommissioned" {
		status := s.restore.Status
		if status == "" || status == "decommissioned" {
			status = "active"
		}
		s.Fields["status"] = status
	}
	if s.restore.AssetID != "" && s.Fields["asset_id"] == nil {
		s.Fields["asset_id"] = s.restore.AssetID
	}
	s.restore = restoreState{}
}

// snapshotDocument is the stored state of a ci_snapshot (WP-312, format
// SnapshotSchemaVersion): the fields, the deletion mark, the survivor of a
// merge and what a restore of a deleted CI brings back.
type snapshotDocument struct {
	Fields       map[string]any `json:"fields"`
	Deleted      bool           `json:"deleted"`
	MergedIntoID string         `json:"merged_into_id,omitempty"`
	Restore      *restoreState  `json:"restore,omitempty"`
}

// document returns the stored form of the state.
func (s *StateSnapshot) document() snapshotDocument {
	doc := snapshotDocument{Fields: s.Fields, Deleted: s.Deleted, MergedIntoID: s.MergedIntoID}
	if doc.Fields == nil {
		doc.Fields = map[string]any{}
	}
	if s.restore != (restoreState{}) {
		r := s.restore
		doc.Restore = &r
	}
	return doc
}

// setDocument replaces the state with a stored document (its JSON form).
func (s *StateSnapshot) setDocument(value any) {
	doc, _ := value.(map[string]any)
	s.Fields = map[string]any{}
	if fields, ok := doc["fields"].(map[string]any); ok {
		for k, v := range fields {
			s.Fields[k] = v
		}
	}
	s.Deleted, _ = doc["deleted"].(bool)
	s.MergedIntoID, _ = doc["merged_into_id"].(string)
	s.restore = restoreState{}
	if r, ok := doc["restore"].(map[string]any); ok {
		s.restore.Status, _ = r["status"].(string)
		s.restore.AssetID, _ = r["asset_id"].(string)
	}
}

// NotHistorizedCIFields are the members of a CI that AUD-03 does not
// historize: the last sighting, heartbeats, interface counters and metric
// values. Its state at a point in time does not hold them (WP-793).
var NotHistorizedCIFields = []string{"last_seen_at", "heartbeats", "interface_counters", "metrics"}

// EdgeState is an edge of a CI at a point in time (WP-793): the edge as
// its last relationship_change row up to then recorded it (id, rel_type,
// source_ci_id, target_ci_id and its other members), or, for an edge of
// the base snapshot without such a row, its id, type and endpoints.
type EdgeState map[string]any

// Members of an edge state.
const (
	edgeID       = "id"
	edgeRelType  = "rel_type"
	edgeSourceCI = "source_ci_id"
	edgeTargetCI = "target_ci_id"
)

// EdgeChange is one row of relationship_change in the edge replay of a CI
// (ReplayEdges): the edge, its kind, its endpoints after the change and
// the edge after it (nil for removed and suppressed).
type EdgeChange struct {
	RelationshipID string
	Kind           string
	RelType        string
	SourceCIID     string
	TargetCIID     string
	After          map[string]any
}

// ReplayEdges reconstructs the edges of the CI ciID (WP-793): base are the
// edges of its snapshot, changes the rows of relationship_change after it,
// oldest first. removed and suppressed drop an edge; every other kind
// (created, updated, verification_changed, refreshed, rewired, restored)
// sets it to the edge after the change. An edge whose endpoints no longer
// include the CI (rewired away by an unmerge or a merge) drops out. The
// result is ordered by edge id.
func ReplayEdges(ciID string, base []EdgeState, changes []EdgeChange) []EdgeState {
	edges := make(map[string]EdgeState, len(base))
	for _, e := range base {
		if id, _ := e[edgeID].(string); id != "" {
			edges[id] = e
		}
	}
	for i := range changes {
		c := &changes[i]
		if c.Kind == "removed" || c.Kind == "suppressed" || (c.SourceCIID != ciID && c.TargetCIID != ciID) {
			delete(edges, c.RelationshipID)
			continue
		}
		e := EdgeState{}
		for k, v := range c.After {
			e[k] = v
		}
		e[edgeID], e[edgeRelType], e[edgeSourceCI], e[edgeTargetCI] = c.RelationshipID, c.RelType, c.SourceCIID, c.TargetCIID
		edges[c.RelationshipID] = e
	}
	out := make([]EdgeState, 0, len(edges))
	for _, e := range edges {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i][edgeID].(string)
		b, _ := out[j][edgeID].(string)
		return a < b
	})
	return out
}
