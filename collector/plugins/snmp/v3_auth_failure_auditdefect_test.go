//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT test (must fail
// until the defect is fixed).
//
// WP-155 (DIS-08), WP-152 (DIS-04 SNMPv3). Finding P2 "Credential failures
// of WMI and SNMPv3 are invisible: the scope run reports ok" (SNMPv3 part;
// the WMI part is TestAuditWMIAuthFailureReachesScopeRun in collectorcmd):
// an SNMPv3 agent that rejects the credentials answers, as RFC 3414 3.2
// requires, with a Report PDU usmStatsWrongDigests after a successful engine
// discovery, but the plugin maps it to "no device" – Probe returns no
// hit and no error, so collectHits/runDueDiscovery record no runError and the
// scope health stays ok. DIS-08: the failure is visible (an error of the
// probe, or a hit whose Collect fails, which runDueDiscovery reports with
// protocol and target).
//
// The agent is the package's SNMPv3 test agent extended to answer requests
// it cannot authenticate with the usmStatsWrongDigests report. The correct
// credentials are the control.
//
// Retest:
//
//	cd collector && go test -tags auditdefect -count=1 -v -run 'TestAuditSNMPv3AuthFailureReported' ./plugins/snmp/
package snmp

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

// startRFC3414Agent wraps the v3 test agent: a request it cannot process
// (wrong key) gets the unauthenticated usmStatsWrongDigests report.
func startRFC3414Agent(t *testing.T) (port int, reports *atomic.Int32) {
	t.Helper()
	inner := startV3Agent(t, "reticora", "auth-secret-1", "priv-secret-1", v3TestMIB())
	// Stop the inner agent's serve loop; its handler serves the new socket.
	_ = inner.conn.Close()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	count := &atomic.Int32{}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, peer, rerr := conn.ReadFromUDP(buf)
			if rerr != nil {
				return
			}
			raw := buf[:n]
			out := inner.handle(raw)
			if out == nil {
				req, _ := inner.decoder().SnmpDecodePacket(raw)
				if req == nil || req.MsgID == 0 {
					continue
				}
				user := ""
				if usm, ok := req.SecurityParameters.(*gosnmp.UsmSecurityParameters); ok {
					user = usm.UserName
				}
				report := &gosnmp.SnmpPacket{
					Version: gosnmp.Version3, MsgFlags: gosnmp.NoAuthNoPriv, SecurityModel: gosnmp.UserSecurityModel,
					SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: user,
						AuthoritativeEngineID: inner.engineID, AuthoritativeEngineBoots: 1, AuthoritativeEngineTime: 1000},
					MsgID: req.MsgID, MsgMaxSize: 65507, PDUType: gosnmp.Report, ContextEngineID: inner.engineID,
					Variables: []gosnmp.SnmpPDU{{Name: ".1.3.6.1.6.3.15.1.1.5.0", Type: gosnmp.Counter32, Value: uint32(1)}},
				}
				if out, err = report.MarshalMsg(); err != nil {
					continue
				}
				count.Add(1)
			}
			_, _ = conn.WriteToUDP(out, peer)
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).Port, count
}

func TestAuditSNMPv3AuthFailureReported(t *testing.T) {
	port, reports := startRFC3414Agent(t)
	good := map[string]string{"username": "reticora", "auth_protocol": "SHA256", "auth_password": "auth-secret-1",
		"priv_protocol": "AES", "priv_password": "priv-secret-1"}
	bad := map[string]string{"username": "reticora", "auth_protocol": "SHA256", "auth_password": "wrong-auth-pw",
		"priv_protocol": "AES", "priv_password": "wrong-priv-pw"}
	p := &Plugin{Timeout: time.Second, Port: port}
	const target = "127.0.0.1"

	if hits, err := p.Probe(context.Background(), []string{target}, good); err != nil || len(hits) != 1 {
		t.Fatalf("control: correct credentials: hits=%d err=%v", len(hits), err)
	}

	// Control: the agent answers the engine discovery and rejects the
	// authenticated request; a plain gosnmp client gets an error (gosnmp
	// v1.43 discards the unauthenticated report as "not authentic", or
	// returns ErrWrongDigest).
	raw := &gosnmp.GoSNMP{Target: target, Port: uint16(port), Version: gosnmp.Version3, Timeout: time.Second, Retries: 0,
		SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.AuthPriv, Logger: gosnmp.NewLogger(nil),
		SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: "reticora", AuthenticationProtocol: gosnmp.SHA256,
			AuthenticationPassphrase: bad["auth_password"], PrivacyProtocol: gosnmp.AES, PrivacyPassphrase: bad["priv_password"]}}
	if cerr := raw.Connect(); cerr != nil {
		t.Fatal(cerr)
	}
	_, gerr := raw.Get([]string{".1.3.6.1.2.1.1.5.0"})
	_ = raw.Conn.Close()
	if gerr == nil || reports.Load() == 0 {
		t.Fatalf("control: a gosnmp client with the wrong key got err=%v after %d report(s), want a rejection", gerr, reports.Load())
	}
	t.Logf("control: gosnmp with the wrong key: %v (wrong-digest reports so far: %d)", gerr, reports.Load())

	hits, err := p.Probe(context.Background(), []string{target}, bad)
	if err != nil {
		return // reported as a run error of the pass (DIS-08)
	}
	for _, h := range hits {
		if h.IP != target {
			continue
		}
		if _, cerr := p.Collect(context.Background(), target, bad); cerr != nil {
			return // reported as a run error with protocol snmp and the target
		}
	}
	t.Errorf("DIS-08 violated: the SNMPv3 agent at %s rejected the credentials with %d usmStatsWrongDigests report(s), "+
		"but Probe returned %d hit(s) and err=<nil> (no Collect error): the scope run shows no error", target, reports.Load(), len(hits))
}
