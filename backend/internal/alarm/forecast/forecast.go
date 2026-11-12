// Package forecast computes capacity forecasts (ALR-08, WP-1059, E-208):
// a least-squares line over the daily values of a window of 30 or 90 days,
// optionally with a weekly profile, and the day a threshold is crossed
// with an 80 % and 95 % prediction interval.
//
// The method is deterministic: the same input gives a bit-identical output
// (points sorted, sums in a fixed order, no maps, no random numbers). It
// needs only the standard library and knows nothing about storage; the
// daily job and the series of each metric are WP-1060. The method is
// documented for operators in docs/betrieb/kapazitaetsprognose.md.
package forecast

import (
	"math"
	"slices"
	"time"
)

// Status values of a forecast (AK5).
const (
	StatusExceeded         = "exceeded"
	StatusCrossing         = "crossing"
	StatusStable           = "stable"
	StatusInsufficientData = "insufficient_data"
)

// MinDays is the least number of days with a value in the window.
const MinDays = 14

// Defaults of Options.
const (
	DefaultWindowDays = 90
	DefaultSearchDays = 365
)

// day is one day.
const day = 24 * time.Hour

// DailyPoint is the value of one UTC day.
type DailyPoint struct {
	Day   time.Time `json:"day"`
	Value float64   `json:"value"`
}

// Options of a fit: WindowDays (30 or 90; 0 and any other value mean 90), Seasonal (weekly
// profile), Threshold and SearchDays (0 means 365), the days after the
// latest point searched for the crossing.
type Options struct {
	WindowDays int
	Seasonal   bool
	Threshold  float64
	SearchDays int
}

// Result of a fit. Slope is per day, Intercept the value at the start of
// the window (x = 0), R2 the coefficient of determination of the model
// (with the weekly profile when applied), Sigma the standard deviation of
// the residuals with n − 2 degrees of freedom, N the days with a value.
// Weekly holds the profile by time.Weekday (zero without seasonality).
// The crossing days are nil when there is none within SearchDays.
type Result struct {
	Status           string
	N                int
	Slope            float64
	Intercept        float64
	R2               float64
	Sigma            float64
	SeasonalApplied  bool
	Weekly           [7]float64
	WindowStart      time.Time
	LastDay          time.Time
	LastValue        float64
	CrossingAt       *time.Time
	CrossingEarliest *time.Time
	CrossingLatest   *time.Time

	xMean, sxx float64
}

// Prediction is the forecast of one day with its 80 % and 95 % prediction
// interval.
type Prediction struct {
	Value  float64
	Low80  float64
	High80 float64
	Low95  float64
	High95 float64
}

// Fit fits the daily points (AK1–AK5). Missing days are allowed; points of
// one day are averaged; the window ends at the latest point.
func Fit(points []DailyPoint, opts Options) Result {
	window := opts.WindowDays
	if window != 30 && window != 90 {
		// Only the windows of the specification (AK1): 0 and any other
		// value fall back to the standard window.
		window = DefaultWindowDays
	}
	search := opts.SearchDays
	if search <= 0 {
		search = DefaultSearchDays
	}
	pts := normalize(points)
	var r Result
	if len(pts) == 0 {
		r.Status = StatusInsufficientData
		return r
	}
	r.LastDay = pts[len(pts)-1].Day
	r.LastValue = pts[len(pts)-1].Value
	r.WindowStart = r.LastDay.Add(-time.Duration(window-1) * day)
	start := 0
	for start < len(pts) && pts[start].Day.Before(r.WindowStart) {
		start++
	}
	pts = pts[start:]
	r.N = len(pts)
	if r.N < MinDays {
		r.Status = StatusInsufficientData
		return r
	}
	xs := make([]float64, r.N)
	ys := make([]float64, r.N)
	for i, p := range pts {
		xs[i] = dayIndex(r.WindowStart, p.Day)
		ys[i] = p.Value
	}
	a, b := leastSquares(xs, ys)
	if opts.Seasonal && everyWeekdayTwice(pts) {
		// Mean residual per weekday of the first fit, subtracted from the
		// values; then a second fit.
		var sum [7]float64
		var count [7]int
		for i, p := range pts {
			w := p.Day.Weekday()
			sum[w] += ys[i] - (a + b*xs[i])
			count[w]++
		}
		for w := range 7 {
			r.Weekly[w] = sum[w] / float64(count[w])
		}
		adjusted := make([]float64, r.N)
		for i, p := range pts {
			adjusted[i] = ys[i] - r.Weekly[p.Day.Weekday()]
		}
		a, b = leastSquares(xs, adjusted)
		r.SeasonalApplied = true
	}
	r.Intercept, r.Slope = a, b
	var yMean, sse, sst, xMean, sxx float64
	for i := range ys {
		yMean += ys[i]
		xMean += xs[i]
	}
	yMean /= float64(r.N)
	xMean /= float64(r.N)
	for i, p := range pts {
		fit := a + b*xs[i] + r.Weekly[p.Day.Weekday()]
		sse += (ys[i] - fit) * (ys[i] - fit)
		sst += (ys[i] - yMean) * (ys[i] - yMean)
		sxx += (xs[i] - xMean) * (xs[i] - xMean)
	}
	switch {
	case sst > 0:
		r.R2 = 1 - sse/sst
	case sse == 0:
		r.R2 = 1
	}
	r.Sigma = math.Sqrt(sse / float64(r.N-2))
	r.xMean, r.sxx = xMean, sxx

	thr := opts.Threshold
	reached := func(v float64) bool { return v >= thr-1e-9*math.Max(1, math.Abs(thr)) }
	switch {
	case reached(r.LastValue):
		r.Status = StatusExceeded
		return r
	case r.Slope <= 0:
		r.Status = StatusStable
		return r
	}
	for d := 1; d <= search; d++ {
		at := r.LastDay.Add(time.Duration(d) * day)
		p := r.Predict(at)
		if r.CrossingAt == nil && reached(p.Value) {
			r.CrossingAt = ptr(at)
		}
		if r.CrossingEarliest == nil && reached(p.High95) {
			r.CrossingEarliest = ptr(at)
		}
		if r.CrossingLatest == nil && reached(p.Low95) {
			r.CrossingLatest = ptr(at)
		}
		if r.CrossingAt != nil && r.CrossingEarliest != nil && r.CrossingLatest != nil {
			break
		}
	}
	r.Status = StatusStable
	if r.CrossingAt != nil {
		r.Status = StatusCrossing
	}
	return r
}

// Predict returns the forecast of a day with its prediction intervals
// (AK4): ŷ(x₀) ± t(n − 2) · σ · √(1 + 1/n + (x₀ − x̄)²/Sxx). Without a fit
// (insufficient data) it is zero.
func (r *Result) Predict(at time.Time) Prediction {
	if r.N < MinDays || r.Status == StatusInsufficientData {
		return Prediction{}
	}
	at = truncate(at)
	x := dayIndex(r.WindowStart, at)
	v := r.Intercept + r.Slope*x + r.Weekly[at.Weekday()]
	se := r.Sigma * math.Sqrt(1+1/float64(r.N)+(x-r.xMean)*(x-r.xMean)/r.sxx)
	df := r.N - 2
	w80, w95 := TQuantile80(df)*se, TQuantile95(df)*se
	return Prediction{Value: v, Low80: v - w80, High80: v + w80, Low95: v - w95, High95: v + w95}
}

func ptr(t time.Time) *time.Time { return &t }

// leastSquares fits y = a + b·x in a fixed order of summation.
func leastSquares(xs, ys []float64) (a, b float64) {
	n := float64(len(xs))
	var sx, sy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
	}
	mx, my := sx/n, sy/n
	var sxx, sxy float64
	for i := range xs {
		dx := xs[i] - mx
		sxx += dx * dx
		sxy += dx * (ys[i] - my)
	}
	if sxx == 0 {
		return my, 0
	}
	b = sxy / sxx
	return my - b*mx, b
}

// everyWeekdayTwice reports at least two values per weekday (AK3).
func everyWeekdayTwice(pts []DailyPoint) bool {
	var count [7]int
	for _, p := range pts {
		count[p.Day.Weekday()]++
	}
	for _, c := range count {
		if c < 2 {
			return false
		}
	}
	return true
}

// normalize truncates the days to UTC midnight, sorts them and averages
// the values of one day; non-finite values are dropped.
func normalize(points []DailyPoint) []DailyPoint {
	pts := make([]DailyPoint, 0, len(points))
	for _, p := range points {
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		pts = append(pts, DailyPoint{Day: truncate(p.Day), Value: p.Value})
	}
	slices.SortStableFunc(pts, func(x, y DailyPoint) int { return x.Day.Compare(y.Day) })
	out := pts[:0]
	for i := 0; i < len(pts); {
		j, sum := i, 0.0
		for j < len(pts) && pts[j].Day.Equal(pts[i].Day) {
			sum += pts[j].Value
			j++
		}
		out = append(out, DailyPoint{Day: pts[i].Day, Value: sum / float64(j-i)})
		i = j
	}
	return out
}

func truncate(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// dayIndex is the number of days from start to d (both UTC midnight).
func dayIndex(start, d time.Time) float64 {
	return math.Round(d.Sub(start).Hours() / 24)
}

// Normal quantiles for more than 120 degrees of freedom.
const (
	z90  = 1.2815515655446004
	z975 = 1.959963984540054
)

// TQuantile80 is the 0.90 quantile of Student's t with df degrees of
// freedom, the half-width factor of the two-sided 80 % interval.
func TQuantile80(df int) float64 { return tQuantile(&t90, df, z90) }

// TQuantile95 is the 0.975 quantile of Student's t with df degrees of
// freedom, the half-width factor of the two-sided 95 % interval.
func TQuantile95(df int) float64 { return tQuantile(&t975, df, z975) }

func tQuantile(table *[120]float64, df int, normal float64) float64 {
	switch {
	case df < 1:
		return math.Inf(1)
	case df <= len(table):
		return table[df-1]
	}
	return normal
}

// t90 and t975 are the 0.90 and 0.975 quantiles of Student's t for 1 to
// 120 degrees of freedom (six decimals; regularized incomplete beta
// function, inverted by bisection).
var t90 = [120]float64{
	3.077684, 1.885618, 1.637744, 1.533206, 1.475884, 1.439756, 1.414924, 1.396815,
	1.383029, 1.372184, 1.363430, 1.356217, 1.350171, 1.345030, 1.340606, 1.336757,
	1.333379, 1.330391, 1.327728, 1.325341, 1.323188, 1.321237, 1.319460, 1.317836,
	1.316345, 1.314972, 1.313703, 1.312527, 1.311434, 1.310415, 1.309464, 1.308573,
	1.307737, 1.306952, 1.306212, 1.305514, 1.304854, 1.304230, 1.303639, 1.303077,
	1.302543, 1.302035, 1.301552, 1.301090, 1.300649, 1.300228, 1.299825, 1.299439,
	1.299069, 1.298714, 1.298373, 1.298045, 1.297730, 1.297426, 1.297134, 1.296853,
	1.296581, 1.296319, 1.296066, 1.295821, 1.295585, 1.295356, 1.295134, 1.294920,
	1.294712, 1.294511, 1.294315, 1.294126, 1.293942, 1.293763, 1.293589, 1.293421,
	1.293256, 1.293097, 1.292941, 1.292790, 1.292643, 1.292500, 1.292360, 1.292224,
	1.292091, 1.291961, 1.291835, 1.291711, 1.291591, 1.291473, 1.291358, 1.291246,
	1.291136, 1.291029, 1.290924, 1.290821, 1.290721, 1.290623, 1.290527, 1.290432,
	1.290340, 1.290250, 1.290161, 1.290075, 1.289990, 1.289907, 1.289825, 1.289745,
	1.289666, 1.289589, 1.289514, 1.289439, 1.289367, 1.289295, 1.289225, 1.289156,
	1.289088, 1.289022, 1.288957, 1.288892, 1.288829, 1.288767, 1.288706, 1.288646,
}

var t975 = [120]float64{
	12.706205, 4.302653, 3.182446, 2.776445, 2.570582, 2.446912, 2.364624, 2.306004,
	2.262157, 2.228139, 2.200985, 2.178813, 2.160369, 2.144787, 2.131450, 2.119905,
	2.109816, 2.100922, 2.093024, 2.085963, 2.079614, 2.073873, 2.068658, 2.063899,
	2.059539, 2.055529, 2.051831, 2.048407, 2.045230, 2.042272, 2.039513, 2.036933,
	2.034515, 2.032245, 2.030108, 2.028094, 2.026192, 2.024394, 2.022691, 2.021075,
	2.019541, 2.018082, 2.016692, 2.015368, 2.014103, 2.012896, 2.011741, 2.010635,
	2.009575, 2.008559, 2.007584, 2.006647, 2.005746, 2.004879, 2.004045, 2.003241,
	2.002465, 2.001717, 2.000995, 2.000298, 1.999624, 1.998972, 1.998341, 1.997730,
	1.997138, 1.996564, 1.996008, 1.995469, 1.994945, 1.994437, 1.993943, 1.993464,
	1.992997, 1.992543, 1.992102, 1.991673, 1.991254, 1.990847, 1.990450, 1.990063,
	1.989686, 1.989319, 1.988960, 1.988610, 1.988268, 1.987934, 1.987608, 1.987290,
	1.986979, 1.986675, 1.986377, 1.986086, 1.985802, 1.985523, 1.985251, 1.984984,
	1.984723, 1.984467, 1.984217, 1.983972, 1.983731, 1.983495, 1.983264, 1.983038,
	1.982815, 1.982597, 1.982383, 1.982173, 1.981967, 1.981765, 1.981567, 1.981372,
	1.981180, 1.980992, 1.980808, 1.980626, 1.980448, 1.980272, 1.980100, 1.979930,
}
