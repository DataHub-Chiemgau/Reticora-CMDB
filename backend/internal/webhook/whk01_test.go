package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
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
	d := NewDispatcher(repo, server.Client(), DispatcherOptions{Deliveries: store, MaxAttempts: 1, PollInterval: -1})
	defer func() { _ = d.Shutdown(context.Background()) }()

	dead := func() int { return len(deadEvents(store)) }
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

	for i, e := range deadEvents(store) {
		disabled, _ := e.Data.(map[string]any)["subscription_disabled"].(bool)
		if disabled != (i == 2) {
			t.Errorf("dead delivery %d: subscription_disabled %v", i+1, disabled)
		}
	}
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

// TestWHK01InvitationCreatedWithoutLink covers WP-421 (NTF-05, F-041): the
// webhook delivery of invitation.created, from the outbox publisher
// (Consume) and from Dispatch, carries the invitation's id, address,
// organization name and expiry, but neither the link nor a token, so a
// webhook receiver cannot accept the invitation; the stored delivery (the
// source of retries) holds neither. An invitation.created payload that is
// not a JSON object is not delivered. Other events keep their keys.
func TestWHK01InvitationCreatedWithoutLink(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.Header.Get("X-Reticora-Event")] = append(bodies[r.Header.Get("X-Reticora-Event")], string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	received := func(event string) []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies[event]...)
	}

	ctx := context.Background()
	repo := NewMemoryRepository()
	if err := repo.Create(ctx, &Subscription{OrganizationID: "org-1", Name: "hook", URL: server.URL,
		Secret: "secret", Events: []string{"invitation.created", "ci.created"}, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	subID := subscriptionID(t, repo)
	store := NewMemoryDeliveryStore()
	d := NewDispatcher(repo, server.Client(), DispatcherOptions{Deliveries: store, PollInterval: -1})
	defer func() { _ = d.Shutdown(context.Background()) }()

	const token = "8f0c3c7e-2a5b-4d4e-9a51-0d1e2f3a4b5c.c2VjcmV0LXRva2VuLW9mLXRoZS1pbnZpdGF0aW9u"
	data, _ := json.Marshal(map[string]any{"id": "inv-1", "email": "new@x.example", "organization_name": "ACME",
		"expires_at": "2026-11-01T12:00:00Z", "link": "https://cmdb.example/invitations/accept?token=" + token, "token": token})
	if err := d.Consume(ctx, &events.Envelope{ID: "e-1", OrgID: "org-1", Type: events.InvitationCreated, Data: data}); err != nil {
		t.Fatal(err)
	}
	d.Dispatch(ctx, "org-1", events.InvitationCreated, map[string]any{"id": "inv-2", "email": "other@x.example",
		"link": "https://cmdb.example/invitations/accept?token=" + token})
	waitFor(t, func() bool { return len(received(events.InvitationCreated)) == 2 })
	for _, body := range received(events.InvitationCreated) {
		var got map[string]any
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("delivery %s: %v", body, err)
		}
		if strings.Contains(body, token) || got["link"] != nil || got["token"] != nil {
			t.Errorf("invitation.created delivered with link or token: %s", body)
		}
		if got["email"] == nil || got["id"] == nil {
			t.Errorf("invitation.created delivered without its other data: %s", body)
		}
		if got["id"] == "inv-1" && (got["organization_name"] != "ACME" || got["expires_at"] != "2026-11-01T12:00:00Z") {
			t.Errorf("invitation.created delivered without organization name or expiry: %s", body)
		}
	}
	records, _, err := store.ListBySubscription(ctx, "org-1", subID, api.PaginationParams{Limit: 10})
	if err != nil || len(records) != 2 {
		t.Fatalf("stored deliveries: %d (%v)", len(records), err)
	}
	for _, rec := range records {
		if strings.Contains(string(rec.Payload), token) || strings.Contains(string(rec.Payload), `"link"`) {
			t.Errorf("stored delivery %s keeps the link: %s", rec.ID, rec.Payload)
		}
	}

	// Not a JSON object: nothing to redact safely, nothing delivered.
	d.Dispatch(ctx, "org-1", events.InvitationCreated, "https://cmdb.example/invitations/accept?token="+token)
	if records, _, _ = store.ListBySubscription(ctx, "org-1", subID, api.PaginationParams{Limit: 10}); len(records) != 2 {
		t.Errorf("a non-object invitation.created was stored for delivery: %d deliveries", len(records))
	}
	// Other events keep their keys.
	d.Dispatch(ctx, "org-1", "ci.created", map[string]any{"id": "ci-1", "link": "https://cmdb.example/cis/ci-1"})
	waitFor(t, func() bool { return len(received("ci.created")) == 1 })
	if body := received("ci.created")[0]; !strings.Contains(body, `"link":"https://cmdb.example/cis/ci-1"`) {
		t.Errorf("ci.created lost its link: %s", body)
	}
	if got := received(events.InvitationCreated); len(got) != 2 {
		t.Errorf("invitation.created deliveries = %d, want 2", len(got))
	}
}
