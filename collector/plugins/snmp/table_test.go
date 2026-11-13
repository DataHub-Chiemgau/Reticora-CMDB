package snmp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Tests of the probe session and the table walk (WP-993, PRT-07, PRT-11)
// against fakesnmp serving walks in the "snmpwalk -On" format of WP-992.

const tableCommunity = "table-community"

// startWalkAgent serves walk (snmpwalk -On text) over SNMPv2c, or over
// SNMPv3 authPriv with v3 set.
func startWalkAgent(t *testing.T, walk string, v3 *fakesnmp.V3, onRequest func(fakesnmp.Request)) *fakesnmp.Agent {
	t.Helper()
	pdus, err := fakesnmp.LoadWalk(strings.NewReader(walk))
	if err != nil {
		t.Fatal(err)
	}
	cfg := fakesnmp.Config{Community: tableCommunity, SysName: "pe-1", SysDescr: "Reticora table fake",
		SysObjectID: ".1.3.6.1.4.1.8072.3.2.10", OnRequest: onRequest}
	var agent *fakesnmp.Agent
	if v3 != nil {
		cfg.Community = ""
		agent, err = fakesnmp.ListenV3("127.0.0.1:0", v3, cfg, pdus...)
	} else {
		agent, err = fakesnmp.Listen("127.0.0.1:0", cfg, pdus...)
	}
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Serve() }()
	t.Cleanup(func() {
		_ = agent.Close()
		<-done
	})
	return agent
}

func openV2c(t *testing.T, agent *fakesnmp.Agent, opts Options) *Session {
	t.Helper()
	opts.Port = agent.Addr().Port
	if opts.Timeout == 0 {
		opts.Timeout = time.Second
	}
	s, err := Open(context.Background(), "127.0.0.1", map[string]string{"community": tableCommunity}, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// bgpPeerTable (BGP4-MIB): bgpPeerState (.2) and bgpPeerRemoteAs (.9),
// indexed by the peer's IPv4 address.
const bgpPeerWalk = `
.1.3.6.1.2.1.15.3.1.2.10.0.0.2 = INTEGER: established(6)
.1.3.6.1.2.1.15.3.1.2.192.0.2.17 = INTEGER: active(3)
.1.3.6.1.2.1.15.3.1.9.10.0.0.2 = INTEGER: 65001
.1.3.6.1.2.1.15.3.1.9.192.0.2.17 = INTEGER: 64512
`

const (
	oidBgpPeerState    = "1.3.6.1.2.1.15.3.1.2"
	oidBgpPeerRemoteAs = "1.3.6.1.2.1.15.3.1.9"
)

func TestWalkColumnsIPv4Index(t *testing.T) {
	s := openV2c(t, startWalkAgent(t, bgpPeerWalk, nil, nil), Options{})
	rows, err := s.WalkColumns([]string{oidBgpPeerState, "." + oidBgpPeerRemoteAs}, 0)
	if err != nil || rows.Truncated || len(rows.Rows) != 2 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	want := []struct {
		peer       string
		state, asn float64
	}{{"10.0.0.2", 6, 65001}, {"192.0.2.17", 3, 64512}}
	for i, w := range want {
		row := rows.Rows[i]
		addr, rest, ierr := IndexIPv4(row.Index)
		if ierr != nil || len(rest) != 0 || addr.String() != w.peer {
			t.Errorf("row %d: peer %s rest %v err %v, want %s", i, addr, rest, ierr, w.peer)
		}
		state, _ := row.Value(oidBgpPeerState)
		asn, _ := row.Value(oidBgpPeerRemoteAs)
		if v, _ := numericValue(state); v != w.state {
			t.Errorf("row %d: state %v, want %v", i, v, w.state)
		}
		if v, _ := numericValue(asn); v != w.asn {
			t.Errorf("row %d: remote AS %v, want %v", i, v, w.asn)
		}
	}
	if _, _, err = IndexIPv4([]uint32{10, 0, 300, 1}); !errors.Is(err, ErrIndex) {
		t.Errorf("octet 300 accepted: %v", err)
	}
	if _, _, err = IndexIPv4([]uint32{10, 0}); !errors.Is(err, ErrIndex) {
		t.Errorf("short IPv4 index accepted: %v", err)
	}
}

// cbgpPeer2Table (CISCO-BGP4-MIB, a BGP4-V2 table): cbgpPeer2State (.3)
// indexed by InetAddressType, length and octets of the peer (RFC 4001).
const bgpV2Walk = `
.1.3.6.1.4.1.9.9.187.1.2.5.1.3.1.4.192.0.2.1 = INTEGER: 6
.1.3.6.1.4.1.9.9.187.1.2.5.1.3.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.1 = INTEGER: 2
`

const oidCbgpPeer2State = "1.3.6.1.4.1.9.9.187.1.2.5.1.3"

func TestWalkColumnsInetAddressIndex(t *testing.T) {
	s := openV2c(t, startWalkAgent(t, bgpV2Walk, nil, nil), Options{MaxRepetitions: 1})
	rows, err := s.WalkColumns([]string{oidCbgpPeer2State}, 100)
	if err != nil || len(rows.Rows) != 2 || rows.Truncated {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	for i, want := range []string{"192.0.2.1", "2001:db8::1"} {
		addr, rest, ierr := IndexInetAddress(rows.Rows[i].Index)
		if ierr != nil || len(rest) != 0 || addr != netip.MustParseAddr(want) {
			t.Errorf("row %d: %s rest %v err %v, want %s", i, addr, rest, ierr, want)
		}
	}
	for name, index := range map[string][]uint32{
		"ipv4z":            {3, 8, 192, 0, 2, 1, 0, 0, 0, 1},
		"ipv6z":            {4, 20, 254, 128, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1},
		"unknown type":     {0, 0},
		"dns":              {16, 3, 'a', 'b', 'c'},
		"wrong length":     {1, 16, 192, 0, 2, 1},
		"too few octets":   {2, 16, 32, 1},
		"octet over 255":   {1, 4, 192, 0, 2, 256},
		"no length at all": {1},
	} {
		if _, _, ierr := IndexInetAddress(index); !errors.Is(ierr, ErrIndex) {
			t.Errorf("%s accepted: %v", name, ierr)
		}
	}
	// The InetAddress may be followed by further index parts.
	addr, rest, err := IndexInetAddress([]uint32{1, 4, 198, 51, 100, 7, 42})
	if err != nil || addr.String() != "198.51.100.7" || len(rest) != 1 || rest[0] != 42 {
		t.Errorf("InetAddress with rest: %s %v %v", addr, rest, err)
	}
}

// ospfNbrTable (OSPF-MIB): ospfNbrState (.6) indexed by ospfNbrIpAddr and
// ospfNbrAddressLessIndex.
const ospfNbrWalk = `
.1.3.6.1.2.1.14.10.1.3.10.1.1.2.0 = IpAddress: 1.1.1.2
.1.3.6.1.2.1.14.10.1.3.10.1.2.2.7 = IpAddress: 1.1.1.3
.1.3.6.1.2.1.14.10.1.6.10.1.1.2.0 = INTEGER: full(8)
.1.3.6.1.2.1.14.10.1.6.10.1.2.2.7 = INTEGER: init(2)
`

const (
	oidOspfNbrRtrID = "1.3.6.1.2.1.14.10.1.3"
	oidOspfNbrState = "1.3.6.1.2.1.14.10.1.6"
)

func TestWalkColumnsCompositeIndex(t *testing.T) {
	s := openV2c(t, startWalkAgent(t, ospfNbrWalk, nil, nil), Options{})
	rows, err := s.WalkColumns([]string{oidOspfNbrRtrID, oidOspfNbrState}, 0)
	if err != nil || len(rows.Rows) != 2 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	decode := IndexComposite(Part(IndexIPv4), Part(IndexInt))
	for i, want := range []struct {
		addr          string
		lessIdx       int
		state, router string
	}{{"10.1.1.2", 0, "8", "1.1.1.2"}, {"10.1.2.2", 7, "2", "1.1.1.3"}} {
		parts, derr := decode(rows.Rows[i].Index)
		if derr != nil || len(parts) != 2 {
			t.Fatalf("row %d: %v %v", i, parts, derr)
		}
		if parts[0].(netip.Addr).String() != want.addr || parts[1].(int) != want.lessIdx {
			t.Errorf("row %d: index %v, want %s/%d", i, parts, want.addr, want.lessIdx)
		}
		state, _ := rows.Rows[i].Value(oidOspfNbrState)
		router, _ := rows.Rows[i].Value(oidOspfNbrRtrID)
		if v, _ := numericValue(state); fmt.Sprint(v) != want.state || router.Value != want.router {
			t.Errorf("row %d: state %v router %v", i, state.Value, router.Value)
		}
	}
	if _, err = decode([]uint32{10, 1, 1, 2}); !errors.Is(err, ErrIndex) {
		t.Errorf("missing part accepted: %v", err)
	}
	if _, err = decode([]uint32{10, 1, 1, 2, 0, 9}); !errors.Is(err, ErrIndex) {
		t.Errorf("trailing sub-identifier accepted: %v", err)
	}
}

// onuWalk serves n rows of a two-part ONU table (PON port, ONU id; PRT-11)
// with two columns.
func onuWalk(n int) string {
	var b strings.Builder
	for col := 2; col <= 3; col++ {
		for i := range n {
			fmt.Fprintf(&b, ".1.3.6.1.4.1.99999.1.%d.%d.%d = INTEGER: %d\n", col, 1+i/10, i%10, col*100+i)
		}
	}
	return b.String()
}

func TestWalkColumnsTruncated(t *testing.T) {
	columns := []string{"1.3.6.1.4.1.99999.1.2", "1.3.6.1.4.1.99999.1.3"}
	s := openV2c(t, startWalkAgent(t, onuWalk(20), nil, nil), Options{MaxRepetitions: 3})
	rows, err := s.WalkColumns(columns, 10)
	if err != nil || !rows.Truncated || len(rows.Rows) != 10 {
		t.Fatalf("maxRows 10 of 20: %d rows, truncated %v, %v", len(rows.Rows), rows.Truncated, err)
	}
	decode := IndexComposite(Part(IndexInt), Part(IndexInt))
	for i, row := range rows.Rows {
		parts, derr := decode(row.Index)
		if derr != nil || parts[0] != 1+i/10 || parts[1] != i%10 {
			t.Errorf("row %d: index %v %v", i, parts, derr)
		}
		for c, column := range columns {
			v, ok := row.Value(column)
			if n, _ := numericValue(v); !ok || int(n) != (c+2)*100+i {
				t.Errorf("row %d column %s: %v %v", i, column, v.Value, ok)
			}
		}
	}

	// Exactly as many rows as the limit: not truncated.
	exact := openV2c(t, startWalkAgent(t, onuWalk(10), nil, nil), Options{})
	if rows, err = exact.WalkColumns(columns, 10); err != nil || rows.Truncated || len(rows.Rows) != 10 {
		t.Errorf("maxRows 10 of 10: %d rows, truncated %v, %v", len(rows.Rows), rows.Truncated, err)
	}

	// An error in the middle of the walk keeps the rows read so far.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	agent := startWalkAgent(t, onuWalk(20), nil, func(fakesnmp.Request) {
		if requests.Add(1) == 2 {
			cancel()
		}
	})
	broken, err := Open(ctx, "127.0.0.1", map[string]string{"community": tableCommunity},
		Options{Port: agent.Addr().Port, Timeout: time.Second, MaxRepetitions: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broken.Close() }()
	rows, err = broken.WalkColumns(columns, 0)
	if err == nil || len(rows.Rows) == 0 || len(rows.Rows) >= 20 {
		t.Errorf("walk broken off: %d rows, %v; want the rows read so far with the error", len(rows.Rows), err)
	}
}

// TestWalkColumnsTruncatedSparseFirstColumn: the first column holds only
// row 1, the second adds rows 2 and 3 and reaches the limit of 2 there;
// the third column still fills every row kept (row 2), it stops after the
// highest index kept, not after the last index of the first column.
func TestWalkColumnsTruncatedSparseFirstColumn(t *testing.T) {
	const walk = `
.1.3.6.1.4.1.99999.2.1.1 = INTEGER: 101
.1.3.6.1.4.1.99999.2.2.1 = INTEGER: 201
.1.3.6.1.4.1.99999.2.2.2 = INTEGER: 202
.1.3.6.1.4.1.99999.2.2.3 = INTEGER: 203
.1.3.6.1.4.1.99999.2.3.1 = INTEGER: 301
.1.3.6.1.4.1.99999.2.3.2 = INTEGER: 302
.1.3.6.1.4.1.99999.2.3.3 = INTEGER: 303
`
	columns := []string{"1.3.6.1.4.1.99999.2.1", "1.3.6.1.4.1.99999.2.2", "1.3.6.1.4.1.99999.2.3"}
	s := openV2c(t, startWalkAgent(t, walk, nil, nil), Options{MaxRepetitions: 1})
	rows, err := s.WalkColumns(columns, 2)
	if err != nil || !rows.Truncated || len(rows.Rows) != 2 {
		t.Fatalf("maxRows 2 of 3: %d rows, truncated %v, %v", len(rows.Rows), rows.Truncated, err)
	}
	for i, row := range rows.Rows {
		if len(row.Index) != 1 || int(row.Index[0]) != i+1 {
			t.Fatalf("row %d: index %v", i, row.Index)
		}
		for c, column := range columns {
			v, ok := row.Value(column)
			if c == 0 && i == 1 {
				if ok {
					t.Errorf("row 2 has a value in the sparse first column: %v", v.Value)
				}
				continue
			}
			if n, _ := numericValue(v); !ok || int(n) != (c+1)*100+i+1 {
				t.Errorf("row %d column %s: %v %v", i+1, column, v.Value, ok)
			}
		}
	}
}

var tableV3User = &fakesnmp.V3{User: "probe", AuthProtocol: gosnmp.SHA256, AuthPassphrase: "auth-secret-1",
	PrivProtocol: gosnmp.AES, PrivPassphrase: "priv-secret-1"}

func tableV3Creds(auth string) map[string]string {
	return map[string]string{"username": "probe", "auth_protocol": "SHA256", "auth_password": auth,
		"priv_protocol": "AES", "priv_password": "priv-secret-1"}
}

func TestSessionV3WithProbeTimeout(t *testing.T) {
	agent := startWalkAgent(t, bgpPeerWalk, tableV3User, nil)
	retries := 0
	opts := Options{Port: agent.Addr().Port, Timeout: 2 * time.Second, Retries: &retries, MaxRepetitions: 10}
	s, err := Open(context.Background(), "127.0.0.1", tableV3Creds("auth-secret-1"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.Version() != "v3" || s.Timeout() != 2*time.Second || s.g.Timeout != 2*time.Second || s.g.Retries != 0 ||
		s.g.MaxRepetitions != 10 || s.g.MsgFlags&gosnmp.AuthPriv != gosnmp.AuthPriv {
		t.Errorf("session: version %s timeout %s/%s retries %d repetitions %d flags %v",
			s.Version(), s.Timeout(), s.g.Timeout, s.g.Retries, s.g.MaxRepetitions, s.g.MsgFlags)
	}
	vars, err := s.Get([]string{fakesnmp.OIDSysName})
	if err != nil || len(vars) != 1 || string(vars[0].Value.([]byte)) != "pe-1" {
		t.Fatalf("v3 Get: %+v, %v", vars, err)
	}
	rows, err := s.WalkColumns([]string{oidBgpPeerState}, 0)
	if err != nil || len(rows.Rows) != 2 {
		t.Fatalf("v3 walk: %+v, %v", rows, err)
	}

	// A silent agent: the request ends after the probe's timeout (one
	// attempt, no retry), not after the plugin's 5 s.
	silent, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	quick, err := Open(context.Background(), "127.0.0.1", tableV3Creds("auth-secret-1"),
		Options{Port: silent.LocalAddr().(*net.UDPAddr).Port, Timeout: time.Second, Retries: &retries})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = quick.Close() }()
	start := time.Now()
	if _, err = quick.Get([]string{fakesnmp.OIDSysName}); err == nil {
		t.Fatal("silent agent answered")
	}
	if d := time.Since(start); d < 900*time.Millisecond || d > 4*time.Second {
		t.Errorf("silent agent: request took %s, want about the probe timeout of 1 s", d)
	}
}

func TestSessionV3AuthFailed(t *testing.T) {
	agent := startWalkAgent(t, bgpPeerWalk, tableV3User, nil)
	s, err := Open(context.Background(), "127.0.0.1", tableV3Creds("wrong-auth-pw"),
		Options{Port: agent.Addr().Port, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err = s.Get([]string{fakesnmp.OIDSysName}); !errors.Is(err, plugins.ErrAuthFailed) || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("Get with a wrong key: %v, want auth_failed with the target", err)
	}
	if _, err = s.WalkColumns([]string{oidBgpPeerState}, 0); !errors.Is(err, plugins.ErrAuthFailed) {
		t.Errorf("walk with a wrong key: %v, want auth_failed", err)
	}
	// Below authPriv no session is opened.
	if _, err = Open(context.Background(), "127.0.0.1", map[string]string{"username": "probe", "auth_password": "a"}, Options{}); !errors.Is(err, ErrV3Incomplete) {
		t.Errorf("authNoPriv: %v, want ErrV3Incomplete", err)
	}
}

func TestOptionsValidate(t *testing.T) {
	ptr := func(n int) *int { return &n }
	valid := []Options{
		{},
		{Port: 1, Timeout: time.Second, Retries: ptr(0), MaxRepetitions: 1},
		{Port: 65535, Timeout: 15 * time.Second, Retries: ptr(2), MaxRepetitions: 50},
	}
	for _, o := range valid {
		if err := o.Validate(); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	for field, o := range map[string]Options{
		"port":             {Port: -1},
		"port ":            {Port: 65536},
		"timeout":          {Timeout: time.Second - time.Millisecond},
		"timeout ":         {Timeout: 15*time.Second + time.Millisecond},
		"retries":          {Retries: ptr(-1)},
		"retries ":         {Retries: ptr(3)},
		"max_repetitions":  {MaxRepetitions: -1},
		"max_repetitions ": {MaxRepetitions: 51},
	} {
		var oe *OptionsError
		if err := o.Validate(); !errors.As(err, &oe) || oe.Field != strings.TrimSpace(field) {
			t.Errorf("%+v: %v, want a field error on %s", o, err, strings.TrimSpace(field))
		}
		if _, err := Open(context.Background(), "127.0.0.1", nil, o); !errors.As(err, &oe) {
			t.Errorf("Open with %+v: %v, want the field error", o, err)
		}
	}
	// Empty fields take the plugin defaults.
	d := Options{}.withDefaults()
	if d.Port != defaultPort || d.Timeout != defaultTimeout || *d.Retries != defaultRetries || d.MaxRepetitions != 50 {
		t.Errorf("defaults %+v", d)
	}
}
