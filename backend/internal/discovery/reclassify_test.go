package discovery

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestDiscoveryRaisesGenericDevice covers the discovery side of CI-11
// (WP-172): a generic_device CI seen again with a specific type is raised
// through the type change service; a specific type is never changed by
// discovery.
func TestDiscoveryRaisesGenericDevice(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	for _, key := range []string{"generic_device", "switch", "router"} {
		discoveryRepo.SeedCIType(key, key)
	}
	ciRepo := ci.NewMemoryRepository()
	for _, key := range []string{"generic_device", "switch", "router"} {
		ciRepo.SetType(key, key)
	}
	h := NewHandler(discoveryRepo, ciRepo)
	ctx := discoveryTenantCtx(httptest.NewRequest(http.MethodGet, "/", http.NoBody)).Context()
	report := func(ciType string) []ci.Item {
		t.Helper()
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: ciType, Name: "dev-7", IP: "10.0.0.7",
			MAC: "aa:bb:cc:00:00:07", Serial: "SN-RECLASS-7"}, time.Now())
		if _, err := h.processItems(ctx, "org-1", []IngestItem{itemFromRecord(&rec)}); err != nil {
			t.Fatal(err)
		}
		items, _, err := ciRepo.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	first := report("generic_device")
	if len(first) != 1 || first[0].CITypeID != "generic_device" {
		t.Fatalf("first sighting: %+v", first)
	}
	if raised := report("switch"); len(raised) != 1 || raised[0].ID != first[0].ID || raised[0].CITypeID != "switch" {
		t.Errorf("generic_device not raised: %+v", raised)
	}
	// A specific type is never changed by discovery (REC-02 does not even
	// match the serial number across specific types).
	report("router")
	if kept, err := ciRepo.GetByID(ctx, "org-1", first[0].ID); err != nil || kept.CITypeID != "switch" {
		t.Errorf("discovery changed a specific type: %+v %v", kept, err)
	}
}
