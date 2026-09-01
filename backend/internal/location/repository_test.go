package location

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

func TestLocationHistoryOrder(t *testing.T) {
	repo := NewMemoryRepository()
	for _, lat := range []float64{48.0, 48.1, 48.2} {
		if err := repo.Record(context.Background(), &Entry{OrganizationID: "org-1", AssetID: "a1", Lat: lat, Lon: 11.0}); err != nil {
			t.Fatal(err)
		}
	}
	items, total, _ := repo.History(context.Background(), "org-1", "a1", api.PaginationParams{Limit: 10})
	if total != 3 || len(items) != 3 {
		t.Fatalf("expected 3 entries, got %d", total)
	}
	// newest first
	if items[0].RecordedAt.Before(items[2].RecordedAt) {
		t.Fatal("expected newest first")
	}
	// tenant isolation
	_, total, _ = repo.History(context.Background(), "org-2", "a1", api.PaginationParams{Limit: 10})
	if total != 0 {
		t.Fatal("cross-tenant history must be empty")
	}
}
