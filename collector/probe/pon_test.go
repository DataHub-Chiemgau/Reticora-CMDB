package probe

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

const (
	ponTestOID    = ".1.3.6.1.4.1.99997.1"
	ponONUColumn  = ".1.3.6.1.4.1.99997.2.1.1."
	ponTestPortA  = "pon0/1"
	ponTestPortB  = "pon0/2"
	ponTestPortIx = 101
)

// usePONProfile makes the test profile of testdata (loaded with LoadDir)
// the profile registry of the SNMP probe types for the test.
func usePONProfile(t *testing.T) {
	t.Helper()
	reg := profiles.NewRegistry()
	if err := reg.LoadDir("testdata"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("test-generic-olt"); !ok {
		t.Fatal("test profile not loaded")
	}
	prev := monitorProfiles
	monitorProfiles = func() *profiles.Registry { return reg }
	t.Cleanup(func() { monitorProfiles = prev })
}

func ponClock(t *testing.T) func(time.Duration) {
	t.Helper()
	at := time.Date(2026, 11, 14, 8, 10, 0, 0, time.UTC)
	ponNow = func() time.Time { return at }
	t.Cleanup(func() { ponNow = time.Now })
	return func(d time.Duration) { at = at.Add(d) }
}

func ponAgent(t *testing.T, pdus ...gosnmp.SnmpPDU) *fakesnmp.Agent {
	t.Helper()
	if pdus == nil {
		pdus = walkFile(t, "generic-pon.walk")
	}
	return startBNGAgent(t, ponTestOID, "Test OLT", pdus...)
}

func ponSpec(agent *fakesnmp.Agent, params string) probewire.ProbeSpec {
	return probewire.ProbeSpec{ID: "p-pon", Type: TypePON, Target: "127.0.0.1:" + strconv.Itoa(agent.Addr().Port),
		IntervalMS: 300_000, TimeoutMS: 30_000, DownCount: 3, UpCount: 1, Epoch: 1, Params: json.RawMessage(params)}
}

func runPON(t *testing.T, store *rate.Store, s probewire.ProbeSpec) probewire.Result { //nolint:gocritic // a copy per check
	t.Helper()
	r := Run(rate.WithStore(context.Background(), store), &s, v2Creds)
	if err := r.Validate(); err != nil {
		t.Fatalf("result invalid: %v (%+v)", err, r)
	}
	return r
}

func newPONStore() *rate.Store { return rate.NewStore(PONMaxONUs*8+16, 5*time.Minute) }

func portKey(name, port string) string {
	return probewire.ValueKey(name, map[string]string{LabelPONPort: port})
}

func rxKey(port, stat string) string {
	return probewire.ValueKey(ValuePONRx, map[string]string{LabelPONPort: port, LabelPONStat: stat})
}

// setONU sets status and last down cause of ONU id on port.
func setONU(agent *fakesnmp.Agent, port, id, status, cause int) {
	idx := strconv.Itoa(port) + "." + strconv.Itoa(id)
	agent.Set(gosnmp.SnmpPDU{Name: ponONUColumn + "2." + idx, Type: gosnmp.Integer, Value: status},
		gosnmp.SnmpPDU{Name: ponONUColumn + "6." + idx, Type: gosnmp.Integer, Value: cause})
}

// changes returns the status change values of a result.
func changes(r probewire.Result) []map[string]string { //nolint:gocritic // results are small
	var out []map[string]string
	for k := range r.Values {
		name, labels, err := probewire.ParseValueKey(k)
		if err == nil && name == ValuePONStatusChange {
			out = append(out, labels)
		}
	}
	return out
}

func countValues(r probewire.Result, name string) int { //nolint:gocritic // results are small
	n := 0
	for k := range r.Values {
		if v, _, err := probewire.ParseValueKey(k); err == nil && v == name {
			n++
		}
	}
	return n
}

// TestPONAggregatesPerPort covers AK1 and AK2: online/offline per port and
// the receive levels min, avg and p05 over the online ONUs.
func TestPONAggregatesPerPort(t *testing.T) {
	usePONProfile(t)
	ponClock(t)
	r := runPON(t, newPONStore(), ponSpec(ponAgent(t), ""))
	expectResult(t, "aggregate", r, probewire.StatusOK, "")
	for k, want := range map[string]float64{
		portKey(ValuePONOnline, ponTestPortA):  9,
		portKey(ValuePONOffline, ponTestPortA): 1,
		portKey(ValuePONOnline, ponTestPortB):  5,
		portKey(ValuePONOffline, ponTestPortB): 0,
		rxKey(ponTestPortA, "min"):             -28.5,
		rxKey(ponTestPortA, "avg"):             -207.5 / 9,
		rxKey(ponTestPortA, "p05"):             -28.5,
		rxKey(ponTestPortB, "min"):             -22.5,
		rxKey(ponTestPortB, "avg"):             -21.5,
	} {
		if got, ok := r.Values[k]; !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("%s = %v (%v), want %v", k, got, ok, want)
		}
	}
	if len(changes(r)) != 0 {
		t.Fatalf("first run has changes: %v", changes(r))
	}
	// A port filter keeps only the named port.
	r = runPON(t, newPONStore(), ponSpec(ponAgent(t), `{"ports":["pon0/2"]}`))
	if _, ok := r.Values[portKey(ValuePONOnline, ponTestPortA)]; ok || r.Values[portKey(ValuePONOnline, ponTestPortB)] != 5 {
		t.Fatalf("port filter: %v", r.Values)
	}
}

// TestPONRxSidesNeverMix covers AK2: probe_pon_rx_dbm without side is
// measured at the ONUs only; an ONU with rx_olt but no rx_onu does not
// enter it, its OLT-side level goes into the statistics with side=olt.
func TestPONRxSidesNeverMix(t *testing.T) {
	usePONProfile(t)
	ponClock(t)
	agent := ponAgent(t)
	agent.Delete(ponONUColumn + "4.101.7")
	r := runPON(t, newPONStore(), ponSpec(agent, ""))
	expectResult(t, "sides", r, probewire.StatusOK, "")
	oltKey := func(stat string) string {
		return probewire.ValueKey(ValuePONRx, map[string]string{LabelPONPort: ponTestPortA, LabelPONStat: stat, LabelPONSide: "olt"})
	}
	for k, want := range map[string]float64{
		rxKey(ponTestPortA, "min"): -24.5,
		rxKey(ponTestPortA, "avg"): -179.0 / 8,
		oltKey("min"):              -29.5,
		oltKey("avg"):              -216.5 / 9,
	} {
		if got, ok := r.Values[k]; !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("%s = %v (%v), want %v", k, got, ok, want)
		}
	}
}

// TestPONUnmappedStatusSkipped covers AK1: a status value the profile maps
// to neither online nor offline (initializing, unknown, …) counts as
// neither and yields no state change, in either direction.
func TestPONUnmappedStatusSkipped(t *testing.T) {
	usePONProfile(t)
	step := ponClock(t)
	agent := ponAgent(t)
	store := newPONStore()
	s := ponSpec(agent, `{"port_offline_warn":0.5}`)
	runPON(t, store, s)
	agent.Set(gosnmp.SnmpPDU{Name: ponONUColumn + "2.101.1", Type: gosnmp.Integer, Value: 9})
	step(5 * time.Minute)
	r := runPON(t, store, s)
	expectResult(t, "unmapped", r, probewire.StatusOK, "")
	if r.Values[portKey(ValuePONOnline, ponTestPortA)] != 8 || r.Values[portKey(ValuePONOffline, ponTestPortA)] != 1 {
		t.Fatalf("counts online %v offline %v", r.Values[portKey(ValuePONOnline, ponTestPortA)], r.Values[portKey(ValuePONOffline, ponTestPortA)])
	}
	if c := changes(r); len(c) != 0 {
		t.Fatalf("state change from an unmapped status: %v", c)
	}
	agent.Set(gosnmp.SnmpPDU{Name: ponONUColumn + "2.101.1", Type: gosnmp.Integer, Value: 1})
	step(5 * time.Minute)
	if c := changes(runPON(t, store, s)); len(c) != 0 {
		t.Fatalf("state change back from an unmapped status: %v", c)
	}
}

// TestPONOnuStateChangeOnlyOnChange covers AK2: a second run without
// change yields no probe_pon_onu_status_change; Agent.Set on one ONU yields
// exactly one change with its serial number and cause.
func TestPONOnuStateChangeOnlyOnChange(t *testing.T) {
	usePONProfile(t)
	step := ponClock(t)
	agent := ponAgent(t)
	store := newPONStore()
	s := ponSpec(agent, "")
	runPON(t, store, s)
	step(5 * time.Minute)
	if r := runPON(t, store, s); len(changes(r)) != 0 {
		t.Fatalf("unchanged run: %v", changes(r))
	}
	setONU(agent, ponTestPortIx, 3, 2, 2)
	step(5 * time.Minute)
	r := runPON(t, store, s)
	got := changes(r)
	if len(got) != 1 {
		t.Fatalf("changes = %v, want one", got)
	}
	want := map[string]string{LabelPONSN: "HWTC00006503", LabelPONPort: ponTestPortA, LabelPONFrom: "online",
		LabelPONTo: "offline", LabelPONCause: PONCauseDyingGasp}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("change label %s = %q, want %q (%v)", k, got[0][k], v, got[0])
		}
	}
	// Back online: one change without a cause.
	setONU(agent, ponTestPortIx, 3, 1, 3)
	step(5 * time.Minute)
	got = changes(runPON(t, store, s))
	if len(got) != 1 || got[0][LabelPONTo] != "online" || got[0][LabelPONCause] != "" {
		t.Fatalf("back online: %v", got)
	}
}

// TestPONOnuRxOnlyOnViolationOrHourly covers AK2 (E-188): the level of an
// ONU is reported when it violates onu_rx_warn_dbm and for every ONU in the
// first run of a UTC hour.
func TestPONOnuRxOnlyOnViolationOrHourly(t *testing.T) {
	usePONProfile(t)
	step := ponClock(t)
	agent := ponAgent(t)
	store := newPONStore()
	s := ponSpec(agent, "")
	if r := runPON(t, store, s); countValues(r, ValuePONONURx) != 14 {
		t.Fatalf("first run of the hour: %d ONU levels, want 14 (every online ONU)", countValues(r, ValuePONONURx))
	}
	step(5 * time.Minute)
	r := runPON(t, store, s)
	if n := countValues(r, ValuePONONURx); n != 1 {
		t.Fatalf("second run: %d ONU levels, want only the violating one", n)
	}
	if v := r.Values[probewire.ValueKey(ValuePONONURx, map[string]string{LabelPONSN: "HWTC00006507", LabelPONPort: ponTestPortA})]; v != -28.5 {
		t.Fatalf("violating level = %v", v)
	}
	// A stricter threshold reports more ONUs.
	if r := runPON(t, store, ponSpec(agent, `{"onu_rx_warn_dbm":-22}`)); countValues(r, ValuePONONURx) < 5 {
		t.Fatalf("threshold -22 dBm: %d levels", countValues(r, ValuePONONURx))
	}
	step(time.Hour)
	if r := runPON(t, store, s); countValues(r, ValuePONONURx) != 14 {
		t.Fatalf("first run of the next hour: %d levels", countValues(r, ValuePONONURx))
	}
}

// TestPONCardinality covers AK3: 8193 ONUs truncate the walk
// (probe_pon_truncated = 1); the port cut by the limit has no port values,
// and an ONU beyond the cut yields no change.
func TestPONCardinality(t *testing.T) {
	usePONProfile(t)
	step := ponClock(t)
	var pdus []gosnmp.SnmpPDU
	add := func(port, id int) {
		pdus = append(pdus, gosnmp.SnmpPDU{Name: ponONUColumn + "2." + strconv.Itoa(port) + "." + strconv.Itoa(id), Type: gosnmp.Integer, Value: 1})
	}
	for id := 1; id <= 8000; id++ {
		add(201, id)
	}
	for id := 1; id <= PONMaxONUs+1-8000; id++ {
		add(202, id)
	}
	agent := ponAgent(t, pdus...)
	store := newPONStore()
	s := ponSpec(agent, "")
	s.TimeoutMS = 60_000
	r := runPON(t, store, s)
	expectResult(t, "truncated", r, probewire.StatusOK, "")
	if r.Values[ValuePONTruncated] != 1 || r.Values[portKey(ValuePONOnline, "201")] != 8000 {
		t.Fatalf("truncated %v, port 201 online %v", r.Values[ValuePONTruncated], r.Values[portKey(ValuePONOnline, "201")])
	}
	if _, ok := r.Values[portKey(ValuePONOnline, "202")]; ok {
		t.Fatal("port value of the port cut by the limit")
	}
	// The ONU beyond the limit goes offline: no change.
	setONU(agent, 202, PONMaxONUs+1-8000, 2, 1)
	step(5 * time.Minute)
	if r = runPON(t, store, s); len(changes(r)) != 0 {
		t.Fatalf("change from a row beyond the limit: %v", changes(r))
	}
}

// TestPONPortOfflineRatioIsWarn covers AK4: a share of offline ONUs above
// port_offline_warn is ok with threshold_exceeded and the port label.
func TestPONPortOfflineRatioIsWarn(t *testing.T) {
	usePONProfile(t)
	ponClock(t)
	agent := ponAgent(t)
	// 1 of 10 offline is not above 0.1.
	expectResult(t, "10 %", runPON(t, newPONStore(), ponSpec(agent, "")), probewire.StatusOK, "")
	setONU(agent, ponTestPortIx, 1, 2, 1)
	r := runPON(t, newPONStore(), ponSpec(agent, ""))
	expectResult(t, "20 %", r, probewire.StatusOK, probewire.WarnThresholdExceeded)
	if r.Labels[LabelPONPort] != ponTestPortA {
		t.Fatalf("labels = %v", r.Labels)
	}
	expectResult(t, "20 % with 0.25", runPON(t, newPONStore(), ponSpec(agent, `{"port_offline_warn":0.25}`)), probewire.StatusOK, "")
}

// TestPONDyingGaspBurstMarkedPower covers AK4: power_event_min ONUs going
// offline with dying gasp in one run mark probe_pon_power_event = 1; fewer
// do not.
func TestPONDyingGaspBurstMarkedPower(t *testing.T) {
	usePONProfile(t)
	step := ponClock(t)
	agent := ponAgent(t)
	store := newPONStore()
	s := ponSpec(agent, "")
	runPON(t, store, s)
	for id := 1; id <= 7; id++ {
		setONU(agent, ponTestPortIx, id, 2, 2)
	}
	step(5 * time.Minute)
	r := runPON(t, store, s)
	if _, ok := r.Values[ValuePONPowerEvent]; ok || len(changes(r)) != 7 {
		t.Fatalf("7 dying gasps: power event %v, %d changes", r.Values[ValuePONPowerEvent], len(changes(r)))
	}
	// Eight in one run across both ports of the OLT: a power event.
	setONU(agent, ponTestPortIx, 8, 2, 2)
	setONU(agent, ponTestPortIx, 9, 2, 2)
	for id := 1; id <= 5; id++ {
		setONU(agent, 102, id, 2, 2)
	}
	setONU(agent, ponTestPortIx, 10, 1, 3)
	step(5 * time.Minute)
	runPON(t, store, s)
	setONU(agent, ponTestPortIx, 10, 2, 2)
	step(5 * time.Minute)
	if r = runPON(t, store, s); r.Values[ValuePONPowerEvent] != 0 {
		t.Fatalf("one dying gasp: power event %v", r.Values[ValuePONPowerEvent])
	}
	fresh := ponAgent(t)
	store = newPONStore()
	s = ponSpec(fresh, "")
	runPON(t, store, s)
	for id := 1; id <= 6; id++ {
		setONU(fresh, ponTestPortIx, id, 2, 2)
	}
	setONU(fresh, 102, 1, 2, 2)
	setONU(fresh, 102, 2, 2, 2)
	step(5 * time.Minute)
	r = runPON(t, store, s)
	if r.Values[ValuePONPowerEvent] != 1 || len(changes(r)) != 8 {
		t.Fatalf("8 dying gasps across ports: power event %v, %d changes", r.Values[ValuePONPowerEvent], len(changes(r)))
	}
	// Only the OLT unreachable is fail: many ONUs offline stay ok (with the
	// warn code of the offline share).
	if r.Status != probewire.StatusOK {
		t.Fatalf("status %s", r.Status)
	}
}

// TestPONUnreachableIsFail covers AK4: an OLT that does not answer is
// fail/timeout.
func TestPONUnreachableIsFail(t *testing.T) {
	silent, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	s := probewire.ProbeSpec{ID: "p-pon", Type: TypePON, Target: silent.LocalAddr().String(), IntervalMS: 300_000, TimeoutMS: 1500,
		DownCount: 3, UpCount: 1, Epoch: 1}
	expectResult(t, "silent OLT", runPON(t, newPONStore(), s), probewire.StatusFail, probewire.ReasonTimeout)
}

// TestPONWithoutProfileIsUnsupported covers AK1: an OLT without
// monitor.pon is error/unsupported (unknown).
func TestPONWithoutProfileIsUnsupported(t *testing.T) {
	ponClock(t)
	agent := startBNGAgent(t, genericSysObjectID, "Linux olt-lab", walkFile(t, "generic-pon.walk")...)
	expectResult(t, "no profile", runPON(t, newPONStore(), ponSpec(agent, "")), probewire.StatusError, probewire.ReasonUnsupported)
}

// TestPONLimits covers AK3 and AK5: a timeout of 61 s and an interval below
// 300 s are field errors of probewire.Validate; the contract entry has
// max_rows 8192 and the namespace snmp.
func TestPONLimits(t *testing.T) {
	typ, ok := probewire.Lookup(TypePON)
	if !ok || !typ.Available() || typ.MaxRows != PONMaxONUs || typ.MaxTimeoutS != 60 || typ.MinIntervalS != 300 ||
		typ.DefaultIntervalS != 300 || len(typ.CredentialNamespaces) != 1 || typ.CredentialNamespaces[0] != "snmp" {
		t.Fatalf("contract entry = %+v", typ)
	}
	base := probewire.ProbeSpec{ID: "p", Type: TypePON, Target: "192.0.2.1", IntervalMS: 300_000, TimeoutMS: 60_000, DownCount: 3, UpCount: 1}
	for name, tc := range map[string]struct {
		mutate func(*probewire.ProbeSpec)
		field  string
	}{
		"timeout 61 s":      {func(s *probewire.ProbeSpec) { s.TimeoutMS = 61_000 }, "timeout_s"},
		"interval 299 s":    {func(s *probewire.ProbeSpec) { s.IntervalMS = 299_000 }, "interval_s"},
		"offline share 1.5": {func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"port_offline_warn":1.5}`) }, "params.port_offline_warn"},
		"power event min 0": {func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"power_event_min":0}`) }, "params.power_event_min"},
		"65 ports":          {func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"ports":[` + manyPorts(65) + `]}`) }, "params.ports"},
		"rx threshold -61":  {func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"onu_rx_warn_dbm":-61}`) }, "params.onu_rx_warn_dbm"},
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
		if r := Run(context.Background(), &s, v2Creds); r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig {
			t.Errorf("%s: result %s/%s, want error/config", name, r.Status, r.Code)
		}
	}
	if errs := probewire.Validate(&base); len(errs) > 0 {
		t.Fatalf("edges rejected: %v", errs)
	}
}

func manyPorts(n int) string {
	out := ""
	for i := range n {
		if i > 0 {
			out += ","
		}
		out += `"pon` + strconv.Itoa(i) + `"`
	}
	return out
}
