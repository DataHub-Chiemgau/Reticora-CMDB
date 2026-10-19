//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT test (must fail
// until the defect is fixed).
//
// WP-161 (TOP-03 power chain, SIM-01 fleet). Finding P2 "powered_by
// redundancy flag depends on batch and lane order and is never corrected
// (wrong in the real stack)": PowerEdges computes attributes.redundant from
// the feeds visible in the current batch, and upsertRelationship
// (topology.go) only re-confirms an existing edge without updating its
// attributes. When the two PDUs of a dual-fed server arrive in different
// batches (or ingest lanes), the first PDU's edges stay redundant=false
// forever, even after that PDU is reported again (data integrity).
//
// The simulated fleet (collector/plugins/simulate) has 16 servers fed by
// sim-pdu-a (PSU1) and sim-pdu-b (PSU2): all 32 server -> PDU edges must
// carry redundant=true regardless of delivery order. Org A gets the fleet in
// one batch (control), org B gets sim-pdu-b in a later batch and then
// sim-pdu-a again. Records go through the real ingest handler on PostgreSQL.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -p 1 -run 'TestAuditPowerRedundancyIndependentOfBatchOrder' ./internal/discovery/
package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/simulate"
)

func TestAuditPowerRedundancyIndependentOfBatchOrder(t *testing.T) {
	f := scopetest.Seed(t, "d9")
	bg := context.Background()
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).RegisterRoutes(mux)
	ingest := func(org string, recs []plugins.DeviceRecord) {
		t.Helper()
		ctx := tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: recs})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	at := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	only := func(recs []plugins.DeviceRecord, keep func(host string) bool) []plugins.DeviceRecord {
		var out []plugins.DeviceRecord
		for _, r := range recs {
			if keep(r.Identity.Hostname) {
				out = append(out, r)
			}
		}
		return out
	}

	// Org A: the whole fleet in one batch.
	ingest(f.OrgA, simulate.Generate(at))
	// Org B: everything but sim-pdu-b, then sim-pdu-b, then sim-pdu-a again.
	ingest(f.OrgB, only(simulate.Generate(at), func(h string) bool { return h != "sim-pdu-b" }))
	ingest(f.OrgB, only(simulate.Generate(at), func(h string) bool { return h == "sim-pdu-b" }))
	ingest(f.OrgB, only(simulate.Generate(at.Add(time.Minute)), func(h string) bool { return h == "sim-pdu-a" }))

	type count struct{ total, redundant int }
	edges := func(org string) map[string]count {
		rows, err := f.Admin.Query(bg, `SELECT p.name, count(*), count(*) FILTER (WHERE r.attributes->>'redundant' = 'true')
			FROM ci_relationship r JOIN ci s ON s.id = r.source_ci_id JOIN ci p ON p.id = r.target_ci_id
			WHERE r.organization_id = $1 AND r.rel_type = 'powered_by' AND s.name LIKE 'sim-srv-%'
			GROUP BY 1`, org)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]count{}
		for rows.Next() {
			var pdu string
			var c count
			if err := rows.Scan(&pdu, &c.total, &c.redundant); err != nil {
				t.Fatal(err)
			}
			out[pdu] = c
		}
		return out
	}
	for _, tc := range []struct{ name, org string }{
		{"control: one batch (org A)", f.OrgA},
		{"sim-pdu-b in a later batch, sim-pdu-a re-reported (org B)", f.OrgB},
	} {
		got := edges(tc.org)
		for _, pdu := range []string{"sim-pdu-a", "sim-pdu-b"} {
			c := got[pdu]
			if c.total != 16 || c.redundant != 16 {
				t.Errorf("%s: server -> %s powered_by edges = %d, redundant=true on %d; want 16/16 (every sim server is fed by both PDUs)",
					tc.name, pdu, c.total, c.redundant)
			}
		}
	}
}
