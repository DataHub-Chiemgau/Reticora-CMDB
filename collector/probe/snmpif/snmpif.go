// Package snmpif is the probe type snmp_if of the collector (PRT-06,
// MON-03, MON-04, WP-996, E-183): the interfaces of a target with a
// selection, status, rates, utilization, errors and discards. It registers
// its executor in init(); the collector links the package in through
// collector/probe/all.
//
// Rates come from the previous-value store of the engine context
// (collector/probe/rate, WP-994, E-185): the executor holds no state.
package snmpif

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Type is the register key of the probe type.
const Type = "snmp_if"

// Row limits of a check (max_interfaces): default and upper bound.
const (
	DefaultMaxInterfaces = 256
	MaxInterfaces        = 1024
)

// Value names of an snmp_if result; the per-interface values carry the
// labels ifIndex and ifName (probewire.ValueKey).
const (
	ValueOperStatus  = "probe_if_oper_status"
	ValueAdminStatus = "probe_if_admin_status"
	ValueInBps       = "probe_if_in_bps"
	ValueOutBps      = "probe_if_out_bps"
	ValueUtil        = "probe_if_util_ratio"
	ValueErrors      = "probe_if_errors_per_s"
	ValueDiscards    = "probe_if_discards_per_s"
	// ValueTruncated is 1 when the walk stopped at max_interfaces or when
	// the values of further interfaces did not fit into one result
	// (probe.MaxResultValueBytes); the state is still rated over every
	// row read.
	ValueTruncated = "probe_if_truncated"
	// ValueInterfaces is the number of selected interfaces.
	ValueInterfaces = "probe_if_interfaces"
	// ValueCriticalUnseen counts the critical interfaces behind a
	// truncated walk (no statement about them).
	ValueCriticalUnseen = "probe_if_critical_unseen"

	LabelIfIndex = "ifIndex"
	LabelIfName  = "ifName"
)

// IF-MIB columns (RFC 2863): ifTable 1.3.6.1.2.1.2.2.1, ifXTable
// 1.3.6.1.2.1.31.1.1.1.
const (
	oidIfSpeed       = "1.3.6.1.2.1.2.2.1.5"
	oidIfAdminStatus = "1.3.6.1.2.1.2.2.1.7"
	oidIfOperStatus  = "1.3.6.1.2.1.2.2.1.8"
	oidIfInOctets    = "1.3.6.1.2.1.2.2.1.10"
	oidIfInDiscards  = "1.3.6.1.2.1.2.2.1.13"
	oidIfInErrors    = "1.3.6.1.2.1.2.2.1.14"
	oidIfOutOctets   = "1.3.6.1.2.1.2.2.1.16"
	oidIfOutDiscards = "1.3.6.1.2.1.2.2.1.19"
	oidIfOutErrors   = "1.3.6.1.2.1.2.2.1.20"
	oidIfName        = "1.3.6.1.2.1.31.1.1.1.1"
	oidIfHCInOctets  = "1.3.6.1.2.1.31.1.1.1.6"
	oidIfHCOutOctets = "1.3.6.1.2.1.31.1.1.1.10"
	oidIfHighSpeed   = "1.3.6.1.2.1.31.1.1.1.15"
	oidIfAlias       = "1.3.6.1.2.1.31.1.1.1.18"
	oidIfDiscontTime = "1.3.6.1.2.1.31.1.1.1.19"
	oidSysUpTime     = "1.3.6.1.2.1.1.3.0"
)

// columns are the twelve columns of a check (max_rows 1024 × 12); the
// 32-bit octet counters and ifSpeed are read only for rows without
// ifXTable counters.
var columns = []string{
	oidIfOperStatus, oidIfAdminStatus, oidIfName, oidIfAlias, oidIfHighSpeed,
	oidIfHCInOctets, oidIfHCOutOctets, oidIfInErrors, oidIfOutErrors,
	oidIfInDiscards, oidIfOutDiscards, oidIfDiscontTime,
}

// fallbackColumns are read when a row lacks the 64-bit counters.
var fallbackColumns = []string{oidIfInOctets, oidIfOutOctets, oidIfSpeed}

// ifOperStatus and ifAdminStatus values.
const (
	statusUp = 1
	// operDown are the oper states that count as down: down(2),
	// unknown(4), notPresent(6) and lowerLayerDown(7); testing(3) and
	// dormant(5) do not.
	operDownMask = 1<<2 | 1<<4 | 1<<6 | 1<<7
)

// plausibility is the factor of ifHighSpeed above which a rate is dropped
// as implausible (WP-994 AK4).
const plausibility = 1.1

// now is the clock of the executor (tests replace it).
var now = time.Now

func init() { probe.Register(Type, &Executor{}) }

// Executor is the probe type snmp_if (PRT-06). A critical interface
// (critical[], by ifName) admin up and oper down is fail/check_failed;
// another selected interface admin up and oper down is ok with the warn
// code check_failed. A utilization above util_crit is fail, above
// util_warn ok with threshold_exceeded, errors above errors_warn ok with
// threshold_exceeded (E-186). Admin-down interfaces go into no rating.
type Executor struct{}

type params struct {
	NameRegex     string   `json:"name_regex"`
	AliasRegex    string   `json:"alias_regex"`
	IfIndex       []int64  `json:"if_index"`
	OnlyAdminUp   *bool    `json:"only_admin_up"`
	MaxInterfaces int      `json:"max_interfaces"`
	Critical      []string `json:"critical"`
	UtilWarn      *float64 `json:"util_warn"`
	UtilCrit      *float64 `json:"util_crit"`
	ErrorsWarn    *float64 `json:"errors_warn"`
	nameRE        *regexp.Regexp
	aliasRE       *regexp.Regexp
	indexes       map[uint32]bool
	critical      map[string]bool
	adminUpOnly   bool
	maxRows       int
	valueLimit    int // budget of the values (0: probe.MaxResultValueBytes; tests)
}

// iface is one interface row.
type iface struct {
	index      uint32
	name       string
	alias      string
	oper       int64
	admin      int64
	speedMbps  float64
	in, out    uint64
	bits       int
	hasOctets  bool
	errIn      uint64
	errOut     uint64
	discIn     uint64
	discOut    uint64
	hasErrors  bool
	hasDiscard bool
	discont    uint64
}

// Execute implements probe.Executor.
func (e *Executor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	p, bad := decode(spec.Params, spec.Thresholds)
	if bad != nil {
		return *bad
	}
	sess, err := probe.OpenSNMP(ctx, &spec, creds)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	defer sess.Close() //nolint:errcheck // UDP socket, nothing to flush

	store := rate.FromContext(ctx)
	at := now()
	// sysUpTime: a restart of the agent is a discontinuity of every
	// counter of the probe (WP-994 AK1).
	up, err := sess.Get([]string{oidSysUpTime})
	if err != nil {
		return probe.SNMPFailure(err)
	}
	for _, v := range up {
		if n, ok := probe.PDUInt(v); ok && n >= 0 && strings.TrimPrefix(v.Name, ".") == oidSysUpTime {
			store.Uptime(uint64(n), at)
		}
	}
	rows, err := sess.WalkColumns(columns, p.maxRows)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	ifaces := parse(rows.Rows)
	if needFallback(ifaces) {
		fb, err := sess.WalkColumns(fallbackColumns, p.maxRows)
		if err != nil {
			return probe.SNMPFailure(err)
		}
		merge(ifaces, fb.Rows)
	}
	return evaluate(store, at, ifaces, &p, rows.Truncated)
}

// decode reads and checks the parameters; thresholds (util_warn,
// util_crit, errors_warn) override params (E-186).
func decode(raw, thresholds json.RawMessage) (params, *probewire.Result) {
	var p params
	fail := func(field, msg string) (params, *probewire.Result) {
		r := probe.ConfigResult(field, msg)
		return p, &r
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return fail("params", "parameters cannot be decoded")
		}
	}
	if len(thresholds) > 0 {
		var t struct {
			UtilWarn   *float64 `json:"util_warn"`
			UtilCrit   *float64 `json:"util_crit"`
			ErrorsWarn *float64 `json:"errors_warn"`
		}
		if err := json.Unmarshal(thresholds, &t); err != nil {
			return fail("thresholds", "thresholds cannot be decoded")
		}
		if t.UtilWarn != nil {
			p.UtilWarn = t.UtilWarn
		}
		if t.UtilCrit != nil {
			p.UtilCrit = t.UtilCrit
		}
		if t.ErrorsWarn != nil {
			p.ErrorsWarn = t.ErrorsWarn
		}
	}
	var err error
	if p.NameRegex != "" {
		if p.nameRE, err = regexp.Compile(p.NameRegex); err != nil {
			return fail("params.name_regex", "must be a valid RE2 regular expression")
		}
	}
	if p.AliasRegex != "" {
		if p.aliasRE, err = regexp.Compile(p.AliasRegex); err != nil {
			return fail("params.alias_regex", "must be a valid RE2 regular expression")
		}
	}
	switch {
	case p.MaxInterfaces == 0:
		p.maxRows = DefaultMaxInterfaces
	case p.MaxInterfaces < 0 || p.MaxInterfaces > MaxInterfaces:
		return fail("params.max_interfaces", "must be between 1 and 1024")
	default:
		p.maxRows = p.MaxInterfaces
	}
	p.adminUpOnly = p.OnlyAdminUp == nil || *p.OnlyAdminUp
	if len(p.IfIndex) > 0 {
		p.indexes = map[uint32]bool{}
		for _, i := range p.IfIndex {
			if i >= 1 && i <= 1<<31-1 {
				p.indexes[uint32(i)] = true
			}
		}
	}
	p.critical = map[string]bool{}
	for _, name := range p.Critical {
		if name = strings.TrimSpace(name); name != "" {
			p.critical[name] = true
		}
	}
	return p, nil
}

// parse builds the interfaces from the walked rows (index ifIndex).
func parse(rows []snmp.Row) []*iface {
	out := make([]*iface, 0, len(rows))
	for _, row := range rows {
		if len(row.Index) != 1 {
			continue
		}
		f := &iface{index: row.Index[0], bits: 64}
		f.oper, _ = intValue(row, oidIfOperStatus)
		f.admin, _ = intValue(row, oidIfAdminStatus)
		f.name = textValue(row, oidIfName)
		if f.name == "" {
			f.name = strconv.FormatUint(uint64(f.index), 10)
		}
		f.alias = textValue(row, oidIfAlias)
		if hs, ok := intValue(row, oidIfHighSpeed); ok {
			f.speedMbps = float64(hs)
		}
		in, okIn := uintValue(row, oidIfHCInOctets)
		outOct, okOut := uintValue(row, oidIfHCOutOctets)
		if okIn && okOut {
			f.in, f.out, f.hasOctets = in, outOct, true
		}
		var ok1, ok2 bool
		f.errIn, ok1 = uintValue(row, oidIfInErrors)
		f.errOut, ok2 = uintValue(row, oidIfOutErrors)
		f.hasErrors = ok1 && ok2
		f.discIn, ok1 = uintValue(row, oidIfInDiscards)
		f.discOut, ok2 = uintValue(row, oidIfOutDiscards)
		f.hasDiscard = ok1 && ok2
		f.discont, _ = uintValue(row, oidIfDiscontTime)
		out = append(out, f)
	}
	return out
}

func needFallback(ifaces []*iface) bool {
	for _, f := range ifaces {
		if !f.hasOctets || f.speedMbps == 0 {
			return true
		}
	}
	return false
}

// merge adds the 32-bit octet counters and ifSpeed to the rows without
// ifXTable values.
func merge(ifaces []*iface, rows []snmp.Row) {
	byIndex := map[uint32]snmp.Row{}
	for _, row := range rows {
		if len(row.Index) == 1 {
			byIndex[row.Index[0]] = row
		}
	}
	for _, f := range ifaces {
		row, ok := byIndex[f.index]
		if !ok {
			continue
		}
		if !f.hasOctets {
			in, okIn := uintValue(row, oidIfInOctets)
			out, okOut := uintValue(row, oidIfOutOctets)
			if okIn && okOut {
				f.in, f.out, f.bits, f.hasOctets = in, out, 32, true
			}
		}
		if f.speedMbps == 0 {
			if s, ok := uintValue(row, oidIfSpeed); ok {
				f.speedMbps = float64(s) / 1e6
			}
		}
	}
}

// selected reports whether the selection takes the interface (AK2): every
// given filter must match; a critical interface is always taken.
func (p *params) selected(f *iface) bool {
	if p.critical[f.name] {
		return true
	}
	switch {
	case p.indexes != nil && !p.indexes[f.index],
		p.nameRE != nil && !p.nameRE.MatchString(f.name),
		p.aliasRE != nil && !p.aliasRE.MatchString(f.alias),
		p.adminUpOnly && f.admin != statusUp:
		return false
	}
	return true
}

func operDown(oper int64) bool { return oper >= 0 && oper < 64 && operDownMask&(1<<oper) != 0 }

// finding is the worst finding so far: rank 4 fail/check_failed, 3
// fail/threshold_exceeded, 2 warn check_failed, 1 warn
// threshold_exceeded.
type finding struct {
	rank int
	name string
}

func (f *finding) raise(rank int, name string) {
	if rank > f.rank || (rank == f.rank && name < f.name) {
		f.rank, f.name = rank, name
	}
}

// evaluate computes the values and the state of the selected interfaces.
func evaluate(store *rate.Store, at time.Time, ifaces []*iface, p *params, truncated bool) probewire.Result {
	values := map[string]float64{}
	var worst finding
	var budget probe.ValueBudget
	if p.valueLimit > 0 {
		budget.Limit = p.valueLimit
	}
	dropped := false
	seen := map[string]bool{}
	n := 0
	for _, f := range ifaces {
		if !p.selected(f) {
			continue
		}
		n++
		seen[f.name] = true
		labels := map[string]string{LabelIfIndex: strconv.FormatUint(uint64(f.index), 10), LabelIfName: f.name}
		key := func(name string) string { return probewire.ValueKey(name, labels) }
		// The values of the interface (row); they enter the result only while
		// the budget of one uplink message lasts (rates are observed
		// either way, so the previous values stay current).
		row := map[string]float64{}
		row[key(ValueOperStatus)] = float64(f.oper)
		row[key(ValueAdminStatus)] = float64(f.admin)
		series := "if:" + labels[LabelIfIndex] + ":"
		maxOctets := 0.0
		if f.speedMbps > 0 {
			maxOctets = f.speedMbps * 1e6 * plausibility / 8
		}
		var inBps, outBps float64
		var okIn, okOut bool
		if f.hasOctets {
			var r float64
			if r, okIn = store.ObserveMax(series+"in", f.in, f.bits, at, f.discont, maxOctets); okIn {
				inBps = r * 8
				row[key(ValueInBps)] = inBps
			}
			if r, okOut = store.ObserveMax(series+"out", f.out, f.bits, at, f.discont, maxOctets); okOut {
				outBps = r * 8
				row[key(ValueOutBps)] = outBps
			}
		}
		util, hasUtil := 0.0, false
		if (okIn || okOut) && f.speedMbps > 0 {
			util, hasUtil = max(inBps, outBps)/(f.speedMbps*1e6), true
			row[key(ValueUtil)] = util
		}
		errRate, hasErrRate := pairRate(store, series+"err", f.errIn, f.errOut, f.hasErrors, at, f.discont)
		if hasErrRate {
			row[key(ValueErrors)] = errRate
		}
		if r, ok := pairRate(store, series+"disc", f.discIn, f.discOut, f.hasDiscard, at, f.discont); ok {
			row[key(ValueDiscards)] = r
		}
		if !budget.Add(values, row) {
			dropped = true
		}

		// Admin-down interfaces go into no rating.
		if f.admin != statusUp {
			continue
		}
		if operDown(f.oper) {
			if p.critical[f.name] {
				worst.raise(4, f.name)
			} else {
				worst.raise(2, f.name)
			}
			continue
		}
		switch {
		case hasUtil && p.UtilCrit != nil && util >= *p.UtilCrit:
			worst.raise(3, f.name)
		case hasUtil && p.UtilWarn != nil && util >= *p.UtilWarn:
			worst.raise(1, f.name)
		}
		if hasErrRate && p.ErrorsWarn != nil && errRate >= *p.ErrorsWarn {
			worst.raise(1, f.name)
		}
	}
	values[ValueInterfaces] = float64(n)
	if truncated || dropped {
		values[ValueTruncated] = 1
	}
	// Critical interfaces outside the rows read: behind a truncated walk
	// no statement (ok with check_failed), else missing (fail).
	var unseen []string
	for name := range p.critical {
		if !seen[name] {
			unseen = append(unseen, name)
		}
	}
	sort.Strings(unseen)
	if len(unseen) > 0 {
		if truncated {
			values[ValueCriticalUnseen] = float64(len(unseen))
			worst.raise(2, unseen[0])
		} else {
			worst.raise(4, unseen[0])
		}
	}
	r := probewire.Result{Status: probewire.StatusOK, Values: values}
	if worst.rank > 0 {
		r.Labels = map[string]string{LabelIfName: worst.name}
	}
	switch worst.rank {
	case 4:
		r.Status, r.Code = probewire.StatusFail, probewire.WarnCheckFailed
	case 3:
		r.Status, r.Code = probewire.StatusFail, probewire.WarnThresholdExceeded
	case 2:
		r.Code = probewire.WarnCheckFailed
	case 1:
		r.Code = probewire.WarnThresholdExceeded
	}
	return r
}

// pairRate returns the summed rate per second of an in/out pair of
// Counter32 values.
func pairRate(store *rate.Store, series string, in, out uint64, has bool, at time.Time, discont uint64) (float64, bool) {
	if !has {
		return 0, false
	}
	a, okA := store.Observe(series+":in", in, 32, at, discont)
	b, okB := store.Observe(series+":out", out, 32, at, discont)
	if !okA || !okB {
		return 0, false
	}
	return a + b, true
}

func intValue(row snmp.Row, column string) (int64, bool) {
	v, ok := row.Value(column)
	if !ok {
		return 0, false
	}
	return probe.PDUInt(v)
}

func uintValue(row snmp.Row, column string) (uint64, bool) {
	v, ok := row.Value(column)
	if !ok {
		return 0, false
	}
	switch v.Type {
	case gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Counter64, gosnmp.Uinteger32, gosnmp.Integer:
		b := gosnmp.ToBigInt(v.Value)
		if b.Sign() < 0 {
			return 0, false
		}
		return b.Uint64(), true
	}
	return 0, false
}

func textValue(row snmp.Row, column string) string {
	v, ok := row.Value(column)
	if !ok {
		return ""
	}
	return strings.TrimSpace(string(probe.PDUBytes(v)))
}
