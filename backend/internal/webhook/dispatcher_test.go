package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		if len(records) == 1 && records[0].Status == StatusFailed {
			if records[0].NextRetryAt != nil || records[0].Error == "" {
				t.Fatalf("unexpected failed delivery record: %+v", records[0])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("delivery was not marked failed after exhausting its attempts")
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
