package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

func TestParseOutletName(t *testing.T) {
	for in, want := range map[string][2]string{
		"web01 PSU1":          {"web01", "psu1"},
		"web01-psu2":          {"web01", "psu2"},
		"db01 (B)":            {"db01", "b"},
		"db01-A":              {"db01", "a"},
		"esx03 PS 2":          {"esx03", "ps2"},
		"core-sw1 Netzteil 1": {"core-sw1", "netzteil1"},
		"nas01 #2":            {"nas01", "2"},
		"pdu-a":               {"pdu", "a"},
		"pdu-a in1":           {"pdu-a", "in1"},
		"firewall":            {"firewall", ""},
	} {
		device, feed := ParseOutletName(in)
		if device != want[0] || feed != want[1] {
			t.Errorf("ParseOutletName(%q) = %q, %q; want %q, %q", in, device, feed, want[0], want[1])
		}
	}
}

// TestPowerChain covers WP-161 (IMP-09): PDU outlet names give powered_by
// edges device → PDU, UPS outlet groups PDU → UPS; a device on two PDUs
// has redundant feeds (IMP-04), a device on one PDU does not.
func TestPowerChain(t *testing.T) {
	cis := ci.NewMemoryRepository()
	ctx := context.Background()
	mk := func(name, hostname string) string {
		item := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: name, Hostname: hostname, Status: "active"}
		if err := cis.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	web, db, pduA, pduB, ups := mk("web01", "web01"), mk("db01", ""), mk("PDU-A", ""), mk("PDU-B", ""), mk("UPS-1", "")
	rels := relationship.NewMemoryRepository()
	h := NewHandler(NewMemoryRepository(), cis, rels)

	power := func(outlets ...string) IngestItem {
		var o []DeviceOutlet
		for _, n := range outlets {
			o = append(o, DeviceOutlet{Name: n, State: "on"})
		}
		return IngestItem{RawData: map[string]any{"power": &DevicePower{DeviceType: "pdu", Outlets: o}}}
	}
	items := []IngestItem{
		power("web01 PSU1", "db01 PSU1", "unknown-box"),
		power("web01 PSU2"),
		power("PDU-A", "PDU-B in1"),
	}
	if n := h.derivePowerChain(ctx, "org-1", items, []string{pduA, pduB, ups}); n != 5 {
		t.Fatalf("created %d edges, want 5", n)
	}
	edges := map[string]relationship.Relationship{}
	for _, id := range []string{web, db, pduA, pduB} {
		list, _, err := rels.List(ctx, "org-1", id, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range list {
			if r.SourceCIID == id && r.RelType == "powered_by" {
				edges[id+"→"+r.TargetCIID] = r
			}
		}
	}
	want := map[string]bool{web + "→" + pduA: true, web + "→" + pduB: true, db + "→" + pduA: false, pduA + "→" + ups: false, pduB + "→" + ups: false}
	if len(edges) != len(want) {
		t.Fatalf("edges = %v", edges)
	}
	for key, redundant := range want {
		e, ok := edges[key]
		if !ok {
			t.Errorf("missing edge %s", key)
			continue
		}
		if e.Attributes["redundant"] != redundant || e.Confidence == nil || *e.Confidence != outletConfidence || e.Source != "discovery" {
			t.Errorf("%s: %+v", key, e)
		}
	}
	if edges[web+"→"+pduB].Attributes["feed"] != "psu2" || edges[web+"→"+pduA].Attributes["outlet"] != "web01 PSU1" {
		t.Errorf("feed/outlet: %v / %v", edges[web+"→"+pduB].Attributes, edges[web+"→"+pduA].Attributes)
	}

	// A later batch adds a second feed for db01: the new edge and its
	// sibling from the first batch are redundant (WP-498, F-112).
	if n := h.derivePowerChain(ctx, "org-1", []IngestItem{power("db01 PSU2")}, []string{pduB}); n != 1 {
		t.Fatalf("second feed: %d", n)
	}
	if edges := poweredBy(t, rels, db); len(edges) != 2 || edges[pduA].Attributes["redundant"] != true || edges[pduB].Attributes["redundant"] != true {
		t.Errorf("feeds of db01 after its second feed: %+v, want both redundant", edges)
	}
}

// poweredBy returns the powered_by edges of the CI fed, by power device.
func poweredBy(t *testing.T, rels relationship.Repository, fed string) map[string]relationship.Relationship {
	t.Helper()
	list, _, err := rels.List(context.Background(), "org-1", fed, api.PaginationParams{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]relationship.Relationship{}
	for _, r := range list {
		if r.SourceCIID == fed && r.RelType == "powered_by" {
			out[r.TargetCIID] = r
		}
	}
	return out
}

// TestPowerChainOutletOff covers E-85 option A (IMP-09, F-275, WP-498): an
// outlet switched off gives its powered_by edge with outlet_state=off, but
// does not count as a feed for redundant; switched on again, it does. The
// state an outlet reports again is written to the stored edge.
func TestPowerChainOutletOff(t *testing.T) {
	cis := ci.NewMemoryRepository()
	ctx := context.Background()
	mk := func(name string) string {
		item := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: name, Status: "active"}
		if err := cis.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	web, pduA, pduB := mk("web01"), mk("PDU-A"), mk("PDU-B")
	rels := relationship.NewMemoryRepository()
	h := NewHandler(NewMemoryRepository(), cis, rels)
	outlet := func(name, state string) IngestItem {
		return IngestItem{RawData: map[string]any{"power": &DevicePower{DeviceType: "pdu", Outlets: []DeviceOutlet{{Name: name, State: state}}}}}
	}
	check := func(step string, wantRedundant bool, stateB string) {
		t.Helper()
		edges := poweredBy(t, rels, web)
		if len(edges) != 2 {
			t.Fatalf("%s: edges = %+v, want web01 → PDU-A and web01 → PDU-B", step, edges)
		}
		for target, e := range edges {
			if e.Attributes["redundant"] != wantRedundant {
				t.Errorf("%s: web01 → %s redundant = %v, want %v", step, target, e.Attributes["redundant"], wantRedundant)
			}
		}
		if got := edges[pduB].Attributes["outlet_state"]; got != stateB {
			t.Errorf("%s: outlet_state of web01 → PDU-B = %v, want %s", step, got, stateB)
		}
	}

	// PSU2 is switched off: two edges, one feed, not redundant.
	items := []IngestItem{outlet("web01 PSU1", "on"), outlet("web01 PSU2", "off")}
	if n := h.derivePowerChain(ctx, "org-1", items, []string{pduA, pduB}); n != 2 {
		t.Fatalf("created %d edges, want 2 (an outlet switched off keeps its edge)", n)
	}
	check("PSU2 off", false, "off")

	// Both outlets on: two feeds, redundant, also on the stored edge of
	// PDU-A, which this batch does not report.
	h.derivePowerChain(ctx, "org-1", []IngestItem{outlet("web01 PSU2", "on")}, []string{pduB})
	check("PSU2 on again", true, "on")

	// PSU2 off again in a later batch: the stored state and the flag of
	// both edges follow.
	h.derivePowerChain(ctx, "org-1", []IngestItem{outlet("web01 PSU2", "off")}, []string{pduB})
	check("PSU2 off again", false, "off")

	// PowerEdges alone: the batch flag counts only feeds not switched off
	// (an outlet without a state counts).
	edges := PowerEdges([]OutletFeed{
		{PowerCIID: "a", FedCIID: "s", Outlet: "s PSU1", Feed: "psu1", State: "on"},
		{PowerCIID: "b", FedCIID: "s", Outlet: "s PSU2", Feed: "psu2", State: "off"},
		{PowerCIID: "a", FedCIID: "t", Outlet: "t PSU1", State: "on"},
		{PowerCIID: "b", FedCIID: "t", Outlet: "t PSU2"},
	})
	want := map[string]bool{"s→a": false, "s→b": false, "t→a": true, "t→b": true}
	if len(edges) != len(want) {
		t.Fatalf("PowerEdges = %+v", edges)
	}
	for _, e := range edges {
		if r, ok := want[e.SourceCIID+"→"+e.TargetCIID]; !ok || e.Attributes["redundant"] != r {
			t.Errorf("PowerEdges %s→%s: %v, want redundant=%v", e.SourceCIID, e.TargetCIID, e.Attributes, r)
		}
	}
	if s := edges[1]; s.Attributes["outlet_state"] != "off" || s.Refresh["outlet_state"] != "off" {
		t.Errorf("s→b keeps outlet_state=off: %+v / %+v", s.Attributes, s.Refresh)
	}
}

// logRecorder records the JSON log lines of slog's default logger.
type logRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logRecorder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// events returns the log records whose event is event.
func (l *logRecorder) events(t *testing.T, event string) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, line := range bytes.Split(l.buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["event"] == event {
			out = append(out, rec)
		}
	}
	return out
}

// TestPowerChainFailsClosed covers WP-160/WP-161 (REL-03, IMP-09, WP-498):
// when the suppressions cannot be loaded no powered_by edge is written and
// discovery.topology.failed is logged, so a suppressed power edge never
// comes back by accident. Control: with the suppressions loaded the edge
// is written.
func TestPowerChainFailsClosed(t *testing.T) {
	cis := ci.NewMemoryRepository()
	ctx := context.Background()
	var ids []string
	for _, name := range []string{"web01", "PDU-A"} {
		item := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: name, Status: "active"}
		if err := cis.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, item.ID)
	}
	items := []IngestItem{{RawData: map[string]any{"power": &DevicePower{DeviceType: "pdu",
		Outlets: []DeviceOutlet{{Name: "web01 PSU1", State: "on"}}}}}}

	logs := &logRecorder{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	rels := relationship.NewMemoryRepository()
	h := NewHandler(brokenSuppressions{NewMemoryRepository()}, cis, rels)
	if n := h.derivePowerChain(ctx, "org-1", items, []string{ids[1]}); n != 0 {
		t.Fatalf("powered_by edges written without suppressions: %d", n)
	}
	if list, _, _ := rels.List(ctx, "org-1", "", api.PaginationParams{Limit: 10}); len(list) != 0 {
		t.Errorf("relationships = %+v", list)
	}
	failed := logs.events(t, "discovery.topology.failed")
	if len(failed) != 1 || failed[0]["organization_id"] != "org-1" || failed[0]["level"] != "ERROR" {
		t.Errorf("discovery.topology.failed log = %+v, want one error of org-1", failed)
	}

	h2 := NewHandler(NewMemoryRepository(), cis, rels)
	if n := h2.derivePowerChain(ctx, "org-1", items, []string{ids[1]}); n != 1 {
		t.Errorf("powered_by edges with suppressions loaded: %d, want 1", n)
	}
}
