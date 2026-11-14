// Package routing holds the routing protocol probe types of the collector
// (PRT-07, E-183): ospf and isis over the standard MIBs (WP-1002) and bgp
// over BGP4-MIB with the BGP4-V2 tables of the profiles (WP-1001). Each
// type registers its executor in init(); the collector links the package
// in through collector/probe/all.
//
// The types read their tables over SNMP with the credentials of the
// namespace snmp. A vendor that deviates from the standard MIB (another
// table or enum) is covered by the monitor section ospf or isis of its
// profile (WP-1000): its table is read first; an empty or absent profile
// table falls back to the standard-MIB core. The executors hold no state.
package routing

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeOSPF is the register key of the OSPF neighbor probe.
const TypeOSPF = "ospf"

// MaxRows is the most neighbors or adjacencies a check reads (max_rows of
// the types, default and upper bound of max_neighbors and
// max_adjacencies).
const MaxRows = 1000

// Value names of an ospf result; the per-neighbor values carry the labels
// router_id and version (probewire.ValueKey).
const (
	ValueOSPFNeighborState  = "probe_ospf_neighbor_state"
	ValueOSPFNeighborEvents = "probe_ospf_neighbor_events_total"
	ValueOSPFNeighbors      = "probe_ospf_neighbors"
	ValueOSPFNeighborsFull  = "probe_ospf_neighbors_full"
	ValueOSPFTruncated      = "probe_ospf_truncated"
	// ValueOSPFNeighborsUnseen counts the expected neighbors behind a
	// truncated walk (no statement about them).
	ValueOSPFNeighborsUnseen = "probe_ospf_neighbors_unseen"
	LabelRouterID            = "router_id"
	LabelVersion             = "version"
)

// Logical tables and columns of the monitor section ospf (WP-1000).
const (
	ospfTableV2   = "neighbors"
	ospfTableV3   = "neighbors_v3"
	ospfColRtrID  = "router_id"
	ospfColState  = "state"
	ospfColEvents = "events"
	ospfStateFull = "full"
	mappedStateUp = "up"
)

// ospfStates is the OSPF neighbor state machine of RFC 4750 and RFC 5643:
// down(1) … full(8).
var ospfStates = map[string]string{
	"1": "down", "2": "attempt", "3": "init", "4": "twoWay",
	"5": "exchangeStart", "6": "exchange", "7": "loading", "8": ospfStateFull,
}

// ospfCoreV2 is ospfNbrTable of OSPF-MIB (RFC 4750, ospfNbrEntry
// 1.3.6.1.2.1.14.10.1): ospfNbrRtrId (3), ospfNbrState (6) and
// ospfNbrEvents (7), index ospfNbrIpAddr + ospfNbrAddressLessIndex.
var ospfCoreV2 = profiles.MonitorTable{
	Columns: map[string]string{
		ospfColRtrID:  "1.3.6.1.2.1.14.10.1.3",
		ospfColState:  "1.3.6.1.2.1.14.10.1.6",
		ospfColEvents: "1.3.6.1.2.1.14.10.1.7",
	},
	Index: "composite:[ipv4,int]",
	Enums: map[string]map[string]string{ospfColState: ospfStates},
}

// ospfCoreV3 is ospfv3NbrTable of OSPFV3-MIB (RFC 5643, ospfv3NbrEntry
// 1.3.6.1.2.1.191.1.9.1): ospfv3NbrState (8) and ospfv3NbrEvents (9);
// ospfv3NbrRtrId is the third part of the index ospfv3NbrIfIndex +
// ospfv3NbrIfInstId + ospfv3NbrRtrId (not accessible).
var ospfCoreV3 = profiles.MonitorTable{
	Columns: map[string]string{
		ospfColState:  "1.3.6.1.2.1.191.1.9.1.8",
		ospfColEvents: "1.3.6.1.2.1.191.1.9.1.9",
	},
	Index: "composite:[int,int,int]",
	Enums: map[string]map[string]string{ospfColState: ospfStates},
}

// monitorFor selects the monitor section of the target (tests replace it).
var monitorFor = probe.SNMPMonitor

func init() {
	probe.Register(TypeOSPF, &OSPFExecutor{})
	probe.Register(TypeISIS, &ISISExecutor{})
}

// OSPFExecutor is the probe type ospf (PRT-07, WP-1002): the OSPFv2 and
// OSPFv3 neighbors of the target. An expected neighbor (router ID,
// optionally the version) that is missing or not full is fail/check_failed
// with the label router_id; fewer full neighbors than min_neighbors is ok
// with the warn code check_failed (degraded). ospfNbrEvents is reported as
// a counter reading and does not change the state. More rows than
// max_neighbors truncate the walk (probe_ospf_truncated = 1); a neighbor
// beyond it is no statement.
//
// Several rows of one neighbor (one adjacency per interface) go in with
// the worst of them. On a broadcast segment two routers that are neither
// DR nor BDR stay in twoWay by design; such a neighbor is not full and
// belongs in neighbors only when it is expected full.
type OSPFExecutor struct{}

type ospfExpected struct {
	RouterID string `json:"router_id"`
	Version  int    `json:"version"`
}

type ospfParams struct {
	Neighbors    []ospfExpected `json:"neighbors"`
	MinNeighbors int            `json:"min_neighbors"`
	MaxNeighbors int            `json:"max_neighbors"`
}

// ospfNeighbor is one row of a neighbor table.
type ospfNeighbor struct {
	routerID  string
	version   int
	stateRaw  int64
	full      bool
	events    int64
	hasEvents bool
}

// Execute implements probe.Executor.
func (e *OSPFExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p ospfParams
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return probe.ConfigResult("params", "parameters cannot be decoded")
		}
	}
	maxRows := rowLimit(p.MaxNeighbors)
	for i := range p.Neighbors {
		addr, err := netip.ParseAddr(strings.TrimSpace(p.Neighbors[i].RouterID))
		if err != nil || !addr.Is4() {
			return probe.ConfigResult("params.neighbors["+strconv.Itoa(i)+"].router_id", "must be a router ID in dotted IPv4 form")
		}
		p.Neighbors[i].RouterID = addr.String()
	}

	sess, err := probe.OpenSNMP(ctx, &spec, creds)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	defer sess.Close() //nolint:errcheck // UDP socket, nothing to flush
	monitor, matched, err := monitorFor(sess, TypeOSPF)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	var rows []ospfNeighbor
	// truncatedBy records per version whether rows of its table lie
	// beyond max_neighbors. A table reached with the limit already filled
	// is probed for one row: only a row there is evidence of a cut.
	truncatedBy := map[int]bool{}
	for _, v := range []struct {
		version int
		table   string
		core    profiles.MonitorTable
	}{{2, ospfTableV2, ospfCoreV2}, {3, ospfTableV3, ospfCoreV3}} {
		limit, probeOnly := maxRows-len(rows), false
		if limit <= 0 {
			limit, probeOnly = 1, true
		}
		tables := []profiles.MonitorTable{v.core}
		if t, ok := monitor.Tables[v.table]; ok && matched {
			tables = []profiles.MonitorTable{t, v.core}
		}
		for i := range tables {
			got, cut, err := readOSPF(sess, &tables[i], v.version, limit)
			if err != nil {
				return probe.SNMPFailure(err)
			}
			if len(got) > 0 || i == len(tables)-1 {
				if probeOnly {
					truncatedBy[v.version] = len(got) > 0
				} else {
					rows = append(rows, got...)
					truncatedBy[v.version] = cut
				}
				break
			}
		}
	}
	return ospfResult(rows, &p, truncatedBy)
}

// rowLimit returns the row limit of a check: the parameter within 1 and
// MaxRows, MaxRows when unset.
func rowLimit(n int) int {
	if n <= 0 || n > MaxRows {
		return MaxRows
	}
	return n
}

// readOSPF walks one neighbor table (at most maxRows rows).
func readOSPF(s *snmp.Session, t *profiles.MonitorTable, version, maxRows int) ([]ospfNeighbor, bool, error) {
	cols, walk := columns(t, ospfColState, ospfColRtrID, ospfColEvents)
	if cols[ospfColState] == "" {
		return nil, false, nil
	}
	rows, err := s.WalkColumns(walk, maxRows)
	if err != nil {
		return nil, false, err
	}
	out := make([]ospfNeighbor, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		v, ok := row.Value(cols[ospfColState])
		if !ok {
			continue
		}
		raw, ok := probe.PDUInt(v)
		if !ok {
			continue
		}
		n := ospfNeighbor{version: version, stateRaw: raw}
		state, _ := t.MapEnum(ospfColState, raw)
		n.full = state == ospfStateFull || state == mappedStateUp
		if rv, ok := row.Value(cols[ospfColRtrID]); ok {
			n.routerID = routerID(probe.PDUBytes(rv), rv.Value)
		}
		if n.routerID == "" && version == 3 && len(row.Index) == 3 {
			// ospfv3NbrRtrId is the last part of the index.
			n.routerID = dotted(row.Index[2])
		}
		if n.routerID == "" {
			n.routerID = "index:" + indexString(row.Index)
		}
		if ev, ok := row.Value(cols[ospfColEvents]); ok {
			n.events, n.hasEvents = probe.PDUInt(ev)
		}
		out = append(out, n)
	}
	return out, rows.Truncated, nil
}

// columns returns the OIDs of the logical columns the table has and the
// list to walk.
func columns(t *profiles.MonitorTable, names ...string) (cols map[string]string, walk []string) {
	cols = map[string]string{}
	for _, name := range names {
		if oid, ok := t.ColumnOID(name); ok && oid != "" {
			cols[name] = oid
			walk = append(walk, oid)
		}
	}
	return cols, walk
}

// routerID formats a router ID column: four octets (IpAddress) or an
// unsigned integer (OSPFv3 RouterIdTC) in dotted form.
func routerID(raw []byte, v any) string {
	if len(raw) == 4 {
		return netip.AddrFrom4([4]byte(raw)).String()
	}
	switch n := v.(type) {
	case uint:
		return dotted(uint32(n)) //nolint:gosec // a RouterIdTC is 32 bits wide
	case uint32:
		return dotted(n)
	case int:
		return dotted(uint32(n)) //nolint:gosec // a RouterIdTC is 32 bits wide
	}
	return ""
}

func dotted(n uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], n)
	return netip.AddrFrom4(b).String()
}

func indexString(index []uint32) string {
	parts := make([]string, len(index))
	for i, v := range index {
		parts[i] = strconv.FormatUint(uint64(v), 10)
	}
	return strings.Join(parts, ".")
}

// ospfResult evaluates the neighbors (WP-1002 AK3, AK4). truncatedBy
// tells per version (2, 3) whether its table was cut at max_neighbors; an
// expected neighbor is no statement only if its table (any table for
// version 0) was cut.
func ospfResult(rows []ospfNeighbor, p *ospfParams, truncatedBy map[int]bool) probewire.Result {
	truncated := truncatedBy[2] || truncatedBy[3]
	type key struct {
		routerID string
		version  int
	}
	worst := map[key]*ospfNeighbor{}
	events := map[key]int64{}
	hasEvents := map[key]bool{}
	for i := range rows {
		n := &rows[i]
		k := key{n.routerID, n.version}
		if w, ok := worst[k]; !ok || worseOSPF(n, w) {
			worst[k] = n
		}
		if n.hasEvents {
			events[k] += n.events
			hasEvents[k] = true
		}
	}
	values := map[string]float64{}
	full := 0
	for k, n := range worst {
		labels := map[string]string{LabelRouterID: k.routerID, LabelVersion: strconv.Itoa(k.version)}
		values[probewire.ValueKey(ValueOSPFNeighborState, labels)] = float64(n.stateRaw)
		if hasEvents[k] {
			values[probewire.ValueKey(ValueOSPFNeighborEvents, labels)] = float64(events[k])
		}
		if n.full {
			full++
		}
	}
	values[ValueOSPFNeighbors] = float64(len(worst))
	values[ValueOSPFNeighborsFull] = float64(full)

	var failing []string
	evaluated, unseen := 0, 0
	for _, exp := range p.Neighbors {
		found, up := false, true
		for k, n := range worst {
			if k.routerID == exp.RouterID && (exp.Version == 0 || exp.Version == k.version) {
				found = true
				up = up && n.full
			}
		}
		cut := truncatedBy[exp.Version]
		if exp.Version == 0 {
			cut = truncated
		}
		switch {
		case found:
			evaluated++
			if !up {
				failing = append(failing, exp.RouterID)
			}
		case !cut:
			evaluated++
			failing = append(failing, exp.RouterID)
		default:
			unseen++
		}
	}
	if truncated {
		values[ValueOSPFTruncated] = 1
	}
	if unseen > 0 {
		values[ValueOSPFNeighborsUnseen] = float64(unseen)
	}
	// Fewer full neighbors than min_neighbors; behind a truncated walk the
	// count is no statement.
	warn := p.MinNeighbors > 0 && full < p.MinNeighbors && !truncated
	return judge(values, failing, warn, LabelRouterID, evaluated, unseen)
}

// worseOSPF orders two rows of one neighbor: not full before full, then
// the lower state.
func worseOSPF(a, b *ospfNeighbor) bool {
	if a.full != b.full {
		return !a.full
	}
	return a.stateRaw < b.stateRaw
}

// judge builds the result of a routing check: an expected entry that is
// missing or not up is fail/check_failed (label with the first in sorted
// order), a count below the minimum ok with the warn code check_failed;
// when every expected entry lies behind a truncated walk the result is
// error/response_too_large (unknown), never ok.
func judge(values map[string]float64, failing []string, warn bool, label string, evaluated, unseen int) probewire.Result {
	r := probewire.Result{Status: probewire.StatusOK, Values: values}
	switch {
	case len(failing) > 0:
		sort.Strings(failing)
		r.Status, r.Code = probewire.StatusFail, probewire.WarnCheckFailed
		r.Labels = map[string]string{label: failing[0]}
	case evaluated == 0 && unseen > 0:
		r.Status, r.Code = probewire.StatusError, probewire.ReasonResponseTooLarge
	case warn:
		r.Code = probewire.WarnCheckFailed
	}
	return r
}
