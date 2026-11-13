package probe_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
)

// Retest (WP-941, PRB-03):
//
//	cd backend && go test -count=1 -run 'TestAssign' ./internal/probe/

// probesN returns n enabled probes with UUID-like ids.
func probesN(n int) []probe.Probe {
	out := make([]probe.Probe, n)
	for i := range out {
		out[i] = probe.Probe{ID: fmt.Sprintf("00000000-0000-4000-8000-%012x", i), OrganizationID: "org", Enabled: true}
	}
	return out
}

func member(id string, weight, priority int, healthy bool) probe.Candidate {
	return probe.Candidate{CollectorID: id, Weight: weight, Priority: priority, Healthy: healthy}
}

// byProbe maps the assignments by probe.
func byProbe(as []probe.Assignment) map[string]string {
	out := make(map[string]string, len(as))
	for _, a := range as {
		out[a.ProbeID] = a.CollectorID
	}
	return out
}

// shares counts the assignments per collector.
func shares(as []probe.Assignment) map[string]int {
	out := map[string]int{}
	for _, a := range as {
		out[a.CollectorID]++
	}
	return out
}

var distributed = &probe.CollectorGroup{ID: "g", Mode: probe.ModeDistributed}

// TestAssignDistributesToHealthyMembers covers AK1: every active probe on
// exactly one healthy member; disabled probes and unhealthy members get
// nothing.
func TestAssignDistributesToHealthyMembers(t *testing.T) {
	probes := probesN(3000)
	probes[0].Enabled = false
	got := probe.Assign(distributed, []probe.Candidate{member("a", 100, 0, true), member("b", 100, 0, true),
		member("c", 100, 0, false)}, probes)
	if len(got) != len(probes)-1 {
		t.Fatalf("%d assignments, want %d (one per active probe)", len(got), len(probes)-1)
	}
	seen := map[string]bool{}
	for _, a := range got {
		if seen[a.ProbeID] {
			t.Fatalf("probe %s assigned twice", a.ProbeID)
		}
		seen[a.ProbeID] = true
		if a.CollectorID != "a" && a.CollectorID != "b" {
			t.Fatalf("probe %s on %q, want a healthy member", a.ProbeID, a.CollectorID)
		}
	}
	if seen[probes[0].ID] {
		t.Error("the disabled probe is assigned")
	}
	s := shares(got)
	if s["a"] < 1300 || s["b"] < 1300 {
		t.Errorf("shares %v, want both members used about equally", s)
	}
	// The result does not depend on the order of the members.
	again := probe.Assign(distributed, []probe.Candidate{member("c", 100, 0, false), member("b", 100, 0, true),
		member("a", 100, 0, true)}, probes)
	first, second := byProbe(got), byProbe(again)
	for id, c := range first {
		if second[id] != c {
			t.Fatalf("probe %s: %s, then %s with the members in another order", id, c, second[id])
		}
	}
}

// TestAssignMinimalMovement covers AK2: 3 → 2 members moves only the
// probes of the member that left (about a third); 2 → 3 moves only probes
// to the new member.
func TestAssignMinimalMovement(t *testing.T) {
	probes := probesN(10000)
	three := []probe.Candidate{member("a", 100, 0, true), member("b", 100, 0, true), member("c", 100, 0, true)}
	before := byProbe(probe.Assign(distributed, three, probes))
	three[2].Healthy = false
	after := byProbe(probe.Assign(distributed, three, probes))
	moved := 0
	for id, was := range before {
		if after[id] == was {
			continue
		}
		moved++
		if was != "c" {
			t.Fatalf("probe %s moved from %s to %s, but only c left", id, was, after[id])
		}
	}
	if share := float64(moved) / float64(len(probes)); math.Abs(share-1.0/3) > 0.02 {
		t.Errorf("moved share %.4f, want 1/3 ± 2 %%", share)
	}
	// c returns: only probes to c move.
	three[2].Healthy = true
	back := byProbe(probe.Assign(distributed, three, probes))
	for id, was := range after {
		if back[id] != was && back[id] != "c" {
			t.Fatalf("probe %s moved from %s to %s when c joined", id, was, back[id])
		}
	}
	plan := probe.PlanAssignments(probe.Assign(distributed, three[:2], probes), probe.Assign(distributed, three, probes))
	if len(plan.Removes) != 0 || len(plan.Collectors) != 3 {
		t.Errorf("plan of the join: removes %d, collectors %v", len(plan.Removes), plan.Collectors)
	}
	for _, a := range plan.Upserts {
		if a.CollectorID != "c" {
			t.Fatalf("join plan moves %s to %s", a.ProbeID, a.CollectorID)
		}
	}
}

// TestAssignFailoverMode covers the mode failover: every probe on the
// healthy member with the highest priority, on a tie the smaller id.
func TestAssignFailoverMode(t *testing.T) {
	group := &probe.CollectorGroup{ID: "g", Mode: probe.ModeFailover}
	probes := probesN(50)
	for _, c := range []struct {
		name    string
		members []probe.Candidate
		want    string
	}{
		{"highest priority", []probe.Candidate{member("a", 100, 1, true), member("b", 100, 5, true), member("c", 100, 3, true)}, "b"},
		{"highest unhealthy", []probe.Candidate{member("a", 100, 1, true), member("b", 100, 5, false), member("c", 100, 3, true)}, "c"},
		{"tie", []probe.Candidate{member("d", 100, 2, true), member("c", 100, 2, true), member("e", 100, 1, true)}, "c"},
	} {
		got := probe.Assign(group, c.members, probes)
		if s := shares(got); s[c.want] != len(probes) {
			t.Errorf("%s: shares %v, want every probe on %s", c.name, s, c.want)
		}
	}
}

// TestAssignWeighting: weight 2:1 gives a share of 2/3 ± 2 %.
func TestAssignWeighting(t *testing.T) {
	probes := probesN(10000)
	s := shares(probe.Assign(distributed, []probe.Candidate{member("heavy", 200, 0, true), member("light", 100, 0, true)}, probes))
	if share := float64(s["heavy"]) / float64(len(probes)); math.Abs(share-2.0/3) > 0.02 {
		t.Errorf("share of weight 2 = %.4f, want 2/3 ± 2 %%", share)
	}
}

// TestAssignWithoutHealthyMemberKeepsState: without a healthy member Assign
// gives nil and the plan changes nothing (network down, WP-964).
func TestAssignWithoutHealthyMemberKeepsState(t *testing.T) {
	probes := probesN(10)
	current := probe.Assign(distributed, []probe.Candidate{member("a", 100, 0, true)}, probes)
	got := probe.Assign(distributed, []probe.Candidate{member("a", 100, 0, false), member("b", 100, 0, false)}, probes)
	if got != nil {
		t.Fatalf("without a healthy member: %d assignments, want nil", len(got))
	}
	if plan := probe.PlanAssignments(current, got); !plan.Empty() || len(plan.Collectors) != 0 {
		t.Errorf("plan without a healthy member: %+v, want no change", plan)
	}
	// With a healthy member a disabled probe loses its assignment.
	probes[3].Enabled = false
	plan := probe.PlanAssignments(current, probe.Assign(distributed, []probe.Candidate{member("a", 100, 0, true)}, probes))
	if len(plan.Upserts) != 0 || len(plan.Removes) != 1 || plan.Removes[0] != probes[3].ID {
		t.Errorf("plan after a disable: %+v", plan)
	}
}
