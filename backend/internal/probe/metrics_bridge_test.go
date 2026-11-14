package probe_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The metric bridge (MON-02, MON-03, WP-976 AK3).

func bridgeResult(values map[string]float64, labels map[string]string) *probewire.Result {
	return &probewire.Result{ProbeID: "p1", Epoch: 1, Seq: 1, ObservedAt: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC),
		Status: probewire.StatusOK, Values: values, Labels: labels}
}

func TestBridgeMapsValuesWithProbeLabels(t *testing.T) {
	r := bridgeResult(map[string]float64{"rtt_min_ms": 1.5, "jitter_ms": 0.2}, map[string]string{"ifIndex": "3", "ifName": "ge-0/0/1"})
	got := probe.MetricsBridge{}.Samples("org", &probe.BridgeProbe{ID: "p1", Type: "icmp", CIID: "ci1"}, r, false)
	if len(got) != 2 {
		t.Fatalf("%d samples, want 2", len(got))
	}
	for _, m := range got {
		if m.OrgID != "org" || m.CIID != "ci1" || !m.Timestamp.Equal(r.ObservedAt) || m.Labels[probe.LabelKind] != probe.LabelKindProbe ||
			m.Labels[probe.LabelProbeID] != "p1" || m.Labels["ifIndex"] != "3" || m.Labels["ifName"] != "ge-0/0/1" {
			t.Errorf("sample %+v", m)
		}
	}
	if got[0].Name != "jitter_ms" || got[0].Value != 0.2 || got[1].Name != "rtt_min_ms" || got[1].Value != 1.5 {
		t.Errorf("samples %+v, want jitter_ms 0.2 and rtt_min_ms 1.5", got)
	}
	many := map[string]string{}
	for i := range 30 {
		many[string(rune('a'+i%26))+string(rune('a'+i/26))] = "x"
	}
	got = probe.MetricsBridge{}.Samples("org", &probe.BridgeProbe{ID: "p1", Type: "icmp", CIID: "ci1"},
		bridgeResult(map[string]float64{"jitter_ms": 1}, many), false)
	if len(got) != 1 || len(got[0].Labels) != probe.MaxBridgeLabels {
		t.Errorf("labels %d, want at most %d", len(got[0].Labels), probe.MaxBridgeLabels)
	}
}

func TestBridgeSkipsProbeWithoutCI(t *testing.T) {
	before := testutil.ToFloat64(probe.BridgeDiscarded(probe.BridgeNoCI))
	got := probe.MetricsBridge{}.Samples("org", &probe.BridgeProbe{ID: "p1", Type: "icmp"},
		bridgeResult(map[string]float64{"jitter_ms": 1, "rtt_min_ms": 2}, nil), false)
	if len(got) != 0 || testutil.ToFloat64(probe.BridgeDiscarded(probe.BridgeNoCI))-before != 2 {
		t.Errorf("without CI: %d samples, discarded %v", len(got), testutil.ToFloat64(probe.BridgeDiscarded(probe.BridgeNoCI))-before)
	}
}

func TestBridgeDropsUndeclaredMetric(t *testing.T) {
	before := testutil.ToFloat64(probe.BridgeDiscarded(probe.BridgeUnknownMetric))
	got := probe.MetricsBridge{}.Samples("org", &probe.BridgeProbe{ID: "p1", Type: "icmp", CIID: "ci1"},
		bridgeResult(map[string]float64{"jitter_ms": 1, "bogus": 2}, nil), false)
	if len(got) != 1 || got[0].Name != "jitter_ms" || testutil.ToFloat64(probe.BridgeDiscarded(probe.BridgeUnknownMetric))-before != 1 {
		t.Errorf("undeclared value: %+v", got)
	}
}

func TestBridgeSkipsStaleEpoch(t *testing.T) {
	before := testutil.ToFloat64(probe.BridgeDiscarded(probe.BridgeStaleEpoch))
	got := probe.MetricsBridge{}.Samples("org", &probe.BridgeProbe{ID: "p1", Type: "icmp", CIID: "ci1"},
		bridgeResult(map[string]float64{"jitter_ms": 1}, nil), true)
	if len(got) != 0 || testutil.ToFloat64(probe.BridgeDiscarded(probe.BridgeStaleEpoch))-before != 1 {
		t.Errorf("stale epoch: %+v", got)
	}
}

// TestBridgeRedfishSensorsCarryCIID covers WP-998 AK3 and AK6: the
// labeled sensor values of a redfish result become samples of the probe's
// CI under the plain value name with the labels sensor and psu.
func TestBridgeRedfishSensorsCarryCIID(t *testing.T) {
	temp := probewire.ValueKey("probe_redfish_temperature_celsius", map[string]string{"sensor": "1/CPU1"})
	psu := probewire.ValueKey("probe_redfish_psu_input_watts", map[string]string{"psu": "1/PSU1"})
	r := bridgeResult(map[string]float64{temp: 61, psu: 240, "probe_redfish_health": 0}, nil)
	got := probe.MetricsBridge{}.Samples("org", &probe.BridgeProbe{ID: "p1", Type: "redfish", CIID: "ci1"}, r, false)
	if len(got) != 3 {
		t.Fatalf("%d samples, want 3: %+v", len(got), got)
	}
	want := map[string]struct {
		value         float64
		label, lvalue string
	}{
		"probe_redfish_temperature_celsius": {61, "sensor", "1/CPU1"},
		"probe_redfish_psu_input_watts":     {240, "psu", "1/PSU1"},
		"probe_redfish_health":              {0, "", ""},
	}
	for _, m := range got {
		w, ok := want[m.Name]
		if !ok || m.Value != w.value || m.CIID != "ci1" || m.Labels[probe.LabelProbeID] != "p1" {
			t.Errorf("sample %+v", m)
			continue
		}
		if w.label != "" && m.Labels[w.label] != w.lvalue {
			t.Errorf("%s: labels %v, want %s=%s", m.Name, m.Labels, w.label, w.lvalue)
		}
	}
	before := testutil.ToFloat64(probe.BridgeDiscarded(probe.BridgeUnknownMetric))
	got = probe.MetricsBridge{}.Samples("org", &probe.BridgeProbe{ID: "p1", Type: "redfish", CIID: "ci1"},
		bridgeResult(map[string]float64{`probe_redfish_bogus{sensor="x"}`: 1, `probe_redfish_fan_rpm{fan=`: 2}, nil), false)
	if len(got) != 0 || testutil.ToFloat64(probe.BridgeDiscarded(probe.BridgeUnknownMetric))-before != 2 {
		t.Errorf("undeclared or malformed labeled keys: %+v", got)
	}
}
