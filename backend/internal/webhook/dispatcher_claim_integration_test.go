package webhook_test

// WP-503 (TEC-14, JOB-01): the webhook dispatcher claims the first attempt
// of a delivery in PostgreSQL like a retry claim, so with two replicas an
// outbox event handed to both (a redelivery, a second publisher) is sent
// once: the delivery is unique per event and subscription, and its lease
// keeps the retry worker of either replica from sending it in parallel.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestTwoDispatchersClaimFirstAttemptOnce' ./internal/webhook/

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// TestTwoDispatchersClaimFirstAttemptOnce: two dispatchers on the same
// database consume one event at the same time while both run their retry
// workers; the subscriber receives exactly one first delivery, stored once
// with one attempt.
func TestTwoDispatchersClaimFirstAttemptOnce(t *testing.T) {
	f := scopetest.Seed(t, "cd")
	bg := context.Background()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		// A slow subscriber keeps the first attempt in flight while the
		// other replica consumes the event and claims due deliveries.
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	repo := webhook.NewPGRepository(f.App)
	sub := &webhook.Subscription{OrganizationID: f.OrgA, Name: "wp503-two-dispatchers", URL: srv.URL + "/hook", Secret: "wp503-secret",
		Events: []string{events.CICreated}, IsActive: true}
	if err := repo.Create(f.OrgCtx(f.OrgA), sub); err != nil {
		t.Fatal(err)
	}
	newDispatcher := func() *webhook.Dispatcher {
		d := webhook.NewDispatcher(repo, srv.Client(), webhook.DispatcherOptions{Deliveries: webhook.NewPGDeliveryStore(f.App), PollInterval: -1})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(bg, 5*time.Second)
			defer cancel()
			_ = d.Shutdown(ctx)
		})
		return d
	}
	replicas := []*webhook.Dispatcher{newDispatcher(), newDispatcher()}

	entity := f.CI(t, f.OrgA, "", "wp503-two-dispatchers")
	var env *events.Envelope
	scope := database.OrgWideScope(f.OrgA, "")
	if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		env, err = events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: f.OrgA, Type: events.CICreated, EntityID: entity,
			Data: map[string]string{"id": entity, "name": "wp503-two-dispatchers"}})
		return err
	}); err != nil {
		t.Fatalf("append outbox event: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, d := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := d.Consume(bg, env); err != nil {
				t.Errorf("replica %d: consume: %v", i, err)
			}
			// The retry worker of the replica runs while the first attempt
			// is in flight.
			for range 5 {
				d.ProcessDue(bg)
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	close(start)
	wg.Wait()

	var n, attempt int
	var status string
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := f.Admin.QueryRow(bg, `SELECT count(*), COALESCE(max(attempt), 0), COALESCE(min(status), '')
			FROM webhook_delivery WHERE subscription_id = $1`, sub.ID).Scan(&n, &attempt, &status); err != nil {
			t.Fatal(err)
		}
		if status == webhook.StatusDelivered || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Let a parallel send arrive, if there were one.
	time.Sleep(500 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Errorf("first deliveries of one event by two dispatchers = %d, want exactly 1", got)
	}
	if n != 1 || attempt != 1 || status != webhook.StatusDelivered {
		t.Errorf("stored deliveries %d, attempt %d, status %q; want 1 delivered delivery after 1 attempt", n, attempt, status)
	}
}
