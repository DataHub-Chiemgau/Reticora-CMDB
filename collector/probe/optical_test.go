package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

func opticalKey(name, ifIndex, ifName string) string {
	return probewire.ValueKey(name, map[string]string{LabelOpticalIfIndex: ifIndex, LabelOpticalIfName: ifName})
}

func runOptical(t *testing.T, sysObjectID, walk, params string, edit func(a interface{ Set(...gosnmp.SnmpPDU) })) probewire.Result {
	t.Helper()
	agent := startSNMPAgent(t, sysObjectID, nil, walkFile(t, walk)...)
	if edit != nil {
		edit(agent)
	}
	spec := snmpSpec(TypeOptical, agent, params)
	r := Run(context.Background(), &spec, v2Creds)
	if err := r.Validate(); err != nil {
		t.Fatalf("result does not validate: %v", err)
	}
	return r
}

// TestOpticalScaleAndPrecision covers AK1: value × 10^scale × 10^-precision
// for the scales milli and units and the precisions 0 to 3, also through
// the executor (Rx in micro watts with precision 1 and 2, temperature in
// units with precision 1, bias in milli amperes, voltage with precision 2).
func TestOpticalScaleAndPrecision(t *testing.T) {
	for _, c := range []struct {
		raw       int64
		scale     int64 // EntitySensorDataScale
		precision int
		want      float64
	}{
		{355, 9, 0, 355}, {355, 9, 1, 35.5}, {355, 9, 2, 3.55}, {355, 9, 3, 0.355},
		{65, 8, 0, 0.065}, {65, 8, 1, 0.0065}, {65, 8, 2, 0.00065}, {65, 8, 3, 0.000065},
		{5012, 7, 1, 0.0005012}, {-30, 9, 1, -3}, {2, 10, 0, 2000},
	} {
		if got := OpticalValue(c.raw, int(c.scale-9)*3, c.precision); math.Abs(got-c.want) > 1e-12*math.Max(1, math.Abs(c.want)) {
			t.Errorf("OpticalValue(%d, scale %d, precision %d) = %v, want %v", c.raw, c.scale, c.precision, got, c.want)
		}
	}
	r := runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"all":true}`, nil)
	for name, want := range map[string]float64{
		opticalKey(ValueOpticalRxDBm, "12", "Te1/0/1"):       -3.0,
		opticalKey(ValueOpticalTxDBm, "12", "Te1/0/1"):       -2.0,
		opticalKey(ValueOpticalTemperature, "12", "Te1/0/1"): 35.5,
		opticalKey(ValueOpticalBiasMA, "12", "Te1/0/1"):      6.5,
		opticalKey(ValueOpticalVoltage, "12", "Te1/0/1"):     3.3,
		opticalKey(ValueOpticalRxDBm, "13", "Te1/0/2"):       -21.0,
	} {
		got, ok := r.Values[name]
		if !ok || math.Abs(got-want) > 0.01 {
			t.Errorf("%s = %v (%v), want %v; values %v", name, got, ok, want, r.Values)
		}
	}
	if r.Values[ValueOpticalSensorsUnavailable] != 1 {
		t.Errorf("unavailable = %v, want 1 (Tx of Te1/0/2)", r.Values[ValueOpticalSensorsUnavailable])
	}
	if _, ok := r.Values[opticalKey(ValueOpticalTxDBm, "13", "Te1/0/2")]; ok {
		t.Error("an unavailable sensor reported a value")
	}
}

// TestOpticalMapsSensorToInterface covers AK2: sensor → transceiver →
// port → ifIndex over entPhysicalContainedIn and entAliasMappingTable, at
// most 4 levels; the chassis temperature is no transceiver.
func TestOpticalMapsSensorToInterface(t *testing.T) {
	r := runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"interfaces":["Te1/0/1"]}`, nil)
	if r.Values[ValueOpticalInterfaces] != 1 || r.Values[ValueOpticalInterfacesNotFound] != 0 {
		t.Fatalf("interfaces %v / not found %v, want 1 / 0", r.Values[ValueOpticalInterfaces], r.Values[ValueOpticalInterfacesNotFound])
	}
	if _, ok := r.Values[opticalKey(ValueOpticalRxDBm, "12", "Te1/0/1")]; !ok {
		t.Fatalf("Rx of Te1/0/1 missing: %v", r.Values)
	}
	for key := range r.Values {
		if strings.Contains(key, `ifIndex="13"`) {
			t.Errorf("an interface not selected is in the values: %s", key)
		}
	}
	// By ifIndex, and an interface that has no transceiver.
	r = runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"interfaces":["13","Gi9/9"]}`, nil)
	if _, ok := r.Values[opticalKey(ValueOpticalRxDBm, "13", "Te1/0/2")]; !ok || r.Values[ValueOpticalInterfacesNotFound] != 1 {
		t.Fatalf("by ifIndex: %v", r.Values)
	}
	// The containment chain: a sensor 4 levels below the port maps, one 5
	// levels below does not.
	deep := func(a interface{ Set(...gosnmp.SnmpPDU) }) {
		var pdus []gosnmp.SnmpPDU
		chain := []int{100, 501, 502, 503, 504} // 504 sits 4 levels below port 100
		for i := 1; i < len(chain); i++ {
			pdus = append(pdus, intPDU(fmt.Sprintf(".1.3.6.1.2.1.47.1.1.1.1.4.%d", chain[i]), chain[i-1]))
		}
		pdus = append(pdus, intPDU(".1.3.6.1.2.1.47.1.1.1.1.4.505", 504))
		for _, sensor := range []int{504, 505} {
			pdus = append(pdus,
				gosnmp.SnmpPDU{Name: fmt.Sprintf(".1.3.6.1.2.1.47.1.1.1.1.7.%d", sensor), Type: gosnmp.OctetString, Value: []byte("lane Rx Power")},
				intPDU(fmt.Sprintf(".1.3.6.1.2.1.99.1.1.1.1.%d", sensor), 6), intPDU(fmt.Sprintf(".1.3.6.1.2.1.99.1.1.1.2.%d", sensor), 7),
				intPDU(fmt.Sprintf(".1.3.6.1.2.1.99.1.1.1.3.%d", sensor), 0), intPDU(fmt.Sprintf(".1.3.6.1.2.1.99.1.1.1.4.%d", sensor), 500),
				intPDU(fmt.Sprintf(".1.3.6.1.2.1.99.1.1.1.5.%d", sensor), 1))
		}
		a.Set(pdus...)
	}
	r = runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"all":true}`, deep)
	if r.Values[ValueOpticalSensorsUnmapped] != 2 {
		t.Fatalf("unmapped = %v, want 2 (the orphan and the sensor 5 levels deep)", r.Values[ValueOpticalSensorsUnmapped])
	}
}

// TestOpticalUnmappedSensorIsReported covers AK2: an Rx sensor without an
// interface is counted and enters no state.
func TestOpticalUnmappedSensorIsReported(t *testing.T) {
	// The orphan Rx sensor reads -30 dBm (1 uW): mapped, it would fail.
	r := runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"all":true}`, nil)
	if r.Values[ValueOpticalSensorsUnmapped] != 1 {
		t.Fatalf("unmapped = %v, want 1", r.Values[ValueOpticalSensorsUnmapped])
	}
	if r.Status == probewire.StatusFail {
		t.Fatalf("result %s/%s: the unmapped sensor must not decide the state", r.Status, r.Code)
	}
}

// TestOpticalDeviceThresholdsWinOverParams covers AK3 and AK5: with the
// Cisco profile the device thresholds of entSensorThresholdTable decide,
// not the parameters; minor below -14 dBm → ok with threshold_exceeded,
// critical below -17 dBm → fail/threshold_exceeded.
func TestOpticalDeviceThresholdsWinOverParams(t *testing.T) {
	// -3 dBm against parameters of -1/-2 dBm would fail; the device
	// thresholds keep it ok.
	r := runOptical(t, ciscoSysObjectID, "cisco-dom.walk", `{"all":true,"rx_warn_dbm":-1,"rx_crit_dbm":-2}`, nil)
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("within the device thresholds: %s/%s, want ok; values %v", r.Status, r.Code, r.Values)
	}
	if v := r.Values[opticalKey(ValueOpticalRxDBm, "12", "Te1/0/1")]; math.Abs(v+3) > 1e-9 {
		t.Fatalf("Rx from CISCO-ENTITY-SENSOR-MIB = %v, want -3.0 dBm", v)
	}
	// Bias in mA against device thresholds in mA (both scaled alike).
	if v := r.Values[opticalKey(ValueOpticalBiasMA, "12", "Te1/0/1")]; math.Abs(v-6.5) > 1e-9 {
		t.Fatalf("bias = %v, want 6.5 mA", v)
	}
	highBias := func(a interface{ Set(...gosnmp.SnmpPDU) }) { a.Set(intPDU(".1.3.6.1.4.1.9.9.91.1.1.1.1.4.104", 110)) }
	if br := runOptical(t, ciscoSysObjectID, "cisco-dom.walk", `{"all":true}`, highBias); br.Status != probewire.StatusFail ||
		br.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("bias 11.0 mA above the critical threshold of 10.5 mA: %s/%s, want fail/threshold_exceeded", br.Status, br.Code)
	}
	minor := func(a interface{ Set(...gosnmp.SnmpPDU) }) { a.Set(intPDU(".1.3.6.1.4.1.9.9.91.1.1.1.1.4.102", -150)) }
	r = runOptical(t, ciscoSysObjectID, "cisco-dom.walk", `{"all":true}`, minor)
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("-15.0 dBm below the minor threshold: %s/%s, want ok/threshold_exceeded", r.Status, r.Code)
	}
	critical := func(a interface{ Set(...gosnmp.SnmpPDU) }) { a.Set(intPDU(".1.3.6.1.4.1.9.9.91.1.1.1.1.4.102", -180)) }
	r = runOptical(t, ciscoSysObjectID, "cisco-dom.walk", `{"all":true}`, critical)
	if r.Status != probewire.StatusFail || r.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("-18.0 dBm below the critical threshold: %s/%s, want fail/threshold_exceeded", r.Status, r.Code)
	}
	high := func(a interface{ Set(...gosnmp.SnmpPDU) }) { a.Set(intPDU(".1.3.6.1.4.1.9.9.91.1.1.1.1.4.102", 25)) }
	if hr := runOptical(t, ciscoSysObjectID, "cisco-dom.walk", `{"all":true}`, high); hr.Status != probewire.StatusFail {
		t.Fatalf("+2.5 dBm above the critical high threshold: %s/%s, want fail", hr.Status, hr.Code)
	}
	// The degraded state comes from the state machine (WP-952).
	sm := newStateMachine(t)
	r = runOptical(t, ciscoSysObjectID, "cisco-dom.walk", `{"all":true}`, minor)
	spec := probewire.ProbeSpec{ID: "p-optical", Type: TypeOptical, DownCount: 1, UpCount: 1}
	if _, _, err := sm.Observe(&spec, r); err != nil {
		t.Fatal(err)
	}
	if c, _ := sm.Counters(spec.ID); c.State != probewire.StateDegraded {
		t.Fatalf("state after a warning: %s, want degraded", c.State)
	}
}

// TestOpticalParamThresholds covers AK5: without device thresholds the
// parameters decide, -21 dBm → ok with threshold_exceeded (default
// -20 dBm), -26 dBm → fail/threshold_exceeded (default -25 dBm); Tx only
// with tx thresholds.
func TestOpticalParamThresholds(t *testing.T) {
	r := runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"interfaces":["Te1/0/2"]}`, nil)
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("-21 dBm: %s/%s, want ok/threshold_exceeded", r.Status, r.Code)
	}
	// 2.51 uW = -26.0 dBm.
	weak := func(a interface{ Set(...gosnmp.SnmpPDU) }) { a.Set(intPDU(".1.3.6.1.2.1.99.1.1.1.4.202", 251)) }
	r = runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"interfaces":["Te1/0/2"]}`, weak)
	if r.Status != probewire.StatusFail || r.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("-26 dBm: %s/%s, want fail/threshold_exceeded", r.Status, r.Code)
	}
	// Thresholds from spec.thresholds before params; Tx -2 dBm against
	// tx_warn_dbm -1.
	r = runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"interfaces":["Te1/0/1"],"rx_warn_dbm":-10}`, nil)
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("Te1/0/1 at -3 dBm: %s/%s, want ok", r.Status, r.Code)
	}
	r = runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"interfaces":["Te1/0/1"],"tx_warn_dbm":-1}`, nil)
	if r.Status != probewire.StatusOK || r.Code != probewire.WarnThresholdExceeded {
		t.Fatalf("Tx -2 dBm at tx_warn_dbm -1: %s/%s, want ok/threshold_exceeded", r.Status, r.Code)
	}
}

// TestOpticalLOSIsFail covers AK5: Rx below -40 dBm (and no light at all)
// is fail/los with probe_optical_los = 1 per interface.
func TestOpticalLOSIsFail(t *testing.T) {
	// 0.0891 uW = -40.5 dBm (precision 4).
	los := func(a interface{ Set(...gosnmp.SnmpPDU) }) {
		a.Set(intPDU(".1.3.6.1.2.1.99.1.1.1.3.202", 4), intPDU(".1.3.6.1.2.1.99.1.1.1.4.202", 891))
	}
	r := runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"all":true}`, los)
	if r.Status != probewire.StatusFail || r.Code != probewire.ReasonLOS {
		t.Fatalf("-40.5 dBm: %s/%s, want fail/los", r.Status, r.Code)
	}
	if r.Values[opticalKey(ValueOpticalLOS, "13", "Te1/0/2")] != 1 {
		t.Fatalf("probe_optical_los of Te1/0/2 missing: %v", r.Values)
	}
	if _, ok := r.Values[opticalKey(ValueOpticalLOS, "12", "Te1/0/1")]; ok {
		t.Error("Te1/0/1 reported LOS")
	}
	dark := func(a interface{ Set(...gosnmp.SnmpPDU) }) { a.Set(intPDU(".1.3.6.1.2.1.99.1.1.1.4.202", 0)) }
	if r := runOptical(t, genericSysObjectID, "entity-sensor.walk", `{"all":true}`, dark); r.Code != probewire.ReasonLOS {
		t.Fatalf("0 W: %s/%s, want fail/los", r.Status, r.Code)
	}
}

// TestOpticalWattsToDBm covers AK4: 10 · log10(mW).
func TestOpticalWattsToDBm(t *testing.T) {
	for _, c := range []struct{ watts, dbm float64 }{
		{0.001, 0}, {0.0001, -10}, {0.002, 3.0103}, {0.00001, -20}, {0.0000000891, -40.5},
	} {
		got, ok := WattsToDBm(c.watts)
		if !ok || math.Abs(got-c.dbm) > 0.005 {
			t.Errorf("WattsToDBm(%v) = %v, %v; want %v", c.watts, got, ok, c.dbm)
		}
	}
	for _, w := range []float64{0, -1} {
		if _, ok := WattsToDBm(w); ok {
			t.Errorf("WattsToDBm(%v) converted", w)
		}
	}
}

// TestOpticalLimits covers AK6: 513 interfaces in params, interfaces with
// all and neither are field errors; 513 transceivers are cut at 512
// (probe_optical_truncated = 1) without a state from the rows left out.
func TestOpticalLimits(t *testing.T) {
	typ, ok := probewire.Lookup(TypeOptical)
	if !ok || !typ.Available() || typ.MaxRows != OpticalMaxTransceivers || typ.MinIntervalS != 60 ||
		typ.DefaultIntervalS != 300 || typ.MaxTimeoutS != 60 || len(typ.CredentialNamespaces) != 1 || typ.CredentialNamespaces[0] != "snmp" {
		t.Fatalf("optical entry = %+v", typ)
	}
	if v, ok := typ.ParamDefault("rx_warn_dbm"); !ok || v != -20.0 {
		t.Errorf("rx_warn_dbm default = %v", v)
	}
	if v, ok := typ.ParamDefault("rx_crit_dbm"); !ok || v != -25.0 {
		t.Errorf("rx_crit_dbm default = %v", v)
	}
	many := make([]string, 513)
	for i := range many {
		many[i] = fmt.Sprintf(`"Te1/0/%d"`, i+1)
	}
	for params, want := range map[string]string{
		`{"interfaces":[` + strings.Join(many, ",") + `]}`: "params.interfaces",
		`{"interfaces":["Te1/0/1"],"all":true}`:            "params.interfaces",
		`{}`:                                               "params.interfaces",
		`{"interfaces":[]}`:                                "params.interfaces",
		`{"all":true,"rx_warn_dbm":-25}`:                   "params.rx_crit_dbm",
		`{"all":true,"tx_warn_dbm":-5,"tx_crit_dbm":-4}`:   "params.tx_crit_dbm",
		`{"all":true,"source":"gnmi"}`:                     "params.source",
	} {
		spec := probewire.ProbeSpec{ID: "p", Type: TypeOptical, Target: "192.0.2.9", IntervalMS: 300_000, TimeoutMS: 15_000,
			DownCount: 3, UpCount: 1, Params: json.RawMessage(params)}
		found := false
		for _, e := range probewire.Validate(&spec) {
			found = found || e.Field == want
		}
		if !found {
			t.Errorf("%.60s: errs %v, want field %s", params, probewire.Validate(&spec), want)
		}
	}
	// thresholds come before params (as the executor reads them): a
	// critical level of thresholds above the default warning level.
	for th, want := range map[string]string{
		`{"rx_crit_dbm":-10}`:                     "thresholds.rx_crit_dbm",
		`{"rx_warn_dbm":-30}`:                     "thresholds.rx_warn_dbm",
		`{"tx_warn_dbm":-8,"tx_crit_dbm":-3}`:     "thresholds.tx_crit_dbm",
		`{"rx_warn_dbm":-18,"rx_crit_dbm":-22.5}`: "",
	} {
		spec := probewire.ProbeSpec{ID: "p", Type: TypeOptical, Target: "192.0.2.9", IntervalMS: 300_000, TimeoutMS: 15_000,
			DownCount: 3, UpCount: 1, Params: json.RawMessage(`{"all":true}`), Thresholds: json.RawMessage(th)}
		errs := probewire.Validate(&spec)
		if want == "" {
			if len(errs) > 0 {
				t.Errorf("thresholds %s rejected: %v", th, errs)
			}
			continue
		}
		if len(errs) != 1 || errs[0].Field != want {
			t.Errorf("thresholds %s: errs %v, want field %s", th, errs, want)
		}
	}
	valid := probewire.ProbeSpec{ID: "p", Type: TypeOptical, Target: "192.0.2.9", IntervalMS: 60_000, TimeoutMS: 59_000,
		DownCount: 3, UpCount: 1, Params: json.RawMessage(`{"interfaces":["Te1/0/1","12"],"rx_warn_dbm":-18,"rx_crit_dbm":-22,"tx_warn_dbm":-6,"tx_crit_dbm":-9}`)}
	if errs := probewire.Validate(&valid); len(errs) > 0 {
		t.Errorf("valid spec rejected: %v", errs)
	}

	// 513 transceivers: port p (ifIndex p) > Rx sensor 1000+p, the last
	// one at -30 dBm (it would fail).
	var pdus []gosnmp.SnmpPDU
	for p := 1; p <= 513; p++ {
		sensor := 1000 + p
		value := 500 // 50.0 uW = -13 dBm
		if p == 513 {
			value = 10 // 1.0 uW = -30 dBm
		}
		pdus = append(pdus,
			gosnmp.SnmpPDU{Name: fmt.Sprintf(".1.3.6.1.2.1.47.1.3.2.1.2.%d.0", p), Type: gosnmp.ObjectIdentifier,
				Value: fmt.Sprintf(".1.3.6.1.2.1.2.2.1.1.%d", p)},
			intPDU(fmt.Sprintf(".1.3.6.1.2.1.47.1.1.1.1.4.%d", sensor), p),
			gosnmp.SnmpPDU{Name: fmt.Sprintf(".1.3.6.1.2.1.47.1.1.1.1.7.%d", sensor), Type: gosnmp.OctetString, Value: []byte("Rx Power")},
			intPDU(fmt.Sprintf(".1.3.6.1.2.1.99.1.1.1.1.%d", sensor), 6), intPDU(fmt.Sprintf(".1.3.6.1.2.1.99.1.1.1.2.%d", sensor), 7),
			intPDU(fmt.Sprintf(".1.3.6.1.2.1.99.1.1.1.3.%d", sensor), 1), intPDU(fmt.Sprintf(".1.3.6.1.2.1.99.1.1.1.4.%d", sensor), value),
			intPDU(fmt.Sprintf(".1.3.6.1.2.1.99.1.1.1.5.%d", sensor), 1))
	}
	agent := startSNMPAgent(t, genericSysObjectID, nil, pdus...)
	spec := snmpSpec(TypeOptical, agent, `{"all":true}`)
	spec.TimeoutMS = 30_000
	r := Run(context.Background(), &spec, v2Creds)
	if r.Values[ValueOpticalTruncated] != 1 || r.Values[ValueOpticalInterfaces] != OpticalMaxTransceivers {
		t.Fatalf("513 transceivers: truncated %v, interfaces %v; %s/%s", r.Values[ValueOpticalTruncated],
			r.Values[ValueOpticalInterfaces], r.Status, r.Code)
	}
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("513 transceivers: %s/%s, want ok (-13 dBm), the cut one at -30 dBm not deciding", r.Status, r.Code)
	}
}

// TestOpticalNoSensorIsUnsupported: neither the core nor a profile table
// answers → error/unsupported.
func TestOpticalNoSensorIsUnsupported(t *testing.T) {
	agent := startSNMPAgent(t, genericSysObjectID, nil)
	spec := snmpSpec(TypeOptical, agent, `{"all":true}`)
	if r := Run(context.Background(), &spec, v2Creds); r.Status != probewire.StatusError || r.Code != probewire.ReasonUnsupported {
		t.Fatalf("no sensor table: %+v, want error/unsupported", r)
	}
}
