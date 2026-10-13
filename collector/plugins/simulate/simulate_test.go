package simulate

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// TestGenerateIsDeterministic covers SIM-02: the fleet has 30 valid
// devices, the same time yields identical records, and the identities do
// not change between runs.
func TestGenerateIsDeterministic(t *testing.T) {
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	a, b := Generate(at), Generate(at)
	if len(a) != DeviceCount {
		t.Fatalf("generated %d devices, want %d", len(a), DeviceCount)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two runs at the same time differ")
	}
	later := Generate(at.Add(time.Hour))
	serials, macs, ips := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range a {
		if v := a[i].Validate(); len(v) > 0 {
			t.Errorf("%s: %v", a[i].Identity.Hostname, v)
		}
		if a[i].Identity != later[i].Identity || a[i].TargetIP != later[i].TargetIP {
			t.Errorf("%s: identity changed between runs", a[i].Identity.Hostname)
		}
		if a[i].RecordID == later[i].RecordID {
			t.Errorf("%s: record id reused by a later run", a[i].Identity.Hostname)
		}
		serials[a[i].Identity.SerialNumber] = true
		macs[a[i].Identity.PrimaryMAC] = true
		ips[a[i].TargetIP] = true
	}
	if len(serials) != DeviceCount || len(macs) != DeviceCount || len(ips) != DeviceCount {
		t.Errorf("identities not unique: %d serials, %d MACs, %d addresses", len(serials), len(macs), len(ips))
	}
}

// TestFleetShape checks the topology the backend derives from: LLDP
// neighbors point at fleet devices, and every server is fed by both PDUs.
func TestFleetShape(t *testing.T) {
	types := map[string]int{}
	feeds := map[string]int{}
	for i, rec := range Generate(time.Unix(0, 0)) {
		types[rec.Classification.CIType]++
		for _, n := range rec.Neighbors {
			if indexOf(n.RemoteHostname) < 0 || n.RemoteMAC != MAC(indexOf(n.RemoteHostname)) {
				t.Errorf("%s: neighbor %+v is not in the fleet", fleet[i].name, n)
			}
		}
		if rec.Power != nil && fleet[i].pdu {
			for _, o := range rec.Power.Outlets {
				feeds[o.Name[:len("sim-srv-00")]]++
			}
		}
	}
	want := map[string]int{"switch": 6, "firewall": 2, "router": 1, "ups": 1, "pdu": 2, "server": 16, "nas": 2}
	if !reflect.DeepEqual(types, want) {
		t.Errorf("types = %v, want %v", types, want)
	}
	if len(feeds) != 16 {
		t.Errorf("fed servers = %d", len(feeds))
	}
	for srv, n := range feeds {
		if n != 2 {
			t.Errorf("%s has %d feeds, want 2", srv, n)
		}
	}
}

// TestPluginProbeAndCollect drives the plugin like the discovery cycle.
func TestPluginProbeAndCollect(t *testing.T) {
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	p := &Plugin{Now: func() time.Time { return at }}
	ctx := context.Background()
	hits, err := p.Probe(ctx, nil, nil)
	if err != nil || len(hits) != DeviceCount {
		t.Fatalf("probe = %d hits, %v", len(hits), err)
	}
	some, err := p.Probe(ctx, []string{IP(0), IP(29), "192.0.2.1"}, nil)
	if err != nil || len(some) != 2 {
		t.Fatalf("probe of two fleet addresses = %d hits, %v", len(some), err)
	}
	rec, err := p.Collect(ctx, IP(29), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := Generate(at)[29]; !reflect.DeepEqual(*rec, want) {
		t.Errorf("collect = %+v, want %+v", *rec, want)
	}
	if _, err := p.Collect(ctx, "192.0.2.1", nil); err == nil {
		t.Error("collect of an unknown address succeeded")
	}
}
