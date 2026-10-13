package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
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
