package probe

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeOptical is the register key of the DOM/DDM probe (PRT-09, WP-1004).
const TypeOptical = "optical"

// OpticalMaxTransceivers is the most interfaces a check evaluates
// (max_rows of the type); more are cut (probe_optical_truncated = 1).
const OpticalMaxTransceivers = 512

// opticalMaxSensors bounds the walk of the sensor table.
const opticalMaxSensors = 8 * OpticalMaxTransceivers * 4

// OpticalLOSDBm is the Rx level below which a transceiver has lost its
// signal (LOS).
const OpticalLOSDBm = -40.0

// Value names of an optical result; the per-interface values carry the
// labels ifIndex and ifName (probewire.ValueKey).
const (
	ValueOpticalRxDBm              = "probe_optical_rx_dbm"
	ValueOpticalTxDBm              = "probe_optical_tx_dbm"
	ValueOpticalTemperature        = "probe_optical_temperature_celsius"
	ValueOpticalBiasMA             = "probe_optical_bias_ma"
	ValueOpticalVoltage            = "probe_optical_voltage_volts"
	ValueOpticalLOS                = "probe_optical_los"
	ValueOpticalSensorsUnavailable = "probe_optical_sensors_unavailable"
	ValueOpticalSensorsUnmapped    = "probe_optical_sensors_unmapped"
	ValueOpticalTruncated          = "probe_optical_truncated"
	ValueOpticalInterfaces         = "probe_optical_interfaces"
	ValueOpticalInterfacesNotFound = "probe_optical_interfaces_not_found"
	LabelOpticalIfIndex            = "ifIndex"
	LabelOpticalIfName             = "ifName"
	opticalQuantityRx              = "rx"
	opticalQuantityTx              = "tx"
	opticalQuantityTemperature     = "temperature"
	opticalQuantityBias            = "bias"
	opticalQuantityVoltage         = "voltage"
	opticalTableSensors            = "sensors"
	opticalTableThresholds         = "thresholds"
	opticalColType                 = "type"
	opticalColScale                = "scale"
	opticalColPrecision            = "precision"
	opticalColValue                = "value"
	opticalColStatus               = "status"
	opticalColSeverity             = "severity"
	opticalColRelation             = "relation"
)

// Sensor kinds the type enum of a sensor table maps to.
const (
	sensorWatts    = "watts"
	sensorDBm      = "dBm"
	sensorCelsius  = "celsius"
	sensorAmperes  = "amperes"
	sensorVoltsDC  = "voltsDC"
	sensorStatusOK = "up"
)

// opticalCore is the standard-MIB core: entPhySensorTable of
// ENTITY-SENSOR-MIB (RFC 3433, mib-2 99; entPhySensorEntry
// 1.3.6.1.2.1.99.1.1.1) with entPhySensorType (1, EntitySensorDataType:
// voltsAC(3), voltsDC(4), amperes(5), watts(6), celsius(8)),
// entPhySensorScale (2, EntitySensorDataScale: yocto(1) … milli(8),
// units(9) … yotta(17)), entPhySensorPrecision (3), entPhySensorValue (4)
// and entPhySensorOperStatus (5: ok(1), unavailable(2),
// nonoperational(3)), index entPhysicalIndex. ENTITY-SENSOR-MIB knows no
// dBm: optical power comes in watts and is converted.
var opticalCore = profiles.MonitorTable{
	Columns: map[string]string{
		opticalColType:      "1.3.6.1.2.1.99.1.1.1.1",
		opticalColScale:     "1.3.6.1.2.1.99.1.1.1.2",
		opticalColPrecision: "1.3.6.1.2.1.99.1.1.1.3",
		opticalColValue:     "1.3.6.1.2.1.99.1.1.1.4",
		opticalColStatus:    "1.3.6.1.2.1.99.1.1.1.5",
	},
	Index: profiles.IndexInt,
	Enums: map[string]map[string]string{
		opticalColType:   {"4": sensorVoltsDC, "5": sensorAmperes, "6": sensorWatts, "8": sensorCelsius},
		opticalColStatus: {"1": sensorStatusOK, "2": "down", "3": "down"},
	},
	Patterns: defaultOpticalPatterns,
}

// defaultOpticalPatterns tell Rx and Tx power sensors apart by the entity
// name and description; a profile may replace them (patterns).
var defaultOpticalPatterns = map[string]string{
	opticalQuantityRx: `(?i)(\b|_)(rx|receive)`,
	opticalQuantityTx: `(?i)(\b|_)(tx|transmit)`,
}

func init() { Register(TypeOptical, &OpticalExecutor{}) }

// OpticalExecutor is the probe type optical (PRT-09, WP-1004): it reads
// the DOM/DDM sensors of the transceivers over SNMP, from the sensor table
// of the monitor section optical of the matching profile (cisco.yaml:
// CISCO-ENTITY-SENSOR-MIB with dBm and entSensorThresholdTable) and
// otherwise, or when that table is empty, from entPhySensorTable of
// ENTITY-SENSOR-MIB, maps every sensor to its interface (optical_entity.go)
// and reports Rx/Tx level (dBm), temperature, bias current and voltage per
// interface. Thresholds (E-186): the device thresholds of the profile when
// it reports them for a sensor, else rx_warn_dbm/rx_crit_dbm (-20/-25 dBm)
// and optionally tx_warn_dbm/tx_crit_dbm. A warning is ok with the warn
// code threshold_exceeded (degraded is derived by the state machine), a
// critical level fail/threshold_exceeded, an Rx level below -40 dBm
// fail/los.
type OpticalExecutor struct{}

type opticalParams struct {
	Interfaces []string `json:"interfaces"`
	All        bool     `json:"all"`
}

// opticalSensor is one sensor of a transceiver.
type opticalSensor struct {
	entity    int
	ifIndex   int
	quantity  string
	kind      string
	value     float64 // in the unit of the quantity (dBm, °C, mA, V)
	los       bool    // power 0 W
	exp       int     // decimal exponent of the raw value
	precision int
}

// opticalThreshold is a device threshold of a sensor.
type opticalThreshold struct {
	severity string // degraded or down
	relation string
	value    float64
}

// Severities of an interface, ordered.
const (
	opticalOK = iota
	opticalWarn
	opticalCrit
	opticalLOS
)

// Execute implements Executor.
func (e *OpticalExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p opticalParams
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "parameters cannot be decoded")
		}
	}
	if !p.All && len(p.Interfaces) == 0 {
		return configResult("params.interfaces", "set interfaces or all: true")
	}
	sess, err := openSNMP(ctx, &spec, creds)
	if err != nil {
		return snmpFailure(err)
	}
	defer sess.Close() //nolint:errcheck // UDP socket, nothing to flush
	monitor, matched, err := snmpMonitor(sess, TypeOptical)
	if err != nil {
		return snmpFailure(err)
	}
	tables := []profiles.MonitorTable{opticalCore}
	var thresholdTable *profiles.MonitorTable
	if t, ok := monitor.Tables[opticalTableSensors]; ok && matched {
		tables = []profiles.MonitorTable{t, opticalCore}
		if th, ok := monitor.Tables[opticalTableThresholds]; ok {
			thresholdTable = &th
		}
	}
	var (
		raw       []rawSensor
		source    *profiles.MonitorTable
		truncated bool
	)
	for i := range tables {
		raw, truncated, err = readSensors(sess, &tables[i])
		if err != nil {
			return snmpFailure(err)
		}
		if len(raw) > 0 {
			source = &tables[i]
			if i > 0 {
				thresholdTable = nil // the core has no device thresholds
			}
			break
		}
	}
	if len(raw) == 0 {
		return failResult(probewire.StatusError, probewire.ReasonUnsupported)
	}
	entities, err := readEntities(sess)
	if err != nil {
		return snmpFailure(err)
	}
	// A cut entity table loses containment, alias or ifName rows: sensors
	// behind it count as unmapped or not found, which is reported.
	truncated = truncated || entities.truncated
	var thresholds map[int][]opticalThreshold
	if thresholdTable != nil {
		var cut bool
		if thresholds, cut, err = readThresholds(sess, thresholdTable, raw); err != nil {
			return snmpFailure(err)
		}
		truncated = truncated || cut
	}
	sensors, unavailable, unmapped := classifySensors(raw, source, entities)
	return opticalResult(&spec, &p, sensors, thresholds, entities, unavailable, unmapped, truncated)
}

// rawSensor is a row of a sensor table.
type rawSensor struct {
	entity    int
	kind      string
	ok        bool
	value     int64
	exp       int
	precision int
}

// readSensors walks the sensor table t.
func readSensors(s *snmp.Session, t *profiles.MonitorTable) ([]rawSensor, bool, error) {
	names := []string{opticalColType, opticalColScale, opticalColPrecision, opticalColValue, opticalColStatus}
	oids := map[string]string{}
	var walk []string
	for _, n := range names {
		if oid, ok := t.ColumnOID(n); ok {
			oids[n] = oid
			walk = append(walk, oid)
		}
	}
	if oids[opticalColValue] == "" || oids[opticalColType] == "" {
		return nil, false, nil
	}
	rows, err := s.WalkColumns(walk, opticalMaxSensors)
	if err != nil {
		return nil, false, err
	}
	out := make([]rawSensor, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		entity, _, ierr := snmp.IndexInt(row.Index)
		if ierr != nil {
			continue
		}
		get := func(col string) (int64, bool) {
			v, ok := row.Value(oids[col])
			if !ok {
				return 0, false
			}
			return pduInt(v)
		}
		typ, hasType := get(opticalColType)
		if !hasType {
			continue
		}
		kind, _ := t.MapEnum(opticalColType, typ)
		rs := rawSensor{entity: entity, kind: kind, ok: true}
		if scale, ok := get(opticalColScale); ok && scale >= 1 && scale <= 17 {
			rs.exp = int(scale-9) * 3
		}
		if prec, ok := get(opticalColPrecision); ok && prec >= -8 && prec <= 9 {
			rs.precision = int(prec)
		}
		if st, ok := get(opticalColStatus); ok {
			mapped, _ := t.MapEnum(opticalColStatus, st)
			rs.ok = mapped == sensorStatusOK
		}
		var hasValue bool
		if rs.value, hasValue = get(opticalColValue); !hasValue {
			rs.ok = false
		}
		out = append(out, rs)
	}
	return out, rows.Truncated, nil
}

// OpticalValue is the value of a sensor reading (WP-1004 AK1):
// value × 10^exp × 10^(−precision), exp the decimal exponent of
// EntitySensorDataScale ((scale − 9) × 3: milli −3, units 0, kilo 3).
func OpticalValue(raw int64, exp, precision int) float64 {
	return float64(raw) * math.Pow10(exp-precision)
}

// WattsToDBm converts optical power in watts to dBm (10 · log10(mW));
// ok is false for 0 W or less (no light).
func WattsToDBm(watts float64) (float64, bool) {
	mw := watts * 1000
	if !(mw > 0) {
		return 0, false
	}
	return 10 * math.Log10(mw), true
}

// classifySensors converts the sensors to their quantities and maps them to
// interfaces. Power sensors (watts, dBm) are Rx or Tx after the patterns;
// a power sensor without interface counts as unmapped. Sensors of other
// quantities without interface are chassis sensors and left out. A sensor
// whose status is not ok counts as unavailable and has no value.
func classifySensors(raw []rawSensor, t *profiles.MonitorTable, m *entityMap) (out []opticalSensor, unavailable, unmapped int) {
	patterns := t.Patterns
	if len(patterns) == 0 {
		patterns = defaultOpticalPatterns
	}
	rx := regexp.MustCompile(patternOr(patterns[opticalQuantityRx], defaultOpticalPatterns[opticalQuantityRx]))
	tx := regexp.MustCompile(patternOr(patterns[opticalQuantityTx], defaultOpticalPatterns[opticalQuantityTx]))
	for i := range raw {
		r := &raw[i]
		var quantity string
		switch r.kind {
		case sensorWatts, sensorDBm:
			switch label := m.label(r.entity); {
			case rx.MatchString(label):
				quantity = opticalQuantityRx
			case tx.MatchString(label):
				quantity = opticalQuantityTx
			}
		case sensorCelsius:
			quantity = opticalQuantityTemperature
		case sensorAmperes:
			quantity = opticalQuantityBias
		case sensorVoltsDC:
			quantity = opticalQuantityVoltage
		}
		if quantity == "" {
			continue
		}
		ifIndex, mapped := m.interfaceOf(r.entity)
		if !mapped {
			if quantity == opticalQuantityRx || quantity == opticalQuantityTx {
				unmapped++
			}
			continue
		}
		if !r.ok {
			unavailable++
			continue
		}
		s := opticalSensor{entity: r.entity, ifIndex: ifIndex, quantity: quantity, kind: r.kind, exp: r.exp, precision: r.precision}
		v, light := sensorUnitValue(r.kind, OpticalValue(r.value, r.exp, r.precision))
		s.value, s.los = v, !light
		out = append(out, s)
	}
	return out, unavailable, unmapped
}

// sensorUnitValue converts a scaled sensor reading (or a device threshold
// of the sensor) to the unit of its quantity: watts to dBm (ok false for no
// light), amperes to mA; the other kinds stay as they are. Sensor value and
// threshold go through the same conversion, so that they compare.
func sensorUnitValue(kind string, v float64) (float64, bool) {
	switch kind {
	case sensorWatts:
		return WattsToDBm(v)
	case sensorAmperes:
		return v * 1000, true // A → mA
	}
	return v, true
}

func patternOr(p, def string) string {
	if strings.TrimSpace(p) == "" {
		return def
	}
	return p
}

// readThresholds walks the device thresholds of the profile (index
// entPhysicalIndex.thresholdIndex) and scales them like their sensor. A
// cut walk (truncated true) may hold only part of the thresholds of the
// last entity read: they are dropped, so that the sensor falls back to the
// parameters instead of being judged by an incomplete set (a minor row
// without its critical row).
func readThresholds(s *snmp.Session, t *profiles.MonitorTable, sensors []rawSensor) (out map[int][]opticalThreshold, truncated bool, err error) {
	sev, ok1 := t.ColumnOID(opticalColSeverity)
	rel, ok2 := t.ColumnOID(opticalColRelation)
	val, ok3 := t.ColumnOID(opticalColValue)
	if !ok1 || !ok2 || !ok3 {
		return nil, false, nil
	}
	bySensor := map[int]*rawSensor{}
	for i := range sensors {
		bySensor[sensors[i].entity] = &sensors[i]
	}
	rows, err := s.WalkColumns([]string{sev, rel, val}, opticalMaxSensors)
	if err != nil {
		return nil, false, err
	}
	out = map[int][]opticalThreshold{}
	last := -1
	for _, row := range rows.Rows {
		entity, _, ierr := snmp.IndexInt(row.Index)
		if ierr != nil {
			continue
		}
		last = max(last, entity)
		sensor, ok := bySensor[entity]
		if !ok {
			continue
		}
		ints := make([]int64, 3)
		complete := true
		for i, col := range []string{sev, rel, val} {
			v, ok := row.Value(col)
			n, isInt := pduInt(v)
			complete = complete && ok && isInt
			ints[i] = n
		}
		if !complete {
			continue
		}
		severity, _ := t.MapEnum(opticalColSeverity, ints[0])
		relation, _ := t.MapEnum(opticalColRelation, ints[1])
		if severity != string(probewire.StateDegraded) && severity != string(probewire.StateDown) {
			continue
		}
		v, light := sensorUnitValue(sensor.kind, OpticalValue(ints[2], sensor.exp, sensor.precision))
		if !light {
			continue
		}
		out[entity] = append(out[entity], opticalThreshold{severity: severity, relation: relation, value: v})
	}
	if rows.Truncated {
		delete(out, last)
	}
	return out, rows.Truncated, nil
}

// holds reports whether the relation of a device threshold holds for v.
func (th opticalThreshold) holds(v float64) bool {
	switch th.relation {
	case "lessThan":
		return v < th.value
	case "lessOrEqual":
		return v <= th.value
	case "greaterThan":
		return v > th.value
	case "greaterOrEqual":
		return v >= th.value
	case "equalTo":
		return v == th.value
	case "notEqualTo":
		return v != th.value
	}
	return false
}

// opticalInterface is the evaluation of one interface.
type opticalInterface struct {
	ifIndex  int
	ifName   string
	values   map[string]float64
	severity int
}

// opticalResult evaluates the sensors per interface (WP-1004 AK4–AK6).
func opticalResult(spec *probewire.ProbeSpec, p *opticalParams, sensors []opticalSensor, thresholds map[int][]opticalThreshold,
	m *entityMap, unavailable, unmapped int, truncated bool) probewire.Result {
	byIf := map[int]*opticalInterface{}
	for i := range sensors {
		s := &sensors[i]
		oi, ok := byIf[s.ifIndex]
		if !ok {
			oi = &opticalInterface{ifIndex: s.ifIndex, ifName: m.ifName[s.ifIndex], values: map[string]float64{}}
			byIf[s.ifIndex] = oi
		}
		name := map[string]string{
			opticalQuantityRx: ValueOpticalRxDBm, opticalQuantityTx: ValueOpticalTxDBm,
			opticalQuantityTemperature: ValueOpticalTemperature, opticalQuantityBias: ValueOpticalBiasMA,
			opticalQuantityVoltage: ValueOpticalVoltage,
		}[s.quantity]
		if !s.los {
			oi.values[name] = s.value
		}
		oi.severity = max(oi.severity, sensorSeverity(spec, s, thresholds[s.entity]))
	}
	// The selection, in ifIndex order, at most OpticalMaxTransceivers.
	wantIndex, wantName := map[int]bool{}, map[string]bool{}
	for _, name := range p.Interfaces {
		name = strings.TrimSpace(name)
		if n, err := strconv.Atoi(name); err == nil {
			wantIndex[n] = true
		}
		wantName[name] = true
	}
	indexes := make([]int, 0, len(byIf))
	for idx, oi := range byIf {
		if p.All || wantIndex[idx] || wantName[oi.ifName] {
			indexes = append(indexes, idx)
		}
	}
	sort.Ints(indexes)
	if len(indexes) > OpticalMaxTransceivers {
		indexes, truncated = indexes[:OpticalMaxTransceivers], true
	}
	values := map[string]float64{
		ValueOpticalSensorsUnavailable: float64(unavailable),
		ValueOpticalSensorsUnmapped:    float64(unmapped),
		ValueOpticalInterfaces:         float64(len(indexes)),
	}
	if !p.All {
		// Listed interfaces without transceiver data: reported, no state.
		notFound := 0
		for _, want := range p.Interfaces {
			want = strings.TrimSpace(want)
			n, isIndex := strconv.Atoi(want)
			found := false
			for idx, oi := range byIf {
				if oi.ifName == want || (isIndex == nil && idx == n) {
					found = true
					break
				}
			}
			if !found {
				notFound++
			}
		}
		values[ValueOpticalInterfacesNotFound] = float64(notFound)
	}
	if truncated {
		values[ValueOpticalTruncated] = 1
	}
	worst := opticalOK
	for _, idx := range indexes {
		oi := byIf[idx]
		labels := map[string]string{LabelOpticalIfIndex: strconv.Itoa(idx), LabelOpticalIfName: oi.ifName}
		for name, v := range oi.values {
			values[probewire.ValueKey(name, labels)] = v
		}
		if oi.severity == opticalLOS {
			values[probewire.ValueKey(ValueOpticalLOS, labels)] = 1
		}
		worst = max(worst, oi.severity)
	}
	r := probewire.Result{Status: probewire.StatusOK, Values: values}
	switch worst {
	case opticalLOS:
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonLOS
	case opticalCrit:
		r.Status, r.Code = probewire.StatusFail, probewire.WarnThresholdExceeded
	case opticalWarn:
		r.Code = probewire.WarnThresholdExceeded
	}
	return r
}

// sensorSeverity evaluates one sensor: LOS for Rx below -40 dBm (or no
// light); the device thresholds when the profile reports some for the
// sensor; else the parameters (Rx -20/-25 dBm by default, Tx only when
// set). Temperature, bias and voltage are evaluated by device thresholds
// only.
func sensorSeverity(spec *probewire.ProbeSpec, s *opticalSensor, device []opticalThreshold) int {
	if s.quantity == opticalQuantityRx && (s.los || s.value < OpticalLOSDBm) {
		return opticalLOS
	}
	if s.los {
		return opticalCrit // a transmitter without light
	}
	if len(device) > 0 {
		sev := opticalOK
		for _, th := range device {
			if th.holds(s.value) {
				if th.severity == string(probewire.StateDown) {
					sev = max(sev, opticalCrit)
				} else {
					sev = max(sev, opticalWarn)
				}
			}
		}
		return sev
	}
	var prefix string
	switch s.quantity {
	case opticalQuantityRx:
		prefix = "rx"
	case opticalQuantityTx:
		prefix = "tx"
	default:
		return opticalOK
	}
	if crit, ok := thresholdOf(spec, prefix+"_crit_dbm"); ok && s.value < crit {
		return opticalCrit
	}
	if warn, ok := thresholdOf(spec, prefix+"_warn_dbm"); ok && s.value < warn {
		return opticalWarn
	}
	return opticalOK
}
