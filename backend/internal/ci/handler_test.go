package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func tenantCtx(r *http.Request) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1"})
	return r.WithContext(ctx)
}

func TestHandler_CreateAndGet(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// Create a CI
	body := `{"name":"sw-core-01","ci_type_id":"type-switch","manufacturer":"Cisco","model":"C9300"}`
	req := httptest.NewRequest("POST", "/api/v1/cis", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created Item
	json.Unmarshal(w.Body.Bytes(), &created)

	if created.Name != "sw-core-01" {
		t.Errorf("expected name sw-core-01, got %s", created.Name)
	}
	if created.ID == "" {
		t.Error("expected non-empty ID")
	}

	// Get the created CI
	req = httptest.NewRequest("GET", "/api/v1/cis/"+created.ID, nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var fetched Item
	json.Unmarshal(w.Body.Bytes(), &fetched)
	if fetched.ID != created.ID {
		t.Errorf("expected ID %s, got %s", created.ID, fetched.ID)
	}
}

// TestHandler_CreateTakesTheFieldsOfCI01 covers CI-01 (WP-120): the
// create path takes every declared field, defaults status and source,
// marks a CI a person creates (manual or import) as manual and rejects an
// unknown source.
func TestHandler_CreateTakesTheFieldsOfCI01(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(NewService(repo)).RegisterRoutes(mux)
	create := func(body string) *httptest.ResponseRecorder {
		req := tenantCtx(httptest.NewRequest(http.MethodPost, "/api/v1/cis", bytes.NewBufferString(body)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	full := map[string]any{
		"ci_type_id": "type-server", "name": "srv-01", "client_id": "client-1", "location_id": "loc-1",
		"parent_ci_id": "ci-chassis", "status": "maintenance",
		"manufacturer": "Dell", "model": "R750", "serial_number": "SN1", "hardware_uuid": "4c4c4544-0000-1000-8000-000000000001",
		"management_ip": "10.0.0.5", "primary_mac": "00:11:22:33:44:55", "hostname": "srv-01", "fqdn": "srv-01.example.org",
		"os_name": "Debian", "os_version": "12", "firmware_version": "2.1", "sys_object_id": "1.3.6.1.4.1.674",
		"attributes": map[string]any{"rack_unit": 12.0},
	}
	raw, _ := json.Marshal(full)
	w := create(string(raw))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range full {
		if key == "attributes" {
			if attrs, _ := got["attributes"].(map[string]any); attrs["rack_unit"] != 12.0 {
				t.Errorf("attributes = %v", got["attributes"])
			}
			continue
		}
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	if got["discovery_source"] != SourceManual || got["is_manual"] != true {
		t.Errorf("manual create: discovery_source %v, is_manual %v", got["discovery_source"], got["is_manual"])
	}

	for source, manual := range map[string]bool{SourceImport: true, SourceManual: true, SourceAPI: false} {
		w = create(`{"ci_type_id":"type-server","name":"srv-` + source + `","discovery_source":"` + source + `"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", source, w.Code, w.Body.String())
		}
		var item Item
		_ = json.Unmarshal(w.Body.Bytes(), &item)
		if item.IsManual != manual || item.Status != StatusActive {
			t.Errorf("%s: is_manual %v status %q, want %v and active", source, item.IsManual, item.Status, manual)
		}
	}

	if w = create(`{"ci_type_id":"type-server","name":"x","discovery_source":"discovery"}`); w.Code != http.StatusBadRequest ||
		!bytes.Contains(w.Body.Bytes(), []byte(`"field":"discovery_source"`)) {
		t.Errorf("unknown source: %d %s, want 400 with the field", w.Code, w.Body.String())
	}
}

func TestHandler_List(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// Create two CIs
	for _, name := range []string{"server-01", "server-02"} {
		item := &Item{
			OrganizationID: "org-1",
			CITypeID:       "type-server",
			Name:           name,
			Status:         "active",
			Attributes:     map[string]any{},
		}
		repo.Create(context.Background(), item)
	}

	req := httptest.NewRequest("GET", "/api/v1/cis?limit=10", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data  []Item `json:"data"`
		Total int    `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Errorf("expected 2 items, got %d", resp.Total)
	}
}

func TestHandler_Update(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	item := &Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "old-name",
		Status:         "active",
		Attributes:     map[string]any{},
	}
	repo.Create(context.Background(), item)

	body := `{"name":"new-name","status":"maintenance"}`
	req := httptest.NewRequest("PATCH", "/api/v1/cis/"+item.ID, bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated Item
	json.Unmarshal(w.Body.Bytes(), &updated)
	if updated.Name != "new-name" {
		t.Errorf("expected new-name, got %s", updated.Name)
	}
	if updated.Status != "maintenance" {
		t.Errorf("expected maintenance, got %s", updated.Status)
	}
}

func TestHandler_Delete(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	item := &Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "to-delete",
		Status:         "active",
		Attributes:     map[string]any{},
	}
	repo.Create(context.Background(), item)

	req := httptest.NewRequest("DELETE", "/api/v1/cis/"+item.ID, nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}

	// Verify deleted
	req = httptest.NewRequest("GET", "/api/v1/cis/"+item.ID, nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_Unauthorized(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// Request without tenant context
	req := httptest.NewRequest("GET", "/api/v1/cis", nil)
	req = req.WithContext(context.Background())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHandler_ListFilter(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	repo.Create(context.Background(), &Item{OrganizationID: "org-1", CITypeID: "type-server", Name: "srv-active", Status: "active", Attributes: map[string]any{}})
	repo.Create(context.Background(), &Item{OrganizationID: "org-1", CITypeID: "type-server", Name: "srv-maint", Status: "maintenance", Attributes: map[string]any{}})

	req := httptest.NewRequest("GET", "/api/v1/cis?status=active", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp struct {
		Data  []Item `json:"data"`
		Total int    `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("expected 1 item with status=active, got %d", resp.Total)
	}
}

// TestHandler_IncludeAndRestore covers API-05 and CI-05: GET /cis/{id}
// embeds the requested related collections (unknown names are 422), and a
// deleted CI is restored within the retention (later: 410; not deleted:
// 404).
func TestHandler_IncludeAndRestore(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo)).WithIncludes(map[string]IncludeFunc{
		"relationships": func(_ context.Context, orgID, ciID string) (any, error) {
			return []map[string]string{{"source_ci_id": ciID, "organization_id": orgID}}, nil
		},
	})
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantCtx(httptest.NewRequest(method, path, bytes.NewBufferString(body))))
		return w
	}
	w := call("POST", "/api/v1/cis", `{"name":"inc-1","ci_type_id":"type-switch"}`)
	var item Item
	_ = json.Unmarshal(w.Body.Bytes(), &item)

	w = call("GET", "/api/v1/cis/"+item.ID+"?include=relationships,asset", "")
	var got map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("include: %d %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(got["relationships"], []byte(item.ID)) || string(got["asset"]) != "null" || string(got["name"]) != `"inc-1"` {
		t.Errorf("include body %s", w.Body.String())
	}
	if w = call("GET", "/api/v1/cis/"+item.ID+"?include=secrets", ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown include: %d", w.Code)
	}

	if w = call("POST", "/api/v1/cis/"+item.ID+"/restore", ""); w.Code != http.StatusNotFound {
		t.Errorf("restore of a live CI: %d", w.Code)
	}
	if w = call("DELETE", "/api/v1/cis/"+item.ID, ""); w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	if w = call("GET", "/api/v1/cis/"+item.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("deleted CI still served: %d", w.Code)
	}
	if w = call("POST", "/api/v1/cis/"+item.ID+"/restore", ""); w.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	if w = call("GET", "/api/v1/cis/"+item.ID, ""); w.Code != http.StatusOK {
		t.Errorf("restored CI not served: %d", w.Code)
	}
	call("DELETE", "/api/v1/cis/"+item.ID, "")
	expired := repo.items[item.ID]
	old := expired.DeletedAt.Add(-RestoreRetention - time.Hour)
	expired.DeletedAt = &old
	if w = call("POST", "/api/v1/cis/"+item.ID+"/restore", ""); w.Code != http.StatusGone {
		t.Errorf("restore after the retention: %d %s", w.Code, w.Body.String())
	}
}
