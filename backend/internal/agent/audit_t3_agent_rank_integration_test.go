package agent_test

// Phase-1 acceptance audit, group T3-data-ingest: REGRESSION test (passes
// today; closes an evidence gap).
//
// WP-059 (AGT-05, OVR-02, REC-02) high test gap "Agent telemetry against
// PostgreSQL: rank boundary, override protection": the existing coverage
// (override_protection_test.go) runs on the memory repositories only.
// Asserted here on PostgreSQL with the production wiring (agent PG
// repository, ci.PGRepository, discovery type resolver, override guard on the
// PG override repository): telemetry (rank 85) replaces an snmp value
// (rank 80), keeps a redfish value (rank 90) and a protected manual override,
// creates no CI, and does not raise ci.version (no manual write).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT3AgentTelemetryRankAndOverridesPG' ./internal/agent/

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func TestAuditT3AgentTelemetryRankAndOverridesPG(t *testing.T) {
	f := scopetest.Seed(t, "ce")
	bg := context.Background()
	const host = "audit-t3-rank"
	id := f.CI(t, f.OrgA, "", host)
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET hostname = $2, discovery_source = 'snmp',
		attributes = '{"os":"Win10 (manual)","arch":"x86","agent_version":"0.9"}' WHERE id = $1`, id, host); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA})
	ovr := override.NewPGRepository(f.App)
	if _, err := ovr.SetOverride(ctx, f.OrgA, id, "os", "Win10 (manual)", f.User, "asset register", true); err != nil {
		t.Fatalf("override: %v", err)
	}
	observed := time.Now().UTC().Add(-time.Hour)
	if _, err := ovr.RecordDiscovered(ctx, f.OrgA, id, "arch", "x86", "snmp", observed); err != nil {
		t.Fatal(err)
	}
	if _, err := ovr.RecordDiscovered(ctx, f.OrgA, id, "agent_version", "0.9", "redfish", observed); err != nil {
		t.Fatal(err)
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	v0 := scalar(`SELECT version::text FROM ci WHERE id = $1`, id)
	cis0 := scalar(`SELECT count(*)::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, f.OrgA)

	repo := agent.NewPGRepository(f.App)
	if err := repo.Register(ctx, &agent.Agent{OrganizationID: f.OrgA, AgentID: "audit-t3-rank", Hostname: host, Status: "online", Policy: agent.DefaultPolicy()}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCI(ctx, f.OrgA, "audit-t3-rank", id); err != nil {
		t.Fatal(err)
	}
	cis := ci.NewPGRepository(f.App)
	mux := chi.NewRouter()
	agent.NewHandler(repo, nil, cis, discovery.NewPGRepository(f.App)).WithFieldGuard(override.NewGuard(ovr)).RegisterRoutes(mux)
	body, err := json.Marshal(agent.TelemetryPayload{AgentID: "audit-t3-rank", Hostname: host, OS: "linux", Arch: "arm64", Version: "2.0"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/telemetry", bytes.NewReader(body)).WithContext(ctx))
	if w.Code != http.StatusAccepted {
		t.Fatalf("telemetry: %d %s", w.Code, w.Body.String())
	}
	got, err := cis.GetByID(ctx, f.OrgA, id)
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{
		"os":            "Win10 (manual)", // protected manual override
		"arch":          "arm64",          // snmp (80) < agent (85): replaced
		"agent_version": "0.9",            // redfish (90) > agent (85): kept
	} {
		if got.Attributes[field] != want {
			t.Errorf("effective %s after telemetry = %v, want %q", field, got.Attributes[field], want)
		}
	}
	if v := scalar(`SELECT version::text FROM ci WHERE id = $1`, id); v != v0 {
		t.Errorf("ci.version after agent telemetry = %s, want unchanged %s (automated write)", v, v0)
	}
	if n := scalar(`SELECT count(*)::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, f.OrgA); n != cis0 {
		t.Errorf("active CIs after telemetry = %s, want %s (no new CI for a linked agent)", n, cis0)
	}
}
