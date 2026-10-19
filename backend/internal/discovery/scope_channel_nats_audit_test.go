// Phase-1 acceptance audit, group T4-collector-load – REGRESSION test (must
// pass).
//
// WP-153 (DIS-09), WP-154 (DIS-10), WP-155 (DIS-08). High test gap
// "ServeCollectorScopes (scopes.request/result, subnets.report) has no
// permanent automated test" – the verifier NATS probe
// (verify-B19 zz_verify_b19_nats_test.go) made permanent with assertions.
//
// Over a real NATS connection (TEST_NATS_URL) and PostgreSQL:
//   - scopes.request answers the collector's enabled scopes (a disabled scope
//     is left out) with the organization's intervals, poll intervals below
//     the 5 min minimum raised to it (DIS-10);
//   - scopes.result stores last/next run and the health with the normalised
//     cause auth_failed (DIS-08); a collector reporting another collector's
//     scope is refused and changes nothing;
//   - subnets.report opens scope suggestions with loopback and link-local
//     networks filtered (DIS-09);
//   - an invalid subject is refused; a disabled collector gets no scopes.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  TEST_NATS_URL=nats://localhost:54222 go test -count=1 -v -p 1 -run 'TestAuditScopeChannelOverNATS' ./internal/discovery/
package discovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

func TestAuditScopeChannelOverNATS(t *testing.T) {
	f := scopetest.Seed(t, "db")
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Fatal("TEST_NATS_URL is required next to TEST_DATABASE_URL: the scope channel runs over NATS")
	}
	bg := context.Background()
	org := f.OrgCtx(f.OrgA)
	repo := discovery.NewPGRepository(f.App)
	h := discovery.NewHandler(repo, nil)
	c1 := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "audit-chan-c1", Status: discovery.CollectorActive, Config: map[string]any{}}
	c2 := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "audit-chan-c2", Status: discovery.CollectorActive, Config: map[string]any{}}
	for _, c := range []*discovery.Collector{c1, c2} {
		if err := repo.RegisterCollector(org, c); err != nil {
			t.Fatal(err)
		}
	}
	enabled := discovery.NewScope(f.OrgA, &discovery.ScopeInput{CollectorID: c1.ID, Kind: "cidr", Target: "10.98.0.0/24", Plugins: []string{"sweep", "redfish"}})
	off := false
	disabled := discovery.NewScope(f.OrgA, &discovery.ScopeInput{CollectorID: c1.ID, Kind: "cidr", Target: "10.98.1.0/24", Enabled: &off})
	for _, s := range []*discovery.Scope{enabled, disabled} {
		if err := repo.CreateScope(org, s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Admin.Exec(bg, `UPDATE organization SET settings = jsonb_set(coalesce(settings, '{}'::jsonb), '{discovery}',
		'{"poll_interval":"2m","poll_intervals":{"redfish":"1h"}}') WHERE id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}

	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	defer nc.Close()
	ctx, cancel := context.WithCancel(bg)
	served := make(chan error, 1)
	go func() { served <- h.ServeCollectorScopes(ctx, nc) }()
	defer func() {
		cancel()
		if serr := <-served; serr != nil {
			t.Errorf("ServeCollectorScopes: %v", serr)
		}
	}()

	request := func(subject string, body, out any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		deadline := time.Now().Add(5 * time.Second)
		for {
			msg, rerr := nc.Request(subject, raw, 3*time.Second)
			if errors.Is(rerr, nats.ErrNoResponders) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond) // subscriptions not active yet
				continue
			}
			if rerr != nil {
				t.Fatalf("request %s: %v", subject, rerr)
			}
			if uerr := json.Unmarshal(msg.Data, out); uerr != nil {
				t.Fatalf("reply of %s: %v: %s", subject, uerr, msg.Data)
			}
			return
		}
	}
	base := "collector." + f.OrgA + "." + c1.ID

	t.Run("scopes.request: enabled scopes and clamped intervals", func(t *testing.T) {
		var resp discovery.ScopesResponse
		request(base+discovery.ScopesRequestSuffix, map[string]any{}, &resp)
		if resp.Error != "" || len(resp.Scopes) != 1 {
			t.Fatalf("scopes = %+v, error %q; want only the enabled scope", resp.Scopes, resp.Error)
		}
		s := resp.Scopes[0]
		if s.ID != enabled.ID || s.Target != "10.98.0.0/24" || s.Kind != "cidr" || s.ClientID != f.Client1 ||
			!slices.Equal(s.Plugins, []string{"sweep", "redfish"}) || s.Schedule == "" || s.CredentialIDs == nil {
			t.Errorf("scope = %+v", s)
		}
		iv := resp.Intervals
		if iv == nil || iv.Poll != "5m0s" || iv.Sweep != "24h0m0s" || iv.Protocols["redfish"] != "1h0m0s" {
			t.Errorf("intervals = %+v, want poll 5m0s (2m raised to the minimum), sweep 24h0m0s, redfish 1h0m0s", iv)
		}
	})

	report := map[string]any{"scope_id": enabled.ID, "at": time.Now().UTC().Truncate(time.Second),
		"next_run": time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second),
		"result": map[string]any{"records": 3, "error_count": 1, "errors": []map[string]any{
			{"protocol": "redfish", "target": "10.98.0.9", "message": "redfish: GET /redfish/v1/Systems: 401 Unauthorized"}}}}

	t.Run("scopes.result of a foreign collector is refused", func(t *testing.T) {
		var resp map[string]string
		request("collector."+f.OrgA+"."+c2.ID+discovery.ScopesResultSuffix, report, &resp)
		if resp["error"] != "not recorded" {
			t.Errorf("foreign collector report = %v, want error not recorded", resp)
		}
		got, gerr := repo.GetScope(org, f.OrgA, enabled.ID)
		if gerr != nil || got.LastRunAt != "" {
			t.Errorf("the foreign report changed the scope: last_run_at=%q err=%v", got.LastRunAt, gerr)
		}
	})

	t.Run("scopes.result stores run state and health", func(t *testing.T) {
		var resp map[string]string
		request(base+discovery.ScopesResultSuffix, report, &resp)
		if resp["status"] != "recorded" {
			t.Fatalf("report = %v", resp)
		}
		got, gerr := repo.GetScope(org, f.OrgA, enabled.ID)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if got.LastRunAt == "" || got.NextRunAt == "" {
			t.Errorf("run state not stored: last_run_at=%q next_run_at=%q", got.LastRunAt, got.NextRunAt)
		}
		health := discovery.ScopeHealthOf(got)
		if health.State != discovery.ScopeStatePartial || len(health.Errors) != 1 ||
			health.Errors[0].Cause != "auth_failed" || health.Errors[0].Protocol != "redfish" || health.Errors[0].Target != "10.98.0.9" {
			t.Errorf("health = %+v, want partial with one auth_failed redfish error for 10.98.0.9", health)
		}
	})

	t.Run("subnets.report suggests routable subnets only", func(t *testing.T) {
		var resp map[string]int
		request(base+discovery.SubnetsReportSuffix, discovery.SubnetsReport{Subnets: []string{"10.98.7.0/24", "127.0.0.0/8", "169.254.0.0/16"}}, &resp)
		if resp["suggested"] != 1 {
			t.Errorf("subnets.report = %v, want suggested 1", resp)
		}
		var cidrs []string
		rows, qerr := f.Admin.Query(bg, `SELECT payload->>'cidr' FROM review_item WHERE organization_id = $1 AND kind = 'scope_suggested'
			AND payload->>'collector_id' = $2 ORDER BY 1`, f.OrgA, c1.ID)
		if qerr != nil {
			t.Fatal(qerr)
		}
		for rows.Next() {
			var c string
			if serr := rows.Scan(&c); serr != nil {
				t.Fatal(serr)
			}
			cidrs = append(cidrs, c)
		}
		rows.Close()
		if !slices.Equal(cidrs, []string{"10.98.7.0/24"}) {
			t.Errorf("scope_suggested items = %v, want [10.98.7.0/24]", cidrs)
		}
	})

	t.Run("invalid subject is refused", func(t *testing.T) {
		var resp map[string]any
		request("collector.x.y"+discovery.ScopesRequestSuffix, map[string]any{}, &resp)
		if resp["error"] != "invalid subject" {
			t.Errorf("invalid subject reply = %v", resp)
		}
	})

	t.Run("a disabled collector gets no scopes", func(t *testing.T) {
		if _, uerr := f.Admin.Exec(bg, `UPDATE collector SET status = 'disabled' WHERE id = $1`, c1.ID); uerr != nil {
			t.Fatal(uerr)
		}
		var resp discovery.ScopesResponse
		request(base+discovery.ScopesRequestSuffix, map[string]any{}, &resp)
		if len(resp.Scopes) != 0 || resp.Error != "not available" {
			t.Errorf("disabled collector reply = %+v", resp)
		}
	})
}
