package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func tenantCtx(r *http.Request) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1"})
	return r.WithContext(ctx)
}

func TestHandler_CreateAndList(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"name":"My Hook","url":"https://example.com/hook","secret":"s3cr3t","events":["ci.created","ci.updated"]}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created Subscription
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.Name != "My Hook" {
		t.Errorf("expected My Hook, got %s", created.Name)
	}
	if !created.IsActive {
		t.Error("expected is_active to be true")
	}

	// List
	req = httptest.NewRequest("GET", "/api/v1/webhooks", nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data  []Subscription `json:"data"`
		Total int            `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("expected 1, got %d", resp.Total)
	}
}

func TestHandler_InvalidEvent(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"name":"Bad","url":"https://example.com","secret":"x","events":["bad.event"]}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_Delete(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	sub := &Subscription{
		OrganizationID: "org-1",
		Name:           "to-delete",
		URL:            "https://example.com",
		Secret:         "secret",
		Events:         []string{"ci.created"},
		IsActive:       true,
	}
	repo.Create(sub)

	req := httptest.NewRequest("DELETE", "/api/v1/webhooks/"+sub.ID, nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

func TestHandler_Test_DeliversSignedPing(t *testing.T) {
	var (
		gotEvent     string
		gotSignature string
		gotBody      []byte
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEvent = r.Header.Get("X-Webhook-Event")
		gotSignature = r.Header.Get("X-Webhook-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	repo := NewMemoryRepository()
	sub := &Subscription{
		OrganizationID: "org-1",
		Name:           "Ping",
		URL:            target.URL,
		Secret:         "s3cr3t",
		Events:         []string{"ci.created"},
		IsActive:       true,
	}
	if err := repo.Create(sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	dispatcher := NewDispatcher(repo, target.Client())
	defer dispatcher.Shutdown(context.Background())

	mux := chi.NewRouter()
	NewHandler(repo, dispatcher).RegisterRoutes(mux)

	req := tenantCtx(httptest.NewRequest("POST", "/api/v1/webhooks/"+sub.ID+"/test", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var delivery Delivery
	if err := json.Unmarshal(w.Body.Bytes(), &delivery); err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	if !delivery.Success || delivery.StatusCode != http.StatusOK {
		t.Errorf("expected a successful delivery, got %+v", delivery)
	}
	if gotEvent != TestEvent {
		t.Errorf("expected event %q, got %q", TestEvent, gotEvent)
	}
	if want := signPayload(sub.Secret, gotBody); gotSignature != want {
		t.Errorf("expected signature %q, got %q", want, gotSignature)
	}
}

func TestHandler_Test_UnknownSubscription(t *testing.T) {
	repo := NewMemoryRepository()
	dispatcher := NewDispatcher(repo, nil)
	defer dispatcher.Shutdown(context.Background())

	mux := chi.NewRouter()
	NewHandler(repo, dispatcher).RegisterRoutes(mux)

	req := tenantCtx(httptest.NewRequest("POST", "/api/v1/webhooks/does-not-exist/test", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
