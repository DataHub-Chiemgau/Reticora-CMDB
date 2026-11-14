package probe_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
)

// The tier selection of /probes/{id}/results (WP-975 AK1): the finest tier
// with at most 2,000 points whose retention covers from, without fixed time
// limits; a forced step outside its window or above the points is 422 step.

var tierNow = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

func selectTier(t *testing.T, span, ago, interval time.Duration) probe.Tier {
	t.Helper()
	to := tierNow.Add(-ago)
	tier, err := probe.SelectTier(to.Add(-span), to, tierNow, interval, "")
	if err != nil {
		t.Fatalf("span %v, %v ago: %v", span, ago, err)
	}
	return tier
}

func TestSelectTierByPointLimit(t *testing.T) {
	// A one-second probe: raw never fits these spans, so the point limit of
	// the aggregates decides.
	for _, c := range []struct {
		span time.Duration
		want probe.Tier
	}{
		{6 * day, probe.Tier5m},  // 1,728 points in 5m
		{7 * day, probe.Tier1h},  // 2,016 points in 5m
		{83 * day, probe.Tier1h}, // 1,992 points in 1h
		{84 * day, probe.Tier1d}, // 2,016 points in 1h
	} {
		if got := selectTier(t, c.span, 0, time.Second); got != c.want {
			t.Errorf("%v: tier %s, want %s", c.span, got, c.want)
		}
	}
}

func TestSelectTierCapsAt2000Points(t *testing.T) {
	if got := selectTier(t, day, 0, 30*time.Second); got != probe.Tier5m {
		t.Errorf("24 h at 30 s (2,880 raw points): tier %s, want 5m", got)
	}
	if got := selectTier(t, day, 0, 60*time.Second); got != probe.TierRaw {
		t.Errorf("24 h at 60 s (1,440 raw points): tier %s, want raw", got)
	}
	if got := selectTier(t, time.Hour, 0, 0); got != probe.Tier5m {
		t.Errorf("unknown interval: tier %s, want 5m (raw needs the interval)", got)
	}
}

func TestSelectTierRespectsRetentionWindow(t *testing.T) {
	if got := selectTier(t, time.Hour, 20*day, 60*time.Second); got != probe.Tier5m {
		t.Errorf("1 h 20 days back: tier %s, want 5m (raw keeps 14 days)", got)
	}
	if got := selectTier(t, day, 100*day, 60*time.Second); got != probe.Tier1h {
		t.Errorf("1 day 100 days back: tier %s, want 1h (5m keeps 90 days)", got)
	}
	if _, err := probe.SelectTier(tierNow.Add(-401*day), tierNow.Add(-400*day), tierNow, time.Minute, ""); fieldOf(err) != "from" {
		t.Errorf("before every retention: %v, want 422 from", err)
	}
	if _, err := probe.SelectTier(tierNow.Add(-401*day), tierNow, tierNow, time.Minute, ""); fieldOf(err) != "from" {
		t.Errorf("401 days: %v, want 422 from", err)
	}
	if _, err := probe.SelectTier(tierNow, tierNow, tierNow, time.Minute, ""); fieldOf(err) != "to" {
		t.Errorf("empty span: %v, want 422 to", err)
	}
}

func TestExplicitStepOutsideWindowIs422(t *testing.T) {
	for _, c := range []struct {
		name      string
		from, to  time.Time
		step      string
		wantField string
		want      probe.Tier
	}{
		{"raw 20 days back", tierNow.Add(-20 * day), tierNow.Add(-20*day + time.Hour), "raw", "step", ""},
		{"raw above 2,000 points", tierNow.Add(-day), tierNow, "raw", "step", ""},
		{"5m 100 days back", tierNow.Add(-100 * day), tierNow.Add(-99 * day), "5m", "step", ""},
		{"5m over 7 days", tierNow.Add(-7 * day), tierNow, "5m", "step", ""},
		{"unknown step", tierNow.Add(-time.Hour), tierNow, "1w", "step", ""},
		{"forced 1d", tierNow.Add(-time.Hour), tierNow, "1d", "", probe.Tier1d},
		{"forced raw", tierNow.Add(-time.Hour), tierNow, "raw", "", probe.TierRaw},
	} {
		tier, err := probe.SelectTier(c.from, c.to, tierNow, 30*time.Second, c.step)
		if fieldOf(err) != c.wantField || tier != c.want {
			t.Errorf("%s: tier %q, error %v; want tier %q, field %q", c.name, tier, err, c.want, c.wantField)
		}
	}
}

// fieldOf returns the field of a 422 (empty without error).
func fieldOf(err error) string {
	var fe *api.FieldError
	if err == nil {
		return ""
	}
	if !errors.As(err, &fe) || fe.Status != http.StatusUnprocessableEntity {
		return "not a 422: " + err.Error()
	}
	return fe.Field
}

// TestSelectTierCountsFromTheBucketOfFrom covers AK1/AK2: an aggregate tier
// reads from the bucket that holds from, so a from off the bucket grid
// counts one bucket more; a span of exactly 2,000 five-minute buckets from
// 12:02 is 2,001 buckets and no longer fits 5m.
func TestSelectTierCountsFromTheBucketOfFrom(t *testing.T) {
	from := tierNow.Add(-8 * day).Add(2 * time.Minute)
	to := from.Add(2000 * 5 * time.Minute)
	if _, err := probe.SelectTier(from, to, tierNow, time.Second, "5m"); err == nil || fieldOf(err) != "step" {
		t.Fatalf("step 5m over 2,001 buckets: %v, want 422 step", err)
	}
	if tier, err := probe.SelectTier(from, to, tierNow, time.Second, ""); err != nil || tier != probe.Tier1h {
		t.Fatalf("automatic tier %s (%v), want 1h", tier, err)
	}
	aligned := from.Truncate(5 * time.Minute)
	if tier, err := probe.SelectTier(aligned, aligned.Add(2000*5*time.Minute), tierNow, time.Second, ""); err != nil || tier != probe.Tier5m {
		t.Fatalf("aligned 2,000 buckets: tier %s (%v), want 5m", tier, err)
	}
}
