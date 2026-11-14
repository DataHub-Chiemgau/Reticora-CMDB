package probe_test

import (
	"fmt"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
)

// The decisions of the automatic ping probes (MON-02, WP-949 AK3-AK5).

func TestAutocreateSkipReasons(t *testing.T) {
	base := probe.AutoCandidate{CIID: "ci", ClientID: "c1", ManagementIP: "10.0.0.1", Collector: "col", CollectorClient: "c1",
		Group: "g", GroupClient: "c1"}
	cases := []struct {
		name   string
		change func(c *probe.AutoCandidate)
		action probe.AutoAction
		reason string
	}{
		{"created", func(*probe.AutoCandidate) {}, probe.AutoCreate, ""},
		{"org-wide collector and group", func(c *probe.AutoCandidate) { c.CollectorClient, c.GroupClient = "", "" }, probe.AutoCreate, ""},
		{"no collector", func(c *probe.AutoCandidate) { c.Collector, c.Group = "", "" }, probe.AutoSkip, probe.SkipNoCollector},
		{"collector of another client", func(c *probe.AutoCandidate) { c.CollectorClient = "c2" }, probe.AutoSkip, probe.SkipClientMismatch},
		{"group of another client", func(c *probe.AutoCandidate) { c.GroupClient = "c2" }, probe.AutoSkip, probe.SkipClientMismatch},
		{"suppressed", func(c *probe.AutoCandidate) { c.Suppressed = true }, probe.AutoSkip, probe.SkipSuppressed},
		{"auto probe on the address", func(c *probe.AutoCandidate) { c.AutoProbe, c.AutoTarget = "p", "10.0.0.1" }, probe.AutoNone, ""},
		{"auto probe follows the address", func(c *probe.AutoCandidate) { c.AutoProbe, c.AutoTarget = "p", "10.0.0.9" }, probe.AutoRetarget, ""},
		{"manual probe stays alone", func(c *probe.AutoCandidate) { c.HasManual = true }, probe.AutoNone, ""},
	}
	for _, tc := range cases {
		c := base
		tc.change(&c)
		if got := c.Plan(); got != tc.action {
			t.Errorf("%s: action %d, want %d", tc.name, got, tc.action)
		}
		if tc.action == probe.AutoSkip && c.SkipReason() != tc.reason {
			t.Errorf("%s: reason %q, want %q", tc.name, c.SkipReason(), tc.reason)
		}
	}
	if !probe.AutoPingFromSettings(map[string]any{"monitoring": map[string]any{"auto_ping": true}}) ||
		probe.AutoPingFromSettings(map[string]any{}) || probe.AutoPingFromSettings(map[string]any{"monitoring": map[string]any{"auto_ping": "yes"}}) {
		t.Error("AutoPingFromSettings: only a true bool switches it on (default off)")
	}
}

func TestAutocreateReportCapsIDs(t *testing.T) {
	r := probe.NewAutocreateReport()
	for i := range probe.MaxReportedIDs + 50 {
		r.Skip(probe.SkipQuota, fmt.Sprintf("ci-%d", i))
	}
	r.Skip(probe.SkipNoCollector, "x")
	q := r.Skipped[probe.SkipQuota]
	if q.Count != probe.MaxReportedIDs+50 || len(q.CIIDs) != probe.MaxReportedIDs {
		t.Errorf("quota: count %d with %d ids, want %d with %d", q.Count, len(q.CIIDs), probe.MaxReportedIDs+50, probe.MaxReportedIDs)
	}
	if n := r.Skipped[probe.SkipNoCollector]; n.Count != 1 || len(n.CIIDs) != 1 {
		t.Errorf("no_collector: %+v", n)
	}
	for _, reason := range []string{probe.SkipClientMismatch, probe.SkipSuppressed} {
		if l := r.Skipped[reason]; l == nil || l.Count != 0 || l.CIIDs == nil {
			t.Errorf("%s: %+v, want an empty list", reason, l)
		}
	}
}
