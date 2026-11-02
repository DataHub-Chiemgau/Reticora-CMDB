package snmp

import (
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Neighbours of a device (DIS-05 neighbors, the input of the topology
// TOP-01): the remote systems of the LLDP-MIB (IEEE 802.1AB) with their
// management address, and the MACs the bridge learned (Q-BRIDGE-MIB
// dot1qTpFdbTable, else BRIDGE-MIB dot1dTpFdbTable). The local port of a
// neighbour is the interface of the record it maps to over ifName/ifDescr
// (LLDP: lldpLocPortTable, FDB: dot1dBasePortIfIndex). Weighing the
// neighbours and recognising trunks is the backend's job (TOP-01); CDP is
// [P3] (DIS-06).
const (
	oidIfName = "1.3.6.1.2.1.31.1.1.1.1"

	oidLldpLocPortIDSubtype = "1.0.8802.1.1.2.1.3.7.1.2"
	oidLldpLocPortID        = "1.0.8802.1.1.2.1.3.7.1.3"
	oidLldpLocPortDesc      = "1.0.8802.1.1.2.1.3.7.1.4"

	oidLldpRemChassisIDSubtype = "1.0.8802.1.1.2.1.4.1.1.4"
	oidLldpRemChassisID        = "1.0.8802.1.1.2.1.4.1.1.5"
	oidLldpRemPortIDSubtype    = "1.0.8802.1.1.2.1.4.1.1.6"
	oidLldpRemPortID           = "1.0.8802.1.1.2.1.4.1.1.7"
	oidLldpRemPortDesc         = "1.0.8802.1.1.2.1.4.1.1.8"
	oidLldpRemSysName          = "1.0.8802.1.1.2.1.4.1.1.9"
	oidLldpRemManAddrIfSubtype = "1.0.8802.1.1.2.1.4.2.1.3"

	oidDot1dBasePortIfIndex = "1.3.6.1.2.1.17.1.4.1.2"
	oidDot1dTpFdbPort       = "1.3.6.1.2.1.17.4.3.1.2"
	oidDot1dTpFdbStatus     = "1.3.6.1.2.1.17.4.3.1.3"
	oidDot1qTpFdbPort       = "1.3.6.1.2.1.17.7.1.2.2.1.2"
	oidDot1qTpFdbStatus     = "1.3.6.1.2.1.17.7.1.2.2.1.3"
)

// LLDP-MIB LldpChassisIdSubtype and LldpPortIdSubtype values, and the
// dot1dTpFdbStatus/dot1qTpFdbStatus values of entries that are no neighbour.
const (
	lldpChassisMAC     = 4
	lldpChassisAddress = 5
	lldpPortAlias      = 1
	lldpPortMAC        = 3
	lldpPortName       = 5
	lldpPortLocal      = 7
	fdbStatusInvalid   = 2
	fdbStatusSelf      = 4
)

// maxTableEntries bounds the walk of every neighbour table; the rest of a
// longer table is skipped with a warning.
const maxTableEntries = 10_000

var errTableLimit = errors.New("snmp: table walk limit reached")

// walkFunc walks the subtree of rootOid (gosnmp BulkWalk or Walk).
type walkFunc func(rootOid string, fn gosnmp.WalkFunc) error

// walkNeighbors reads the neighbours of the device of session g; interfaces
// are the interfaces of its record. A device without the tables has no
// neighbours; a failed walk ends the reading with the neighbours found so
// far, the inventory stays valid.
func walkNeighbors(g *gosnmp.GoSNMP, target string, interfaces []plugins.NetworkInterface) []plugins.DeviceNeighbor {
	walk := g.BulkWalk
	if g.Version == gosnmp.Version1 {
		walk = g.Walk
	}
	return (&neighborWalk{walk: walk, target: target}).neighbors(interfaces)
}

// neighborWalk reads the neighbour tables of one device.
type neighborWalk struct {
	walk   walkFunc
	target string
	// err is the first failed walk; the walks after it are skipped.
	err       error
	truncated map[string]bool
}

// column calls fn with the index and value of every row of a table column,
// at most maxTableEntries rows (a row with an unreadable index counts too).
// A missing table (noSuchObject, noSuchInstance, endOfMibView) has none.
func (n *neighborWalk) column(table, column string, fn func(index []int, v gosnmp.SnmpPDU)) {
	if n.err != nil {
		return
	}
	rows := 0
	err := n.walk(column, func(v gosnmp.SnmpPDU) error {
		if rows == maxTableEntries {
			return errTableLimit
		}
		rows++
		if index, ok := oidIndex(v.Name, column); ok {
			fn(index, v)
		}
		return nil
	})
	switch {
	case errors.Is(err, errTableLimit):
		if !n.truncated[table] {
			if n.truncated == nil {
				n.truncated = map[string]bool{}
			}
			n.truncated[table] = true
			slog.Warn("snmp: neighbour table truncated", "target", n.target, "table", table, "limit", maxTableEntries)
		}
	case err != nil:
		slog.Warn("snmp: neighbour table not read", "target", n.target, "table", table, "error", err)
		n.err = err
	}
}

func (n *neighborWalk) neighbors(interfaces []plugins.NetworkInterface) []plugins.DeviceNeighbor {
	remotes := n.lldpRemotes()
	fdb := n.fdbEntries()
	if len(remotes) == 0 && len(fdb) == 0 {
		return nil
	}
	ports := n.localPorts(interfaces, len(remotes) > 0)
	var out []plugins.DeviceNeighbor
	for _, r := range remotes {
		nb := plugins.DeviceNeighbor{
			Protocol:        "lldp",
			LocalInterface:  ports.lldpPort(r.localPort),
			RemoteMAC:       r.mac(),
			RemoteIP:        r.ip(),
			RemoteHostname:  plugins.OctetText(r.sysName),
			RemoteInterface: r.port(),
		}
		// A neighbour without MAC, address and name breaks the contract.
		if nb.RemoteMAC != "" || nb.RemoteIP != "" || nb.RemoteHostname != "" {
			out = append(out, nb)
		}
	}
	seen := map[string]bool{}
	for _, e := range fdb {
		nb := plugins.DeviceNeighbor{Protocol: "fdb", LocalInterface: ports.bridgePort(e.port), RemoteMAC: e.mac}
		// A MAC learned in several VLANs on the same port is one neighbour.
		if key := nb.RemoteMAC + "|" + nb.LocalInterface; !seen[key] {
			seen[key] = true
			out = append(out, nb)
		}
	}
	return out
}

// lldpRemote is a row of lldpRemTable.
type lldpRemote struct {
	localPort      int
	chassisSubtype int
	chassisID      []byte
	portSubtype    int
	portID         []byte
	portDesc       []byte
	sysName        []byte
	manAddr        string
}

// lldpRemotes reads lldpRemTable and lldpRemManAddrTable. The rows are
// keyed by local port and remote index; lldpRemTimeMark (a TimeFilter) is
// ignored, so an agent listing a row under several time marks reports it
// once.
func (n *neighborWalk) lldpRemotes() []*lldpRemote {
	byKey := map[[2]int]*lldpRemote{}
	var order []*lldpRemote
	n.column("lldpRemTable", oidLldpRemChassisIDSubtype, func(idx []int, v gosnmp.SnmpPDU) {
		key, ok := lldpKey(idx)
		if !ok || byKey[key] != nil {
			return
		}
		r := &lldpRemote{localPort: key[0], chassisSubtype: pduInt(v)}
		byKey[key] = r
		order = append(order, r)
	})
	if len(order) == 0 {
		return nil
	}
	rows := func(column string, set func(r *lldpRemote, v gosnmp.SnmpPDU)) {
		n.column("lldpRemTable", column, func(idx []int, v gosnmp.SnmpPDU) {
			if key, ok := lldpKey(idx); ok && byKey[key] != nil {
				set(byKey[key], v)
			}
		})
	}
	rows(oidLldpRemChassisID, func(r *lldpRemote, v gosnmp.SnmpPDU) { r.chassisID = pduBytes(v) })
	rows(oidLldpRemPortIDSubtype, func(r *lldpRemote, v gosnmp.SnmpPDU) { r.portSubtype = pduInt(v) })
	rows(oidLldpRemPortID, func(r *lldpRemote, v gosnmp.SnmpPDU) { r.portID = pduBytes(v) })
	rows(oidLldpRemPortDesc, func(r *lldpRemote, v gosnmp.SnmpPDU) { r.portDesc = pduBytes(v) })
	rows(oidLldpRemSysName, func(r *lldpRemote, v gosnmp.SnmpPDU) { r.sysName = pduBytes(v) })
	// The management address is part of the index: timeMark, local port,
	// remote index, address subtype, address; the first one (IPv4 sorts
	// before IPv6) wins.
	n.column("lldpRemManAddrTable", oidLldpRemManAddrIfSubtype, func(idx []int, _ gosnmp.SnmpPDU) {
		key, ok := lldpKey(idx)
		if !ok || len(idx) < 5 || byKey[key] == nil || byKey[key].manAddr != "" {
			return
		}
		byKey[key].manAddr = indexAddress(idx[3], idx[4:])
	})
	return order
}

func lldpKey(idx []int) ([2]int, bool) {
	if len(idx) < 3 {
		return [2]int{}, false
	}
	return [2]int{idx[1], idx[2]}, true
}

// mac is the chassis ID of subtype macAddress.
func (r *lldpRemote) mac() string {
	if r.chassisSubtype != lldpChassisMAC {
		return ""
	}
	return octetMAC(r.chassisID)
}

// ip is the management address, else a chassis ID of subtype
// networkAddress (IANA address family 1 IPv4, 2 IPv6, then the address).
func (r *lldpRemote) ip() string {
	if r.manAddr != "" {
		return r.manAddr
	}
	if r.chassisSubtype == lldpChassisAddress && len(r.chassisID) > 0 {
		return addressOf(int(r.chassisID[0]), r.chassisID[1:])
	}
	return ""
}

// port is the remote port: its name or alias, else its description, else
// the port ID.
func (r *lldpRemote) port() string {
	id := plugins.OctetText(r.portID)
	if (r.portSubtype == lldpPortName || r.portSubtype == lldpPortAlias) && isPrintable(id) && id != "" {
		return id
	}
	if desc := plugins.OctetText(r.portDesc); desc != "" {
		return desc
	}
	if r.portSubtype == lldpPortMAC {
		return octetMAC(r.portID)
	}
	if isPrintable(id) {
		return id
	}
	return ""
}

// fdbEntry is a MAC the bridge learned on a bridge port.
type fdbEntry struct {
	mac  string
	port int
}

// fdbEntries reads the forwarding database: dot1qTpFdbTable (index FDB ID
// and MAC), without it dot1dTpFdbTable (index MAC). Entries of the bridge
// itself, invalid ones, multicast MACs and MACs without a port are no
// neighbours.
func (n *neighborWalk) fdbEntries() []fdbEntry {
	tables := []struct {
		name, port, status string
		indexLen           int
	}{
		{"dot1qTpFdbTable", oidDot1qTpFdbPort, oidDot1qTpFdbStatus, 7},
		{"dot1dTpFdbTable", oidDot1dTpFdbPort, oidDot1dTpFdbStatus, 6},
	}
	for _, t := range tables {
		type row struct {
			mac  string
			port int
		}
		rows := map[string]*row{}
		var order []string
		n.column(t.name, t.port, func(idx []int, v gosnmp.SnmpPDU) {
			if len(idx) != t.indexLen {
				return
			}
			key := joinIndex(idx)
			if rows[key] != nil {
				return
			}
			rows[key] = &row{mac: indexMAC(idx[t.indexLen-6:]), port: pduInt(v)}
			order = append(order, key)
		})
		if len(order) == 0 {
			continue
		}
		n.column(t.name, t.status, func(idx []int, v gosnmp.SnmpPDU) {
			if status := pduInt(v); status == fdbStatusInvalid || status == fdbStatusSelf {
				if r := rows[joinIndex(idx)]; r != nil {
					r.port = 0
				}
			}
		})
		var out []fdbEntry
		for _, key := range order {
			if r := rows[key]; r.mac != "" && r.port > 0 {
				out = append(out, fdbEntry{mac: r.mac, port: r.port})
			}
		}
		return out
	}
	return nil
}

// localPorts names the local ports of the device.
type localPorts struct {
	// names maps an ifIndex to the name of the interface in the record,
	// else its ifName; byName maps ifName and ifDescr (lower case) to it.
	names  map[int]string
	byName map[string]string
	// bridge maps dot1dBasePort to ifIndex; lldp holds lldpLocPortTable.
	bridge map[int]int
	lldp   map[int]*lldpLocalPort
}

type lldpLocalPort struct {
	subtype int
	id      string
	desc    string
}

func (n *neighborWalk) localPorts(interfaces []plugins.NetworkInterface, lldp bool) *localPorts {
	p := &localPorts{names: map[int]string{}, byName: map[string]string{}, bridge: map[int]int{}, lldp: map[int]*lldpLocalPort{}}
	for _, ifc := range interfaces {
		p.names[ifc.IfIndex] = ifc.Name
		p.byName[strings.ToLower(ifc.Name)] = ifc.Name
	}
	n.column("ifXTable", oidIfName, func(idx []int, v gosnmp.SnmpPDU) {
		name := plugins.OctetText(pduBytes(v))
		if len(idx) != 1 || name == "" {
			return
		}
		if p.names[idx[0]] == "" {
			p.names[idx[0]] = name
		}
		if _, taken := p.byName[strings.ToLower(name)]; !taken {
			p.byName[strings.ToLower(name)] = p.names[idx[0]]
		}
	})
	n.column("dot1dBasePortTable", oidDot1dBasePortIfIndex, func(idx []int, v gosnmp.SnmpPDU) {
		if len(idx) == 1 {
			p.bridge[idx[0]] = pduInt(v)
		}
	})
	if !lldp {
		return p
	}
	local := func(idx []int) *lldpLocalPort {
		if len(idx) != 1 {
			return nil
		}
		if p.lldp[idx[0]] == nil {
			p.lldp[idx[0]] = &lldpLocalPort{}
		}
		return p.lldp[idx[0]]
	}
	n.column("lldpLocPortTable", oidLldpLocPortIDSubtype, func(idx []int, v gosnmp.SnmpPDU) {
		if lp := local(idx); lp != nil {
			lp.subtype = pduInt(v)
		}
	})
	n.column("lldpLocPortTable", oidLldpLocPortID, func(idx []int, v gosnmp.SnmpPDU) {
		if lp := local(idx); lp != nil {
			lp.id = plugins.OctetText(pduBytes(v))
		}
	})
	n.column("lldpLocPortTable", oidLldpLocPortDesc, func(idx []int, v gosnmp.SnmpPDU) {
		if lp := local(idx); lp != nil {
			lp.desc = plugins.OctetText(pduBytes(v))
		}
	})
	return p
}

// lldpPort names the LLDP port lldpLocPortNum: the interface its port ID
// (name, alias or local) or description names, else the interface of the
// bridge port of the same number (LLDP-MIB LldpPortNumber), else the
// port's own name or description.
func (p *localPorts) lldpPort(num int) string {
	lp := p.lldp[num]
	if lp == nil {
		lp = &lldpLocalPort{}
	}
	var candidates []string
	if lp.subtype == lldpPortName || lp.subtype == lldpPortAlias || lp.subtype == lldpPortLocal {
		candidates = append(candidates, lp.id)
	}
	candidates = append(candidates, lp.desc)
	for _, c := range candidates {
		if name, ok := p.byName[strings.ToLower(c)]; ok && c != "" {
			return name
		}
	}
	if name := p.bridgePort(num); name != "" {
		return name
	}
	for _, c := range candidates {
		if c != "" && isPrintable(c) {
			return c
		}
	}
	return ""
}

// bridgePort names the interface of a bridge port; empty when the bridge
// does not map the port to an interface.
func (p *localPorts) bridgePort(port int) string {
	if ifIndex, ok := p.bridge[port]; ok {
		return p.names[ifIndex]
	}
	return ""
}

// oidIndex returns the sub-identifiers of name after column.
func oidIndex(name, column string) ([]int, bool) {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(name, "."), column+".")
	if !ok {
		return nil, false
	}
	parts := strings.Split(rest, ".")
	idx := make([]int, len(parts))
	for i, s := range parts {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return nil, false
		}
		idx[i] = int(v)
	}
	return idx, true
}

func joinIndex(idx []int) string {
	parts := make([]string, len(idx))
	for i, v := range idx {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ".")
}

// indexMAC is the unicast MAC of six sub-identifiers.
func indexMAC(idx []int) string {
	mac := make(net.HardwareAddr, 0, 6)
	for _, v := range idx {
		if v > 255 {
			return ""
		}
		mac = append(mac, byte(v))
	}
	if len(mac) != 6 || mac[0]&1 == 1 || mac.String() == "00:00:00:00:00:00" {
		return ""
	}
	return mac.String()
}

// indexAddress decodes an address of an index: the length, then the
// octets (some agents leave the length out).
func indexAddress(family int, idx []int) string {
	size := net.IPv4len
	if family == 2 {
		size = net.IPv6len
	}
	if len(idx) == size+1 && idx[0] == size {
		idx = idx[1:]
	}
	b := make([]byte, 0, len(idx))
	for _, v := range idx {
		if v > 255 {
			return ""
		}
		b = append(b, byte(v))
	}
	return addressOf(family, b)
}

// addressOf renders an address of IANA family 1 (IPv4) or 2 (IPv6).
func addressOf(family int, b []byte) string {
	if (family == 1 && len(b) == net.IPv4len) || (family == 2 && len(b) == net.IPv6len) {
		return net.IP(b).String()
	}
	return ""
}

// octetMAC reads a MAC from six octets or from its text form.
func octetMAC(b []byte) string {
	if len(b) == 6 {
		return net.HardwareAddr(b).String()
	}
	if hw, err := net.ParseMAC(strings.TrimSpace(string(b))); err == nil && len(hw) == 6 {
		return hw.String()
	}
	return ""
}

func pduInt(v gosnmp.SnmpPDU) int { return int(gosnmp.ToBigInt(v.Value).Int64()) }

func pduBytes(v gosnmp.SnmpPDU) []byte {
	b, _ := v.Value.([]byte)
	return b
}
