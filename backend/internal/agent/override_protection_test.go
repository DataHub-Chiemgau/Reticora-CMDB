package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestAgentUpdateUsesAnchorAndCentralDecision covers WP-059 (AGT-05,
// OVR-02): agent telemetry updates the CI linked to the agent, not the first
// CI with its hostname; attributes go through the central decision with
// rank 85, so a manual override and a value of a higher-ranked source stay
// while empty values are filled; an unlinked agent whose hostname matches
// several CIs is not linked to any.
func TestAgentUpdateUsesAnchorAndCentralDecision(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
	repo := NewMemoryRepository()
	cis := ci.NewMemoryRepository()
	overrides := override.NewMemoryRepository()
	newCI := func(hostname string, attrs map[string]any, source string) *ci.Item {
		t.Helper()
		item := &ci.Item{OrganizationID: "org-1", CITypeID: "client", Name: hostname, Hostname: hostname, Attributes: attrs, DiscoverySource: source}
		if err := cis.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	decoy := newCI("laptop-1", map[string]any{}, ci.SourceSweep)
	anchor := newCI("laptop-1", map[string]any{"os": "Windows 11 (asset register)", "arch": "x64"}, ci.SourceRedfish)
	for _, a := range []*Agent{
		{OrganizationID: "org-1", AgentID: "a1", Hostname: "laptop-1", Policy: DefaultPolicy()},
		{OrganizationID: "org-1", AgentID: "a2", Hostname: "kiosk", Policy: DefaultPolicy()},
	} {
		if err := repo.Register(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetCI(ctx, "org-1", "a1", anchor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := overrides.SetOverride(ctx, "org-1", anchor.ID, "os", "Windows 11 (asset register)", "", "asset register", true); err != nil {
		t.Fatal(err)
	}
	newCI("kiosk", map[string]any{}, ci.SourceSweep)
	newCI("kiosk", map[string]any{}, ci.SourceSweep)

	mux := chi.NewRouter()
	NewHandler(repo, nil, cis).WithFieldGuard(override.NewGuard(overrides)).RegisterRoutes(mux)
	post := func(payload TelemetryPayload) string {
		t.Helper()
		body, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/telemetry", bytes.NewReader(body)).WithContext(ctx))
		if w.Code != http.StatusAccepted {
			t.Fatalf("telemetry: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			CIID string `json:"ci_id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.CIID
	}

	if got := post(TelemetryPayload{AgentID: "a1", Hostname: "laptop-1", OS: "windows", Arch: "arm64", Version: "1.4"}); got != anchor.ID {
		t.Fatalf("telemetry reconciled CI %s, want the linked CI %s", got, anchor.ID)
	}
	updated, err := cis.GetByID(ctx, "org-1", anchor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Attributes["os"] != "Windows 11 (asset register)" {
		t.Errorf("manual override overwritten by the agent: %v", updated.Attributes["os"])
	}
	if updated.Attributes["arch"] != "x64" {
		t.Errorf("value of a higher-ranked source (redfish 90) overwritten by the agent (85): %v", updated.Attributes["arch"])
	}
	if updated.Attributes["agent_version"] != "1.4" || updated.Attributes["agent_id"] != "a1" {
		t.Errorf("empty fields not filled: %v", updated.Attributes)
	}
	if fv, err := overrides.Get(ctx, "org-1", anchor.ID, "agent_version"); err != nil || fv.DiscoveredSource != ci.SourceAgent {
		t.Errorf("provenance of the agent write: %+v, %v", fv, err)
	}
	if got, _ := cis.GetByID(ctx, "org-1", decoy.ID); len(got.Attributes) != 0 {
		t.Errorf("first CI with the hostname was updated: %v", got.Attributes)
	}

	// Ambiguous hostname without a link: no CI is guessed.
	if got := post(TelemetryPayload{AgentID: "a2", Hostname: "kiosk", OS: "linux"}); got != "" {
		t.Errorf("agent with an ambiguous hostname linked to %s", got)
	}
}
