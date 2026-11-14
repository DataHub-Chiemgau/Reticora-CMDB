//go:build load

package probe

// Capacity test of the collector (WP-958, NFR-11, PRB-04): the complete
// engine (scheduler, state machine, ICMP multiplexer, spool, uplink, alive
// message) runs 5,000 ping probes every 30 s against a fake ICMP socket
// (round trip 1-50 ms, 10 % of the checks time out after 10 s) and a fake
// edge that acknowledges every message, for 3 min of real time.
//
//	cd collector && go test -tags load -run TestCollectorCapacity -timeout 20m ./probe/...
//
// The engine runs on the wall clock (the ticker of the scheduler, the
// deadlines of the multiplexer), so the run takes real time; the duration
// and the number of probes are set by environment variables for a reduced
// local run:
//
//	RETICORA_CAPACITY_PROBES    number of probes (default 5000)
//	RETICORA_CAPACITY_DURATION  real run time (default 3m, at least 90s)
//	RETICORA_CAPACITY_OUT       file the measured values go to as JSON
//
// Both tests share one run of the engine.
//
// Spool write rate (AK2, ≤ 2 files/s): checked for the result spool over
// the whole run and for the whole spool in the steady state. The cold start
// is excepted (deviation from AK2, recorded in the plan section of WP-958
// and in docs/ops/sizing.md): the first assignment writes one fsynced file
// of the state queue per probe (unknown → up), 5,000 files within the
// first interval. The state queue keeps one file per change on purpose (no
// state change is lost on a crash, PRB-07); after the cold start state
// files arise only with real changes.
//
// Even spread (AK3, PRB-04): the phase is FNV(probe_id) mod interval, so
// the starts per second are a random spread around the mean; the bound
// 1.5 × the mean holds for 5,000 probes (measured 1.18 ×), but not for a
// few hundred (500 probes: about 1.56 ×). TestCollectorCapacitySpreadsStarts
// therefore skips a reduced run below 5,000 probes; the catalogue's "from
// 100 probes" is not met by a pure hash phase (open finding to PRB-04,
// WP-951).

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io/fs"
	"math"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Budgets of WP-958 AK2 and AK3.
const (
	capacityDriftP99Limit  = 500 * time.Millisecond
	capacityHeapLimit      = 256 << 20
	capacitySpoolRateLimit = 2.0 // files per second
	capacitySpreadLimit    = 1.5 // × mean starts per second (PRB-04)
)

// Profile of the run (the defaults of the type icmp: interval 30 s, timeout
// 10 s, down count 5). A check sends one echo request: with the default of
// three requests 1 s apart a check holds a slot of
// max_parallel_per_collector (256) for about 2 s, and 5,000 probes with
// 10 % timeouts would need about 470 concurrent checks (docs/ops/sizing.md).
const (
	capacityInterval    = 30 * time.Second
	capacityTimeout     = 10 * time.Second
	capacityTimeoutRate = 10 // percent of the checks without reply
	capacityRTTMin      = 1  // ms
	capacityRTTMax      = 50 // ms
)

// capacityResult holds the measured values of a run; it is written to
// RETICORA_CAPACITY_OUT.
type capacityResult struct {
	Probes          int     `json:"probes"`
	IntervalS       float64 `json:"interval_s"`
	TimeoutS        float64 `json:"timeout_s"`
	TimeoutPercent  int     `json:"timeout_percent"`
	DurationS       float64 `json:"duration_s"`
	GoVersion       string  `json:"go_version"`
	NumCPU          int     `json:"num_cpu"`
	Checks          int     `json:"checks"`
	ChecksPerS      float64 `json:"checks_per_s"`
	DriftP50MS      float64 `json:"drift_p50_ms"`
	DriftP99MS      float64 `json:"drift_p99_ms"`
	DriftMaxMS      float64 `json:"drift_max_ms"`
	HeapPeakMiB     float64 `json:"heap_peak_mib"`
	SysPeakMiB      float64 `json:"sys_peak_mib"`
	GoroutinesPeak  int     `json:"goroutines_peak"`
	GoroutinesStart int     `json:"goroutines_start"`
	GoroutinesEnd   int     `json:"goroutines_end"`
	CPUSeconds      float64 `json:"cpu_seconds"`
	CPUCores        float64 `json:"cpu_cores"`
	// The spool writes: whole run (states include the first state of
	// every probe), steady state after two intervals.
	StateWrites       uint64  `json:"state_writes"`
	ResultWrites      uint64  `json:"result_writes"`
	ResultWriteRate   float64 `json:"result_write_rate_per_s"`
	SteadyWindowS     float64 `json:"steady_window_s"`
	SteadyWriteRate   float64 `json:"steady_spool_write_rate_per_s"`
	SpoolPeakBytes    int64   `json:"spool_peak_bytes"`
	ResultBytesPerRes float64 `json:"result_spool_bytes_per_result"`
	Overrun           uint64  `json:"overrun"`
	Skipped           uint64  `json:"skipped"`
	StatesSent        int64   `json:"states_sent"`
	ResultsSent       int64   `json:"results_sent"`
	Timeouts          int64   `json:"timeouts"`
	// The state changes the edge received by transition and reason.
	Transitions map[string]int `json:"transitions"`
	// The spool writes per 10 s from the start.
	WritesPer10s []uint64 `json:"spool_writes_per_10s"`
	// Starts per second over whole intervals (PRB-04).
	SpreadSeconds int     `json:"spread_seconds"`
	SpreadMean    float64 `json:"spread_mean_per_s"`
	SpreadMax     int     `json:"spread_max_per_s"`
	SpreadRatio   float64 `json:"spread_ratio"`
}

var (
	capacityOnce sync.Once
	capacityRes  *capacityResult
	capacityErr  error
)

// capacityRun runs the engine once for both tests.
func capacityRun(t *testing.T) *capacityResult {
	t.Helper()
	capacityOnce.Do(func() { capacityRes, capacityErr = runCapacity(t) })
	if capacityErr != nil {
		t.Fatal(capacityErr)
	}
	return capacityRes
}

// capacityMinDuration is the shortest run: two intervals before the steady
// state may begin, then room for the state queue of the cold start to
// drain and a steady window of its own.
const capacityMinDuration = 2*capacityInterval + 30*time.Second

func capacityEnv(t *testing.T) (probes int, duration time.Duration, err error) {
	t.Helper()
	probes, duration = 5000, 3*time.Minute
	if v := os.Getenv("RETICORA_CAPACITY_PROBES"); v != "" {
		if probes, err = strconv.Atoi(v); err != nil || probes < 100 {
			return 0, 0, fmt.Errorf("RETICORA_CAPACITY_PROBES=%q: want a number of at least 100", v)
		}
	}
	if v := os.Getenv("RETICORA_CAPACITY_DURATION"); v != "" {
		if duration, err = time.ParseDuration(v); err != nil || duration < capacityMinDuration {
			return 0, 0, fmt.Errorf("RETICORA_CAPACITY_DURATION=%q: want a duration of at least %s", v, capacityMinDuration)
		}
	}
	return probes, duration, nil
}

// capacityTarget is the address of probe i (10.0.0.0/8, one per probe).
func capacityTarget(i int) netip.Addr {
	n := uint32(i + 1) //nolint:gosec // i is below the number of probes
	return netip.AddrFrom4([4]byte{10, byte(n >> 16), byte(n >> 8), byte(n)})
}

// capacityHash is a deterministic pseudo random number of a target and a
// round of its probe.
func capacityHash(peer netip.Addr, round int64) uint64 {
	h := fnv.New64a()
	b := peer.As16()
	_, _ = h.Write(b[:])
	var r [8]byte
	binary.BigEndian.PutUint64(r[:], uint64(round)) //nolint:gosec // the bits of the round, sign irrelevant
	_, _ = h.Write(r[:])
	return h.Sum64()
}

// capacityRecorder collects the starts of the checks.
type capacityRecorder struct {
	mu        sync.Mutex
	drifts    []float64 // ms
	perSecond map[int64]int
}

func (c *capacityRecorder) record(spec *probewire.ProbeSpec, now time.Time) {
	// The due time of a check is the last instant ≡ Phase (mod interval);
	// the drift is how late the check starts after it (WP-951 AK1).
	off := mod(now.UnixMilli()-Phase(spec.ID, spec.IntervalMS), spec.IntervalMS)
	if off > spec.IntervalMS/2 {
		off -= spec.IntervalMS // started early (cannot happen; counted as is)
	}
	drift := float64(off) + float64(now.Nanosecond()%int(time.Millisecond))/1e6
	c.mu.Lock()
	c.drifts = append(c.drifts, drift)
	c.perSecond[now.Unix()]++
	c.mu.Unlock()
}

// capacityEdge answers the probe subjects like the server: 202 with the
// highest seq of the message, 200 with the server time for the alive
// message. It keeps only counts.
type capacityEdge struct {
	states, results, timeouts atomic.Int64
	mu                        sync.Mutex
	transitions               map[string]int
}

func (e *capacityEdge) send(_ context.Context, kind string, data []byte) ([]byte, error) {
	var top uint64
	switch kind {
	case KindAlive:
		now := time.Now().UTC()
		return json.Marshal(UplinkReply{Status: http.StatusOK, ServerTime: &now})
	case KindState:
		var sc probewire.StateChange
		if err := probewire.Decode(data, &sc); err != nil {
			return json.Marshal(UplinkReply{Status: http.StatusBadRequest})
		}
		e.states.Add(1)
		e.mu.Lock()
		e.transitions[string(sc.From)+"→"+string(sc.To)+" "+sc.Reason]++
		e.mu.Unlock()
		top = sc.Seq
	case KindResults:
		var batch probewire.ResultBatch
		if err := probewire.Decode(data, &batch); err != nil || len(batch.Results) == 0 {
			return json.Marshal(UplinkReply{Status: http.StatusBadRequest})
		}
		e.results.Add(int64(len(batch.Results)))
		for i := range batch.Results {
			if batch.Results[i].Code == probewire.ReasonTimeout {
				e.timeouts.Add(1)
			}
		}
		top = batch.Results[len(batch.Results)-1].Seq
	default:
		return json.Marshal(UplinkReply{Status: http.StatusBadRequest})
	}
	return json.Marshal(UplinkReply{Status: http.StatusAccepted, Seq: &top})
}

func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func cpuSeconds() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	tv := func(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
	return tv(ru.Utime) + tv(ru.Stime)
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

// bundleBytesPerResult is the size on disk of a full result bundle of
// typical ping results, per result: the basis of the 24 h spool in
// docs/ops/sizing.md.
func bundleBytesPerResult(o *Outbox) (float64, error) {
	results := make([]probewire.Result, OutboxBundleResults)
	at := time.Now().UTC()
	for i := range results {
		r := icmpResult([]PacketResult{{RTT: time.Duration(1+i%50) * time.Millisecond}})
		r.ProbeID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		r.Epoch, r.Seq, r.ObservedAt = 1, uint64(1_000_000+i), at.Add(time.Duration(i)*6*time.Millisecond) //nolint:gosec // i is small
		results[i] = r
	}
	msg, err := o.bundle(results, nil, false)
	if err != nil {
		return 0, err
	}
	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return 0, err
	}
	return float64(len(data)) / float64(len(results)), nil
}

//nolint:gocyclo,funlen // one sequential measurement
func runCapacity(t *testing.T) (*capacityResult, error) {
	probes, duration, err := capacityEnv(t)
	if err != nil {
		return nil, err
	}
	intervalMS := capacityInterval.Milliseconds()

	// Fake ICMP socket: round trip 1-50 ms; every tenth check of a target
	// (by its round) gets no reply and runs into the timeout.
	phases := make(map[netip.Addr]int64, probes)
	specs := make([]probewire.ProbeSpec, probes)
	for i := range specs {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		target := capacityTarget(i)
		phases[target] = Phase(id, intervalMS)
		specs[i] = probewire.ProbeSpec{
			ID: id, Type: TypeICMP, Target: target.String(),
			IntervalMS: intervalMS, TimeoutMS: capacityTimeout.Milliseconds(),
			DownCount: 5, UpCount: 1, Epoch: 1, Params: json.RawMessage(`{"count":1}`),
		}
	}
	round := func(peer netip.Addr) int64 {
		return (time.Now().UnixMilli() - phases[peer] + intervalMS/2) / intervalMS
	}
	conn := newFakeICMP(4, true)
	conn.drop = func(peer netip.Addr, _ int) bool {
		return capacityHash(peer, round(peer))%100 < capacityTimeoutRate
	}
	conn.delay = func(peer netip.Addr, seq int) time.Duration {
		h := capacityHash(peer, int64(seq)) >> 8
		return time.Duration(capacityRTTMin+int(h%uint64(capacityRTTMax-capacityRTTMin+1))) * time.Millisecond //nolint:gosec // below 50
	}
	mux := NewMux(func(family int) (PacketConn, bool, error) {
		if family != 4 {
			return nil, false, ErrICMPUnavailable
		}
		return conn, true, nil
	})
	orig, _ := Lookup(TypeICMP)
	unregister(TypeICMP)
	Register(TypeICMP, &ICMPExecutor{Mux: mux})
	defer func() {
		unregister(TypeICMP)
		Register(TypeICMP, orig)
	}()

	dir, err := os.MkdirTemp("", "reticora-capacity-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	rec := &capacityRecorder{perSecond: map[int64]int{}}
	run := func(ctx context.Context, spec *probewire.ProbeSpec, creds map[string]string) probewire.Result {
		rec.record(spec, time.Now())
		return Run(ctx, spec, creds)
	}
	edge := &capacityEdge{transitions: map[string]int{}}
	runtime.GC()
	goroutinesStart := runtime.NumGoroutine()

	e, err := NewEngine(&EngineConfig{SpoolDir: dir, Send: edge.send, Run: run, ICMP: mux})
	if err != nil {
		return nil, err
	}
	e.Apply(1, specs)

	res := &capacityResult{
		Probes: probes, IntervalS: capacityInterval.Seconds(), TimeoutS: capacityTimeout.Seconds(),
		TimeoutPercent: capacityTimeoutRate, GoVersion: runtime.Version(), NumCPU: runtime.NumCPU(),
		GoroutinesStart: goroutinesStart,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	cpu0, start := cpuSeconds(), time.Now()
	go func() { defer close(done); e.Run(ctx) }()

	// Sampling: heap and goroutines every second, the spool size every 5 s,
	// the spool writes every 10 s and at the start of the steady state.
	steadyAt := start.Add(2 * capacityInterval)
	var steadyWrites, lastWrites uint64
	var steadyStart time.Time
	writes := func() uint64 { return e.Outbox.States().Writes() + e.Outbox.Results().Writes() }
	tick := time.NewTicker(time.Second)
	end := time.After(duration)
	var ms runtime.MemStats
	for n := 0; ; n++ {
		select {
		case <-end:
		case now := <-tick.C:
			runtime.ReadMemStats(&ms)
			res.HeapPeakMiB = max(res.HeapPeakMiB, float64(ms.HeapAlloc)/(1<<20))
			res.SysPeakMiB = max(res.SysPeakMiB, float64(ms.Sys)/(1<<20))
			res.GoroutinesPeak = max(res.GoroutinesPeak, runtime.NumGoroutine())
			if n%5 == 0 {
				res.SpoolPeakBytes = max(res.SpoolPeakBytes, dirBytes(dir))
			}
			if n%10 == 0 {
				w := writes()
				res.WritesPer10s = append(res.WritesPer10s, w-lastWrites)
				lastWrites = w
			}
			// The steady state begins after two intervals once the first
			// state of every probe (unknown → up) has reached the edge and
			// left the spool: the cold start writes one state file per
			// probe.
			if steadyStart.IsZero() && !now.Before(steadyAt) && edge.states.Load() >= int64(probes) {
				if n, lenErr := e.Outbox.States().Len(ctx); lenErr == nil && n == 0 {
					steadyStart, steadyWrites = now, writes()
				}
			}
			continue
		}
		break
	}
	tick.Stop()
	steadyEnd, steadyEndWrites := time.Now(), writes()
	cancel()
	<-done
	res.DurationS = time.Since(start).Seconds()
	res.CPUSeconds = cpuSeconds() - cpu0
	res.CPUCores = res.CPUSeconds / res.DurationS
	mux.Close()

	// No goroutine may outlive the engine: wait for the timers of the fake
	// socket and the readers of the multiplexer.
	deadline := time.Now().Add(15 * time.Second)
	for {
		runtime.GC()
		res.GoroutinesEnd = runtime.NumGoroutine()
		if res.GoroutinesEnd <= goroutinesStart || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	st := e.Scheduler.Stats()
	res.Overrun, res.Skipped = st.Overrun, st.Skipped
	res.StateWrites, res.ResultWrites = e.Outbox.States().Writes(), e.Outbox.Results().Writes()
	res.ResultWriteRate = float64(res.ResultWrites) / res.DurationS
	if !steadyStart.IsZero() {
		res.SteadyWindowS = steadyEnd.Sub(steadyStart).Seconds()
		res.SteadyWriteRate = float64(steadyEndWrites-steadyWrites) / res.SteadyWindowS
	}
	res.StatesSent, res.ResultsSent, res.Timeouts = edge.states.Load(), edge.results.Load(), edge.timeouts.Load()
	edge.mu.Lock()
	res.Transitions = edge.transitions
	edge.mu.Unlock()
	res.ResultBytesPerRes, err = bundleBytesPerResult(e.Outbox)
	if err != nil {
		return nil, err
	}

	rec.mu.Lock()
	drifts := slices.Clone(rec.drifts)
	perSecond := rec.perSecond
	rec.mu.Unlock()
	slices.Sort(drifts)
	res.Checks = len(drifts)
	res.ChecksPerS = float64(res.Checks) / res.DurationS
	res.DriftP50MS = percentile(drifts, 0.50)
	res.DriftP99MS = percentile(drifts, 0.99)
	if len(drifts) > 0 {
		res.DriftMaxMS = drifts[len(drifts)-1]
	}

	// Whole intervals from the first complete second on.
	first := start.Unix() + 1
	intervalS := int64(capacityInterval.Seconds())
	whole := (steadyEnd.Unix() - first) / intervalS
	var sum int
	for s := first; s < first+whole*intervalS; s++ {
		n := perSecond[s]
		sum += n
		res.SpreadMax = max(res.SpreadMax, n)
		res.SpreadSeconds++
	}
	if res.SpreadSeconds > 0 {
		res.SpreadMean = float64(sum) / float64(res.SpreadSeconds)
		res.SpreadRatio = float64(res.SpreadMax) / res.SpreadMean
	}

	out, _ := json.MarshalIndent(res, "", "  ")
	t.Logf("capacity run:\n%s", out)
	if path := os.Getenv("RETICORA_CAPACITY_OUT"); path != "" {
		if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}
	return res, nil
}

// TestCollectorCapacity5000PingProbes covers WP-958 AK1 and AK2 (NFR-11):
// drift p99 below 500 ms, heap below 256 MiB, no goroutine left after the
// stop, at most two spool files per second, no overrun and no skipped
// check.
func TestCollectorCapacity5000PingProbes(t *testing.T) {
	r := capacityRun(t)
	if r.DriftP99MS >= float64(capacityDriftP99Limit.Milliseconds()) {
		t.Errorf("drift p99 = %.1f ms, want < %s", r.DriftP99MS, capacityDriftP99Limit)
	}
	if r.HeapPeakMiB >= capacityHeapLimit/(1<<20) {
		t.Errorf("heap peak = %.1f MiB, want < %d MiB", r.HeapPeakMiB, capacityHeapLimit/(1<<20))
	}
	if r.GoroutinesEnd > r.GoroutinesStart {
		t.Errorf("%d goroutines after the stop, %d before the start: leak", r.GoroutinesEnd, r.GoroutinesStart)
	}
	if r.ResultWriteRate > capacitySpoolRateLimit {
		t.Errorf("result spool: %.2f files/s, want ≤ %.0f", r.ResultWriteRate, capacitySpoolRateLimit)
	}
	if r.SteadyWindowS == 0 || r.SteadyWriteRate > capacitySpoolRateLimit {
		t.Errorf("spool in the steady state: %.2f files/s over %.0f s, want ≤ %.0f", r.SteadyWriteRate, r.SteadyWindowS, capacitySpoolRateLimit)
	}
	if r.Overrun != 0 || r.Skipped != 0 {
		t.Errorf("overrun = %d, skipped = %d, want 0 and 0", r.Overrun, r.Skipped)
	}
	// Plausibility: every probe ran in every interval and roughly a tenth
	// of the checks timed out.
	want := float64(r.Probes) / r.IntervalS
	if r.ChecksPerS < 0.9*want {
		t.Errorf("%.1f checks/s, want about %.1f", r.ChecksPerS, want)
	}
	if r.ResultsSent == 0 {
		t.Fatal("no result reached the edge")
	}
	if share := float64(r.Timeouts) / float64(r.ResultsSent); share < 0.05 || share > 0.15 {
		t.Errorf("%.1f %% timeouts among the results, want about %d %%", 100*share, capacityTimeoutRate)
	}
}

// capacitySpreadProbes is the number of probes AK3 states the even spread
// for (see the head of the file for smaller runs).
const capacitySpreadProbes = 5000

// TestCollectorCapacitySpreadsStarts covers WP-958 AK3 (PRB-04): under the
// real engine with 5,000 probes no second of whole intervals starts more
// than 1.5 × the mean.
func TestCollectorCapacitySpreadsStarts(t *testing.T) {
	r := capacityRun(t)
	if r.Probes < capacitySpreadProbes {
		t.Skipf("reduced run with %d probes: the spread bound of AK3 is stated for %d probes (phase = hash mod interval; measured %.2f ×)",
			r.Probes, capacitySpreadProbes, r.SpreadRatio)
	}
	if r.SpreadSeconds < int(r.IntervalS) {
		t.Fatalf("only %d seconds of whole intervals measured", r.SpreadSeconds)
	}
	if r.SpreadRatio > capacitySpreadLimit {
		t.Errorf("busiest second starts %d checks, %.2f × the mean of %.1f, want ≤ %.1f ×",
			r.SpreadMax, r.SpreadRatio, r.SpreadMean, capacitySpreadLimit)
	}
}
