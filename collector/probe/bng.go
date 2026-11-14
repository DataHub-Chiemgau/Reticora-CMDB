package probe

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeBNG is the register key of the subscriber session probe (PRT-10,
// WP-1006).
const TypeBNG = "bng"

// BNGMaxInterfaces is the most interface rows a check reads (max_rows of
// the type); more truncate the walk (probe_bng_truncated = 1).
const BNGMaxInterfaces = 256

// bngMaxRows bounds the rows of the session tables (totals and per type).
const bngMaxRows = 1024

// Value names and labels of a bng result: probe_bng_sessions carries the
// label type (total, pppoe, ipoe) and, per interface, interface.
const (
	ValueBNGSessions  = "probe_bng_sessions"
	ValueBNGTruncated = "probe_bng_truncated"
	LabelBNGType      = "type"
	LabelBNGInterface = "interface"
)

// Session kinds of the logical model.
const (
	bngTotal = "total"
	bngPPPoE = "pppoe"
	bngIPoE  = "ipoe"
)

// Logical tables and columns of the monitor section bng (WP-1000):
//
//   - sessions: columns total, pppoe and ipoe (each optional), summed over
//     the rows (a scalar object walks as one row);
//   - by_type: column sessions per session type; the enum of the column
//     maps the last index sub-identifier (the session or client type) to
//     pppoe or ipoe; every row counts towards total, unless a row maps to
//     total (the row of all types), which then is the total. The index
//     parts before the type name an aggregation point (CISCO-SUBSCRIBER-
//     SESSION-MIB: point type and point, e.g. physical(1) per
//     entPhysicalIndex and interface(2) per ifIndex); only the first point
//     in walk order counts (physical before interface, the chassis with
//     the smallest entPhysicalIndex), never a sum across points. A table
//     without rows yields no by_type series;
//   - interfaces: columns total, pppoe, ipoe and optionally name per row
//     (at most BNGMaxInterfaces rows).
const (
	bngTableSessions   = "sessions"
	bngTableByType     = "by_type"
	bngTableInterfaces = "interfaces"
	bngColSessions     = "sessions"
	bngColName         = "name"
)

// Default drop ratios (PRT-10): 20 % degraded, 50 % down.
const (
	bngDefaultDropWarn = 0.2
	bngDefaultDropCrit = 0.5
)

// bngNow is the clock of the executor (tests replace it).
var bngNow = time.Now

func init() { Register(TypeBNG, &BNGExecutor{}) }

// BNGExecutor is the probe type bng (PRT-10, WP-1006): the active
// subscriber sessions of a BNG in total and per access type (PPPoE, IPoE)
// and optionally per interface. The source is only the monitor section
// bng of the matching profile (vendor MIBs; there is no standard MIB): a
// target without it is error/unsupported (unknown, never down). A drop
// against the previous value of a series (previous-value store, WP-994) of
// at least drop_warn_ratio is ok with threshold_exceeded (degraded through
// WP-952), of at least drop_crit_ratio or a total below min_sessions
// fail/threshold_exceeded. The first run without a previous value rates
// only min_sessions.
type BNGExecutor struct{}

type bngParams struct {
	DropWarnRatio *float64 `json:"drop_warn_ratio"`
	DropCritRatio *float64 `json:"drop_crit_ratio"`
	MinSessions   *int64   `json:"min_sessions"`
}

// bngSeries is one counted series: a session kind, optionally of an
// interface.
type bngSeries struct {
	kind  string
	iface string
	value float64
}

// Execute implements Executor.
func (e *BNGExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p bngParams
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "parameters cannot be decoded")
		}
	}
	warn, crit := bngDefaultDropWarn, bngDefaultDropCrit
	if p.DropWarnRatio != nil {
		warn = *p.DropWarnRatio
	}
	if p.DropCritRatio != nil {
		crit = *p.DropCritRatio
	}
	if warn < 0 || warn > 1 || crit < 0 || crit > 1 || warn > crit {
		return configResult("params.drop_warn_ratio", "drop ratios lie between 0 and 1, warn not above crit")
	}

	sess, err := openSNMP(ctx, &spec, creds)
	if err != nil {
		return snmpFailure(err)
	}
	defer sess.Close() //nolint:errcheck // UDP socket, nothing to flush
	monitor, matched, err := snmpMonitor(sess, TypeBNG)
	if err != nil {
		return snmpFailure(err)
	}
	if !matched {
		return failResult(probewire.StatusError, probewire.ReasonUnsupported)
	}
	series, truncated, found, err := readBNG(sess, &monitor)
	if errors.Is(err, errBNGIncomplete) {
		// A partial sum is no measurement: rating it would look like a
		// drop.
		return failResult(probewire.StatusError, probewire.ReasonResponseTooLarge)
	}
	if err != nil {
		return snmpFailure(err)
	}
	if !found {
		// The profile names tables the target does not answer.
		return failResult(probewire.StatusError, probewire.ReasonUnsupported)
	}
	return bngResult(rate.FromContext(ctx), bngNow(), series, truncated, warn, crit, p.MinSessions)
}

// errBNGIncomplete is a session table cut at bngMaxRows before the rows
// that are summed were complete.
var errBNGIncomplete = errors.New("bng: session table cut at the row limit")

// readBNG reads the tables of the monitor section; found is false when no
// table yielded a row.
func readBNG(s *snmp.Session, m *profiles.MonitorSpec) (series []bngSeries, truncated, found bool, err error) {
	sums := map[string]float64{}
	has := map[string]bool{}
	if t, ok := m.Tables[bngTableSessions]; ok {
		cols, walk := monitorColumns(&t, bngTotal, bngPPPoE, bngIPoE)
		if len(walk) > 0 {
			rows, err := s.WalkColumns(walk, bngMaxRows)
			if err != nil {
				return nil, false, false, err
			}
			if rows.Truncated {
				return nil, false, false, errBNGIncomplete
			}
			for _, row := range rows.Rows {
				for kind, oid := range cols {
					if v, ok := row.Value(oid); ok {
						if n, ok := pduInt(v); ok {
							sums[kind] += float64(n)
							has[kind], found = true, true
						}
					}
				}
			}
		}
	}
	if t, ok := m.Tables[bngTableByType]; ok {
		if oid, ok := t.ColumnOID(bngColSessions); ok && oid != "" {
			rows, err := s.WalkColumns([]string{oid}, bngMaxRows)
			if err != nil {
				return nil, false, false, err
			}
			hasAll, byTypeRows := false, 0
			point, pointDone := "", false
			for _, row := range rows.Rows {
				v, ok := row.Value(oid)
				if !ok || len(row.Index) == 0 {
					continue
				}
				n, ok := pduInt(v)
				if !ok {
					continue
				}
				// Only the first aggregation point counts.
				p := indexString(row.Index[:len(row.Index)-1])
				if byTypeRows == 0 {
					point = p
				} else if p != point {
					pointDone = true
					continue
				}
				byTypeRows++
				found = true
				kind, _ := t.MapEnum(bngColSessions, int64(row.Index[len(row.Index)-1]))
				switch kind {
				case bngTotal:
					// A row of all types (CISCO-SUBSCRIBER-SESSION-TC
					// all(1)) is the total itself.
					sums["by_type:all"] += float64(n)
					hasAll = true
					continue
				case bngPPPoE, bngIPoE:
					sums["by_type:"+kind] += float64(n)
				}
				sums["by_type:"+bngTotal] += float64(n)
			}
			if rows.Truncated && !pointDone {
				// The walk ended inside the first point: its rows may be
				// incomplete.
				return nil, false, false, errBNGIncomplete
			}
			if hasAll {
				sums["by_type:"+bngTotal] = sums["by_type:all"]
			}
			// An empty table (older firmware, briefly after a restart of
			// subscriber management) measured nothing: no series from it.
			for _, kind := range []string{bngTotal, bngPPPoE, bngIPoE} {
				if !has[kind] && byTypeRows > 0 {
					sums[kind], has[kind] = sums["by_type:"+kind], true
				}
			}
		}
	}
	if found && !has[bngTotal] && (has[bngPPPoE] || has[bngIPoE]) {
		sums[bngTotal], has[bngTotal] = sums[bngPPPoE]+sums[bngIPoE], true
	}
	for _, kind := range []string{bngTotal, bngPPPoE, bngIPoE} {
		if has[kind] {
			series = append(series, bngSeries{kind: kind, value: sums[kind]})
		}
	}
	if t, ok := m.Tables[bngTableInterfaces]; ok {
		cols, walk := monitorColumns(&t, bngTotal, bngPPPoE, bngIPoE, bngColName)
		if len(walk) > 0 {
			rows, err := s.WalkColumns(walk, BNGMaxInterfaces)
			if err != nil {
				return nil, false, false, err
			}
			truncated = rows.Truncated
			for _, row := range rows.Rows {
				name := indexString(row.Index)
				if v, ok := row.Value(cols[bngColName]); ok && cols[bngColName] != "" {
					if s := strings.TrimSpace(pduString(v)); s != "" {
						name = s
					}
				}
				for _, kind := range []string{bngTotal, bngPPPoE, bngIPoE} {
					oid := cols[kind]
					if oid == "" {
						continue
					}
					if v, ok := row.Value(oid); ok {
						if n, ok := pduInt(v); ok {
							series = append(series, bngSeries{kind: kind, iface: name, value: float64(n)})
							found = true
						}
					}
				}
			}
		}
	}
	return series, truncated, found, nil
}

// monitorColumns returns the OIDs of the logical columns the table has and the
// list to walk.
func monitorColumns(t *profiles.MonitorTable, names ...string) (cols map[string]string, walk []string) {
	cols = map[string]string{}
	for _, name := range names {
		if oid, ok := t.ColumnOID(name); ok && oid != "" {
			cols[name] = oid
			walk = append(walk, oid)
		}
	}
	return cols, walk
}

// bngResult rates the series against their previous values (AK3, AK5).
func bngResult(store *rate.Store, at time.Time, series []bngSeries, truncated bool, warn, crit float64, minSessions *int64) probewire.Result {
	values := map[string]float64{}
	rank, worst := 0, bngSeries{}
	raise := func(r int, s bngSeries) {
		if r > rank || (r == rank && r > 0 && s.iface < worst.iface) {
			rank, worst = r, s
		}
	}
	sort.SliceStable(series, func(i, j int) bool { return series[i].iface < series[j].iface })
	for _, s := range series {
		labels := map[string]string{LabelBNGType: s.kind}
		key := "bng:" + s.kind
		if s.iface != "" {
			labels[LabelBNGInterface] = s.iface
			key += ":if:" + s.iface
		}
		values[probewire.ValueKey(ValueBNGSessions, labels)] = s.value
		prev, _, ok := store.Swap(key, rate.Num(s.value), at)
		if ok && !prev.IsEnum() && prev.Number > 0 {
			drop := (prev.Number - s.value) / prev.Number
			switch {
			case drop >= crit && crit > 0:
				raise(2, s)
			case drop >= warn && warn > 0:
				raise(1, s)
			}
		}
		if s.kind == bngTotal && s.iface == "" && minSessions != nil && s.value < float64(*minSessions) {
			raise(2, s)
		}
	}
	if truncated {
		values[ValueBNGTruncated] = 1
	}
	r := probewire.Result{Status: probewire.StatusOK, Values: values}
	if rank > 0 {
		r.Code = probewire.WarnThresholdExceeded
		r.Labels = map[string]string{LabelBNGType: worst.kind}
		if worst.iface != "" {
			r.Labels[LabelBNGInterface] = worst.iface
		}
	}
	if rank == 2 {
		r.Status = probewire.StatusFail
	}
	return r
}
