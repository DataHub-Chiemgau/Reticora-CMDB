// Package fakesnmp is an SNMPv2c agent for the real-stack test
// frontend/e2e-real/onboarding-bundle.spec.ts (WP-384): it serves sysDescr,
// sysObjectID and sysName to requests with its community only and records
// the community of every request it receives, so the test can prove which
// secret the collector sent (COL-02). The PDU handling follows the test
// agent of collector/plugins/snmp/v3_agent_test.go. It is test
// infrastructure: only collector/cmd/fakesnmp and tests (plugins/snmp
// TestCollectFromSNMPFake) use it, the release build (./cmd/collector) does
// not.
package fakesnmp

import (
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
)

// OIDs the agent serves (SNMPv2-MIB system group).
const (
	OIDSysDescr    = ".1.3.6.1.2.1.1.1.0"
	OIDSysObjectID = ".1.3.6.1.2.1.1.2.0"
	OIDSysName     = ".1.3.6.1.2.1.1.5.0"
)

// Config is what the agent answers and to whom.
type Config struct {
	// Community is the only community the agent answers.
	Community   string
	SysDescr    string
	SysObjectID string
	SysName     string
	// OnRequest, if set, is called with every request the agent received,
	// answered or not.
	OnRequest func(Request)
}

// Request is a received packet as the agent saw it.
type Request struct {
	At   time.Time `json:"at"`
	From string    `json:"from"`
	// Version is "1", "2c" or "3"; empty for a packet that is no SNMP
	// message.
	Version string `json:"version"`
	// Community is the community of an SNMPv1 or v2c message.
	Community string   `json:"community"`
	PDU       string   `json:"pdu,omitempty"`
	OIDs      []string `json:"oids,omitempty"`
	Answered  bool     `json:"answered"`
}

// Agent is a running fake agent.
type Agent struct {
	cfg  Config
	conn *net.UDPConn
	mib  []gosnmp.SnmpPDU // sorted by OID

	mu   sync.Mutex
	seen []Request
}

// Listen binds the agent to addr (host:port, UDP); Serve answers.
func Listen(addr string, cfg Config) (*Agent, error) {
	if cfg.Community == "" {
		return nil, errors.New("fakesnmp: a community is required")
	}
	udp, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udp)
	if err != nil {
		return nil, err
	}
	mib := []gosnmp.SnmpPDU{
		{Name: OIDSysDescr, Type: gosnmp.OctetString, Value: []byte(cfg.SysDescr)},
		{Name: OIDSysObjectID, Type: gosnmp.ObjectIdentifier, Value: cfg.SysObjectID},
		{Name: OIDSysName, Type: gosnmp.OctetString, Value: []byte(cfg.SysName)},
	}
	slices.SortFunc(mib, func(a, b gosnmp.SnmpPDU) int { return compareOID(a.Name, b.Name) })
	return &Agent{cfg: cfg, conn: conn, mib: mib}, nil
}

// Addr is the address the agent listens on.
func (a *Agent) Addr() *net.UDPAddr { return a.conn.LocalAddr().(*net.UDPAddr) }

// Close stops Serve.
func (a *Agent) Close() error { return a.conn.Close() }

// Requests returns the requests received so far.
func (a *Agent) Requests() []Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.seen)
}

// Serve answers requests until Close.
func (a *Agent) Serve() error {
	buf := make([]byte, 65535)
	for {
		n, peer, err := a.conn.ReadFromUDP(buf)
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		out, req := a.handle(buf[:n])
		req.At, req.From = time.Now().UTC(), peer.String()
		if out != nil {
			_, err = a.conn.WriteToUDP(out, peer)
			req.Answered = err == nil
		}
		a.mu.Lock()
		a.seen = append(a.seen, req)
		a.mu.Unlock()
		if a.cfg.OnRequest != nil {
			a.cfg.OnRequest(req)
		}
	}
}

// handle returns the answer to a packet (nil: none) and what it carried.
// Only SNMPv2c GET, GETNEXT and GETBULK with the agent's community are
// answered; another community, another version and a packet that does not
// decode stay unanswered, like a real agent.
func (a *Agent) handle(raw []byte) ([]byte, Request) {
	var req Request
	version, ok := messageVersion(raw)
	switch {
	case !ok:
		return nil, req
	case version == int(gosnmp.Version3):
		// The USM header is not decoded; the version is enough to tell
		// the test that the message was no v2c request.
		req.Version = "3"
		return nil, req
	case version == int(gosnmp.Version1):
		req.Version = "1"
	case version == int(gosnmp.Version2c):
		req.Version = "2c"
	default:
		return nil, req
	}
	decoder := &gosnmp.GoSNMP{Version: gosnmp.SnmpVersion(version), Logger: gosnmp.NewLogger(nil)}
	pkt, err := decoder.SnmpDecodePacket(raw)
	if err != nil {
		return nil, req
	}
	req.Community, req.PDU = pkt.Community, pkt.PDUType.String()
	for _, v := range pkt.Variables {
		req.OIDs = append(req.OIDs, v.Name)
	}
	if pkt.Version != gosnmp.Version2c || pkt.Community != a.cfg.Community {
		return nil, req
	}
	var vars []gosnmp.SnmpPDU
	switch pkt.PDUType {
	case gosnmp.GetRequest:
		for _, v := range pkt.Variables {
			vars = append(vars, a.lookup(v.Name))
		}
	case gosnmp.GetNextRequest:
		for _, v := range pkt.Variables {
			vars = append(vars, a.next(v.Name))
		}
	case gosnmp.GetBulkRequest:
		vars = a.bulk(pkt.Variables, int(pkt.NonRepeaters), int(pkt.MaxRepetitions))
	default:
		return nil, req
	}
	resp := &gosnmp.SnmpPacket{
		Version: gosnmp.Version2c, Community: pkt.Community, PDUType: gosnmp.GetResponse,
		RequestID: pkt.RequestID, Variables: vars, Logger: gosnmp.NewLogger(nil),
	}
	out, err := resp.MarshalMsg()
	if err != nil {
		return nil, req
	}
	return out, req
}

// lookup returns the value of name, or noSuchObject.
func (a *Agent) lookup(name string) gosnmp.SnmpPDU {
	for _, v := range a.mib {
		if compareOID(v.Name, name) == 0 {
			return v
		}
	}
	return gosnmp.SnmpPDU{Name: name, Type: gosnmp.NoSuchObject}
}

// next returns the first value after name in OID order, or endOfMibView.
func (a *Agent) next(name string) gosnmp.SnmpPDU {
	for _, v := range a.mib {
		if compareOID(name, v.Name) < 0 {
			return v
		}
	}
	return gosnmp.SnmpPDU{Name: name, Type: gosnmp.EndOfMibView}
}

// bulk answers GETBULK (RFC 3416 4.2.3): the first nonRepeaters variables
// once, the others up to maxRepetitions times, interleaved.
func (a *Agent) bulk(vars []gosnmp.SnmpPDU, nonRepeaters, maxRepetitions int) []gosnmp.SnmpPDU {
	nonRepeaters = min(max(nonRepeaters, 0), len(vars))
	var out []gosnmp.SnmpPDU
	for _, v := range vars[:nonRepeaters] {
		out = append(out, a.next(v.Name))
	}
	cursor := make([]string, 0, len(vars)-nonRepeaters)
	for _, v := range vars[nonRepeaters:] {
		cursor = append(cursor, v.Name)
	}
	for range max(maxRepetitions, 0) {
		ended := true
		for i, name := range cursor {
			v := a.next(name)
			out = append(out, v)
			if v.Type != gosnmp.EndOfMibView {
				cursor[i], ended = v.Name, false
			}
		}
		if ended {
			break
		}
	}
	return out
}

// messageVersion reads the version of an SNMP message: SEQUENCE { INTEGER
// version, ... }.
func messageVersion(raw []byte) (int, bool) {
	if len(raw) < 2 || raw[0] != 0x30 {
		return 0, false
	}
	i := 2
	if raw[1]&0x80 != 0 {
		i += int(raw[1] & 0x7f)
	}
	if len(raw) < i+3 || raw[i] != 0x02 || raw[i+1] != 0x01 {
		return 0, false
	}
	return int(raw[i+2]), true
}

// compareOID orders dotted OIDs numerically.
func compareOID(a, b string) int {
	pa, pb := strings.Split(strings.Trim(a, "."), "."), strings.Split(strings.Trim(b, "."), ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x - y
		}
	}
	return len(pa) - len(pb)
}
