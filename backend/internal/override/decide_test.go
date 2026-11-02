package override

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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

// TestDefaultPrioritiesFollowSourceRanks covers WP-478 (REC-03, retest of
// F-230): the built-in source priorities, which GET
// /reconciliation/source-policy shows without a policy of the
// organization, are the sources of the REC-03 rank table by descending
// rank (same rank by name), each once; the former list ranked manual and
// import behind sweep.
func TestDefaultPrioritiesFollowSourceRanks(t *testing.T) {
	want := []string{
		"manual", "manual_override", "override", "import", "workflow", "ipmi", "redfish",
		"agent", "snmp", "api", "integration", "wmi", "nas", "ssh", "sweep",
	}
	if strings.Join(DefaultPriorities, ",") != strings.Join(want, ",") {
		t.Errorf("DefaultPriorities = %v, want %v", DefaultPriorities, want)
	}
	if len(DefaultPriorities) != len(SourceRanks) {
		t.Errorf("DefaultPriorities has %d sources, the rank table %d", len(DefaultPriorities), len(SourceRanks))
	}
	seen := map[string]bool{}
	for i, source := range DefaultPriorities {
		if _, ok := SourceRanks[source]; !ok || seen[source] {
			t.Errorf("DefaultPriorities[%d] = %q: not in the rank table or repeated", i, source)
		}
		seen[source] = true
		if i > 0 && SourceRank(DefaultPriorities[i-1]) < SourceRank(source) {
			t.Errorf("%s (%d) before %s (%d): not descending", DefaultPriorities[i-1],
				SourceRank(DefaultPriorities[i-1]), source, SourceRank(source))
		}
	}
	policy, err := NewMemoryRepository().Policy(context.Background(), "org")
	if err != nil || strings.Join(policy.Priorities, ",") != strings.Join(want, ",") || !policy.IsDefault {
		t.Errorf("default policy = %+v, %v; want the priorities %v", policy, err, want)
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

// TestDecideComparesObservedAtPerField covers WP-084 (GLO-12, REC-05): a
// value of the same rank wins only with a newer source time; the comparison
// uses the field's stored observed_at and rank, not the time the value was
// recorded.
func TestDecideComparesObservedAtPerField(t *testing.T) {
	now := time.Now().UTC()
	at := func(t time.Time) *time.Time { return &t }
	write := func(source string, observedAt time.Time) *Write {
		return &Write{Field: "model", Value: "new", Current: "old", Source: source, ObservedAt: observedAt}
	}
	// Recorded just now, observed an hour ago.
	late := &FieldValue{DiscoveredValue: "old", DiscoveredSource: "snmp", DiscoveredRank: 80,
		DiscoveredAt: at(now), ObservedAt: at(now.Add(-time.Hour))}
	// Recorded with a rank that differs from today's table.
	ranked := &FieldValue{DiscoveredValue: "old", DiscoveredSource: "custom", DiscoveredRank: 85,
		DiscoveredAt: at(now), ObservedAt: at(now.Add(-time.Hour))}
	// Written before WP-084: no observed_at, no rank.
	legacy := &FieldValue{DiscoveredValue: "old", DiscoveredSource: "snmp", DiscoveredAt: at(now.Add(-time.Hour))}

	for _, c := range []struct {
		name   string
		fv     *FieldValue
		w      *Write
		write  bool
		reason string
	}{
		{"same rank newer than observed_at, older than recording", late, write("snmp", now.Add(-30*time.Minute)), true, ReasonAllowed},
		{"same rank, same source time", late, write("snmp", now.Add(-time.Hour)), false, ReasonStale},
		{"same rank, older source time", late, write("snmp", now.Add(-2*time.Hour)), false, ReasonStale},
		{"same rank without source time", late, write("snmp", time.Time{}), false, ReasonStale},
		{"higher rank with older source time", late, write("redfish", now.Add(-2*time.Hour)), true, ReasonAllowed},
		{"lower rank with newer source time", late, write("sweep", now), false, ReasonLowerRank},
		{"stored rank decides", ranked, write("snmp", now), false, ReasonLowerRank},
		{"stored rank, same rank newer", ranked, write("agent", now), true, ReasonAllowed},
		{"legacy row falls back to discovered_at", legacy, write("snmp", now.Add(-2*time.Hour)), false, ReasonStale},
		{"legacy row, newer", legacy, write("snmp", now), true, ReasonAllowed},
	} {
		got := DecideAutomatedWrite(context.Background(), stubReader{fv: c.fv}, c.w)
		if got.Write != c.write || got.Reason != c.reason {
			t.Errorf("%s: %+v, want write=%v reason=%s", c.name, got, c.write, c.reason)
		}
	}
}

// TestRecordDiscoveredKeepsSourceRankAndObservedAt covers WP-084 (REC-05):
// the provenance of a field stores source, rank and observed_at; the
// recording time stays separate.
func TestRecordDiscoveredKeepsSourceRankAndObservedAt(t *testing.T) {
	repo := NewMemoryRepository()
	observed := time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC)
	fv, err := repo.RecordDiscovered(context.Background(), "org", "ci", "model", "R760", "redfish", observed)
	if err != nil {
		t.Fatal(err)
	}
	if fv.DiscoveredSource != "redfish" || fv.DiscoveredRank != 90 || fv.ObservedAt == nil || !fv.ObservedAt.Equal(observed) ||
		fv.DiscoveredAt == nil || !fv.DiscoveredAt.After(observed) {
		t.Errorf("provenance %+v", fv)
	}
}

// TestMigrationRanksFollowTheRankTable keeps the backfill of migration
// 000093 in step with SourceRanks.
func TestMigrationRanksFollowTheRankTable(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000093_field_provenance_observed_at.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for source, rank := range SourceRanks {
		if !strings.Contains(sql, fmt.Sprintf("WHEN '%s' THEN %d", source, rank)) {
			t.Errorf("migration 000093 lacks rank %d of %s", rank, source)
		}
	}
	if n := strings.Count(sql, "WHEN '"); n != len(SourceRanks) {
		t.Errorf("migration 000093 ranks %d sources, the table %d", n, len(SourceRanks))
	}
}
