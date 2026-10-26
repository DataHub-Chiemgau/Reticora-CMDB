package collectorcmd

// Power readings and interface counters of the collector (WP-512, MON-02).
//
//   - TestPDUPowerValues (F-168): pollPowerValues asked every power hit,
//     PDUs too, for the UPS-MIB only; a PDU gave no samples. Now the power
//     hit carries device type and vendor family of the outlet table
//     (power.HitDevice) and the poll reads a PDU's load per phase or inlet
//     from the metering table of its family. Fake SNMP agents with the
//     outlet walks of collector/plugins/power/testdata (WP-498) plus the
//     metering tables in testdata/power: APC rPDU2, APC rPDU (outlets in
//     the sPDU tables) and Raritan PX give kind=power, device=pdu, target
//     and phase or inlet; a UPS gives the four UPS-MIB values (runtime in
//     seconds) with device=ups; a PDU of a family without a table gives no
//     samples and no error.
//   - TestInterfaceCountersWalkAllInterfaces (F-339): the standard poll
//     read the counters of ifIndex 1 only, and only with
//     RETICORA_METRICS_INTERVAL set. Now it walks ifTable/ifXTable for all
//     interfaces (at most 256 per device) with the ifIndex and ifName
//     labels and runs by default every 300 s; RETICORA_METRICS_INTERVAL=0
//     turns it off, RETICORA_SNMP_POLL_METRICS still replaces it.
//
// Retest (F-168, F-339):
//
//	cd collector && go test -count=1 -run 'TestPDUPowerValues|TestInterfaceCountersWalkAllInterfaces' ./collectorcmd/

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/power"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
)

const powerFakeCommunity = "power-fake-4711"

// loadWalks reads fixture walks (snmpwalk -On: "OID = TYPE: value",
// STRING, INTEGER and Gauge32) as the variables of an SNMP fake; the
// system group is left out, the fake serves its own.
func loadWalks(t *testing.T, paths ...string) []gosnmp.SnmpPDU {
	t.Helper()
	var mib []gosnmp.SnmpPDU
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			oid, typed, ok := strings.Cut(line, " = ")
			kind, value, ok2 := strings.Cut(typed, ": ")
			if !ok || !ok2 {
				t.Fatalf("%s:%d: %q is no walk line", path, n+1, line)
			}
			if strings.HasPrefix(oid, ".1.3.6.1.2.1.1.") {
				continue
			}
			switch kind {
			case "STRING":
				mib = append(mib, gosnmp.SnmpPDU{Name: oid, Type: gosnmp.OctetString, Value: []byte(strings.Trim(value, `"`))})
			case "INTEGER", "Gauge32":
				v, err := strconv.Atoi(value)
				if err != nil {
					t.Fatalf("%s:%d: %v", path, n+1, err)
				}
				if kind == "INTEGER" {
					mib = append(mib, gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Integer, Value: v})
				} else {
					mib = append(mib, gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Gauge32, Value: uint(v)})
				}
			default:
				t.Fatalf("%s:%d: type %s not supported", path, n+1, kind)
			}
		}
	}
	return mib
}

// startPowerFake serves mib and puts SNMP and power plugins that ask it
// into the registry.
func startPowerFake(t *testing.T, mib []gosnmp.SnmpPDU) *fakesnmp.Agent {
	t.Helper()
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: powerFakeCommunity, SysName: "power-01",
		SysDescr: "Reticora SNMP fake", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"}, mib...)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Serve() }()
	t.Cleanup(func() {
		_ = agent.Close()
		<-done
	})
	port := agent.Addr().Port
	auditUsePlugins(t, &snmp.Plugin{Timeout: time.Second, Port: port}, &power.Plugin{Timeout: time.Second, Concurrency: 1, Port: port})
	return agent
}

// sampleKey names a sample by metric and its row (phase or inlet).
func sampleKey(s plugins.MetricSample) string {
	row := ""
	for _, label := range []string{"phase", "inlet"} {
		if v, ok := s.Labels[label]; ok {
			row += " " + label + "=" + v
		}
	}
	return s.Name + row
}

func TestPDUPowerValues(t *testing.T) {
	outletWalk := func(name string) string { return filepath.Join("..", "plugins", "power", "testdata", name) }
	meterWalk := func(name string) string { return filepath.Join("testdata", "power", name) }
	for _, tc := range []struct {
		name   string
		walks  []string
		device string
		// want: value by metric and row; nil: no samples.
		want map[string]float64
	}{
		{"APC rPDU2", []string{meterWalk("apc-rpdu2.walk")}, "pdu", map[string]float64{
			"pdu_current_amps phase=1": 5.2, "pdu_current_amps phase=2": 3.1, "pdu_current_amps phase=3": 0,
			"pdu_active_power_watts phase=1": 1180, "pdu_active_power_watts phase=2": 690, "pdu_active_power_watts phase=3": 0,
		}},
		// The banks of the phase repeat its load and are left out.
		{"APC rPDU", []string{outletWalk("apc-spdu.walk"), meterWalk("apc-rpdu-load.walk")}, "pdu", map[string]float64{
			"pdu_current_amps phase=1": 7.4,
		}},
		{"Raritan PX", []string{outletWalk("raritan-px.walk"), meterWalk("raritan-px-inlet.walk")}, "pdu", map[string]float64{
			"pdu_current_amps inlet=1": 4.25, "pdu_current_amps inlet=2": 1.1,
			"pdu_active_power_watts inlet=1": 912, "pdu_active_power_watts inlet=2": 236,
		}},
		{"APC UPS", []string{outletWalk("apc-ups.walk"), meterWalk("ups-values.walk")}, "ups", map[string]float64{
			"ups_battery_status": 2, "ups_battery_runtime_seconds": 42 * 60, "ups_battery_charge_percent": 97, "ups_output_load_percent": 31,
		}},
		// What the UPS does not answer (noSuchObject) gives no sample.
		{"UPS with battery status only", []string{outletWalk("ups-no-outlets.walk")}, "ups", map[string]float64{
			"ups_battery_status": 2,
		}},
		{"PDU without known table", []string{meterWalk("epdu-unknown.walk")}, "pdu", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			startPowerFake(t, loadWalks(t, tc.walks...))
			creds := map[string]string{"community": powerFakeCommunity}
			ctx := context.Background()

			hits, err := pluginRegistry["power"].Probe(ctx, []string{"127.0.0.1"}, creds)
			if err != nil || len(hits) != 1 {
				t.Fatalf("Probe = %+v, %v; want one power device", hits, err)
			}
			if deviceType, _ := power.HitDevice(&hits[0]); deviceType != tc.device {
				t.Fatalf("hit device type = %q, want %q", deviceType, tc.device)
			}
			samples := pollPowerValues(ctx, hits, creds)
			got := map[string]float64{}
			for _, s := range samples {
				if s.Labels["kind"] != "power" || s.Labels["target"] != "127.0.0.1" || s.Labels["device"] != tc.device {
					t.Errorf("sample %s labels = %v, want kind=power, target=127.0.0.1, device=%s", s.Name, s.Labels, tc.device)
				}
				key := sampleKey(s)
				if _, dup := got[key]; dup {
					t.Errorf("sample %q twice", key)
				}
				got[key] = s.Value
			}
			if len(got) != len(tc.want) {
				t.Errorf("samples = %v, want %v", got, tc.want)
			}
			for key, want := range tc.want {
				if v, ok := got[key]; !ok || fmt.Sprintf("%.3f", v) != fmt.Sprintf("%.3f", want) {
					t.Errorf("%s = %v (present %v), want %v", key, v, ok, want)
				}
			}
		})
	}
}

func TestInterfaceCountersWalkAllInterfaces(t *testing.T) {
	var mib []gosnmp.SnmpPDU
	for i := 1; i <= 3; i++ {
		idx := fmt.Sprintf(".%d", i)
		mib = append(mib,
			gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.2.2.1.8" + idx, Type: gosnmp.Integer, Value: 1},
			gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.31.1.1.1.1" + idx, Type: gosnmp.OctetString, Value: []byte(fmt.Sprintf("eth%d", i-1))},
			gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.31.1.1.1.6" + idx, Type: gosnmp.Counter64, Value: uint64(i) * 1000},
			gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.31.1.1.1.10" + idx, Type: gosnmp.Counter64, Value: uint64(i) * 2000})
	}
	startPowerFake(t, mib)

	// Default: on every 300 s with the standard poll; 0 turns it off; the
	// OID list replaces the standard poll.
	t.Setenv("RETICORA_METRICS_INTERVAL", "")
	t.Setenv("RETICORA_SNMP_POLL_METRICS", "")
	cfg := loadCollectorConfig()
	if cfg.MetricsInterval != 300*time.Second || cfg.PollMetrics != nil {
		t.Errorf("default metric poll: interval %v, OIDs %v; want 300s and the standard poll", cfg.MetricsInterval, cfg.PollMetrics)
	}
	t.Setenv("RETICORA_METRICS_INTERVAL", "0")
	t.Setenv("RETICORA_SNMP_POLL_METRICS", "ifInOctets=1.3.6.1.2.1.2.2.1.10.1")
	if off := loadCollectorConfig(); off.MetricsInterval != 0 || len(off.PollMetrics) != 1 || off.PollMetrics[0].Name != "ifInOctets" {
		t.Errorf("RETICORA_METRICS_INTERVAL=0: interval %v, OIDs %v; want off and the configured OID", off.MetricsInterval, off.PollMetrics)
	}

	poll, ok := metricPollFunc(cfg)
	if !ok {
		t.Fatal("no poll for the standard metrics")
	}
	samples := pollTargets(context.Background(), []string{"127.0.0.1"}, map[string]string{"community": powerFakeCommunity}, poll)
	got := map[string]float64{}
	for _, s := range samples {
		if s.Labels["target"] != "127.0.0.1" || s.Labels["ifName"] != "eth"+strconv.Itoa(atoi(t, s.Labels["ifIndex"])-1) {
			t.Errorf("sample %s labels = %v, want target, ifIndex and its ifName", s.Name, s.Labels)
		}
		got[s.Name+" "+s.Labels["ifIndex"]] = s.Value
	}
	want := map[string]float64{}
	for i := 1; i <= 3; i++ {
		want[fmt.Sprintf("if_in_octets %d", i)] = float64(i * 1000)
		want[fmt.Sprintf("if_out_octets %d", i)] = float64(i * 2000)
		want[fmt.Sprintf("if_oper_status %d", i)] = 1
	}
	if len(got) != 9 || len(samples) != 9 {
		t.Fatalf("samples = %v, want 3×2 counters and 3 states: %v", got, want)
	}
	for key, v := range want {
		if got[key] != v {
			t.Errorf("%s = %v, want %v", key, got[key], v)
		}
	}

	// The configured OIDs replace the standard poll.
	explicit, ok := metricPollFunc(collectorConfig{PollMetrics: []snmp.PollMetric{{Name: "if1_in", OID: "1.3.6.1.2.1.31.1.1.1.6.1"}}})
	if !ok {
		t.Fatal("no poll for the configured OIDs")
	}
	if s := pollTargets(context.Background(), []string{"127.0.0.1"}, map[string]string{"community": powerFakeCommunity}, explicit); len(s) != 1 || s[0].Name != "if1_in" || s[0].Value != 1000 {
		t.Errorf("configured OID poll = %+v, want if1_in = 1000", s)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("%q is no number", s)
	}
	return n
}
