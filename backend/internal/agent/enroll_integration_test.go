package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestAgentEnrollmentBindsClientAndSite covers WP-038 (AGT-06): agents
// enroll only with a valid single-use token, which binds client and site;
// roaming agents get a site suggestion from their network fingerprint that
// takes effect only on manual confirmation; the policies hide agents of other
// clients.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAgentEnrollmentBindsClientAndSite(t *testing.T) {
	f := scopetest.Seed(t, "51")
	bg := context.Background()
	site1, site2, foreignSite := f.ID(), f.ID(), f.ID()
	for _, s := range []struct{ id, client string }{{site1, f.Client1}, {site2, f.Client1}, {foreignSite, f.Client2}} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
			s.id, f.OrgA, s.client, "agent "+s.id); err != nil {
			t.Fatalf("seed site: %v", err)
		}
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO subnet (organization_id, client_id, site_id, cidr) VALUES ($1, $2, $3, '10.51.2.0/24')`,
		f.OrgA, f.Client1, site2); err != nil {
		t.Fatalf("seed subnet: %v", err)
	}

	repo := agent.NewPGRepository(f.App)
	mux := chi.NewRouter()
	agent.NewHandler(repo, nil, nil).RegisterRoutes(mux)
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	post := func(scope *database.TenantScope, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		req = req.WithContext(database.ContextWithTenantScope(ctx, scope))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	createToken := func(body map[string]any) string {
		t.Helper()
		w := post(&orgWide, "/api/v1/agents/enrollment-tokens", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create token %v: status %d: %s", body, w.Code, w.Body.String())
		}
		var tok agent.EnrollmentToken
		if err := json.Unmarshal(w.Body.Bytes(), &tok); err != nil || tok.Token == "" {
			t.Fatalf("token response without secret: %s", w.Body.String())
		}
		var stored string
		if err := f.Admin.QueryRow(bg, `SELECT token_hash FROM agent_enrollment_token WHERE id = $1`, tok.ID).Scan(&stored); err != nil || stored == tok.Token {
			t.Fatalf("token secret stored in plain text or missing: %v", err)
		}
		return tok.Token
	}
	enroll := func(scope *database.TenantScope, agentID, token, ip string) (int, agent.Agent) {
		t.Helper()
		w := post(scope, "/api/v1/agents/enroll", map[string]any{
			"agent_id": agentID, "hostname": agentID, "enrollment_token": token, "ip_address": ip,
		})
		var a agent.Agent
		_ = json.Unmarshal(w.Body.Bytes(), &a)
		return w.Code, a
	}

	// A token binding client 1 and site 1.
	siteToken := createToken(map[string]any{"client_id": f.Client1, "site_id": site1})
	if code, a := enroll(&orgWide, "fixed-1", siteToken, "10.51.2.7"); code != http.StatusCreated ||
		a.ClientID != f.Client1 || a.SiteID != site1 || a.SiteConfirmedAt == nil || a.SuggestedSiteID != "" {
		t.Fatalf("enroll with site token: status %d agent %+v", code, a)
	}
	// Single use.
	if code, _ := enroll(&orgWide, "fixed-2", siteToken, ""); code != http.StatusForbidden {
		t.Errorf("reused token: status %d, want 403", code)
	}
	// No token, unknown token.
	if code, _ := enroll(&orgWide, "no-token", "", ""); code != http.StatusBadRequest {
		t.Errorf("enroll without token: status %d, want 400", code)
	}
	if code, _ := enroll(&orgWide, "bad-token", "agt_unknown", ""); code != http.StatusForbidden {
		t.Errorf("enroll with unknown token: status %d, want 403", code)
	}
	// Expired token.
	expired := createToken(map[string]any{"client_id": f.Client1})
	if _, err := f.Admin.Exec(bg, `UPDATE agent_enrollment_token SET expires_at = now() - interval '1 minute' WHERE used_at IS NULL AND client_id = $1 AND site_id IS NULL`, f.Client1); err != nil {
		t.Fatalf("expire token: %v", err)
	}
	if code, _ := enroll(&orgWide, "late", expired, ""); code != http.StatusForbidden {
		t.Errorf("expired token: status %d, want 403", code)
	}
	// A site of another client cannot be bound.
	if w := post(&orgWide, "/api/v1/agents/enrollment-tokens", map[string]any{"client_id": f.Client1, "site_id": foreignSite}); w.Code == http.StatusCreated {
		t.Errorf("token with a site of client 2 was created")
	}

	// Roaming token: client 1 without site; the site is suggested from the
	// subnet containing the fingerprint and set only on confirmation.
	roaming := createToken(map[string]any{"client_id": f.Client1})
	code, laptop := enroll(&orgWide, "laptop", roaming, "10.51.2.42")
	if code != http.StatusCreated || laptop.ClientID != f.Client1 || laptop.SiteID != "" || laptop.SuggestedSiteID != site2 {
		t.Fatalf("roaming enroll: status %d agent %+v, want client 1, no site, suggested site 2", code, laptop)
	}
	if w := post(&orgWide, "/api/v1/agents/"+laptop.ID+"/site", map[string]any{"site_id": foreignSite}); w.Code != http.StatusNotFound {
		t.Errorf("confirm a site of client 2: status %d, want 404", w.Code)
	}
	w := post(&orgWide, "/api/v1/agents/"+laptop.ID+"/site", map[string]any{"site_id": site2})
	var confirmed agent.Agent
	if err := json.Unmarshal(w.Body.Bytes(), &confirmed); err != nil || w.Code != http.StatusOK ||
		confirmed.SiteID != site2 || confirmed.SuggestedSiteID != "" || confirmed.SiteConfirmedAt == nil {
		t.Fatalf("confirm site: status %d agent %+v", w.Code, confirmed)
	}

	// The fixed agent reports a fingerprint of site 2: suggested, not applied.
	if err := repo.SuggestSite(f.OrgCtx(f.OrgA), f.OrgA, "fixed-1", "10.51.2.9"); err != nil {
		t.Fatalf("suggest site: %v", err)
	}
	fixed, err := repo.GetByAgentID(f.OrgCtx(f.OrgA), f.OrgA, "fixed-1")
	if err != nil || fixed.SiteID != site1 || fixed.SuggestedSiteID != site2 {
		t.Errorf("fixed agent after moving: %+v, %v; want site 1 with suggestion site 2", fixed, err)
	}

	// Client 2 sees no agent of client 1 and cannot use a client-1 token.
	client2 := database.OrgWideScope(f.OrgA, f.User)
	client2.Clients = database.ScopeIDs(f.Client2)
	c2ctx := database.ContextWithTenantScope(bg, &client2)
	if list, total, err := repo.List(c2ctx, f.OrgA, api.PaginationParams{Limit: 50}); err != nil || total != 0 || len(list) != 0 {
		t.Errorf("client 2 lists %d agents (%v), want none", total, err)
	}
	other := createToken(map[string]any{"client_id": f.Client1, "site_id": site1})
	if code, _ := enroll(&client2, "intruder", other, ""); code != http.StatusForbidden {
		t.Errorf("client 2 enrolls with a client-1 token: status %d, want 403", code)
	}

	// The repository consumes the token in the same transaction as the
	// registration: a failed registration leaves it usable.
	a := &agent.Agent{OrganizationID: f.OrgA, AgentID: "direct", Hostname: "direct"}
	if err := repo.Enroll(f.OrgCtx(f.OrgA), a, "no-such-hash", ""); !errors.Is(err, agent.ErrInvalidEnrollmentToken) {
		t.Errorf("enroll with unknown hash: %v, want ErrInvalidEnrollmentToken", err)
	}
}
