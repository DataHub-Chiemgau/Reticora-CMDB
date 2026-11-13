package probe

import (
	"context"
	"encoding/json"
	"net/netip"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeBFD is the register key of the BFD session probe (PRT-08, WP-1003).
const TypeBFD = "bfd"

// BFDMaxRows is the most sessions a check reads (max_rows of the type);
// a longer table is truncated (probe_bfd_truncated = 1).
const BFDMaxRows = 1000

// Value names of a bfd result. The per-session values carry the label
// peer (neighbor address), probe_bfd_diag also diag (the name of the
// diagnostic code), in the key form of probewire.ValueKey.
const (
	ValueBFDSessionState = "probe_bfd_session_state"
	ValueBFDDiag         = "probe_bfd_diag"
	ValueBFDTruncated    = "probe_bfd_truncated"
	ValueBFDSessions     = "probe_bfd_sessions"
	ValueBFDSessionsDown = "probe_bfd_sessions_down"
	// ValueBFDSessionsUnseen counts the expected sessions behind a
	// truncated walk (no statement about them).
	ValueBFDSessionsUnseen = "probe_bfd_sessions_unseen"
	LabelBFDPeer           = "peer"
	LabelBFDDiag           = "diag"
)

// Logical columns and table of the monitor section bfd (WP-1000): the
// profile maps them onto a vendor table (cisco.yaml: CISCO-IETF-BFD-MIB).
const (
	bfdTable      = "sessions"
	bfdColState   = "state"
	bfdColDiag    = "diag"
	bfdColAddrTyp = "addr_type"
	bfdColAddr    = "addr"
	// Mapped session states.
	bfdStateUp        = "up"
	bfdStateAdminDown = "adminDown"
)

// bfdCore is the standard-MIB core: bfdSessTable of BFD-STD-MIB (RFC 7331,
// mib-2 222; bfdSessEntry 1.3.6.1.2.1.222.1.2.1) with bfdSessState (11,
// BfdSessStateTC of RFC 7330: adminDown(1), down(2), init(3), up(4)),
// bfdSessDiag (13, BfdDiagTC), bfdSessDstAddrType (21, InetAddressType)
// and bfdSessDstAddr (22, InetAddress), index bfdSessIndex.
var bfdCore = profiles.MonitorTable{
	Columns: map[string]string{
		bfdColState:   "1.3.6.1.2.1.222.1.2.1.11",
		bfdColDiag:    "1.3.6.1.2.1.222.1.2.1.13",
		bfdColAddrTyp: "1.3.6.1.2.1.222.1.2.1.21",
		bfdColAddr:    "1.3.6.1.2.1.222.1.2.1.22",
	},
	Index: profiles.IndexInt,
	Enums: map[string]map[string]string{
		bfdColState: {"1": bfdStateAdminDown, "2": "down", "3": "init", "4": bfdStateUp},
		bfdColDiag: {
			"0": "noDiagnostic", "1": "controlDetectionTimeExpired", "2": "echoFunctionFailed",
			"3": "neighborSignaledSessionDown", "4": "forwardingPlaneReset", "5": "pathDown",
			"6": "concatenatedPathDown", "7": "administrativelyDown", "8": "reverseConcatenatedPathDown",
			"9": "misConnectivityDefect",
		},
	},
}

func init() { Register(TypeBFD, &BFDExecutor{}) }

// BFDExecutor is the probe type bfd (PRT-08, WP-1003): it reads the BFD
// sessions of the target over SNMP, from the table of the monitor section
// bfd of the matching profile (vendor MIB, e.g. CISCO-IETF-BFD-MIB) and
// otherwise, or when that table is empty, from bfdSessTable of
// BFD-STD-MIB. A target that answers neither table is error/unsupported.
// With sessions every listed neighbor must have a session up, with all
// every session but adminDown; otherwise the result is fail/session_down.
// State and diagnostic of the sessions go into the values with the label
// peer. More than BFDMaxRows sessions truncate the walk
// (probe_bfd_truncated = 1); a session beyond it is no failure.
type BFDExecutor struct{}

type bfdParams struct {
	Sessions []string `json:"sessions"`
	All      bool     `json:"all"`
}

// bfdSession is one row of the session table.
type bfdSession struct {
	peer     string
	addr     netip.Addr
	stateRaw int64
	state    string
	diagRaw  int64
	diag     string
}

// Execute implements Executor.
func (e *BFDExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p bfdParams
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return configResult("params", "parameters cannot be decoded")
		}
	}
	expected := map[netip.Addr]bool{}
	for _, s := range p.Sessions {
		addr, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			return configResult("params.sessions", "must be IPv4 or IPv6 addresses")
		}
		// A zone (fe80::1%eth0) is not compared: the table carries the zone
		// index, not the interface name.
		expected[addr.WithZone("").Unmap()] = true
	}
	if !p.All && len(expected) == 0 {
		return configResult("params.sessions", "set sessions or all: true")
	}

	sess, err := openSNMP(ctx, &spec, creds)
	if err != nil {
		return snmpFailure(err)
	}
	defer sess.Close() //nolint:errcheck // UDP socket, nothing to flush
	monitor, matched, err := snmpMonitor(sess, TypeBFD)
	if err != nil {
		return snmpFailure(err)
	}
	tables := []profiles.MonitorTable{bfdCore}
	if t, ok := monitor.Tables[bfdTable]; ok && matched {
		tables = []profiles.MonitorTable{t, bfdCore}
	}
	var sessions []bfdSession
	truncated := false
	for i := range tables {
		sessions, truncated, err = readBFDSessions(sess, &tables[i])
		if err != nil {
			return snmpFailure(err)
		}
		if len(sessions) > 0 {
			break
		}
	}
	if len(sessions) == 0 {
		return failResult(probewire.StatusError, probewire.ReasonUnsupported)
	}
	return bfdResult(sessions, expected, p.All, truncated)
}

// readBFDSessions walks the session table t (at most BFDMaxRows rows).
func readBFDSessions(s *snmp.Session, t *profiles.MonitorTable) ([]bfdSession, bool, error) {
	cols := map[string]string{}
	var walk []string
	for _, name := range []string{bfdColState, bfdColDiag, bfdColAddrTyp, bfdColAddr} {
		if oid, ok := t.ColumnOID(name); ok {
			cols[name] = oid
			walk = append(walk, oid)
		}
	}
	if cols[bfdColState] == "" {
		return nil, false, nil
	}
	rows, err := s.WalkColumns(walk, BFDMaxRows)
	if err != nil {
		return nil, false, err
	}
	out := make([]bfdSession, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		v, ok := row.Value(cols[bfdColState])
		if !ok {
			continue
		}
		raw, ok := pduInt(v)
		if !ok {
			continue
		}
		bs := bfdSession{stateRaw: raw}
		bs.state, _ = t.MapEnum(bfdColState, raw)
		if dv, ok := row.Value(cols[bfdColDiag]); ok {
			if d, ok := pduInt(dv); ok {
				bs.diagRaw = d
				if name, ok := t.MapEnum(bfdColDiag, d); ok {
					bs.diag = name
				} else {
					bs.diag = "diag" + strconv.FormatInt(d, 10)
				}
			}
		}
		var typ int64
		if tv, ok := row.Value(cols[bfdColAddrTyp]); ok {
			typ, _ = pduInt(tv)
		}
		if av, ok := row.Value(cols[bfdColAddr]); ok {
			bs.addr, _ = inetAddress(typ, pduBytes(av))
		}
		bs.peer = bs.addr.String()
		if !bs.addr.IsValid() {
			bs.peer = "index:" + indexString(row.Index)
		}
		out = append(out, bs)
	}
	return out, rows.Truncated, nil
}

func indexString(index []uint32) string {
	parts := make([]string, len(index))
	for i, v := range index {
		parts[i] = strconv.FormatUint(uint64(v), 10)
	}
	return strings.Join(parts, ".")
}

// bfdRank orders the session states for several rows of one neighbor
// (micro-BFD on LAG members, one session per VRF or interface): a row that
// is neither up nor adminDown is the worst, then adminDown, then up.
func bfdRank(s *bfdSession) int {
	switch s.state {
	case bfdStateUp:
		return 0
	case bfdStateAdminDown:
		return 1
	}
	return 2
}

// bfdResult evaluates the sessions (WP-1003 AK4, AK5). Several rows of one
// neighbor go into the per-session values as the worst of them; with
// sessions an expected neighbor is down when any of its rows is not up.
// An expected neighbor behind a truncated walk is no statement
// (probe_bfd_sessions_unseen); when no expected neighbor could be read at
// all the result is error/response_too_large (unknown), never ok.
func bfdResult(sessions []bfdSession, expected map[netip.Addr]bool, all, truncated bool) probewire.Result {
	values := map[string]float64{}
	worst := map[string]*bfdSession{}
	record := func(s *bfdSession) {
		if w, ok := worst[s.peer]; !ok || bfdRank(s) > bfdRank(w) {
			worst[s.peer] = s
		}
	}
	down, evaluated, unseen := 0, 0, 0
	if all {
		for i := range sessions {
			s := &sessions[i]
			record(s)
			evaluated++
			if s.state != bfdStateUp && s.state != bfdStateAdminDown {
				down++
			}
		}
	} else {
		byAddr := map[netip.Addr]*bfdSession{}
		for i := range sessions {
			s := &sessions[i]
			if !s.addr.IsValid() {
				continue
			}
			// In this mode adminDown is not up either: the worst row decides.
			if w, ok := byAddr[s.addr]; !ok || (w.state == bfdStateUp && s.state != bfdStateUp) {
				byAddr[s.addr] = s
			}
		}
		for addr := range expected {
			s, ok := byAddr[addr]
			switch {
			case ok:
				for i := range sessions {
					if sessions[i].addr == addr {
						record(&sessions[i])
					}
				}
				evaluated++
				if s.state != bfdStateUp {
					down++
				}
			case !truncated:
				// Missing from a complete table: an expected session that
				// is not up. Behind a truncated walk it is no statement.
				evaluated++
				down++
			default:
				unseen++
			}
		}
	}
	for _, s := range worst {
		values[probewire.ValueKey(ValueBFDSessionState, map[string]string{LabelBFDPeer: s.peer})] = float64(s.stateRaw)
		values[probewire.ValueKey(ValueBFDDiag, map[string]string{LabelBFDPeer: s.peer, LabelBFDDiag: s.diag})] = float64(s.diagRaw)
	}
	values[ValueBFDSessions] = float64(evaluated)
	values[ValueBFDSessionsDown] = float64(down)
	if truncated {
		values[ValueBFDTruncated] = 1
	}
	if unseen > 0 {
		values[ValueBFDSessionsUnseen] = float64(unseen)
	}
	r := probewire.Result{Status: probewire.StatusOK, Values: values}
	switch {
	case down > 0:
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonSessionDown
	case evaluated == 0 && unseen > 0:
		// Every expected session lies behind the cut: not read, so no up.
		r.Status, r.Code = probewire.StatusError, probewire.ReasonResponseTooLarge
	}
	return r
}
