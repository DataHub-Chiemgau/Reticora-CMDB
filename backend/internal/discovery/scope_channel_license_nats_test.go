// WP-671 (ENT-05, ENT-07, CH21), finding F-400: the collectors' NATS scope
// channel checks the organization's discovery license like the edge data
// path and the entitlement middleware (cmd/server/workers.go wires
// server.DiscoveryLicense into it).
//
// Over a real NATS connection (TEST_NATS_URL) and PostgreSQL with the
// entitlement service (enforcement on):
//   - discovery disabled (not entitled): scopes.request, jobs.request,
//     scopes.result and subnets.report are refused; the collector gets no
//     scope and no scope_suggested item is opened, the scope keeps no run;
//   - discovery expired (CH21): scopes.request still answers the scopes
//     (the collector is paused), the results scopes.result and
//     subnets.report are refused like the ingest;
//   - discovery active again: scopes are delivered and a reported subnet
//     opens its scope_suggested item.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… TEST_NATS_URL=… go test -count=1 -v -p 1 -run 'TestScopeChannelChecksTheDiscoveryLicense' ./internal/discovery/
package discovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
)

// entitlementLicense is the discovery license of the entitlement service as
// the server's adapter (server.DiscoveryLicense) reports it.
type entitlementLicense struct{ svc *entitlement.Service }

func (l entitlementLicense) DiscoveryLicense(ctx context.Context, orgID string) (state, problemType string, err error) {
	_, status, err := l.svc.Status(ctx, orgID, entitlement.FeatureDiscovery)
	if err != nil {
		return "", "", err
	}
	switch status {
	case entitlement.StatusActive:
		return discovery.LicenseActive, "", nil
	case entitlement.StatusExpired:
		return discovery.LicenseExpired, (&entitlement.LicenseExpiredError{}).ProblemType(), nil
	default:
		return discovery.LicenseNotEntitled, (&entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureDiscovery}).ProblemType(), nil
	}
}

func TestScopeChannelChecksTheDiscoveryLicense(t *testing.T) {
	f := scopetest.Seed(t, "04")
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Fatal("TEST_NATS_URL is required next to TEST_DATABASE_URL: the scope channel runs over NATS")
	}
	bg := context.Background()
	org := f.OrgCtx(f.OrgA)
	repo := discovery.NewPGRepository(f.App)
	c1 := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "license-chan-c1", Status: discovery.CollectorActive, Config: map[string]any{}}
	if err := repo.RegisterCollector(org, c1); err != nil {
		t.Fatal(err)
	}
	scope := discovery.NewScope(f.OrgA, &discovery.ScopeInput{CollectorID: c1.ID, Kind: "cidr", Target: "10.96.0.0/24", Plugins: []string{"sweep"}})
	if err := repo.CreateScope(org, scope); err != nil {
		t.Fatal(err)
	}
	ents := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
	if _, err := ents.Provision(org, f.OrgA, entitlement.PlanEssential); err != nil {
		t.Fatal(err)
	}
	grant := func(enabled bool, validUntil *time.Time, want entitlement.Status) {
		t.Helper()
		if _, err := ents.Grant(org, &entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureDiscovery,
			Plan: entitlement.PlanEssential, Enabled: enabled, ValidUntil: validUntil, Source: "manual"}); err != nil {
			t.Fatal(err)
		}
		if _, status, err := ents.Status(org, f.OrgA, entitlement.FeatureDiscovery); err != nil || status != want {
			t.Fatalf("setup: discovery status %s (%v), want %s", status, err, want)
		}
	}

	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	defer nc.Close()
	h := discovery.NewHandler(repo, nil).WithEdgeLicense(entitlementLicense{svc: ents})
	ctx, cancel := context.WithCancel(bg)
	served := make(chan error, 1)
	go func() { served <- h.ServeCollectorScopes(ctx, nc) }()
	defer func() {
		cancel()
		if serr := <-served; serr != nil {
			t.Errorf("ServeCollectorScopes: %v", serr)
		}
	}()
	request := func(suffix string, body any) string {
		t.Helper()
		raw, _ := json.Marshal(body)
		deadline := time.Now().Add(5 * time.Second)
		for {
			msg, rerr := nc.Request("collector."+f.OrgA+"."+c1.ID+suffix, raw, 3*time.Second)
			if errors.Is(rerr, nats.ErrNoResponders) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond) // subscriptions not active yet
				continue
			}
			if rerr != nil {
				t.Fatalf("request %s: %v", suffix, rerr)
			}
			return string(msg.Data)
		}
	}
	scopesOf := func(raw string) (int, string) {
		var resp discovery.ScopesResponse
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			t.Fatalf("scopes reply %s: %v", raw, err)
		}
		return len(resp.Scopes), resp.Error
	}
	errorOf := func(raw string) string {
		var resp map[string]any
		_ = json.Unmarshal([]byte(raw), &resp)
		e, _ := resp["error"].(string)
		return e
	}
	suggestions := func() int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'scope_suggested'`, f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	lastRun := func() string {
		got, gerr := repo.GetScope(org, f.OrgA, scope.ID)
		if gerr != nil {
			t.Fatal(gerr)
		}
		return got.LastRunAt
	}
	result := map[string]any{"scope_id": scope.ID, "at": time.Now().UTC().Truncate(time.Second),
		"next_run": time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second), "result": map[string]any{"records": 1}}

	// Not entitled: nothing of discovery over the channel.
	grant(false, nil, entitlement.StatusNotEntitled)
	if n, e := scopesOf(request(discovery.ScopesRequestSuffix, map[string]any{})); n != 0 || e != "discovery license not_entitled" {
		t.Errorf("scopes.request without discovery: %d scopes, error %q; want none and discovery license not_entitled", n, e)
	}
	if e := errorOf(request(discovery.JobsRequestSuffix, map[string]any{})); e != "discovery license not_entitled" {
		t.Errorf("jobs.request without discovery: error %q, want discovery license not_entitled", e)
	}
	if e := errorOf(request(discovery.ScopesResultSuffix, result)); e != "discovery license not_entitled" || lastRun() != "" {
		t.Errorf("scopes.result without discovery: error %q, last run %q; want refused and no run", e, lastRun())
	}
	if e := errorOf(request(discovery.SubnetsReportSuffix, discovery.SubnetsReport{Subnets: []string{"10.96.8.0/24"}})); e != "discovery license not_entitled" || suggestions() != 0 {
		t.Errorf("subnets.report without discovery: error %q, %d scope_suggested items; want refused and none", e, suggestions())
	}

	// Expired (CH21): the results stop, the scopes are still answered.
	past := time.Now().Add(-time.Hour)
	grant(true, &past, entitlement.StatusExpired)
	if n, e := scopesOf(request(discovery.ScopesRequestSuffix, map[string]any{})); n != 1 || e != "" {
		t.Errorf("scopes.request after expiry: %d scopes, error %q; want the scope", n, e)
	}
	if e := errorOf(request(discovery.ScopesResultSuffix, result)); e != "discovery license expired" || lastRun() != "" {
		t.Errorf("scopes.result after expiry: error %q, last run %q; want refused and no run", e, lastRun())
	}
	if e := errorOf(request(discovery.SubnetsReportSuffix, discovery.SubnetsReport{Subnets: []string{"10.96.8.0/24"}})); e != "discovery license expired" || suggestions() != 0 {
		t.Errorf("subnets.report after expiry: error %q, %d scope_suggested items; want refused and none", e, suggestions())
	}

	// Active again: the channel answers.
	grant(true, nil, entitlement.StatusActive)
	if n, e := scopesOf(request(discovery.ScopesRequestSuffix, map[string]any{})); n != 1 || e != "" {
		t.Errorf("scopes.request with discovery: %d scopes, error %q; want the scope", n, e)
	}
	if raw := request(discovery.SubnetsReportSuffix, discovery.SubnetsReport{Subnets: []string{"10.96.8.0/24"}}); errorOf(raw) != "" || suggestions() != 1 {
		t.Errorf("subnets.report with discovery: %s, %d scope_suggested items; want one", raw, suggestions())
	}
	if e := errorOf(request(discovery.ScopesResultSuffix, result)); e != "" || lastRun() == "" {
		t.Errorf("scopes.result with discovery: error %q, last run %q; want recorded", e, lastRun())
	}
}
