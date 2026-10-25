package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// TestWHK01RetrySchedule covers the backoff of WHK-01: 30 s, 2 min, 10 min,
// 60 min, 360 min, then dead after the sixth attempt.
func TestWHK01RetrySchedule(t *testing.T) {
	d := NewDispatcher(NewMemoryRepository(), nil, DispatcherOptions{PollInterval: -1})
	defer func() { _ = d.Shutdown(context.Background()) }()
	want := []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour}
	for i, delay := range want {
		if got := d.backoff(i + 1); got != delay {
			t.Errorf("delay after attempt %d: %s, want %s", i+1, got, delay)
		}
	}
	if d.opts.MaxAttempts != 6 {
		t.Errorf("max attempts %d, want 6 (first attempt and five retries)", d.opts.MaxAttempts)
	}
}

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWHK01HeadersDeactivationAndRetry covers WHK-01: deliveries carry the
// X-Reticora-* headers; the third dead delivery within 24 hours switches the
// subscription off and the webhook.dead event tells the owners; a dead
// delivery can be retried through the API and is delivered.
func TestWHK01HeadersDeactivationAndRetry(t *testing.T) {
	var mu sync.Mutex
	fail := true
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		headers = r.Header.Clone()
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	repo := NewMemoryRepository()
	if err := repo.Create(context.Background(), &Subscription{OrganizationID: "org-1", Name: "hook", URL: server.URL,
		Secret: "secret", Events: []string{"ci.created"}, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	subID := subscriptionID(t, repo)
	store := NewMemoryDeliveryStore()
	emitter := &fakeEmitter{}
	d := NewDispatcher(repo, server.Client(), DispatcherOptions{Deliveries: store, MaxAttempts: 1, PollInterval: -1, Events: emitter})
	defer func() { _ = d.Shutdown(context.Background()) }()

	dead := func() int {
		emitter.mu.Lock()
		defer emitter.mu.Unlock()
		return len(emitter.events)
	}
	for i := 1; i <= 3; i++ {
		d.Dispatch(context.Background(), "org-1", "ci.created", map[string]any{"id": "ci-1"})
		waitFor(t, func() bool { return dead() == i })
	}
	mu.Lock()
	for _, h := range []string{"X-Reticora-Signature", "X-Reticora-Event", "X-Reticora-Delivery"} {
		if headers.Get(h) == "" {
			t.Errorf("header %s missing", h)
		}
	}
	if !strings.HasPrefix(headers.Get("X-Reticora-Signature"), "sha256=") && headers.Get("X-Reticora-Signature") != headers.Get("X-Webhook-Signature") {
		t.Errorf("signature %q", headers.Get("X-Reticora-Signature"))
	}
	mu.Unlock()

	emitter.mu.Lock()
	for i, e := range emitter.events {
		disabled, _ := e.Data.(map[string]any)["subscription_disabled"].(bool)
		if disabled != (i == 2) {
			t.Errorf("dead delivery %d: subscription_disabled %v", i+1, disabled)
		}
	}
	emitter.mu.Unlock()
	sub, err := repo.GetByID(context.Background(), "org-1", subID)
	if err != nil || sub.IsActive {
		t.Fatalf("subscription active after three dead deliveries: %+v (err %v)", sub, err)
	}

	// Retry through the API: the subscriber answers again; the delivery is
	// sent and delivered.
	sub.IsActive = true
	mu.Lock()
	fail = false
	mu.Unlock()
	records, _, err := store.ListBySubscription(context.Background(), "org-1", subID, api.PaginationParams{Limit: 10})
	if err != nil || len(records) == 0 || records[0].Status != StatusDead {
		t.Fatalf("dead deliveries: %+v (err %v)", records, err)
	}
	mux := chi.NewRouter()
	NewHandler(repo, d).RegisterRoutes(mux)
	ctx := orgWideCtx(context.Background(), "org-1", "")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/"+subID+"/deliveries/"+records[0].ID+"/retry", nil).WithContext(ctx))
	if w.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/"+subID+"/deliveries/"+records[0].ID+"/retry", nil).WithContext(ctx))
	if w.Code != http.StatusConflict {
		t.Errorf("retry of a pending delivery: %d, want 409", w.Code)
	}
	d.ProcessDue(context.Background())
	waitFor(t, func() bool {
		recs, _, _ := store.ListBySubscription(context.Background(), "org-1", subID, api.PaginationParams{Limit: 10})
		for _, r := range recs {
			if r.ID == records[0].ID {
				return r.Status == StatusDelivered
			}
		}
		return false
	})
}
