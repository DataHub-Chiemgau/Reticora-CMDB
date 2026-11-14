package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

const community = "probe-ro"

var v2Creds = map[string]string{"community": community}

func walkFile(t *testing.T, names ...string) []gosnmp.SnmpPDU {
	t.Helper()
	var out []gosnmp.SnmpPDU
	for _, name := range names {
		pdus, err := fakesnmp.LoadWalkFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, pdus...)
	}
	return out
}

// startAgent serves pdus on a fake SNMPv2c agent (generic Net-SNMP
// sysObjectID, no monitor profile) until the test ends.
func startAgent(t *testing.T, pdus ...gosnmp.SnmpPDU) *fakesnmp.Agent {
	t.Helper()
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: community, SysName: "p-1",
		SysDescr: "Reticora routing fake", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, pdus...)
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

func spec(typ string, agent *fakesnmp.Agent, params string) probewire.ProbeSpec {
	return probewire.ProbeSpec{ID: "p-" + typ, Type: typ, Target: "127.0.0.1:" + strconv.Itoa(agent.Addr().Port),
		IntervalMS: 60_000, TimeoutMS: 2000, DownCount: 3, UpCount: 1, Epoch: 1, Params: json.RawMessage(params)}
}

func run(t *testing.T, s probewire.ProbeSpec) probewire.Result { //nolint:gocritic // a copy per check
	t.Helper()
	r := probe.Run(context.Background(), &s, v2Creds)
	if err := r.Validate(); err != nil {
		t.Fatalf("result invalid: %v (%+v)", err, r)
	}
	return r
}

func want(t *testing.T, r probewire.Result, status probewire.Status, code string) { //nolint:gocritic // results are small
	t.Helper()
	if r.Status != status || r.Code != code {
		t.Fatalf("result = %s/%s (labels %v, values %v), want %s/%s", r.Status, r.Code, r.Labels, r.Values, status, code)
	}
}

func ospfState(routerID string, version int) string {
	return probewire.ValueKey(ValueOSPFNeighborState, map[string]string{LabelRouterID: routerID, LabelVersion: strconv.Itoa(version)})
}

func ospfEvents(routerID string, version int) string {
	return probewire.ValueKey(ValueOSPFNeighborEvents, map[string]string{LabelRouterID: routerID, LabelVersion: strconv.Itoa(version)})
}

func isisState(systemID, level string) string {
	return probewire.ValueKey(ValueISISAdjacencyState, map[string]string{LabelSystemID: systemID, LabelLevel: level})
}

// TestOSPFFull covers WP-1002 AK1, AK3 and AK4: two full OSPFv2 neighbors
// with state and event counter per neighbor.
func TestOSPFFull(t *testing.T) {
	agent := startAgent(t, walkFile(t, "ospf-mib.walk")...)
	r := run(t, spec(TypeOSPF, agent, `{"neighbors":[{"router_id":"192.0.2.2"},{"router_id":"192.0.2.3","version":2}],"min_neighbors":2}`))
	want(t, r, probewire.StatusOK, "")
	for k, v := range map[string]float64{
		ospfState("192.0.2.2", 2): 8, ospfState("192.0.2.3", 2): 8,
		ospfEvents("192.0.2.2", 2): 6, ospfEvents("192.0.2.3", 2): 12,
		ValueOSPFNeighbors: 2, ValueOSPFNeighborsFull: 2,
	} {
		if got, ok := r.Values[k]; !ok || got != v {
			t.Errorf("%s = %v (%v), want %v", k, got, ok, v)
		}
	}
	if _, ok := r.Values[ValueOSPFTruncated]; ok {
		t.Error("truncated without truncation")
	}
}

// TestOSPFv3Full covers WP-1002 AK1: OSPFv3 neighbors with the router ID
// from the index, next to the OSPFv2 neighbors.
func TestOSPFv3Full(t *testing.T) {
	agent := startAgent(t, walkFile(t, "ospf-mib.walk", "ospfv3-mib.walk")...)
	r := run(t, spec(TypeOSPF, agent, `{"neighbors":[{"router_id":"192.0.2.4","version":3},{"router_id":"192.0.2.5"},{"router_id":"192.0.2.2"}]}`))
	want(t, r, probewire.StatusOK, "")
	if r.Values[ospfState("192.0.2.4", 3)] != 8 || r.Values[ospfEvents("192.0.2.5", 3)] != 9 {
		t.Fatalf("v3 values = %v", r.Values)
	}
	if r.Values[ValueOSPFNeighbors] != 4 {
		t.Fatalf("neighbors = %v, want 4", r.Values[ValueOSPFNeighbors])
	}
	// The version narrows the match: 192.0.2.4 is no OSPFv2 neighbor.
	r = run(t, spec(TypeOSPF, agent, `{"neighbors":[{"router_id":"192.0.2.4","version":2}]}`))
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
}

// TestOSPFStateChange covers WP-1002 AK3: a neighbor falling to attempt(2)
// is fail/check_failed with the label router_id; the event counter does
// not change the state by itself.
func TestOSPFStateChange(t *testing.T) {
	agent := startAgent(t, walkFile(t, "ospf-mib.walk")...)
	s := spec(TypeOSPF, agent, `{"neighbors":[{"router_id":"192.0.2.2"},{"router_id":"192.0.2.3"}]}`)
	want(t, run(t, s), probewire.StatusOK, "")
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.14.10.1.7.10.0.1.2.0", Type: gosnmp.Counter32, Value: uint32(99)})
	want(t, run(t, s), probewire.StatusOK, "")
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.14.10.1.6.10.0.0.2.0", Type: gosnmp.Integer, Value: 2})
	r := run(t, s)
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if r.Labels[LabelRouterID] != "192.0.2.2" || r.Values[ospfState("192.0.2.2", 2)] != 2 {
		t.Fatalf("labels = %v, values = %v", r.Labels, r.Values)
	}
}

// TestOSPFMissingNeighborDown covers WP-1002 AK3.
func TestOSPFMissingNeighborDown(t *testing.T) {
	agent := startAgent(t, walkFile(t, "ospf-mib.walk")...)
	r := run(t, spec(TypeOSPF, agent, `{"neighbors":[{"router_id":"192.0.2.2"},{"router_id":"198.51.100.1"}]}`))
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if r.Labels[LabelRouterID] != "198.51.100.1" {
		t.Fatalf("labels = %v", r.Labels)
	}
	// A router without any neighbor: the expected one is missing.
	empty := startAgent(t)
	want(t, run(t, spec(TypeOSPF, empty, `{"neighbors":[{"router_id":"192.0.2.2"}]}`)), probewire.StatusFail, probewire.WarnCheckFailed)
}

// TestOSPFMinNeighborsDegraded covers WP-1002 AK3: fewer full neighbors
// than min_neighbors is ok with the warn code check_failed.
func TestOSPFMinNeighborsDegraded(t *testing.T) {
	agent := startAgent(t, walkFile(t, "ospf-mib.walk")...)
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.14.10.1.6.10.0.1.2.0", Type: gosnmp.Integer, Value: 4})
	r := run(t, spec(TypeOSPF, agent, `{"min_neighbors":2}`))
	want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
	if !probewire.IsWarnCode(r.Code) || r.Values[ValueOSPFNeighborsFull] != 1 {
		t.Fatalf("result = %+v", r)
	}
	want(t, run(t, spec(TypeOSPF, agent, `{"min_neighbors":1}`)), probewire.StatusOK, "")
}

// TestOSPFProfileTable covers WP-1002 AK4: the table of the monitor
// section ospf replaces the core, with its own enum.
func TestOSPFProfileTable(t *testing.T) {
	agent := startAgent(t,
		gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.99999.1.1.2.7", Type: gosnmp.IPAddress, Value: "203.0.113.7"},
		gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.99999.1.1.3.7", Type: gosnmp.Integer, Value: 1},
	)
	prev := monitorFor
	t.Cleanup(func() { monitorFor = prev })
	monitorFor = func(*snmp.Session, string) (profiles.MonitorSpec, bool, error) {
		return profiles.MonitorSpec{Tables: map[string]profiles.MonitorTable{ospfTableV2: {
			Columns: map[string]string{ospfColRtrID: "1.3.6.1.4.1.99999.1.1.2", ospfColState: "1.3.6.1.4.1.99999.1.1.3"},
			Index:   profiles.IndexInt,
			Enums:   map[string]map[string]string{ospfColState: {"1": "up", "2": "down"}},
		}}}, true, nil
	}
	r := run(t, spec(TypeOSPF, agent, `{"neighbors":[{"router_id":"203.0.113.7"}]}`))
	want(t, r, probewire.StatusOK, "")
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.99999.1.1.3.7", Type: gosnmp.Integer, Value: 2})
	want(t, run(t, spec(TypeOSPF, agent, `{"neighbors":[{"router_id":"203.0.113.7"}]}`)), probewire.StatusFail, probewire.WarnCheckFailed)
}

// TestOSPFTruncated: more neighbors than max_neighbors cut the walk; an
// expected neighbor behind the cut is no statement.
func TestOSPFTruncated(t *testing.T) {
	agent := startAgent(t, walkFile(t, "ospf-mib.walk", "ospfv3-mib.walk")...)
	r := run(t, spec(TypeOSPF, agent, `{"max_neighbors":2,"neighbors":[{"router_id":"192.0.2.2"},{"router_id":"192.0.2.5"}],"min_neighbors":2}`))
	want(t, r, probewire.StatusOK, "")
	if r.Values[ValueOSPFTruncated] != 1 || r.Values[ValueOSPFNeighborsUnseen] != 1 {
		t.Fatalf("values = %v", r.Values)
	}
	r = run(t, spec(TypeOSPF, agent, `{"max_neighbors":2,"neighbors":[{"router_id":"192.0.2.5"}]}`))
	want(t, r, probewire.StatusError, probewire.ReasonResponseTooLarge)
}

// TestOSPFLimitFilledNotTruncated covers WP-1002 AK3: a v2 table that
// fills max_neighbors exactly, without an OSPFv3 table, is no truncated
// walk; a missing expected neighbor is fail/check_failed. With an OSPFv3
// table beyond the limit, an expected version-2 neighbor is still judged:
// its table was read in full.
func TestOSPFLimitFilledNotTruncated(t *testing.T) {
	agent := startAgent(t, walkFile(t, "ospf-mib.walk")...)
	r := run(t, spec(TypeOSPF, agent, `{"max_neighbors":2,"neighbors":[{"router_id":"192.0.2.2"},{"router_id":"203.0.113.7"}]}`))
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if _, ok := r.Values[ValueOSPFTruncated]; ok || r.Labels[LabelRouterID] != "203.0.113.7" {
		t.Fatalf("values = %v, labels = %v", r.Values, r.Labels)
	}
	both := startAgent(t, walkFile(t, "ospf-mib.walk", "ospfv3-mib.walk")...)
	r = run(t, spec(TypeOSPF, both, `{"max_neighbors":2,"neighbors":[{"router_id":"203.0.113.7","version":2}]}`))
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if r.Values[ValueOSPFTruncated] != 1 {
		t.Fatalf("values = %v, want the cut OSPFv3 table reported", r.Values)
	}
}

// TestISISUp covers WP-1002 AK2 and AK4.
func TestISISUp(t *testing.T) {
	agent := startAgent(t, walkFile(t, "isis-mib.walk")...)
	r := run(t, spec(TypeISIS, agent, `{"adjacencies":[{"system_id":"1921.6800.1002","level":2},{"system_id":"19:21:68:00:10:03","level":1}],"min_adjacencies":2}`))
	want(t, r, probewire.StatusOK, "")
	if r.Values[isisState("1921.6800.1002", LevelL2)] != 3 || r.Values[isisState("1921.6800.1003", LevelL1L2)] != 3 {
		t.Fatalf("values = %v", r.Values)
	}
	if r.Values[ValueISISAdjacenciesUp] != 2 {
		t.Fatalf("up = %v", r.Values[ValueISISAdjacenciesUp])
	}
	// A level-2-only adjacency does not serve level 1.
	r = run(t, spec(TypeISIS, agent, `{"adjacencies":[{"system_id":"1921.6800.1002","level":1}]}`))
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
}

// TestISISStateChange covers WP-1002 AK3: an adjacency falling to
// initializing is fail/check_failed with the label system_id; fewer up
// than min_adjacencies without an expected list is degraded.
func TestISISStateChange(t *testing.T) {
	agent := startAgent(t, walkFile(t, "isis-mib.walk")...)
	s := spec(TypeISIS, agent, `{"adjacencies":[{"system_id":"1921.6800.1003"}]}`)
	want(t, run(t, s), probewire.StatusOK, "")
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.138.1.6.1.1.2.2.1", Type: gosnmp.Integer, Value: 2})
	r := run(t, s)
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if r.Labels[LabelSystemID] != "1921.6800.1003" {
		t.Fatalf("labels = %v", r.Labels)
	}
	want(t, run(t, spec(TypeISIS, agent, `{"min_adjacencies":2}`)), probewire.StatusOK, probewire.WarnCheckFailed)
}

// TestISISMissingAdjacencyDown covers WP-1002 AK3.
func TestISISMissingAdjacencyDown(t *testing.T) {
	agent := startAgent(t, walkFile(t, "isis-mib.walk")...)
	r := run(t, spec(TypeISIS, agent, `{"adjacencies":[{"system_id":"1921.6800.1002"},{"system_id":"0000.0000.0099"}]}`))
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if r.Labels[LabelSystemID] != "0000.0000.0099" {
		t.Fatalf("labels = %v", r.Labels)
	}
}

// limitsCase is one rejected spec of the limits tests.
type limitsCase struct {
	name  string
	mod   func(*probewire.ProbeSpec)
	field string
}

func checkLimits(t *testing.T, typ string, cases []limitsCase) {
	t.Helper()
	ct, ok := probewire.Lookup(typ)
	if !ok || !ct.Available() || ct.MinIntervalS != 60 || ct.MaxTimeoutS != 30 || ct.MaxRows != MaxRows {
		t.Fatalf("contract entry %s = %+v", typ, ct)
	}
	if len(ct.CredentialNamespaces) != 1 || ct.CredentialNamespaces[0] != "snmp" {
		t.Fatalf("credential namespaces = %v", ct.CredentialNamespaces)
	}
	agent := startAgent(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := spec(typ, agent, `{}`)
			tc.mod(&s)
			found := false
			errs := probewire.Validate(&s)
			for _, e := range errs {
				found = found || e.Field == tc.field
			}
			if !found {
				t.Fatalf("errors = %v, want field %s", errs, tc.field)
			}
			want(t, run(t, s), probewire.StatusError, probewire.ReasonConfig)
		})
	}
}

func entries(n int, format string) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(format, i/256%256, i%256)
	}
	return strings.Join(parts, ",")
}

// TestOSPFLimits covers WP-1002 AK5: 1001 expected neighbors and an
// interval of 59 s are rejected, as are a router ID that is no IPv4
// address and min_neighbors above max_neighbors.
func TestOSPFLimits(t *testing.T) {
	checkLimits(t, TypeOSPF, []limitsCase{
		{"1001 neighbors", func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"neighbors":[` + entries(1001, `{"router_id":"10.0.%d.%d"}`) + `]}`)
		}, "params.neighbors"},
		{"interval 59 s", func(s *probewire.ProbeSpec) { s.IntervalMS = 59_000 }, "interval_s"},
		{"timeout 31 s", func(s *probewire.ProbeSpec) { s.TimeoutMS = 31_000 }, "timeout_s"},
		{"router id", func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"neighbors":[{"router_id":"2001:db8::1"}]}`)
		}, "params.neighbors[0].router_id"},
		{"version 4", func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"neighbors":[{"router_id":"192.0.2.1","version":4}]}`)
		}, "params.neighbors[0].version"},
		{"min above max", func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"min_neighbors":10,"max_neighbors":5}`)
		}, "params.min_neighbors"},
		{"max 1001", func(s *probewire.ProbeSpec) { s.Params = json.RawMessage(`{"max_neighbors":1001}`) }, "params.max_neighbors"},
	})
	agent := startAgent(t)
	ok := spec(TypeOSPF, agent, `{"neighbors":[`+entries(1000, `{"router_id":"10.0.%d.%d"}`)+`]}`)
	if errs := probewire.Validate(&ok); len(errs) > 0 {
		t.Fatalf("1000 neighbors rejected: %v", errs[0])
	}
}

// TestISISLimits covers WP-1002 AK5: 1001 expected adjacencies and an
// interval of 59 s are rejected, as is a malformed system ID.
func TestISISLimits(t *testing.T) {
	checkLimits(t, TypeISIS, []limitsCase{
		{"1001 adjacencies", func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"adjacencies":[` + entries(1001, `{"system_id":"0000.0000.%02x%02x"}`) + `]}`)
		}, "params.adjacencies"},
		{"interval 59 s", func(s *probewire.ProbeSpec) { s.IntervalMS = 59_000 }, "interval_s"},
		{"system id", func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"adjacencies":[{"system_id":"1921.6800.10"}]}`)
		}, "params.adjacencies[0].system_id"},
		{"level 3", func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"adjacencies":[{"system_id":"1921.6800.1002","level":3}]}`)
		}, "params.adjacencies[0].level"},
		{"min above max", func(s *probewire.ProbeSpec) {
			s.Params = json.RawMessage(`{"min_adjacencies":3,"max_adjacencies":2}`)
		}, "params.min_adjacencies"},
	})
	if id, ok := NormalizeSystemID("1921-6800-10AB"); !ok || id != "1921.6800.10ab" {
		t.Fatalf("NormalizeSystemID = %q, %v", id, ok)
	}
}
