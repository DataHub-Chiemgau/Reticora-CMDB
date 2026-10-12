package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
)

type recordedSync struct {
	ciID     string
	observed []ipam.ObservedInterface
}

type syncRecorder struct{ calls []recordedSync }

func (s *syncRecorder) SyncDiscoveredInterfaces(_ context.Context, _, ciID string, observed []ipam.ObservedInterface) (ipam.SyncResult, error) {
	s.calls = append(s.calls, recordedSync{ciID, observed})
	return ipam.SyncResult{}, nil
}

// TestSightingUpdatesLastSeenAndInterfaces covers WP-090 (REC-07, AUD-03,
// NET-01) without a database: a sighting sets last_seen_at and brings an
// unknown CI back to active; a record that reports interfaces hands them
// with their addresses to the interface sync, a record without interfaces
// does not (it cannot miss any).
func TestSightingUpdatesLastSeenAndInterfaces(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	device := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "ls", SerialNumber: "LS-90", Status: "unknown",
		Attributes: map[string]any{}}
	if err := cis.Create(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	syncer := &syncRecorder{}
	mux := chi.NewRouter()
	NewHandler(repo, cis).WithInterfaces(syncer).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})

	before := time.Now().Add(-time.Second)
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "ls", IP: "192.0.2.90", Serial: "LS-90"}, time.Now())
	rec.Interfaces = []plugins.DeviceInterface{
		{Name: "eth0", MAC: "00:1B:21:00:00:90", OperStatus: "up", IPs: []plugins.DeviceIP{{Address: "192.0.2.90", PrefixLength: 24}}},
		{Name: "eth1"},
	}
	if resp := postRecords(t, mux, ctx, rec); resp.Updated != 1 {
		t.Fatalf("sighting: %+v", resp)
	}
	got, err := cis.GetByID(context.Background(), "org-1", device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "active" || got.LastSeenAt == nil || got.LastSeenAt.Before(before) {
		t.Errorf("after the sighting: status %q, last_seen_at %v", got.Status, got.LastSeenAt)
	}
	if len(syncer.calls) != 1 || syncer.calls[0].ciID != device.ID || len(syncer.calls[0].observed) != 2 {
		t.Fatalf("interface sync: %+v", syncer.calls)
	}
	eth0 := syncer.calls[0].observed[0]
	if eth0.Name != "eth0" || eth0.MAC != "00:1b:21:00:00:90" || len(eth0.IPs) != 1 || eth0.IPs[0] != "192.0.2.90/24" || !eth0.IsManagement {
		t.Errorf("eth0: %+v", eth0)
	}

	// A sweep reports no interfaces: nothing to sync, nothing missed.
	sweep := plugins.RecordFromResult("sweep", &plugins.Result{CIType: "server", Name: "ls", IP: "192.0.2.90", Serial: "LS-90"}, time.Now())
	postRecords(t, mux, ctx, sweep)
	if len(syncer.calls) != 1 {
		t.Errorf("record without interfaces synced: %+v", syncer.calls)
	}
}
