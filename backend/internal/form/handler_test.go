package form

import (
	"bytes"
	"encoding/json"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"testing"
)

func formReq(mux chi.Router, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}
func TestFormSubmissionValidation(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	w := formReq(mux, http.MethodPost, "/api/v1/forms", `{"name":"Access","schema":{"type":"object","required":["email"],"properties":{"email":{"type":"string","pattern":"@"}}}}`)
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var def Definition
	_ = json.Unmarshal(w.Body.Bytes(), &def)
	w = formReq(mux, http.MethodPost, "/api/v1/forms/"+def.ID+"/submissions", `{"values":{"email":"nope"}}`)
	if w.Code != 400 {
		t.Fatalf("invalid got %d", w.Code)
	}
	w = formReq(mux, http.MethodPost, "/api/v1/forms/"+def.ID+"/submissions", `{"values":{"email":"a@b"}}`)
	if w.Code != 201 {
		t.Fatalf("valid got %d %s", w.Code, w.Body.String())
	}
}
