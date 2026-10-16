// Phase-1 acceptance audit, group T2-auth-authz: regression test of the
// licensed CI limit on the NATS edge ingest path against PostgreSQL.
//
//   - WP-073 (ENT-03, CH21, E-22), gap (high) "Unlicensed handling on the
//     NATS edge ingest path": with max_cis reached, a new device sent over
//     the collector edge (EdgeIngest with the system context the NATS
//     endpoint builds) is held as an unlicensed_ci review item and the edge
//     reply is 202 without a new CI; a known device is still updated; raising
//     the limit through the entitlement service adopts the held device.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_02?sslmode=disable go test -count=1 -v -run 'TestT2EdgeIngestUnlicensed' ./internal/discovery/
package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

func TestT2EdgeIngestUnlicensed(t *testing.T) {
	f := scopetest.Seed(t, "d1")
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	ciRepo := ci.NewPGRepository(f.App)
	svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
	h := NewHandler(NewPGRepository(f.App), ciRepo).WithLimits(svc)
	svc.WithUnlicensed(h)

	countCIs := func() int {
		t.Helper()
		_, total, err := ciRepo.List(orgCtx, f.OrgA, ci.FilterParams{}, api.PaginationParams{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		return total
	}
	setLimit := func(limit int) {
		t.Helper()
		if _, err := svc.Grant(orgCtx, entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureCMDBCore,
			Plan: entitlement.PlanEssential, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxCIs: int64(limit)}}); err != nil {
			t.Fatal(err)
		}
	}
	edge := func(records ...DeviceRecord) EdgeReply {
		t.Helper()
		body, err := json.Marshal(DeviceRecordBatch{Records: records})
		if err != nil {
			t.Fatal(err)
		}
		// The NATS endpoint calls the handler with the organization's
		// system context (ServeCollectorEdge).
		return h.EdgeIngest(systemContext(bg, f.OrgA), f.OrgA, "t2-edge-collector", body)
	}
	device := func(serial, model string) DeviceRecord {
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "t2-edge-" + serial, IP: "192.0.2.77",
			Serial: serial, Model: model}, time.Now().UTC().Add(-time.Minute))
	}
	modelOf := func(serial string) string {
		t.Helper()
		var model string
		if err := f.Admin.QueryRow(bg, `SELECT model FROM ci WHERE organization_id = $1 AND serial_number = $2`, f.OrgA, serial).Scan(&model); err != nil {
			t.Fatalf("CI %s: %v", serial, err)
		}
		return model
	}
	openUnlicensed := func() int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'unlicensed_ci' AND status = 'open'`, f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	base := countCIs()
	setLimit(base + 1)
	if r := edge(device("T2-EDGE-1", "m1")); r.Status != http.StatusAccepted || r.Result == nil || r.Result.Created != 1 {
		t.Fatalf("first device within the limit: %+v", r)
	}

	// The limit is reached: a new device is held, the known one updated.
	r := edge(device("T2-EDGE-1", "m2"), device("T2-EDGE-2", "m1"))
	if r.Status != http.StatusAccepted || r.Result == nil || r.Result.Updated != 1 || r.Result.Unlicensed != 1 || r.Result.Created != 0 {
		t.Fatalf("edge ingest at max_cis: %+v (result %+v), want 202 with 1 updated and 1 unlicensed", r, r.Result)
	}
	if got := countCIs(); got != base+1 {
		t.Errorf("CIs after the edge ingest at max_cis: %d, want %d (no new CI)", got, base+1)
	}
	if got := modelOf("T2-EDGE-1"); got != "m2" {
		t.Errorf("known device model %q, want m2 (still updated over the limit)", got)
	}
	if got := openUnlicensed(); got != 1 {
		t.Errorf("open unlicensed_ci review items %d, want 1", got)
	}

	// Raising the limit adopts the held device.
	setLimit(base + 2)
	if got := countCIs(); got != base+2 {
		t.Errorf("CIs after raising the limit: %d, want %d (held device adopted)", got, base+2)
	}
	if got := modelOf("T2-EDGE-2"); got != "m1" {
		t.Errorf("adopted device model %q, want m1", got)
	}
	if got := openUnlicensed(); got != 0 {
		t.Errorf("open unlicensed_ci review items after the adoption: %d, want 0", got)
	}
}
