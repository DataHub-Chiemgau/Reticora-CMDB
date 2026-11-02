package snmp

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
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

// rfc3414Agent wraps the v3 test agent and answers like an agent after
// RFC 3414 3.2: a request it cannot process (wrong keys) gets the
// unauthenticated Report PDU of the counter report. With stale > 0 the
// first stale authenticated requests of the right user get an
// authenticated usmStatsNotInTimeWindows report instead of the response,
// like an agent whose engine time the client has not synchronised yet.
type rfc3414Agent struct {
	port        int
	reports     atomic.Int32 // reports sent to rejected requests
	discoveries atomic.Int32 // engine discoveries received
	stale       atomic.Int32
}

func startRFC3414Agent(t *testing.T, report string, stale int32) *rfc3414Agent {
	t.Helper()
	inner := startV3Agent(t, "reticora", "auth-secret-1", "priv-secret-1", v3TestMIB())
	// Stop the inner agent's serve loop; its handler serves the new socket.
	_ = inner.conn.Close()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	a := &rfc3414Agent{port: conn.LocalAddr().(*net.UDPAddr).Port}
	a.stale.Store(stale)
	reportTo := func(req *gosnmp.SnmpPacket, flags gosnmp.SnmpV3MsgFlags, counter string) []byte {
		sp := &gosnmp.UsmSecurityParameters{AuthoritativeEngineID: inner.engineID, AuthoritativeEngineBoots: 1, AuthoritativeEngineTime: 1000}
		if usm, ok := req.SecurityParameters.(*gosnmp.UsmSecurityParameters); ok {
			sp.UserName = usm.UserName
		}
		if flags&gosnmp.AuthNoPriv != 0 {
			sp = inner.params.Copy().(*gosnmp.UsmSecurityParameters)
		}
		pkt := &gosnmp.SnmpPacket{
			Version: gosnmp.Version3, MsgFlags: flags, SecurityModel: gosnmp.UserSecurityModel, SecurityParameters: sp,
			MsgID: req.MsgID, RequestID: req.RequestID, MsgMaxSize: 65507, PDUType: gosnmp.Report, ContextEngineID: inner.engineID,
			Variables: []gosnmp.SnmpPDU{{Name: "." + counter, Type: gosnmp.Counter32, Value: uint32(1)}},
		}
		if flags&gosnmp.AuthNoPriv != 0 && sp.InitPacket(pkt) != nil {
			return nil
		}
		out, merr := pkt.MarshalMsg()
		if merr != nil {
			return nil
		}
		return out
	}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, peer, rerr := conn.ReadFromUDP(buf)
			if rerr != nil {
				return
			}
			raw := buf[:n]
			// The decoder decrypts in place: decode a copy for the header.
			req, _ := inner.decoder().SnmpDecodePacket(slices.Clone(raw))
			if req == nil || req.MsgID == 0 {
				continue
			}
			out := inner.handle(raw)
			switch {
			case req.MsgFlags&gosnmp.AuthNoPriv == 0: // engine discovery
				a.discoveries.Add(1)
			case out == nil:
				out = reportTo(req, gosnmp.NoAuthNoPriv, report)
				a.reports.Add(1)
			case a.stale.Add(-1) >= 0:
				out = reportTo(req, gosnmp.AuthNoPriv, oidUsmStatsNotInTimeWindows)
			}
			if out != nil {
				_, _ = conn.WriteToUDP(out, peer)
			}
		}
	}()
	return a
}

var (
	v3Good = map[string]string{"username": "reticora", "auth_protocol": "SHA256", "auth_password": "auth-secret-1",
		"priv_protocol": "AES", "priv_password": "priv-secret-1"}
	v3Bad = map[string]string{"username": "reticora", "auth_protocol": "SHA256", "auth_password": "wrong-auth-pw",
		"priv_protocol": "AES", "priv_password": "wrong-priv-pw"}
)

// TestAuditSNMPv3AuthFailureReported covers WP-492 (DIS-04, DIS-08; F-105,
// moved from v3_auth_failure_auditdefect_test.go): an SNMPv3 agent that
// refuses the credentials answers, as RFC 3414 3.2 requires, with an
// unauthenticated Report PDU usmStatsWrongDigests, usmStatsUnknownUserNames
// or usmStatsDecryptionErrors. The probe keeps the answering agent as a hit
// and Collect fails with plugins.ErrAuthFailed and the target, which the
// scope run reports as cause auth_failed with protocol snmp. The correct
// credentials are the control.
func TestAuditSNMPv3AuthFailureReported(t *testing.T) {
	const target = "127.0.0.1"
	for _, counter := range []string{oidUsmStatsWrongDigests, oidUsmStatsUnknownUserNames, oidUsmStatsDecryptionErrors} {
		name := usmAuthReports[counter]
		t.Run(name, func(t *testing.T) {
			agent := startRFC3414Agent(t, counter, 0)
			p := &Plugin{Timeout: time.Second, Port: agent.port}

			if hits, err := p.Probe(context.Background(), []string{target}, v3Good); err != nil || len(hits) != 1 {
				t.Fatalf("control: correct credentials: hits=%d err=%v", len(hits), err)
			}
			if rec, err := p.Collect(context.Background(), target, v3Good); err != nil || rec.System.Name != "core-sw-1" {
				t.Fatalf("control: correct credentials: collect %+v, %v", rec, err)
			}

			// Control: the agent answers the engine discovery and rejects the
			// authenticated request; a plain gosnmp client gets an error
			// (gosnmp v1.43 discards the unauthenticated report as "not
			// authentic").
			raw := &gosnmp.GoSNMP{Target: target, Port: uint16(agent.port), Version: gosnmp.Version3, Timeout: time.Second, Retries: 0,
				SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.AuthPriv, Logger: gosnmp.NewLogger(nil),
				SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: "reticora", AuthenticationProtocol: gosnmp.SHA256,
					AuthenticationPassphrase: v3Bad["auth_password"], PrivacyProtocol: gosnmp.AES, PrivacyPassphrase: v3Bad["priv_password"]}}
			if cerr := raw.Connect(); cerr != nil {
				t.Fatal(cerr)
			}
			_, gerr := raw.Get([]string{".1.3.6.1.2.1.1.5.0"})
			_ = raw.Conn.Close()
			if gerr == nil || agent.reports.Load() == 0 {
				t.Fatalf("control: a gosnmp client with the wrong key got err=%v after %d report(s), want a rejection", gerr, agent.reports.Load())
			}

			hits, err := p.Probe(context.Background(), []string{target}, v3Bad)
			if err != nil || len(hits) != 1 || hits[0].IP != target {
				t.Fatalf("DIS-08: the agent at %s refused the credentials with %s, but Probe returned %+v, err=%v (want the answering agent as hit)",
					target, name, hits, err)
			}
			before := agent.reports.Load()
			rec, err := p.Collect(context.Background(), target, v3Bad)
			if !errors.Is(err, plugins.ErrAuthFailed) || plugins.ErrorCause(err) != "auth_failed" {
				t.Fatalf("DIS-08: Collect with refused credentials = %+v, %v; want plugins.ErrAuthFailed", rec, err)
			}
			if msg := err.Error(); !strings.Contains(msg, target) || !strings.Contains(msg, name) {
				t.Errorf("error %q names neither the target %s nor the report %s", msg, target, name)
			}
			if agent.reports.Load() == before {
				t.Error("Collect sent no request the agent refused")
			}
		})
	}
}

// TestSNMPv3NotInTimeWindowResync covers WP-492 (DIS-04): a request the
// agent refuses with usmStatsNotInTimeWindows is retried after a time
// resync (a new engine discovery); only a refusal after the resync is an
// error, and that is no credential failure.
func TestSNMPv3NotInTimeWindowResync(t *testing.T) {
	const target = "127.0.0.1"
	collect := func(agent *rfc3414Agent, creds map[string]string) (int32, error) {
		p := &Plugin{Timeout: time.Second, Port: agent.port}
		before := agent.discoveries.Load()
		rec, err := p.Collect(context.Background(), target, creds)
		if err == nil && rec.System.Name != "core-sw-1" {
			t.Errorf("record = %+v", rec)
		}
		return agent.discoveries.Load() - before, err
	}

	// gosnmp resynchronises on the first authenticated report itself.
	if _, err := collect(startRFC3414Agent(t, oidUsmStatsWrongDigests, 1), v3Good); err != nil {
		t.Errorf("one stale request: %v", err)
	}
	// A second report after gosnmp's resync: the session resynchronises with
	// a new engine discovery and succeeds.
	if discoveries, err := collect(startRFC3414Agent(t, oidUsmStatsWrongDigests, 2), v3Good); err != nil || discoveries != 2 {
		t.Errorf("two stale requests: discoveries=%d err=%v, want a resync with a second discovery and success", discoveries, err)
	}
	// An agent that stays out of its time window: error after the resync.
	agent := startRFC3414Agent(t, oidUsmStatsWrongDigests, 1<<20)
	discoveries, err := collect(agent, v3Good)
	if !errors.Is(err, errNotInTimeWindow) || errors.Is(err, plugins.ErrAuthFailed) || discoveries != 2 ||
		!strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "after a time resync") {
		t.Errorf("persistent usmStatsNotInTimeWindows: discoveries=%d err=%v, want the error after one resync", discoveries, err)
	}
	if hits, perr := (&Plugin{Timeout: time.Second, Port: agent.port}).Probe(context.Background(), []string{target}, v3Good); perr != nil || len(hits) != 1 {
		t.Errorf("persistent usmStatsNotInTimeWindows: probe %+v, %v; want the answering agent as hit", hits, perr)
	}
	// The unauthenticated report (gosnmp discards it): resync, then error.
	agent = startRFC3414Agent(t, oidUsmStatsNotInTimeWindows, 0)
	if discoveries, err = collect(agent, v3Bad); !errors.Is(err, errNotInTimeWindow) || discoveries != 2 {
		t.Errorf("unauthenticated usmStatsNotInTimeWindows: discoveries=%d err=%v, want the error after one resync", discoveries, err)
	}
}

// fixedConn reads one fixed datagram and discards writes.
type fixedConn struct {
	net.Conn
	in []byte
}

func (c *fixedConn) Read(b []byte) (int, error)  { return copy(b, c.in), nil }
func (c *fixedConn) Write(b []byte) (int, error) { return len(b), nil }

// TestSNMPv3ReportNeedsOwnMsgID (WP-492): an unauthenticated usmStats report
// counts only when it echoes the msgID of a request the session sent, so a
// stray or spoofed datagram does not turn a target into auth_failed.
func TestSNMPv3ReportNeedsOwnMsgID(t *testing.T) {
	msg := func(msgID uint32, flags gosnmp.SnmpV3MsgFlags, pdu gosnmp.PDUType, vars []gosnmp.SnmpPDU) []byte {
		out, err := (&gosnmp.SnmpPacket{Version: gosnmp.Version3, MsgFlags: flags, SecurityModel: gosnmp.UserSecurityModel,
			SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: "reticora", AuthoritativeEngineID: "engine"},
			MsgID:              msgID, MsgMaxSize: 65507, PDUType: pdu, Variables: vars}).MarshalMsg()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	report := msg(4711, gosnmp.NoAuthNoPriv, gosnmp.Report,
		[]gosnmp.SnmpPDU{{Name: "." + oidUsmStatsWrongDigests, Type: gosnmp.Counter32, Value: uint32(1)}})
	conn := &reportConn{Conn: &fixedConn{in: report}, user: "reticora"}
	buf := make([]byte, 65535)
	if _, err := conn.Write(msg(4710, gosnmp.Reportable, gosnmp.GetRequest, []gosnmp.SnmpPDU{{Name: "." + oidSysObjectID, Type: gosnmp.Null}})); err != nil {
		t.Fatal(err)
	}
	if _, _ = conn.Read(buf); conn.report() != "" {
		t.Errorf("a report for msgID 4711 counted although the session sent only 4710: %s", conn.report())
	}
	if _, err := conn.Write(msg(4711, gosnmp.Reportable, gosnmp.GetRequest, []gosnmp.SnmpPDU{{Name: "." + oidSysObjectID, Type: gosnmp.Null}})); err != nil {
		t.Fatal(err)
	}
	if _, _ = conn.Read(buf); conn.report() != oidUsmStatsWrongDigests {
		t.Errorf("the report for the session's msgID 4711 = %q, want usmStatsWrongDigests", conn.report())
	}
}
