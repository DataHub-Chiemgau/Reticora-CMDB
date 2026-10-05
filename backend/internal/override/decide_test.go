package override

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSourceRanksFollowREC03 pins the rank table of REC-03 (WP-058), with
// IPMI ranked as Redfish (E-32).
func TestSourceRanksFollowREC03(t *testing.T) {
	want := map[string]int{
		"manual": 100, "override": 100, "manual_override": 100, "import": 95, "workflow": 92,
		"redfish": 90, "ipmi": 90, "agent": 85, "snmp": 80, "api": 75, "integration": 75,
		"wmi": 70, "ssh": 60, "nas": 60, "sweep": 20, "unknown-source": 0, "": 0,
	}
	for source, rank := range want {
		if got := SourceRank(source); got != rank {
			t.Errorf("SourceRank(%q) = %d, want %d", source, got, rank)
		}
	}
	if len(SourceRanks) != 15 {
		t.Errorf("rank table has %d entries, want the 15 of REC-03 and E-32", len(SourceRanks))
	}
}

type stubReader struct {
	fv  *FieldValue
	err error
}

func (s stubReader) Get(context.Context, string, string, string) (*FieldValue, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.fv == nil {
		return nil, ErrNotFound
	}
	return s.fv, nil
}

// TestDecideAutomatedWrite covers WP-058 (REC-03, REC-05, REC-12, OVR-01,
// CI-10, API-07): overrides are never overwritten and conflicts are
// reported, ranks and observation times decide otherwise, and an unreadable
// state fails closed.
func TestDecideAutomatedWrite(t *testing.T) {
	now := time.Now().UTC()
	earlier, later := now.Add(-time.Hour), now.Add(time.Hour)
	at := func(t time.Time) *time.Time { return &t }
	write := func(source string, value, current any) *Write {
		return &Write{OrganizationID: "org", CIID: "ci", Field: "model", Value: value, Source: source, ObservedAt: now, Current: current}
	}
	override := &FieldValue{OverrideValue: "manual-model", OverrideAt: at(earlier), Protected: true}
	removed := &FieldValue{OverrideAt: at(earlier), Protected: true} // field removed by hand
	fromRedfish := &FieldValue{DiscoveredValue: "x", DiscoveredSource: "redfish", DiscoveredAt: at(earlier)}
	newerSNMP := &FieldValue{DiscoveredValue: "x", DiscoveredSource: "snmp", DiscoveredAt: at(later)}

	for _, c := range []struct {
		name     string
		reader   FieldReader
		w        *Write
		write    bool
		conflict bool
		reason   string
	}{
		{"override differs: conflict, no write", stubReader{fv: override}, write("redfish", "other", "manual-model"), false, true, ReasonOverrideConflict},
		{"override equal: nothing to do", stubReader{fv: override}, write("snmp", "manual-model", "manual-model"), false, false, ReasonOverride},
		{"import never over an override", stubReader{fv: override}, write("import", "other", "manual-model"), false, true, ReasonOverrideConflict},
		{"workflow never over an override", stubReader{fv: override}, write("workflow", "other", "manual-model"), false, true, ReasonOverrideConflict},
		{"removed field stays removed", stubReader{fv: removed}, write("redfish", "back", nil), false, true, ReasonOverrideConflict},
		{"empty value is filled by any source", stubReader{}, write("sweep", "x", ""), true, false, ReasonEmpty},
		{"lower rank does not replace", stubReader{fv: fromRedfish}, write("snmp", "y", "x"), false, false, ReasonLowerRank},
		{"higher rank replaces", stubReader{fv: newerSNMP}, write("agent", "y", "x"), true, false, ReasonAllowed},
		{"equal rank replaces when newer", stubReader{fv: fromRedfish}, write("ipmi", "y", "x"), true, false, ReasonAllowed},
		{"equal rank older observation is refused", stubReader{fv: newerSNMP}, write("snmp", "y", "x"), false, false, ReasonStale},
		{"manual CI without provenance outranks discovery", stubReader{}, &Write{Field: "model", Value: "y", Source: "redfish", Current: "x", FallbackSource: "manual", ObservedAt: now}, false, false, ReasonLowerRank},
		{"unknown source only fills empty values", stubReader{}, &Write{Field: "model", Value: "y", Source: "mystery", Current: "x", FallbackSource: "sweep", ObservedAt: now}, false, false, ReasonLowerRank},
		{"unreadable state fails closed", stubReader{err: errors.New("db down")}, write("manual_override", "y", ""), false, false, ReasonError},
	} {
		got := DecideAutomatedWrite(context.Background(), c.reader, c.w)
		if got.Write != c.write || got.Conflict != c.conflict || got.Reason != c.reason {
			t.Errorf("%s: %+v, want write=%v conflict=%v reason=%s", c.name, got, c.write, c.conflict, c.reason)
		}
	}
}

func TestDecisionFailuresAreCounted(t *testing.T) {
	before := DecisionFailures()
	DecideAutomatedWrite(context.Background(), stubReader{err: errors.New("db down")}, &Write{Field: "name", Source: "snmp"})
	if DecisionFailures() != before+1 {
		t.Errorf("failure counter %d, want %d", DecisionFailures(), before+1)
	}
}

func TestIsProtectedFailsClosed(t *testing.T) {
	repo := NewMemoryRepository()
	if protected, err := IsProtected(context.Background(), repo, "org", "ci", "name"); err != nil || protected {
		t.Errorf("field without row: protected=%v, %v", protected, err)
	}
}
