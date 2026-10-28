package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
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

// TestTopologyConfidenceIndependentOfOrder covers WP-582 (TOP-01, F-269)
// against PostgreSQL through POST /api/v1/discovery/ingest: two switches
// report the same peer as LLDP neighbor and its MAC in the FDB of an access
// port, sw1 the FDB entry first, sw2 the LLDP neighbor first. Both edges
// get confidence 1.0, once each; a re-ingest of both, sw1 again with the
// FDB entry first and sw2 now with it first too, re-confirms them and
// leaves 1.0 (before the fix sw1 -> peer was 0.6 and a re-ingest with the
// FDB entry first set it back to 0.6).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -v \
//	  -run 'TestTopologyConfidence|TestUnknownRelationshipReviewLifecycle' ./internal/discovery/
func TestTopologyConfidenceIndependentOfOrder(t *testing.T) {
	f := scopetest.Seed(t, "a9")
	bg := context.Background()
	const peerMAC = "00:1b:21:58:20:01"
	peer := f.CI(t, f.OrgA, "", "wp582-peer")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = $2, hostname = 'wp582-peer' WHERE id = $1`, peer, peerMAC); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	fdbEntry := plugins.DeviceNeighbor{Protocol: "fdb", LocalInterface: "Gi1/0/5", RemoteMAC: peerMAC}
	lldpNeighbor := plugins.DeviceNeighbor{Protocol: "lldp", LocalInterface: "GigabitEthernet1/0/1", RemoteHostname: "wp582-peer"}
	ips := map[string]string{"sw1": "192.0.2.181", "sw2": "192.0.2.183"}
	record := func(sw string, at time.Time, fdbFirst bool) plugins.DeviceRecord {
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "wp582-" + sw, IP: ips[sw], Serial: "WP582" + strings.ToUpper(sw)}, at)
		rec.Neighbors = []plugins.DeviceNeighbor{lldpNeighbor, fdbEntry}
		if fdbFirst {
			rec.Neighbors = []plugins.DeviceNeighbor{fdbEntry, lldpNeighbor}
		}
		return rec
	}
	ingest := func(recs ...plugins.DeviceRecord) {
		t.Helper()
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
	type edge struct {
		confidence float64
		confirmed  time.Time
	}
	edges := func() map[string]edge {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT s.name, d.name, r.confidence::float8, COALESCE(r.last_confirmed_at, 'epoch') FROM ci_relationship r
			JOIN ci s ON s.id = r.source_ci_id JOIN ci d ON d.id = r.target_ci_id
			WHERE r.organization_id = $1 AND r.source = 'discovery' AND r.rel_type = 'connected_to'`, f.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]edge{}
		for rows.Next() {
			var s, d string
			var e edge
			if err := rows.Scan(&s, &d, &e.confidence, &e.confirmed); err != nil {
				t.Fatal(err)
			}
			if _, dup := out[s+" -> "+d]; dup {
				t.Errorf("edge %s -> %s twice", s, d)
			}
			out[s+" -> "+d] = e
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	check := func(stage string, confirmedAfter time.Time) {
		t.Helper()
		got := edges()
		if len(got) != 2 {
			t.Errorf("%s: edges = %v, want wp582-sw1 -> wp582-peer and wp582-sw2 -> wp582-peer", stage, got)
		}
		for _, sw := range []string{"wp582-sw1", "wp582-sw2"} {
			e, ok := got[sw+" -> wp582-peer"]
			if !ok || e.confidence != 1.0 {
				t.Errorf("%s: %s -> wp582-peer confidence %v (present %v), want 1.0 (LLDP, F-269)", stage, sw, e.confidence, ok)
			}
			if ok && !e.confirmed.After(confirmedAfter) {
				t.Errorf("%s: %s -> wp582-peer last confirmed %v, not re-confirmed after %v", stage, sw, e.confirmed, confirmedAfter)
			}
		}
	}

	start := time.Now()
	ingest(record("sw1", start.Add(-2*time.Minute), true), record("sw2", start.Add(-2*time.Minute), false))
	check("first ingest (sw1 FDB first, sw2 LLDP first)", time.Time{})

	// Set the confirmation back a day, so the re-ingest must re-confirm.
	if _, err := f.Admin.Exec(bg, `UPDATE ci_relationship SET last_confirmed_at = now() - interval '1 day'
		WHERE organization_id = $1 AND source = 'discovery'`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	reconfirmed := time.Now().Add(-time.Hour)
	ingest(record("sw1", start.Add(-time.Minute), true), record("sw2", start.Add(-time.Minute), true))
	check("re-ingest (both FDB first)", reconfirmed)
}

// TestUnknownRelationshipReviewLifecycle covers WP-582 (TOP-01, open
// evidence of WP-158) against PostgreSQL through the legacy ingest and the
// review API: a neighbor reported with an unknown relationship key opens
// an unknown_relationship review and gives no edge; accept records the
// neighbor as connected_to edge of source discovery; dismiss keeps the edge
// away, and the next ingest of the same key opens no new review.
func TestUnknownRelationshipReviewLifecycle(t *testing.T) {
	f := scopetest.Seed(t, "a9")
	bg := context.Background()
	macs := map[string]string{"accepted": "00:1b:21:58:21:01", "dismissed": "00:1b:21:58:21:02"}
	peers := map[string]string{}
	for name, mac := range macs {
		peers[name] = f.CI(t, f.OrgA, "", "wp582-"+name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = $2 WHERE id = $1`, peers[name], mac); err != nil {
			t.Fatal(err)
		}
	}
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	do := func(path string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx))
		return w
	}
	ingest := func() {
		t.Helper()
		body, err := json.Marshal(map[string]any{"collector_id": "c", "items": []map[string]any{{"ci_type_name": "switch",
			"name": "wp582-leaf", "serial_number": "WP582LEAF", "management_ip": "192.0.2.182",
			"relationships": []map[string]any{
				{"target_mac": macs["accepted"], "type_key": "vxlan_peer"},
				{"target_mac": macs["dismissed"], "type_key": "vxlan_peer"},
			}}}})
		if err != nil {
			t.Fatal(err)
		}
		if w := do("/api/v1/ingest/bulk", body); w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	// reviews returns the ids of the unknown_relationship reviews of a
	// peer by status.
	reviews := func(peer string) map[string][]string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT id::text, status FROM review_item WHERE organization_id = $1
			AND kind = 'unknown_relationship' AND payload->>'target_ci_id' = $2 ORDER BY created_at`, f.OrgA, peers[peer])
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string][]string{}
		for rows.Next() {
			var id, status string
			if err := rows.Scan(&id, &status); err != nil {
				t.Fatal(err)
			}
			out[status] = append(out[status], id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// edges returns the source and type of the edges between the leaf and
	// a peer, in either direction.
	edges := func(peer string) []string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT r.source || ' ' || r.rel_type FROM ci_relationship r JOIN ci l ON l.name = 'wp582-leaf' AND l.organization_id = r.organization_id
			WHERE r.organization_id = $1 AND ((r.source_ci_id = l.id AND r.target_ci_id = $2) OR (r.source_ci_id = $2 AND r.target_ci_id = l.id))`,
			f.OrgA, peers[peer])
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// An unknown key: an open review per peer, no edge.
	ingest()
	open := map[string]string{}
	for name := range peers {
		r := reviews(name)
		if len(r["open"]) != 1 || len(r) != 1 {
			t.Fatalf("%s: unknown_relationship reviews after the ingest = %v, want one open", name, r)
		}
		open[name] = r["open"][0]
		if e := edges(name); len(e) != 0 {
			t.Errorf("%s: edges before the review is resolved = %v, want none", name, e)
		}
	}

	// accept: the neighbor becomes a connected_to edge of source discovery.
	if w := do("/api/v1/review-items/"+open["accepted"]+"/resolve", []byte(`{"action":"accept"}`)); w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	if r := reviews("accepted"); len(r["resolved"]) != 1 || len(r["open"]) != 0 {
		t.Errorf("accepted: reviews after accept = %v, want one resolved", r)
	}
	if e := edges("accepted"); !slices.Equal(e, []string{"discovery connected_to"}) {
		t.Errorf("accepted: edges after accept = %v, want one connected_to of source discovery", e)
	}

	// dismiss: no edge, and the next ingest of the key opens no new review.
	if w := do("/api/v1/review-items/"+open["dismissed"]+"/resolve", []byte(`{"action":"dismiss"}`)); w.Code != http.StatusOK {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body.String())
	}
	ingest()
	if r := reviews("dismissed"); len(r["dismissed"]) != 1 || len(r) != 1 {
		t.Errorf("dismissed: reviews after the next ingest = %v, want only the dismissed one", r)
	}
	if e := edges("dismissed"); len(e) != 0 {
		t.Errorf("dismissed: edges after the next ingest = %v, want none", e)
	}
	if e := edges("accepted"); !slices.Equal(e, []string{"discovery connected_to"}) {
		t.Errorf("accepted: edges after the next ingest = %v, want the one connected_to of source discovery", e)
	}
}

// TestAuditLLDPEdgesOfFirstCycleIndependentOfLaneOrder covers WP-645
// (TOP-01, NFR-03, REL-03, AUD-03; F-374) against PostgreSQL as the
// application role with the tenant GUCs: the first discovery cycle of the
// simulated fleet (30 devices, 30 LLDP neighbours reported), its records in
// the lane the server computes (discovery.IngestLane, 4 lanes), writes
// every LLDP edge "reporter -> neighbour" with confidence 1.0, once,
// whatever the order of the lanes. A reporter processed before its
// neighbour's CI exists stores its neighbours (raw_data.neighbors); the
// batch that creates the neighbour's CI writes the edge from them
// (deriveNeighborsOf). Before the fix the edges across lanes were missing
// until the reporter's next report (21 of 30; in CI, R01 of
// relationships-suppression.spec.ts saw 4 of the 7 edges of sim-access-01).
//   - lanes one after the other: org A in the order the CI run of R01
//     showed (lanes 0, 3, 1, 2), org B in the reverse order; sim-access-01
//     has its 7 edges (two uplinks to the core switches, four servers and
//     one NAS reporting it). Its stored neighbours are its two uplinks with
//     IP, MAC, hostname, local port and protocol, and the same report again
//     with the neighbours in another order writes no change (AUD-03);
//   - the four lanes of both organizations side by side, three rounds, each
//     on fresh organizations: each side derives after its own commit;
//   - REL-03: sim-access-01 -> sim-core-02, which in the order 0, 3, 1, 2
//     only the new path writes (sim-core-02 comes last), is suppressed when
//     the batch of sim-core-02 reads the stored neighbours: the edge is not
//     created, the 29 others are.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -v \
//	  -run 'TestAuditLLDPEdgesOfFirstCycleIndependentOfLaneOrder|TestTopology' ./internal/discovery/
func TestAuditLLDPEdgesOfFirstCycleIndependentOfLaneOrder(t *testing.T) {
	bg := context.Background()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	recs := simulate.Generate(at)

	// The edges the fleet reports: one per LLDP neighbour, from the
	// reporting device to the neighbour.
	var want []string
	for i := range recs {
		for _, n := range recs[i].Neighbors {
			want = append(want, fmt.Sprintf("%s -> %s", recs[i].Identity.Hostname, n.RemoteHostname))
		}
	}
	sort.Strings(want)
	want = slices.Compact(want)

	batches := map[int][]plugins.DeviceRecord{}
	for i := range recs {
		item := discovery.ItemFromRecord(&recs[i])
		lane := discovery.IngestLane(&item, 4)
		batches[lane] = append(batches[lane], recs[i])
	}
	for lane := range 4 {
		var names []string
		for i := range batches[lane] {
			names = append(names, batches[lane][i].Identity.Hostname)
		}
		t.Logf("lane %d: %v", lane, names)
	}

	edges := func(f *scopetest.Fixture, org string) []string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT s.name, d.name FROM ci_relationship r
			JOIN ci s ON s.id = r.source_ci_id JOIN ci d ON d.id = r.target_ci_id
			WHERE r.organization_id = $1 AND r.rel_type = 'connected_to' AND r.source = 'discovery'
			ORDER BY 1, 2`, org)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s, d string
			if err := rows.Scan(&s, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%s -> %s", s, d))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(out)
		return out
	}
	of := func(all []string, host string) []string {
		var out []string
		for _, e := range all {
			if strings.HasPrefix(e, host+" -> ") || strings.HasSuffix(e, " -> "+host) {
				out = append(out, e)
			}
		}
		return out
	}
	// check compares the edges of org with expected: every edge once (no
	// duplicate), none more, LLDP confidence 1.0 (TOP-01).
	check := func(f *scopetest.Fixture, name, org string, expected []string, access int) {
		t.Helper()
		got := edges(f, org)
		if a := of(got, "sim-access-01"); len(a) != access {
			t.Errorf("%s: sim-access-01 has %d LLDP edges after the first cycle, want %d: %v", name, len(a), access, a)
		}
		var missing, extra []string
		for _, e := range expected {
			if _, ok := slices.BinarySearch(got, e); !ok {
				missing = append(missing, e)
			}
		}
		for i, e := range got {
			if _, ok := slices.BinarySearch(expected, e); !ok || (i > 0 && got[i-1] == e) {
				extra = append(extra, e)
			}
		}
		if len(missing) > 0 || len(extra) > 0 {
			t.Errorf("%s: %d of %d LLDP edges after the first cycle; missing %d: %v; extra or duplicate %d: %v",
				name, len(got), len(expected), len(missing), missing, len(extra), extra)
		}
		var weak int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE organization_id = $1
			AND rel_type = 'connected_to' AND source = 'discovery' AND confidence IS DISTINCT FROM 1.0`, org).Scan(&weak); err != nil {
			t.Fatal(err)
		}
		if weak != 0 {
			t.Errorf("%s: %d LLDP edges without confidence 1.0", name, weak)
		}
	}
	newHandler := func(f *scopetest.Fixture, repo discovery.Repository) *discovery.Handler {
		return discovery.NewHandler(repo, ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).WithIngestLanes(4)
	}
	inOrder := func(f *scopetest.Fixture, h *discovery.Handler, name, org string, order []int) {
		t.Helper()
		ctx := lanesCtx(f, org)
		for _, lane := range order {
			if _, err := discovery.ProcessInLane(ctx, h, org, lane, batches[lane]...); err != nil {
				t.Fatalf("%s: lane %d: %v", name, lane, err)
			}
		}
	}

	t.Run("lanes one after the other", func(t *testing.T) {
		f := scopetest.Seed(t, "f4")
		h := newHandler(f, discovery.NewPGRepository(f.App))
		for _, run := range []struct {
			name  string
			org   string
			order []int
		}{
			{"lanes 0, 3, 1, 2 (CI run of R01)", f.OrgA, []int{0, 3, 1, 2}},
			{"lanes 2, 1, 3, 0", f.OrgB, []int{2, 1, 3, 0}},
		} {
			inOrder(f, h, run.name, run.org, run.order)
			check(f, run.name, run.org, want, 7)
		}

		// The neighbours sim-access-01 reported are stored with its CI.
		var access *plugins.DeviceRecord
		for i := range recs {
			if recs[i].Identity.Hostname == "sim-access-01" {
				access = &recs[i]
			}
		}
		var accessID string
		var stored []discovery.StoredNeighbor
		if err := f.Admin.QueryRow(bg, `SELECT id::text, attributes->'raw_data'->'neighbors' FROM ci WHERE organization_id = $1 AND name = 'sim-access-01'`,
			f.OrgA).Scan(&accessID, &stored); err != nil {
			t.Fatal(err)
		}
		var reported []discovery.StoredNeighbor
		for _, n := range access.Neighbors {
			reported = append(reported, discovery.StoredNeighbor{Protocol: n.Protocol, LocalInterface: n.LocalInterface,
				RemoteIP: n.RemoteIP, RemoteMAC: n.RemoteMAC, RemoteHostname: n.RemoteHostname})
		}
		slices.SortFunc(reported, func(a, b discovery.StoredNeighbor) int { return strings.Compare(a.LocalInterface, b.LocalInterface) })
		if len(reported) != 2 || !slices.Equal(stored, reported) {
			t.Errorf("raw_data.neighbors of sim-access-01 = %+v, want its two uplinks %+v", stored, reported)
		}

		// The same report again, its neighbours in another order, is a
		// sighting: no ci_change (AUD-03), the stored neighbours as before.
		changes := func() int {
			t.Helper()
			var n int
			if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_change WHERE ci_id = $1`, accessID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		before := changes()
		again := *access
		again.RecordID = "6e3f5a10-6450-5c3a-9a45-000000000645"
		again.ObservedAt = access.ObservedAt.Add(time.Second)
		again.Neighbors = slices.Clone(access.Neighbors)
		slices.Reverse(again.Neighbors)
		if _, err := discovery.ProcessInLane(lanesCtx(f, f.OrgA), h, f.OrgA, 1, again); err != nil {
			t.Fatalf("same report again: %v", err)
		}
		if n := changes(); n != before {
			t.Errorf("the same report again wrote %d ci_change rows for sim-access-01, want none", n-before)
		}
		var after []discovery.StoredNeighbor
		if err := f.Admin.QueryRow(bg, `SELECT attributes->'raw_data'->'neighbors' FROM ci WHERE id = $1`, accessID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(after, stored) {
			t.Errorf("raw_data.neighbors after the same report = %+v, want %+v", after, stored)
		}
	})

	for round := 1; round <= 3; round++ {
		t.Run(fmt.Sprintf("lanes side by side, round %d", round), func(t *testing.T) {
			f := scopetest.Seed(t, "f4")
			h := newHandler(f, discovery.NewPGRepository(f.App))
			start := make(chan struct{})
			var wg sync.WaitGroup
			for _, org := range []string{f.OrgA, f.OrgB} {
				ctx := lanesCtx(f, org)
				for lane := range 4 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						if _, err := discovery.ProcessInLane(ctx, h, org, lane, batches[lane]...); err != nil {
							t.Errorf("round %d: lane %d: %v", round, lane, err)
						}
					}()
				}
			}
			close(start)
			wg.Wait()
			check(f, fmt.Sprintf("round %d, org A", round), f.OrgA, want, 7)
			check(f, fmt.Sprintf("round %d, org B", round), f.OrgB, want, 7)
		})
	}

	t.Run("a suppressed edge is not created by the new path", func(t *testing.T) {
		f := scopetest.Seed(t, "f4")
		const suppressedEdge = "sim-access-01 -> sim-core-02"
		var typeID string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM relationship_type WHERE key = 'connected_to' AND organization_id IS NULL`).Scan(&typeID); err != nil {
			t.Fatal(err)
		}
		suppressed := false
		repo := suppressFirst{PGRepository: discovery.NewPGRepository(f.App), before: func() {
			if suppressed {
				return
			}
			// Once sim-core-02 exists, the edge is suppressed before the
			// stored neighbours are read.
			tag, err := f.Admin.Exec(bg, `INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id)
				SELECT $1, s.id, d.id, $2 FROM ci s, ci d
				WHERE s.organization_id = $1 AND s.name = 'sim-access-01' AND d.organization_id = $1 AND d.name = 'sim-core-02'`, f.OrgA, typeID)
			if err != nil {
				t.Errorf("suppress %s: %v", suppressedEdge, err)
				return
			}
			suppressed = tag.RowsAffected() == 1
		}}
		h := newHandler(f, repo)
		inOrder(f, h, "lanes 0, 3, 1, 2 with a suppression", f.OrgA, []int{0, 3, 1, 2})
		if !suppressed {
			t.Fatalf("%s was never suppressed: the batch of sim-core-02 did not read the stored neighbours", suppressedEdge)
		}
		var expected []string
		for _, e := range want {
			if e != suppressedEdge {
				expected = append(expected, e)
			}
		}
		check(f, "suppressed "+suppressedEdge, f.OrgA, expected, 6)
	})
}

// suppressFirst is the discovery repository that calls before when the
// stored neighbours of new CIs are read (REL-03 case of
// TestAuditLLDPEdgesOfFirstCycleIndependentOfLaneOrder).
type suppressFirst struct {
	*discovery.PGRepository
	before func()
}

func (r suppressFirst) NeighborReporters(ctx context.Context, orgID string, keys *discovery.NeighborKeys) ([]discovery.NeighborReporter, error) {
	r.before()
	return r.PGRepository.NeighborReporters(ctx, orgID, keys)
}
