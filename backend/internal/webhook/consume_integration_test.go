package webhook_test

// WP-506 (EVT-01: at-least-once delivery; acceptance criterion "Webhooks
// konsumieren Outbox"; findings F-151, F-320): Dispatcher.Consume returns a
// failed subscription lookup (ListByEvent) and a failed delivery
// persistence (Enqueue), so events.Publisher keeps the outbox row pending
// (published_at NULL, attempts counted, last_error set) and hands the event
// over again once the dependency recovers; the deliveries are idempotent per
// event and subscription (unique index of migration 000169), so a repeated
// hand-off creates no second delivery and keeps the delivery ID.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestAuditDefectWebhookConsumer' ./internal/webhook/

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// flakySubscriptions fails the subscription lookup while fail is set.
type flakySubscriptions struct {
	*webhook.MemoryRepository
	fail atomic.Bool
}

func (r *flakySubscriptions) ListByEvent(ctx context.Context, orgID, event string) ([]webhook.Subscription, error) {
	if r.fail.Load() {
		return nil, errors.New("audit-e4: subscription store temporarily unavailable")
	}
	return r.MemoryRepository.ListByEvent(ctx, orgID, event)
}

// flakyDeliveries fails persisting a new delivery while fail is set.
type flakyDeliveries struct {
	*webhook.MemoryDeliveryStore
	fail atomic.Bool
}

func (s *flakyDeliveries) Enqueue(ctx context.Context, rec webhook.DeliveryRecord) (webhook.DeliveryRecord, error) {
	if s.fail.Load() {
		return webhook.DeliveryRecord{}, errors.New("audit-e4: delivery store temporarily unavailable")
	}
	return s.MemoryDeliveryStore.Enqueue(ctx, rec)
}

func TestAuditDefectWebhookConsumerKeepsEventsOnTransientErrors(t *testing.T) {
	f := scopetest.Seed(t, "e4")
	bg := context.Background()
	var mu sync.Mutex
	var received []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received = append(received, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	subs := &flakySubscriptions{MemoryRepository: webhook.NewMemoryRepository()}
	sub := &webhook.Subscription{OrganizationID: f.OrgA, Name: "audit-e4", URL: srv.URL + "/hook", Secret: "audit-e4-secret",
		Events: []string{events.CICreated}, IsActive: true}
	if err := subs.Create(bg, sub); err != nil {
		t.Fatal(err)
	}
	store := &flakyDeliveries{MemoryDeliveryStore: webhook.NewMemoryDeliveryStore()}
	d := webhook.NewDispatcher(subs, srv.Client(), webhook.DispatcherOptions{Deliveries: store, PollInterval: -1})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(bg, 2*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
	})
	pub := events.NewPublisher(f.App, nil, d)

	appendEvent := func(name string) (env *events.Envelope, entity string) {
		t.Helper()
		entity = f.CI(t, f.OrgA, "", name)
		scope := database.OrgWideScope(f.OrgA, "")
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			env, err = events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: f.OrgA, Type: events.CICreated, EntityID: entity,
				Data: map[string]string{"id": entity, "name": name}})
			return err
		}); err != nil {
			t.Fatalf("append outbox event: %v", err)
		}
		return env, entity
	}
	type outboxRow struct {
		Published bool
		Attempts  int
		LastError string
	}
	row := func(eventID string) outboxRow {
		t.Helper()
		var r outboxRow
		if err := f.Admin.QueryRow(bg, `SELECT published_at IS NOT NULL, attempts, COALESCE(last_error, '') FROM event_outbox WHERE id = $1`, eventID).
			Scan(&r.Published, &r.Attempts, &r.LastError); err != nil {
			t.Fatalf("outbox row %s: %v", eventID, err)
		}
		return r
	}
	deliveries := func(entity string) int {
		t.Helper()
		recs, _, err := store.ListBySubscription(bg, f.OrgA, sub.ID, api.PaginationParams{Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for i := range recs {
			if strings.Contains(string(recs[i].Payload), entity) {
				n++
			}
		}
		return n
	}

	t.Run("control: without a fault the event yields one delivery", func(t *testing.T) {
		env, entity := appendEvent("audit-e4-control")
		if _, err := pub.PublishPending(bg); err != nil {
			t.Logf("publisher run: %v", err)
		}
		if got := row(env.ID); !got.Published {
			t.Fatalf("control event not published: %+v", got)
		}
		if n := deliveries(entity); n != 1 {
			t.Fatalf("control deliveries = %d, want 1 (fixture broken)", n)
		}
	})

	for _, tc := range []struct {
		name  string
		fault *atomic.Bool
	}{
		{"subscription lookup fails", &subs.fail},
		{"delivery persistence fails", &store.fail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, entity := appendEvent("audit-e4-" + strings.ReplaceAll(tc.name, " ", "-"))

			tc.fault.Store(true)
			_, pubErr := pub.PublishPending(bg)
			tc.fault.Store(false)
			failed := row(env.ID)
			t.Logf("publisher run during the fault: err=%v, outbox row %+v, deliveries %d", pubErr, failed, deliveries(entity))
			if failed.Published {
				t.Errorf("outbox row marked published although the webhook hand-off failed (%s): the event is lost (EVT-01 at-least-once)", tc.name)
			}
			if failed.Attempts < 1 || failed.LastError == "" {
				t.Errorf("failed hand-off not recorded on the outbox row: attempts %d, last_error %q", failed.Attempts, failed.LastError)
			}

			if _, err := pub.PublishPending(bg); err != nil {
				t.Logf("publisher run after recovery: %v", err)
			}
			if got := row(env.ID); !got.Published {
				t.Errorf("outbox row still unpublished after the dependency recovered: %+v", got)
			}
			if n := deliveries(entity); n != 1 {
				t.Errorf("webhook deliveries of the event after recovery = %d, want exactly 1", n)
			}
		})
	}

	// The PostgreSQL store: a repeated hand-off of an event (the publisher
	// failed after the dispatcher stored the delivery) finds the delivery
	// by its unique index and stores and sends nothing new; the delivery
	// carries the event and the deterministic delivery ID (F-320).
	t.Run("postgres store: a repeated hand-off adds no delivery", func(t *testing.T) {
		repo := webhook.NewPGRepository(f.App)
		pgSub := &webhook.Subscription{OrganizationID: f.OrgA, Name: "audit-e4-pg", URL: srv.URL + "/pg", Secret: "audit-e4-secret",
			Events: []string{events.CICreated}, IsActive: true}
		if err := repo.Create(f.OrgCtx(f.OrgA), pgSub); err != nil {
			t.Fatal(err)
		}
		pgStore := webhook.NewPGDeliveryStore(f.App)
		pd := webhook.NewDispatcher(repo, srv.Client(), webhook.DispatcherOptions{Deliveries: pgStore, PollInterval: -1})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(bg, 2*time.Second)
			defer cancel()
			_ = pd.Shutdown(ctx)
		})
		env, _ := appendEvent("audit-e4-pg")
		for i := 0; i < 2; i++ {
			if err := pd.Consume(bg, env); err != nil {
				t.Fatalf("hand-off %d: %v", i+1, err)
			}
		}

		var n int
		var id, eventID string
		var seq, entitySeq int64
		if err := f.Admin.QueryRow(bg, `SELECT count(*), min(id::text), min(event_id::text), min(event_sequence), min(event_entity_seq)
			FROM webhook_delivery WHERE subscription_id = $1`, pgSub.ID).Scan(&n, &id, &eventID, &seq, &entitySeq); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("deliveries after two hand-offs of one event = %d, want 1", n)
		}
		if eventID != env.ID || seq != env.Sequence || entitySeq != env.EntitySeq {
			t.Errorf("stored event = %s/%d/%d, want %s/%d/%d", eventID, seq, entitySeq, env.ID, env.Sequence, env.EntitySeq)
		}

		var got http.Header
		deadline := time.Now().Add(2 * time.Second)
		for got == nil && time.Now().Before(deadline) {
			mu.Lock()
			for _, h := range received {
				if h.Get("X-Reticora-Event-ID") == env.ID && h.Get("X-Reticora-Delivery") == id {
					got = h
				}
			}
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
		}
		if got == nil {
			t.Fatalf("no delivery of event %s with delivery ID %s received", env.ID, id)
		}
		if got.Get("X-Reticora-Sequence") != strconv.FormatInt(env.Sequence, 10) ||
			got.Get("X-Reticora-Entity-Seq") != strconv.FormatInt(env.EntitySeq, 10) {
			t.Errorf("sequence headers = %q/%q, want %d/%d", got.Get("X-Reticora-Sequence"), got.Get("X-Reticora-Entity-Seq"),
				env.Sequence, env.EntitySeq)
		}
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		sent := 0
		for _, h := range received {
			if h.Get("X-Reticora-Event-ID") == env.ID {
				sent++
			}
		}
		mu.Unlock()
		if sent != 1 {
			t.Errorf("HTTP deliveries of the event = %d, want 1", sent)
		}
	})
}
