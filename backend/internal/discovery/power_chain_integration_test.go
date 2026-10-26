package discovery_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/simulate"
)

// TestAuditPowerRedundancyIndependentOfBatchOrder covers WP-161 and WP-498
// (IMP-09, F-112) against PostgreSQL: attributes.redundant of the
// powered_by edges is recomputed after every derivation from all stored
// discovery edges of the fed CI and set on all its edges, so it depends
// neither on the batch nor on the ingest lane a PDU arrives in.
//
// The simulated fleet (collector/plugins/simulate) has 16 servers fed by
// sim-pdu-a (PSU1) and sim-pdu-b (PSU2): all 32 server -> PDU edges carry
// redundant=true, the two PDU -> UPS edges (one UPS) redundant=false. The
// records go through the ingest of the server with 4 lanes:
//   - org A: the fleet without the PDUs spread over the lanes, then
//     sim-pdu-a in a batch of lane 1 and sim-pdu-b in a later batch of lane
//     2; org B the other order. Before the fix the first PDU's edges stayed
//     redundant=false;
//   - a second discovery cycle: the whole fleet again, the four lanes at
//     the same time;
//   - the edges deleted, both PDUs at the same time in two lanes, which
//     write the first edges of the servers side by side, the UPS in a
//     third (three rounds);
//   - E-85 option A: an outlet reported switched off keeps its edge, the
//     stored edge takes the state, and both edges of the server are not
//     redundant until it is reported on again.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -p 1 -run 'TestAuditPowerRedundancyIndependentOfBatchOrder' ./internal/discovery/
func TestAuditPowerRedundancyIndependentOfBatchOrder(t *testing.T) {
	f := scopetest.Seed(t, "d9")
	bg := context.Background()
	h := discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithIngestLanes(4)
	// Every delivery is a new observation (new record ids), as a new
	// discovery cycle of the collector is.
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	fleet := func(keep func(host string) bool) []plugins.DeviceRecord {
		at = at.Add(time.Minute)
		var out []plugins.DeviceRecord
		for _, r := range simulate.Generate(at) {
			if keep(r.Identity.Hostname) {
				out = append(out, r)
			}
		}
		return out
	}
	isPDU := func(host string) bool { return host == "sim-pdu-a" || host == "sim-pdu-b" }
	// inLanes ingests recs with the lanes side by side, one batch per
	// lane: a PDU in the lane pduLane names, the other records spread
	// over the four lanes.
	inLanes := func(org string, recs []plugins.DeviceRecord, pduLane map[string]int) {
		t.Helper()
		batches := map[int][]plugins.DeviceRecord{}
		for i, r := range recs {
			lane, ok := pduLane[r.Identity.Hostname]
			if !ok {
				lane = i % 4
			}
			batches[lane] = append(batches[lane], r)
		}
		ctx := lanesCtx(f, org)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for lane, batch := range batches {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := discovery.ProcessInLane(ctx, h, org, lane, batch...); err != nil {
					t.Errorf("lane %d: %v", lane, err)
				}
			}()
		}
		close(start)
		wg.Wait()
	}
	type count struct{ total, redundant int }
	edges := func(org, from string) map[string]count {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT p.name, count(*), count(*) FILTER (WHERE r.attributes->>'redundant' = 'true')
			FROM ci_relationship r JOIN ci s ON s.id = r.source_ci_id JOIN ci p ON p.id = r.target_ci_id
			WHERE r.organization_id = $1 AND r.rel_type = 'powered_by' AND r.source = 'discovery' AND s.name LIKE $2
			GROUP BY 1`, org, from)
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
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// check counts the edges; with ups also the PDU -> UPS edges, which
	// exist once the UPS was reported after the PDUs.
	check := func(step, org string, ups bool) {
		t.Helper()
		servers := edges(org, "sim-srv-%")
		for _, pdu := range []string{"sim-pdu-a", "sim-pdu-b"} {
			if c := servers[pdu]; c.total != 16 || c.redundant != 16 {
				t.Errorf("%s: server -> %s powered_by edges = %d, redundant=true on %d; want 16/16 (every sim server is fed by both PDUs)",
					step, pdu, c.total, c.redundant)
			}
		}
		// Control: each PDU hangs on the one UPS, its edge is not redundant.
		if !ups {
			return
		}
		if c := edges(org, "sim-pdu-%")["sim-ups-01"]; c.total != 2 || c.redundant != 0 {
			t.Errorf("%s: PDU -> UPS powered_by edges = %d, redundant=true on %d; want 2/0", step, c.total, c.redundant)
		}
	}

	for _, tc := range []struct {
		name, org     string
		first, second string
	}{
		{"sim-pdu-a before sim-pdu-b (org A)", f.OrgA, "sim-pdu-a", "sim-pdu-b"},
		{"sim-pdu-b before sim-pdu-a (org B)", f.OrgB, "sim-pdu-b", "sim-pdu-a"},
	} {
		// Cycle 1: the devices, then one PDU per batch, in two lanes.
		inLanes(tc.org, fleet(func(h string) bool { return !isPDU(h) }), nil)
		inLanes(tc.org, fleet(func(h string) bool { return h == tc.first }), map[string]int{tc.first: 1})
		inLanes(tc.org, fleet(func(h string) bool { return h == tc.second }), map[string]int{tc.second: 2})
		check(tc.name+", cycle 1", tc.org, false)

		// Cycle 2: the whole fleet, the four lanes side by side.
		inLanes(tc.org, fleet(func(string) bool { return true }), map[string]int{tc.first: 1, tc.second: 2})
		check(tc.name+", cycle 2", tc.org, true)

		// The edges are written anew by both PDUs at the same time (and
		// the UPS in a third lane).
		for round := range 3 {
			if _, err := f.Admin.Exec(bg, `DELETE FROM ci_relationship WHERE organization_id = $1 AND rel_type = 'powered_by'`, tc.org); err != nil {
				t.Fatal(err)
			}
			inLanes(tc.org, fleet(func(h string) bool { return isPDU(h) || h == "sim-ups-01" }),
				map[string]int{tc.first: 1 + round%2, tc.second: 2 - round%2, "sim-ups-01": 3})
			check(fmt.Sprintf("%s, PDUs side by side, round %d", tc.name, round+1), tc.org, true)
		}
	}

	// E-85 option A on PostgreSQL: sim-pdu-b reports the PSU2 outlet of
	// sim-srv-01 switched off, then without a state, then on again. The
	// stored edge takes the reported state (a missing state removes it);
	// while it is off both edges of sim-srv-01 are not redundant.
	srv01 := func() (redundant map[string]string, stateB *string) {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT p.name, r.attributes->>'redundant', r.attributes->>'outlet_state'
			FROM ci_relationship r JOIN ci s ON s.id = r.source_ci_id JOIN ci p ON p.id = r.target_ci_id
			WHERE r.organization_id = $1 AND r.rel_type = 'powered_by' AND s.name = 'sim-srv-01'`, f.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		redundant = map[string]string{}
		for rows.Next() {
			var pdu, flag string
			var st *string
			if err := rows.Scan(&pdu, &flag, &st); err != nil {
				t.Fatal(err)
			}
			redundant[pdu] = flag
			if pdu == "sim-pdu-b" {
				stateB = st
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return redundant, stateB
	}
	for _, step := range []struct {
		state, flag string
	}{{"off", "false"}, {"", "true"}, {"on", "true"}, {"off", "false"}, {"on", "true"}} {
		recs := fleet(func(h string) bool { return h == "sim-pdu-b" })
		for i := range recs[0].Power.Outlets {
			if o := &recs[0].Power.Outlets[i]; o.Name == "sim-srv-01 PSU2" {
				o.State = step.state
			}
		}
		inLanes(f.OrgA, recs, map[string]int{"sim-pdu-b": 2})
		redundant, stateB := srv01()
		if redundant["sim-pdu-a"] != step.flag || redundant["sim-pdu-b"] != step.flag {
			t.Errorf("PSU2 outlet state %q: sim-srv-01 redundant = %v, want %s on both edges", step.state, redundant, step.flag)
		}
		switch {
		case step.state == "" && stateB != nil:
			t.Errorf("PSU2 outlet without a state: stored outlet_state %q kept, want it removed", *stateB)
		case step.state != "" && (stateB == nil || *stateB != step.state):
			t.Errorf("PSU2 outlet state %q: stored outlet_state = %v", step.state, stateB)
		}
	}
	check("after the outlet states", f.OrgA, true)
}
