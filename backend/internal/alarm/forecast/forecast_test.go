package forecast

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"reflect"
	"testing"
	"time"
)

// start is a Monday; the test series begin there.
var start = time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)

func dayAt(x int) time.Time { return start.Add(time.Duration(x) * day) }

func line(n int, f func(x int) float64) []DailyPoint {
	pts := make([]DailyPoint, n)
	for x := range n {
		pts[x] = DailyPoint{Day: dayAt(x), Value: f(x)}
	}
	return pts
}

// TestFitExactLine covers AK2 and AK5: y = 0.2 + 0.005·x over 90 days,
// threshold 0.8 → slope 0.005, R² = 1, crossing at x = 120 (31 days after
// the latest point), all three crossing days equal.
func TestFitExactLine(t *testing.T) {
	r := Fit(line(90, func(x int) float64 { return 0.2 + 0.005*float64(x) }), Options{WindowDays: 90, Threshold: 0.8})
	if math.Abs(r.Slope-0.005) > 1e-9 || math.Abs(r.Intercept-0.2) > 1e-9 || math.Abs(r.R2-1) > 1e-9 || r.N != 90 {
		t.Fatalf("slope %v intercept %v R² %v n %d", r.Slope, r.Intercept, r.R2, r.N)
	}
	if r.Status != StatusCrossing || r.CrossingAt == nil || !r.CrossingAt.Equal(dayAt(120)) {
		t.Fatalf("status %s crossing %v, want crossing at %v", r.Status, r.CrossingAt, dayAt(120))
	}
	if r.CrossingAt.Sub(r.LastDay) != 31*day {
		t.Errorf("crossing %v after the latest point, want 31 days", r.CrossingAt.Sub(r.LastDay))
	}
	if r.CrossingEarliest == nil || r.CrossingLatest == nil || !r.CrossingEarliest.Equal(*r.CrossingAt) || !r.CrossingLatest.Equal(*r.CrossingAt) {
		t.Errorf("earliest %v latest %v, want %v", r.CrossingEarliest, r.CrossingLatest, r.CrossingAt)
	}
	// A window of 30 days uses the last 30 points only.
	r30 := Fit(line(90, func(x int) float64 { return 0.2 + 0.005*float64(x) }), Options{WindowDays: 30, Threshold: 0.8})
	if r30.N != 30 || !r30.WindowStart.Equal(dayAt(60)) || math.Abs(r30.Slope-0.005) > 1e-9 {
		t.Errorf("30-day window: n %d start %v slope %v", r30.N, r30.WindowStart, r30.Slope)
	}
	// Only 30 and 90 are windows (AK1): 0 and any other value mean 90.
	pts := line(120, func(x int) float64 { return 0.2 + 0.005*float64(x) })
	for _, w := range []int{0, -30, 7, 45, 400} {
		if r := Fit(pts, Options{WindowDays: w, Threshold: 2}); r.N != 90 || !r.WindowStart.Equal(dayAt(30)) {
			t.Errorf("window %d: n %d start %v, want the 90-day window", w, r.N, r.WindowStart)
		}
	}
}

// TestFitNoisyIntervalCoverage covers AK4: 1,000 runs with a fixed seed
// and σ = 0.02; a new observation 30 days ahead lies in the 95 % interval
// in 93–97 % of the runs.
func TestFitNoisyIntervalCoverage(t *testing.T) {
	rng := rand.New(rand.NewPCG(1059, 1))
	inside80, inside95 := 0, 0
	const runs = 1000
	for range runs {
		pts := line(90, func(x int) float64 { return 0.3 + 0.002*float64(x) + 0.02*rng.NormFloat64() })
		r := Fit(pts, Options{WindowDays: 90, Threshold: 10})
		x0 := 119
		actual := 0.3 + 0.002*float64(x0) + 0.02*rng.NormFloat64()
		p := r.Predict(dayAt(x0))
		if actual >= p.Low95 && actual <= p.High95 {
			inside95++
		}
		if actual >= p.Low80 && actual <= p.High80 {
			inside80++
		}
	}
	if inside95 < 930 || inside95 > 970 {
		t.Errorf("95 %% interval covers %d of %d runs, want 930–970", inside95, runs)
	}
	if inside80 < 770 || inside80 > 830 {
		t.Errorf("80 %% interval covers %d of %d runs, want 770–830", inside80, runs)
	}
}

// TestFitWeeklySeasonality covers AK3: a weekend dip of 0.2 on a linear
// trend; with Seasonal R² ≥ 0.95 and slope error < 5 %, without it a
// smaller R².
func TestFitWeeklySeasonality(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	pts := line(90, func(x int) float64 {
		v := 0.2 + 0.005*float64(x) + 0.005*rng.NormFloat64()
		if w := dayAt(x).Weekday(); w == time.Saturday || w == time.Sunday {
			v -= 0.2
		}
		return v
	})
	with := Fit(pts, Options{WindowDays: 90, Seasonal: true, Threshold: 2})
	without := Fit(pts, Options{WindowDays: 90, Threshold: 2})
	if !with.SeasonalApplied || with.R2 < 0.95 || math.Abs(with.Slope-0.005)/0.005 >= 0.05 {
		t.Errorf("seasonal: applied %v R² %v slope %v", with.SeasonalApplied, with.R2, with.Slope)
	}
	if without.SeasonalApplied || without.R2 >= with.R2 {
		t.Errorf("without seasonality: R² %v, with %v", without.R2, with.R2)
	}
	if with.Weekly[time.Saturday] > -0.1 || with.Weekly[time.Monday] < 0 {
		t.Errorf("weekly profile %v", with.Weekly)
	}
	// The profile enters the forecast: a Saturday is predicted lower.
	if sat, mon := with.Predict(dayAt(96)), with.Predict(dayAt(98)); sat.Value >= mon.Value-0.15 {
		t.Errorf("Saturday %v, Monday %v", sat.Value, mon.Value)
	}
	// Two values per weekday suffice (14 days).
	if short := Fit(pts[:14], Options{WindowDays: 90, Seasonal: true, Threshold: 2}); !short.SeasonalApplied {
		t.Error("seasonality not applied with two values per weekday")
	}
	// Fewer than two values per weekday: no seasonality.
	gappy := append([]DailyPoint{}, pts[:60]...)
	filtered := gappy[:0]
	for _, p := range gappy {
		if p.Day.Weekday() != time.Wednesday || p.Day.Equal(dayAt(2)) {
			filtered = append(filtered, p)
		}
	}
	if r := Fit(filtered, Options{WindowDays: 90, Seasonal: true, Threshold: 2}); r.SeasonalApplied {
		t.Error("seasonality applied with one Wednesday")
	}
}

// TestFitInsufficientData: 13 days are not enough.
func TestFitInsufficientData(t *testing.T) {
	r := Fit(line(13, func(x int) float64 { return float64(x) }), Options{Threshold: 100})
	if r.Status != StatusInsufficientData || r.CrossingAt != nil {
		t.Fatalf("status %s", r.Status)
	}
	if r := Fit(nil, Options{}); r.Status != StatusInsufficientData {
		t.Fatalf("empty: %s", r.Status)
	}
	// 13 days inside a 30-day window of a longer series.
	pts := line(60, func(x int) float64 { return float64(x) })
	var sparse []DailyPoint
	for i, p := range pts {
		if i < 30 || i%3 == 0 {
			sparse = append(sparse, p)
		}
	}
	if r := Fit(sparse, Options{WindowDays: 30, Threshold: 100}); r.Status != StatusInsufficientData {
		t.Fatalf("10 days in the window: %s (n %d)", r.Status, r.N)
	}
}

// TestFitFlatOrFalling: a flat or falling series is stable.
func TestFitFlatOrFalling(t *testing.T) {
	for name, f := range map[string]func(int) float64{
		"flat":    func(int) float64 { return 0.5 },
		"falling": func(x int) float64 { return 0.7 - 0.002*float64(x) },
	} {
		r := Fit(line(60, f), Options{Threshold: 0.8})
		if r.Status != StatusStable || r.CrossingAt != nil {
			t.Errorf("%s: %s %v", name, r.Status, r.CrossingAt)
		}
	}
	// Rising, but not within the search days.
	r := Fit(line(60, func(x int) float64 { return 0.1 + 0.0001*float64(x) }), Options{Threshold: 0.8, SearchDays: 30})
	if r.Status != StatusStable {
		t.Errorf("slow rise: %s", r.Status)
	}
}

// TestFitAlreadyExceeded: the latest value is at or above the threshold.
func TestFitAlreadyExceeded(t *testing.T) {
	r := Fit(line(30, func(x int) float64 { return 0.5 + 0.01*float64(x) }), Options{Threshold: 0.79})
	if r.Status != StatusExceeded {
		t.Fatalf("status %s, last %v", r.Status, r.LastValue)
	}
}

// TestFitGaps: missing days and several values of one day are allowed; x
// counts days, not points.
func TestFitGaps(t *testing.T) {
	var pts []DailyPoint
	for x := 0; x < 90; x++ {
		if x%4 == 1 || x%7 == 3 {
			continue
		}
		v := 0.2 + 0.005*float64(x)
		// Two readings of one day average to the value, at any time.
		pts = append(pts, DailyPoint{Day: dayAt(x).Add(13 * time.Hour), Value: v + 0.01}, DailyPoint{Day: dayAt(x).Add(time.Hour), Value: v - 0.01})
	}
	r := Fit(pts, Options{Threshold: 0.8})
	if math.Abs(r.Slope-0.005) > 1e-9 || r.Status != StatusCrossing || !r.CrossingAt.Equal(dayAt(120)) {
		t.Fatalf("slope %v status %s crossing %v", r.Slope, r.Status, r.CrossingAt)
	}
	if r.N >= 90 || !r.LastDay.Equal(dayAt(88)) {
		t.Fatalf("n %d last %v", r.N, r.LastDay)
	}
}

// TestFitDeterministic covers AK6: ten calls give bit-identical results,
// also with shuffled input.
func TestFitDeterministic(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	pts := line(90, func(x int) float64 { return 0.3 + 0.003*float64(x) + 0.03*rng.NormFloat64() })
	opts := Options{WindowDays: 90, Seasonal: true, Threshold: 0.8}
	first := Fit(pts, opts)
	for i := range 10 {
		shuffled := append([]DailyPoint{}, pts...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got := Fit(shuffled, opts)
		if !reflect.DeepEqual(got, first) || math.Float64bits(got.Slope) != math.Float64bits(first.Slope) {
			t.Fatalf("run %d differs: %+v vs %+v", i, got, first)
		}
	}
}

// TestTQuantiles: the table matches known quantiles; above 120 degrees of
// freedom the normal distribution applies.
func TestTQuantiles(t *testing.T) {
	for _, c := range []struct {
		df       int
		q80, q95 float64
	}{{1, 3.0777, 12.7062}, {10, 1.3722, 2.2281}, {30, 1.3104, 2.0423}, {120, 1.2886, 1.9799}, {121, 1.2816, 1.9600}} {
		if math.Abs(TQuantile80(c.df)-c.q80) > 1e-4 || math.Abs(TQuantile95(c.df)-c.q95) > 1e-4 {
			t.Errorf("df %d: %v %v", c.df, TQuantile80(c.df), TQuantile95(c.df))
		}
	}
}

type goldenSeries struct {
	Name    string `json:"name"`
	Options struct {
		WindowDays int     `json:"window_days"`
		Seasonal   bool    `json:"seasonal"`
		Threshold  float64 `json:"threshold"`
	} `json:"options"`
	Points []DailyPoint `json:"points"`
	Expect struct {
		Status          string  `json:"status"`
		Slope           float64 `json:"slope"`
		SeasonalApplied bool    `json:"seasonal_applied"`
		CrossingAt      string  `json:"crossing_at"`
	} `json:"expect"`
}

// goldenTolerances are the tolerances of TestFitGolden per series: they
// stand in the test, not next to the data, so loosening one is a visible
// change of the test. The 30-day window without seasonality sees the
// weekend dip as noise, hence its lower R² and wider crossing tolerance.
var goldenTolerances = map[string]struct {
	slope        float64 // |slope − expected| per day
	r2Min        float64
	crossingDays float64 // |crossing − expected| in days
}{
	"uplink-trend-weekly": {slope: 0.0002, r2Min: 0.95, crossingDays: 7},
	"uplink-trend-30d":    {slope: 0.0005, r2Min: 0.3, crossingDays: 7},
	"access-flat":         {slope: 0.0003, r2Min: 0.5},
	"backbone-saturated":  {slope: 0.0002, r2Min: 0.5},
}

// TestFitGolden: the series of testdata/series.json (interface utilisation
// with trend and weekly pattern) against the documented values of their
// generating model; the tolerances stand in goldenTolerances.
func TestFitGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/series.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Series []goldenSeries `json:"series"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Series) == 0 || len(doc.Series) != len(goldenTolerances) {
		t.Fatalf("%d series, %d tolerances: every series has its tolerances", len(doc.Series), len(goldenTolerances))
	}
	for _, s := range doc.Series {
		r := Fit(s.Points, Options{WindowDays: s.Options.WindowDays, Seasonal: s.Options.Seasonal, Threshold: s.Options.Threshold})
		e := s.Expect
		tol, ok := goldenTolerances[s.Name]
		if !ok {
			t.Errorf("%s: no tolerances in goldenTolerances", s.Name)
			continue
		}
		if r.Status != e.Status || math.Abs(r.Slope-e.Slope) > tol.slope || r.R2 < tol.r2Min || r.SeasonalApplied != e.SeasonalApplied {
			t.Errorf("%s: status %s slope %v R² %v seasonal %v, want %+v within %+v", s.Name, r.Status, r.Slope, r.R2, r.SeasonalApplied, e, tol)
			continue
		}
		if e.CrossingAt != "" {
			want, err := time.Parse(time.DateOnly, e.CrossingAt)
			if err != nil {
				t.Fatal(err)
			}
			if r.CrossingAt == nil || math.Abs(r.CrossingAt.Sub(want).Hours()/24) > tol.crossingDays {
				t.Errorf("%s: crossing %v, want %s ± %v days", s.Name, r.CrossingAt, e.CrossingAt, tol.crossingDays)
			}
			if r.CrossingEarliest == nil || r.CrossingEarliest.After(*r.CrossingAt) ||
				(r.CrossingLatest != nil && r.CrossingLatest.Before(*r.CrossingAt)) {
				t.Errorf("%s: interval %v–%v around %v", s.Name, r.CrossingEarliest, r.CrossingLatest, r.CrossingAt)
			}
		}
		t.Logf("%s: %s slope %.6f R² %.4f σ %.4f crossing %v (%v–%v)", s.Name, r.Status, r.Slope, r.R2, r.Sigma, r.CrossingAt, r.CrossingEarliest, r.CrossingLatest)
	}
}
