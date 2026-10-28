// Package fakesnmp is an SNMPv2c agent for the real-stack test
// frontend/e2e-real/onboarding-bundle.spec.ts (WP-384): it serves sysDescr,
// sysObjectID and sysName to requests with its community only and records
// the community of every request it receives, so the test can prove which
// secret the collector sent (COL-02). The PDU handling follows the test
// agent of collector/plugins/snmp/v3_agent_test.go. It is test
// infrastructure: only collector/cmd/fakesnmp and tests (plugins/snmp
// TestCollectFromSNMPFake, plugins/power) use it, the release build
// (./cmd/collector) does not.
//
// Bound with ListenV3 it is an SNMPv3 agent of one USM user at authPriv for
// frontend/e2e-real/discovery-errors.spec.ts (WP-542, DIS-08), answering
// like the agent startRFC3414Agent of plugins/snmp/v3_agent_test.go
// (WP-492): the engine discovery gets the report usmStatsUnknownEngineIDs
// with engine ID, boots and time; a request of the user it cannot
// authenticate or decrypt with the user's keys gets the unauthenticated
// report usmStatsWrongDigests (another user: usmStatsUnknownUserNames,
// RFC 3414 3.2); a request with the user's keys gets the authPriv
// response.
package fakesnmp

import (
	"errors"
	"fmt"
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

// usmStats report counters (RFC 3414 3.2, SNMP-USER-BASED-SM-MIB) of the
// SNMPv3 mode.
const (
	OIDUsmStatsUnknownUserNames = ".1.3.6.1.6.3.15.1.1.3.0"
	OIDUsmStatsUnknownEngineIDs = ".1.3.6.1.6.3.15.1.1.4.0"
	OIDUsmStatsWrongDigests     = ".1.3.6.1.6.3.15.1.1.5.0"
)

// usmReportNames name the report counters in Request.Report.
var usmReportNames = map[string]string{
	OIDUsmStatsUnknownUserNames: "usmStatsUnknownUserNames",
	OIDUsmStatsUnknownEngineIDs: "usmStatsUnknownEngineIDs",
	OIDUsmStatsWrongDigests:     "usmStatsWrongDigests",
}

// The engine of the SNMPv3 mode: a fixed ID, boots and time, like the test
// agent of plugins/snmp/v3_agent_test.go.
const (
	engineID    = "\x80\x00\x1f\x88\x80reticora-fake"
	engineBoots = 1
	engineTime  = 1000
)

// V3 is the one USM user of the SNMPv3 mode, at security level authPriv.
type V3 struct {
	User           string
	AuthProtocol   gosnmp.SnmpV3AuthProtocol
	AuthPassphrase string
	PrivProtocol   gosnmp.SnmpV3PrivProtocol
	PrivPassphrase string
}

// Config is what the agent answers and to whom.
type Config struct {
	// Community is the only community the agent answers (SNMPv2c); empty
	// in the SNMPv3 mode (ListenV3), then no v2c request is answered.
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
	Community string `json:"community"`
	// User is the USM user name of an SNMPv3 message (empty for the
	// engine discovery).
	User string `json:"user,omitempty"`
	// PDU and OIDs of an SNMPv1/v2c message, and of an SNMPv3 message the
	// agent authenticated and decrypted.
	PDU  string   `json:"pdu,omitempty"`
	OIDs []string `json:"oids,omitempty"`
	// Report is the usmStats counter (e.g. usmStatsWrongDigests) the agent
	// answered an SNMPv3 message with instead of a response.
	Report   string `json:"report,omitempty"`
	Answered bool   `json:"answered"`
}

// Agent is a running fake agent.
type Agent struct {
	cfg  Config
	conn *net.UDPConn
	mib  []gosnmp.SnmpPDU // sorted by OID
	// usm holds the keys of the V3 user; nil without the SNMPv3 mode.
	usm *gosnmp.UsmSecurityParameters

	mu   sync.Mutex
	seen []Request
}

// Listen binds the agent to addr (host:port, UDP); Serve answers. extra
// are further variables the agent serves (names with a leading dot), e.g.
// the UPS-MIB identity of a power device.
func Listen(addr string, cfg Config, extra ...gosnmp.SnmpPDU) (*Agent, error) {
	if cfg.Community == "" {
		return nil, errors.New("fakesnmp: a community is required")
	}
	return listen(addr, &cfg, nil, extra)
}

// ListenV3 binds the agent in the SNMPv3 mode (WP-542): it answers the user
// v3 (authPriv) and, only if cfg names a community, SNMPv2c requests with it.
func ListenV3(addr string, v3 *V3, cfg Config, extra ...gosnmp.SnmpPDU) (*Agent, error) {
	if v3 == nil || v3.User == "" || v3.AuthPassphrase == "" || v3.PrivPassphrase == "" ||
		v3.AuthProtocol <= gosnmp.NoAuth || v3.PrivProtocol <= gosnmp.NoPriv {
		return nil, errors.New("fakesnmp: the SNMPv3 user needs a name, auth and privacy protocols and passphrases (authPriv)")
	}
	usm := &gosnmp.UsmSecurityParameters{
		UserName: v3.User, AuthenticationProtocol: v3.AuthProtocol, AuthenticationPassphrase: v3.AuthPassphrase,
		PrivacyProtocol: v3.PrivProtocol, PrivacyPassphrase: v3.PrivPassphrase,
		AuthoritativeEngineID: engineID, AuthoritativeEngineBoots: engineBoots, AuthoritativeEngineTime: engineTime,
		Logger: gosnmp.NewLogger(nil),
	}
	if err := usm.InitSecurityKeys(); err != nil {
		return nil, fmt.Errorf("fakesnmp: SNMPv3 keys: %w", err)
	}
	return listen(addr, &cfg, usm, extra)
}

// listen binds the agent; usm holds the keys of the SNMPv3 user, nil
// without the SNMPv3 mode.
func listen(addr string, cfg *Config, usm *gosnmp.UsmSecurityParameters, extra []gosnmp.SnmpPDU) (*Agent, error) {
	udp, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udp)
	if err != nil {
		return nil, err
	}
	mib := append([]gosnmp.SnmpPDU{
		{Name: OIDSysDescr, Type: gosnmp.OctetString, Value: []byte(cfg.SysDescr)},
		{Name: OIDSysObjectID, Type: gosnmp.ObjectIdentifier, Value: cfg.SysObjectID},
		{Name: OIDSysName, Type: gosnmp.OctetString, Value: []byte(cfg.SysName)},
	}, extra...)
	slices.SortFunc(mib, func(a, b gosnmp.SnmpPDU) int { return compareOID(a.Name, b.Name) })
	return &Agent{cfg: *cfg, conn: conn, mib: mib, usm: usm}, nil
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
// answered, and in the SNMPv3 mode the SNMPv3 messages (handleV3); another
// community, another version and a packet that does not decode stay
// unanswered, like a real agent.
func (a *Agent) handle(raw []byte) ([]byte, Request) {
	var req Request
	version, ok := messageVersion(raw)
	switch {
	case !ok:
		return nil, req
	case version == int(gosnmp.Version3):
		req.Version = "3"
		if a.usm == nil {
			// Without the SNMPv3 mode the USM header is not decoded; the
			// version tells the test that the message was no v2c request.
			return nil, req
		}
		out := a.handleV3(raw, &req)
		return out, req
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
	if pkt.Version != gosnmp.Version2c || a.cfg.Community == "" || pkt.Community != a.cfg.Community {
		return nil, req
	}
	vars, ok := a.answer(pkt)
	if !ok {
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

// answer returns the variables of the response to a GET, GETNEXT or
// GETBULK; ok is false for another PDU.
func (a *Agent) answer(pkt *gosnmp.SnmpPacket) (vars []gosnmp.SnmpPDU, ok bool) {
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
		return nil, false
	}
	return vars, true
}

// handleV3 answers an SNMPv3 message after RFC 3414 3.2: the engine
// discovery (no authentication) with the report usmStatsUnknownEngineIDs, a
// message of another user with usmStatsUnknownUserNames, a message of the
// user that does not authenticate or decrypt with its keys with
// usmStatsWrongDigests – the reports are unauthenticated and echo the
// msgID – and a GET, GETNEXT or GETBULK of the user with the authPriv
// response.
func (a *Agent) handleV3(raw []byte, req *Request) []byte {
	// The header decodes without keys (gosnmp wants some user name for
	// it); gosnmp decrypts in place, so every decode gets its own copy.
	hdr, _ := v3Decoder(gosnmp.NoAuthNoPriv, &gosnmp.UsmSecurityParameters{UserName: "fakesnmp", Logger: gosnmp.NewLogger(nil)}).
		SnmpDecodePacket(slices.Clone(raw))
	if hdr == nil || hdr.Version != gosnmp.Version3 || hdr.MsgID == 0 {
		return nil
	}
	if usm, ok := hdr.SecurityParameters.(*gosnmp.UsmSecurityParameters); ok {
		req.User = usm.UserName
	}
	switch {
	case hdr.MsgFlags&gosnmp.AuthNoPriv == 0:
		return report(hdr, req, OIDUsmStatsUnknownEngineIDs)
	case req.User != a.usm.UserName:
		return report(hdr, req, OIDUsmStatsUnknownUserNames)
	}
	// SnmpDecodePacket does not check the digest; UnmarshalTrap does, with
	// the decoder's keys, before it decrypts.
	pkt, err := v3Decoder(gosnmp.AuthPriv, a.usm.Copy()).UnmarshalTrap(slices.Clone(raw), false)
	if err != nil || pkt == nil {
		return report(hdr, req, OIDUsmStatsWrongDigests)
	}
	req.PDU = pkt.PDUType.String()
	for _, v := range pkt.Variables {
		req.OIDs = append(req.OIDs, v.Name)
	}
	vars, ok := a.answer(pkt)
	if !ok {
		return nil
	}
	sp := a.usm.Copy().(*gosnmp.UsmSecurityParameters)
	resp := &gosnmp.SnmpPacket{
		Version: gosnmp.Version3, MsgFlags: gosnmp.AuthPriv, SecurityModel: gosnmp.UserSecurityModel,
		SecurityParameters: sp, MsgID: pkt.MsgID, RequestID: pkt.RequestID, MsgMaxSize: 65507,
		PDUType: gosnmp.GetResponse, ContextEngineID: engineID, ContextName: pkt.ContextName, Variables: vars,
		Logger: gosnmp.NewLogger(nil),
	}
	if err = sp.InitPacket(resp); err != nil {
		return nil
	}
	out, err := resp.MarshalMsg()
	if err != nil {
		return nil
	}
	return out
}

// v3Decoder decodes SNMPv3 messages with the security parameters sp.
func v3Decoder(flags gosnmp.SnmpV3MsgFlags, sp gosnmp.SnmpV3SecurityParameters) *gosnmp.GoSNMP {
	return &gosnmp.GoSNMP{Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: flags,
		SecurityParameters: sp, Logger: gosnmp.NewLogger(nil)}
}

// report is the unauthenticated Report PDU of the usmStats counter oid to
// the message hdr: it echoes msgID, requestID and the user name and
// carries the engine's ID, boots and time.
func report(hdr *gosnmp.SnmpPacket, req *Request, oid string) []byte {
	req.Report = usmReportNames[oid]
	out, err := (&gosnmp.SnmpPacket{
		Version: gosnmp.Version3, MsgFlags: gosnmp.NoAuthNoPriv, SecurityModel: gosnmp.UserSecurityModel,
		SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: req.User, AuthoritativeEngineID: engineID,
			AuthoritativeEngineBoots: engineBoots, AuthoritativeEngineTime: engineTime, Logger: gosnmp.NewLogger(nil)},
		MsgID: hdr.MsgID, RequestID: hdr.RequestID, MsgMaxSize: 65507, PDUType: gosnmp.Report, ContextEngineID: engineID,
		Variables: []gosnmp.SnmpPDU{{Name: oid, Type: gosnmp.Counter32, Value: uint32(1)}},
		Logger:    gosnmp.NewLogger(nil),
	}).MarshalMsg()
	if err != nil {
		return nil
	}
	return out
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
