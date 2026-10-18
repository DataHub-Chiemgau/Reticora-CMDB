//go:build auditdefect

package server_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT test (it fails until
// the product is fixed).
//
// WP-080 (DB-05: ci.health "persistiert und berechnet"; MON-02 metrics are
// G1) P2 / critical test gap "ci.health is never computed in the G1 default
// configuration (always 'unknown')": cmd/server/main.go hands the
// HealthRefresher only to the alert evaluator, which runs as worker
// config.WorkerMonitoring; LateModules gates that worker behind module
// 'alerting' (P3), and ParseModules("") (RETICORA_MODULES unset, the G1
// default after E-03) switches it off. Verified on a running server binary:
// "worker disabled ... monitoring", health stays 'unknown'.
// Requirement: under the G1 default module set a worker that refreshes
// ci.health is allowed to start. The test accepts either the monitoring
// worker (where main.go wires the refresher today) or a dedicated worker
// whose registered name contains "health".
//
// Retest:
//   cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT3HealthRefreshRunsInG1' ./internal/server/

import (
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

func TestAuditT3HealthRefreshRunsInG1(t *testing.T) {
	// Control: with the alerting module the monitoring worker (and with it the
	// health refresh) starts.
	alerting, err := server.ParseModules("alerting")
	if err != nil {
		t.Fatal(err)
	}
	if !alerting.WorkerAllowed(config.WorkerMonitoring) {
		t.Fatalf("control: monitoring worker not allowed with RETICORA_MODULES=alerting")
	}

	g1, err := server.ParseModules("")
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
	if len(allowed) == 0 {
		t.Errorf("G1 default module set (RETICORA_MODULES unset): no worker that refreshes ci.health may start (%q is gated by module 'alerting'); ci.health stays 'unknown' (DB-05)",
			config.WorkerMonitoring)
	}
}
