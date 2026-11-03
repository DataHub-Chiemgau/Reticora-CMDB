package history

import (
	"testing"
	"time"
)

// ciTrailRows is the trail of a CI as the CI writers record it: create,
// a rename, the deletion (status and asset link released) and a restore of
// the given change type (update: RestoreDeleted, reconciliation_decision:
// Restore).
func ciTrailRows(restoreType string) []Change {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return []Change{
		{EntityType: "ci", ChangeType: "create", ChangeKind: "created", CreatedAt: at, NewValue: map[string]any{
			"name": "c1", "status": "maintenance", "asset_id": "a-1", "attributes": map[string]any{"rack": "r1"},
		}},
		{EntityType: "ci", ChangeType: "update", ChangeKind: "updated", FieldName: "name", OldValue: "c1", NewValue: "c2", CreatedAt: at.Add(time.Hour)},
		{EntityType: "ci", ChangeType: "delete", ChangeKind: "deleted", CreatedAt: at.Add(2 * time.Hour),
			OldValue: map[string]any{"name": "c2", "status": "maintenance", "asset_id": "a-1"},
			NewValue: map[string]any{"status": "decommissioned", "unlinked_asset_id": "a-1", "asset_primary": true}},
		{EntityType: "ci", ChangeType: restoreType, ChangeKind: "restored", FieldName: "deleted_at", OldValue: "deleted", CreatedAt: at.Add(3 * time.Hour)},
	}
}

// TestReplayFollowsDeleteAndRestore covers AUD-08 (WP-312): the replay of
// create, update, delete and restore gives the state of each point in time
// with deleted, status and asset_id as the CI writers left the row, for
// both restore paths; the fields of the deletion that are no CI fields do
// not appear.
func TestReplayFollowsDeleteAndRestore(t *testing.T) {
	for _, restoreType := range []string{"update", "reconciliation_decision"} {
		trail := ciTrailRows(restoreType)
		for _, c := range []struct {
			rows                int
			name, status, asset any
			deleted             bool
		}{
			{1, "c1", "maintenance", "a-1", false},
			{2, "c2", "maintenance", "a-1", false},
			{3, "c2", "decommissioned", nil, true},
			{4, "c2", "maintenance", "a-1", false},
		} {
			s := Replay(trail[:c.rows])
			if s.Fields["name"] != c.name || s.Fields["status"] != c.status || s.Fields["asset_id"] != c.asset || s.Deleted != c.deleted {
				t.Errorf("%s, after %d rows: name %v status %v asset_id %v deleted %v, want %v %v %v %v", restoreType, c.rows,
					s.Fields["name"], s.Fields["status"], s.Fields["asset_id"], s.Deleted, c.name, c.status, c.asset, c.deleted)
			}
			for _, k := range []string{"unlinked_asset_id", "asset_primary", "deleted_at"} {
				if _, ok := s.Fields[k]; ok {
					t.Errorf("%s, after %d rows: field %s in the state %v", restoreType, c.rows, k, s.Fields)
				}
			}
		}
	}

	// A CI deleted while decommissioned comes back active, without a
	// released asset link without one.
	trail := []Change{
		{EntityType: "ci", ChangeType: "create", NewValue: map[string]any{"name": "c", "status": "decommissioned"}},
		{EntityType: "ci", ChangeType: "delete", OldValue: map[string]any{"status": "decommissioned"}, NewValue: map[string]any{"status": "decommissioned"}},
		{EntityType: "ci", ChangeType: "update", ChangeKind: "restored", FieldName: "deleted_at", OldValue: "deleted"},
	}
	if s := Replay(trail); s.Fields["status"] != "active" || s.Fields["asset_id"] != nil || s.Deleted {
		t.Errorf("restore of a decommissioned CI: %+v, want active without asset", s)
	}
}

// TestReplayMergeAndSnapshotBase covers the merge deletion and the snapshot
// base of the replay (WP-312): the delete row of a merged victim marks it
// deleted with the survivor; a ci_snapshot element replaces the whole state,
// deletion and survivor included, and later rows apply on top of it.
func TestReplayMergeAndSnapshotBase(t *testing.T) {
	merged := Replay([]Change{
		{EntityType: "ci", ChangeType: "create", NewValue: map[string]any{"name": "victim", "status": "active"}},
		{EntityType: "ci", ChangeType: "delete", ChangeKind: "merged", FieldName: "merged_into_id",
			OldValue: map[string]any{"status": "active"}, NewValue: map[string]any{"merged_into_id": "s-1", "merge_id": "m-1"}},
	})
	if !merged.Deleted || merged.MergedIntoID != "s-1" || merged.Fields["status"] != "active" || merged.Fields["name"] != "victim" {
		t.Errorf("merged victim: %+v, want deleted with the survivor and its fields", merged)
	}
	if _, ok := merged.Fields["merged_into_id"]; ok {
		t.Errorf("merged victim: merged_into_id in the fields %v", merged.Fields)
	}

	// The snapshot replaces what came before it.
	base := []Change{
		{EntityType: "ci", ChangeType: "create", NewValue: map[string]any{"name": "old", "status": "active", "model": "m1"}},
		{EntityType: "ci", Source: SourceCISnapshot, ChangeType: "snapshot", NewValue: map[string]any{
			"fields": map[string]any{"name": "snap", "status": "active"}, "deleted": true, "merged_into_id": "s-2",
		}},
	}
	s := Replay(base)
	if !s.Deleted || s.MergedIntoID != "s-2" || s.Fields["name"] != "snap" || s.Fields["model"] != nil {
		t.Errorf("snapshot base: %+v, want the snapshot state only", s)
	}
	base[1].NewValue = map[string]any{
		"fields": map[string]any{"name": "snap", "status": "decommissioned"}, "deleted": true,
		"restore": map[string]any{"status": "maintenance", "asset_id": "a-9"},
	}
	s = Replay(append(base,
		Change{EntityType: "ci", ChangeType: "update", ChangeKind: "restored", FieldName: "deleted_at", OldValue: "deleted"},
		Change{EntityType: "ci", ChangeType: "update", ChangeKind: "updated", FieldName: "name", NewValue: "after"}))
	if s.Deleted || s.MergedIntoID != "" || s.Fields["status"] != "maintenance" || s.Fields["asset_id"] != "a-9" || s.Fields["name"] != "after" {
		t.Errorf("restore after a snapshot of a deleted CI: %+v, want its status and asset back and the later rename", s)
	}

	// The stored document round-trips through the base.
	doc := s.document()
	var again StateSnapshot
	again.setDocument(map[string]any{"fields": doc.Fields, "deleted": doc.Deleted})
	if again.Fields["name"] != "after" || again.Deleted {
		t.Errorf("document round trip: %+v", again)
	}
}
