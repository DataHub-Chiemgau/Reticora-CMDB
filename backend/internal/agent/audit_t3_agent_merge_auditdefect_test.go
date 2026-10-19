//go:build auditdefect

package agent_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT test (it fails until
// the product is fixed).
//
// WP-059 (AGT-05 "keine Duplikate", REC-11, OVR-02) P2 "Agent telemetry
// re-creates a duplicate CI after the agent's linked CI was merged":
// agent/handler.go anchorCI returns nil,nil for any GetByID error on the
// linked ci_id (the merged victim is filtered by deleted_at), reconcileCI then
// creates a new endpoint CI, and ci/merge.go does not move
// endpoint_agent.ci_id. Requirement: the next telemetry follows
// merged_into_id to the survivor; the number of active CIs with the hostname
// stays unchanged.
//
// PostgreSQL repositories (agent, CI with the real merge, discovery type
// resolver, override guard) like the production wiring.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3AgentTelemetryFollowsMergedCI' ./internal/agent/

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func TestAuditT3AgentTelemetryFollowsMergedCI(t *testing.T) {
	f := scopetest.Seed(t, "fe")
	bg := context.Background()
	const host = "audit-t3-laptop"
	victim := f.CI(t, f.OrgA, "", host)
	survivor := f.CI(t, f.OrgA, "", host)
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET hostname = $3 WHERE id IN ($1, $2)`, victim, survivor, host); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA})
	repo := agent.NewPGRepository(f.App)
	if err := repo.Register(ctx, &agent.Agent{OrganizationID: f.OrgA, AgentID: "audit-t3-agent", Hostname: host, Status: "online", Policy: agent.DefaultPolicy()}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCI(ctx, f.OrgA, "audit-t3-agent", victim); err != nil {
		t.Fatal(err)
	}
	cis := ci.NewPGRepository(f.App)
	if _, err := cis.Merge(ctx, f.OrgA, survivor, victim, ""); err != nil {
		t.Fatalf("merge: %v", err)
	}
	active := func() int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND hostname = $2 AND deleted_at IS NULL`, f.OrgA, host).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := active()

	mux := chi.NewRouter()
	agent.NewHandler(repo, nil, cis, discovery.NewPGRepository(f.App)).
		WithFieldGuard(override.NewGuard(override.NewPGRepository(f.App))).RegisterRoutes(mux)
	body, err := json.Marshal(agent.TelemetryPayload{AgentID: "audit-t3-agent", Hostname: host, OS: "windows", Version: "1.0"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/telemetry", bytes.NewReader(body)).WithContext(ctx))
	if w.Code != http.StatusAccepted {
		t.Fatalf("telemetry: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		CIID string `json:"ci_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CIID != survivor {
		t.Errorf("telemetry after the merge anchored to CI %s, want the merge survivor %s", out.CIID, survivor)
	}
	if after := active(); after != before {
		t.Errorf("active CIs with hostname %s: %d before the telemetry, %d after; want unchanged (duplicate created, merge silently undone)", host, before, after)
	}
}
