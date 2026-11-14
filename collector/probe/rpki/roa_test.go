package rpki

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakertr"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// tableOf builds a VRP table directly (as an End of Data would).
func tableOf(vs ...fakertr.VRP) *Table {
	d := &delta{reset: true}
	for _, v := range vs {
		p := &pdu{flags: 1, prefix: v.Prefix.Addr(), plen: uint8(v.Prefix.Bits()), maxLen: v.MaxLen, asn: v.ASN} //nolint:gosec // prefix lengths
		d.add(p)
	}
	return d.apply(nil)
}

func vrp(prefix string, maxLen uint8, asn uint32) fakertr.VRP {
	return fakertr.VRP{Prefix: netip.MustParsePrefix(prefix), MaxLen: maxLen, ASN: asn}
}

func roaSpec(port int, routes []map[string]any, extra map[string]any) probewire.ProbeSpec {
	p := map[string]any{"validator": map[string]any{"host": "127.0.0.1", "port": port}, "routes": routes}
	for k, v := range extra {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	return probewire.ProbeSpec{ID: "roa-1", Type: TypeROA, Target: "192.0.2.250", IntervalMS: 300_000, TimeoutMS: 5_000,
		DownCount: 3, UpCount: 1, Epoch: 1, Params: raw}
}

func checkROA(t *testing.T, m *Manager, s probewire.ProbeSpec, timeout time.Duration) probewire.Result { //nolint:gocritic // a copy per check
	t.Helper()
	if errs := probewire.Validate(&s); len(errs) > 0 {
		t.Fatalf("spec rejected: %v", errs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r := (&ROAExecutor{Manager: m}).Execute(ctx, s, nil)
	r.ProbeID = s.ID
	if err := r.Validate(); err != nil {
		t.Fatalf("result invalid: %v (%+v)", err, r)
	}
	for k := range r.Values {
		name, _, err := probewire.ParseValueKey(k)
		if err != nil || !probewire.DeclaresMetric(TypeROA, name) {
			t.Fatalf("value %s not declared", k)
		}
	}
	return r
}

func route(prefix string, as uint32) map[string]any {
	return map[string]any{"prefix": prefix, "origin_as": as}
}

// TestROARFC6811Cases covers AK2: the cases of RFC 6811 section 2.
func TestROARFC6811Cases(t *testing.T) {
	cases := []struct {
		name   string
		vrps   []fakertr.VRP
		prefix string
		as     uint32
		want   ROAState
	}{
		{"exact prefix", []fakertr.VRP{vrp("192.0.2.0/24", 24, 64500)}, "192.0.2.0/24", 64500, ROAValid},
		{"longer within maxLength", []fakertr.VRP{vrp("198.51.100.0/22", 24, 64501)}, "198.51.101.0/24", 64501, ROAValid},
		{"longer beyond maxLength", []fakertr.VRP{vrp("198.51.100.0/22", 23, 64501)}, "198.51.101.0/24", 64501, ROAInvalid},
		{"wrong AS", []fakertr.VRP{vrp("192.0.2.0/24", 24, 64500)}, "192.0.2.0/24", 64999, ROAInvalid},
		{"AS0 VRP", []fakertr.VRP{vrp("192.0.2.0/24", 24, 0)}, "192.0.2.0/24", 64500, ROAInvalid},
		{"two VRPs one match", []fakertr.VRP{vrp("192.0.2.0/24", 24, 64500), vrp("192.0.0.0/16", 24, 64510)}, "192.0.2.0/24", 64510, ROAValid},
		{"shorter route not covered", []fakertr.VRP{vrp("192.0.2.0/24", 24, 64500)}, "192.0.0.0/16", 64500, ROANotFound},
		{"no VRP", []fakertr.VRP{vrp("203.0.113.0/24", 24, 64503)}, "192.0.2.0/24", 64500, ROANotFound},
		{"IPv6 valid", []fakertr.VRP{vrp("2001:db8::/32", 48, 64502)}, "2001:db8:1::/48", 64502, ROAValid},
		{"IPv6 beyond maxLength", []fakertr.VRP{vrp("2001:db8::/32", 48, 64502)}, "2001:db8:1:1::/64", 64502, ROAInvalid},
		{"IPv6 notfound", []fakertr.VRP{vrp("2001:db8::/32", 48, 64502)}, "2001:db9::/32", 64502, ROANotFound},
		{"IPv6 /128 route", []fakertr.VRP{vrp("2001:db8::/32", 128, 64502)}, "2001:db8::1/128", 64502, ROAValid},
		// An IPv4-mapped IPv6 prefix is IPv6, never IPv4 (no panic).
		{"4in6 route", []fakertr.VRP{vrp("192.0.2.0/24", 24, 64500)}, "::ffff:192.0.2.0/120", 64500, ROANotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tableOf(c.vrps...).Validate(netip.MustParsePrefix(c.prefix), c.as); got != c.want {
				t.Fatalf("%s AS%d = %s, want %s", c.prefix, c.as, got, c.want)
			}
		})
	}
}

// TestROAInvalidIsDown covers AK3: an invalid route is fail/rpki_invalid
// with the route in the label; the values per route and state.
func TestROAInvalidIsDown(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, VRPs: []fakertr.VRP{vrpA, vrpB}})
	m := newManager(t)
	r := checkROA(t, m, roaSpec(s.Addr().Port, []map[string]any{route("192.0.2.0/24", 64500), route("198.51.100.0/24", 64999)}, nil), 5*time.Second)
	want(t, r, probewire.StatusFail, probewire.ReasonRPKIInvalid)
	if r.Labels[LabelInvalid] != "198.51.100.0/24 AS64999" {
		t.Fatalf("labels = %v", r.Labels)
	}
	if r.Values[probewire.ValueKey(ValueROAState, map[string]string{LabelPrefix: "198.51.100.0/24", LabelOriginAS: "64999"})] != 2 ||
		r.Values[probewire.ValueKey(ValueROAState, map[string]string{LabelPrefix: "192.0.2.0/24", LabelOriginAS: "64500"})] != 0 {
		t.Fatalf("values = %v", r.Values)
	}
	for state, n := range map[string]float64{"valid": 1, "invalid": 1, "notfound": 0} {
		if got := r.Values[probewire.ValueKey(ValueROARoutes, map[string]string{LabelState: state})]; got != n {
			t.Fatalf("routes %s = %v, want %v", state, got, n)
		}
	}
	// All valid: ok without code.
	want(t, checkROA(t, m, roaSpec(s.Addr().Port, []map[string]any{route("192.0.2.0/24", 64500)}, nil), 5*time.Second), probewire.StatusOK, "")
}

// TestROANotFoundWarnSwitchable covers AK1 and AK3: notfound is ok with
// rpki_notfound only with notfound_degraded (default true).
func TestROANotFoundWarnSwitchable(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, VRPs: []fakertr.VRP{vrpA}})
	m := newManager(t)
	routes := []map[string]any{route("192.0.2.0/24", 64500), route("203.0.113.0/24", 64503)}
	r := checkROA(t, m, roaSpec(s.Addr().Port, routes, nil), 5*time.Second)
	want(t, r, probewire.StatusOK, probewire.WarnRPKINotFound)
	if !probewire.IsWarnCode(r.Code) || r.Labels[LabelNotFound] != "203.0.113.0/24 AS64503" {
		t.Fatalf("result = %+v", r)
	}
	r = checkROA(t, m, roaSpec(s.Addr().Port, routes, map[string]any{"notfound_degraded": false}), 5*time.Second)
	want(t, r, probewire.StatusOK, "")
	if r.Labels[LabelNotFound] == "" {
		t.Fatal("the notfound route is not named")
	}
}

// TestROAStaleIsUnknown covers AK3: data older than Expire and no End of
// Data within the timeout are error/rpki_stale (unknown).
func TestROAStaleIsUnknown(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, Expire: 900, VRPs: []fakertr.VRP{vrpA}})
	var mu sync.Mutex
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	m := newManager(t)
	m.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	spec := roaSpec(s.Addr().Port, []map[string]any{route("192.0.2.0/24", 64500)}, nil)
	want(t, checkROA(t, m, spec, 5*time.Second), probewire.StatusOK, "")
	mu.Lock()
	now = now.Add(901 * time.Second)
	mu.Unlock()
	want(t, checkROA(t, m, spec, 5*time.Second), probewire.StatusError, probewire.WarnRPKIStale)

	// A cache that never sends End of Data within the timeout.
	slow := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, VRPs: []fakertr.VRP{vrpA}})
	slow.SetDelay(3 * time.Second)
	m2 := newManager(t)
	spec2 := roaSpec(slow.Addr().Port, []map[string]any{route("192.0.2.0/24", 64500)}, nil)
	spec2.TimeoutMS = 1_000
	want(t, checkROA(t, m2, spec2, time.Second), probewire.StatusError, probewire.WarnRPKIStale)

	// The same through probe.Run with the deadline of the spec: the wait
	// ends before it, so the check is unknown and not fail/timeout.
	slow2 := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, VRPs: []fakertr.VRP{vrpA}})
	slow2.SetDelay(3 * time.Second)
	spec3 := roaSpec(slow2.Addr().Port, []map[string]any{route("192.0.2.0/24", 64500)}, nil)
	spec3.TimeoutMS = 1_000
	t.Cleanup(defaultManager.Close)
	if r := probe.Run(context.Background(), &spec3, nil); r.Status != probewire.StatusError || r.Code != probewire.WarnRPKIStale {
		t.Fatalf("through probe.Run: %s/%s, want error/%s", r.Status, r.Code, probewire.WarnRPKIStale)
	}
}

// TestROASharesValidatorSession covers AK1: an rpki_rtr and an rpki_roa
// probe of the same validator share one session; without an rtr probe
// the roa check opens it.
func TestROASharesValidatorSession(t *testing.T) {
	s := startCache(t, fakertr.Config{Version: 1, SessionID: 7, Serial: 1, VRPs: []fakertr.VRP{vrpA}})
	m := newManager(t)
	want(t, checkROA(t, m, roaSpec(s.Addr().Port, []map[string]any{route("192.0.2.0/24", 64500)}, nil), 5*time.Second), probewire.StatusOK, "")
	want(t, check(t, m, rtrSpec("rtr-1", s.Addr().Port), 5*time.Second), probewire.StatusOK, "")
	if s.Accepted() != 1 || m.Sessions() != 1 {
		t.Fatalf("connections = %d, sessions = %d, want 1 and 1", s.Accepted(), m.Sessions())
	}
	// The validator from the target when validator.host is absent.
	spec := roaSpec(s.Addr().Port, []map[string]any{route("192.0.2.0/24", 64500)}, nil)
	spec.Target = "127.0.0.1"
	spec.Params, _ = json.Marshal(map[string]any{"validator": map[string]any{"port": s.Addr().Port}, "routes": []map[string]any{route("192.0.2.0/24", 64500)}})
	want(t, checkROA(t, m, spec, 5*time.Second), probewire.StatusOK, "")
	if s.Accepted() != 1 {
		t.Fatalf("connections = %d, want 1", s.Accepted())
	}
}

// TestROAPerformance2MVRPs covers AK4: 1,000 routes against 2,000,000
// VRPs in at most 50 ms (search over the prefix structure).
func TestROAPerformance2MVRPs(t *testing.T) {
	const n = MaxVRPs / 2
	d := &delta{reset: true, ann4: make([]vrp4, 0, n), ann6: make([]vrp6, 0, n)}
	for i := range n {
		d.ann4 = append(d.ann4, vrp4{addr: 0x0A000000 + uint32(i)<<8, asn: 64500 + uint32(i%100), plen: 24, maxLen: 24}) //nolint:gosec // i < 2^24
		d.ann6 = append(d.ann6, vrp6{hi: 0x20010db800000000 | uint64(i)<<16, asn: 64500, plen: 48, maxLen: 48})          //nolint:gosec // i ≥ 0
	}
	tb := d.apply(nil)
	cfg := make([]roaRoute, 1000)
	routes := make([]netip.Prefix, 1000)
	for i := range routes {
		if i%2 == 0 {
			a := netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 0})
			routes[i] = netip.PrefixFrom(a, 24)
		} else {
			routes[i] = netip.MustParsePrefix(fmt.Sprintf("2001:db8:0:%x::/64", i))
		}
		cfg[i] = roaRoute{Prefix: routes[i].String(), OriginAS: 64500}
	}
	best := time.Hour
	var r probewire.Result
	for range 3 {
		start := time.Now()
		r = roaResult(tb, cfg, routes, true)
		best = min(best, time.Since(start))
	}
	if best > 50*time.Millisecond {
		t.Fatalf("1,000 routes against 2,000,000 VRPs took %v, want at most 50 ms", best)
	}
	total := 0.0
	for _, s := range []string{"valid", "invalid", "notfound"} {
		total += r.Values[probewire.ValueKey(ValueROARoutes, map[string]string{LabelState: s})]
	}
	if total != 1000 {
		t.Fatalf("routes counted %v", total)
	}
	t.Logf("1,000 routes against %d VRPs: %v", MaxVRPs, best)
}

// TestROALimits covers AK1, AK5 and PRT-21 (limits test): 1,001 routes,
// an invalid prefix and AS 0 are field errors; the register entry.
func TestROALimits(t *testing.T) {
	typ, ok := probewire.Lookup(TypeROA)
	if !ok || !typ.Available() || typ.DefaultIntervalS != 300 || typ.MinIntervalS != 60 || typ.DefaultTimeoutS != 1 ||
		typ.MaxTimeoutS != 5 || typ.MaxRatePerCollector != 100 || typ.MaxRows != 1000 {
		t.Fatalf("register entry = %+v", typ)
	}
	many := make([]map[string]any, 1001)
	for i := range many {
		many[i] = route("10.0."+strconv.Itoa(i/256)+"."+strconv.Itoa(i%256)+"/32", 64500)
	}
	for name, c := range map[string]struct {
		routes []map[string]any
		field  string
	}{
		"1001 routes":   {many, "params.routes"},
		"bad prefix":    {[]map[string]any{route("192.0.2.300/24", 64500)}, "params.routes[0].prefix"},
		"host bits":     {[]map[string]any{route("192.0.2.1/24", 64500)}, "params.routes[0].prefix"},
		"4in6 prefix":   {[]map[string]any{route("::ffff:192.0.2.0/120", 64500)}, "params.routes[0].prefix"},
		"AS 0":          {[]map[string]any{route("192.0.2.0/24", 0)}, "params.routes[0].origin_as"},
		"AS above 2^32": {[]map[string]any{route("192.0.2.0/24", 0)}, "params.routes[0].origin_as"},
	} {
		spec := roaSpec(323, c.routes, nil)
		if name == "AS above 2^32" {
			spec.Params = json.RawMessage(`{"routes":[{"prefix":"192.0.2.0/24","origin_as":4294967296}]}`)
		}
		errs := probewire.Validate(&spec)
		if !slices.ContainsFunc(errs, func(e probewire.FieldError) bool { return e.Field == c.field }) {
			t.Errorf("%s: errs = %v, want %s", name, errs, c.field)
		}
	}
	spec := roaSpec(323, []map[string]any{route("192.0.2.0/24", 64500)}, nil)
	spec.TimeoutMS = 6_000
	if errs := probewire.Validate(&spec); !slices.ContainsFunc(errs, func(e probewire.FieldError) bool { return e.Field == "timeout_s" }) {
		t.Errorf("timeout 6 s: %v", errs)
	}
}
