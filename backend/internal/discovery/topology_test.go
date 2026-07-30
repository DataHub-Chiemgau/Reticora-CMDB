package discovery

import (
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/wire"
)

func TestBuildNeighborIndexResolve(t *testing.T) {
	items := []ci.Item{
		{ID: "ci-switch", ManagementIP: "10.0.0.1", PrimaryMAC: "AA:BB:CC:00:00:01", Hostname: "sw1"},
		{ID: "ci-pdu", ManagementIP: "10.0.0.2", Hostname: "PDU-1", FQDN: "pdu-1.dc.local"},
	}
	idx := BuildNeighborIndex(items)

	tests := []struct {
		name          string
		ip, mac, host string
		wantID        string
		wantOK        bool
	}{
		{"by ip", "10.0.0.1", "", "", "ci-switch", true},
		{"by mac normalized", "", "aabb.cc00.0001", "", "ci-switch", true},
		{"by hostname case-insensitive", "", "", "pdu-1", "ci-pdu", true},
		{"by fqdn", "", "", "PDU-1.DC.LOCAL", "ci-pdu", true},
		{"no match", "192.168.1.1", "", "", "", false},
		{"empty", "", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := idx.Resolve(tc.ip, tc.mac, tc.host)
			if ok != tc.wantOK || id != tc.wantID {
				t.Fatalf("Resolve(%q,%q,%q)=%q,%v want %q,%v", tc.ip, tc.mac, tc.host, id, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

func TestDeriveRelationships(t *testing.T) {
	resolve := func(ip, mac, host string) (string, bool) {
		switch {
		case ip == "10.0.0.2":
			return "ci-pdu", true
		case mac == "aa:bb:cc:00:00:03":
			return "ci-peer", true
		case host == "sw2":
			return "ci-sw2", true
		case ip == "10.0.0.9":
			return "ci-self", true
		}
		return "", false
	}

	tests := []struct {
		name       string
		source     string
		rels       []wire.IngestRelationship
		suppressed func(string, string) bool
		want       []DerivedRelationship
	}{
		{
			name:   "powered_by resolved by ip",
			source: "ci-src",
			rels:   []wire.IngestRelationship{{TargetIP: "10.0.0.2", TypeKey: "powered_by", Confidence: 0.9}},
			want:   []DerivedRelationship{{SourceCIID: "ci-src", TargetCIID: "ci-pdu", RelType: "powered_by", Confidence: 0.9}},
		},
		{
			name:   "connected_to default confidence and type",
			source: "ci-src",
			rels:   []wire.IngestRelationship{{TargetMAC: "aa:bb:cc:00:00:03", TypeKey: "lldp"}},
			want:   []DerivedRelationship{{SourceCIID: "ci-src", TargetCIID: "ci-peer", RelType: "connected_to", Confidence: defaultConfidence}},
		},
		{
			name:   "unresolved neighbor skipped",
			source: "ci-src",
			rels:   []wire.IngestRelationship{{TargetIP: "203.0.113.7", TypeKey: "connected_to"}},
			want:   nil,
		},
		{
			name:   "self loop skipped",
			source: "ci-self",
			rels:   []wire.IngestRelationship{{TargetIP: "10.0.0.9", TypeKey: "connected_to"}},
			want:   nil,
		},
		{
			name:   "duplicate deduplicated",
			source: "ci-src",
			rels: []wire.IngestRelationship{
				{TargetHostname: "sw2", TypeKey: "connected_to"},
				{TargetHostname: "sw2", TypeKey: "connected_to"},
			},
			want: []DerivedRelationship{{SourceCIID: "ci-src", TargetCIID: "ci-sw2", RelType: "connected_to", Confidence: defaultConfidence}},
		},
		{
			name:       "suppressed pair skipped",
			source:     "ci-src",
			rels:       []wire.IngestRelationship{{TargetIP: "10.0.0.2", TypeKey: "powered_by"}},
			suppressed: func(s, tgt string) bool { return s == "ci-src" && tgt == "ci-pdu" },
			want:       nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveRelationships(tc.source, tc.rels, resolve, tc.suppressed)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d edges %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("edge %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestNormalizeRelType(t *testing.T) {
	cases := map[string]string{
		"":             "connected_to",
		"connected_to": "connected_to",
		"LLDP":         "connected_to",
		"powered_by":   "powered_by",
		"powers":       "powered_by",
		"unknown_key":  "connected_to",
	}
	for in, want := range cases {
		if got := normalizeRelType(in); got != want {
			t.Errorf("normalizeRelType(%q)=%q want %q", in, got, want)
		}
	}
}
