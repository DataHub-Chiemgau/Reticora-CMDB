package snmp

import (
	"net"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// v3Agent is a minimal SNMPv3 agent for tests: it answers the engine
// discovery with a Report and authPriv GET/GETBULK requests of its user
// from a fixed MIB; requests with wrong credentials stay unanswered.
type v3Agent struct {
	conn     *net.UDPConn
	engineID string
	params   *gosnmp.UsmSecurityParameters
	mib      []gosnmp.SnmpPDU // sorted by OID
}

func startV3Agent(t *testing.T, user, authPass, privPass string, mib []gosnmp.SnmpPDU) *v3Agent {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	a := &v3Agent{
		conn:     conn,
		engineID: "\x80\x00\x1f\x88\x80reticora-test",
		mib:      mib,
	}
	a.params = &gosnmp.UsmSecurityParameters{
		UserName:                 user,
		AuthenticationProtocol:   gosnmp.SHA256,
		AuthenticationPassphrase: authPass,
		PrivacyProtocol:          gosnmp.AES,
		PrivacyPassphrase:        privPass,
		AuthoritativeEngineID:    a.engineID,
		AuthoritativeEngineBoots: 1,
		AuthoritativeEngineTime:  1000,
	}
	if err = a.params.InitSecurityKeys(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go a.serve()
	return a
}

func (a *v3Agent) port() int { return a.conn.LocalAddr().(*net.UDPAddr).Port }

func (a *v3Agent) serve() {
	buf := make([]byte, 65535)
	for {
		n, peer, err := a.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if out := a.handle(buf[:n]); out != nil {
			_, _ = a.conn.WriteToUDP(out, peer)
		}
	}
}

func (a *v3Agent) decoder() *gosnmp.GoSNMP {
	return &gosnmp.GoSNMP{
		Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.AuthPriv,
		SecurityParameters: a.params.Copy(), Logger: gosnmp.NewLogger(nil),
	}
}

func (a *v3Agent) handle(raw []byte) []byte {
	req, err := a.decoder().SnmpDecodePacket(raw)
	if err != nil {
		return nil // wrong user, key or corrupt packet
	}
	if req.MsgFlags&gosnmp.AuthPriv != gosnmp.AuthPriv {
		// Engine discovery: report the engine id, boots and time.
		resp := &gosnmp.SnmpPacket{
			Version: gosnmp.Version3, MsgFlags: gosnmp.NoAuthNoPriv, SecurityModel: gosnmp.UserSecurityModel,
			SecurityParameters: &gosnmp.UsmSecurityParameters{
				AuthoritativeEngineID: a.engineID, AuthoritativeEngineBoots: 1, AuthoritativeEngineTime: 1000,
			},
			MsgID: req.MsgID, RequestID: req.RequestID, MsgMaxSize: 65507, PDUType: gosnmp.Report,
			ContextEngineID: a.engineID,
			Variables:       []gosnmp.SnmpPDU{{Name: ".1.3.6.1.6.3.15.1.1.4.0", Type: gosnmp.Counter32, Value: uint32(1)}},
		}
		out, _ := resp.MarshalMsg()
		return out
	}
	usm, ok := req.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if !ok || usm.UserName != a.params.UserName {
		return nil
	}
	var vars []gosnmp.SnmpPDU
	switch req.PDUType {
	case gosnmp.GetRequest:
		for _, v := range req.Variables {
			vars = append(vars, a.lookup(v.Name))
		}
	case gosnmp.GetNextRequest, gosnmp.GetBulkRequest:
		limit := 1
		if req.PDUType == gosnmp.GetBulkRequest {
			limit = int(req.MaxRepetitions)
		}
		next := req.Variables[0].Name
		for range limit {
			v, found := a.next(next)
			if !found {
				vars = append(vars, gosnmp.SnmpPDU{Name: next, Type: gosnmp.EndOfMibView})
				break
			}
			vars = append(vars, v)
			next = v.Name
		}
	default:
		return nil
	}
	sp := a.params.Copy().(*gosnmp.UsmSecurityParameters)
	resp := &gosnmp.SnmpPacket{
		Version: gosnmp.Version3, MsgFlags: gosnmp.AuthPriv, SecurityModel: gosnmp.UserSecurityModel,
		SecurityParameters: sp, MsgID: req.MsgID, RequestID: req.RequestID, MsgMaxSize: 65507,
		PDUType: gosnmp.GetResponse, ContextEngineID: a.engineID, ContextName: req.ContextName, Variables: vars,
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

func (a *v3Agent) lookup(name string) gosnmp.SnmpPDU {
	for _, v := range a.mib {
		if v.Name == name {
			return v
		}
	}
	return gosnmp.SnmpPDU{Name: name, Type: gosnmp.NoSuchObject}
}

// next returns the first MIB entry after name in OID order.
func (a *v3Agent) next(name string) (gosnmp.SnmpPDU, bool) {
	for _, v := range a.mib {
		if oidLess(name, v.Name) {
			return v, true
		}
	}
	return gosnmp.SnmpPDU{}, false
}
