package probe

import (
	"testing"
	"time"
)

// feed runs offsets (ms) through a fresh evaluation, one alive message
// every 10 s, and returns every step.
func feed(st *skewState, start time.Time, offsets ...int64) []skewStep {
	out := make([]skewStep, 0, len(offsets))
	for i, o := range offsets {
		s := st.step(o, start.Add(time.Duration(i)*10*time.Second))
		*st = s.next
		out = append(out, s)
	}
	return out
}

// TestClockSkewMedianOverSixValues: the median of the last six offsets,
// the mean of the middle two for an even count; an outlier does not move
// it (WP-962 AK2).
func TestClockSkewMedianOverSixValues(t *testing.T) {
	for _, tc := range []struct {
		in   []int64
		want int64
	}{
		{nil, 0},
		{[]int64{5}, 5},
		{[]int64{1, 9}, 5},
		{[]int64{3, 1, 2}, 2},
		{[]int64{10, 10, 10, 10, 10, 900_000}, 10},
		{[]int64{-4, -2, -8, -6}, -5},
	} {
		if got := median(tc.in); got != tc.want {
			t.Errorf("median(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
	var st skewState
	steps := feed(&st, time.Now(), 100, 100, 100, 100, 100, 100, 50_000, 50_000)
	if got := steps[len(steps)-1].median; got != 100 {
		t.Fatalf("two outliers among six: median %d, want 100", got)
	}
	if len(st.offsets) != ClockSkewWindow {
		t.Fatalf("window holds %d offsets, want %d", len(st.offsets), ClockSkewWindow)
	}
}

// TestClockSkewThresholdsAndHysteresis: one warning above 2 s, one degrade
// above 60 s, healthy again after six medians below 30 s, resolved after six
// medians below 2 s (WP-962 AK3).
func TestClockSkewThresholdsAndHysteresis(t *testing.T) {
	var st skewState
	now := time.Now()
	count := func(steps []skewStep) (warn, degrade, recoverN, resolve int) {
		for _, s := range steps {
			if s.warn {
				warn++
			}
			if s.degrade {
				degrade++
			}
			if s.recover {
				recoverN++
			}
			if s.resolve {
				resolve++
			}
		}
		return
	}
	// Below 2 s: nothing.
	steps := feed(&st, now, 1_000, 1_500, 1_999, -1_999)
	if w, d, _, _ := count(steps); w+d != 0 || st.warned {
		t.Fatalf("below 2 s: %d warnings, %d degrades", w, d)
	}
	// Above 2 s: exactly one warning, however long it lasts.
	steps = feed(&st, now, 5_000, 5_000, 5_000, 5_000, 5_000, 5_000, 5_000)
	if w, d, _, _ := count(steps); w != 1 || d != 0 {
		t.Fatalf("above 2 s: %d warnings, %d degrades, want 1 and 0", w, d)
	}
	// Above 60 s: exactly one degrade.
	steps = feed(&st, now, 90_000, 90_000, 90_000, 90_000, 90_000, 90_000, 90_000)
	if w, d, _, _ := count(steps); w != 0 || d != 1 || !st.degraded {
		t.Fatalf("above 60 s: %d warnings, %d degrades, want 0 and 1", w, d)
	}
	// Between 30 s and 60 s: stays degraded.
	steps = feed(&st, now, 40_000, 40_000, 40_000, 40_000, 40_000, 40_000, 40_000, 40_000)
	if _, _, r, _ := count(steps); r != 0 || !st.degraded {
		t.Fatalf("between 30 s and 60 s: %d recoveries, degraded %v", r, st.degraded)
	}
	// Below 30 s for six medians: healthy again, exactly once; the warning
	// stays open above 2 s.
	steps = feed(&st, now, 10_000, 10_000, 10_000, 10_000, 10_000, 10_000, 10_000, 10_000, 10_000)
	if _, _, r, res := count(steps); r != 1 || res != 0 || st.degraded || !st.warned {
		t.Fatalf("below 30 s: %d recoveries, %d resolutions, degraded %v, warned %v", r, res, st.degraded, st.warned)
	}
	// Below 2 s: resolved once the median stayed there for six offsets;
	// one median above 2 s in between starts the count again.
	steps = feed(&st, now, 0, 0, 0, 0, 0, 3_000, 3_000, 3_000, 3_000, 3_000, 3_000)
	if _, _, _, res := count(steps); res != 0 || !st.warned {
		t.Fatalf("interrupted run below 2 s: %d resolutions, warned %v", res, st.warned)
	}
	steps = feed(&st, now, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if w, _, _, res := count(steps); res != 1 || w != 0 || st.warned {
		t.Fatalf("below 2 s for six medians: %d resolutions, %d warnings, warned %v", res, w, st.warned)
	}
	// A new crossing warns again.
	steps = feed(&st, now, 3_000, 3_000, 3_000, 3_000)
	if w, _, _, _ := count(steps); w != 1 {
		t.Fatalf("new crossing: %d warnings, want 1", w)
	}
}

// TestClockSkewWarningThrottled: the log warning at most once every 5 min
// per collector while the offset stays above 2 s.
func TestClockSkewWarningThrottled(t *testing.T) {
	var st skewState
	now := time.Now()
	logs := 0
	for i := range 60 { // 10 min of alive messages
		s := st.step(3_000, now.Add(time.Duration(i)*10*time.Second))
		st = s.next
		if s.log {
			logs++
		}
	}
	if logs != 2 {
		t.Fatalf("%d log warnings in 10 min, want 2", logs)
	}
}

// TestClockSkewSign: a collector behind the server has a negative offset;
// the thresholds apply to the absolute value, and the correction subtracts
// the signed offset.
func TestClockSkewSign(t *testing.T) {
	var st skewState
	steps := feed(&st, time.Now(), -90_000, -90_000, -90_000)
	last := steps[len(steps)-1]
	if last.median != -90_000 || !st.warned || !st.degraded {
		t.Fatalf("behind by 90 s: median %d, warned %v, degraded %v", last.median, st.warned, st.degraded)
	}
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	ahead, behind := int32(90_000), int32(-90_000)
	if got := CorrectObservedAt(at, &ahead); !got.Equal(at.Add(-90 * time.Second)) {
		t.Fatalf("collector ahead by 90 s: corrected to %v", got)
	}
	if got := CorrectObservedAt(at, &behind); !got.Equal(at.Add(90 * time.Second)) {
		t.Fatalf("collector behind by 90 s: corrected to %v", got)
	}
	if got := CorrectObservedAt(at, nil); !got.Equal(at) {
		t.Fatalf("no correction: %v", got)
	}
}
