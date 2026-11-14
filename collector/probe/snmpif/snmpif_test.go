package snmpif

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

const community = "if-ro"

var creds = map[string]string{"community": community}

func startAgent(t *testing.T, pdus []gosnmp.SnmpPDU) *fakesnmp.Agent {
	t.Helper()
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: community, SysName: "sw-1",
		SysDescr: "Reticora switch fake", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, pdus...)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Serve() }()
	t.Cleanup(func() {
		_ = agent.Close()
		<-done
	})
	return agent
}

func switch48(t *testing.T) *fakesnmp.Agent {
	t.Helper()
	pdus, err := fakesnmp.LoadWalkFile(filepath.Join("testdata", "switch48.walk"))
	if err != nil {
		t.Fatal(err)
	}
	return startAgent(t, pdus)
}

// clock replaces the executor clock for the test and returns a function
// that moves it on.
func clock(t *testing.T) func(time.Duration) {
	t.Helper()
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	now = func() time.Time { return at }
	t.Cleanup(func() { now = time.Now })
	return func(d time.Duration) { at = at.Add(d) }
}

func spec(agent *fakesnmp.Agent, params string) probewire.ProbeSpec {
	s := probewire.ProbeSpec{ID: "p-if", Type: Type, Target: "127.0.0.1:" + strconv.Itoa(agent.Addr().Port),
		IntervalMS: 60_000, TimeoutMS: 10_000, DownCount: 3, UpCount: 1, Epoch: 1}
	if params != "" {
		s.Params = json.RawMessage(params)
	}
	return s
}

func run(t *testing.T, store *rate.Store, s probewire.ProbeSpec) probewire.Result { //nolint:gocritic // a copy per check
	t.Helper()
	r := probe.Run(rate.WithStore(context.Background(), store), &s, creds)
	if err := r.Validate(); err != nil {
		t.Fatalf("result invalid: %v (%+v)", err, r)
	}
	return r
}

func want(t *testing.T, r probewire.Result, status probewire.Status, code string) { //nolint:gocritic // results are small
	t.Helper()
	if r.Status != status || r.Code != code {
		t.Fatalf("result = %s/%s (labels %v), want %s/%s", r.Status, r.Code, r.Labels, status, code)
	}
}

func key(name string, index int, ifName string) string {
	return probewire.ValueKey(name, map[string]string{LabelIfIndex: strconv.Itoa(index), LabelIfName: ifName})
}

func newStore() *rate.Store { return rate.NewStore(MaxInterfaces*8+16, time.Minute) }

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }

// TestSNMPIfSelection covers AK1 and AK2: name and alias regex, ifIndex
// list and only_admin_up select the interfaces; a non-selected interface
// yields no value and no state.
func TestSNMPIfSelection(t *testing.T) {
	clock(t)
	agent := switch48(t)
	for _, tc := range []struct {
		params string
		n      float64
		has    []int
		code   string
	}{
		{`{"name_regex":"^Gi1/0/[1-3]$"}`, 3, []int{1, 2, 3}, ""},
		{`{"alias_regex":"^server-"}`, 4, []int{1, 4}, ""},
		{`{"if_index":[49,50]}`, 2, []int{49, 50}, ""},
		{`{"if_index":[46,49]}`, 1, []int{49}, ""}, // 46 is admin down
		{``, 46, []int{1, 7, 44, 49, 50}, probewire.WarnCheckFailed},
		{`{"only_admin_up":false}`, 50, []int{45, 48}, probewire.WarnCheckFailed},
		{`{"name_regex":"^Gi","if_index":[1,49]}`, 1, []int{1}, ""},
	} {
		r := run(t, newStore(), spec(agent, tc.params))
		if r.Status != probewire.StatusOK || r.Code != tc.code || r.Values[ValueInterfaces] != tc.n {
			t.Errorf("%s: %s/%s, %v interfaces; want ok/%s, %v", tc.params, r.Status, r.Code, r.Values[ValueInterfaces], tc.code, tc.n)
			continue
		}
		for _, i := range tc.has {
			name := fmt.Sprintf("Gi1/0/%d", i)
			switch i {
			case 49:
				name = "Te1/1/1"
			case 50:
				name = "mgmt0"
			}
			if _, ok := r.Values[key(ValueOperStatus, i, name)]; !ok {
				t.Errorf("%s: no oper status of %s", tc.params, name)
			}
		}
	}
	r := run(t, newStore(), spec(agent, `{"name_regex":"^Gi1/0/1$"}`))
	if len(r.Values) != 3 || r.Values[key(ValueAdminStatus, 1, "Gi1/0/1")] != 1 {
		t.Fatalf("first run of one interface: values %v, want oper, admin and count only (no rate yet)", r.Values)
	}
	if _, ok := r.Values[ValueTruncated]; ok {
		t.Fatal("truncated without truncation")
	}
}

// TestSNMPIfRatesOverTwoRuns covers AK3: rates in bit/s, utilization,
// errors and discards per second over two runs with Agent.Advance, for a
// 64-bit (ifXTable) and a 32-bit (ifTable only) interface.
func TestSNMPIfRatesOverTwoRuns(t *testing.T) {
	step := clock(t)
	agent := switch48(t)
	store := newStore()
	s := spec(agent, `{"if_index":[1,49,50]}`)
	want(t, run(t, store, s), probewire.StatusOK, "")
	agent.Advance(7_500_000)
	step(time.Minute)
	r := run(t, store, s)
	want(t, r, probewire.StatusOK, "")
	for k, v := range map[string]float64{
		key(ValueInBps, 1, "Gi1/0/1"):        1e6,
		key(ValueOutBps, 1, "Gi1/0/1"):       1e6,
		key(ValueUtil, 1, "Gi1/0/1"):         0.001,
		key(ValueUtil, 49, "Te1/1/1"):        0.0001,
		key(ValueInBps, 50, "mgmt0"):         1e6,
		key(ValueUtil, 50, "mgmt0"):          0.01,
		key(ValueErrors, 1, "Gi1/0/1"):       250_000,
		key(ValueDiscards, 49, "Te1/1/1"):    250_000,
		key(ValueOperStatus, 50, "mgmt0"):    1,
		key(ValueAdminStatus, 49, "Te1/1/1"): 1,
	} {
		if got, ok := r.Values[k]; !ok || !near(got, v) {
			t.Errorf("%s = %v (%v), want %v", k, got, ok, v)
		}
	}
	// A changed ifCounterDiscontinuityTime yields no rate.
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.31.1.1.1.19.1", Type: gosnmp.TimeTicks, Value: uint32(500)})
	agent.Advance(7_500_000)
	step(time.Minute)
	r = run(t, store, s)
	if _, ok := r.Values[key(ValueInBps, 1, "Gi1/0/1")]; ok {
		t.Error("rate across a counter discontinuity")
	}
	if _, ok := r.Values[key(ValueInBps, 49, "Te1/1/1")]; !ok {
		t.Error("discontinuity of one interface dropped the rate of another")
	}
	// A restart of the agent (sysUpTime back) yields no rate either.
	agent.Set(gosnmp.SnmpPDU{Name: fakesnmp.OIDSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(100)})
	agent.Advance(7_500_000)
	step(time.Minute)
	r = run(t, store, s)
	if _, ok := r.Values[key(ValueInBps, 49, "Te1/1/1")]; ok {
		t.Error("rate across a reboot")
	}
}

// TestSNMPIfCounterWrap32 covers AK3: a 32-bit counter (ifInOctets of an
// interface without ifXTable counters) wraps between two runs.
func TestSNMPIfCounterWrap32(t *testing.T) {
	step := clock(t)
	agent := switch48(t)
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.10.50", Type: gosnmp.Counter32, Value: uint32(math.MaxUint32 - 999)})
	store := newStore()
	s := spec(agent, `{"if_index":[50]}`)
	run(t, store, s)
	agent.Advance(6000)
	step(time.Minute)
	r := run(t, store, s)
	in := agent.Variables()
	for _, v := range in {
		if v.Name == ".1.3.6.1.2.1.2.2.1.10.50" && v.Value.(uint32) != 5000 {
			t.Fatalf("counter did not wrap: %v", v.Value)
		}
	}
	if got := r.Values[key(ValueInBps, 50, "mgmt0")]; !near(got, 6000*8.0/60) {
		t.Fatalf("in_bps over the wrap = %v, want %v", got, 6000*8.0/60)
	}
}

// TestSNMPIfCriticalDown covers AK4: a critical interface admin up and
// oper down is fail/check_failed with its ifName.
func TestSNMPIfCriticalDown(t *testing.T) {
	clock(t)
	agent := switch48(t)
	r := run(t, newStore(), spec(agent, `{"name_regex":"^Te","critical":["Gi1/0/7"]}`))
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if r.Labels[LabelIfName] != "Gi1/0/7" || r.Values[key(ValueOperStatus, 7, "Gi1/0/7")] != 2 {
		t.Fatalf("labels %v values %v", r.Labels, r.Values)
	}
	// A critical interface that does not exist on a complete walk is
	// missing, which is a failure as well.
	r = run(t, newStore(), spec(agent, `{"name_regex":"^Te","critical":["Gi9/9/9"]}`))
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	// Up again: ok.
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.8.7", Type: gosnmp.Integer, Value: 1})
	want(t, run(t, newStore(), spec(agent, `{"critical":["Gi1/0/7"]}`)), probewire.StatusOK, "")
}

// TestSNMPIfNonCriticalDownDegraded covers AK4: another selected interface
// admin up and oper down is ok with the warn code check_failed.
func TestSNMPIfNonCriticalDownDegraded(t *testing.T) {
	clock(t)
	agent := switch48(t)
	r := run(t, newStore(), spec(agent, `{"name_regex":"^Gi1/0/[0-9]$"}`))
	want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
	if r.Labels[LabelIfName] != "Gi1/0/7" || !probewire.IsWarnCode(r.Code) {
		t.Fatalf("labels %v", r.Labels)
	}
	// dormant(5) is no failure.
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.8.7", Type: gosnmp.Integer, Value: 5})
	want(t, run(t, newStore(), spec(agent, `{"name_regex":"^Gi1/0/[0-9]$"}`)), probewire.StatusOK, "")
}

// TestSNMPIfAdminDownIgnored covers AK4: admin-down interfaces report
// their status but go into no rating, also when critical.
func TestSNMPIfAdminDownIgnored(t *testing.T) {
	clock(t)
	agent := switch48(t)
	r := run(t, newStore(), spec(agent, `{"only_admin_up":false,"if_index":[45,46],"critical":["Gi1/0/45"]}`))
	want(t, r, probewire.StatusOK, "")
	if r.Values[key(ValueAdminStatus, 45, "Gi1/0/45")] != 2 || r.Values[key(ValueOperStatus, 46, "Gi1/0/46")] != 2 {
		t.Fatalf("values %v", r.Values)
	}
}

// TestSNMPIfThresholdDegraded covers AK4 and E-186: utilization above
// util_warn and errors above errors_warn are ok with threshold_exceeded,
// above util_crit fail with threshold_exceeded; thresholds win over
// params.
func TestSNMPIfThresholdDegraded(t *testing.T) {
	for _, tc := range []struct {
		params, thresholds string
		status             probewire.Status
		code               string
	}{
		{`{"if_index":[1],"util_warn":0.0005}`, "", probewire.StatusOK, probewire.WarnThresholdExceeded},
		{`{"if_index":[1],"util_warn":0.0005,"util_crit":0.0008}`, "", probewire.StatusFail, probewire.WarnThresholdExceeded},
		{`{"if_index":[1],"util_warn":0.5}`, "", probewire.StatusOK, ""},
		{`{"if_index":[1],"util_warn":0.5}`, `{"util_warn":0.0005}`, probewire.StatusOK, probewire.WarnThresholdExceeded},
		{`{"if_index":[1],"errors_warn":1000}`, "", probewire.StatusOK, probewire.WarnThresholdExceeded},
		{`{"if_index":[1],"errors_warn":1000000}`, "", probewire.StatusOK, ""},
	} {
		step := clock(t)
		agent := switch48(t)
		store := newStore()
		s := spec(agent, tc.params)
		if tc.thresholds != "" {
			s.Thresholds = json.RawMessage(tc.thresholds)
		}
		// The first run has no rate and so no threshold.
		want(t, run(t, store, s), probewire.StatusOK, "")
		agent.Advance(7_500_000)
		step(time.Minute)
		r := run(t, store, s)
		if r.Status != tc.status || r.Code != tc.code {
			t.Errorf("%s %s: %s/%s, want %s/%s", tc.params, tc.thresholds, r.Status, r.Code, tc.status, tc.code)
		}
	}
}

// TestSNMPIfTruncated covers AK5: 1025 interfaces with max_interfaces
// 1024 truncate the walk; a critical interface beyond it is ok with
// check_failed, not fail.
func TestSNMPIfTruncated(t *testing.T) {
	clock(t)
	const n = MaxInterfaces + 1
	pdus := []gosnmp.SnmpPDU{{Name: fakesnmp.OIDSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(1000)}}
	for i := 1; i <= n; i++ {
		idx := "." + strconv.Itoa(i)
		pdus = append(pdus,
			gosnmp.SnmpPDU{Name: "." + oidIfOperStatus + idx, Type: gosnmp.Integer, Value: 1},
			gosnmp.SnmpPDU{Name: "." + oidIfAdminStatus + idx, Type: gosnmp.Integer, Value: 1},
			gosnmp.SnmpPDU{Name: "." + oidIfName + idx, Type: gosnmp.OctetString, Value: []byte("if" + strconv.Itoa(i))},
			gosnmp.SnmpPDU{Name: "." + oidIfHighSpeed + idx, Type: gosnmp.Gauge32, Value: uint32(1000)},
			gosnmp.SnmpPDU{Name: "." + oidIfHCInOctets + idx, Type: gosnmp.Counter64, Value: uint64(i)},
			gosnmp.SnmpPDU{Name: "." + oidIfHCOutOctets + idx, Type: gosnmp.Counter64, Value: uint64(i)},
		)
	}
	agent := startAgent(t, pdus)
	r := run(t, newStore(), spec(agent, `{"max_interfaces":1024,"critical":["if1025"]}`))
	want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
	if r.Values[ValueTruncated] != 1 || r.Values[ValueInterfaces] != MaxInterfaces || r.Values[ValueCriticalUnseen] != 1 {
		t.Fatalf("truncated %v interfaces %v unseen %v", r.Values[ValueTruncated], r.Values[ValueInterfaces], r.Values[ValueCriticalUnseen])
	}
	if _, ok := r.Values[key(ValueOperStatus, n, "if1025")]; ok {
		t.Fatal("value from a row beyond the limit")
	}
	// The default limit is 256 rows.
	if r = run(t, newStore(), spec(agent, "")); r.Values[ValueInterfaces] != DefaultMaxInterfaces || r.Values[ValueTruncated] != 1 {
		t.Fatalf("default limit: %v interfaces", r.Values[ValueInterfaces])
	}
}

// TestSNMPIfResultFitsMessage covers AK2 with max_interfaces 1024: 1024
// selected interfaces with 64-character names and every rate would exceed
// one uplink message (probe.MaxUplinkMessageBytes). The values stop at the
// budget (probe.MaxResultValueBytes, probe_if_truncated = 1), the encoded
// result fits, and the state still covers every row read: a critical
// interface down behind the budget is fail.
func TestSNMPIfResultFitsMessage(t *testing.T) {
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	store := rate.NewStore(MaxInterfaces*8+16, time.Minute)
	build := func(step uint64) []*iface {
		out := make([]*iface, 0, MaxInterfaces)
		for i := 1; i <= MaxInterfaces; i++ {
			name := fmt.Sprintf("GigabitEthernet%d/0/%d.%s", i%8, i, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")[:64]
			c := uint64(i) * step * 1_000_000
			f := &iface{index: uint32(i), name: name, oper: 1, admin: 1, speedMbps: 1000, bits: 64,
				in: c, out: c, hasOctets: true, errIn: step, errOut: step, hasErrors: true,
				discIn: step, discOut: step, hasDiscard: true}
			out = append(out, f)
		}
		return out
	}
	last := build(1)[MaxInterfaces-1].name
	p, bad := decode(json.RawMessage(`{"max_interfaces":1024,"critical":["`+last+`"]}`), nil)
	if bad != nil {
		t.Fatalf("decode: %+v", *bad)
	}
	evaluate(store, at, build(1), &p, false)
	ifaces := build(2)
	ifaces[MaxInterfaces-1].oper = 2
	r := evaluate(store, at.Add(time.Minute), ifaces, &p, false)
	r.ProbeID, r.Epoch = "p-if", 1
	data, err := json.Marshal(probewire.ResultBatch{Results: []probewire.Result{r}})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > probe.MaxUplinkMessageBytes {
		t.Fatalf("encoded result %d bytes, above %d", len(data), probe.MaxUplinkMessageBytes)
	}
	if r.Values[ValueTruncated] != 1 || r.Values[ValueInterfaces] != MaxInterfaces {
		t.Fatalf("truncated %v interfaces %v", r.Values[ValueTruncated], r.Values[ValueInterfaces])
	}
	if r.Status != probewire.StatusFail || r.Code != probewire.WarnCheckFailed || r.Labels[LabelIfName] != last {
		t.Fatalf("result %s/%s labels %v", r.Status, r.Code, r.Labels)
	}
	first := map[string]string{LabelIfIndex: "1", LabelIfName: ifaces[0].name}
	if _, ok := r.Values[probewire.ValueKey(ValueInBps, first)]; !ok {
		t.Fatal("no rate of the first interface")
	}
	if _, ok := r.Values[probewire.ValueKey(ValueOperStatus, map[string]string{LabelIfIndex: "1024", LabelIfName: last})]; ok {
		t.Fatal("values of the last interface beyond the budget")
	}
	// A smaller budget cuts earlier: no interface values at all.
	p.valueLimit = 1
	if r = evaluate(store, at.Add(2*time.Minute), build(3), &p, false); len(r.Values) > 4 || r.Values[ValueTruncated] != 1 {
		t.Fatalf("limit 1: %d values", len(r.Values))
	}
}

// TestSNMPIfLimits covers AK6: max_interfaces 1025 and an interval of
// 59 s are field errors; the check is not executed (error/config).
func TestSNMPIfLimits(t *testing.T) {
	typ, ok := probewire.Lookup(Type)
	if !ok || !typ.Available() || typ.MinIntervalS != 60 || typ.MaxTimeoutS != 30 || typ.MaxRows != MaxInterfaces ||
		len(typ.CredentialNamespaces) != 1 || typ.CredentialNamespaces[0] != "snmp" {
		t.Fatalf("contract entry = %+v", typ)
	}
	base := probewire.ProbeSpec{ID: "p", Type: Type, Target: "192.0.2.1", IntervalMS: 300_000, TimeoutMS: 10_000, DownCount: 3, UpCount: 1}
	for name, tc := range map[string]struct {
		mutate func(*probewire.ProbeSpec)
		field  string
	}{
		"max_interfaces 1025": {func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"max_interfaces":1025}`) }, "params.max_interfaces"},
		"interval 59 s":       {func(s *probewire.ProbeSpec) { s.IntervalMS = 59_000 }, "interval_s"},
		"timeout 31 s":        {func(s *probewire.ProbeSpec) { s.TimeoutMS = 31_000 }, "timeout_s"},
		"util_crit 1.1":       {func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"util_crit":1.1}`) }, "params.util_crit"},
		"bad alias regex":     {func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"alias_regex":"["}`) }, "params.alias_regex"},
	} {
		s := base
		tc.mutate(&s)
		errs := probewire.Validate(&s)
		found := false
		for _, e := range errs {
			found = found || e.Field == tc.field
		}
		if !found {
			t.Errorf("%s: errs = %v, want field %s", name, errs, tc.field)
			continue
		}
		if r := probe.Run(context.Background(), &s, creds); r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig {
			t.Errorf("%s: result %s/%s, want error/config", name, r.Status, r.Code)
		}
	}
}
