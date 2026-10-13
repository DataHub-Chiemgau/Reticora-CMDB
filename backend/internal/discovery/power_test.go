package discovery

import (
	"context"
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

	// A later batch adds a second feed for db01: the new edge is redundant.
	if n := h.derivePowerChain(ctx, "org-1", []IngestItem{power("db01 PSU2")}, []string{pduB}); n != 1 {
		t.Fatalf("second feed: %d", n)
	}
	list, _, _ := rels.List(ctx, "org-1", db, api.PaginationParams{Limit: 50})
	for _, r := range list {
		if r.TargetCIID == pduB && r.Attributes["redundant"] != true {
			t.Errorf("second feed of db01 not redundant: %+v", r)
		}
	}
}
