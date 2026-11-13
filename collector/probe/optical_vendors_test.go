package probe

import (
	"math"
	"testing"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The optical vendor profiles Juniper, Huawei and Arista (PRT-09,
// WP-1005) against fakesnmp: scaling, interface mapping and device
// thresholds per vendor, and the selection of the profile by sysObjectID.

const (
	juniperSysObjectID = ".1.3.6.1.4.1.2636.1.1.1.2.25"
	aristaSysObjectID  = ".1.3.6.1.4.1.30065.1.3011.7048.427.3648"
	huaweiSysObjectID  = ".1.3.6.1.4.1.2011.2.224.67"
)

// wantValues checks the values of a result within 1e-9.
func wantValues(t *testing.T, r probewire.Result, want map[string]float64) {
	t.Helper()
	for name, v := range want {
		got, ok := r.Values[name]
		if !ok || math.Abs(got-v) > 1e-9 {
			t.Errorf("%s = %v (%v), want %v; values %v", name, got, ok, v, r.Values)
		}
	}
}

// vendorCase is one table row: a device, an edit of its walk and the
// expected outcome.
type vendorCase struct {
	name   string
	params string
	edit   func(a interface{ Set(...gosnmp.SnmpPDU) })
	status probewire.Status
	code   string
}

func runVendorCases(t *testing.T, sysObjectID, walk string, cases []vendorCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			params := c.params
			if params == "" {
				params = `{"all":true}`
			}
			r := runOptical(t, sysObjectID, walk, params, c.edit)
			if r.Status != c.status || r.Code != c.code {
				t.Fatalf("%s/%s, want %s/%s; values %v", r.Status, r.Code, c.status, c.code, r.Values)
			}
		})
	}
}

func setInt(oid string, v int) func(a interface{ Set(...gosnmp.SnmpPDU) }) {
	return func(a interface{ Set(...gosnmp.SnmpPDU) }) { a.Set(intPDU(oid, v)) }
}

// TestOpticalJuniperDOM covers AK1 and AK4: JUNIPER-DOM-MIB per ifIndex
// with 0.01 dBm, 0.001 mA, Celsius and 0.001 V, and the alarm and warning
// thresholds of the MIB deciding before the parameters.
func TestOpticalJuniperDOM(t *testing.T) {
	r := runOptical(t, juniperSysObjectID, "juniper-dom.walk", `{"all":true}`, nil)
	wantValues(t, r, map[string]float64{
		opticalKey(ValueOpticalRxDBm, "501", "xe-0/0/0"):       -3.12,
		opticalKey(ValueOpticalTxDBm, "501", "xe-0/0/0"):       -2.01,
		opticalKey(ValueOpticalBiasMA, "501", "xe-0/0/0"):      6.5,
		opticalKey(ValueOpticalTemperature, "501", "xe-0/0/0"): 38,
		opticalKey(ValueOpticalVoltage, "501", "xe-0/0/0"):     3.3,
		opticalKey(ValueOpticalRxDBm, "502", "xe-0/0/1"):       -12.5,
		ValueOpticalInterfaces:                                 2,
		ValueOpticalSensorsUnmapped:                            0,
	})
	const rx502 = ".1.3.6.1.4.1.2636.3.60.1.1.1.1.5.502"
	runVendorCases(t, juniperSysObjectID, "juniper-dom.walk", []vendorCase{
		// -12.50 dBm below the low warning of -11.00 dBm.
		{name: "low warning", status: probewire.StatusOK, code: probewire.WarnThresholdExceeded},
		{name: "within", edit: setInt(rx502, -500), status: probewire.StatusOK},
		// The device thresholds win: -5 dBm against rx_warn_dbm -1 is ok.
		{name: "device before params", params: `{"all":true,"rx_warn_dbm":-1,"rx_crit_dbm":-2}`, edit: setInt(rx502, -500),
			status: probewire.StatusOK},
		{name: "low alarm", edit: setInt(rx502, -1500), status: probewire.StatusFail, code: probewire.WarnThresholdExceeded},
		{name: "high alarm", edit: setInt(rx502, 80), status: probewire.StatusFail, code: probewire.WarnThresholdExceeded},
		{name: "no light", edit: setInt(rx502, -4010), status: probewire.StatusFail, code: probewire.ReasonLOS},
		// The floor of the table: exactly -40.00 dBm is no light as well.
		{name: "floor -40.00 dBm", edit: setInt(rx502, -4000), status: probewire.StatusFail, code: probewire.ReasonLOS},
		// Bias 12.5 mA above the high warning of 12.0 mA (0.001 mA).
		{name: "bias warning", edit: setInt(".1.3.6.1.4.1.2636.3.60.1.1.1.1.6.501", 12500), params: `{"interfaces":["xe-0/0/0"]}`,
			status: probewire.StatusOK, code: probewire.WarnThresholdExceeded},
		// 3.70 V above the high alarm of 3.63 V (0.001 V).
		{name: "voltage alarm", edit: setInt(".1.3.6.1.4.1.2636.3.60.1.1.1.1.25.501", 3700), params: `{"interfaces":["xe-0/0/0"]}`,
			status: probewire.StatusFail, code: probewire.WarnThresholdExceeded},
		{name: "temperature alarm", edit: setInt(".1.3.6.1.4.1.2636.3.60.1.1.1.1.8.501", 80), params: `{"interfaces":["501"]}`,
			status: probewire.StatusFail, code: probewire.WarnThresholdExceeded},
	})
}

// TestOpticalAristaThresholds covers AK2 and AK4: the levels from the
// ENTITY-SENSOR-MIB core (watts to dBm, mapped over ENTITY-MIB), the
// thresholds from ARISTA-ENTITY-SENSOR-MIB scaled like their sensor.
func TestOpticalAristaThresholds(t *testing.T) {
	r := runOptical(t, aristaSysObjectID, "arista-sensor.walk", `{"all":true}`, nil)
	if r.Status != probewire.StatusOK || r.Code != "" {
		t.Fatalf("within the device thresholds: %s/%s; values %v", r.Status, r.Code, r.Values)
	}
	for name, want := range map[string]float64{
		opticalKey(ValueOpticalRxDBm, "1", "Ethernet1"):       -3.0,
		opticalKey(ValueOpticalTxDBm, "1", "Ethernet1"):       -2.0,
		opticalKey(ValueOpticalTemperature, "1", "Ethernet1"): 40.5,
	} {
		if got := r.Values[name]; math.Abs(got-want) > 0.01 {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	const rx = ".1.3.6.1.2.1.99.1.1.1.4.100003"
	runVendorCases(t, aristaSysObjectID, "arista-sensor.walk", []vendorCase{
		// 0.0631 mW = -12.0 dBm: below the low warning of -11 dBm, above the
		// low critical of -14 dBm; the parameters (-20/-25 dBm) would pass.
		{name: "low warning", edit: setInt(rx, 631), status: probewire.StatusOK, code: probewire.WarnThresholdExceeded},
		// 0.0316 mW = -15.0 dBm: below the low critical.
		{name: "low critical", edit: setInt(rx, 316), status: probewire.StatusFail, code: probewire.WarnThresholdExceeded},
		// 1.4125 mW = +1.5 dBm: above the high warning of +1 dBm.
		{name: "high warning", edit: setInt(rx, 14125), status: probewire.StatusOK, code: probewire.WarnThresholdExceeded},
		// Device thresholds before parameters: -3 dBm against -1/-2 dBm.
		{name: "device before params", params: `{"all":true,"rx_warn_dbm":-1,"rx_crit_dbm":-2}`, status: probewire.StatusOK},
		{name: "temperature critical", edit: setInt(".1.3.6.1.2.1.99.1.1.1.4.100005", 760), status: probewire.StatusFail,
			code: probewire.WarnThresholdExceeded},
	})
	// Without the Arista profile (another sysObjectID) the same levels
	// have no device thresholds: -12 dBm passes the parameters.
	if g := runOptical(t, genericSysObjectID, "arista-sensor.walk", `{"all":true}`, setInt(rx, 631)); g.Status != probewire.StatusOK || g.Code != "" {
		t.Fatalf("core without the profile: %s/%s, want ok", g.Status, g.Code)
	}
}

// TestOpticalHuaweiExtent covers AK3 and AK4: HUAWEI-ENTITY-EXTENT-MIB per
// entPhysicalIndex mapped to the interface over ENTITY-MIB, power in
// 0.01 dBm, voltage in mV, bias in uA, an invalid temperature left out,
// and the alarm and warning thresholds of the MIB.
func TestOpticalHuaweiExtent(t *testing.T) {
	r := runOptical(t, huaweiSysObjectID, "huawei-optical.walk", `{"all":true}`, nil)
	wantValues(t, r, map[string]float64{
		opticalKey(ValueOpticalRxDBm, "7", "GE0/0/1"):       -5.3,
		opticalKey(ValueOpticalTxDBm, "7", "GE0/0/1"):       -4.8,
		opticalKey(ValueOpticalTemperature, "7", "GE0/0/1"): 36,
		opticalKey(ValueOpticalVoltage, "7", "GE0/0/1"):     3.31,
		opticalKey(ValueOpticalBiasMA, "7", "GE0/0/1"):      12.5,
		opticalKey(ValueOpticalRxDBm, "8", "GE0/0/2"):       -19.2,
		ValueOpticalInterfaces:                              2,
		ValueOpticalSensorsUnmapped:                         0,
	})
	if _, ok := r.Values[opticalKey(ValueOpticalTemperature, "8", "GE0/0/2")]; ok {
		t.Error("the invalid temperature 2147483647 was reported")
	}
	const rx2 = ".1.3.6.1.4.1.2011.5.25.31.1.1.3.1.8.16842756"
	runVendorCases(t, huaweiSysObjectID, "huawei-optical.walk", []vendorCase{
		{name: "low warning", status: probewire.StatusOK, code: probewire.WarnThresholdExceeded},
		{name: "within", edit: setInt(rx2, -700), status: probewire.StatusOK},
		{name: "low alarm", edit: setInt(rx2, -2100), status: probewire.StatusFail, code: probewire.WarnThresholdExceeded},
		{name: "tx high alarm", edit: setInt(".1.3.6.1.4.1.2011.5.25.31.1.1.3.1.9.16842754", 20), params: `{"interfaces":["GE0/0/1"]}`,
			status: probewire.StatusFail, code: probewire.WarnThresholdExceeded},
		{name: "selection by name", params: `{"interfaces":["GE0/0/1"]}`, status: probewire.StatusOK},
		{name: "floor -40.00 dBm", edit: setInt(rx2, -4000), status: probewire.StatusFail, code: probewire.ReasonLOS},
	})
	// An invalid Rx reading (2147483647) with valid thresholds: the level
	// is unavailable, not silently left out.
	if inv := runOptical(t, huaweiSysObjectID, "huawei-optical.walk", `{"all":true}`, setInt(rx2, 2147483647)); inv.Values[ValueOpticalSensorsUnavailable] != 1 {
		t.Fatalf("invalid Rx reading: values %v, want %s 1", inv.Values, ValueOpticalSensorsUnavailable)
	} else if _, ok := inv.Values[opticalKey(ValueOpticalRxDBm, "8", "GE0/0/2")]; ok {
		t.Fatal("the invalid Rx reading was reported as a level")
	}
	if r.Values[ValueOpticalSensorsUnavailable] != 0 {
		t.Fatalf("valid walk: %s %v, want 0 (an invalid temperature is no level)", ValueOpticalSensorsUnavailable,
			r.Values[ValueOpticalSensorsUnavailable])
	}
	// A module whose port has no alias mapping counts as unmapped.
	unalias := func(a interface{ Set(...gosnmp.SnmpPDU) }) {
		a.Set(intPDU(".1.3.6.1.2.1.47.1.1.1.1.4.16842756", 0))
	}
	if u := runOptical(t, huaweiSysObjectID, "huawei-optical.walk", `{"all":true}`, unalias); u.Values[ValueOpticalSensorsUnmapped] != 2 ||
		u.Values[ValueOpticalInterfaces] != 1 {
		t.Fatalf("unmapped module: %v", u.Values)
	}
}

// TestOpticalVendorProfileSelectedBySysObjectID covers AK1 to AK3 and AK5:
// each sysObjectID selects the optical section of its profile, the
// embedded profiles validate, the Huawei profile classifies nothing but
// names vendor and profile for discovery, and a Juniper walk under a
// foreign sysObjectID falls back to the core (which finds no sensor).
func TestOpticalVendorProfileSelectedBySysObjectID(t *testing.T) {
	reg := profiles.NewRegistry()
	if err := reg.LoadEmbedded(); err != nil {
		t.Fatalf("embedded profiles: %v", err)
	}
	for _, c := range []struct {
		oid, profile, table string
	}{
		{juniperSysObjectID, "juniper-junos-snmp", "interface_power"},
		{aristaSysObjectID, "arista-eos-snmp", "sensor_thresholds"},
		{huaweiSysObjectID, "huawei-vrp-snmp", "entity_power"},
		{ciscoSysObjectID, "cisco-ios-snmp", "sensors"},
	} {
		spec, name, ok := reg.MonitorFor(TypeOptical, c.oid[1:], "")
		if !ok || name != c.profile {
			t.Errorf("%s: profile %q (%v), want %s", c.oid, name, ok, c.profile)
			continue
		}
		if _, ok := spec.Tables[c.table]; !ok {
			t.Errorf("%s: no table %s in %v", c.profile, c.table, spec.Tables)
		}
	}
	huawei, ok := reg.BySysObjectID(huaweiSysObjectID[1:])
	if !ok || huawei.Name != "huawei-vrp-snmp" || huawei.Vendor != "Huawei" || huawei.CIType != "" || huawei.Catalog != "" {
		t.Fatalf("Huawei profile %+v (%v), want vendor Huawei without ciType and catalog", huawei, ok)
	}
	if huawei.Model != "" {
		t.Fatalf("Huawei profile names the model %q; it covers every Huawei product and names none (AK3)", huawei.Model)
	}
	if res := reg.Classify(&profiles.Observation{SysObjectID: huaweiSysObjectID[1:]}); res.Classified() && res.Profile.Name == "huawei-vrp-snmp" {
		t.Fatal("the Huawei profile classified a device")
	}
	if r := runOptical(t, genericSysObjectID, "juniper-dom.walk", `{"all":true}`, nil); r.Status != probewire.StatusError ||
		r.Code != probewire.ReasonUnsupported {
		t.Fatalf("Juniper walk without the profile: %s/%s, want error/unsupported", r.Status, r.Code)
	}
}
