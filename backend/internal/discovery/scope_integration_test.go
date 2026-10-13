package discovery_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// TestDiscoveryScopes covers WP-153 (DIS-09, API-05) against PostgreSQL:
// scopes with client/site/VRF/schedule/credentials/plugins and CRUD, the
// site and collector deciding the client, refused references, the client
// scope of RLS, the collector's scope list and run results, and the
// scope_suggested review after enrollment whose acceptance creates the scope.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDiscoveryScopes(t *testing.T) {
	f := scopetest.Seed(t, "a7")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	repo := discovery.NewPGRepository(f.App)
	h := discovery.NewHandler(repo, nil)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	fieldOf := func(err error) (string, int) {
		var fe *api.FieldError
		if errors.As(err, &fe) {
			return fe.Field, fe.Status
		}
		return "", 0
	}

	site, vrf, otherVRF, cred, otherCred := f.ID(), f.ID(), f.ID(), f.ID(), f.ID()
	exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'wp153-site')`, site, f.OrgA, f.Client1)
	exec(`INSERT INTO vrf (id, organization_id, name) VALUES ($1, $2, 'wp153-vrf')`, vrf, f.OrgA)
	exec(`INSERT INTO vrf (id, organization_id, name) VALUES ($1, $2, 'wp153-vrf-b')`, otherVRF, f.OrgB)
	exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'wp153-snmp', 'snmp_v3', '\x00')`, cred, f.OrgA)
	exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'wp153-b', 'snmp_v3', '\x00')`, otherCred, f.OrgB)

	col := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "wp153-col", Status: discovery.CollectorActive, Config: map[string]any{}}
	other := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "wp153-col-c2", Status: discovery.CollectorActive, Config: map[string]any{}}
	for _, c := range []*discovery.Collector{col, other} {
		if err := repo.RegisterCollector(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	// Create: defaults, the site decides the client.
	s := discovery.NewScope(f.OrgA, &discovery.ScopeInput{CollectorID: col.ID, Kind: "cidr", Target: "10.20.0.7/24",
		SiteID: site, VRFID: vrf, CredentialIDs: []string{cred}, Plugins: []string{"sweep", "snmp", "ssh"}})
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateScope(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s.Target != "10.20.0.0/24" || s.ClientID != f.Client1 || s.SiteID != site || s.VRFID != vrf || s.Schedule != "24h" ||
		!s.Enabled || s.NextRunAt == "" || len(s.CredentialIDs) != 1 || len(s.Plugins) != 3 {
		t.Fatalf("created scope = %+v", s)
	}

	// Refused references and duplicates.
	for name, tc := range map[string]struct {
		in     discovery.ScopeInput
		field  string
		status int
	}{
		"foreign VRF":         {discovery.ScopeInput{CollectorID: col.ID, Kind: "host", Target: "10.20.1.1", VRFID: otherVRF}, "vrf_id", http.StatusUnprocessableEntity},
		"foreign credential":  {discovery.ScopeInput{CollectorID: col.ID, Kind: "host", Target: "10.20.1.2", CredentialIDs: []string{otherCred}}, "credential_ids", http.StatusUnprocessableEntity},
		"unknown collector":   {discovery.ScopeInput{CollectorID: f.ID(), Kind: "host", Target: "10.20.1.3"}, "collector_id", http.StatusUnprocessableEntity},
		"other client's site": {discovery.ScopeInput{CollectorID: other.ID, Kind: "host", Target: "10.20.1.4", SiteID: site}, "client_id", http.StatusUnprocessableEntity},
		"duplicate target":    {discovery.ScopeInput{CollectorID: col.ID, Kind: "cidr", Target: "10.20.0.0/24"}, "target", http.StatusConflict},
	} {
		in := tc.in
		sc := discovery.NewScope(f.OrgA, &in)
		if err := sc.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		field, status := fieldOf(repo.CreateScope(ctx, sc))
		if field != tc.field || status != tc.status {
			t.Errorf("%s: field %q status %d, want %q %d", name, field, status, tc.field, tc.status)
		}
	}

	// Update, list, client scope.
	sched, disabled := "0 2 * * *", false
	updated, err := repo.UpdateScope(ctx, f.OrgA, s.ID, &discovery.ScopePatch{Schedule: &sched})
	if err != nil || updated.Schedule != sched {
		t.Fatalf("update: %+v %v", updated, err)
	}
	bad := "1m"
	_, err = repo.UpdateScope(ctx, f.OrgA, s.ID, &discovery.ScopePatch{Schedule: &bad})
	if field, _ := fieldOf(err); field != "schedule" {
		t.Errorf("an interval below 5m: field %q", field)
	}
	if list, total, lerr := repo.ListScopes(f.ClientCtx(f.Client2), f.OrgA, discovery.ScopeFilter{}, api.PaginationParams{Limit: 10}); lerr != nil || total != 0 || len(list) != 0 {
		t.Errorf("client 2 sees %d scopes of client 1: %v", total, lerr)
	}
	if list, total, lerr := repo.ListScopes(f.ClientCtx(f.Client1), f.OrgA, discovery.ScopeFilter{CollectorID: col.ID}, api.PaginationParams{Limit: 10}); lerr != nil || total != 1 || list[0].ID != s.ID {
		t.Errorf("client 1 scopes: %d %v", total, lerr)
	}

	// The collector receives its enabled scopes and reports runs.
	scopes, err := h.CollectorScopes(ctx, f.OrgA, col.ID)
	if err != nil || len(scopes) != 1 || scopes[0].Target != "10.20.0.0/24" || scopes[0].VRFID != vrf || scopes[0].Schedule != sched {
		t.Fatalf("collector scopes: %+v %v", scopes, err)
	}
	next := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	if err = h.RecordCollectorScopeRun(ctx, f.OrgA, col.ID, &discovery.ScopeResultReport{ScopeID: s.ID, NextRun: next,
		Result: map[string]any{"status": "ok", "records": 12}}); err != nil {
		t.Fatal(err)
	}
	if err = h.RecordCollectorScopeRun(ctx, f.OrgA, other.ID, &discovery.ScopeResultReport{ScopeID: s.ID}); err == nil {
		t.Error("another collector reported a run of the scope")
	}
	got, err := repo.GetScope(ctx, f.OrgA, s.ID)
	if err != nil || got.LastRunAt == "" || got.LastResult["records"] != float64(12) || got.NextRunAt != next.Format(time.RFC3339) {
		t.Errorf("after the run: %+v %v", got, err)
	}
	if _, err = repo.UpdateScope(ctx, f.OrgA, s.ID, &discovery.ScopePatch{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if scopes, _ = h.CollectorScopes(ctx, f.OrgA, col.ID); len(scopes) != 0 {
		t.Errorf("a disabled scope is delivered: %+v", scopes)
	}

	// scope_suggested: the reported subnets that no scope covers.
	n, err := h.SuggestScopes(ctx, f.OrgA, col.ID, []string{"10.20.0.0/25", "192.168.50.10/24", "127.0.0.1/8", "fe80::1/64", "10.0.0.0/8"})
	if err != nil || n != 1 {
		t.Fatalf("suggestions: %d %v", n, err)
	}
	items, _, err := repo.ListReviewItems(ctx, f.OrgA, discovery.ReviewFilter{Kind: discovery.ReviewKindScopeSuggested}, api.PaginationParams{Limit: 10})
	if err != nil || len(items) != 1 || items[0].Payload["cidr"] != "192.168.50.0/24" {
		t.Fatalf("scope_suggested items: %+v %v", items, err)
	}
	if n, _ = h.SuggestScopes(ctx, f.OrgA, col.ID, []string{"192.168.50.0/24"}); n != 0 {
		t.Error("an open suggestion was opened twice")
	}
	if _, err = h.ReviewService().Resolve(ctx, f.OrgA, items[0].ID, &discovery.Resolution{Action: "accept"}); err != nil {
		t.Fatal(err)
	}
	list, _, err := repo.ListScopes(ctx, f.OrgA, discovery.ScopeFilter{CollectorID: col.ID}, api.PaginationParams{Limit: 10})
	if err != nil || len(list) != 2 {
		t.Fatalf("scopes after accepting: %+v %v", list, err)
	}
	if n, _ = h.SuggestScopes(ctx, f.OrgA, col.ID, []string{"192.168.50.0/24"}); n != 0 {
		t.Error("a subnet with a scope was suggested again")
	}
	// A dismissed suggestion stays dismissed.
	if n, _ = h.SuggestScopes(ctx, f.OrgA, col.ID, []string{"172.16.5.0/24"}); n != 1 {
		t.Fatal("second suggestion missing")
	}
	items, _, _ = repo.ListReviewItems(ctx, f.OrgA, discovery.ReviewFilter{Kind: discovery.ReviewKindScopeSuggested, Status: discovery.ReviewStatusOpen}, api.PaginationParams{Limit: 10})
	if len(items) != 1 {
		t.Fatalf("open suggestions: %d", len(items))
	}
	if _, err = h.ReviewService().Resolve(ctx, f.OrgA, items[0].ID, &discovery.Resolution{Action: "dismiss"}); err != nil {
		t.Fatal(err)
	}
	if n, _ = h.SuggestScopes(ctx, f.OrgA, col.ID, []string{"172.16.5.0/24"}); n != 0 {
		t.Error("a dismissed suggestion came back")
	}

	// The organization's intervals (DIS-10, WP-154) come from its settings.
	exec(`UPDATE organization SET settings = '{"discovery": {"poll_interval": "2m", "poll_intervals": {"redfish": "1h"}}}' WHERE id = $1`, f.OrgA)
	iv, err := repo.DiscoveryIntervals(ctx, f.OrgA)
	if err != nil || iv.Sweep != "24h0m0s" || iv.Poll != "5m0s" || iv.Protocols["redfish"] != "1h0m0s" {
		t.Errorf("intervals = %+v, %v", iv, err)
	}

	// Delete.
	if err = repo.DeleteScope(ctx, f.OrgA, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.GetScope(ctx, f.OrgA, s.ID); err == nil {
		t.Error("deleted scope still readable")
	}
}
