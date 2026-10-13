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
	"github.com/go-chi/chi/v5"
)

// TestTopologyConfidence covers WP-158 (TOP-01) against PostgreSQL: a
// switch record with LLDP, FDB, trunk, ARP and an unknown neighbor key
// gives LLDP edges with confidence 1.0 and access-port FDB edges with 0.6
// in the confidence column, nothing for the trunk and ARP, and an
// unknown_relationship review instead of a connected_to edge.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTopologyConfidence(t *testing.T) {
	f := scopetest.Seed(t, "a9")
	bg := context.Background()
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	peers := map[string]string{
		"lldp-peer": "00:1b:21:00:00:01", "fdb-host": "00:1b:21:00:00:02", "trunk-1": "00:1b:21:00:00:03",
		"arp-host": "00:1b:21:00:00:04", "odd-peer": "00:1b:21:00:00:05",
	}
	ids := map[string]string{}
	for name, mac := range peers {
		id := f.ID()
		ids[name] = id
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci (id, organization_id, ci_type_id, name, status, primary_mac) VALUES ($1, $2, $3, $4, 'active', $5)`,
			id, f.OrgA, serverType, "wp158-"+name, mac); err != nil {
			t.Fatal(err)
		}
	}

	repo := discovery.NewPGRepository(f.App)
	rels := relationship.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(repo, ci.NewPGRepository(f.App), rels).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "wp158-core", IP: "192.0.2.158", Serial: "WP158SW"}, time.Now().Add(-time.Minute))
	n := func(proto, port, mac string) plugins.DeviceNeighbor {
		return plugins.DeviceNeighbor{Protocol: proto, LocalInterface: port, RemoteMAC: mac}
	}
	rec.Neighbors = []plugins.DeviceNeighbor{
		n("lldp", "Gi1/0/1", peers["lldp-peer"]),
		n("fdb", "Gi1/0/1", peers["odd-peer"]), // behind the LLDP neighbor: no FDB edge
		n("fdb", "Gi1/0/5", peers["fdb-host"]),
		n("fdb", "Gi1/0/48", peers["trunk-1"]), n("fdb", "Gi1/0/48", "00:1b:21:00:01:01"), n("fdb", "Gi1/0/48", "00:1b:21:00:01:02"),
		n("fdb", "Gi1/0/48", "00:1b:21:00:01:03"), n("fdb", "Gi1/0/48", "00:1b:21:00:01:04"),
		n("arp", "", peers["arp-host"]),
	}
	body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
	if err != nil {
		t.Fatal(err)
	}
	// An unknown key arrives over the legacy items contract.
	legacy := map[string]any{"collector_id": "c", "items": []map[string]any{{"ci_type_name": "switch", "name": "wp158-core",
		"serial_number": "WP158SW", "management_ip": "192.0.2.158",
		"relationships": []map[string]any{{"target_mac": peers["odd-peer"], "type_key": "vxlan_peer"}}}}}
	legacyBody, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for path, b := range map[string][]byte{"/api/v1/discovery/ingest": body} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b)).WithContext(ctx))
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ingest/bulk", bytes.NewReader(legacyBody)).WithContext(ctx))
	if w.Code != http.StatusAccepted {
		t.Fatalf("legacy ingest: %d %s", w.Code, w.Body.String())
	}

	edges := map[string]float64{}
	rows, err := f.Admin.Query(bg, `SELECT target_ci_id::text, rel_type, confidence::float8 FROM ci_relationship
		WHERE organization_id = $1 AND source = 'discovery'`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var target, relType string
		var confidence float64
		if err = rows.Scan(&target, &relType, &confidence); err != nil {
			t.Fatal(err)
		}
		if relType != "connected_to" {
			t.Errorf("edge type %s", relType)
		}
		edges[target] = confidence
	}
	rows.Close()
	if len(edges) != 2 || edges[ids["lldp-peer"]] != 1.0 || edges[ids["fdb-host"]] != 0.6 {
		t.Errorf("edges = %v (lldp-peer %s, fdb-host %s)", edges, ids["lldp-peer"], ids["fdb-host"])
	}
	var reviews int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'unknown_relationship'
		AND payload->>'type_key' = 'vxlan_peer' AND payload->>'target_ci_id' = $2`, f.OrgA, ids["odd-peer"]).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 {
		t.Errorf("unknown_relationship reviews = %d", reviews)
	}
}
