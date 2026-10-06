package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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
		if len(records) == 1 && records[0].Status == StatusRetrying {
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
		if len(records) == 1 && records[0].Status == StatusSuccess {
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
	delivery, err := dispatcher.DeliverOnce(Subscription{ID: "s", OrganizationID: "org-1", URL: server.URL, Secret: "k", Events: []string{"ci.created"}, IsActive: true}, "ci.created", map[string]string{"x": "y"})
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

// fakeAccess grants read rights per service account, permission and client.
type fakeAccess map[string]bool

func (f fakeAccess) CanReadEvent(_ context.Context, _, serviceAccountID, permission, clientID, _ string) (bool, error) {
	return f[serviceAccountID+"|"+permission+"|"+clientID], nil
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
