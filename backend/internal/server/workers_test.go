package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gopkg.in/yaml.v3"
)

// TestDisabledWorkerDoesNotStart covers WP-098 (OPS-02): a worker whose
// switch is off is never started, the others run until Stop; unregistered
// or duplicate workers are refused.
func TestDisabledWorkerDoesNotStart(t *testing.T) {
	var exportRuns, ingestRuns atomic.Int32
	running := make(chan struct{})
	worker := func(counter *atomic.Int32) func(context.Context) {
		return func(ctx context.Context) {
			counter.Add(1)
			running <- struct{}{}
			<-ctx.Done()
		}
	}
	enabled := func(name string) bool { return name != config.WorkerExport }

	set, err := StartWorkers(enabled,
		Worker{Name: config.WorkerExport, Run: worker(&exportRuns)},
		Worker{Name: config.WorkerIngest, Run: worker(&ingestRuns)},
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-running:
	case <-time.After(5 * time.Second):
		t.Fatal("enabled worker did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = set.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if exportRuns.Load() != 0 || ingestRuns.Load() != 1 {
		t.Errorf("runs: export %d, ingest %d", exportRuns.Load(), ingestRuns.Load())
	}
	if !slices.Equal(set.Started(), []string{config.WorkerIngest}) || !slices.Equal(set.Disabled(), []string{config.WorkerExport}) {
		t.Errorf("started %v, disabled %v", set.Started(), set.Disabled())
	}

	noop := func(context.Context) {}
	all := func(string) bool { return true }
	if _, err = StartWorkers(all, Worker{Name: "unknown", Run: noop}); err == nil {
		t.Error("unregistered worker accepted")
	}
	if _, err = StartWorkers(all, Worker{Name: config.WorkerIngest, Run: noop}, Worker{Name: config.WorkerIngest, Run: noop}); err == nil {
		t.Error("duplicate worker accepted")
	}
	if _, err = StartWorkers(all, Worker{Name: config.WorkerIngest}); err == nil {
		t.Error("worker without Run accepted")
	}
}

// TestEveryRegisteredWorkerHasASwitch: each name in config.WorkerNames is
// switchable through its environment variable.
func TestEveryRegisteredWorkerHasASwitch(t *testing.T) {
	for _, name := range config.WorkerNames {
		t.Setenv(config.WorkerEnvKey(name), "false")
	}
	cfg := config.Load()
	for _, name := range config.WorkerNames {
		if cfg.WorkerEnabled(name) {
			t.Errorf("%s stays enabled with %s=false", name, config.WorkerEnvKey(name))
		}
	}
}

// TestWorkerMetricsAreRegisteredAndRecorded covers WP-103 (OPS-05): /metrics
// exports run, error, last-success, enabled, up and backlog series per
// worker; StartWorkers marks running and disabled workers, a stopped loop
// drops worker_up to 0 and the backlog probe is sampled.
func TestWorkerMetricsAreRegisteredAndRecorded(t *testing.T) {
	stopEarly := make(chan struct{})
	set, err := StartWorkers(func(name string) bool { return name != config.WorkerExport },
		Worker{Name: config.WorkerExport, Run: func(ctx context.Context) { <-ctx.Done() }},
		Worker{Name: config.WorkerIngest, Run: func(ctx context.Context) { <-ctx.Done() },
			Backlog: func(context.Context) (int, error) { return 42, nil }},
		Worker{Name: config.WorkerScheduler, Run: func(context.Context) { <-stopEarly }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = set.Stop(ctx)
	}()
	set.SampleBacklog(context.Background())
	observability.ObserveWorkerRun(config.WorkerIngest, 0)
	observability.ObserveWorkerRun(config.WorkerIngest, 2)
	close(stopEarly)
	deadline := time.Now().Add(5 * time.Second)
	for testutil.ToFloat64(observability.WorkerUp.WithLabelValues(config.WorkerScheduler)) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	mux := chi.NewRouter()
	registerOperational(mux, "test", false, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	body := rec.Body.String()
	for _, want := range []string{
		`reticora_worker_enabled{worker="export"} 0`,
		`reticora_worker_up{worker="export"} 0`,
		`reticora_worker_enabled{worker="ingest"} 1`,
		`reticora_worker_up{worker="ingest"} 1`,
		`reticora_worker_up{worker="scheduler"} 0`,
		`reticora_worker_backlog{worker="ingest"} 42`,
		`reticora_worker_runs_total{worker="ingest"}`,
		`reticora_worker_errors_total{worker="ingest"}`,
		`reticora_worker_last_success_timestamp_seconds{worker="ingest"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
}

// TestWorkerAlertRulesCoverTheWorkerMetrics: deploy/monitoring alerts on
// stopped workers, errors, missing successful runs and backlog, each on its
// worker metrics with its severity and hold time. ReticoraWorkerNoSuccess
// starts from the running workers and removes those with a recent success,
// so a worker without a last-success series fires too (WP-446, F-295). Every
// alert has a promtool case where it fires and one where it does not in
// deploy/monitoring/tests/worker-rules.test.yaml (run by
// tests/monitoring-config.test.sh).
func TestWorkerAlertRulesCoverTheWorkerMetrics(t *testing.T) {
	var rule struct {
		Kind string
		Spec struct {
			Groups []struct {
				Rules []struct {
					Alert  string
					Expr   string
					For    string
					Labels map[string]string
				}
			}
		}
	}
	readYAML(t, "../../../deploy/monitoring/worker-rules.yaml", &rule)
	if rule.Kind != "PrometheusRule" {
		t.Fatalf("worker-rules.yaml: kind %q", rule.Kind)
	}
	type alert struct{ expr, hold, severity string }
	alerts := map[string]alert{}
	for _, g := range rule.Spec.Groups {
		for _, r := range g.Rules {
			if r.Alert == "" {
				continue
			}
			if _, dup := alerts[r.Alert]; dup {
				t.Errorf("worker-rules.yaml defines %s twice", r.Alert)
			}
			alerts[r.Alert] = alert{r.Expr, r.For, r.Labels["severity"]}
		}
	}
	for _, want := range []struct {
		alert, severity, hold string
		metrics               []string
	}{
		{"ReticoraWorkerStopped", "critical", "5m", []string{"reticora_worker_enabled", "reticora_worker_up"}},
		{"ReticoraWorkerErrors", "warning", "15m", []string{"reticora_worker_errors_total"}},
		{"ReticoraWorkerNoSuccess", "critical", "30m", []string{"reticora_worker_up", "reticora_worker_last_success_timestamp_seconds"}},
		{"ReticoraWorkerBacklog", "warning", "15m", []string{"reticora_worker_backlog"}},
		{"ReticoraWorkerBacklogGrowing", "warning", "30m", []string{"reticora_worker_backlog"}},
	} {
		got, ok := alerts[want.alert]
		if !ok {
			t.Errorf("worker-rules.yaml lacks alert %s", want.alert)
			continue
		}
		for _, m := range want.metrics {
			if !strings.Contains(got.expr, m) {
				t.Errorf("%s: expression does not use %s: %s", want.alert, m, got.expr)
			}
		}
		if got.severity != want.severity || got.hold != want.hold {
			t.Errorf("%s: severity %q, for %q; want %q, %q", want.alert, got.severity, got.hold, want.severity, want.hold)
		}
	}
	// F-295: the last-success series may be missing (no successful cycle
	// since the process started); it may only remove workers, never be
	// required for the alert.
	parts := regexp.MustCompile(`\bunless\s+on\s*\(\s*worker\s*\)`).Split(alerts["ReticoraWorkerNoSuccess"].expr, -1)
	if len(parts) != 2 || !strings.Contains(parts[0], "reticora_worker_up") ||
		strings.Contains(parts[0], "reticora_worker_last_success_timestamp_seconds") ||
		!strings.Contains(parts[1], "reticora_worker_last_success_timestamp_seconds") {
		t.Errorf("ReticoraWorkerNoSuccess must be 'running workers unless on (worker) a recent success', "+
			"so that a worker without a last-success series fires: %s", alerts["ReticoraWorkerNoSuccess"].expr)
	}

	var unit struct {
		RuleFiles []string `yaml:"rule_files"`
		Tests     []struct {
			AlertRuleTest []struct {
				Alertname string           `yaml:"alertname"`
				ExpAlerts []map[string]any `yaml:"exp_alerts"`
			} `yaml:"alert_rule_test"`
		}
	}
	readYAML(t, "../../../deploy/monitoring/tests/worker-rules.test.yaml", &unit)
	if !slices.Equal(unit.RuleFiles, []string{"worker-rules.groups.yaml"}) {
		t.Errorf("worker-rules.test.yaml: rule_files %v, want the extracted groups of worker-rules.yaml", unit.RuleFiles)
	}
	fires, silent := map[string]bool{}, map[string]bool{}
	for _, tc := range unit.Tests {
		for _, a := range tc.AlertRuleTest {
			if len(a.ExpAlerts) > 0 {
				fires[a.Alertname] = true
			} else {
				silent[a.Alertname] = true
			}
		}
	}
	for name := range alerts {
		if !fires[name] || !silent[name] {
			t.Errorf("worker-rules.test.yaml: %s needs a case where it fires (%v) and one where it does not (%v)", name, fires[name], silent[name])
		}
	}
}

func readYAML(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = yaml.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// TestAuditT3HealthRefreshRunsInG1 covers WP-480 (DB-05, MON-02, F-087):
// under the G1 default module set (RETICORA_MODULES unset, E-03) the worker
// that refreshes ci.health may start. The refresh used to run only inside
// the alert evaluator, worker config.WorkerMonitoring, which the module
// alerting (P3) gates, so ci.health stayed 'unknown'. Now the worker
// config.WorkerHealth refreshes it; it belongs to no late module, the
// monitoring worker stays gated by alerting.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestAuditT3HealthRefreshRunsInG1' ./internal/server/
func TestAuditT3HealthRefreshRunsInG1(t *testing.T) {
	// Control: with the alerting module the monitoring worker starts.
	alerting, err := ParseModules("alerting")
	if err != nil {
		t.Fatal(err)
	}
	if !alerting.WorkerAllowed(config.WorkerMonitoring) {
		t.Fatalf("control: monitoring worker not allowed with RETICORA_MODULES=alerting")
	}

	g1, err := ParseModules("")
	if err != nil {
		t.Fatal(err)
	}
	var allowed []string
	for _, name := range config.WorkerNames {
		hostsHealth := name == config.WorkerMonitoring || strings.Contains(name, "health")
		if hostsHealth && g1.WorkerAllowed(name) {
			allowed = append(allowed, name)
		}
	}
	if !slices.Equal(allowed, []string{config.WorkerHealth}) {
		t.Errorf("G1 default module set (RETICORA_MODULES unset): workers that may refresh ci.health %v, want [%s] (%q stays gated by module 'alerting'; DB-05)",
			allowed, config.WorkerHealth, config.WorkerMonitoring)
	}
	// The health worker belongs to no late module, so no module set stops it.
	for _, m := range LateModules {
		if slices.Contains(m.Workers, config.WorkerHealth) {
			t.Errorf("module %s lists the worker %s; ci.health belongs to G1", m.Key, config.WorkerHealth)
		}
	}
	if !alerting.WorkerAllowed(config.WorkerHealth) {
		t.Errorf("RETICORA_MODULES=alerting stops the worker %s", config.WorkerHealth)
	}
}
