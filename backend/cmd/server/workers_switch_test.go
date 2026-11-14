package main

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// TestAlarmCheckWorkerSwitchesAlone covers WP-986 AK1 and AK7 (OPS-02
// [P3]): the worker alarm_check belongs to the module monitoring and runs
// only with its switch and the module; switched off alone, the other
// workers of the module still run. Without database and event stream it is
// built all the same and every cycle fails its check (no silent worker).
func TestAlarmCheckWorkerSwitchesAlone(t *testing.T) {
	if !monitoringWorker(config.WorkerAlarmCheck) {
		t.Fatal("alarm_check is no worker of the module monitoring")
	}
	if !slices.Contains(config.WorkerNames, config.WorkerAlarmCheck) {
		t.Fatal("alarm_check is not registered in config.WorkerNames")
	}
	mods, err := server.ParseModules(server.ModuleMonitoring)
	if err != nil {
		t.Fatal(err)
	}
	none, err := server.ParseModules("")
	if err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{true, false} {
		for _, name := range config.WorkerNames {
			t.Setenv(config.WorkerEnvKey(name), "true")
		}
		t.Setenv(config.WorkerEnvKey(config.WorkerAlarmCheck), strconv.FormatBool(on))
		cfg := config.Load()
		enabled := workerSwitch(cfg, mods)
		if enabled(config.WorkerAlarmCheck) != on {
			t.Errorf("switch %v: alarm_check starts %v", on, enabled(config.WorkerAlarmCheck))
		}
		for _, other := range []string{config.WorkerMonitoring, config.WorkerProbeAssign, config.WorkerProbeIngest} {
			if !enabled(other) {
				t.Errorf("switch alarm_check %v: %s does not start, want it independent", on, other)
			}
		}
		if workerSwitch(cfg, none)(config.WorkerAlarmCheck) {
			t.Errorf("switch %v: alarm_check starts without the module monitoring", on)
		}
	}
	repos := server.MemoryRepositories()
	built, err := buildWorkers(&workerDeps{cfg: config.Load(), repos: &repos, cycle: testCycle, modules: mods})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(built, func(w server.Worker) bool { return w.Name == config.WorkerAlarmCheck })
	if i < 0 {
		t.Fatal("buildWorkers builds no worker alarm_check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*testCycle)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		built[i].Run(ctx)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker alarm_check without database does not stop with its context")
	}
}
