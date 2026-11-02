package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// CI types of the tests: a type is named by its ID, a UUID (CI-01; a key
// such as "server" is refused with 422, WP-396).
const (
	testServerType = "c1000000-0000-4000-8000-000000000001"
	testSwitchType = "c1000000-0000-4000-8000-000000000002"
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
	body := `{"name":"sw-core-01","ci_type_id":"` + testSwitchType + `","manufacturer":"Cisco","model":"C9300"}`
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
		"ci_type_id": testServerType, "name": "srv-01", "client_id": "client-1", "location_id": "loc-1",
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
		w = create(`{"ci_type_id":"` + testServerType + `","name":"srv-` + source + `","discovery_source":"` + source + `"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", source, w.Code, w.Body.String())
		}
		var item Item
		_ = json.Unmarshal(w.Body.Bytes(), &item)
		if item.IsManual != manual || item.Status != StatusActive {
			t.Errorf("%s: is_manual %v status %q, want %v and active", source, item.IsManual, item.Status, manual)
		}
	}

	if w = create(`{"ci_type_id":"` + testServerType + `","name":"x","discovery_source":"discovery"}`); w.Code != http.StatusBadRequest ||
		!bytes.Contains(w.Body.Bytes(), []byte(`"field":"discovery_source"`)) {
		t.Errorf("unknown source: %d %s, want 400 with the field", w.Code, w.Body.String())
	}
}

// TestHandler_CreateNeedsTheTypeID covers WP-396 (CI-01; backend part of
// F-016): a ci_type_id that is no UUID, such as the key "server" the CI
// dialog sent, is 422 on ci_type_id instead of 400, and no CI is stored.
func TestHandler_CreateNeedsTheTypeID(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(NewService(repo)).RegisterRoutes(mux)
	for _, typeID := range []string{"server", "type-switch"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantCtx(httptest.NewRequest(http.MethodPost, "/api/v1/cis",
			bytes.NewBufferString(`{"name":"srv-key","ci_type_id":"`+typeID+`"}`))))
		var problem struct {
			Violations []Violation `json:"violations"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusUnprocessableEntity || len(problem.Violations) != 1 ||
			problem.Violations[0] != (Violation{Field: "ci_type_id", Detail: "not the ID (UUID) of a CI type"}) {
			t.Errorf("POST /cis with ci_type_id %q: %d %s, want 422 on ci_type_id", typeID, w.Code, w.Body.String())
		}
	}
	if _, total, err := repo.List(context.Background(), "org-1", FilterParams{}, api.PaginationParams{Limit: 10}); err != nil || total != 0 {
		t.Errorf("CIs stored: %d, %v; want none", total, err)
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

// TestHandler_UpdateMergePatchNull covers WP-476 (CI-04, F-080) through the
// OpenAPI validation middleware of the server: PATCH /cis/{id} with a null
// member passes the middleware, clears the typed field and leaves absent
// members; null on name or status is 422 naming the field; an unknown member
// stays 400.
func TestHandler_UpdateMergePatchNull(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(NewService(repo)).RegisterRoutes(mux)
	validated := middleware.NewOpenAPIValidator().Middleware(mux)
	item := &Item{OrganizationID: "org-1", CITypeID: "type-server", Name: "null-ci", Status: "active",
		Model: "M1", FirmwareVersion: "1.2.3", Attributes: map[string]any{}}
	if err := repo.Create(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	patch := func(body, contentType string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/cis/"+item.ID, strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		validated.ServeHTTP(w, tenantCtx(req))
		return w
	}
	// The middleware validates this route: a body without JSON type is 400.
	if w := patch(`{"firmware_version":null}`, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH without Content-Type = %d, want 400 from the validation middleware", w.Code)
	}

	w := patch(`{"firmware_version":null}`, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH {firmware_version:null} = %d %s, want 200", w.Code, w.Body.String())
	}
	var updated Item
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.FirmwareVersion != "" || updated.Model != "M1" || updated.Name != "null-ci" {
		t.Errorf("after the null patch: firmware_version %q model %q name %q, want cleared, M1, null-ci",
			updated.FirmwareVersion, updated.Model, updated.Name)
	}

	for _, field := range []string{"name", "status"} {
		w := patch(`{"`+field+`":null}`, "application/json")
		var problem struct {
			Violations []Violation `json:"violations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || w.Code != http.StatusUnprocessableEntity ||
			len(problem.Violations) != 1 || problem.Violations[0].Field != field {
			t.Errorf("PATCH {%q:null} = %d %s, want 422 naming %s", field, w.Code, w.Body.String(), field)
		}
	}
	if got, _ := repo.GetByID(context.Background(), "org-1", item.ID); got.Name != "null-ci" || got.Status != "active" {
		t.Errorf("required fields after the refused null: %q %q", got.Name, got.Status)
	}
	if w := patch(`{"hostnme":null}`, "application/json"); w.Code != http.StatusBadRequest {
		t.Errorf("PATCH with an unknown member = %d, want 400", w.Code)
	}
}

// failingUpdateRepo is a repository whose update fails like a broken
// database connection.
type failingUpdateRepo struct {
	*MemoryRepository
	err error
}

func (r failingUpdateRepo) Update(context.Context, string, string, UpdateRequest) (*Item, error) {
	return nil, r.err
}

// TestHandler_UpdateErrorIsNotNotFound covers WP-476 (F-229): only
// ErrNotFound of the repository is 404 "CI not found"; any other failure,
// such as a reset database connection, is 500 without internals.
func TestHandler_UpdateErrorIsNotNotFound(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
	}{
		{errors.New("read tcp 10.0.0.1:5432: connection reset by peer"), http.StatusInternalServerError},
		{ErrNotFound, http.StatusNotFound},
	} {
		mux := chi.NewRouter()
		NewHandler(NewService(failingUpdateRepo{NewMemoryRepository(), c.err})).RegisterRoutes(mux)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/cis/c2000000-0000-4000-8000-000000000001", strings.NewReader(`{"name":"x"}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantCtx(req))
		if w.Code != c.status {
			t.Errorf("repository error %q: %d %s, want %d", c.err, w.Code, w.Body.String(), c.status)
		}
		if c.status == http.StatusInternalServerError && (strings.Contains(w.Body.String(), "connection reset") || strings.Contains(w.Body.String(), "not found")) {
			t.Errorf("500 body %s leaks the cause or claims not found", w.Body.String())
		}
	}

	// An unknown CI of the memory repository is still 404.
	mux := chi.NewRouter()
	NewHandler(NewService(NewMemoryRepository())).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantCtx(httptest.NewRequest(http.MethodPatch, "/api/v1/cis/unknown", strings.NewReader(`{"name":"x"}`))))
	if w.Code != http.StatusNotFound {
		t.Errorf("PATCH of an unknown CI = %d, want 404", w.Code)
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
// embeds the requested related collections (unknown names are 422; a
// capped one is named in truncated, F-223), and a deleted CI is restored
// within the retention (later: 410; not deleted: 409, WP-567; unknown:
// 404).
func TestHandler_IncludeAndRestore(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo)).WithIncludes(map[string]IncludeFunc{
		"relationships": func(_ context.Context, orgID, ciID string) (any, bool, error) {
			return []map[string]string{{"source_ci_id": ciID, "organization_id": orgID}}, false, nil
		},
		// contacts stands for a collection the server capped (F-223).
		"contacts": func(_ context.Context, _, ciID string) (any, bool, error) {
			return []map[string]string{{"ci_id": ciID}}, true, nil
		},
	})
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantCtx(httptest.NewRequest(method, path, bytes.NewBufferString(body))))
		return w
	}
	w := call("POST", "/api/v1/cis", `{"name":"inc-1","ci_type_id":"`+testSwitchType+`"}`)
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
	if capped, ok := got[IncludeTruncated]; ok {
		t.Errorf("include of complete collections: truncated %s, want absent", capped)
	}
	// A capped collection is named in truncated, the complete one not.
	w = call("GET", "/api/v1/cis/"+item.ID+"?include=relationships,contacts", "")
	got = nil
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("include with a capped collection: %d %s", w.Code, w.Body.String())
	}
	if string(got[IncludeTruncated]) != `{"contacts":true}` || !bytes.Contains(got["contacts"], []byte(item.ID)) {
		t.Errorf("include with a capped collection: %s, want truncated {\"contacts\":true}", w.Body.String())
	}
	if w = call("GET", "/api/v1/cis/"+item.ID+"?include=secrets", ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown include: %d", w.Code)
	}

	if w = call("POST", "/api/v1/cis/"+item.ID+"/restore", ""); w.Code != http.StatusConflict {
		t.Errorf("restore of a live CI: %d, want 409", w.Code)
	}
	if w = call("POST", "/api/v1/cis/00000000-0000-4000-8000-0000000005a7/restore", ""); w.Code != http.StatusNotFound {
		t.Errorf("restore of an unknown CI: %d, want 404", w.Code)
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
