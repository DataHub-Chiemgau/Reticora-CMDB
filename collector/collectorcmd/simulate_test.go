package collectorcmd

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/simulate"
)

// TestSimulationRefusedInProduction covers SIM-02: RETICORA_SIMULATE cannot
// be enabled in production, and without it nothing changes.
func TestSimulationRefusedInProduction(t *testing.T) {
	t.Cleanup(func() { delete(pluginRegistry, "simulate") })
	cfg := collectorConfig{Simulate: true, Protocols: []string{"snmp"}}
	if err := applySimulation(&cfg, " Production "); !errors.Is(err, errSimulateInProduction) {
		t.Fatalf("production: %v", err)
	}
	if _, ok := pluginRegistry["simulate"]; ok || cfg.Protocols[0] != "snmp" {
		t.Fatal("refused simulation changed the configuration")
	}
	off := collectorConfig{Protocols: []string{"snmp"}}
	if err := applySimulation(&off, "development"); err != nil || off.Protocols[0] != "snmp" {
		t.Fatalf("disabled simulation: %v %v", err, off.Protocols)
	}
	t.Setenv("RETICORA_SIMULATE", "true")
	if !boolEnv("RETICORA_SIMULATE") {
		t.Error("RETICORA_SIMULATE=true not read")
	}
}

// TestSimulationUploadsTheFleet: with RETICORA_SIMULATE the discovery cycle
// uploads the 30 simulated devices to the ingest endpoint, also when the
// server assigned scopes.
func TestSimulationUploadsTheFleet(t *testing.T) {
	t.Cleanup(func() {
		collectorScopes = &scopeCache{}
		delete(pluginRegistry, "simulate")
	})
	var mu sync.Mutex
	var got []plugins.DeviceRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/discovery/ingest" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var batch plugins.DeviceRecordBatch
		if err := json.NewDecoder(zr).Decode(&batch); err != nil {
			t.Error(err)
		}
		mu.Lock()
		got = append(got, batch.Records...)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL, "")
	cfg.Simulate = true
	if err := applySimulation(&cfg, "development"); err != nil {
		t.Fatal(err)
	}
	up, err := newUploader(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-000000000002", Kind: "cidr", Target: "10.0.0.0/29", Plugins: []string{"snmp"}}}, nil)
	now := time.Now()
	if n := runDueDiscovery(context.Background(), &cfg, up, newScheduler(), &hostCache{}, now); n != simulate.DeviceCount {
		t.Fatalf("uploaded %d records, want %d", n, simulate.DeviceCount)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != simulate.DeviceCount {
		t.Fatalf("ingest received %d records", len(got))
	}
	hosts := map[string]bool{}
	for i := range got {
		hosts[got[i].Identity.Hostname] = true
		if got[i].Attributes["simulated"] != true {
			t.Errorf("%s is not marked simulated", got[i].Identity.Hostname)
		}
	}
	if len(hosts) != simulate.DeviceCount || !hosts["sim-core-01"] || !hosts["sim-srv-16"] {
		t.Errorf("hosts = %v", hosts)
	}
}
