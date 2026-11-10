package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/go-chi/chi/v5"
)

func TestDispatcherDeliversSignedWebhook(t *testing.T) {
	repo := NewMemoryRepository()
	var received struct {
		Event     string
		Signature string
		ID        string
		Payload   map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Event = r.Header.Get("X-Webhook-Event")
		received.Signature = r.Header.Get("X-Webhook-Signature")
		received.ID = r.Header.Get("X-Webhook-ID")
		_ = json.NewDecoder(r.Body).Decode(&received.Payload)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := repo.Create(context.Background(), &Subscription{OrganizationID: "org-1", Name: "hook", URL: server.URL, Secret: "secret", Events: []string{"ci.created"}, IsActive: true}); err != nil {
		t.Fatal(err)
	}

	dispatcher := NewDispatcher(repo, server.Client())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	}()

	dispatcher.Dispatch(context.Background(), "org-1", "ci.created", map[string]any{"id": "ci-1"})
	deadline := time.Now().Add(2 * time.Second)
	for len(dispatcher.Deliveries()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	deliveries := dispatcher.Deliveries()
	if len(deliveries) != 1 || !deliveries[0].Success {
		t.Fatalf("unexpected deliveries: %+v", deliveries)
	}
	if received.Event != "ci.created" || received.ID == "" || received.Signature == "" {
		t.Fatalf("unexpected headers: %+v", received)
	}
}

func TestDispatcherPersistsAndRetriesFailedDelivery(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewMemoryDeliveryStore()

	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := repo.Create(context.Background(), &Subscription{
		OrganizationID: "org-1", Name: "hook", URL: server.URL,
		Secret: "secret", Events: []string{"ci.created"}, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}

	dispatcher := NewDispatcher(repo, server.Client(), DispatcherOptions{
		Deliveries:   store,
		MaxAttempts:  3,
		BaseBackoff:  time.Millisecond,
		PollInterval: -1, // the test drives the retry worker explicitly
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	}()

	dispatcher.Dispatch(context.Background(), "org-1", "ci.created", map[string]any{"id": "ci-1"})

	page := api.PaginationParams{Limit: 10}
	deadline := time.Now().Add(2 * time.Second)
	var stored []DeliveryRecord
	for time.Now().Before(deadline) {
		records, _, err := store.ListBySubscription(context.Background(), "org-1", subscriptionID(t, repo), page)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 1 && records[0].Status == StatusFailed {
			stored = records
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(stored) != 1 {
		t.Fatalf("expected the failed delivery to be persisted for retry, got %+v", stored)
	}
	if stored[0].Attempt != 1 || stored[0].NextRetryAt == nil {
		t.Fatalf("expected a scheduled retry, got %+v", stored[0])
	}

	// The scheduled retry becomes due and succeeds.
	time.Sleep(5 * time.Millisecond)
	dispatcher.ProcessDue(context.Background())

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		records, _, err := store.ListBySubscription(context.Background(), "org-1", subscriptionID(t, repo), page)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 1 && records[0].Status == StatusDelivered {
			if records[0].Attempt != 2 || records[0].DeliveredAt == nil || records[0].NextRetryAt != nil {
				t.Fatalf("unexpected successful delivery record: %+v", records[0])
			}
			if atomic.LoadInt32(&attempts) != 2 {
				t.Fatalf("expected exactly two HTTP attempts, got %d", attempts)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("delivery was not marked successful after the retry")
}

func TestDispatcherMarksDeliveryFailedAfterMaxAttempts(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewMemoryDeliveryStore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	if err := repo.Create(context.Background(), &Subscription{
		OrganizationID: "org-1", Name: "hook", URL: server.URL,
		Secret: "secret", Events: []string{"ci.created"}, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}

	dispatcher := NewDispatcher(repo, server.Client(), DispatcherOptions{
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

	dispatcher.Dispatch(context.Background(), "org-1", "ci.created", map[string]any{"id": "ci-1"})

	page := api.PaginationParams{Limit: 10}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		records, _, err := store.ListBySubscription(context.Background(), "org-1", subscriptionID(t, repo), page)
		if err != nil {
			t.Fatal(err)
		}
		// Exhausted deliveries are moved to the dead-letter queue and marked
		// dead on the delivery record.
		if len(records) == 1 && records[0].Status == StatusDead {
			if records[0].NextRetryAt != nil || records[0].Error == "" {
				t.Fatalf("unexpected dead delivery record: %+v", records[0])
			}
			letters, total, err := store.ListDeadLetters(context.Background(), "org-1", page)
			if err != nil {
				t.Fatal(err)
			}
			if total != 1 || len(letters) != 1 {
				t.Fatalf("expected exactly one dead letter, got %d", total)
			}
			if letters[0].DeliveryID != records[0].ID || letters[0].Attempts != 1 || letters[0].LastError == "" {
				t.Fatalf("unexpected dead letter: %+v", letters[0])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("delivery was not moved to the dead-letter queue after exhausting its attempts")
}

func subscriptionID(t *testing.T, repo *MemoryRepository) string {
	t.Helper()
	subs, err := repo.ListByEvent(context.Background(), "org-1", "ci.created")
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected exactly one subscription, got %d", len(subs))
	}
	return subs[0].ID
}

// TestDispatcherDefaultClientBlocksInternalDestinations covers WP-049
// (SEC-08): without an injected client the dispatcher uses the egress
// client, so a subscriber in the internal network is never reached.
func TestDispatcherDefaultClientBlocksInternalDestinations(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dispatcher := NewDispatcher(NewMemoryRepository(), nil)
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()
	delivery, err := dispatcher.DeliverOnce(&Subscription{ID: "s", OrganizationID: "org-1", URL: server.URL, Secret: "k", Events: []string{"ci.created"}, IsActive: true}, "ci.created", map[string]string{"x": "y"})
	if err == nil && delivery.Success {
		t.Fatalf("delivery to a loopback subscriber succeeded: %+v", delivery)
	}
	if hits.Load() != 0 {
		t.Fatal("the internal subscriber was reached")
	}
	if !strings.Contains(delivery.Error+fmt.Sprint(err), "not allowed") {
		t.Errorf("delivery error %q / %v does not name the blocked destination", delivery.Error, err)
	}
}

// fakeAccess grants read rights per service account, permission and client;
// the client "*" stands for an event of unknown scope.
type fakeAccess map[string]bool

func (f fakeAccess) CanReadEvent(_ context.Context, _, serviceAccountID, permission string, objects []ObjectScope) (bool, error) {
	if objects == nil {
		return f[serviceAccountID+"|"+permission+"|*"], nil
	}
	for _, o := range objects {
		if !f[serviceAccountID+"|"+permission+"|"+o.ClientID] {
			return false, nil
		}
	}
	return true, nil
}

// FieldPolicy implements SubscriberAccess: the fake knows no field
// permissions, so every payload passes unchanged (fieldrights_test.go
// covers the projection).
func (f fakeAccess) FieldPolicy(context.Context, string, string) (*permission.FieldPolicy, error) {
	return permission.UnrestrictedPolicy(identity.AllPermissions()), nil
}

// TestDispatcherFiltersBoundSubscriptions covers WP-069 (RBA-08): a
// subscription bound to a service account receives an event only when the
// account may read the event's object; unbound subscriptions receive every
// event; without an access checker or for events without a known object,
// bound subscriptions receive nothing.
func TestDispatcherFiltersBoundSubscriptions(t *testing.T) {
	var mu sync.Mutex
	received := map[string][]string{} // subscription name -> client ids
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ClientID string `json:"client_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		received[r.URL.Path] = append(received[r.URL.Path], payload.ClientID)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	run := func(access SubscriberAccess, event string, clients ...string) map[string][]string {
		t.Helper()
		mu.Lock()
		received = map[string][]string{}
		mu.Unlock()
		repo := NewMemoryRepository()
		for _, sub := range []Subscription{
			{Name: "unbound", URL: server.URL + "/unbound"},
			{Name: "bound", URL: server.URL + "/bound", ServiceAccountID: "sa-1"},
		} {
			sub.OrganizationID, sub.Secret, sub.Events, sub.IsActive = "org-1", "secret", []string{event}, true
			if err := repo.Create(context.Background(), &sub); err != nil {
				t.Fatal(err)
			}
		}
		d := NewDispatcher(repo, server.Client(), DispatcherOptions{Access: access, PollInterval: -1})
		for _, c := range clients {
			d.Dispatch(context.Background(), "org-1", event, map[string]any{"id": "ci-" + c, "client_id": c})
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
		mu.Lock()
		defer mu.Unlock()
		out := map[string][]string{}
		for k, v := range received {
			out[k] = append([]string(nil), v...)
		}
		return out
	}

	access := fakeAccess{"sa-1|ci:read|c1": true}
	got := run(access, "ci.created", "c1", "c2")
	if len(got["/unbound"]) != 2 {
		t.Errorf("unbound subscription received %v, want both events", got["/unbound"])
	}
	if strings.Join(got["/bound"], ",") != "c1" {
		t.Errorf("bound subscription received %v, want only the event of client c1", got["/bound"])
	}
	if got = run(nil, "ci.created", "c1"); len(got["/bound"]) != 0 {
		t.Errorf("bound subscription without access checker received %v", got["/bound"])
	}
	if got = run(access, "workflow.custom", "c1"); len(got["/bound"]) != 0 || len(got["/unbound"]) != 1 {
		t.Errorf("event without known object: %v", got)
	}
}

// TestDispatcherClaimsTheFirstAttempt covers TEC-14 (WP-130): the first
// attempt of a delivery is leased to the dispatching process, so a retry
// worker running meanwhile does not send it a second time; when the
// process dies before recording the attempt, the retry worker takes the
// delivery over after the lease.
func TestDispatcherClaimsTheFirstAttempt(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewMemoryDeliveryStore()
	var attempts int32
	release := make(chan struct{})
	inFlight := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		inFlight <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := repo.Create(context.Background(), &Subscription{
		OrganizationID: "org-1", Name: "hook", URL: server.URL,
		Secret: "secret", Events: []string{"ci.created"}, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(repo, server.Client(), DispatcherOptions{Deliveries: store, PollInterval: -1})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	}()

	dispatcher.Dispatch(context.Background(), "org-1", "ci.created", map[string]any{"id": "ci-1"})
	<-inFlight
	// The retry worker runs while the first attempt is in flight.
	dispatcher.ProcessDue(context.Background())
	close(release)
	page := api.PaginationParams{Limit: 10}
	deadline := time.Now().Add(2 * time.Second)
	for {
		records, _, err := store.ListBySubscription(context.Background(), "org-1", subscriptionID(t, repo), page)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 1 && records[0].Status == StatusDelivered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery not recorded: %+v", records)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("HTTP attempts: %d, want 1", n)
	}

	// A process died after storing a delivery: it is due once its lease
	// has ended.
	leased := time.Now().UTC().Add(defaultClaimLease)
	if _, err := store.Enqueue(context.Background(), &DeliveryRecord{
		OrganizationID: "org-1", SubscriptionID: subscriptionID(t, repo), Event: "ci.created",
		Payload: []byte(`{"id":"ci-2"}`), Status: StatusPending, MaxAttempts: 3, NextRetryAt: &leased,
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher.ProcessDue(context.Background())
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("a leased delivery was sent before its lease ended (%d attempts)", n)
	}
	dispatcher.now = func() time.Time { return leased.Add(time.Second) }
	dispatcher.ProcessDue(context.Background())
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Errorf("HTTP attempts after the lease: %d, want 2", n)
	}
}

// TestDispatcherConsumesOutboxEvents covers EVT-02 (WP-136): the outbox
// publisher hands domain events to the dispatcher; subscriptions of the
// event type and of its former names receive them once. A status change
// reaches ci.status_changed only as an event of its own, once and under
// its name, also with further changed fields; ci.changed with
// changed_fields [status] does not reach it (WP-707, EVT-03).
func TestDispatcherConsumesOutboxEvents(t *testing.T) {
	repo := NewMemoryRepository()
	var mu sync.Mutex
	received := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		event := r.Header.Get("X-Webhook-Event")
		if reticora := r.Header.Get("X-Reticora-Event"); reticora != event {
			event += " (X-Reticora-Event " + reticora + ")"
		}
		received[r.URL.Path] = append(received[r.URL.Path], event)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	for name, evs := range map[string][]string{
		"/new":    {"ci.changed"},
		"/legacy": {"ci.updated"},
		"/both":   {"ci.changed", "ci.updated"},
		"/status": {"ci.status_changed"},
	} {
		if err := repo.Create(context.Background(), &Subscription{OrganizationID: "org-1", Name: name, URL: server.URL + name,
			Secret: "secret", Events: evs, IsActive: true}); err != nil {
			t.Fatal(err)
		}
	}
	d := NewDispatcher(repo, server.Client())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
	}()
	for i, env := range []struct{ typ, data string }{
		{events.CIChanged, `{"id":"ci-1","changed_fields":["name"]}`},
		{events.CIChanged, `{"id":"ci-1","changed_fields":["status","name"]}`},
		{events.CIStatusChanged, `{"id":"ci-1","changed_fields":["status","name"],"from":"active","to":"maintenance"}`},
	} {
		if err := d.Consume(context.Background(), &events.Envelope{ID: fmt.Sprintf("e%d", i), Type: env.typ, OrgID: "org-1",
			Data: json.RawMessage(env.data)}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(received["/new"]) + len(received["/legacy"]) + len(received["/both"]) + len(received["/status"])
		mu.Unlock()
		if n >= 7 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received["/new"]) != 2 || len(received["/legacy"]) != 2 || len(received["/both"]) != 2 || len(received["/status"]) != 1 {
		t.Errorf("deliveries: %v", received)
	}
	if !slices.Equal(received["/status"], []string{events.CIStatusChanged}) {
		t.Errorf("deliveries to the ci.status_changed subscription: %v, want one under ci.status_changed", received["/status"])
	}
	if received["/legacy"][0] != "ci.changed" {
		t.Errorf("event name for a legacy subscription: %v", received["/legacy"])
	}
}

// deadEvents returns the webhook.dead events the memory store recorded with
// its dead letters (WP-550).
func deadEvents(s *MemoryDeliveryStore) []events.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]events.Event(nil), s.outbox...)
}

// TestDeadDeliveryIsReported covers NTF-05 (WP-140): a delivery moved to
// the dead-letter queue reports webhook.dead, recorded with the move
// (WP-550).
func TestDeadDeliveryIsReported(t *testing.T) {
	repo := NewMemoryRepository()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	if err := repo.Create(context.Background(), &Subscription{OrganizationID: "org-1", Name: "hook", URL: server.URL,
		Secret: "secret", Events: []string{"ci.created"}, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryDeliveryStore()
	d := NewDispatcher(repo, server.Client(), DispatcherOptions{Deliveries: store, MaxAttempts: 1, PollInterval: -1})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
	}()
	d.Dispatch(context.Background(), "org-1", "ci.created", map[string]any{"id": "ci-1"})
	deadline := time.Now().Add(2 * time.Second)
	for len(deadEvents(store)) == 0 && !time.Now().After(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := deadEvents(store)
	if len(got) != 1 || got[0].Type != events.WebhookDead || got[0].OrgID != "org-1" {
		t.Fatalf("events: %+v", got)
	}
	data := got[0].Data.(map[string]any)
	if data["event"] != "ci.created" || data["attempts"] != 1 || data["subscription_id"] != subscriptionID(t, repo) {
		t.Errorf("data: %v", data)
	}
}

// TestRotatedSecretSignsDuringOverlap covers SEC-06 (WP-141): after a
// rotation a delivery carries the signature of the new secret and, during
// the overlap, of the previous one; the rotate endpoint returns the new
// secret once.
func TestRotatedSecretSignsDuringOverlap(t *testing.T) {
	repo := NewMemoryRepository()
	var mu sync.Mutex
	var headers []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	sub := &Subscription{OrganizationID: "org-1", Name: "hook", URL: server.URL, Secret: "old-secret-123456", Events: []string{"ci.created"}, IsActive: true}
	if err := repo.Create(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(repo, server.Client())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
	}()
	h := NewHandler(repo, d)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/"+sub.ID+"/rotate-secret", nil)
	req = req.WithContext(orgWideCtx(req.Context(), "org-1", "u-1"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var out struct {
		Secret  string     `json:"secret"`
		Expires *time.Time `json:"previous_secret_expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK || !strings.HasPrefix(out.Secret, "whsec_") ||
		out.Expires == nil || time.Until(*out.Expires) < 23*time.Hour {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}

	payload := map[string]any{"id": "ci-1"}
	body, _ := json.Marshal(payload)
	d.Dispatch(context.Background(), "org-1", "ci.created", payload)
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(headers)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 1 {
		t.Fatalf("deliveries: %d", len(headers))
	}
	if headers[0].Get("X-Webhook-Signature") != signPayload(out.Secret, body) ||
		headers[0].Get("X-Webhook-Signature-Previous") != signPayload("old-secret-123456", body) {
		t.Errorf("signatures: %v", headers[0])
	}
	stored, _ := repo.GetByID(context.Background(), "org-1", sub.ID)
	expired := time.Now().Add(-time.Minute)
	stored.PreviousSecretExpiresAt = &expired
	d.Dispatch(context.Background(), "org-1", "ci.created", payload)
	deadline = time.Now().Add(2 * time.Second)
	for len(headers) < 2 && time.Now().Before(deadline) {
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
	}
	if len(headers) != 2 || headers[1].Get("X-Webhook-Signature-Previous") != "" {
		t.Errorf("previous signature after the overlap: %v", headers)
	}
}

// TestDispatcherDeliversOutboxEventsIdempotently covers WP-506 (EVT-01,
// F-151, F-320): a delivery of an outbox event carries the event ID and
// its sequence numbers; its delivery ID derives from the event and the
// subscription, so a repeated hand-off of the event stores and sends no
// second delivery and a redelivery keeps the ID and the headers.
func TestDispatcherDeliversOutboxEventsIdempotently(t *testing.T) {
	repo := NewMemoryRepository()
	store := NewMemoryDeliveryStore()
	var mu sync.Mutex
	var received []http.Header
	status := atomic.Int32{}
	status.Store(http.StatusServiceUnavailable)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received = append(received, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	sub := &Subscription{OrganizationID: "org-1", Name: "hook", URL: server.URL, Secret: "secret", Events: []string{events.CICreated}, IsActive: true}
	if err := repo.Create(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(repo, server.Client(), DispatcherOptions{Deliveries: store, PollInterval: -1})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
	}()
	env := &events.Envelope{ID: "7a1c0de0-0000-4000-8000-000000000001", Type: events.CICreated, OrgID: "org-1",
		Sequence: 42, EntityID: "ci-1", EntitySeq: 3, Data: json.RawMessage(`{"id":"ci-1"}`)}
	headersOf := func(n int) []http.Header {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			mu.Lock()
			got := append([]http.Header(nil), received...)
			mu.Unlock()
			if len(got) >= n || time.Now().After(deadline) {
				return got
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitStatus := func(want string) DeliveryRecord {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			recs, _, err := store.ListBySubscription(context.Background(), "org-1", sub.ID, api.PaginationParams{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) == 1 && recs[0].Status == want {
				return recs[0]
			}
			if time.Now().After(deadline) {
				t.Fatalf("deliveries = %+v, want one %s", recs, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	for i := 0; i < 2; i++ {
		if err := d.Consume(context.Background(), env); err != nil {
			t.Fatalf("hand-off %d: %v", i+1, err)
		}
	}
	rec := waitStatus(StatusFailed)
	if want := deliveryIDFor(env.ID, sub.ID); rec.ID != want {
		t.Errorf("delivery ID = %s, want %s (derived from event and subscription)", rec.ID, want)
	}
	if rec.EventID != env.ID {
		t.Errorf("stored event ID = %q, want %q", rec.EventID, env.ID)
	}
	if deliveryIDFor(env.ID, sub.ID) != rec.ID || deliveryIDFor(env.ID, "other") == rec.ID {
		t.Errorf("delivery ID is not deterministic per event and subscription")
	}
	first := headersOf(1)
	if len(first) != 1 {
		t.Fatalf("HTTP attempts after two hand-offs of one event = %d, want 1", len(first))
	}
	for name, want := range map[string]string{
		"X-Reticora-Event-ID":   env.ID,
		"X-Reticora-Sequence":   "42",
		"X-Reticora-Entity-Seq": "3",
		"X-Reticora-Delivery":   rec.ID,
		"X-Reticora-Event":      events.CICreated,
	} {
		if got := first[0].Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}

	// The redelivery by the retry worker keeps delivery ID and event.
	status.Store(http.StatusNoContent)
	d.now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	d.ProcessDue(context.Background())
	waitStatus(StatusDelivered)
	all := headersOf(2)
	if len(all) != 2 {
		t.Fatalf("HTTP attempts = %d, want 2", len(all))
	}
	for _, name := range []string{"X-Reticora-Delivery", "X-Reticora-Event-ID", "X-Reticora-Sequence", "X-Reticora-Entity-Seq"} {
		if all[1].Get(name) != all[0].Get(name) {
			t.Errorf("redelivery header %s = %q, first attempt %q", name, all[1].Get(name), all[0].Get(name))
		}
	}

	// An event without an entity carries no entity sequence.
	orgEvent := &events.Envelope{ID: "7a1c0de0-0000-4000-8000-000000000002", Type: events.CICreated, OrgID: "org-1",
		Sequence: 43, Data: json.RawMessage(`{"id":"ci-2"}`)}
	if err := d.Consume(context.Background(), orgEvent); err != nil {
		t.Fatal(err)
	}
	if h := headersOf(3); len(h) != 3 || h[2].Get("X-Reticora-Sequence") != "43" || h[2].Values("X-Reticora-Entity-Seq") != nil {
		t.Errorf("headers of an event without entity: %v", h)
	}
}

// failingDeliveries fails every Enqueue.
type failingDeliveries struct{ *MemoryDeliveryStore }

func (failingDeliveries) Enqueue(context.Context, *DeliveryRecord) (DeliveryRecord, error) {
	return DeliveryRecord{}, fmt.Errorf("delivery store down")
}

// failingSubscriptions fails the subscription lookup.
type failingSubscriptions struct{ *MemoryRepository }

func (failingSubscriptions) ListByEvent(context.Context, string, string) ([]Subscription, error) {
	return nil, fmt.Errorf("subscription store down")
}

// TestDispatcherConsumeReturnsHandOffErrors covers F-151 (WP-506): a
// failed subscription lookup or delivery persistence reaches the outbox
// publisher, which keeps the event pending.
func TestDispatcherConsumeReturnsHandOffErrors(t *testing.T) {
	repo := NewMemoryRepository()
	if err := repo.Create(context.Background(), &Subscription{OrganizationID: "org-1", Name: "hook", URL: "https://hook.example.invalid",
		Secret: "secret", Events: []string{events.CICreated}, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	env := &events.Envelope{ID: "7a1c0de0-0000-4000-8000-000000000003", Type: events.CICreated, OrgID: "org-1", Sequence: 1,
		Data: json.RawMessage(`{"id":"ci-1"}`)}
	for name, d := range map[string]*Dispatcher{
		"subscription lookup": NewDispatcher(failingSubscriptions{repo}, nil, DispatcherOptions{
			Deliveries: NewMemoryDeliveryStore(), PollInterval: -1}),
		"delivery persistence": NewDispatcher(repo, nil, DispatcherOptions{
			Deliveries: failingDeliveries{NewMemoryDeliveryStore()}, PollInterval: -1}),
	} {
		if err := d.Consume(context.Background(), env); err == nil || !strings.Contains(err.Error(), "down") {
			t.Errorf("%s fails: Consume returned %v, want the error", name, err)
		}
		_ = d.Shutdown(context.Background())
	}
}

// deliverOnce dispatches one event to the given subscriptions (Consume of
// env when set, else Dispatch) and returns the X-Webhook-Event headers of
// the deliveries per subscription name.
func deliverOnce(t *testing.T, subs []Subscription, opts DispatcherOptions, env *events.Envelope, event string, payload any) map[string][]string {
	t.Helper()
	var mu sync.Mutex
	got := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		mu.Lock()
		got[name] = append(got[name], r.Header.Get("X-Webhook-Event"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	repo := NewMemoryRepository()
	for _, sub := range subs {
		sub.OrganizationID, sub.URL, sub.Secret, sub.IsActive = "org-1", server.URL+"/"+sub.Name, "secret", true
		if err := repo.Create(context.Background(), &sub); err != nil {
			t.Fatal(err)
		}
	}
	opts.PollInterval = -1
	d := NewDispatcher(repo, server.Client(), opts)
	if env != nil {
		if err := d.Consume(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	} else {
		d.Dispatch(context.Background(), "org-1", event, payload)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = d.Shutdown(ctx)
	mu.Lock()
	defer mu.Unlock()
	return got
}

// TestLegacyNamesReceiveTheNewTypeOnce covers EVT-03 (WP-246): the
// subscriptions of inventory.movement and asset.movement receive a
// stock.movement of the outbox once, also one naming both former names
// and the new type; those of lifecycle.transitioned a lifecycle.changed.
// The delivery carries the new type.
func TestLegacyNamesReceiveTheNewTypeOnce(t *testing.T) {
	subs := []Subscription{
		{Name: "inventory", Events: []string{"inventory.movement"}},
		{Name: "asset", Events: []string{"asset.movement"}},
		{Name: "all", Events: []string{"inventory.movement", "asset.movement", events.StockMovement}},
		{Name: "stock", Events: []string{events.StockMovement}},
		{Name: "lifecycle", Events: []string{"lifecycle.transitioned"}},
		{Name: "both", Events: []string{"lifecycle.transitioned", events.LifecycleChanged}},
	}
	got := deliverOnce(t, subs, DispatcherOptions{}, &events.Envelope{ID: "e-1", Type: events.StockMovement, OrgID: "org-1",
		EntityID: "q-1", EntitySeq: 1, Data: json.RawMessage(`{"id":"m-1","item_kind":"quantity_item","quantity_item_id":"q-1","movement_type":"issue"}`)}, "", nil)
	want := map[string][]string{"inventory": {"stock.movement"}, "asset": {"stock.movement"}, "all": {"stock.movement"}, "stock": {"stock.movement"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("stock.movement delivered %v, want %v", got, want)
	}
	got = deliverOnce(t, subs, DispatcherOptions{}, &events.Envelope{ID: "e-2", Type: events.LifecycleChanged, OrgID: "org-1",
		EntityID: "a-1", EntitySeq: 1, Data: json.RawMessage(`{"entity_type":"asset","entity_id":"a-1","to":"in_use"}`)}, "", nil)
	want = map[string][]string{"lifecycle": {"lifecycle.changed"}, "both": {"lifecycle.changed"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("lifecycle.changed delivered %v, want %v", got, want)
	}
}

// TestBoundSubscriptionsReceiveP2EventsWithTheReadPermission covers EVT-03
// and RBA-08 (WP-246): per phase 2 type a subscription bound to an account
// holding the read permission of the type's object receives the event, one
// bound to an account holding every other permission does not.
// stock.movement needs asset:read, stock.below_minimum stock:read; the
// entity_type of lifecycle.changed decides between ci:read and asset:read,
// and without entity_type no bound subscription receives it.
func TestBoundSubscriptionsReceiveP2EventsWithTheReadPermission(t *testing.T) {
	const ci1, asset1, item1 = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	objects := fakeResolver{ci1: {ClientID: "c1"}, asset1: {ClientID: "c1"}, item1: {ClientID: "c1"}}
	permissions := []string{"ci:read", "asset:read", "stock:read", "contract:read", "ticket:read", "document:read", "import:run", "reservation:read", "monitoring:read",
		"maintenance:read", "workflow:read", "finding:read", "escalation:manage", "entitlement:read", "org:export",
		"ipam:read", "credential:read", "admp:read"}
	want := map[string]string{
		events.CIStatusChanged: "ci:read", events.RelationshipChanged: "ci:read",
		events.AssetCreated: "asset:read", events.AssetChanged: "asset:read", events.AssetDeleted: "asset:read",
		events.AssetAssigned: "asset:read", events.AssetReturned: "asset:read", events.AssetWarrantyExpiring: "asset:read",
		events.ContractExpiring: "contract:read", events.StockMovement: "asset:read", events.StockBelowMinimum: "stock:read",
		events.ReservationCreated: "reservation:read", events.ReservationReleased: "reservation:read",
		events.ReservationExpired: "reservation:read", events.ReservationConverted: "reservation:read",
		events.TicketUpdated: "ticket:read", events.TicketSLABreached: "ticket:read", events.MetamodelPublished: "ci:read",
		events.DocumentScanned: "document:read", events.ImportCompleted: "import:run",
	}
	// The types of phase 3 (WP-867, WP-868): the monitoring reads with
	// monitoring:read (ci.config_changed too, not with ci:read), the modules
	// with the read permission of their route.
	for _, typ := range events.Phase3Types {
		prefix, _, _ := strings.Cut(typ, ".")
		switch prefix {
		case "maintenance":
			want[typ] = "maintenance:read"
		case "workflow":
			want[typ] = "workflow:read"
		case "finding":
			want[typ] = "finding:read"
		case "notification":
			want[typ] = "escalation:manage"
		case "entitlement":
			want[typ] = "entitlement:read"
		case "org":
			want[typ] = "org:export"
		case "ipam", "asn":
			want[typ] = "ipam:read"
		case "credential":
			want[typ] = "credential:read"
		case "admp":
			want[typ] = "admp:read"
		default:
			want[typ] = "monitoring:read"
		}
	}
	type deliveryCase struct {
		event      string
		payload    map[string]any
		permission string
	}
	cases := []deliveryCase{
		{events.LifecycleChanged, map[string]any{"entity_type": "ci", "entity_id": ci1, "to": "retired"}, "ci:read"},
		{events.LifecycleChanged, map[string]any{"entity_type": "asset", "entity_id": asset1, "to": "in_use"}, "asset:read"},
	}
	for typ := range events.Payloads {
		if typ == events.LifecycleChanged {
			continue
		}
		payload := map[string]any{"id": asset1, "client_id": "c1"}
		switch typ {
		case events.RelationshipChanged:
			payload = map[string]any{"id": "r", "source_ci_id": ci1, "target_ci_id": ci1}
		case events.StockMovement:
			payload = map[string]any{"id": "m", "item_kind": "quantity_item", "quantity_item_id": item1, "movement_type": "issue"}
		case events.ReservationCreated, events.ReservationReleased, events.ReservationExpired, events.ReservationConverted:
			payload = map[string]any{"id": "r", "item_kind": "asset", "asset_id": asset1}
		case events.MetamodelPublished, events.DocumentScanned, events.ImportCompleted:
			payload = map[string]any{"id": "x"}
		}
		if EventReadPermission(typ) != want[typ] {
			t.Errorf("EventReadPermission(%s) = %q, want %q", typ, EventReadPermission(typ), want[typ])
		}
		cases = append(cases, deliveryCase{typ, payload, want[typ]})
	}
	for _, c := range cases {
		access := fakeAccess{}
		for _, p := range permissions {
			account := "sa-without"
			if p == c.permission {
				account = "sa-with"
			}
			access[account+"|"+p+"|c1"], access[account+"|"+p+"|"] = true, true
		}
		got := deliverOnce(t, []Subscription{
			{Name: "unbound", Events: []string{c.event}},
			{Name: "with", Events: []string{c.event}, ServiceAccountID: "sa-with"},
			{Name: "without", Events: []string{c.event}, ServiceAccountID: "sa-without"},
		}, DispatcherOptions{Access: access, Objects: objects}, nil, c.event, c.payload)
		if len(got["unbound"]) != 1 || len(got["with"]) != 1 || len(got["without"]) != 0 {
			t.Errorf("%s %v (needs %s): delivered %v, want the unbound subscription and the one with the permission", c.event, c.payload, c.permission, got)
		}
	}

	// Without entity_type the read permission is unknown: bound
	// subscriptions receive nothing, even with every permission.
	all := fakeAccess{}
	for _, p := range permissions {
		all["sa-all|"+p+"|c1"], all["sa-all|"+p+"|"], all["sa-all|"+p+"|*"] = true, true, true
	}
	got := deliverOnce(t, []Subscription{
		{Name: "unbound", Events: []string{events.LifecycleChanged}},
		{Name: "bound", Events: []string{events.LifecycleChanged}, ServiceAccountID: "sa-all"},
	}, DispatcherOptions{Access: all, Objects: objects}, nil, events.LifecycleChanged, map[string]any{"entity_id": ci1, "client_id": "c1", "to": "x"})
	if len(got["unbound"]) != 1 || len(got["bound"]) != 0 {
		t.Errorf("lifecycle.changed without entity_type delivered %v, want the unbound subscription only", got)
	}
	if EventDataReadPermission(events.LifecycleChanged, []byte(`{"entity_type":"location"}`)) != "" ||
		EventDataReadPermission(events.LifecycleChanged, []byte(`[]`)) != "" ||
		EventDataReadPermission("lifecycle.transitioned", []byte(`{"entity_type":"asset"}`)) != "asset:read" {
		t.Error("lifecycle read permission by entity_type")
	}
}
