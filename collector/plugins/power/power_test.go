package power

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

func TestClassifyPowerDevice(t *testing.T) {
	cases := []struct {
		mfg, model, want string
	}{
		{"APC", "Smart-UPS 1500", "ups"},
		{"APC", "Rack PDU AP8941", "pdu"},
		{"Eaton", "ATS 16A", "ats"},
		{"", "automatic transfer switch", "ats"},
		{"Generic", "Unknown", "ups"},
		{"", "", "ups"},
	}
	for _, c := range cases {
		if got := classifyPowerDevice(c.mfg, c.model); got != c.want {
			t.Errorf("classifyPowerDevice(%q, %q) = %q, want %q", c.mfg, c.model, got, c.want)
		}
	}
}

func TestIsPowerDeviceSilent(t *testing.T) {
	if (&Plugin{}).isPowerDevice(context.Background(), "192.0.2.1", "public", 150*time.Millisecond) {
		t.Error("isPowerDevice = true, want false for silent target")
	}
}

// Probe and collection against the SNMP fake (internal/fakesnmp, ephemeral
// port, answers its community only). The values come from the decoded
// varbinds: the plugin's former v2c parser (extractStringValue) returned
// the first printable OctetString of the response, the echoed community,
// so every device that answered the community was a power device with the
// community as name, model, serial, manufacturer and firmware.

const fakeCommunity = "fake-community-4711"

func startFake(t *testing.T, extra []gosnmp.SnmpPDU) *fakesnmp.Agent {
	t.Helper()
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: fakeCommunity, SysName: "fake-sw-01",
		SysDescr: "Reticora SNMP fake", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, extra...)
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

func octets(oid, value string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.OctetString, Value: []byte(value)}
}

func TestProbeAndCollectAgainstSNMPFake(t *testing.T) {
	cases := []struct {
		name  string
		extra []gosnmp.SnmpPDU
		// power: the probe finds a power device; want: the collected
		// identity.
		power bool
		want  plugins.Result
	}{
		{
			// An SNMP device without UPS identity (system group only).
			name: "switch", want: plugins.Result{Name: "127.0.0.1"},
		},
		{
			// UPS-MIB identity first; the APC OIDs are only the fallback.
			name: "UPS-MIB", power: true,
			extra: []gosnmp.SnmpPDU{
				octets(oidUPSIdentManufacturer, "APC"), octets(oidUPSIdentModel, "Smart-UPS 1500"),
				octets(oidUPSIdentSerial, "AS1234567890\x00\x00"), octets(oidUPSIdentFirmware, "UPS 09.3 / ID18"),
				octets(oidAPCModel, "SMT1500RMI2U"), octets(oidAPCSerial, "AS0000000000"), octets(oidAPCFirmware, "UPS 10.0"),
			},
			want: plugins.Result{Name: "Smart-UPS 1500", Manufacturer: "APC", Model: "Smart-UPS 1500", Serial: "AS1234567890", Firmware: "UPS 09.3 / ID18"},
		},
		{
			name: "APC PowerNet-MIB only", power: true,
			extra: []gosnmp.SnmpPDU{
				octets(oidAPCModel, "Smart-UPS X 3000"), octets(oidAPCSerial, "AS1111111111"), octets(oidAPCFirmware, "UPS 15.5 / ID20"),
			},
			want: plugins.Result{Name: "Smart-UPS X 3000", Model: "Smart-UPS X 3000", Serial: "AS1111111111", Firmware: "UPS 15.5 / ID20"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := startFake(t, tc.extra)
			p := &Plugin{Timeout: time.Second, Concurrency: 1, Port: agent.Addr().Port}
			creds := map[string]string{"community": fakeCommunity}

			hits, err := p.Probe(context.Background(), []string{"127.0.0.1"}, creds)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			switch {
			case !tc.power && len(hits) != 0:
				t.Errorf("Probe = %+v, want no power device", hits)
			case tc.power && len(hits) != 1:
				t.Errorf("Probe = %d hits, want 1", len(hits))
			case tc.power && (hits[0].CIType != "power-device" || hits[0].Name != "127.0.0.1" || hits[0].Attributes["upsCapable"] != true):
				t.Errorf("hit = %+v, want a power-device named by its address", hits[0])
			}

			res, err := p.collect(context.Background(), "127.0.0.1", creds)
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			got := plugins.Result{Name: res.Name, Manufacturer: res.Manufacturer, Model: res.Model, Serial: res.Serial, Firmware: res.Firmware}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("collected identity = %+v, want %+v", got, tc.want)
			}
			if tc.power {
				if res.Attributes["deviceType"] != "ups" {
					t.Errorf("deviceType = %v, want ups", res.Attributes["deviceType"])
				}
				if len(res.Metrics) != 4 {
					t.Errorf("expected 4 UPS metric placeholders, got %d", len(res.Metrics))
				}
				for _, m := range res.Metrics {
					if m.Labels["target"] != "127.0.0.1" {
						t.Errorf("metric %q missing target label", m.Name)
					}
				}
			}
			rec, err := p.Collect(context.Background(), "127.0.0.1", creds)
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}

			// The community is in no field or attribute of any result.
			for what, v := range map[string]any{"probe hits": hits, "result": res, "record": rec} {
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), fakeCommunity) {
					t.Errorf("the community is part of the %s: %s", what, raw)
				}
			}
			// The device answered: the fake saw only answered v2c
			// requests with the community.
			seen := agent.Requests()
			if len(seen) == 0 {
				t.Fatal("the fake received no request")
			}
			for _, r := range seen {
				if r.Version != "2c" || r.Community != fakeCommunity || !r.Answered {
					t.Errorf("request %+v: want an answered v2c request with the community", r)
				}
			}
		})
	}
}

func TestDiscoverSkipsSilentTarget(t *testing.T) {
	p := &Plugin{Timeout: 150 * time.Millisecond, Concurrency: 2}
	results, err := p.Probe(context.Background(), []string{"192.0.2.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for silent target, got %d", len(results))
	}
}

func TestDiscoverNoTargetsReturnsEmptySlice(t *testing.T) {
	p := New()
	results, err := p.Probe(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v", results)
	}
}

func TestDiscoverCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Plugin{Timeout: 100 * time.Millisecond}
	results, err := p.Probe(ctx, []string{"192.0.2.1"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Discover error = %v, want context.Canceled", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

func TestNewDefaults(t *testing.T) {
	p := New()
	if p.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", p.Timeout, defaultTimeout)
	}
	if p.Concurrency != defaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", p.Concurrency, defaultConcurrency)
	}
	if p.Name() != "power" {
		t.Errorf("Name() = %q, want power", p.Name())
	}
}
