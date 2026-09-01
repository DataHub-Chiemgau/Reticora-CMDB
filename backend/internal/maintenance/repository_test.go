package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

func TestMaintenanceWindowNotifyDerivesClients(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SetCIClient("ci-1", "client-a")
	repo.SetCIClient("ci-2", "client-a") // same client twice → one notification
	repo.SetCIClient("ci-3", "client-b")
	w := &Window{OrganizationID: "org-1", Title: "Core upgrade", StartsAt: time.Now(), EndsAt: time.Now().Add(2 * time.Hour), CIIDs: []string{"ci-1", "ci-2", "ci-3"}}
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	notifs, err := repo.NotifyClients(context.Background(), "org-1", w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notifs) != 2 {
		t.Fatalf("expected 2 client notifications (deduped), got %d", len(notifs))
	}
	clients := map[string]bool{}
	for _, n := range notifs {
		clients[n.ClientID] = true
		if n.Status != "sent" {
			t.Errorf("expected sent, got %s", n.Status)
		}
	}
	if !clients["client-a"] || !clients["client-b"] {
		t.Errorf("expected clients a+b notified, got %v", clients)
	}
	// tenant isolation
	if _, err := repo.NotifyClients(context.Background(), "org-2", w.ID); err == nil {
		t.Fatal("cross-tenant notify must fail")
	}
	// status filter
	list, total, _ := repo.List(context.Background(), "org-1", FilterParams{Status: "scheduled"}, api.PaginationParams{Limit: 10})
	if total != 1 || list[0].ID != w.ID {
		t.Fatalf("expected scheduled window, got %d", total)
	}
}
