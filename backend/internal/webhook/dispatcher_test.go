package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

	if err := repo.Create(&Subscription{OrganizationID: "org-1", Name: "hook", URL: server.URL, Secret: "secret", Events: []string{"ci.created"}, IsActive: true}); err != nil {
		t.Fatal(err)
	}

	dispatcher := NewDispatcher(repo, server.Client())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	}()

	dispatcher.Dispatch("org-1", "ci.created", map[string]any{"id": "ci-1"})
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
