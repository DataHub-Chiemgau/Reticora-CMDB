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
	opticalTableSensorThresholds   = "sensor_thresholds"
	opticalTablePrefixInterface    = "interface_"
	opticalTablePrefixEntity       = "entity_"
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

// Vendor tables of the monitor section optical (WP-1005), besides the
// sensor table "sensors" with its row-per-threshold table "thresholds"
// (cisco.yaml):
//
//   - "interface_<name>" and "entity_<name>": wide tables with one row per
//     transceiver, indexed by ifIndex (JUNIPER-DOM-MIB) or by
//     entPhysicalIndex mapped to the interface like a sensor
//     (HUAWEI-ENTITY-EXTENT-MIB). The columns rx, tx, temperature, bias and
//     voltage hold the quantities; threshold_columns maps
//     <quantity>_<low|high>_<warn|alarm> to the column of a device
//     threshold. Value and thresholds of a table are multiplied by its
//     scale and converted by its unit: W to dBm for rx and tx (dBm
//     otherwise), A to mA for bias. Several tables of the same index kind
//     are merged per transceiver (one table per unit).
//   - "sensor_thresholds": a wide threshold table per sensor
//     (ARISTA-ENTITY-SENSOR-MIB, index entPhysicalIndex of the sensor) with
//     threshold_columns low_warn, low_alarm, high_warn and high_alarm,
//     scaled like the sensor; it belongs to the sensor table of the
//     profile or, without one, to the ENTITY-SENSOR-MIB core.
//
// A low threshold holds below its value, a high one above it; warn is a
// warning (degraded), alarm critical.

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
	// wide marks a quantity of a vendor transceiver table; device holds
	// its thresholds (the thresholds map of the sensor tables does not
	// apply).
	wide   bool
	device []opticalThreshold
	// invalid marks a quantity of a vendor table whose value column holds
	// the "no reading" value (2147483647): it counts as unavailable.
	invalid bool
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
	if matched {
		if r, ok := e.executeWide(&spec, &p, sess, &monitor); ok {
			return r
		}
	}
	tables := []profiles.MonitorTable{opticalCore}
	var thresholdTable, wideThresholdTable *profiles.MonitorTable
	profileSensors := false
	if matched {
		if t, ok := monitor.Tables[opticalTableSensors]; ok {
			tables = []profiles.MonitorTable{t, opticalCore}
			profileSensors = true
		}
		if th, ok := monitor.Tables[opticalTableThresholds]; ok {
			thresholdTable = &th
		}
		if th, ok := monitor.Tables[opticalTableSensorThresholds]; ok {
			wideThresholdTable = &th
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
			if i > 0 && profileSensors {
				// The thresholds of the profile belong to its sensor table,
				// not to the core it fell back to.
				thresholdTable, wideThresholdTable = nil, nil
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
	if wideThresholdTable != nil {
		wide, cut, werr := readWideThresholds(sess, wideThresholdTable, raw)
		if werr != nil {
			return snmpFailure(werr)
		}
		if thresholds == nil {
			thresholds = map[int][]opticalThreshold{}
		}
		for entity, list := range wide {
			thresholds[entity] = append(thresholds[entity], list...)
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
		device := thresholds[s.entity]
		if s.wide {
			device = s.device
		}
		oi.severity = max(oi.severity, sensorSeverity(spec, s, device))
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
	// A vendor table in 0.01 dBm reports a port without light at its floor,
	// often exactly -40.00 dBm: there -40 dBm itself is LOS.
	if s.quantity == opticalQuantityRx && (s.los || s.value < OpticalLOSDBm || (s.wide && s.value <= OpticalLOSDBm)) {
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

// opticalQuantities are the quantity columns of a wide transceiver table.
var opticalQuantities = []string{opticalQuantityRx, opticalQuantityTx, opticalQuantityTemperature, opticalQuantityBias, opticalQuantityVoltage}

// opticalThresholdKinds are the device thresholds of a wide table: the
// suffix of the threshold_columns key with its severity and relation.
var opticalThresholdKinds = []struct {
	suffix, severity, relation string
}{
	{"low_warn", string(probewire.StateDegraded), "lessThan"},
	{"low_alarm", string(probewire.StateDown), "lessThan"},
	{"high_warn", string(probewire.StateDegraded), "greaterThan"},
	{"high_alarm", string(probewire.StateDown), "greaterThan"},
}

// wideTables returns the wide transceiver tables of the monitor section
// in name order, with whether each is indexed by entPhysicalIndex.
func wideTables(m *profiles.MonitorSpec) (tables []profiles.MonitorTable, entity []bool) {
	names := make([]string, 0, len(m.Tables))
	for name := range m.Tables {
		if strings.HasPrefix(name, opticalTablePrefixInterface) || strings.HasPrefix(name, opticalTablePrefixEntity) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		tables = append(tables, m.Tables[name])
		entity = append(entity, strings.HasPrefix(name, opticalTablePrefixEntity))
	}
	return tables, entity
}

// wideUnitValue converts a scaled value of a wide table to the unit of its
// quantity: rx and tx in W to dBm (light false for 0 W or less), bias in A
// to mA; dBm, mA, °C and V stay as they are.
func wideUnitValue(quantity, unit string, v float64) (float64, bool) {
	switch {
	case (quantity == opticalQuantityRx || quantity == opticalQuantityTx) && strings.EqualFold(unit, "W"):
		return WattsToDBm(v)
	case quantity == opticalQuantityBias && unit == "A":
		return v * 1000, true
	}
	return v, true
}

// wideKey is one quantity of one transceiver row.
type wideKey struct {
	entity   bool
	index    int
	quantity string
}

// executeWide evaluates the wide transceiver tables of the profile
// (JUNIPER-DOM-MIB, HUAWEI-ENTITY-EXTENT-MIB); ok is false without such a
// table or when none of them has a row, and the sensor tables decide.
func (e *OpticalExecutor) executeWide(spec *probewire.ProbeSpec, p *opticalParams, sess *snmp.Session, m *profiles.MonitorSpec) (probewire.Result, bool) {
	tables, entityIndexed := wideTables(m)
	if len(tables) == 0 {
		return probewire.Result{}, false
	}
	values := map[wideKey]*opticalSensor{}
	var order []wideKey
	truncated := false
	for i := range tables {
		cut, err := readWide(sess, &tables[i], entityIndexed[i], values, &order)
		if err != nil {
			return snmpFailure(err), true
		}
		truncated = truncated || cut
	}
	if len(values) == 0 {
		return probewire.Result{}, false
	}
	entities, err := readEntities(sess)
	if err != nil {
		return snmpFailure(err), true
	}
	truncated = truncated || entities.truncated
	var sensors []opticalSensor
	unavailable, unmapped := 0, 0
	for _, k := range order {
		s := values[k]
		power := k.quantity == opticalQuantityRx || k.quantity == opticalQuantityTx
		if !s.wide && (!s.invalid || !power) {
			continue // thresholds without a value
		}
		if k.entity {
			ifIndex, mapped := entities.interfaceOf(k.index)
			if !mapped {
				if power {
					unmapped++
				}
				continue
			}
			s.ifIndex = ifIndex
		}
		if !s.wide {
			// An invalid level of a transceiver: unavailable, like a
			// sensor whose status is not ok.
			unavailable++
			continue
		}
		sensors = append(sensors, *s)
	}
	return opticalResult(spec, p, sensors, nil, entities, unavailable, unmapped, truncated), true
}

// readWide walks one wide table and adds its quantities and device
// thresholds to values (order keeps the first appearance).
func readWide(sess *snmp.Session, t *profiles.MonitorTable, entity bool, values map[wideKey]*opticalSensor, order *[]wideKey) (bool, error) {
	walk := make([]string, 0, len(t.Columns))
	for _, col := range sortedColumns(t) {
		oid, _ := t.ColumnOID(col)
		walk = append(walk, oid)
	}
	if len(walk) == 0 {
		return false, nil
	}
	rows, err := sess.WalkColumns(walk, opticalMaxSensors)
	if err != nil {
		return false, err
	}
	factor := t.Factor()
	for _, row := range rows.Rows {
		index, _, ierr := snmp.IndexInt(row.Index)
		if ierr != nil {
			continue
		}
		// read returns the scaled value of a column; invalid reports a
		// column that holds the "no reading" value.
		read := func(col string) (value float64, ok, invalid bool) {
			oid, ok := t.ColumnOID(col)
			if !ok {
				return 0, false, false
			}
			v, ok := row.Value(oid)
			if !ok {
				return 0, false, false
			}
			n, ok := pduInt(v)
			if !ok {
				return 0, false, false
			}
			if opticalInvalid(n) {
				return 0, false, true
			}
			return float64(n) * factor, true, false
		}
		for _, q := range opticalQuantities {
			k := wideKey{entity: entity, index: index, quantity: q}
			get := func() *opticalSensor {
				s, ok := values[k]
				if !ok {
					s = &opticalSensor{entity: index, ifIndex: index, quantity: q}
					values[k] = s
					*order = append(*order, k)
				}
				return s
			}
			switch raw, ok, invalid := read(q); {
			case ok:
				s := get()
				v, light := wideUnitValue(q, t.Unit, raw)
				s.value, s.los, s.wide = v, !light, true
			case invalid:
				get().invalid = true
			}
			for _, kind := range opticalThresholdKinds {
				col, ok := t.ThresholdColumns[q+"_"+kind.suffix]
				if !ok {
					continue
				}
				raw, ok, _ := read(col)
				if !ok {
					continue
				}
				v, light := wideUnitValue(q, t.Unit, raw)
				if !light {
					continue
				}
				s := get()
				s.device = append(s.device, opticalThreshold{severity: kind.severity, relation: kind.relation, value: v})
			}
		}
	}
	return rows.Truncated, nil
}

// sortedColumns returns the logical columns of t in name order.
func sortedColumns(t *profiles.MonitorTable) []string {
	cols := make([]string, 0, len(t.Columns))
	for col := range t.Columns {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	return cols
}

// readWideThresholds walks a wide threshold table per sensor
// (ARISTA-ENTITY-SENSOR-MIB: low/high warning and critical per
// entPhysicalIndex of the sensor) and scales every threshold like its
// sensor. A cut walk drops the thresholds of the last entity read, as
// readThresholds does.
func readWideThresholds(s *snmp.Session, t *profiles.MonitorTable, sensors []rawSensor) (out map[int][]opticalThreshold, truncated bool, err error) {
	type column struct {
		oid, severity, relation string
	}
	var cols []column
	for _, kind := range opticalThresholdKinds {
		name, ok := t.ThresholdColumns[kind.suffix]
		if !ok {
			continue
		}
		oid, ok := t.ColumnOID(name)
		if !ok {
			continue
		}
		cols = append(cols, column{oid: oid, severity: kind.severity, relation: kind.relation})
	}
	if len(cols) == 0 {
		return nil, false, nil
	}
	walk := make([]string, 0, len(cols))
	for _, c := range cols {
		walk = append(walk, c.oid)
	}
	bySensor := map[int]*rawSensor{}
	for i := range sensors {
		bySensor[sensors[i].entity] = &sensors[i]
	}
	rows, err := s.WalkColumns(walk, opticalMaxSensors)
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
		for _, c := range cols {
			v, ok := row.Value(c.oid)
			if !ok {
				continue
			}
			n, isInt := pduInt(v)
			if !isInt || opticalInvalid(n) {
				continue
			}
			value, light := sensorUnitValue(sensor.kind, OpticalValue(n, sensor.exp, sensor.precision))
			if !light {
				continue
			}
			out[entity] = append(out[entity], opticalThreshold{severity: c.severity, relation: c.relation, value: value})
		}
	}
	if rows.Truncated {
		delete(out, last)
	}
	return out, rows.Truncated, nil
}

// opticalInvalid reports the raw value of a vendor table that stands for
// "no reading": 2147483647 (HUAWEI-ENTITY-EXTENT-MIB: the value is
// invalid) and its negative counterpart.
func opticalInvalid(raw int64) bool {
	return raw == math.MaxInt32 || raw == math.MinInt32
}
