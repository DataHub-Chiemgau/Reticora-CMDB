// Package snmpcustom is the probe type snmp_custom of the collector
// (MON-03, WP-997, E-183): freely configured OIDs per probe with kind,
// unit, scale and thresholds. It registers its executor in init(); the
// collector links the package in through collector/probe/all.
//
// Counters become rates per second over the previous-value store of the
// engine context (collector/probe/rate, WP-994, E-185); the executor holds
// no state.
package snmpcustom

import (
	"context"
	"encoding/json"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Type is the register key of the probe type.
const Type = "snmp_custom"

// Limits of a probe: metrics per probe and rows per column (MON-03).
const (
	MaxMetrics = 64
	MaxRows    = 256
)

// MaxSeries is the series limit of a probe's previous-value store
// (probe.SeriesBounder): every metric may be a counter column of MaxRows
// rows, plus the probe-wide series (sysUpTime).
func (e *Executor) MaxSeries() int { return MaxMetrics*MaxRows + 16 }

// Value names of an snmp_custom result. Every configured metric is the
// value probe_snmp_custom with the label metric (its name), unit when set
// and the index label of a column, so that the metric bridge stores the
// values under one declared name (WP-976).
const (
	ValueCustom    = "probe_snmp_custom"
	// ValueTruncated is 1 with the label metric for a metric whose values
	// are incomplete: its column walk stopped at MaxRows, the result ran
	// out of room for its values (probe.MaxResultValueBytes) or the
	// previous-value store refused its counter series.
	ValueTruncated = "probe_snmp_custom_truncated"
	LabelMetric    = "metric"
	LabelUnit      = "unit"
)

// Kinds and directions of a metric.
const (
	KindGauge      = "gauge"
	KindCounter    = "counter"
	DirectionAbove = "above"
	DirectionBelow = "below"
)

const oidSysUpTime = "1.3.6.1.2.1.1.3.0"

// metricName is the rule for the name of a metric.
var metricName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// indexLabel is the rule for the label of a column's rows.
var indexLabel = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// oidForm is an OID in dotted form.
var oidForm = regexp.MustCompile(`^[0-9]+(\.[0-9]+)+$`)

// now is the clock of the executor (tests replace it).
var now = time.Now

func init() { probe.Register(Type, &Executor{}) }

// Executor is the probe type snmp_custom (MON-03). A crit threshold
// violated is fail/threshold_exceeded, a warn threshold ok with
// threshold_exceeded (E-186, degraded through WP-952); an OID the agent
// does not answer is ok with no_such_object, the other metrics are still
// delivered.
type Executor struct{}

// Metric is one configured metric of a probe.
type Metric struct {
	Name       string   `json:"name"`
	OID        string   `json:"oid,omitempty"`
	Column     string   `json:"column,omitempty"`
	IndexLabel string   `json:"index_label,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Unit       string   `json:"unit,omitempty"`
	Scale      *float64 `json:"scale,omitempty"`
	Warn       *float64 `json:"warn,omitempty"`
	Crit       *float64 `json:"crit,omitempty"`
	Direction  string   `json:"direction,omitempty"`
}

// Params are the parameters of a probe.
type Params struct {
	Metrics []Metric `json:"metrics"`
}

// factor returns the scale of the metric (default 1).
func (m *Metric) factor() float64 {
	if m.Scale == nil {
		return 1
	}
	return *m.Scale
}

// Execute implements probe.Executor.
func (e *Executor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p Params
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return probe.ConfigResult("params", "parameters cannot be decoded")
		}
	}
	if field, msg := p.check(); field != "" {
		return probe.ConfigResult(field, msg)
	}
	sess, err := probe.OpenSNMP(ctx, &spec, creds)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	defer sess.Close() //nolint:errcheck // UDP socket, nothing to flush

	store := rate.FromContext(ctx)
	at := now()
	oids := []string{oidSysUpTime}
	for i := range p.Metrics {
		if p.Metrics[i].OID != "" {
			oids = append(oids, p.Metrics[i].OID)
		}
	}
	pdus, err := sess.Get(oids)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	scalars := make(map[string]gosnmp.SnmpPDU, len(pdus))
	for _, v := range pdus {
		scalars[strings.TrimPrefix(v.Name, ".")] = v
	}
	if up, ok := scalars[oidSysUpTime]; ok {
		// A restart of the agent is a discontinuity of every counter.
		if n, ok := probe.PDUInt(up); ok && n >= 0 {
			store.Uptime(uint64(n), at)
		}
	}

	ev := evaluation{values: map[string]float64{}, store: store}
	for i := range p.Metrics {
		m := &p.Metrics[i]
		if m.OID != "" {
			v, ok := scalars[m.OID]
			ev.observe(at, m, nil, v, ok)
			continue
		}
		rows, err := sess.WalkColumns([]string{m.Column}, MaxRows)
		if err != nil {
			return probe.SNMPFailure(err)
		}
		if rows.Truncated {
			ev.truncate(m)
		}
		if len(rows.Rows) == 0 {
			ev.raise(1, m.Name)
			continue
		}
		for _, row := range rows.Rows {
			v, ok := row.Value(m.Column)
			ev.observe(at, m, row.Index, v, ok)
		}
	}
	return ev.result()
}

// check validates the parameters like probewire.Validate (AK1); it
// returns the field and message of the first violation.
func (p *Params) check() (field, msg string) {
	if len(p.Metrics) > MaxMetrics {
		return "params.metrics", "at most 64 metrics"
	}
	seen := map[string]bool{}
	for i := range p.Metrics {
		m := &p.Metrics[i]
		prefix := "params.metrics[" + strconv.Itoa(i) + "]"
		m.OID = strings.TrimPrefix(strings.TrimSpace(m.OID), ".")
		m.Column = strings.TrimPrefix(strings.TrimSpace(m.Column), ".")
		switch {
		case !metricName.MatchString(m.Name):
			return prefix + ".name", "must match ^[a-z][a-z0-9_]{0,62}$"
		case seen[m.Name]:
			return prefix + ".name", "is used twice"
		case (m.OID == "") == (m.Column == ""):
			return prefix + ".oid", "set exactly one of oid and column"
		case m.OID != "" && !oidForm.MatchString(m.OID), m.Column != "" && !oidForm.MatchString(m.Column):
			return prefix + ".oid", "must be an OID in dotted form"
		case m.Scale != nil && (!(*m.Scale > 0) || math.IsInf(*m.Scale, 0)):
			return prefix + ".scale", "must be a finite number above 0"
		}
		seen[m.Name] = true
		if m.Kind == "" {
			m.Kind = KindGauge
		}
		if m.Direction == "" {
			m.Direction = DirectionAbove
		}
		if m.Column != "" && m.IndexLabel == "" {
			m.IndexLabel = "index"
		}
		if m.IndexLabel != "" && (!indexLabel.MatchString(m.IndexLabel) || m.IndexLabel == LabelMetric || m.IndexLabel == LabelUnit) {
			return prefix + ".index_label", "must match ^[a-z][a-z0-9_]{0,31}$ and not be metric or unit"
		}
	}
	return "", ""
}

// evaluation collects the values and the worst finding of a check: rank 3
// fail/threshold_exceeded, 2 warn threshold_exceeded, 1 warn
// no_such_object.
type evaluation struct {
	values map[string]float64
	rank   int
	metric string
	store  *rate.Store
	budget probe.ValueBudget
}

// truncate marks the values of the metric as incomplete; the key carries
// the metric, so the series stays stable whatever metric is worst.
func (ev *evaluation) truncate(m *Metric) {
	key := probewire.ValueKey(ValueTruncated, map[string]string{LabelMetric: m.Name})
	if _, ok := ev.values[key]; ok {
		return
	}
	// The marker itself is always admitted: it is small and says that
	// values are missing.
	ev.values[key] = 1
}

func (ev *evaluation) raise(rank int, metric string) {
	if rank > ev.rank {
		ev.rank, ev.metric = rank, metric
	}
}

// observe turns one answered variable into a value of the metric.
func (ev *evaluation) observe(at time.Time, m *Metric, index []uint32, v gosnmp.SnmpPDU, answered bool) { //nolint:gocritic // a PDU as gosnmp hands it out
	if !answered || missing(v) {
		ev.raise(1, m.Name)
		return
	}
	labels := map[string]string{LabelMetric: m.Name}
	if m.Unit != "" {
		labels[LabelUnit] = m.Unit
	}
	series := m.Name
	if index != nil {
		idx := indexString(index)
		labels[m.IndexLabel] = idx
		series += ":" + idx
	}
	var value float64
	if m.Kind == KindCounter {
		raw, bits, ok := counter(v)
		if !ok {
			ev.raise(1, m.Name)
			return
		}
		rejected := ev.store.Stats().Rejected
		r, ok := ev.store.Observe(series, raw, bits, at, 0)
		if !ok {
			if ev.store.Stats().Rejected > rejected {
				// The store is full: no rate now or later for this
				// series, which the result says.
				ev.truncate(m)
			}
			// Else the first observation, a wrap across a restart: no
			// rate yet.
			return
		}
		value = r * m.factor()
	} else {
		n, ok := number(v)
		if !ok {
			ev.raise(1, m.Name)
			return
		}
		value = n * m.factor()
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	if !ev.budget.Add(ev.values, map[string]float64{probewire.ValueKey(ValueCustom, labels): value}) {
		ev.truncate(m)
	}
	switch {
	case m.Crit != nil && beyond(value, *m.Crit, m.Direction):
		ev.raise(3, m.Name)
	case m.Warn != nil && beyond(value, *m.Warn, m.Direction):
		ev.raise(2, m.Name)
	}
}

func (ev *evaluation) result() probewire.Result {
	r := probewire.Result{Status: probewire.StatusOK, Values: ev.values}
	if ev.rank > 0 {
		r.Labels = map[string]string{LabelMetric: ev.metric}
	}
	switch ev.rank {
	case 3:
		r.Status, r.Code = probewire.StatusFail, probewire.WarnThresholdExceeded
	case 2:
		r.Code = probewire.WarnThresholdExceeded
	case 1:
		r.Code = probewire.WarnNoSuchObject
	}
	return r
}

// beyond reports whether value violates the threshold in the direction.
func beyond(value, threshold float64, direction string) bool {
	if direction == DirectionBelow {
		return value < threshold
	}
	return value > threshold
}

func missing(v gosnmp.SnmpPDU) bool { //nolint:gocritic // a PDU as gosnmp hands it out
	switch v.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		return true
	}
	return false
}

// number returns the numeric value of a variable: integers, counters,
// gauges, TimeTicks and Opaque float/double.
func number(v gosnmp.SnmpPDU) (float64, bool) { //nolint:gocritic // a PDU as gosnmp hands it out
	switch v.Type {
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Counter64, gosnmp.Uinteger32:
		f, _ := new(big.Float).SetInt(gosnmp.ToBigInt(v.Value)).Float64()
		return f, true
	case gosnmp.OpaqueFloat:
		if f, ok := v.Value.(float32); ok {
			return float64(f), true
		}
	case gosnmp.OpaqueDouble:
		if f, ok := v.Value.(float64); ok {
			return f, true
		}
	}
	return 0, false
}

// counter returns the raw value and width of a counter variable.
func counter(v gosnmp.SnmpPDU) (uint64, int, bool) { //nolint:gocritic // a PDU as gosnmp hands it out
	switch v.Type {
	case gosnmp.Counter64:
		return gosnmp.ToBigInt(v.Value).Uint64(), 64, true
	case gosnmp.Counter32, gosnmp.Gauge32, gosnmp.Uinteger32:
		return gosnmp.ToBigInt(v.Value).Uint64(), 32, true
	}
	return 0, 0, false
}

func indexString(index []uint32) string {
	parts := make([]string, len(index))
	for i, v := range index {
		parts[i] = strconv.FormatUint(uint64(v), 10)
	}
	return strings.Join(parts, ".")
}
