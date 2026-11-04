package main

// WP-671 (ENT-05, F-400): the worker discovery_scopes as main builds it
// (buildWorkers) checks the organization's discovery license on the
// collectors' scope channel: an organization without the discovery
// entitlement gets no scopes; with it the scopes are delivered.
//
// Retest:
//
//	cd backend && TEST_NATS_URL=nats://localhost:54222 go test -count=1 -v -run 'TestDiscoveryScopeWorkerChecksTheDiscoveryLicense' ./cmd/server/

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

func TestDiscoveryScopeWorkerChecksTheDiscoveryLicense(t *testing.T) {
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set: the scope channel runs over NATS")
	}
	const org = "6710a000-0000-4000-8000-000000000001"
	bg := context.Background()
	repos := server.MemoryRepositories()
	collectors, ok := repos.Discovery.(interface {
		RegisterCollector(context.Context, *discovery.Collector) error
		CreateScope(context.Context, *discovery.Scope) error
	})
	if !ok {
		t.Fatal("the memory discovery repository cannot register collectors and scopes")
	}
	col := &discovery.Collector{OrganizationID: org, ClientID: "6710a000-0000-4000-8000-0000000000c1", Name: "wp671-collector",
		Status: discovery.CollectorActive, Config: map[string]any{}}
	if err := collectors.RegisterCollector(bg, col); err != nil {
		t.Fatal(err)
	}
	if err := collectors.CreateScope(bg, discovery.NewScope(org, &discovery.ScopeInput{CollectorID: col.ID, Kind: "cidr", Target: "10.96.71.0/24"})); err != nil {
		t.Fatal(err)
	}
	// Enforced as in production; the organization has no discovery row.
	ents := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: true})

	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	t.Cleanup(nc.Close)
	built, err := buildWorkers(&workerDeps{cfg: config.Load(), repos: &repos, coreNATS: nc, entitlements: ents, cycle: testCycle})
	if err != nil {
		t.Fatal(err)
	}
	var worker *server.Worker
	for i := range built {
		if built[i].Name == config.WorkerDiscoveryScopes {
			worker = &built[i]
		}
	}
	if worker == nil {
		t.Fatal("buildWorkers: no worker discovery_scopes")
	}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	scopes := func() discovery.ScopesResponse {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			msg, rerr := nc.Request("collector."+org+"."+col.ID+discovery.ScopesRequestSuffix, []byte(`{}`), 3*time.Second)
			if errors.Is(rerr, nats.ErrNoResponders) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond) // subscriptions not active yet
				continue
			}
			if rerr != nil {
				t.Fatalf("scopes.request: %v", rerr)
			}
			var resp discovery.ScopesResponse
			if uerr := json.Unmarshal(msg.Data, &resp); uerr != nil {
				t.Fatalf("scopes.request reply %s: %v", msg.Data, uerr)
			}
			return resp
		}
	}
	if resp := scopes(); len(resp.Scopes) != 0 || resp.Error != "discovery license not_entitled" {
		t.Errorf("scopes.request without the discovery entitlement: %+v, want no scopes and discovery license not_entitled", resp)
	}
	if _, err = ents.Grant(bg, &entitlement.Entitlement{OrganizationID: org, FeatureKey: entitlement.FeatureDiscovery,
		Plan: entitlement.PlanEssential, Enabled: true, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if resp := scopes(); len(resp.Scopes) != 1 || resp.Error != "" {
		t.Errorf("scopes.request with the discovery entitlement: %+v, want the scope", resp)
	}
}
