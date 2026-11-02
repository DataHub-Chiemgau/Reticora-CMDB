// The collectors' metric samples on the edge against PostgreSQL and NATS
// (WP-426, MON-02; F-046, F-025 data part).
//
//   - TestEdgeMetricsAgainstPostgres: a sample sent on
//     collector.<org>.<id>.metrics with target = the management IP of a CI
//     is stored in metric_sample with that CI's ci_id, for the organization
//     of the subject only: the same IP at a CI of organization B is not
//     assigned for organization A, a sample without a matching CI counts as
//     unassigned and is not stored. The samples are assigned in the
//     collector's scope: a collector of client 1 leaves the IP of a
//     client-2 CI unassigned (no row), assigns the same IP at a client-1 CI
//     and cannot name the client-2 CI by ci_id; a collector of a site leaves
//     a CI of another site of its client unassigned; an org-wide collector
//     assigns the client-2 CI. An organization without the monitoring
//     entitlement gets 403 with the problem type of the entitlement
//     middleware. F-025: .metrics of a disabled (403) or unknown (404)
//     collector is refused and stores nothing; the active collector gets 202.
//
// Retest (F-046, F-025 data part):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  TEST_NATS_URL=nats://localhost:54222 go test -count=1 -v -p 1 \
//	  -run '^TestEdgeMetricsAgainstPostgres$' ./internal/discovery/
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
)

func TestEdgeMetricsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "7e")
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Fatal("TEST_NATS_URL is required next to TEST_DATABASE_URL: the metric samples run over the NATS edge")
	}
	bg := context.Background()
	t.Cleanup(func() {
		// metric_sample has no foreign key to the organization.
		if _, err := f.Admin.Exec(bg, `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`,
			[]string{f.OrgA, f.OrgB}); err != nil {
			t.Errorf("cleanup metric samples: %v", err)
		}
	})
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}

	// The production chain: the monitoring ingest of POST /monitoring/metrics
	// with the PostgreSQL store and the CI resolver of the router, the
	// entitlement service. Organization A has the monitoring feature,
	// organization B has not.
	repo := NewPGRepository(f.App)
	ciRepo := ci.NewPGRepository(f.App)
	features := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
	if _, err := features.Grant(f.OrgCtx(f.OrgA), entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureMonitoring,
		Plan: entitlement.PlanPro, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	metrics := monitoring.NewHandler(monitoring.NewPGMetricStore(f.App)).WithCIResolver(monitoring.CIAddressResolver{CIs: ciRepo})
	h := NewHandler(repo, ciRepo).WithEdgeMetrics(metrics, features)

	// Two sites of client 1.
	site1, site2 := f.ID(), f.ID()
	for _, s := range []string{site1, site2} {
		exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`, s, f.OrgA, f.Client1, "edge-metrics "+s)
	}
	ciWithIP := func(orgID, clientID, site, name, ip string) string {
		t.Helper()
		id := f.CI(t, orgID, clientID, name)
		exec(`UPDATE ci SET management_ip = $2::inet WHERE id = $1`, id, ip)
		if site != "" {
			exec(`UPDATE ci SET location_id = $2 WHERE id = $1`, id, site)
		}
		return id
	}
	const (
		ipShared  = "192.0.2.10" // a CI of organization A (client 1) and one of organization B
		ipOnlyB   = "192.0.2.11" // a CI of organization B only
		ipClient2 = "192.0.2.20" // a CI of client 2, later also one of client 1
		ipSite2   = "192.0.2.30" // a CI of client 1 at site 2
		ipNone    = "198.51.100.77"
	)
	ciA := ciWithIP(f.OrgA, f.Client1, "", "edge-metrics-a", ipShared)
	ciB := ciWithIP(f.OrgB, "", "", "edge-metrics-b", ipShared)
	ciOnlyB := ciWithIP(f.OrgB, "", "", "edge-metrics-only-b", ipOnlyB)
	ciClient2 := ciWithIP(f.OrgA, f.Client2, "", "edge-metrics-client-2", ipClient2)
	ciSite2 := ciWithIP(f.OrgA, f.Client1, site2, "edge-metrics-site-2", ipSite2)

	register := func(orgID, name, clientID, siteID string) string {
		t.Helper()
		c := &Collector{OrganizationID: orgID, ClientID: clientID, SiteID: siteID, Name: "edge-metrics-" + name,
			Status: CollectorActive, Config: map[string]any{}}
		if err := repo.RegisterCollector(f.OrgCtx(orgID), c); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	orgWide := register(f.OrgA, "org-wide", "", "")
	client1 := register(f.OrgA, "client-1", f.Client1, "")
	atSite1 := register(f.OrgA, "site-1", f.Client1, site1)
	orgB := register(f.OrgB, "org-b", "", "")
	disabledID := register(f.OrgA, "disabled", "", "")
	disabled := CollectorDisabled
	if _, err := repo.UpdateCollector(f.OrgCtx(f.OrgA), f.OrgA, disabledID, &CollectorPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	unknownID := f.ID()

	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	defer nc.Close()
	ctx, cancel := context.WithCancel(bg)
	served := make(chan error, 1)
	go func() { served <- h.ServeCollectorEdge(ctx, nc) }()
	defer func() {
		cancel()
		if serr := <-served; serr != nil {
			t.Errorf("serve: %v", serr)
		}
	}()

	// send delivers samples like the collector (uploadMetricSamples) on the
	// collector's subject and returns the reply as the collector reads it.
	send := func(t *testing.T, orgID, collectorID string, samples ...map[string]any) metricsReply {
		t.Helper()
		raw, _ := json.Marshal(samples)
		subject := "collector." + orgID + "." + collectorID + MetricsSuffix
		deadline := time.Now().Add(5 * time.Second)
		for {
			msg, rerr := nc.Request(subject, raw, 10*time.Second)
			if errors.Is(rerr, nats.ErrNoResponders) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond) // subscription not active yet
				continue
			}
			if rerr != nil {
				t.Fatalf("request %s: %v", subject, rerr)
			}
			var reply metricsReply
			if jerr := json.Unmarshal(msg.Data, &reply); jerr != nil {
				t.Fatalf("reply %s: %v", msg.Data, jerr)
			}
			return reply
		}
	}
	sweep := func(collectorID, target string) map[string]any {
		return map[string]any{"name": "sweep_reachable", "value": 1, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"labels": map[string]string{"target": target, "kind": "sweep", "collector_id": collectorID}}
	}
	rows := func(ciID string) int {
		t.Helper()
		return count(`SELECT count(*) FROM metric_sample WHERE ci_id = $1 AND metric_name = 'sweep_reachable'`, ciID)
	}
	total := func() int {
		t.Helper()
		return count(`SELECT count(*) FROM metric_sample WHERE organization_id = ANY($1::uuid[])`, []string{f.OrgA, f.OrgB})
	}
	accepted := func(t *testing.T, reply metricsReply, ingested, unassigned int) {
		t.Helper()
		if !reply.accepted(ingested, unassigned) {
			raw, _ := json.Marshal(reply)
			t.Errorf("reply %s, want 202 with ingested %d and unassigned %d", raw, ingested, unassigned)
		}
	}

	t.Run("organization boundary", func(t *testing.T) {
		accepted(t, send(t, f.OrgA, orgWide, sweep(orgWide, ipShared)), 1, 0)
		if rows(ciA) != 1 || rows(ciB) != 0 {
			t.Errorf("rows of the org A CI %d, of the org B CI with the same IP %d; want 1 and 0", rows(ciA), rows(ciB))
		}
		if n := count(`SELECT count(*) FROM metric_sample WHERE ci_id = $1 AND organization_id = $2`, ciA, f.OrgA); n != 1 {
			t.Errorf("the sample is not stored for organization A: %d", n)
		}
		// An IP only a CI of organization B has is not assigned for A.
		accepted(t, send(t, f.OrgA, orgWide, sweep(orgWide, ipOnlyB)), 0, 1)
		if rows(ciOnlyB) != 0 {
			t.Errorf("the sample of organization A's collector was stored for organization B's CI")
		}
	})

	t.Run("no matching CI", func(t *testing.T) {
		before := total()
		accepted(t, send(t, f.OrgA, orgWide, sweep(orgWide, ipNone)), 0, 1)
		if n := total(); n != before {
			t.Errorf("an unassigned sample was stored: %d rows more", n-before)
		}
	})

	t.Run("collector scope client 1, client 2, org-wide", func(t *testing.T) {
		// Negative: the client-1 collector does not reach the client-2 CI.
		accepted(t, send(t, f.OrgA, client1, sweep(client1, ipClient2)), 0, 1)
		if rows(ciClient2) != 0 {
			t.Fatalf("TEN-05: the client-1 collector stored a sample of the client-2 CI")
		}
		// Not by its id either.
		named := sweep(client1, ipClient2)
		named["ci_id"] = ciClient2
		if reply := send(t, f.OrgA, client1, named); reply.Status < 400 || rows(ciClient2) != 0 {
			raw, _ := json.Marshal(reply)
			t.Errorf("TEN-05: a sample naming the client-2 CI by ci_id from the client-1 collector: %s, rows %d; want a refusal and no row", raw, rows(ciClient2))
		}
		// The org-wide collector assigns the client-2 CI.
		accepted(t, send(t, f.OrgA, orgWide, sweep(orgWide, ipClient2)), 1, 0)
		if rows(ciClient2) != 1 {
			t.Errorf("rows of the client-2 CI after the org-wide collector: %d, want 1", rows(ciClient2))
		}
		// Positive: the same IP at a client-1 CI is assigned to that one.
		ciClient1 := ciWithIP(f.OrgA, f.Client1, "", "edge-metrics-client-1", ipClient2)
		accepted(t, send(t, f.OrgA, client1, sweep(client1, ipClient2)), 1, 0)
		if rows(ciClient1) != 1 || rows(ciClient2) != 1 {
			t.Errorf("rows of the client-1 CI %d, of the client-2 CI %d; want 1 and 1", rows(ciClient1), rows(ciClient2))
		}
	})

	t.Run("collector scope site", func(t *testing.T) {
		accepted(t, send(t, f.OrgA, atSite1, sweep(atSite1, ipSite2)), 0, 1)
		if rows(ciSite2) != 0 {
			t.Fatalf("the site-1 collector stored a sample of a CI at site 2")
		}
		accepted(t, send(t, f.OrgA, client1, sweep(client1, ipSite2)), 1, 0)
		if rows(ciSite2) != 1 {
			t.Errorf("rows of the site-2 CI after the client-1 collector: %d, want 1", rows(ciSite2))
		}
	})

	t.Run("organization without the monitoring entitlement", func(t *testing.T) {
		before := total()
		reply := send(t, f.OrgB, orgB, sweep(orgB, ipOnlyB))
		if reply.Status != http.StatusForbidden || reply.Type != api.ProblemFeatureNotEntitled {
			t.Errorf("reply %+v, want 403 with %s", reply, api.ProblemFeatureNotEntitled)
		}
		if n := total(); n != before || rows(ciOnlyB) != 0 {
			t.Errorf("refused samples were stored: %d rows more", n-before)
		}
	})

	t.Run("F-025 disabled and unknown collector", func(t *testing.T) {
		for _, tc := range []struct {
			name, id string
			status   int
		}{
			{"disabled", disabledID, http.StatusForbidden},
			{"unknown", unknownID, http.StatusNotFound},
			{"active", orgWide, http.StatusAccepted},
		} {
			before := rows(ciA)
			reply := send(t, f.OrgA, tc.id, sweep(tc.id, ipShared))
			stored := rows(ciA) - before
			want := 0
			if tc.status == http.StatusAccepted {
				want = 1
			}
			if reply.Status != tc.status || stored != want {
				t.Errorf("COL-04: .metrics of the %s collector = %d with %d rows stored; want %d with %s",
					tc.name, reply.Status, stored, tc.status, fmt.Sprint(want))
			}
		}
	})
}
