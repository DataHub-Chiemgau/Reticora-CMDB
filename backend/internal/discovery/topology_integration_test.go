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
