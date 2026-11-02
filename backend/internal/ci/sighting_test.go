package ci

import (
	"context"
	"testing"
	"time"
)

// TestUpdateSightingRevivesUnknownCI covers the decision of a sighting
// (UpdateRequest.Sighting, WP-683, F-412; REC-07, REC-09) on the current
// row: an unknown CI is active again when the sighting is newer than its
// last one, whatever the caller read before; an older sighting, a CI in
// another status and a write that is no sighting leave the status as it
// is. The PostgreSQL repository makes the same decision on the locked row
// (TestOfflineDetectorSkipsCISeenDuringRun in internal/lifecycle).
func TestUpdateSightingRevivesUnknownCI(t *testing.T) {
	ctx := context.Background()
	last := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	newer, older := last.Add(time.Hour).Format(time.RFC3339Nano), last.Add(-time.Hour).Format(time.RFC3339Nano)
	for _, tc := range []struct {
		name, status, seen string
		sighting           bool
		want               string
	}{
		{"unknown, newer sighting", StatusUnknown, newer, true, StatusActive},
		{"unknown, older sighting", StatusUnknown, older, true, StatusUnknown},
		{"unknown, no sighting", StatusUnknown, newer, false, StatusUnknown},
		{"maintenance, newer sighting", StatusMaintenance, newer, true, StatusMaintenance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewMemoryRepository()
			seen := last
			item := &Item{OrganizationID: "org-1", CITypeID: "server", Name: "s", Status: tc.status, LastSeenAt: &seen}
			if err := repo.Create(ctx, item); err != nil {
				t.Fatal(err)
			}
			got, err := repo.Update(ctx, "org-1", item.ID, UpdateRequest{LastSeenAt: &tc.seen, Sighting: tc.sighting})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Errorf("status %s, want %s", got.Status, tc.want)
			}
		})
	}
	// A malformed time of a sighting is the caller's error.
	repo := NewMemoryRepository()
	item := &Item{OrganizationID: "org-1", CITypeID: "server", Name: "s", Status: StatusUnknown}
	if err := repo.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	bad := "yesterday"
	if _, err := repo.Update(ctx, "org-1", item.ID, UpdateRequest{LastSeenAt: &bad, Sighting: true}); err == nil {
		t.Error("sighting with a malformed last_seen_at accepted")
	}
}
