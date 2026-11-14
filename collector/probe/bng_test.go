package probe

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

const (
	asrSysDescr   = "Cisco IOS XR Software (ASR9K), Version 7.5.2"
	junosMXOID    = ".1.3.6.1.4.1.2636.1.1.1.2.25"
	asrCSUBColumn = ".1.3.6.1.4.1.9.9.786.1.2.1.1.4.1.1."
)

// startBNGAgent serves a walk fixture with sysObjectID and sysDescr of a
// BNG platform.
func startBNGAgent(t *testing.T, sysObjectID, sysDescr string, pdus ...gosnmp.SnmpPDU) *fakesnmp.Agent {
	t.Helper()
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: probeCommunity, SysName: "bng-1",
		SysDescr: sysDescr, SysObjectID: sysObjectID}, pdus...)
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

func asrAgent(t *testing.T) *fakesnmp.Agent {
	t.Helper()
	return startBNGAgent(t, ciscoSysObjectID, asrSysDescr, walkFile(t, "cisco-asr9k-bng.walk")...)
}

func bngClock(t *testing.T) func(time.Duration) {
	t.Helper()
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	bngNow = func() time.Time { return at }
	t.Cleanup(func() { bngNow = time.Now })
	return func(d time.Duration) { at = at.Add(d) }
}

func runBNG(t *testing.T, store *rate.Store, s probewire.ProbeSpec) probewire.Result { //nolint:gocritic // a copy per check
	t.Helper()
	ctx := context.Background()
	if store != nil {
		ctx = rate.WithStore(ctx, store)
	}
	r := Run(ctx, &s, v2Creds)
	if err := r.Validate(); err != nil {
		t.Fatalf("result invalid: %v (%+v)", err, r)
	}
	return r
}

func bngKey(kind string) string {
	return probewire.ValueKey(ValueBNGSessions, map[string]string{LabelBNGType: kind})
}

func setGauge(agent *fakesnmp.Agent, oid string, v uint32) {
	agent.Set(gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Gauge32, Value: v})
}

// scaleASR sets the session counts of the ASR fixture to factor of their
// values.
func scaleASR(agent *fakesnmp.Agent, factor float64) {
	for typ, v := range map[string]float64{"1": 1500, "2": 50, "4": 1000, "9": 450} {
		setGauge(agent, asrCSUBColumn+typ, uint32(v*factor))
	}
}

func newBNGStore() *rate.Store { return rate.NewStore(BNGMaxInterfaces*8+16, time.Minute) }

// TestBNGDrop30PercentIsWarn covers AK3: a drop of 30 % against the
// previous run (store of WP-994) is ok with threshold_exceeded.
func TestBNGDrop30PercentIsWarn(t *testing.T) {
	step := bngClock(t)
	agent := asrAgent(t)
	store := newBNGStore()
	s := snmpSpec(TypeBNG, agent, "")
	runBNG(t, store, s)
	scaleASR(agent, 0.7)
	step(time.Minute)
	r := runBNG(t, store, s)
	expectResult(t, "drop 30 %", r, probewire.StatusOK, probewire.WarnThresholdExceeded)
	if r.Labels[LabelBNGType] != bngTotal || r.Values[bngKey(bngTotal)] != 1050 {
		t.Fatalf("labels %v values %v", r.Labels, r.Values)
	}
	// The next run against the lower value is ok again.
	step(time.Minute)
	expectResult(t, "stable", runBNG(t, store, s), probewire.StatusOK, "")
}

// TestBNGDrop60PercentIsFail covers AK3: a drop of 60 % (≥ drop_crit_ratio
// 0.5) is fail/threshold_exceeded; the ratios are parameters.
func TestBNGDrop60PercentIsFail(t *testing.T) {
	step := bngClock(t)
	agent := asrAgent(t)
	store := newBNGStore()
	s := snmpSpec(TypeBNG, agent, "")
	runBNG(t, store, s)
	scaleASR(agent, 0.4)
	step(time.Minute)
	expectResult(t, "drop 60 %", runBNG(t, store, s), probewire.StatusFail, probewire.WarnThresholdExceeded)

	// With drop_crit_ratio 0.7 the same drop is only degraded.
	agent2 := asrAgent(t)
	store2 := newBNGStore()
	s2 := snmpSpec(TypeBNG, agent2, `{"drop_warn_ratio":0.3,"drop_crit_ratio":0.7}`)
	runBNG(t, store2, s2)
	scaleASR(agent2, 0.4)
	step(time.Minute)
	expectResult(t, "drop 60 % with crit 0.7", runBNG(t, store2, s2), probewire.StatusOK, probewire.WarnThresholdExceeded)
}

// TestBNGBelowMinSessionsIsFail covers AK3: a total below min_sessions is
// fail/threshold_exceeded, also in the first run.
func TestBNGBelowMinSessionsIsFail(t *testing.T) {
	bngClock(t)
	agent := asrAgent(t)
	r := runBNG(t, newBNGStore(), snmpSpec(TypeBNG, agent, `{"min_sessions":2000}`))
	expectResult(t, "below min", r, probewire.StatusFail, probewire.WarnThresholdExceeded)
	if r.Labels[LabelBNGType] != bngTotal {
		t.Fatalf("labels %v", r.Labels)
	}
}

// TestBNGFirstRunHasNoDropRating covers AK3: the first run (no previous
// value, also without a store) rates only min_sessions.
func TestBNGFirstRunHasNoDropRating(t *testing.T) {
	bngClock(t)
	agent := asrAgent(t)
	scaleASR(agent, 0.1)
	expectResult(t, "first run", runBNG(t, newBNGStore(), snmpSpec(TypeBNG, agent, `{"min_sessions":100}`)), probewire.StatusOK, "")
	expectResult(t, "without store", runBNG(t, nil, snmpSpec(TypeBNG, agent, `{"min_sessions":100}`)), probewire.StatusOK, "")
}

// TestBNGTimeout covers AK4: an agent that does not answer is
// fail/timeout.
func TestBNGTimeout(t *testing.T) {
	silent, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	s := probewire.ProbeSpec{ID: "p-bng", Type: TypeBNG, Target: silent.LocalAddr().String(), IntervalMS: 60_000, TimeoutMS: 1500,
		DownCount: 3, UpCount: 1, Epoch: 1}
	expectResult(t, "silent agent", runBNG(t, newBNGStore(), s), probewire.StatusFail, probewire.ReasonTimeout)
}

// TestBNGWithoutProfileIsUnsupported covers AK4: a device without
// monitor.bng (generic agent, a Cisco switch that is no ASR) is
// error/unsupported.
func TestBNGWithoutProfileIsUnsupported(t *testing.T) {
	for name, agent := range map[string]*fakesnmp.Agent{
		"generic":      startBNGAgent(t, genericSysObjectID, "Linux bng-lab", walkFile(t, "cisco-asr9k-bng.walk")...),
		"cisco switch": startBNGAgent(t, ciscoSysObjectID, "Cisco IOS Software, C2960X", walkFile(t, "cisco-asr9k-bng.walk")...),
		"empty asr":    startBNGAgent(t, ciscoSysObjectID, asrSysDescr),
	} {
		expectResult(t, name, runBNG(t, newBNGStore(), snmpSpec(TypeBNG, agent, "")), probewire.StatusError, probewire.ReasonUnsupported)
	}
}

// TestBNGCiscoAndJuniperProfiles covers AK1, AK2 and AK5: both fixtures
// yield total, pppoe and ipoe through their profile.
func TestBNGCiscoAndJuniperProfiles(t *testing.T) {
	bngClock(t)
	for name, tc := range map[string]struct {
		agent              *fakesnmp.Agent
		total, pppoe, ipoe float64
	}{
		"cisco asr9k": {asrAgent(t), 1500, 1000, 450},
		"juniper mx":  {startBNGAgent(t, junosMXOID, "Juniper Networks, Inc. mx480", walkFile(t, "juniper-mx-bng.walk")...), 2000, 1200, 750},
	} {
		r := runBNG(t, newBNGStore(), snmpSpec(TypeBNG, tc.agent, ""))
		expectResult(t, name, r, probewire.StatusOK, "")
		for kind, want := range map[string]float64{bngTotal: tc.total, bngPPPoE: tc.pppoe, bngIPoE: tc.ipoe} {
			if got, ok := r.Values[bngKey(kind)]; !ok || got != want {
				t.Errorf("%s %s = %v (%v), want %v", name, kind, got, ok, want)
			}
		}
		if _, ok := r.Values[ValueBNGTruncated]; ok {
			t.Errorf("%s: truncated", name)
		}
	}
}

// TestBNGCiscoCountsOneAggregationPoint covers AK2: csubAggStatsTable has
// rows for several aggregation points (chassis, line card, interface);
// only the first point (the chassis) counts, no sum across points, also
// when another point changes. A session table cut at the row limit inside
// that point is no measurement: error/response_too_large, never a drop.
func TestBNGCiscoCountsOneAggregationPoint(t *testing.T) {
	step := bngClock(t)
	agent := asrAgent(t)
	store := newBNGStore()
	s := snmpSpec(TypeBNG, agent, "")
	r := runBNG(t, store, s)
	expectResult(t, "points", r, probewire.StatusOK, "")
	if r.Values[bngKey(bngTotal)] != 1500 || r.Values[bngKey(bngPPPoE)] != 1000 || r.Values[bngKey(bngIPoE)] != 450 {
		t.Fatalf("values %v, want the chassis point only", r.Values)
	}
	setGauge(agent, ".1.3.6.1.4.1.9.9.786.1.2.1.1.4.1.5.1", 0)
	step(time.Minute)
	expectResult(t, "line card change", runBNG(t, store, s), probewire.StatusOK, "")

	var rows []gosnmp.SnmpPDU
	for typ := 1; typ <= bngMaxRows+1; typ++ {
		rows = append(rows, gosnmp.SnmpPDU{Name: asrCSUBColumn + strconv.Itoa(typ), Type: gosnmp.Gauge32, Value: uint32(1)})
	}
	cut := startBNGAgent(t, ciscoSysObjectID, asrSysDescr, rows...)
	expectResult(t, "cut table", runBNG(t, newBNGStore(), snmpSpec(TypeBNG, cut, "")), probewire.StatusError, probewire.ReasonResponseTooLarge)
}

// TestBNGJuniperEmptyClientTypeTable covers AK2: jnxSubscriberTotalCount
// answers but the client type table has no rows (older Junos, briefly
// after a restart of subscriber management): only type=total, no pppoe or
// ipoe series of 0 and so no drop of them.
func TestBNGJuniperEmptyClientTypeTable(t *testing.T) {
	step := bngClock(t)
	agent := startBNGAgent(t, junosMXOID, "Juniper Networks, Inc. mx480", walkFile(t, "juniper-mx-bng.walk")...)
	store := newBNGStore()
	s := snmpSpec(TypeBNG, agent, "")
	expectResult(t, "full", runBNG(t, store, s), probewire.StatusOK, "")
	for _, typ := range []string{"1", "3", "4", "5"} {
		agent.Delete(".1.3.6.1.4.1.2636.3.64.1.1.1.5.1.3." + typ)
	}
	step(time.Minute)
	r := runBNG(t, store, s)
	expectResult(t, "empty client types", r, probewire.StatusOK, "")
	if r.Values[bngKey(bngTotal)] != 2000 {
		t.Fatalf("total = %v", r.Values[bngKey(bngTotal)])
	}
	for _, kind := range []string{bngPPPoE, bngIPoE} {
		if v, ok := r.Values[bngKey(kind)]; ok {
			t.Errorf("%s = %v from an empty table", kind, v)
		}
	}
}

// bngTestProfile is a profile with an interfaces table (sessions per
// interface, name from ifName).
const bngTestProfile = `
- name: test-bng-interfaces
  vendor: Test
  protocol: snmp
  ciType: router
  sysObjectIds: [1.3.6.1.4.1.99998.1]
  monitor:
    bng:
      tables:
        interfaces:
          index: int
          columns:
            total: 1.3.6.1.4.1.99998.2.1.1.2
            pppoe: 1.3.6.1.4.1.99998.2.1.1.3
            name: 1.3.6.1.2.1.31.1.1.1.1
`

// TestBNGLimits covers AK3 and AK5: drop_warn_ratio above drop_crit_ratio
// and ratios outside 0–1 are field errors (error/config before the
// check); 257 interfaces truncate the walk (probe_bng_truncated = 1) with
// values of the first 256 only; a drop per interface is rated.
func TestBNGLimits(t *testing.T) {
	typ, ok := probewire.Lookup(TypeBNG)
	if !ok || !typ.Available() || typ.MaxRows != BNGMaxInterfaces || typ.MinIntervalS != 60 || typ.DefaultIntervalS != 60 ||
		typ.MaxTimeoutS != 30 || strings.Join(typ.CredentialNamespaces, ",") != "snmp" {
		t.Fatalf("contract entry = %+v", typ)
	}
	base := probewire.ProbeSpec{ID: "p", Type: TypeBNG, Target: "192.0.2.1", IntervalMS: 60_000, TimeoutMS: 10_000, DownCount: 3, UpCount: 1}
	for params, field := range map[string]string{
		`{"drop_warn_ratio":0.6,"drop_crit_ratio":0.5}`: "params.drop_warn_ratio",
		`{"drop_crit_ratio":0.1}`:                       "params.drop_crit_ratio",
		`{"drop_warn_ratio":1.5}`:                       "params.drop_warn_ratio",
		`{"drop_crit_ratio":-0.1}`:                      "params.drop_crit_ratio",
		`{"min_sessions":-1}`:                           "params.min_sessions",
	} {
		s := base
		s.Params = json.RawMessage(params)
		errs := probewire.Validate(&s)
		if len(errs) == 0 || errs[0].Field != field {
			t.Errorf("%s: errs = %v, want %s", params, errs, field)
		}
		if r := Run(context.Background(), &s, v2Creds); r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig {
			t.Errorf("%s: result %s/%s, want error/config", params, r.Status, r.Code)
		}
	}

	reg := profiles.NewRegistry()
	if err := reg.LoadYAML(strings.NewReader(bngTestProfile)); err != nil {
		t.Fatal(err)
	}
	prev := monitorProfiles
	monitorProfiles = func() *profiles.Registry { return reg }
	t.Cleanup(func() { monitorProfiles = prev })

	step := bngClock(t)
	var pdus []gosnmp.SnmpPDU
	for i := 1; i <= BNGMaxInterfaces+1; i++ {
		idx := "." + strconv.Itoa(i)
		pdus = append(pdus,
			gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.99998.2.1.1.2" + idx, Type: gosnmp.Gauge32, Value: uint32(100)},
			gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.99998.2.1.1.3" + idx, Type: gosnmp.Gauge32, Value: uint32(80)},
			gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.31.1.1.1.1" + idx, Type: gosnmp.OctetString, Value: []byte("Bundle-Ether" + strconv.Itoa(i))})
	}
	agent := startBNGAgent(t, ".1.3.6.1.4.1.99998.1", "Test BNG", pdus...)
	store := newBNGStore()
	s := snmpSpec(TypeBNG, agent, "")
	s.TimeoutMS = 10_000
	r := runBNG(t, store, s)
	expectResult(t, "interfaces", r, probewire.StatusOK, "")
	if r.Values[ValueBNGTruncated] != 1 {
		t.Fatalf("truncated = %v", r.Values[ValueBNGTruncated])
	}
	ifKey := func(kind, name string) string {
		return probewire.ValueKey(ValueBNGSessions, map[string]string{LabelBNGType: kind, LabelBNGInterface: name})
	}
	if r.Values[ifKey(bngTotal, "Bundle-Ether256")] != 100 || r.Values[ifKey(bngPPPoE, "Bundle-Ether1")] != 80 {
		t.Fatalf("interface values missing: %d values", len(r.Values))
	}
	if _, ok := r.Values[ifKey(bngTotal, "Bundle-Ether257")]; ok {
		t.Fatal("value from a row beyond the limit")
	}
	// A drop of one interface is rated with its name.
	setGauge(agent, ".1.3.6.1.4.1.99998.2.1.1.2.7", 10)
	step(time.Minute)
	r = runBNG(t, store, s)
	expectResult(t, "interface drop", r, probewire.StatusFail, probewire.WarnThresholdExceeded)
	if r.Labels[LabelBNGInterface] != "Bundle-Ether7" || r.Labels[LabelBNGType] != bngTotal {
		t.Fatalf("labels = %v", r.Labels)
	}
}
