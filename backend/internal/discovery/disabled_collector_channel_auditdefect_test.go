//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT test (must fail
// until the defect is fixed).
//
// WP-145 (COL-04), WP-153 (DIS-09). Finding P1 "Disabled collector keeps its
// open NATS connection and can keep ingesting", critical gap part
// "ScopesResult and SubnetsReport for the disabled collector are refused":
// CollectorScopes refuses a disabled collector, but RecordCollectorScopeRun
// (scopes.result) and SuggestScopes (subnets.report) do not check the
// collector's status, so a disabled collector still overwrites the health of
// its scopes and opens scope suggestions. COL-04: after disable, further data
// of the collector is refused.
//
// The handler functions behind the NATS subjects are called directly on
// PostgreSQL (the subject mapping itself is covered by
// TestAuditScopeChannelOverNATS); the active collector is the control.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -p 1 -run 'TestAuditDisabledCollectorScopeChannelRefused' ./internal/discovery/
package discovery_test

import (
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

func TestAuditDisabledCollectorScopeChannelRefused(t *testing.T) {
	f := scopetest.Seed(t, "de")
	org := f.OrgCtx(f.OrgA)
	repo := discovery.NewPGRepository(f.App)
	h := discovery.NewHandler(repo, nil)
	disabledStatus := discovery.CollectorDisabled

	for _, tc := range []struct {
		name    string
		disable bool
		host    string // the collector's scope
		cidr    string // the subnet it reports
	}{
		{name: "control: active collector", host: "10.99.0.1", cidr: "10.99.1.0/24"},
		{name: "disabled collector", disable: true, host: "10.99.0.2", cidr: "10.99.2.0/24"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			col := &discovery.Collector{OrganizationID: f.OrgA, Name: "audit-dis-" + tc.cidr, Status: discovery.CollectorActive, Config: map[string]any{}}
			if err := repo.RegisterCollector(org, col); err != nil {
				t.Fatal(err)
			}
			scope := discovery.NewScope(f.OrgA, &discovery.ScopeInput{CollectorID: col.ID, Kind: "host", Target: tc.host})
			if err := repo.CreateScope(org, scope); err != nil {
				t.Fatal(err)
			}
			if tc.disable {
				if _, err := repo.UpdateCollector(org, f.OrgA, col.ID, &discovery.CollectorPatch{Status: &disabledStatus}); err != nil {
					t.Fatal(err)
				}
			}
			refused := 0
			runErr := h.RecordCollectorScopeRun(org, f.OrgA, col.ID, &discovery.ScopeResultReport{ScopeID: scope.ID, At: time.Now().UTC(),
				NextRun: time.Now().Add(24 * time.Hour).UTC(), Result: map[string]any{"records": 7, "error_count": 0}})
			got, gerr := repo.GetScope(org, f.OrgA, scope.ID)
			if gerr != nil {
				t.Fatal(gerr)
			}
			if runErr != nil && got.LastRunAt == "" {
				refused++
			} else if tc.disable {
				t.Errorf("COL-04 violated: scopes.result of the disabled collector was recorded (err=%v, last_run_at=%q)", runErr, got.LastRunAt)
			}
			n, sugErr := h.SuggestScopes(org, f.OrgA, col.ID, []string{tc.cidr})
			if sugErr != nil && n == 0 {
				refused++
			} else if tc.disable {
				t.Errorf("COL-04 violated: subnets.report of the disabled collector opened %d scope suggestion(s) (err=%v)", n, sugErr)
			}
			if !tc.disable && refused != 0 {
				t.Errorf("control: the active collector's report or subnets were refused (%d): run err=%v, suggest err=%v", refused, runErr, sugErr)
			}
		})
	}
}
