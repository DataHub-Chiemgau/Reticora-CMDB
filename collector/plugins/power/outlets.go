package power

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Outlet mapping (IMP-09): the names of PDU outlets and UPS outlet groups
// name what they feed ("web01 PSU1", "pdu-a"). The server turns them into
// powered_by edges device → PDU → UPS, marking redundant feeds.

// outletTable is a vendor table of outlet names and (optionally) states.
type outletTable struct {
	vendor string
	// pdu marks the tables of PDUs (the others are UPS outlet groups).
	pdu      bool
	nameOID  string
	stateOID string
	// on is the state value of a switched-on outlet.
	on int
}

var outletTables = []outletTable{
	// APC Rack PDU 2 (rPDU2OutletSwitchedStatusName/State: off 1, on 2).
	{vendor: "apc-rpdu2", pdu: true, nameOID: "1.3.6.1.4.1.318.1.1.26.9.2.3.1.3", stateOID: "1.3.6.1.4.1.318.1.1.26.9.2.3.1.5", on: 2},
	// APC MasterSwitch / Rack PDU, PowerNet-MIB sPDU (sPDUOutletName of
	// sPDUOutletConfigTable, sPDUOutletCtl of sPDUOutletControlTable:
	// on 1, off 2).
	{vendor: "apc-spdu", pdu: true, nameOID: "1.3.6.1.4.1.318.1.1.4.5.2.1.3", stateOID: "1.3.6.1.4.1.318.1.1.4.4.2.1.3", on: 1},
	// Raritan PX2/PX3, PDU2-MIB (outletName/outletSwitchingState of PDU 1:
	// on 7, off 8).
	{vendor: "raritan", pdu: true, nameOID: "1.3.6.1.4.1.13742.6.3.5.3.1.3.1", stateOID: "1.3.6.1.4.1.13742.6.4.1.2.1.3.1", on: 7},
	// APC UPS outlet groups (upsOutletGroupStatusName, column 2, and
	// upsOutletGroupStatusGroupState, column 3: on 1, off 2). Column 3 is
	// the state, not the name (WP-498).
	{vendor: "apc-ups", nameOID: "1.3.6.1.4.1.318.1.1.1.12.1.2.1.2", stateOID: "1.3.6.1.4.1.318.1.1.1.12.1.2.1.3", on: 1},
}

// walkOutlets reads the outlet names of the first vendor table the device
// answers; outlets without a name are left out. pdu reports a PDU table.
// A device that answers none of the tables has no outlets; that is no
// error.
func walkOutlets(g *gosnmp.GoSNMP) (outlets []plugins.DeviceOutlet, pdu bool) {
	for _, table := range outletTables {
		names := walkColumn(g, table.nameOID)
		if len(names) == 0 {
			continue
		}
		states := walkColumn(g, table.stateOID)
		idx := make([]int, 0, len(names))
		for i := range names {
			idx = append(idx, i)
		}
		sortInts(idx)
		out := make([]plugins.DeviceOutlet, 0, len(idx))
		for _, i := range idx {
			name := strings.TrimSpace(names[i])
			if name == "" || isDefaultOutletName(name) {
				continue
			}
			o := plugins.DeviceOutlet{Name: name}
			if st, ok := states[i]; ok {
				if n, err := strconv.Atoi(st); err == nil {
					o.State = map[bool]string{true: "on", false: "off"}[n == table.on]
				}
			}
			out = append(out, o)
		}
		return out, table.pdu
	}
	return nil, false
}

// detectOutletTable reports the first vendor table (in the order of
// walkOutlets) the device answers: one GETNEXT of all name columns, a
// table answers when the next OID lies in its column.
func detectOutletTable(g *gosnmp.GoSNMP) (outletTable, bool) {
	oids := make([]string, len(outletTables))
	for i, table := range outletTables {
		oids[i] = table.nameOID
	}
	pkt, err := g.GetNext(oids)
	if err != nil || len(pkt.Variables) != len(oids) {
		return outletTable{}, false
	}
	for i, table := range outletTables {
		v := pkt.Variables[i]
		if v.Type != gosnmp.EndOfMibView && strings.HasPrefix(strings.TrimPrefix(v.Name, "."), table.nameOID+".") {
			return table, true
		}
	}
	return outletTable{}, false
}

// Power readings of PDUs (MON-02): the load current and active power per
// phase or inlet from the metering table of the vendor family that
// detectOutletTable found. The metric names are the same for all
// families; the row is the label "phase" or "inlet".
const (
	MetricPDUCurrent = "pdu_current_amps"
	MetricPDUPower   = "pdu_active_power_watts"
)

// meterTable is the metering table of a vendor family.
type meterTable struct {
	vendor string
	// index names the label of the row index ("" for none).
	index string
	// labels are read from columns of the row (label → column OID).
	labels []meterLabel
	// bankOID is the bank number column of rows that are a bank, not a
	// phase (value ≠ 0); they repeat the phase load and are left out.
	bankOID string
	columns []meterColumn
}

type meterLabel struct{ label, oid string }

// meterColumn is one reading of a meterTable.
type meterColumn struct {
	metric string
	oid    string
	// scale converts the raw value into the unit of the metric.
	scale float64
	// sensor selects the rows of a PDU2-MIB sensor table, whose index is
	// <row>.<sensorType>; empty for a plain column.
	sensor string
	// digitsOID is the column of the decimal digits of the sensor (same
	// index): the value is raw / 10^digits (PDU2-MIB).
	digitsOID string
}

var meterTables = []meterTable{
	// APC Rack PDU 2, rPDU2PhaseStatusTable: Module (2), Number (3),
	// Current in tenths of amps (5), Power in hundredths of kW (7).
	{vendor: "apc-rpdu2",
		labels: []meterLabel{{"phase", "1.3.6.1.4.1.318.1.1.26.6.3.1.3"}, {"module", "1.3.6.1.4.1.318.1.1.26.6.3.1.2"}},
		columns: []meterColumn{
			{metric: MetricPDUCurrent, oid: "1.3.6.1.4.1.318.1.1.26.6.3.1.5", scale: 0.1},
			{metric: MetricPDUPower, oid: "1.3.6.1.4.1.318.1.1.26.6.3.1.7", scale: 10},
		}},
	// APC Rack PDU (rPDU of the PowerNet-MIB, AP78xx/AP79xx, whose
	// outlets are the sPDU tables), rPDULoadStatusTable: Load in tenths of
	// amps (2), PhaseNumber (4), BankNumber (5, 0 for a phase row). The
	// rPDU reports no active power per phase.
	{vendor: "apc-spdu",
		labels:  []meterLabel{{"phase", "1.3.6.1.4.1.318.1.1.12.2.3.1.1.4"}},
		bankOID: "1.3.6.1.4.1.318.1.1.12.2.3.1.1.5",
		columns: []meterColumn{
			{metric: MetricPDUCurrent, oid: "1.3.6.1.4.1.318.1.1.12.2.3.1.1.2", scale: 0.1},
		}},
	// Raritan PX2/PX3, PDU2-MIB of PDU 1: measurementsInletSensorValue
	// (index inletId.sensorType; rmsCurrent 1 in A, activePower 5 in W),
	// scaled by inletSensorDecimalDigits.
	{vendor: "raritan", index: "inlet",
		columns: []meterColumn{
			{metric: MetricPDUCurrent, oid: "1.3.6.1.4.1.13742.6.5.2.3.1.4.1", scale: 1, sensor: "1", digitsOID: "1.3.6.1.4.1.13742.6.3.3.4.1.7.1"},
			{metric: MetricPDUPower, oid: "1.3.6.1.4.1.13742.6.5.2.3.1.4.1", scale: 1, sensor: "5", digitsOID: "1.3.6.1.4.1.13742.6.3.3.4.1.7.1"},
		}},
}

// PDUValues reads the load of a PDU of the vendor family (HitDevice): one
// sample per reading and phase or inlet, labelled target, device=pdu and
// the row. A family without a metering table yields no samples and no
// error; an unanswered table yields none.
func (p *Plugin) PDUValues(ctx context.Context, target string, creds map[string]string, family string) ([]plugins.MetricSample, error) {
	var table *meterTable
	for i := range meterTables {
		if meterTables[i].vendor == family {
			table = &meterTables[i]
		}
	}
	if table == nil {
		return nil, nil
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	community := creds["community"]
	if community == "" {
		community = "public"
	}
	g := p.v2cSession(ctx, target, community, timeout)
	if err := g.Connect(); err != nil {
		return nil, fmt.Errorf("power: dial %s: %w", target, err)
	}
	defer g.Conn.Close()
	return table.read(g, target), nil
}

// read walks the columns of t and returns the samples.
func (t *meterTable) read(g *gosnmp.GoSNMP, target string) []plugins.MetricSample {
	walked := map[string]map[string]string{}
	rows := func(oid string) map[string]string {
		if _, ok := walked[oid]; !ok {
			walked[oid] = walkRows(g, oid)
		}
		return walked[oid]
	}
	var samples []plugins.MetricSample
	for _, c := range t.columns {
		values := rows(c.oid)
		for _, idx := range sortedIndexes(values) {
			row := idx
			if c.sensor != "" {
				var ok bool
				if row, ok = strings.CutSuffix(idx, "."+c.sensor); !ok {
					continue
				}
			}
			if t.bankOID != "" {
				if bank := rows(t.bankOID)[row]; bank != "" && bank != "0" {
					continue
				}
			}
			raw, err := strconv.ParseFloat(values[idx], 64)
			if err != nil {
				continue
			}
			value := raw * c.scale
			if c.digitsOID != "" {
				if digits, err := strconv.Atoi(rows(c.digitsOID)[idx]); err == nil && digits > 0 {
					value /= math.Pow10(digits)
				}
			}
			labels := map[string]string{"target": target, "device": "pdu"}
			if t.index != "" {
				labels[t.index] = row
			}
			for _, l := range t.labels {
				if v := rows(l.oid)[row]; v != "" {
					labels[l.label] = v
				}
			}
			samples = append(samples, plugins.MetricSample{Name: c.metric, Value: value, Labels: labels})
		}
	}
	return samples
}

// Attributes of a power hit (Probe) that select its readings.
const (
	attrDeviceType   = "deviceType"
	attrVendorFamily = "vendorFamily"
)

// HitDevice returns the device type ("ups", "pdu", "ats"; "" when the hit
// does not say) and the vendor family of the outlet table ("apc-rpdu2",
// "apc-spdu", "raritan", "apc-ups"; "" for none) of a power hit.
func HitDevice(hit *plugins.Result) (deviceType, family string) {
	deviceType, _ = hit.Attributes[attrDeviceType].(string)
	family, _ = hit.Attributes[attrVendorFamily].(string)
	return deviceType, family
}

// walkRows returns the values of a table column by row index, the OID
// suffix after the column ("1" or, for a multi-part index, "1.5").
func walkRows(g *gosnmp.GoSNMP, column string) map[string]string {
	out := map[string]string{}
	_ = g.BulkWalk(column, func(v gosnmp.SnmpPDU) error {
		idx, ok := strings.CutPrefix(strings.TrimPrefix(v.Name, "."), column+".")
		if !ok {
			return nil
		}
		switch val := v.Value.(type) {
		case []byte:
			out[idx] = string(val)
		default:
			out[idx] = gosnmp.ToBigInt(v.Value).String()
		}
		return nil
	})
	return out
}

// sortedIndexes returns the row indexes of values in OID order.
func sortedIndexes(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for idx := range values {
		out = append(out, idx)
	}
	slices.SortFunc(out, func(a, b string) int {
		pa, pb := strings.Split(a, "."), strings.Split(b, ".")
		for i := 0; i < len(pa) && i < len(pb); i++ {
			x, _ := strconv.Atoi(pa[i])
			y, _ := strconv.Atoi(pb[i])
			if x != y {
				return cmp.Compare(x, y)
			}
		}
		return cmp.Compare(len(pa), len(pb))
	})
	return out
}

// walkColumn returns the values of a table column by row index.
func walkColumn(g *gosnmp.GoSNMP, column string) map[int]string {
	out := map[int]string{}
	_ = g.BulkWalk(column, func(v gosnmp.SnmpPDU) error {
		oid := strings.TrimPrefix(v.Name, ".")
		i := strings.LastIndexByte(oid, '.')
		if i < 0 {
			return nil
		}
		n, err := strconv.Atoi(oid[i+1:])
		if err != nil {
			return nil
		}
		switch val := v.Value.(type) {
		case []byte:
			out[n] = string(val)
		default:
			out[n] = gosnmp.ToBigInt(v.Value).String()
		}
		return nil
	})
	return out
}

// isDefaultOutletName reports factory names ("Outlet 1", "Outlet_12",
// the UPS outlet group "Outlet Group 3") that name nothing.
func isDefaultOutletName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimLeft(strings.TrimPrefix(n, "outlet"), " _-#")
	n = strings.TrimLeft(strings.TrimPrefix(n, "group"), " _-#")
	if _, err := strconv.Atoi(n); err == nil {
		return true
	}
	return n == ""
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func (p *Plugin) port() int {
	if p.Port > 0 {
		return p.Port
	}
	return snmpPort
}
