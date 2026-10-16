//go:build auditdefect

// Phase-1 acceptance audit, group T6-epic-b – DEFECT test (must fail until fixed).
//
// WP-136 (EVT-01: at-least-once delivery; acceptance criterion "Webhooks
// konsumieren Outbox")
// Finding P2 "Webhook consumer drops events on transient errors while the
// outbox marks them published": Dispatcher.Consume (dispatcher.go) always
// returns nil – dispatchAs only logs a failed subscription lookup
// (ListByEvent) and a failed delivery persistence (Enqueue) – so
// events.Publisher marks the outbox row published and the webhook delivery is
// lost for good.
// Gap (high): "Webhook consumer durability through the publisher".
//
// Expected: while the hand-off fails, the outbox row stays unpublished
// (published_at NULL, attempts counted, last_error set); once the dependency
// recovers, the next publisher run creates exactly one delivery.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -run 'TestAuditDefectWebhookConsumer' ./internal/webhook/
package webhook_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
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

	appendEvent := func(name string) (eventID, entity string) {
		t.Helper()
		entity = f.CI(t, f.OrgA, "", name)
		scope := database.OrgWideScope(f.OrgA, "")
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			env, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: f.OrgA, Type: events.CICreated, EntityID: entity,
				Data: map[string]string{"id": entity, "name": name}})
			if err == nil {
				eventID = env.ID
			}
			return err
		}); err != nil {
			t.Fatalf("append outbox event: %v", err)
		}
		return eventID, entity
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
		eventID, entity := appendEvent("audit-e4-control")
		if _, err := pub.PublishPending(bg); err != nil {
			t.Logf("publisher run: %v", err)
		}
		if got := row(eventID); !got.Published {
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
			eventID, entity := appendEvent("audit-e4-" + strings.ReplaceAll(tc.name, " ", "-"))

			tc.fault.Store(true)
			_, pubErr := pub.PublishPending(bg)
			tc.fault.Store(false)
			failed := row(eventID)
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
			if got := row(eventID); !got.Published {
				t.Errorf("outbox row still unpublished after the dependency recovered: %+v", got)
			}
			if n := deliveries(entity); n != 1 {
				t.Errorf("webhook deliveries of the event after recovery = %d, want exactly 1", n)
			}
		})
	}
}
