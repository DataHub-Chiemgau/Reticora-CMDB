package probe_test

import (
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
)

// TestLivenessThresholds covers WP-963 AK1: a member is down once the last
// alive message is older than the liveness timeout of its group (20 s,
// the default 30 s, 300 s), not at the timeout itself; a member without any
// alive message is not swept.
func TestLivenessThresholds(t *testing.T) {
	last := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, timeout := range []time.Duration{20 * time.Second, probe.DefaultLivenessTimeout, 300 * time.Second} {
		if probe.SilentMember(&last, last.Add(timeout), timeout) {
			t.Errorf("timeout %s: down exactly at the timeout", timeout)
		}
		if !probe.SilentMember(&last, last.Add(timeout+time.Millisecond), timeout) {
			t.Errorf("timeout %s: not down after the timeout", timeout)
		}
		if probe.SilentMember(&last, last.Add(timeout/2), timeout) {
			t.Errorf("timeout %s: down within the timeout", timeout)
		}
	}
	if probe.SilentMember(nil, last.Add(time.Hour), probe.DefaultLivenessTimeout) {
		t.Error("a member without alive message was swept")
	}
}

// TestLivenessReturnAfterTwoAliveMessages covers AK1: a down member
// returns with the second alive message within one timeout; the first
// alive message after the outage (its predecessor older than the timeout)
// does not make it assignable, an older receipt neither.
func TestLivenessReturnAfterTwoAliveMessages(t *testing.T) {
	timeout := probe.DefaultLivenessTimeout
	last := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	first := last.Add(5 * time.Minute)
	if probe.MemberReturns(&last, first, timeout) {
		t.Error("the first alive message after the outage made the member assignable")
	}
	if !probe.MemberReturns(&first, first.Add(10*time.Second), timeout) {
		t.Error("the second alive message within the timeout did not return the member")
	}
	if probe.MemberReturns(&first, first.Add(timeout+time.Second), timeout) {
		t.Error("a second alive message after the timeout returned the member")
	}
	if probe.MemberReturns(&first, first.Add(-time.Second), timeout) || probe.MemberReturns(&first, first, timeout) {
		t.Error("an older or the same receipt returned the member")
	}
	if probe.MemberReturns(nil, first, timeout) {
		t.Error("an alive message without predecessor returned the member")
	}
	// With a timeout of 20 s the alive messages of 10 s still return it.
	if !probe.MemberReturns(&first, first.Add(10*time.Second), 20*time.Second) {
		t.Error("timeout 20 s: no return with alive messages 10 s apart")
	}
}

// TestLivenessDegradedStaysAssignable covers AK1 and AK2: healthy and
// degraded members take probes, a down member does not; a group whose
// only healthy member is degraded keeps all probes on it.
func TestLivenessDegradedStaysAssignable(t *testing.T) {
	for status, want := range map[string]bool{probe.MemberHealthy: true, probe.MemberDegraded: true, probe.MemberDown: false} {
		if got := probe.Assignable(status); got != want {
			t.Errorf("Assignable(%s) = %v, want %v", status, got, want)
		}
	}
	group := &probe.CollectorGroup{ID: "g", Mode: probe.ModeDistributed}
	members := []probe.Candidate{
		{CollectorID: "a", Weight: 100, Healthy: probe.Assignable(probe.MemberDown)},
		{CollectorID: "b", Weight: 100, Healthy: probe.Assignable(probe.MemberDegraded)},
	}
	probes := []probe.Probe{{ID: "p1", Enabled: true, Type: "icmp", Target: "10.0.0.1"}, {ID: "p2", Enabled: true, Type: "icmp", Target: "10.0.0.2"}}
	got := probe.Assign(group, members, probes)
	if len(got) != 2 {
		t.Fatalf("assignments %+v, want both probes", got)
	}
	for _, a := range got {
		if a.CollectorID != "b" {
			t.Errorf("probe %s on %s, want the degraded member b", a.ProbeID, a.CollectorID)
		}
	}
}
