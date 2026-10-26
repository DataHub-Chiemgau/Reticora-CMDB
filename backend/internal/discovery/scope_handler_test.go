package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// TestScopeREST covers the discovery-scopes resource (DIS-09, API-05):
// create with defaults, validation, list by collector, patch, delete.
func TestScopeREST(t *testing.T) {
	repo := NewMemoryRepository()
	col := &Collector{OrganizationID: "org-1", Name: "col", Config: map[string]any{}}
	if err := repo.RegisterCollector(context.Background(), col); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	NewHandler(repo, nil).RegisterRoutes(mux)
	call := func(method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, discoveryTenantCtx(httptest.NewRequest(method, path, &buf)))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}

	w, created := call(http.MethodPost, "/api/v1/discovery-scopes", map[string]any{"collector_id": col.ID, "kind": "range", "target": "10.0.0.10 - 10.0.0.20"})
	if w.Code != http.StatusCreated || created["target"] != "10.0.0.10-10.0.0.20" || created["schedule"] != "24h" || created["enabled"] != true {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	id, _ := created["id"].(string)
	for name, body := range map[string]map[string]any{
		"kind":     {"collector_id": col.ID, "kind": "subnet", "target": "10.0.0.0/24"},
		"size":     {"collector_id": col.ID, "kind": "cidr", "target": "10.0.0.0/8"},
		"schedule": {"collector_id": col.ID, "kind": "host", "target": "sw1.example.com", "schedule": "every day"},
		"plugin":   {"collector_id": col.ID, "kind": "host", "target": "sw1.example.com", "plugins": []string{"telnet"}},
		"cron":     {"collector_id": col.ID, "kind": "host", "target": "sw1.example.com", "schedule": "61 * * * *"},
	} {
		if w, _ = call(http.MethodPost, "/api/v1/discovery-scopes", body); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w, _ = call(http.MethodPost, "/api/v1/discovery-scopes", map[string]any{"collector_id": col.ID, "kind": "host", "target": "sw1.example.com", "schedule": "*/15 6-18 * * 1-5"}); w.Code != http.StatusCreated {
		t.Errorf("cron schedule: %d %s", w.Code, w.Body.String())
	}
	w, list := call(http.MethodGet, "/api/v1/discovery-scopes?collector_id="+col.ID, nil)
	if w.Code != http.StatusOK || list["total"] != float64(2) {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}
	w, patched := call(http.MethodPatch, "/api/v1/discovery-scopes/"+id, map[string]any{"enabled": false, "plugins": []string{"sweep"}})
	if w.Code != http.StatusOK || patched["enabled"] != false {
		t.Errorf("patch: %d %s", w.Code, w.Body.String())
	}
	if w, _ = call(http.MethodDelete, "/api/v1/discovery-scopes/"+id, nil); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d", w.Code)
	}
	if w, _ = call(http.MethodGet, "/api/v1/discovery-scopes/"+id, nil); w.Code != http.StatusNotFound {
		t.Errorf("get deleted: %d", w.Code)
	}
}

// TestDiscoveryIntervalsFromSettings covers WP-154 (DIS-10): defaults sweep
// 24 h and poll 15 min, organization overrides per protocol, polls never
// below 5 min.
func TestDiscoveryIntervalsFromSettings(t *testing.T) {
	def := IntervalsFromSettings(nil)
	if def.Sweep != "24h0m0s" || def.Poll != "15m0s" || len(def.Protocols) != 0 {
		t.Errorf("defaults = %+v", def)
	}
	got := IntervalsFromSettings(map[string]any{"discovery": map[string]any{
		"sweep_interval": "12h", "poll_interval": "1m",
		"poll_intervals": map[string]any{"snmp": "10m", "redfish": "30s", "sweep": "1h", "telnet": "5m", "ssh": "bogus"},
	}})
	if got.Sweep != "12h0m0s" || got.Poll != "5m0s" || got.Protocols["snmp"] != "10m0s" || got.Protocols["redfish"] != "5m0s" ||
		got.Protocols["ssh"] != "15m0s" || got.Protocols["sweep"] != "" || got.Protocols["telnet"] != "" {
		t.Errorf("org intervals = %+v", got)
	}
	repo := NewMemoryRepository()
	repo.SetOrgSettings("org-1", map[string]any{"discovery": map[string]any{"poll_interval": "20m"}})
	if iv, _ := repo.DiscoveryIntervals(context.Background(), "org-1"); iv.Poll != "20m0s" {
		t.Errorf("memory intervals = %+v", iv)
	}
}

// TestScopeTargetWithoutZone covers WP-383 (DIS-09, API-05; F-108): a host
// or range target with an IPv6 zone (fe80::1%eth0) is refused with 422 on
// the field target, on create and on change, because PostgreSQL inet cannot
// evaluate it; the same addresses without zone are accepted, and CIDR
// targets behave as before (a prefix with zone was never one).
func TestScopeTargetWithoutZone(t *testing.T) {
	for _, tc := range []struct {
		kind, target, want string
	}{
		{ScopeKindHost, "fe80::1%eth0", ""},
		{ScopeKindHost, " FE80::1%25 ", ""},
		{ScopeKindRange, "fe80::1%eth0-fe80::5%eth0", ""},
		{ScopeKindRange, "fe80::1%eth0-fe80::5", ""},
		{ScopeKindRange, "fe80::1 - fe80::5%eth0", ""},
		{ScopeKindCIDR, "fe80::%eth0/64", ""},
		{ScopeKindHost, "FE80::1", "fe80::1"},
		{ScopeKindHost, "10.0.0.1", "10.0.0.1"},
		{ScopeKindRange, "fe80::1 - fe80::5", "fe80::1-fe80::5"},
		{ScopeKindCIDR, "fe80::1/112", "fe80::/112"},
		{ScopeKindCIDR, "10.0.0.7/24", "10.0.0.0/24"},
	} {
		got, err := NormalizeScopeTarget(tc.kind, tc.target)
		if tc.want != "" {
			if err != nil || got != tc.want {
				t.Errorf("NormalizeScopeTarget(%s, %q) = %q, %v; want %q", tc.kind, tc.target, got, err, tc.want)
			}
			continue
		}
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != "target" || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("NormalizeScopeTarget(%s, %q) = %q, %v; want a 422 error on target", tc.kind, tc.target, got, err)
		}
	}
	// CIDR keeps its message: a zone never parsed as a prefix.
	var fe *api.FieldError
	if _, err := NormalizeScopeTarget(ScopeKindCIDR, "fe80::%eth0/112"); !errors.As(err, &fe) || fe.Message != "not a CIDR" {
		t.Errorf("cidr with zone: %v, want the unchanged \"not a CIDR\"", err)
	}

	repo := NewMemoryRepository()
	col := &Collector{OrganizationID: "org-1", Name: "col", Config: map[string]any{}}
	if err := repo.RegisterCollector(context.Background(), col); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	NewHandler(repo, nil).RegisterRoutes(mux)
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, discoveryTenantCtx(httptest.NewRequest(method, path, &buf)))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for _, body := range []map[string]any{
		{"collector_id": col.ID, "kind": "host", "target": "fe80::1%eth0"},
		{"collector_id": col.ID, "kind": "range", "target": "fe80::1%eth0-fe80::5%eth0"},
	} {
		if code, out := call(http.MethodPost, "/api/v1/discovery-scopes", body); code != http.StatusUnprocessableEntity || out["field"] != "target" {
			t.Errorf("POST %v = %d %v, want 422 on target", body, code, out)
		}
	}
	code, created := call(http.MethodPost, "/api/v1/discovery-scopes", map[string]any{"collector_id": col.ID, "kind": "host", "target": "fe80::1"})
	if code != http.StatusCreated || created["target"] != "fe80::1" {
		t.Fatalf("POST host fe80::1 = %d %v", code, created)
	}
	id, _ := created["id"].(string)
	if code, out := call(http.MethodPatch, "/api/v1/discovery-scopes/"+id, map[string]any{"target": "fe80::1%eth0"}); code != http.StatusUnprocessableEntity || out["field"] != "target" {
		t.Errorf("PATCH target with zone = %d %v, want 422 on target", code, out)
	}
	if code, out := call(http.MethodPatch, "/api/v1/discovery-scopes/"+id, map[string]any{"kind": "range", "target": "fe80::1-fe80::5%eth0"}); code != http.StatusUnprocessableEntity || out["field"] != "target" {
		t.Errorf("PATCH range with zone = %d %v, want 422 on target", code, out)
	}
	if s, err := repo.GetScope(context.Background(), "org-1", id); err != nil || s.Kind != ScopeKindHost || s.Target != "fe80::1" {
		t.Errorf("scope after the refused changes = %+v, %v", s, err)
	}
}

// TestScopeLimitContract: the largest scope the API accepts is the one the
// collector expands completely (collector/collectorcmd/testdata/
// scope-limits.json, checked against maxScopeAddresses by the collector's
// test; WP-494, F-106).
func TestScopeLimitContract(t *testing.T) {
	raw, err := os.ReadFile("../../../collector/collectorcmd/testdata/scope-limits.json")
	if err != nil {
		t.Fatal(err)
	}
	var limits struct {
		MaxScopeAddresses int `json:"max_scope_addresses"`
	}
	if err := json.Unmarshal(raw, &limits); err != nil {
		t.Fatal(err)
	}
	if limits.MaxScopeAddresses != MaxScopeAddresses {
		t.Fatalf("MaxScopeAddresses = %d, scope-limits.json = %d: the collector would cut scopes the API accepts",
			MaxScopeAddresses, limits.MaxScopeAddresses)
	}
	// The API accepts a /16 and a range of exactly the limit, nothing larger.
	for _, tc := range []struct {
		kind, target string
		ok           bool
	}{
		{ScopeKindCIDR, "10.0.0.0/16", true},
		{ScopeKindRange, "10.0.0.0-10.0.255.255", true},
		{ScopeKindCIDR, "10.0.0.0/15", false},
		{ScopeKindRange, "10.0.0.0-10.1.0.0", false},
	} {
		if _, err := NormalizeScopeTarget(tc.kind, tc.target); (err == nil) != tc.ok {
			t.Errorf("NormalizeScopeTarget(%s, %s) = %v, want accepted %v", tc.kind, tc.target, err, tc.ok)
		}
	}
}
