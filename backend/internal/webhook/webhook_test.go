package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

func TestHandler_ListDeadLetters(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewMemoryDeliveryStore()

	if err := repo.Create(context.Background(), &Subscription{
		OrganizationID: "org-1", Name: "hook", URL: "https://example.com/hook",
		Secret: "secret", Events: []string{"ci.created"}, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}

	dispatcher := NewDispatcher(repo, nil, DispatcherOptions{
		Deliveries:   store,
		MaxAttempts:  1,
		BaseBackoff:  time.Millisecond,
		PollInterval: -1,
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	}()

	h := NewHandler(repo, dispatcher)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// Empty queue initially.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", "/api/v1/webhooks/dead-letters", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data  []DeadLetter `json:"data"`
		Total int          `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 0 {
		t.Fatalf("expected empty dead-letter queue, got %d", resp.Total)
	}

	// Dispatch to an unreachable endpoint and let the attempt exhaust.
	dispatcher.Dispatch(context.Background(), "org-1", "ci.created", map[string]any{"id": "ci-1"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", "/api/v1/webhooks/dead-letters", nil)))
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Total == 1 {
			if resp.Data[0].Event != "ci.created" || resp.Data[0].Attempts != 1 {
				t.Fatalf("unexpected dead letter: %+v", resp.Data[0])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("dead letter never surfaced on the API")
}

func TestHandler_ListDeadLetters_Unauthorized(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// No tenant context: the handler checks the tenant before touching the
	// (here deliberately absent) dead-letter store, so this exercises the 401
	// path rather than the 503 path.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/webhooks/dead-letters", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHandler_ListDeadLetters_NoStore(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo) // no dispatcher: dead-letter listing unavailable
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", "/api/v1/webhooks/dead-letters", nil)))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
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
	repo.Create(context.Background(), sub)

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
	if err := repo.Create(context.Background(), sub); err != nil {
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

// TestHandler_HeadersAreWriteOnly covers WP-047 (SEC-01): custom header
// values (authentication) are masked in create, get and list responses, while
// the stored subscription keeps them for delivery.
func TestHandler_HeadersAreWriteOnly(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	body := `{"name":"Hook","url":"https://example.com/hook","secret":"s","events":["ci.created"],"headers":{"Authorization":"Bearer hdr-s3cret"}}`
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("POST", "/api/v1/webhooks", bytes.NewBufferString(body))))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created Subscription
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	responses := []string{w.Body.String()}
	for _, path := range []string{"/api/v1/webhooks", "/api/v1/webhooks/" + created.ID} {
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", path, nil)))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, w.Code)
		}
		responses = append(responses, w.Body.String())
	}
	for _, out := range responses {
		if bytes.Contains([]byte(out), []byte("hdr-s3cret")) || !bytes.Contains([]byte(out), []byte(`"Authorization":"***"`)) {
			t.Errorf("response does not mask the header: %s", out)
		}
	}
	stored, err := repo.GetByID(context.Background(), "org-1", created.ID)
	if err != nil || stored.Headers["Authorization"] != "Bearer hdr-s3cret" {
		t.Errorf("stored header %v, %v; want the real value for delivery", stored, err)
	}
}
