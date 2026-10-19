// Phase-1 acceptance audit, group T4-collector-load – REGRESSION test (must
// pass).
//
// WP-159 (TOP-02, REL-08), WP-158 (TOP-01). High test gap "Re-sighting
// through the real ingest handler and the other-source rule": an LLDP
// neighbour ingested through POST /api/v1/discovery/ingest on PostgreSQL
// creates a discovery edge with confidence 1.0; after its timestamps are set
// back 9 days, the next ingest of the same neighbour refreshes last_seen_at
// and last_confirmed_at (so the TOP-02 cleanup keeps it) without creating a
// second edge. An agent-source edge between two other CIs keeps its
// last_seen_at, confidence and attributes when discovery reports the same
// neighbourship (REL-08: discovery never changes another source's edge).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditDiscoveryResightingThroughIngest' ./internal/discovery/
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
)

func TestAuditDiscoveryResightingThroughIngest(t *testing.T) {
	f := scopetest.Seed(t, "df")
	bg := context.Background()
	mac := map[string]string{"sw1": "00:1b:21:23:00:01", "peer1": "00:1b:21:23:00:02", "sw2": "00:1b:21:23:00:03", "peer2": "00:1b:21:23:00:04"}
	ids := map[string]string{}
	for name, m := range mac {
		id := f.CI(t, f.OrgA, "", "audit-resight-"+name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = $2 WHERE id = $1`, id, m); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ingest := func(sw, ip, peer string) {
		t.Helper()
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "audit-resight-" + sw, IP: ip, MAC: mac[sw]}, time.Now().Add(-time.Minute))
		rec.Neighbors = []plugins.DeviceNeighbor{{Protocol: "lldp", LocalInterface: "Gi1/0/1", RemoteMAC: mac[peer]}}
		body, _ := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	type edgeRow struct {
		id, source      string
		count           int
		seen, confirmed time.Time
		confidence      float64
		attributes      string
	}
	edge := func(src, dst string) edgeRow {
		t.Helper()
		var e edgeRow
		if err := f.Admin.QueryRow(bg, `SELECT count(*) OVER (), id::text, source, last_seen_at, COALESCE(last_confirmed_at, 'epoch'),
				COALESCE(confidence, 0)::float8, attributes::text
			FROM ci_relationship WHERE organization_id = $1 AND rel_type = 'connected_to'
			  AND ((source_ci_id = $2 AND target_ci_id = $3) OR (source_ci_id = $3 AND target_ci_id = $2))
			ORDER BY created_at LIMIT 1`, f.OrgA, ids[src], ids[dst]).Scan(&e.count, &e.id, &e.source, &e.seen, &e.confirmed, &e.confidence, &e.attributes); err != nil {
			t.Fatalf("edge %s-%s: %v", src, dst, err)
		}
		return e
	}

	t.Run("re-sighting refreshes the discovery edge", func(t *testing.T) {
		ingest("sw1", "192.0.2.231", "peer1")
		first := edge("sw1", "peer1")
		if first.source != "discovery" || first.confidence != 1.0 || first.count != 1 {
			t.Fatalf("LLDP edge = %+v, want one discovery edge with confidence 1.0", first)
		}
		if _, err := f.Admin.Exec(bg, `UPDATE ci_relationship SET last_seen_at = now() - interval '9 days', last_confirmed_at = now() - interval '9 days'
			WHERE id = $1`, first.id); err != nil {
			t.Fatal(err)
		}
		ingest("sw1", "192.0.2.231", "peer1")
		again := edge("sw1", "peer1")
		if again.id != first.id || again.count != 1 {
			t.Errorf("re-sighting created another edge: %+v (first %s)", again, first.id)
		}
		if time.Since(again.seen) > time.Minute || time.Since(again.confirmed) > time.Minute || again.confidence != 1.0 {
			t.Errorf("re-sighting did not refresh the edge: last_seen_at=%s last_confirmed_at=%s confidence=%v", again.seen, again.confirmed, again.confidence)
		}
	})

	t.Run("an agent edge is left untouched", func(t *testing.T) {
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				verification_state, last_seen_at, last_confirmed_at, confidence, attributes)
			VALUES ($1, $2, $3, 'connected_to', 'agent', 'unverified', now() - interval '9 days', now() - interval '9 days', 0.5, '{"audit":"agent"}')`,
			f.OrgA, ids["sw2"], ids["peer2"]); err != nil {
			t.Fatal(err)
		}
		before := edge("sw2", "peer2")
		ingest("sw2", "192.0.2.232", "peer2")
		after := edge("sw2", "peer2")
		if after.id != before.id || after.source != "agent" || !after.seen.Equal(before.seen) || !after.confirmed.Equal(before.confirmed) ||
			after.confidence != 0.5 || after.attributes != before.attributes {
			t.Errorf("REL-08 violated: discovery changed the agent edge: before %+v, after %+v", before, after)
		}
	})
}
