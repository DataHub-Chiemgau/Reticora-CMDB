//go:build auditdefect

package discovery_test

// Defect test found while driving PR #102 (milestone K5) to green (TOP-01,
// NFR-03; F-374): deriveTopology (topology.go) writes the LLDP edge
// "reporter -> neighbour" only when the neighbour's CI already exists. The
// records of one collector cycle are spread over the ingest lanes
// (fnv32a("serial:<serial>") % RETICORA_INGEST_LANES, lanes.go), so a
// reporter processed in a lane before its neighbour's lane finds no
// neighbour CI and its edge is skipped; it appears only with the reporter's
// next report (5 to 15 minutes later). Nothing derives the incoming edges of
// a CI when it is created, as derivePowerFeedsOf does for the powered_by
// edges since WP-498 (F-112). In CI, relationships-suppression.spec.ts R01
// saw 4 of the 7 edges of sim-access-01 after the first cycle.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=... go test -tags auditdefect -count=1 -v \
//	  -run '^TestAuditLLDPEdgesOfFirstCycleIndependentOfLaneOrder$' ./internal/discovery/

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/simulate"
)

// TestAuditLLDPEdgesOfFirstCycleIndependentOfLaneOrder: the first discovery
// cycle of the simulated fleet (30 devices, 30 LLDP neighbours reported),
// its records in the lane the server computes (discovery.IngestLane, 4
// lanes) and the lanes processed one after the other, writes every LLDP
// edge "reporter -> neighbour", whatever the order of the lanes: org A in
// the order the CI run of R01 showed (lanes 0, 3, 1, 2), org B in the
// reverse order. In particular sim-access-01 has its 7 edges (two uplinks
// to the core switches, four servers and one NAS reporting it).
func TestAuditLLDPEdgesOfFirstCycleIndependentOfLaneOrder(t *testing.T) {
	f := scopetest.Seed(t, "f4")
	bg := context.Background()
	h := discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithIngestLanes(4)
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

	edges := func(org string) []string {
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
	missing := func(got []string) []string {
		var out []string
		for _, e := range want {
			if _, ok := slices.BinarySearch(got, e); !ok {
				out = append(out, e)
			}
		}
		return out
	}

	for _, run := range []struct {
		name  string
		org   string
		order []int
	}{
		{"lanes 0, 3, 1, 2 (CI run of R01)", f.OrgA, []int{0, 3, 1, 2}},
		{"lanes 2, 1, 3, 0", f.OrgB, []int{2, 1, 3, 0}},
	} {
		ctx := lanesCtx(f, run.org)
		for _, lane := range run.order {
			if _, err := discovery.ProcessInLane(ctx, h, run.org, lane, batches[lane]...); err != nil {
				t.Fatalf("%s: lane %d: %v", run.name, lane, err)
			}
		}
		got := edges(run.org)
		access := of(got, "sim-access-01")
		if len(access) != 7 {
			t.Errorf("%s: sim-access-01 has %d LLDP edges after the first cycle, want 7: %v", run.name, len(access), access)
		}
		if m := missing(got); len(m) > 0 {
			t.Errorf("%s: %d of %d LLDP edges after the first cycle; missing %d: %v", run.name, len(got), len(want), len(m), m)
		}
	}
}
