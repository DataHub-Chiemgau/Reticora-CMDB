package rpki

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakertr"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

var (
	vrpA = fakertr.VRP{Prefix: netip.MustParsePrefix("192.0.2.0/24"), MaxLen: 24, ASN: 64500}
	vrpB = fakertr.VRP{Prefix: netip.MustParsePrefix("198.51.100.0/22"), MaxLen: 24, ASN: 64501}
	vrpC = fakertr.VRP{Prefix: netip.MustParsePrefix("2001:db8::/32"), MaxLen: 48, ASN: 64502}
	vrpD = fakertr.VRP{Prefix: netip.MustParsePrefix("203.0.113.0/24"), MaxLen: 24, ASN: 64503}
)

func startCache(t *testing.T, cfg fakertr.Config) *fakertr.Server {
	t.Helper()
	s, err := fakertr.Listen(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newManager(t *testing.T) *Manager {
	t.Helper()
	m := &Manager{}
	t.Cleanup(m.Close)
	return m
}

func rtrSpec(id string, port int) probewire.ProbeSpec {
	return probewire.ProbeSpec{ID: id, Type: TypeRTR, Target: "127.0.0.1", IntervalMS: 300_000, TimeoutMS: 30_000,
		DownCount: 3, UpCount: 1, Epoch: 1, Params: json.RawMessage(`{"port":` + strconv.Itoa(port) + `}`)}
}

func check(t *testing.T, m *Manager, s probewire.ProbeSpec, timeout time.Duration) probewire.Result { //nolint:gocritic // a copy per check
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r := (&RTRExecutor{Manager: m}).Execute(ctx, s, nil)
	r.ProbeID = s.ID
	if err := r.Validate(); err != nil {
		t.Fatalf("result invalid: %v (%+v)", err, r)
	}
	return r
}

func want(t *testing.T, r probewire.Result, status probewire.Status, code string) { //nolint:gocritic // results are small
	t.Helper()
	if r.Status != status || r.Code != code {
		t.Fatalf("result = %s/%s (labels %v, values %v), want %s/%s", r.Status, r.Code, r.Labels, r.Values, status, code)
	}
}

func vrps(af string) string { return probewire.ValueKey(ValueVRPs, map[string]string{LabelAF: af}) }

func addrOf(s *fakertr.Server) string { return validatorAddr("127.0.0.1", s.Addr().Port) }

// eventually polls cond for up to d.
func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func tableSet(tb *Table) []string {
	var out []string
	for _, v := range tb.VRPs() {
		out = append(out, v.Prefix.String()+"-"+strconv.Itoa(int(v.MaxLen))+"-AS"+strconv.Itoa(int(v.ASN)))
	}
	slices.Sort(out)
	return out
}

func vrpKey(v fakertr.VRP) string {
	return v.Prefix.String() + "-" + strconv.Itoa(int(v.MaxLen)) + "-AS" + strconv.Itoa(int(v.ASN))
}

func keys(vs ...fakertr.VRP) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = vrpKey(v)
	}
	slices.Sort(out)
	return out
}

// TestRTRResetQuery covers WP-1020 AK1 and AK4: the first check sends a
// version 1 Reset Query, reads the full set (Router Keys skipped) and
// reports serial, VRPs per family and the protocol version.
func TestRTRResetQuery(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 10, VRPs: []fakertr.VRP{vrpA, vrpB, vrpC}, RouterKeys: true})
	m := newManager(t)
	r := check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second)
	want(t, r, probewire.StatusOK, "")
	for k, v := range map[string]float64{ValueUp: 1, ValueSerial: 10, vrps("ipv4"): 2, vrps("ipv6"): 1, ValueProtocolVersion: 1} {
		if r.Values[k] != v {
			t.Errorf("%s = %v, want %v", k, r.Values[k], v)
		}
	}
	if _, ok := r.Values[ValueDataAge]; !ok {
		t.Error("no data age")
	}
	if q := s.Queries(); len(q) != 1 || q[0].Type != fakertr.TypeResetQuery || q[0].Version != 1 {
		t.Fatalf("queries = %+v", q)
	}
	if got := tableSet(m.Table(addrOf(s))); !slices.Equal(got, keys(vrpA, vrpB, vrpC)) {
		t.Fatalf("table = %v", got)
	}
}

// TestRTRIncrementalSerialWithWithdraw covers WP-1020 AK2 and AK3: Serial
// Notify leads to a Serial Query with the held serial; announce and
// withdraw apply incrementally at End of Data.
func TestRTRIncrementalSerialWithWithdraw(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 10, VRPs: []fakertr.VRP{vrpA, vrpB, vrpC}})
	m := newManager(t)
	want(t, check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second), probewire.StatusOK, "")
	s.Update(fakertr.Delta{Announce: []fakertr.VRP{vrpD}, Withdraw: []fakertr.VRP{vrpB}})
	v := m.validator(addrOf(s))
	eventually(t, 5*time.Second, "serial 11", func() bool { return v.snapshot().serial == 11 })
	if got := tableSet(m.Table(addrOf(s))); !slices.Equal(got, keys(vrpA, vrpC, vrpD)) {
		t.Fatalf("table = %v", got)
	}
	q := s.Queries()
	if len(q) != 2 || q[1].Type != fakertr.TypeSerialQuery || q[1].Serial != 10 || q[1].Session != 7 {
		t.Fatalf("queries = %+v", q)
	}
	r := check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second)
	if r.Values[ValueSerial] != 11 || r.Values[vrps("ipv4")] != 2 {
		t.Fatalf("values = %v", r.Values)
	}
	if s.Accepted() != 1 {
		t.Fatalf("connections = %d, want 1", s.Accepted())
	}
}

// TestRTRCacheReset covers WP-1020 AK2: Cache Reset leads to a Reset Query
// and a full rebuild.
func TestRTRCacheReset(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 10, VRPs: []fakertr.VRP{vrpA, vrpB}})
	m := newManager(t)
	want(t, check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second), probewire.StatusOK, "")
	s.CacheResetNext()
	s.Update(fakertr.Delta{Announce: []fakertr.VRP{vrpC}, Withdraw: []fakertr.VRP{vrpA}})
	v := m.validator(addrOf(s))
	eventually(t, 5*time.Second, "serial 11", func() bool { return v.snapshot().serial == 11 })
	q := s.Queries()
	if len(q) != 3 || q[1].Type != fakertr.TypeSerialQuery || q[2].Type != fakertr.TypeResetQuery {
		t.Fatalf("queries = %+v, want reset, serial, reset", q)
	}
	if got := tableSet(m.Table(addrOf(s))); !slices.Equal(got, keys(vrpB, vrpC)) {
		t.Fatalf("table = %v", got)
	}
}

// TestRTRSessionIDChangeRebuilds covers WP-1020 AK2: a new session ID of
// the cache (restart) rebuilds the table with a Reset Query.
func TestRTRSessionIDChangeRebuilds(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 10, VRPs: []fakertr.VRP{vrpA, vrpB}})
	m := newManager(t)
	want(t, check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second), probewire.StatusOK, "")
	s.SetSessionID(8)
	s.Update(fakertr.Delta{Withdraw: []fakertr.VRP{vrpB}, Announce: []fakertr.VRP{vrpD}})
	v := m.validator(addrOf(s))
	eventually(t, 5*time.Second, "session 8", func() bool { st := v.snapshot(); return st.sessionID == 8 && st.serial == 11 })
	if got := tableSet(m.Table(addrOf(s))); !slices.Equal(got, keys(vrpA, vrpD)) {
		t.Fatalf("table = %v", got)
	}
	if q := s.Queries(); q[len(q)-1].Type != fakertr.TypeResetQuery {
		t.Fatalf("queries = %+v, want a final Reset Query", q)
	}

	// A Cache Response with another session ID than the held one: the
	// answer is dropped and the table rebuilt with a Reset Query.
	v.mu.Lock()
	v.haveSess = true
	v.mu.Unlock()
	c1, c2 := net.Pipe()
	defer c1.Close() //nolint:errcheck // test cleanup
	defer c2.Close() //nolint:errcheck // test cleanup
	go func() { _, _ = io.Copy(io.Discard, c2) }()
	next, err := v.handle(c1, 1, &pdu{typ: TypeCacheResponse, session: 9}, nil)
	if err != nil || next == nil || !next.discard {
		t.Fatalf("changed session: delta %+v, err %v", next, err)
	}
}

// TestRTRVersionFallbackToV0 covers WP-1020 AK1: a version 0 cache answers
// the version 1 query with Error Report "Unsupported Protocol Version";
// the client reconnects with version 0 and takes the default intervals.
func TestRTRVersionFallbackToV0(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 0, SessionID: 3, Serial: 1, VRPs: []fakertr.VRP{vrpA, vrpC}})
	m := newManager(t)
	r := check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second)
	want(t, r, probewire.StatusOK, "")
	if r.Values[ValueProtocolVersion] != 0 || r.Values[vrps("ipv6")] != 1 {
		t.Fatalf("values = %v", r.Values)
	}
	q := s.Queries()
	if len(q) != 2 || q[0].Version != 1 || q[1].Version != 0 || q[1].Type != fakertr.TypeResetQuery {
		t.Fatalf("queries = %+v", q)
	}
	v := m.validator(addrOf(s))
	v.mu.Lock()
	refresh, retry := v.refresh, v.retry
	v.mu.Unlock()
	if refresh != DefaultRefresh || retry != DefaultRetry || v.snapshot().expire != DefaultExpire {
		t.Fatalf("intervals %v/%v/%v, want the RFC 8210 defaults", refresh, retry, v.snapshot().expire)
	}
	if s.Accepted() != 2 {
		t.Fatalf("connections = %d, want 2", s.Accepted())
	}
}

// TestRTRErrorReportIsUnknown covers WP-1020 AK5: an Error Report of the
// cache is error/unsupported with the code as label (unknown); a closed
// port is fail/refused, a corrupt PDU an Error Report "Corrupt Data" of
// the client and error/unsupported.
func TestRTRErrorReportIsUnknown(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, VRPs: []fakertr.VRP{vrpA}})
	s.FailNext(fakertr.ErrNoDataAvailable)
	m := newManager(t)
	r := check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second)
	want(t, r, probewire.StatusError, probewire.ReasonUnsupported)
	if r.Labels[LabelErrorCode] != "2" || r.Labels[LabelError] != "no_data_available" || r.Values[ValueUp] != 0 {
		t.Fatalf("labels = %v, values = %v", r.Labels, r.Values)
	}
	// The next check opens the session again and succeeds.
	want(t, check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second), probewire.StatusOK, "")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	want(t, check(t, m, rtrSpec("p-closed", port), 5*time.Second), probewire.StatusFail, probewire.ReasonRefused)

	// A validator slower than the check is fail/timeout.
	slow := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, VRPs: []fakertr.VRP{vrpA}})
	slow.SetDelay(2 * time.Second)
	want(t, check(t, newManager(t), rtrSpec("p-slow", slow.Addr().Port), 200*time.Millisecond), probewire.StatusFail, probewire.ReasonTimeout)

	// A dial error that is neither refused nor a timeout is
	// fail/unreachable.
	unreach := newManager(t)
	unreach.Dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EHOSTUNREACH}
	}
	want(t, check(t, unreach, rtrSpec("p-unreach", 323), 5*time.Second), probewire.StatusFail, probewire.ReasonUnreachable)

	// A cache that sends a PDU of 70,000 bytes.
	bad, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close() //nolint:errcheck // test cleanup
	report := make(chan []byte, 1)
	go func() {
		c, err := bad.Accept()
		if err != nil {
			return
		}
		defer c.Close() //nolint:errcheck // test cleanup
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		q := make([]byte, 8)
		_, _ = io.ReadFull(c, q)
		hdr := []byte{1, TypeCacheResponse, 0, 7, 0, 1, 0x11, 0x70}
		_, _ = c.Write(hdr)
		r := bufio.NewReader(c)
		raw, err := readPDU(r)
		if err == nil {
			report <- raw
		}
	}()
	r = check(t, m, rtrSpec("p-bad", bad.Addr().(*net.TCPAddr).Port), 5*time.Second)
	want(t, r, probewire.StatusError, probewire.ReasonUnsupported)
	if r.Labels[LabelErrorCode] != "0" {
		t.Fatalf("labels = %v", r.Labels)
	}
	select {
	case raw := <-report:
		p, err := parsePDU(raw)
		if err != nil || p.typ != TypeErrorReport || p.errorCode != ErrCorruptData {
			t.Fatalf("client report = %+v, %v", p, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Error Report from the client")
	}
}

// TestRTRDataAgeWarn covers WP-1020 AK5: data older than the Expire
// interval is ok with the warn code rpki_stale (degraded).
func TestRTRDataAgeWarn(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, Refresh: 3600, Retry: 600, Expire: 900, VRPs: []fakertr.VRP{vrpA}})
	var mu sync.Mutex
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	m := newManager(t)
	m.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	want(t, check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second), probewire.StatusOK, "")
	mu.Lock()
	now = now.Add(901 * time.Second)
	mu.Unlock()
	r := check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second)
	want(t, r, probewire.StatusOK, probewire.WarnRPKIStale)
	if r.Values[ValueDataAge] != 901 || !probewire.IsWarnCode(r.Code) {
		t.Fatalf("values = %v", r.Values)
	}
}

// TestRTRVRPLimit covers WP-1020 AK3: 2,000,001 VRPs are error/vrp_limit;
// the incomplete state is discarded and the old table stays.
func TestRTRVRPLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("transfers 2,000,001 VRPs")
	}
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, VRPs: []fakertr.VRP{vrpA, vrpC}})
	m := newManager(t)
	want(t, check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second), probewire.StatusOK, "")
	s.Update(fakertr.Delta{GenN: MaxVRPs + 1, Gen: func(i int) fakertr.VRP {
		a := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		return fakertr.VRP{Prefix: netip.PrefixFrom(a, 32), MaxLen: 32, ASN: 64500}
	}})
	v := m.validator(addrOf(s))
	eventually(t, 60*time.Second, "vrp_limit", func() bool { return errors.Is(v.snapshot().err, errVRPLimit) })
	if got := tableSet(m.Table(addrOf(s))); !slices.Equal(got, keys(vrpA, vrpC)) {
		t.Fatalf("table after the limit = %v, want the old one", got)
	}
	r := check(t, m, rtrSpec("p-1", s.Addr().Port), 60*time.Second)
	want(t, r, probewire.StatusError, probewire.ReasonVRPLimit)
	if r.Values[ValueSerial] != 1 || r.Values[vrps("ipv4")] != 1 || r.Values[vrps("ipv6")] != 1 {
		t.Fatalf("values = %v, want the old state", r.Values)
	}
}

// TestRTRSharedSessionPerValidator covers WP-1020 AK2: two probes of one
// validator share one TCP connection; the session ends after two Refresh
// intervals without a probe.
func TestRTRSharedSessionPerValidator(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, Refresh: 1, Retry: 1, Expire: 600, VRPs: []fakertr.VRP{vrpA}})
	m := newManager(t)
	var wg sync.WaitGroup
	results := make([]probewire.Result, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			results[i] = (&RTRExecutor{Manager: m}).Execute(ctx, rtrSpec("p-"+strconv.Itoa(i), s.Addr().Port), nil)
		}(i)
	}
	wg.Wait()
	for _, r := range results {
		want(t, r, probewire.StatusOK, "")
	}
	if s.Accepted() != 1 || m.Sessions() != 1 {
		t.Fatalf("connections = %d, sessions = %d, want 1 and 1", s.Accepted(), m.Sessions())
	}
	eventually(t, 10*time.Second, "idle teardown", func() bool { return m.Sessions() == 0 && s.Open() == 0 })
}

// TestRTRVRPTableMemory covers WP-1020 AK3: 2,000,000 VRPs (half IPv4,
// half IPv6) take at most 128 MiB.
func TestRTRVRPTableMemory(t *testing.T) {
	const n = MaxVRPs / 2
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	d := &delta{reset: true, ann4: make([]vrp4, 0, n), ann6: make([]vrp6, 0, n)}
	for i := range n {
		d.ann4 = append(d.ann4, vrp4{addr: 0x0A000000 + uint32(i), asn: 64500, plen: 32, maxLen: 32})       //nolint:gosec // i < 2^24
		d.ann6 = append(d.ann6, vrp6{hi: 0x20010db800000000 | uint64(i), asn: 64500, plen: 64, maxLen: 64}) //nolint:gosec // i ≥ 0
	}
	tb := d.apply(nil)
	d = nil
	runtime.GC()
	runtime.ReadMemStats(&after)
	v4, v6 := tb.Len()
	if v4 != n || v6 != n {
		t.Fatalf("table = %d + %d", v4, v6)
	}
	used := int64(after.HeapAlloc) - int64(before.HeapAlloc) //nolint:gosec // heap sizes fit
	if used > 128<<20 {
		t.Fatalf("table of 2,000,000 VRPs uses %d MiB, want at most 128", used>>20)
	}
	runtime.KeepAlive(tb)
	runtime.KeepAlive(d)
}

// TestRTRLimits covers WP-1020 AK6: the contract entry with the limits of
// E-154 and the rejection of a port and interval outside them.
func TestRTRLimits(t *testing.T) {
	typ, ok := probewire.Lookup(TypeRTR)
	if !ok || !typ.Available() || typ.DefaultIntervalS != 300 || typ.MinIntervalS != 60 || typ.DefaultTimeoutS != 30 ||
		typ.MaxTimeoutS != 120 || typ.MaxParallelPerTarget != 1 || typ.MaxRatePerCollector != 1 || typ.MaxRows != MaxVRPs {
		t.Fatalf("contract entry = %+v", typ)
	}
	for _, tc := range []struct {
		name  string
		mod   func(*probewire.ProbeSpec)
		field string
	}{
		{"port 70000", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"port":70000}`) }, "params.port"},
		{"interval 59 s", func(s *probewire.ProbeSpec) { s.IntervalMS = 59_000 }, "interval_s"},
		{"timeout 121 s", func(s *probewire.ProbeSpec) { s.TimeoutMS = 121_000 }, "timeout_s"},
		{"unknown parameter", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"stale_warn_s":60}`) }, "params.stale_warn_s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := rtrSpec("p-limits", 323)
			tc.mod(&s)
			found := false
			for _, e := range probewire.Validate(&s) {
				found = found || e.Field == tc.field
			}
			if !found {
				t.Fatalf("no field error %s", tc.field)
			}
			want(t, probe.Run(context.Background(), &s, nil), probewire.StatusError, probewire.ReasonConfig)
		})
	}
	if _, ok := probe.Lookup(TypeRTR); !ok {
		t.Fatal("rpki_rtr executor not registered")
	}
	if got := validatorAddr("2001:db8::1", 0); got != "[2001:db8::1]:323" {
		t.Fatalf("validatorAddr = %q", got)
	}
}

// FuzzPDU covers WP-1020 AK1: the parser does not panic on any bytes.
func FuzzPDU(f *testing.F) {
	f.Add(resetQuery(1))
	f.Add(serialQuery(1, 7, 10))
	f.Add(errorReport(1, ErrCorruptData, resetQuery(1)))
	eod := header(1, TypeEndOfData, 7, 24)
	f.Add(eod)
	pfx := header(1, TypeIPv4Prefix, 0, 20)
	pfx[8], pfx[9], pfx[10] = 1, 24, 24
	f.Add(pfx)
	f.Add([]byte{1, TypeErrorReport, 0, 0, 0, 0, 0, 16, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parsePDU(data)
		r := bufio.NewReader(bytes.NewReader(data))
		for {
			raw, err := readPDU(r)
			if err != nil {
				return
			}
			_, _ = parsePDU(raw)
			if len(raw) > MaxPDU {
				t.Fatalf("PDU of %d bytes accepted", len(raw))
			}
		}
	})
}

// eod is an End of Data of version 1 with the timers.
func eod(serial, refresh, retry, expire uint32) *pdu {
	return &pdu{version: 1, typ: TypeEndOfData, session: 7, serial: serial, hasTimers: true, refresh: refresh, retry: retry, expire: expire}
}

func prefix4(i uint32, announce bool) *pdu {
	p := &pdu{version: 1, typ: TypeIPv4Prefix, plen: 32, maxLen: 32, asn: 64500,
		prefix: netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})}
	if announce {
		p.flags = 1
	}
	return p
}

// TestRTRVRPLimitWithdrawUnknown covers WP-1020 AK3: a delta on a table
// at the limit with one announcement and one withdrawal of an unknown
// record would end above 2,000,000 VRPs; End of Data refuses it and the
// old table stays.
func TestRTRVRPLimitWithdrawUnknown(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a table of 2,000,000 VRPs")
	}
	v := newValidator(&Manager{}, "192.0.2.1:323")
	base := &Table{v4: make([]vrp4, MaxVRPs)}
	for i := range base.v4 {
		base.v4[i] = vrp4{addr: 0x0A000000 + uint32(i), asn: 64500, plen: 32, maxLen: 32} //nolint:gosec // i < 2^24
	}
	v.st.table, v.haveSess = base, true
	pending := &delta{baseLen: MaxVRPs}
	var err error
	// The withdrawal of the unknown record first: projected() stays at
	// the limit.
	for _, p := range []*pdu{{version: 1, typ: TypeIPv4Prefix, plen: 32, maxLen: 32, asn: 1, prefix: netip.MustParseAddr("192.0.2.77")}, prefix4(0xFFFFF0, true)} {
		if pending, err = v.handle(nil, 1, p, pending); err != nil {
			t.Fatalf("prefix: %v", err)
		}
	}
	if _, err := v.handle(nil, 1, eod(2, 3600, 600, 7200), pending); !errors.Is(err, errVRPLimit) {
		t.Fatalf("End of Data above the limit: err = %v, want errVRPLimit", err)
	}
	if v.snapshot().table != base {
		t.Fatal("the table above the limit replaced the old one")
	}
}

// TestRTRVRPLimitFlood covers WP-1020 AK3: announcements and withdrawals
// of invented records without net growth do not grow the buffer of a
// delta beyond the limit.
func TestRTRVRPLimitFlood(t *testing.T) {
	v := newValidator(&Manager{}, "192.0.2.1:323")
	pending := &delta{baseLen: 10}
	var err error
	for i := range uint32(MaxVRPs + 100) {
		if pending, err = v.handle(nil, 1, prefix4(i%(1<<24), i%2 == 0), pending); err != nil {
			break
		}
	}
	if !errVRPLimitIs(err) {
		t.Fatalf("flood: err = %v, want errVRPLimit", err)
	}
}

func errVRPLimitIs(err error) bool { return errors.Is(err, errVRPLimit) }

// TestRTRTimersClamped covers WP-1020 AK2 and RFC 8210 section 6: Refresh,
// Retry and Expire of 0 (or too large) are clamped into the ranges of the
// RFC, so that the session neither spins nor is always stale.
func TestRTRTimersClamped(t *testing.T) {
	v := newValidator(&Manager{}, "192.0.2.1:323")
	if _, err := v.handle(nil, 1, eod(1, 0, 0, 0), &delta{reset: true}); err != nil {
		t.Fatal(err)
	}
	if v.refresh != time.Second || v.retry != time.Second || v.snapshot().expire != 600*time.Second {
		t.Fatalf("refresh %v, retry %v, expire %v; want 1s, 1s, 600s", v.refresh, v.retry, v.snapshot().expire)
	}
	if _, err := v.handle(nil, 1, eod(2, 1<<31, 1<<31, 1<<31), &delta{}); err != nil {
		t.Fatal(err)
	}
	if v.refresh != 86400*time.Second || v.retry != 7200*time.Second || v.snapshot().expire != 172800*time.Second {
		t.Fatalf("refresh %v, retry %v, expire %v; want the maxima", v.refresh, v.retry, v.snapshot().expire)
	}
}

// TestRTRUnreachableValidatorEndsWhenIdle covers WP-1020 AK2: a validator
// that cannot be reached is not dialled forever; without a probe for two
// Refresh intervals its session object ends in the retry loop.
func TestRTRUnreachableValidatorEndsWhenIdle(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC).UnixNano())
	var dials atomic.Int64
	m := &Manager{
		Now: func() time.Time { return time.Unix(0, now.Load()) },
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		},
	}
	t.Cleanup(m.Close)
	// A short Retry interval (a session never reached keeps the default
	// of 600 s) so that the retry loop runs within the test.
	v := m.validator(validatorAddr("127.0.0.1", 323))
	v.mu.Lock()
	v.retry = 0 // the loop waits at least minRetryWait
	v.mu.Unlock()
	want(t, check(t, m, rtrSpec("p-gone", 323), 5*time.Second), probewire.StatusFail, probewire.ReasonRefused)
	if m.Sessions() != 1 {
		t.Fatalf("sessions = %d, want 1", m.Sessions())
	}
	// The probe is deleted: no check for more than two Refresh intervals.
	now.Add(int64(2*DefaultRefresh + time.Second))
	eventually(t, 5*time.Second, "idle end in the retry loop", func() bool { return m.Sessions() == 0 })
	n := dials.Load()
	time.Sleep(1500 * time.Millisecond)
	if dials.Load() != n {
		t.Fatal("the ended validator is still dialled")
	}
}

// TestRTRAwaitTakesNewValidatorAfterIdleEnd covers WP-1020 AK2: a probe
// that holds a validator ended as idle takes a new one instead of waiting
// for its context.
func TestRTRAwaitTakesNewValidatorAfterIdleEnd(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, VRPs: []fakertr.VRP{vrpA}})
	m := newManager(t)
	old := m.validator(addrOf(s))
	old.mu.Lock()
	old.closed = true
	old.mu.Unlock()
	want(t, check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second), probewire.StatusOK, "")
	if m.validator(addrOf(s)) == old {
		t.Fatal("await kept the ended validator")
	}
}

// TestRTRVRPTablePeakMemory covers WP-1020 AK3 on the real path: a delta
// of 2,000,000 VRPs (half IPv4, half IPv6) loaded through fakertr, the
// Manager and the session, while the old table lives. The live heap,
// sampled after every collection during the build, stays within 128 MiB.
func TestRTRVRPTablePeakMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("transfers 2,000,000 VRPs")
	}
	// Frequent collections make the samples of the live heap dense.
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, VRPs: []fakertr.VRP{vrpA, vrpC}})
	m := newManager(t)
	want(t, check(t, m, rtrSpec("p-1", s.Addr().Port), 5*time.Second), probewire.StatusOK, "")
	runtime.GC()
	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(sample)
	before := sample[0].Value.Uint64()
	var peak atomic.Uint64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		local := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
			metrics.Read(local)
			if v := local[0].Value.Uint64(); v > peak.Load() {
				peak.Store(v)
			}
		}
	}()
	const n = MaxVRPs - 2
	s.Update(fakertr.Delta{GenN: n, Gen: func(i int) fakertr.VRP {
		if i%2 == 0 {
			a := netip.AddrFrom4([4]byte{10, byte(i >> 17), byte(i >> 9), byte(i >> 1)})
			return fakertr.VRP{Prefix: netip.PrefixFrom(a, 32), MaxLen: 32, ASN: 64500}
		}
		var b [16]byte
		b[0], b[1], b[2], b[3] = 0x20, 0x01, 0x0d, 0xb8
		b[5], b[6], b[7] = byte(i>>17), byte(i>>9), byte(i>>1)
		return fakertr.VRP{Prefix: netip.PrefixFrom(netip.AddrFrom16(b), 64), MaxLen: 64, ASN: 64500}
	}})
	eventually(t, 120*time.Second, "the delta of 2,000,000 VRPs", func() bool {
		a, b := m.Table(addrOf(s)).Len()
		return a+b == MaxVRPs
	})
	close(stop)
	<-sampled
	if used := int64(peak.Load()) - int64(before); used > 128<<20 { //nolint:gosec // heap sizes fit
		t.Fatalf("peak live heap while loading 2,000,000 VRPs: %d MiB above the start, want at most 128", used>>20)
	} else {
		t.Logf("peak live heap while loading: %d MiB above the start", used>>20)
	}
}
