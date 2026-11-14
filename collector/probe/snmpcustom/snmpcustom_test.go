package snmpcustom

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

const community = "custom-ro"

var creds = map[string]string{"community": community}

// device are the variables of a small device: scalars of several types
// and a two-row table with a gauge and a counter column.
var device = []gosnmp.SnmpPDU{
	{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(100_000)},
	{Name: ".1.3.6.1.4.1.9999.1.0", Type: gosnmp.Integer, Value: 42},
	{Name: ".1.3.6.1.4.1.9999.2.0", Type: gosnmp.Gauge32, Value: uint32(250)},
	{Name: ".1.3.6.1.4.1.9999.3.0", Type: gosnmp.Counter64, Value: uint64(1_000_000)},
	{Name: ".1.3.6.1.4.1.9999.4.0", Type: gosnmp.OpaqueFloat, Value: float32(0.5)},
	{Name: ".1.3.6.1.4.1.9999.10.1.2.1", Type: gosnmp.Gauge32, Value: uint32(215)},
	{Name: ".1.3.6.1.4.1.9999.10.1.2.2", Type: gosnmp.Gauge32, Value: uint32(380)},
	{Name: ".1.3.6.1.4.1.9999.10.1.3.1", Type: gosnmp.Counter32, Value: uint32(10)},
	{Name: ".1.3.6.1.4.1.9999.10.1.3.2", Type: gosnmp.Counter32, Value: uint32(20)},
}

func startAgent(t *testing.T, pdus []gosnmp.SnmpPDU) *fakesnmp.Agent {
	t.Helper()
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: community, SysName: "dev-1",
		SysDescr: "Reticora custom fake", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, pdus...)
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

func walkAgent(t *testing.T, path string) *fakesnmp.Agent {
	t.Helper()
	pdus, err := fakesnmp.LoadWalkFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return startAgent(t, pdus)
}

func clock(t *testing.T) func(time.Duration) {
	t.Helper()
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	now = func() time.Time { return at }
	t.Cleanup(func() { now = time.Now })
	return func(d time.Duration) { at = at.Add(d) }
}

func spec(agent *fakesnmp.Agent, params string) probewire.ProbeSpec {
	return probewire.ProbeSpec{ID: "p-custom", Type: Type, Target: "127.0.0.1:" + strconv.Itoa(agent.Addr().Port),
		IntervalMS: 60_000, TimeoutMS: 5000, DownCount: 3, UpCount: 1, Epoch: 1, Params: json.RawMessage(params)}
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
		t.Fatalf("result = %s/%s (labels %v, values %v), want %s/%s", r.Status, r.Code, r.Labels, r.Values, status, code)
	}
}

func newStore() *rate.Store { return rate.NewStore((&Executor{}).MaxSeries(), time.Minute) }

func truncKey(metric string) string {
	return probewire.ValueKey(ValueTruncated, map[string]string{LabelMetric: metric})
}

func vkey(metric string, labels ...string) string {
	l := map[string]string{LabelMetric: metric}
	for i := 0; i+1 < len(labels); i += 2 {
		l[labels[i]] = labels[i+1]
	}
	return probewire.ValueKey(ValueCustom, l)
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }

func checkValues(t *testing.T, r probewire.Result, want map[string]float64) { //nolint:gocritic // results are small
	t.Helper()
	for k, v := range want {
		if got, ok := r.Values[k]; !ok || !near(got, v) {
			t.Errorf("%s = %v (%v), want %v", k, got, ok, v)
		}
	}
}

// TestSNMPCustomScalarAndColumn covers AK1 and AK2: scalars of several
// types with scale and unit, and a column with one value per row under
// its index label.
func TestSNMPCustomScalarAndColumn(t *testing.T) {
	clock(t)
	agent := startAgent(t, device)
	r := run(t, newStore(), spec(agent, `{"metrics":[
		{"name":"sessions","oid":"1.3.6.1.4.1.9999.1.0"},
		{"name":"temp_celsius","oid":".1.3.6.1.4.1.9999.2.0","scale":0.1,"unit":"C"},
		{"name":"ratio","oid":"1.3.6.1.4.1.9999.4.0"},
		{"name":"voltage","column":"1.3.6.1.4.1.9999.10.1.2","index_label":"rail","scale":0.01,"unit":"V"},
		{"name":"rows","column":"1.3.6.1.4.1.9999.10.1.2"}]}`))
	want(t, r, probewire.StatusOK, "")
	checkValues(t, r, map[string]float64{
		vkey("sessions"):                             42,
		vkey("temp_celsius", LabelUnit, "C"):         25,
		vkey("ratio"):                                0.5,
		vkey("voltage", LabelUnit, "V", "rail", "1"): 2.15,
		vkey("voltage", LabelUnit, "V", "rail", "2"): 3.8,
		vkey("rows", "index", "2"):                   380,
	})
	for k := range r.Values {
		if n, _, _ := probewire.ParseValueKey(k); n == ValueTruncated {
			t.Fatalf("truncated without truncation: %s", k)
		}
	}
}

// TestSNMPCustomCounterRate covers AK2: a counter becomes a rate per
// second over two runs (Agent.Advance), scaled; the first run has none.
func TestSNMPCustomCounterRate(t *testing.T) {
	step := clock(t)
	agent := startAgent(t, device)
	store := newStore()
	s := spec(agent, `{"metrics":[
		{"name":"octets","oid":"1.3.6.1.4.1.9999.3.0","kind":"counter","scale":8,"unit":"bit/s"},
		{"name":"drops","column":"1.3.6.1.4.1.9999.10.1.3","kind":"counter","index_label":"queue"}]}`)
	r := run(t, store, s)
	want(t, r, probewire.StatusOK, "")
	if len(r.Values) != 0 {
		t.Fatalf("first run values = %v, want none", r.Values)
	}
	agent.Advance(600)
	step(time.Minute)
	r = run(t, store, s)
	checkValues(t, r, map[string]float64{
		vkey("octets", LabelUnit, "bit/s"): 80,
		vkey("drops", "queue", "1"):        10,
		vkey("drops", "queue", "2"):        10,
	})
	// A restart of the agent: no rate across it.
	agent.Set(gosnmp.SnmpPDU{Name: fakesnmp.OIDSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(5)})
	agent.Advance(600)
	step(time.Minute)
	if r = run(t, store, s); len(r.Values) != 0 {
		t.Fatalf("values across a reboot = %v", r.Values)
	}
}

// TestSNMPCustomThresholdAboveBelow covers AK3 and E-186: crit violated is
// fail/threshold_exceeded, warn ok with threshold_exceeded, in the
// direction of the metric.
func TestSNMPCustomThresholdAboveBelow(t *testing.T) {
	clock(t)
	agent := startAgent(t, device)
	for _, tc := range []struct {
		metric string
		status probewire.Status
		code   string
	}{
		{`{"name":"s","oid":"1.3.6.1.4.1.9999.1.0","warn":40,"crit":50}`, probewire.StatusOK, probewire.WarnThresholdExceeded},
		{`{"name":"s","oid":"1.3.6.1.4.1.9999.1.0","warn":30,"crit":40}`, probewire.StatusFail, probewire.WarnThresholdExceeded},
		{`{"name":"s","oid":"1.3.6.1.4.1.9999.1.0","warn":50,"crit":60}`, probewire.StatusOK, ""},
		{`{"name":"s","oid":"1.3.6.1.4.1.9999.1.0","warn":45,"crit":20,"direction":"below"}`, probewire.StatusOK, probewire.WarnThresholdExceeded},
		{`{"name":"s","oid":"1.3.6.1.4.1.9999.1.0","warn":60,"crit":50,"direction":"below"}`, probewire.StatusFail, probewire.WarnThresholdExceeded},
		{`{"name":"s","oid":"1.3.6.1.4.1.9999.1.0","warn":10,"crit":5,"direction":"below"}`, probewire.StatusOK, ""},
		{`{"name":"v","column":"1.3.6.1.4.1.9999.10.1.2","warn":300}`, probewire.StatusOK, probewire.WarnThresholdExceeded},
	} {
		r := run(t, newStore(), spec(agent, `{"metrics":[`+tc.metric+`]}`))
		if r.Status != tc.status || r.Code != tc.code {
			t.Errorf("%s: %s/%s, want %s/%s", tc.metric, r.Status, r.Code, tc.status, tc.code)
		}
		if tc.code != "" && r.Labels[LabelMetric] == "" {
			t.Errorf("%s: no metric label", tc.metric)
		}
	}
}

// TestSNMPCustomNoSuchObjectDegraded covers AK3: an OID or column the
// agent does not have is ok with no_such_object; the other metrics are
// still delivered, and a violated threshold ranks above it.
func TestSNMPCustomNoSuchObjectDegraded(t *testing.T) {
	clock(t)
	agent := startAgent(t, device)
	r := run(t, newStore(), spec(agent, `{"metrics":[
		{"name":"gone","oid":"1.3.6.1.4.1.9999.77.0"},
		{"name":"sessions","oid":"1.3.6.1.4.1.9999.1.0"},
		{"name":"table","column":"1.3.6.1.4.1.9999.88.1.1"}]}`))
	want(t, r, probewire.StatusOK, probewire.WarnNoSuchObject)
	if r.Values[vkey("sessions")] != 42 || r.Labels[LabelMetric] != "gone" {
		t.Fatalf("values %v labels %v", r.Values, r.Labels)
	}
	r = run(t, newStore(), spec(agent, `{"metrics":[
		{"name":"gone","oid":"1.3.6.1.4.1.9999.77.0"},
		{"name":"sessions","oid":"1.3.6.1.4.1.9999.1.0","crit":10}]}`))
	want(t, r, probewire.StatusFail, probewire.WarnThresholdExceeded)
}

// TestSNMPCustomInvalidName covers AK1: a name outside
// ^[a-z][a-z0-9_]{0,62}$ is a field error.
func TestSNMPCustomInvalidName(t *testing.T) {
	for _, name := range []string{"Upper", "1st", "with-dash", "", strings.Repeat("a", 64), "ä"} {
		s := probewire.ProbeSpec{ID: "p", Type: Type, Target: "192.0.2.1", IntervalMS: 60_000, TimeoutMS: 5000, DownCount: 3, UpCount: 1,
			Params: json.RawMessage(fmt.Sprintf(`{"metrics":[{"name":%q,"oid":"1.3.6.1.2.1.1.7.0"}]}`, name))}
		errs := probewire.Validate(&s)
		if len(errs) == 0 || errs[0].Field != "params.metrics[0].name" {
			t.Errorf("name %q: errs = %v", name, errs)
		}
	}
	// The executor applies the same rule.
	p := Params{Metrics: []Metric{{Name: "Bad", OID: "1.3.6.1.2.1.1.7.0"}}}
	if field, _ := p.check(); field != "params.metrics[0].name" {
		t.Fatalf("executor check: %q", field)
	}
}

// TestSNMPCustomLimits covers AK1 and AK5: 65 metrics, a duplicate name,
// oid and column together, neither of them, interval 29 s and timeout
// 16 s are field errors; the check is not executed (error/config). A
// column of more than 256 rows is cut (probe_snmp_custom_truncated).
func TestSNMPCustomLimits(t *testing.T) {
	typ, ok := probewire.Lookup(Type)
	if !ok || !typ.Available() || typ.MinIntervalS != 30 || typ.MaxTimeoutS != 15 ||
		len(typ.CredentialNamespaces) != 1 || typ.CredentialNamespaces[0] != "snmp" {
		t.Fatalf("contract entry = %+v", typ)
	}
	many := make([]string, MaxMetrics+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"name":"m%d","oid":"1.3.6.1.4.1.9999.%d.0"}`, i, i+1)
	}
	base := probewire.ProbeSpec{ID: "p", Type: Type, Target: "192.0.2.1", IntervalMS: 60_000, TimeoutMS: 5000, DownCount: 3, UpCount: 1}
	for name, tc := range map[string]struct {
		params string
		field  string
		mutate func(*probewire.ProbeSpec)
	}{
		"65 metrics":      {params: `{"metrics":[` + strings.Join(many, ",") + `]}`, field: "params.metrics"},
		"duplicate name":  {params: `{"metrics":[{"name":"a","oid":"1.3.6.1.2.1.1.7.0"},{"name":"a","column":"1.3.6.1.2.1.2.2.1.10"}]}`, field: "params.metrics[1].name"},
		"oid and column":  {params: `{"metrics":[{"name":"a","oid":"1.3.6.1.2.1.1.7.0","column":"1.3.6.1.2.1.2.2.1.10"}]}`, field: "params.metrics[0].oid"},
		"neither":         {params: `{"metrics":[{"name":"a"}]}`, field: "params.metrics[0].oid"},
		"bad oid":         {params: `{"metrics":[{"name":"a","oid":"ifInOctets.1"}]}`, field: "params.metrics[0].oid"},
		"label on scalar": {params: `{"metrics":[{"name":"a","oid":"1.3.6.1.2.1.1.7.0","index_label":"x"}]}`, field: "params.metrics[0].index_label"},
		"label metric":    {params: `{"metrics":[{"name":"a","column":"1.3.6.1.2.1.2.2.1.10","index_label":"metric"}]}`, field: "params.metrics[0].index_label"},
		"crit before warn": {params: `{"metrics":[{"name":"a","oid":"1.3.6.1.2.1.1.7.0","warn":10,"crit":5}]}`,
			field: "params.metrics[0].crit"},
		"bad kind":      {params: `{"metrics":[{"name":"a","oid":"1.3.6.1.2.1.1.7.0","kind":"enum"}]}`, field: "params.metrics[0].kind"},
		"interval 29 s": {field: "interval_s", mutate: func(s *probewire.ProbeSpec) { s.IntervalMS = 29_000 }},
		"timeout 16 s":  {field: "timeout_s", mutate: func(s *probewire.ProbeSpec) { s.TimeoutMS = 16_000 }},
	} {
		s := base
		if tc.params != "" {
			s.Params = json.RawMessage(tc.params)
		}
		if tc.mutate != nil {
			tc.mutate(&s)
		}
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

	clock(t)
	rows := []gosnmp.SnmpPDU{}
	for i := 1; i <= MaxRows+1; i++ {
		rows = append(rows, gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.9999.20.1.1." + strconv.Itoa(i), Type: gosnmp.Gauge32, Value: uint32(i)})
	}
	agent := startAgent(t, rows)
	r := run(t, newStore(), spec(agent, `{"metrics":[{"name":"row","column":"1.3.6.1.4.1.9999.20.1.1"},
		{"name":"missing","oid":"1.3.6.1.4.1.9999.99.0"}]}`))
	// The truncation marker carries its metric, not the worst metric of
	// the result label (missing: no_such_object).
	want(t, r, probewire.StatusOK, probewire.WarnNoSuchObject)
	if r.Values[truncKey("row")] != 1 || len(r.Values) != MaxRows+1 || r.Labels[LabelMetric] != "missing" {
		t.Fatalf("truncated %v, %d values, labels %v", r.Values[truncKey("row")], len(r.Values), r.Labels)
	}
	if _, ok := r.Values[ValueTruncated]; ok {
		t.Fatal("truncation marker without the label metric")
	}
}

// TestSNMPCustomManyCounterSeries covers AK2 with nine counter columns of
// 256 rows (2304 series, above max_rows × 8): the store of the probe
// (Executor.MaxSeries, as the scheduler sizes it) holds every series, so
// the second run has a rate per row. A store that is too small marks the
// metrics it refused with probe_snmp_custom_truncated{metric} instead of
// dropping their rates silently.
func TestSNMPCustomManyCounterSeries(t *testing.T) {
	step := clock(t)
	const columns = 9
	pdus := []gosnmp.SnmpPDU{{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(100_000)}}
	metrics := make([]string, 0, columns)
	for c := 1; c <= columns; c++ {
		col := ".1.3.6.1.4.1.9999.30.1." + strconv.Itoa(c)
		for i := 1; i <= MaxRows; i++ {
			pdus = append(pdus, gosnmp.SnmpPDU{Name: col + "." + strconv.Itoa(i), Type: gosnmp.Counter64, Value: uint64(i)})
		}
		metrics = append(metrics, `{"name":"c`+strconv.Itoa(c)+`","column":"`+col[1:]+`","kind":"counter"}`)
	}
	agent := startAgent(t, pdus)
	s := spec(agent, `{"metrics":[`+strings.Join(metrics, ",")+`]}`)
	s.TimeoutMS = 15_000
	store := newStore()
	want(t, run(t, store, s), probewire.StatusOK, "")
	agent.Advance(60)
	step(time.Minute)
	r := run(t, store, s)
	want(t, r, probewire.StatusOK, "")
	if len(r.Values) != columns*MaxRows {
		t.Fatalf("%d values, want %d", len(r.Values), columns*MaxRows)
	}
	checkValues(t, r, map[string]float64{vkey("c9", "index", "256"): 1})

	small := rate.NewStore(MaxRows*8+16, time.Minute)
	run(t, small, s)
	agent.Advance(60)
	step(time.Minute)
	r = run(t, small, s)
	want(t, r, probewire.StatusOK, "")
	if r.Values[truncKey("c9")] != 1 || r.Values[truncKey("c1")] != 0 {
		t.Fatalf("store limit: c9 truncated %v, c1 %v", r.Values[truncKey("c9")], r.Values[truncKey("c1")])
	}
}

// TestSNMPCustomResultFitsMessage covers AK2 with 64 columns of 256 rows
// (16384 values): the values stop at probe.MaxResultValueBytes, the
// encoded result fits one uplink message, and every metric left without
// room is marked probe_snmp_custom_truncated{metric}.
func TestSNMPCustomResultFitsMessage(t *testing.T) {
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	ev := evaluation{values: map[string]float64{}, store: newStore()}
	for c := range MaxMetrics {
		m := &Metric{Name: fmt.Sprintf("metric_with_a_long_name_%02d", c), Column: "1.3.6.1.4.1.9999.40.1.1",
			IndexLabel: "index", Kind: KindGauge, Unit: "bit/s", Direction: DirectionAbove}
		for i := 1; i <= MaxRows; i++ {
			ev.observe(at, m, []uint32{1, 2, 3, uint32(i)}, gosnmp.SnmpPDU{Type: gosnmp.Gauge32, Value: uint32(123456789)}, true)
		}
	}
	r := ev.result()
	r.ProbeID, r.Epoch = "p-custom", 1
	data, err := json.Marshal(probewire.ResultBatch{Results: []probewire.Result{r}})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > probe.MaxUplinkMessageBytes {
		t.Fatalf("encoded result %d bytes, above %d", len(data), probe.MaxUplinkMessageBytes)
	}
	last := fmt.Sprintf("metric_with_a_long_name_%02d", MaxMetrics-1)
	if r.Values[truncKey(last)] != 1 || r.Values[truncKey("metric_with_a_long_name_00")] != 0 {
		t.Fatalf("truncation markers: last %v first %v", r.Values[truncKey(last)], r.Values[truncKey("metric_with_a_long_name_00")])
	}
}

// templateSpec returns a spec with the parameters of the named template.
func templateSpec(t *testing.T, agent *fakesnmp.Agent, name string) probewire.ProbeSpec {
	t.Helper()
	params, ok := Template(name)
	if !ok {
		t.Fatalf("template %s missing (have %v)", name, TemplateNames())
	}
	return spec(agent, string(params))
}

// TestTemplateUPSRFC1628 covers AK4: the template ups_rfc1628 reads the
// UPS-MIB values of collector/collectorcmd/testdata/power/ups-values.walk
// as pollPowerValues does (runtime in minutes converted to seconds).
func TestTemplateUPSRFC1628(t *testing.T) {
	clock(t)
	agent := walkAgent(t, filepath.Join("..", "..", "collectorcmd", "testdata", "power", "ups-values.walk"))
	r := run(t, newStore(), templateSpec(t, agent, TemplateUPS))
	want(t, r, probewire.StatusOK, "")
	checkValues(t, r, map[string]float64{
		vkey("ups_battery_status"):                          2,
		vkey("ups_battery_runtime_seconds", LabelUnit, "s"): 42 * 60,
		vkey("ups_battery_charge_percent", LabelUnit, "%"):  97,
		vkey("ups_output_load_percent", LabelUnit, "%"):     31,
	})
	// A battery on low (3) is down by the template's thresholds, a
	// runtime below 10 min degraded.
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.33.1.2.3.0", Type: gosnmp.Integer, Value: 8})
	want(t, run(t, newStore(), templateSpec(t, agent, TemplateUPS)), probewire.StatusOK, probewire.WarnThresholdExceeded)
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.33.1.2.1.0", Type: gosnmp.Integer, Value: 4})
	want(t, run(t, newStore(), templateSpec(t, agent, TemplateUPS)), probewire.StatusFail, probewire.WarnThresholdExceeded)
}

// TestTemplatePDUApcRpdu2 covers AK4: the template pdu_apc_rpdu2 reads
// current and power per phase of
// collector/collectorcmd/testdata/power/apc-rpdu2.walk with the values of
// pollPowerValues (TestPDUPowerValues).
func TestTemplatePDUApcRpdu2(t *testing.T) {
	clock(t)
	agent := walkAgent(t, filepath.Join("..", "..", "collectorcmd", "testdata", "power", "apc-rpdu2.walk"))
	r := run(t, newStore(), templateSpec(t, agent, TemplatePDUApcRpdu2))
	want(t, r, probewire.StatusOK, "")
	checkValues(t, r, map[string]float64{
		vkey("pdu_current_amps", LabelUnit, "A", "phase", "1"):       5.2,
		vkey("pdu_current_amps", LabelUnit, "A", "phase", "2"):       3.1,
		vkey("pdu_current_amps", LabelUnit, "A", "phase", "3"):       0,
		vkey("pdu_active_power_watts", LabelUnit, "W", "phase", "1"): 1180,
		vkey("pdu_active_power_watts", LabelUnit, "W", "phase", "2"): 690,
		vkey("pdu_active_power_watts", LabelUnit, "W", "phase", "3"): 0,
	})
	if len(r.Values) != 6 {
		t.Fatalf("values = %v, want six", r.Values)
	}
}
