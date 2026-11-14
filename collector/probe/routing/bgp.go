package routing

import (
	"context"
	"encoding/json"
	"maps"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TypeBGP is the register key of the BGP peer probe (PRT-07, WP-1001).
const TypeBGP = "bgp"

// BGPMaxPeers is the most peers a check reads (max_rows of the type,
// default and upper bound of max_peers).
const BGPMaxPeers = 2000

// bgpPrefixRowsPerPeer is the room for address families per peer in the
// prefix walk (IPv4/IPv6 unicast, VPNv4/VPNv6, EVPN, flowspec, …).
const bgpPrefixRowsPerPeer = 8

// bgpASTrans is AS_TRANS (RFC 6793): a 2-byte table such as bgpPeerRemoteAs
// of BGP4-MIB (0..65535) reports it for a peer with a 4-byte AS.
const bgpASTrans = 23456

// Value names of a bgp result; the per-peer values carry the labels peer
// and remote_as, probe_bgp_prefixes_received also afi (probewire.ValueKey).
// A remote address that occurs in more than one row (the same peer address
// in several routing instances/VRFs) carries the label instance as well.
const (
	ValueBGPPeerState         = "probe_bgp_peer_state"
	ValueBGPEstablishedSecs   = "probe_bgp_established_seconds"
	ValueBGPPrefixesReceived  = "probe_bgp_prefixes_received"
	ValueBGPUpdatesInTotal    = "probe_bgp_updates_in_total"
	ValueBGPUpdatesOutTotal   = "probe_bgp_updates_out_total"
	ValueBGPTruncated         = "probe_bgp_truncated"
	ValueBGPPeers             = "probe_bgp_peers"
	ValueBGPPeersEstablished  = "probe_bgp_peers_established"
	ValueBGPPeersUnseen       = "probe_bgp_peers_unseen"
	ValueBGPSessionResets     = "probe_bgp_session_resets"
	LabelPeer                 = "peer"
	LabelRemoteAS             = "remote_as"
	LabelAFI                  = "afi"
	LabelInstance             = "instance"
	bgpStateEstablished       = "established"
	bgpStateEstablishedRaw    = 6
	bgpAdminDown              = "down"
	bgpTablePeers             = "peers"
	bgpTableCounters          = "counters"
	bgpTableEventTimes        = "event_times"
	bgpTablePrefixes          = "prefixes"
	bgpColState               = "state"
	bgpColAdmin               = "admin_status"
	bgpColRemoteAS            = "remote_as"
	bgpColAddrType            = "remote_addr_type"
	bgpColAddr                = "remote_addr"
	bgpColPeerIndex           = "peer_index"
	bgpColInUpdates           = "in_updates"
	bgpColOutUpdates          = "out_updates"
	bgpColTransitions         = "established_transitions"
	bgpColEstablishedTime     = "established_time"
	bgpColPrefixes            = "prefixes"
	bgpSeriesTransitions      = ":transitions"
	bgpSeriesEstablishedTime  = ":established_time"
	bgpSeriesPrefix           = "bgp:"
	bgpPrefixIndexAfterPeerID = 2 // afi and safi follow the peer key
)

// bgpStates are the BGP FSM states of RFC 4273 (bgpPeerState): idle(1) …
// established(6).
var bgpStates = map[string]string{
	"1": "idle", "2": "connect", "3": "active", "4": "openSent", "5": "openConfirm", "6": bgpStateEstablished,
}

// bgpCore is bgpPeerTable of BGP4-MIB (RFC 4273, bgpPeerEntry
// 1.3.6.1.2.1.15.3.1): bgpPeerState (2), bgpPeerAdminStatus (3, stop(1),
// start(2)), bgpPeerRemoteAs (9), bgpPeerInUpdates (10),
// bgpPeerOutUpdates (11), bgpPeerFsmEstablishedTransitions (15) and
// bgpPeerFsmEstablishedTime (16), index bgpPeerRemoteAddr (IPv4). The
// MIB has no prefix counters and no IPv6 peers.
var bgpCore = profiles.MonitorTable{
	Columns: map[string]string{
		bgpColState:           "1.3.6.1.2.1.15.3.1.2",
		bgpColAdmin:           "1.3.6.1.2.1.15.3.1.3",
		bgpColRemoteAS:        "1.3.6.1.2.1.15.3.1.9",
		bgpColInUpdates:       "1.3.6.1.2.1.15.3.1.10",
		bgpColOutUpdates:      "1.3.6.1.2.1.15.3.1.11",
		bgpColTransitions:     "1.3.6.1.2.1.15.3.1.15",
		bgpColEstablishedTime: "1.3.6.1.2.1.15.3.1.16",
	},
	Index: profiles.IndexIPv4,
	Enums: map[string]map[string]string{
		bgpColState: bgpStates,
		bgpColAdmin: {"1": bgpAdminDown, "2": "up"},
	},
}

// BGPExecutor is the probe type bgp (PRT-07, WP-1001): the BGP peers of
// the target from bgpPeerTable of BGP4-MIB or, when the profile of the
// target has monitor.bgp (cisco, juniper, arista), from its BGP4-V2 table
// with IPv6 peers and prefix counters. An expected peer that is missing or
// not established is fail/check_failed (label peer); a different
// remote_as, fewer prefixes than min_prefixes or a session reset since the
// previous run is ok with the warn code check_failed (degraded through
// WP-952). The previous values per peer lie in the previous-value store of
// the engine context (WP-994); admin-stopped peers go into no rating.
type BGPExecutor struct{}

type bgpExpected struct {
	Address     string `json:"address"`
	RemoteAS    *int64 `json:"remote_as"`
	MinPrefixes *int64 `json:"min_prefixes"`
	addr        netip.Addr
}

type bgpParams struct {
	Peers    []bgpExpected `json:"peers"`
	AllPeers bool          `json:"all_peers"`
	MaxPeers int           `json:"max_peers"`
}

// bgpPeer is one peer of the table.
type bgpPeer struct {
	addr        netip.Addr
	key         []uint32 // index of the row, or [peer_index] for the join
	stateRaw    int64
	established bool
	adminDown   bool
	remoteAS    int64
	hasAS       bool
	instance    string // routing instance of the row index, if it has one
	inUpdates   int64
	hasUpdates  bool
	outUpdates  int64
	hasOut      bool
	transitions int64
	hasTrans    bool
	estTime     int64
	hasEstTime  bool
	prefixes    map[string]int64 // by afi
	reset       bool
}

// bgpNow is the clock of the executor (tests replace it).
var bgpNow = time.Now

func init() { probe.Register(TypeBGP, &BGPExecutor{}) }

// Execute implements probe.Executor.
func (e *BGPExecutor) Execute(ctx context.Context, spec probewire.ProbeSpec, creds map[string]string) probewire.Result { //nolint:gocritic // signature of the Executor contract (WP-950 AK1)
	var p bgpParams
	if len(spec.Params) > 0 {
		if err := json.Unmarshal(spec.Params, &p); err != nil {
			return probe.ConfigResult("params", "parameters cannot be decoded")
		}
	}
	for i := range p.Peers {
		addr, err := netip.ParseAddr(strings.TrimSpace(p.Peers[i].Address))
		if err != nil {
			return probe.ConfigResult("params.peers["+strconv.Itoa(i)+"].address", "must be an IPv4 or IPv6 address")
		}
		p.Peers[i].addr = addr.WithZone("").Unmap()
	}
	maxPeers := p.MaxPeers
	if maxPeers <= 0 || maxPeers > BGPMaxPeers {
		maxPeers = BGPMaxPeers
	}

	sess, err := probe.OpenSNMP(ctx, &spec, creds)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	defer sess.Close() //nolint:errcheck // UDP socket, nothing to flush
	monitor, matched, err := monitorFor(sess, TypeBGP)
	if err != nil {
		return probe.SNMPFailure(err)
	}
	tables := []profiles.MonitorTable{bgpCore}
	var spec2 *profiles.MonitorSpec
	if t, ok := monitor.Tables[bgpTablePeers]; ok && matched {
		tables = []profiles.MonitorTable{t, bgpCore}
		spec2 = &monitor
	}
	var peers []*bgpPeer
	truncated, prefixesCut := false, false
	for i := range tables {
		peers, truncated, err = readBGPPeers(sess, &tables[i], maxPeers)
		if err != nil {
			return probe.SNMPFailure(err)
		}
		if len(peers) > 0 || i == len(tables)-1 {
			if i == 0 && spec2 != nil {
				if prefixesCut, err = joinBGP(sess, spec2, peers); err != nil {
					return probe.SNMPFailure(err)
				}
			}
			break
		}
	}
	markResets(rate.FromContext(ctx), bgpNow(), peers)
	return bgpResult(peers, &p, truncated, prefixesCut)
}

// readBGPPeers walks the peer table t (at most maxRows rows).
func readBGPPeers(s *snmp.Session, t *profiles.MonitorTable, maxRows int) ([]*bgpPeer, bool, error) {
	cols, walk := columns(t, bgpColState, bgpColAdmin, bgpColRemoteAS, bgpColAddrType, bgpColAddr, bgpColPeerIndex,
		bgpColInUpdates, bgpColOutUpdates, bgpColTransitions, bgpColEstablishedTime)
	if cols[bgpColState] == "" {
		return nil, false, nil
	}
	rows, err := s.WalkColumns(walk, maxRows)
	if err != nil {
		return nil, false, err
	}
	kinds, _ := profiles.ParseIndex(t.Index)
	out := make([]*bgpPeer, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		v, ok := row.Value(cols[bgpColState])
		if !ok {
			continue
		}
		raw, ok := probe.PDUInt(v)
		if !ok {
			continue
		}
		pr := &bgpPeer{stateRaw: raw, key: row.Index}
		if len(kinds) > 1 && kinds[0] == profiles.IndexInt && len(row.Index) > 0 {
			pr.instance = strconv.FormatUint(uint64(row.Index[0]), 10)
		}
		state, mapped := t.MapEnum(bgpColState, raw)
		pr.established = state == bgpStateEstablished || state == mappedStateUp || (!mapped && raw == bgpStateEstablishedRaw)
		if av, ok := row.Value(cols[bgpColAdmin]); ok {
			if a, ok := probe.PDUInt(av); ok {
				admin, _ := t.MapEnum(bgpColAdmin, a)
				pr.adminDown = admin == bgpAdminDown
			}
		}
		if rv, ok := row.Value(cols[bgpColAddr]); ok {
			var typ int64
			if tv, ok := row.Value(cols[bgpColAddrType]); ok {
				typ, _ = probe.PDUInt(tv)
			}
			pr.addr, _ = inetAddr(typ, probe.PDUBytes(rv))
		}
		if !pr.addr.IsValid() {
			pr.addr = indexAddress(kinds, row.Index)
		}
		if !pr.addr.IsValid() {
			continue
		}
		if iv, ok := row.Value(cols[bgpColPeerIndex]); ok {
			if n, ok := probe.PDUInt(iv); ok && n >= 0 {
				pr.key = []uint32{uint32(n)} //nolint:gosec // a peer index is a 32-bit Unsigned32
			}
		}
		pr.setCounters(row, cols)
		out = append(out, pr)
	}
	return out, rows.Truncated, nil
}

// setCounters takes the counter columns of a row.
func (pr *bgpPeer) setCounters(row snmp.Row, cols map[string]string) {
	if v, ok := row.Value(cols[bgpColRemoteAS]); ok && cols[bgpColRemoteAS] != "" {
		pr.remoteAS, pr.hasAS = probe.PDUInt(v)
	}
	if v, ok := row.Value(cols[bgpColInUpdates]); ok && cols[bgpColInUpdates] != "" {
		pr.inUpdates, pr.hasUpdates = probe.PDUInt(v)
	}
	if v, ok := row.Value(cols[bgpColOutUpdates]); ok && cols[bgpColOutUpdates] != "" {
		pr.outUpdates, pr.hasOut = probe.PDUInt(v)
	}
	if v, ok := row.Value(cols[bgpColTransitions]); ok && cols[bgpColTransitions] != "" {
		pr.transitions, pr.hasTrans = probe.PDUInt(v)
	}
	if v, ok := row.Value(cols[bgpColEstablishedTime]); ok && cols[bgpColEstablishedTime] != "" {
		pr.estTime, pr.hasEstTime = probe.PDUInt(v)
	}
}

// joinBGP adds the counter, event time and prefix tables of the profile
// to the peers: a row belongs to the peer whose key (its peer_index, else
// its row index) starts the row's index; prefix rows carry afi and safi
// after it. The join tables are walked up to BGPMaxPeers rows
// (bgpPrefixRowsPerPeer per peer for the prefixes), independent of
// max_peers: their order (e.g. by jnxBgpM2PeerIndex) need not be the
// order of the peer table. prefixesCut reports a prefix walk cut at its
// limit; peers without prefix rows then have no statement on
// min_prefixes.
func joinBGP(s *snmp.Session, m *profiles.MonitorSpec, peers []*bgpPeer) (prefixesCut bool, err error) {
	byKey := map[string]*bgpPeer{}
	for _, pr := range peers {
		byKey[indexString(pr.key)] = pr
	}
	for _, name := range []string{bgpTableCounters, bgpTableEventTimes} {
		t, ok := m.Tables[name]
		if !ok {
			continue
		}
		cols, walk := columns(&t, bgpColInUpdates, bgpColOutUpdates, bgpColTransitions, bgpColEstablishedTime, bgpColRemoteAS)
		if len(walk) == 0 {
			continue
		}
		rows, err := s.WalkColumns(walk, BGPMaxPeers)
		if err != nil {
			return false, err
		}
		for _, row := range rows.Rows {
			if pr, ok := byKey[indexString(row.Index)]; ok {
				pr.setCounters(row, cols)
			}
		}
	}
	t, ok := m.Tables[bgpTablePrefixes]
	if !ok {
		return false, nil
	}
	cols, walk := columns(&t, bgpColPrefixes)
	if len(walk) == 0 {
		return false, nil
	}
	rows, err := s.WalkColumns(walk, bgpPrefixRowsPerPeer*BGPMaxPeers)
	if err != nil {
		return false, err
	}
	for _, row := range rows.Rows {
		if len(row.Index) <= bgpPrefixIndexAfterPeerID {
			continue
		}
		head := row.Index[:len(row.Index)-bgpPrefixIndexAfterPeerID]
		pr, ok := byKey[indexString(head)]
		if !ok {
			continue
		}
		v, ok := row.Value(cols[bgpColPrefixes])
		if !ok {
			continue
		}
		n, ok := probe.PDUInt(v)
		if !ok {
			continue
		}
		afi := row.Index[len(row.Index)-2]
		safi := row.Index[len(row.Index)-1]
		if pr.prefixes == nil {
			pr.prefixes = map[string]int64{}
		}
		pr.prefixes[afiName(afi, safi)] += n
	}
	return rows.Truncated, nil
}

// afiName names an address family (RFC 4760 AFI, SAFI): ipv4, ipv6 for
// unicast, else afi.safi.
func afiName(afi, safi uint32) string {
	switch {
	case afi == 1 && safi == 1:
		return "ipv4"
	case afi == 2 && safi == 1:
		return "ipv6"
	}
	return strconv.FormatUint(uint64(afi), 10) + "." + strconv.FormatUint(uint64(safi), 10)
}

// indexAddress returns the last address part of a row index of the given
// kinds (the remote address: BGP4-V2 tables lead the local before the
// remote address).
func indexAddress(kinds []string, index []uint32) netip.Addr {
	var addr netip.Addr
	rest := index
	for _, kind := range kinds {
		var a netip.Addr
		var err error
		switch kind {
		case profiles.IndexInt:
			_, rest, err = snmp.IndexInt(rest)
		case profiles.IndexIPv4:
			a, rest, err = snmp.IndexIPv4(rest)
		case profiles.IndexInet:
			a, rest, err = snmp.IndexInetAddress(rest)
		case profiles.IndexIPv6:
			if len(rest) < 16 {
				return netip.Addr{}
			}
			b := make([]byte, 16)
			for i := range b {
				b[i] = byte(rest[i]) //nolint:gosec // an octet of the index
			}
			a, _ = netip.AddrFromSlice(b)
			rest = rest[16:]
		}
		if err != nil {
			return netip.Addr{}
		}
		if a.IsValid() {
			addr = a.Unmap()
		}
	}
	return addr
}

// inetAddr decodes an InetAddress column (with the type when known).
func inetAddr(typ int64, raw []byte) (netip.Addr, bool) {
	switch {
	case len(raw) == 4 && (typ == 0 || typ == 1):
		return netip.AddrFrom4([4]byte(raw)), true
	case len(raw) == 16 && (typ == 0 || typ == 2):
		return netip.AddrFrom16([16]byte(raw)).Unmap(), true
	}
	return netip.Addr{}, false
}

// markResets compares FsmEstablishedTransitions and FsmEstablishedTime of
// every peer with its previous values in the store (Swap): a raised
// transition count or a smaller established time is a session reset. The
// series is keyed by the row (its peer_index or full row index, which
// includes the routing instance), not by the address, so the same address
// in two VRFs has two series.
func markResets(store *rate.Store, at time.Time, peers []*bgpPeer) {
	for _, pr := range peers {
		series := bgpSeriesPrefix + indexString(pr.key)
		if pr.hasTrans {
			prev, _, ok := store.Swap(series+bgpSeriesTransitions, rate.Num(float64(pr.transitions)), at)
			if ok && !prev.IsEnum() && float64(pr.transitions) > prev.Number {
				pr.reset = true
			}
		}
		if pr.hasEstTime && pr.established {
			prev, _, ok := store.Swap(series+bgpSeriesEstablishedTime, rate.Num(float64(pr.estTime)), at)
			if ok && !prev.IsEnum() && float64(pr.estTime) < prev.Number {
				pr.reset = true
			}
		}
	}
}

// bgpResult evaluates the peers (WP-1001 AK4, AK5). truncated reports a
// peer table cut at max_peers, prefixesCut a prefix walk cut at its limit.
// Several rows with the same address (one peer address in several
// routing instances) count as one expected peer and its worst row decides.
func bgpResult(peers []*bgpPeer, p *bgpParams, truncated, prefixesCut bool) probewire.Result {
	values := map[string]float64{}
	byAddr := map[netip.Addr][]*bgpPeer{}
	for _, pr := range peers {
		byAddr[pr.addr] = append(byAddr[pr.addr], pr)
	}
	var budget probe.ValueBudget
	dropped := false
	established, resets := 0, 0
	for _, pr := range peers {
		labels := map[string]string{LabelPeer: pr.addr.String()}
		if pr.hasAS {
			labels[LabelRemoteAS] = strconv.FormatInt(pr.remoteAS, 10)
		}
		if len(byAddr[pr.addr]) > 1 {
			labels[LabelInstance] = pr.instance
			if pr.instance == "" {
				labels[LabelInstance] = indexString(pr.key)
			}
		}
		row := map[string]float64{probewire.ValueKey(ValueBGPPeerState, labels): float64(pr.stateRaw)}
		if pr.hasEstTime {
			row[probewire.ValueKey(ValueBGPEstablishedSecs, labels)] = float64(pr.estTime)
		}
		if pr.hasUpdates {
			row[probewire.ValueKey(ValueBGPUpdatesInTotal, labels)] = float64(pr.inUpdates)
		}
		if pr.hasOut {
			row[probewire.ValueKey(ValueBGPUpdatesOutTotal, labels)] = float64(pr.outUpdates)
		}
		for afi, n := range pr.prefixes {
			l := maps.Clone(labels)
			l[LabelAFI] = afi
			row[probewire.ValueKey(ValueBGPPrefixesReceived, l)] = float64(n)
		}
		if !budget.Add(values, row) {
			// One uplink message has no room for the values of further
			// peers; the state still covers every row read.
			dropped = true
		}
		if pr.established {
			established++
		}
		if pr.reset {
			resets++
		}
	}
	values[ValueBGPPeers] = float64(len(peers))
	values[ValueBGPPeersEstablished] = float64(established)
	values[ValueBGPSessionResets] = float64(resets)
	if truncated || prefixesCut || dropped {
		values[ValueBGPTruncated] = 1
	}

	expected := slices.Clone(p.Peers)
	if p.AllPeers {
		listed := map[netip.Addr]bool{}
		for _, e := range expected {
			listed[e.addr] = true
		}
		for _, pr := range peers {
			if !pr.adminDown && !listed[pr.addr] {
				listed[pr.addr] = true
				expected = append(expected, bgpExpected{addr: pr.addr})
			}
		}
	}
	var failing, warning []string
	evaluated, unseen := 0, 0
	for _, exp := range expected {
		rows := byAddr[exp.addr]
		var active []*bgpPeer
		for _, pr := range rows {
			if !pr.adminDown {
				active = append(active, pr)
			}
		}
		switch {
		case len(rows) == 0 && truncated:
			unseen++
			continue
		case len(rows) == 0:
			evaluated++
			failing = append(failing, exp.addr.String())
			continue
		case len(active) == 0:
			// Admin-stopped peers go into no rating.
			continue
		}
		evaluated++
		down, warn, prefixRows := false, false, false
		for _, pr := range active {
			down = down || !pr.established
			prefixRows = prefixRows || pr.prefixes != nil
			warn = warn || exp.remoteASDiffers(pr) || pr.reset ||
				(exp.MinPrefixes != nil && pr.prefixes != nil && sumPrefixes(pr.prefixes) < *exp.MinPrefixes)
		}
		if down {
			failing = append(failing, exp.addr.String())
			continue
		}
		if exp.MinPrefixes != nil && !prefixRows && prefixesCut {
			// The prefix rows may lie behind the cut: no statement on
			// min_prefixes.
			unseen++
		}
		if warn {
			warning = append(warning, exp.addr.String())
		}
	}
	if unseen > 0 {
		values[ValueBGPPeersUnseen] = float64(unseen)
	}
	r := judge(values, failing, len(warning) > 0, LabelPeer, evaluated, unseen)
	if r.Status == probewire.StatusOK && r.Code == probewire.WarnCheckFailed {
		sort.Strings(warning)
		r.Labels = map[string]string{LabelPeer: warning[0]}
	}
	return r
}

// remoteASDiffers reports whether the remote AS of the row differs from the
// expected one. AS_TRANS (23456) for an expected 4-byte AS is no
// difference: a 2-byte table such as bgpPeerRemoteAs of BGP4-MIB cannot
// report the real AS.
func (exp *bgpExpected) remoteASDiffers(pr *bgpPeer) bool {
	if exp.RemoteAS == nil || !pr.hasAS || pr.remoteAS == *exp.RemoteAS {
		return false
	}
	return pr.remoteAS != bgpASTrans || *exp.RemoteAS <= 65535
}

func sumPrefixes(m map[string]int64) int64 {
	var n int64
	for _, v := range m {
		n += v
	}
	return n
}
