package routing

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeISIS is the register key of the IS-IS adjacency probe.
const TypeISIS = "isis"

// Value names of an isis result; the per-adjacency values carry the
// labels system_id and level (probewire.ValueKey).
const (
	ValueISISAdjacencyState = "probe_isis_adjacency_state"
	ValueISISAdjacencies    = "probe_isis_adjacencies"
	ValueISISAdjacenciesUp  = "probe_isis_adjacencies_up"
	ValueISISTruncated      = "probe_isis_truncated"
	// ValueISISAdjacenciesUnseen counts the expected adjacencies behind a
	// truncated walk (no statement about them).
	ValueISISAdjacenciesUnseen = "probe_isis_adjacencies_unseen"
	LabelSystemID              = "system_id"
	LabelLevel                 = "level"
)

// Levels of an adjacency (label level): isisISAdjUsage level1(1),
// level2(2), level1and2(3).
const (
	LevelL1   = "L1"
	LevelL2   = "L2"
	LevelL1L2 = "L1L2"
)

// Logical table and columns of the monitor section isis (WP-1000).
const (
	isisTable    = "adjacencies"
	isisColState = "state"
	isisColSysID = "sys_id"
	isisColUsage = "usage"
)

// isisCore is isisISAdjTable of ISIS-MIB (RFC 4444, isisISAdjEntry
// 1.3.6.1.2.1.138.1.6.1.1): isisISAdjState (2: down(1), initializing(2),
// up(3), failed(4)), isisISAdjNeighSysID (6, six octets) and
// isisISAdjUsage (8), index isisCircIndex + isisISAdjIndex.
var isisCore = profiles.MonitorTable{
	Columns: map[string]string{
		isisColState: "1.3.6.1.2.1.138.1.6.1.1.2",
		isisColSysID: "1.3.6.1.2.1.138.1.6.1.1.6",
		isisColUsage: "1.3.6.1.2.1.138.1.6.1.1.8",
	},
	Index: "composite:[int,int]",
	Enums: map[string]map[string]string{
		isisColState: {"1": "down", "2": "initializing", "3": mappedStateUp, "4": "failed"},
	},
}

// ISISExecutor is the probe type isis (PRT-07, WP-1002): the IS-IS
// adjacencies of the target. An expected adjacency (system ID, optionally
// the level) that is missing or not up is fail/check_failed with the label
// system_id; fewer adjacencies up than min_adjacencies is ok with the warn
// code check_failed (degraded). More rows than max_adjacencies truncate the
// walk (probe_isis_truncated = 1); an adjacency beyond it is no statement.
type ISISExecutor struct{}

type isisExpected struct {
	SystemID string `json:"system_id"`
	Level    int    `json:"level"`
}

type isisParams struct {
	Adjacencies    []isisExpected `json:"adjacencies"`
	MinAdjacencies int            `json:"min_adjacencies"`
	MaxAdjacencies int            `json:"max_adjacencies"`
}

// isisAdjacency is one row of the adjacency table.
type isisAdjacency struct {
	systemID string
	level    string
	stateRaw int64
	up       bool
}

// NormalizeSystemID returns the IS-IS system ID in the form
// 1921.6800.1001 (six octets as three groups of four lowercase hex
// digits); the input may separate the digits with '.', ':' or '-'.
func NormalizeSystemID(s string) (string, bool) {
	digits := strings.NewReplacer(".", "", ":", "", "-", "").Replace(strings.TrimSpace(s))
	raw, err := hex.DecodeString(digits)
	if err != nil || len(raw) != 6 {
		return "", false
	}
	return formatSystemID(raw), true
}

func formatSystemID(raw []byte) string {
	h := hex.EncodeToString(raw)
	return h[0:4] + "." + h[4:8] + "." + h[8:12]
}

// Execute implements probe.Executor.
func (e *ISISExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p isisParams
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return probe.ConfigResult("params", "parameters cannot be decoded")
		}
	}
	for i := range p.Adjacencies {
		id, ok := NormalizeSystemID(p.Adjacencies[i].SystemID)
		if !ok {
			return probe.ConfigResult("params.adjacencies["+strconv.Itoa(i)+"].system_id", "must be a system ID of six octets (e.g. 1921.6800.1001)")
		}
		p.Adjacencies[i].SystemID = id
	}

	sess, err := probe.OpenSNMP(ctx, &spec, creds)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	defer sess.Close() //nolint:errcheck // UDP socket, nothing to flush
	monitor, matched, err := monitorFor(sess, TypeISIS)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	tables := []profiles.MonitorTable{isisCore}
	if t, ok := monitor.Tables[isisTable]; ok && matched {
		tables = []profiles.MonitorTable{t, isisCore}
	}
	var rows []isisAdjacency
	truncated := false
	for i := range tables {
		rows, truncated, err = readISIS(sess, &tables[i], rowLimit(p.MaxAdjacencies))
		if err != nil {
			return probe.SNMPFailure(err)
		}
		if len(rows) > 0 {
			break
		}
	}
	return isisResult(rows, &p, truncated)
}

// readISIS walks the adjacency table (at most maxRows rows).
func readISIS(s *snmp.Session, t *profiles.MonitorTable, maxRows int) ([]isisAdjacency, bool, error) {
	cols, walk := columns(t, isisColState, isisColSysID, isisColUsage)
	if cols[isisColState] == "" {
		return nil, false, nil
	}
	rows, err := s.WalkColumns(walk, maxRows)
	if err != nil {
		return nil, false, err
	}
	out := make([]isisAdjacency, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		v, ok := row.Value(cols[isisColState])
		if !ok {
			continue
		}
		raw, ok := probe.PDUInt(v)
		if !ok {
			continue
		}
		a := isisAdjacency{stateRaw: raw}
		state, _ := t.MapEnum(isisColState, raw)
		a.up = state == mappedStateUp
		if sv, ok := row.Value(cols[isisColSysID]); ok {
			if b := probe.PDUBytes(sv); len(b) == 6 {
				a.systemID = formatSystemID(b)
			}
		}
		if a.systemID == "" {
			a.systemID = "index:" + indexString(row.Index)
		}
		if uv, ok := row.Value(cols[isisColUsage]); ok {
			if u, ok := probe.PDUInt(uv); ok {
				a.level = levelName(u)
			}
		}
		out = append(out, a)
	}
	return out, rows.Truncated, nil
}

// levelName maps isisISAdjUsage.
func levelName(usage int64) string {
	switch usage {
	case 1:
		return LevelL1
	case 2:
		return LevelL2
	case 3:
		return LevelL1L2
	}
	return "unknown"
}

// servesLevel reports whether an adjacency of level serves the expected
// level (0: any).
func servesLevel(level string, want int) bool {
	switch want {
	case 1:
		return level == LevelL1 || level == LevelL1L2
	case 2:
		return level == LevelL2 || level == LevelL1L2
	}
	return true
}

// isisResult evaluates the adjacencies (WP-1002 AK3, AK4).
func isisResult(rows []isisAdjacency, p *isisParams, truncated bool) probewire.Result {
	type key struct{ systemID, level string }
	worst := map[key]*isisAdjacency{}
	for i := range rows {
		a := &rows[i]
		k := key{a.systemID, a.level}
		if w, ok := worst[k]; !ok || (w.up && !a.up) || (w.up == a.up && a.stateRaw < w.stateRaw) {
			worst[k] = a
		}
	}
	values := map[string]float64{}
	up := 0
	for k, a := range worst {
		values[probewire.ValueKey(ValueISISAdjacencyState, map[string]string{LabelSystemID: k.systemID, LabelLevel: k.level})] = float64(a.stateRaw)
		if a.up {
			up++
		}
	}
	values[ValueISISAdjacencies] = float64(len(worst))
	values[ValueISISAdjacenciesUp] = float64(up)

	var failing []string
	evaluated, unseen := 0, 0
	for _, exp := range p.Adjacencies {
		found, ok := false, true
		for k, a := range worst {
			if k.systemID == exp.SystemID && servesLevel(k.level, exp.Level) {
				found = true
				ok = ok && a.up
			}
		}
		switch {
		case found:
			evaluated++
			if !ok {
				failing = append(failing, exp.SystemID)
			}
		case !truncated:
			evaluated++
			failing = append(failing, exp.SystemID)
		default:
			unseen++
		}
	}
	if truncated {
		values[ValueISISTruncated] = 1
	}
	if unseen > 0 {
		values[ValueISISAdjacenciesUnseen] = float64(unseen)
	}
	warn := p.MinAdjacencies > 0 && up < p.MinAdjacencies && !truncated
	return judge(values, failing, warn, LabelSystemID, evaluated, unseen)
}
