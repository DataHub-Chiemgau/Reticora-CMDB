package server

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// TestDiscoveryMetricsExposition covers WP-542 (DIS-08, WP-155): GET
// /metrics, as registerOperational mounts it in the router, exposes the
// discovery metrics of the scope runs the collectors report. Without a run
// there is no series of reticora_discovery_scope_runs_total and
// reticora_discovery_errors_total (negative case); after a clean run only
// the run series state="ok"; after the runs with the SNMPv3 login failure
// the collector reports (collectorcmd.reportScopeRun: protocol snmp, the
// target, cause auth_failed) the run series of the error results failed
// (no record) and partial (the probe hit as record) and
// reticora_discovery_errors_total{cause="auth_failed",protocol="snmp"}.
// The counters are process-wide: no other test of this package reports a
// scope run, so the test needs -count=1 (the default of go test runs).
func TestDiscoveryMetricsExposition(t *testing.T) {
	const runs, errs = "reticora_discovery_scope_runs_total", "reticora_discovery_errors_total"
	mux := chi.NewRouter()
	registerOperational(mux, "test", false, nil)
	// series returns the series of the metric name with their values.
	series := func(name string) map[string]string {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /metrics = %d", rec.Code)
		}
		out := map[string]string{}
		for _, line := range strings.Split(rec.Body.String(), "\n") {
			if labels, ok := strings.CutPrefix(line, name+"{"); ok {
				key, value, _ := strings.Cut(labels, "} ")
				out["{"+key+"}"] = value
			}
		}
		return out
	}
	expect := func(step, name string, want map[string]string) {
		t.Helper()
		if got := series(name); !maps.Equal(got, want) {
			t.Errorf("%s: /metrics series of %s = %v, want %v", step, name, got, want)
		}
	}

	expect("no run reported", runs, map[string]string{})
	expect("no run reported", errs, map[string]string{})

	ctx := context.Background()
	repo := discovery.NewMemoryRepository()
	col := &discovery.Collector{OrganizationID: "org-1", Name: "metrics-col", Status: discovery.CollectorActive, Config: map[string]any{}}
	if err := repo.RegisterCollector(ctx, col); err != nil {
		t.Fatal(err)
	}
	scope := discovery.NewScope("org-1", &discovery.ScopeInput{CollectorID: col.ID, Kind: discovery.ScopeKindHost, Target: "127.0.0.163", Plugins: []string{"snmp"}})
	if err := repo.CreateScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	h := discovery.NewHandler(repo, nil)
	// report hands the handler a scope run as the collector sends it on
	// collector.<org>.<id>.scopes.result (JSON).
	report := func(result string) {
		t.Helper()
		var rep discovery.ScopeResultReport
		raw := `{"scope_id":"` + scope.ID + `","at":"2026-10-27T10:00:00Z","next_run":"2026-10-28T10:00:00Z","result":` + result + `}`
		if err := json.Unmarshal([]byte(raw), &rep); err != nil {
			t.Fatal(err)
		}
		if err := h.RecordCollectorScopeRun(ctx, "org-1", col.ID, &rep); err != nil {
			t.Fatalf("RecordCollectorScopeRun(%s): %v", result, err)
		}
	}

	report(`{"records":3,"duration_ms":12}`)
	expect("clean run", runs, map[string]string{`{state="ok"}`: "1"})
	expect("clean run", errs, map[string]string{})

	const authFailure = `{"protocol":"snmp","target":"127.0.0.163","cause":"auth_failed",` +
		`"message":"snmp v3: auth_failed: 127.0.0.163: authentication failed, the agent reports usmStatsWrongDigests"}`
	report(`{"records":0,"duration_ms":40,"error_count":1,"errors":[` + authFailure + `]}`)
	report(`{"records":1,"duration_ms":41,"error_count":1,"errors":[` + authFailure + `]}`)
	expect("runs with the SNMPv3 login failure", runs, map[string]string{`{state="ok"}`: "1", `{state="failed"}`: "1", `{state="partial"}`: "1"})
	expect("runs with the SNMPv3 login failure", errs, map[string]string{`{cause="auth_failed",protocol="snmp"}`: "2"})

	// The stored run carries the same cause (DIS-08: UI and metrics agree).
	stored, err := repo.GetScope(ctx, "org-1", scope.ID)
	if err != nil {
		t.Fatal(err)
	}
	if health := discovery.ScopeHealthOf(stored); health.State != discovery.ScopeStatePartial || len(health.Errors) != 1 ||
		health.Errors[0].Cause != discovery.CauseAuthFailed || health.Errors[0].Protocol != "snmp" || health.Errors[0].Target != "127.0.0.163" {
		t.Errorf("health of the last run = %+v, want partial with auth_failed, snmp, 127.0.0.163", health)
	}
}
