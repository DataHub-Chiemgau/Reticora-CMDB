package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/simulate"
	"github.com/go-chi/chi/v5"
)

// TestSimulatedFleetIngest covers WP-162 (SIM-02) end to end against
// PostgreSQL: the 30 simulated devices go through the real ingest handler
// and become 30 CIs with LLDP and power edges; a second run of the
// deterministic fleet matches the same CIs instead of creating new ones.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSimulatedFleetIngest(t *testing.T) {
	f := scopetest.Seed(t, "ad")
	bg := context.Background()
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	ingest := func(at time.Time) {
		t.Helper()
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: simulate.Generate(at)})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, query, f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	const cis = `SELECT count(*) FROM ci WHERE organization_id = $1 AND name LIKE 'sim-%'`
	const edges = `SELECT count(*) FROM ci_relationship WHERE organization_id = $1 AND source = 'discovery' AND rel_type = `

	at := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	ingest(at)
	if n := count(cis); n != simulate.DeviceCount {
		t.Fatalf("first run: %d simulated CIs, want %d", n, simulate.DeviceCount)
	}
	// The second run finds every device of the first one.
	ingest(at.Add(time.Minute))
	if n := count(cis); n != simulate.DeviceCount {
		t.Fatalf("second run: %d simulated CIs, want %d", n, simulate.DeviceCount)
	}
	if n := count(edges + `'connected_to'`); n == 0 {
		t.Error("no LLDP edges from the simulated fleet")
	}
	// 16 servers on two PDUs each, two PDUs on the UPS.
	if n := count(edges + `'powered_by'`); n != 34 {
		t.Errorf("powered_by edges = %d, want 34", n)
	}
}
