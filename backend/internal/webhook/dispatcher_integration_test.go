package webhook_test

// End-to-end webhook filter against PostgreSQL and NATS (WP-356, RBA-08;
// finding F-009): real outbox events run through events.Publisher (stream
// RETICORA_EVENTS) and Dispatcher.Consume to an httptest receiver. Bound
// subscriptions receive exactly the events whose CIs their service account
// may read.
//
// Retest (needs a migrated database and NATS with JetStream):
//
//	cd backend && TEST_DATABASE_URL=… TEST_NATS_URL=nats://localhost:4222 go test -count=1 -v -run '^TestDispatcherDeliversOutboxEventsByReadRights$' ./internal/webhook/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// TestDispatcherDeliversOutboxEventsByReadRights: ci.updated (ci.changed),
// ci.merged, relationship.created and relationship.removed about CIs of
// client 1 and client 2 reach the client-1 account exactly with the client-1
// events, the client-2 account exactly with the client-2 events, and the
// unbound and the org-wide subscription with all of them; a merge across
// both clients reaches only the org-wide receivers, a merge of two client-1
// CIs the client-1 account, and ci.updated of a CI without client both
// client accounts.
func TestDispatcherDeliversOutboxEventsByReadRights(t *testing.T) {
	f := newBoundFixture(t, "6b")
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	nc, connErr := nats.Connect(url)
	if connErr != nil {
		t.Fatal(connErr)
	}
	defer nc.Close()
	js, jsErr := jetstream.New(nc)
	if jsErr != nil {
		t.Fatal(jsErr)
	}
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	sa2 := f.account("t2-webhook-client2", f.Client2)

	// Receiver: event type and object key per subscriber.
	var mu sync.Mutex
	got := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		key, _ := body["id"].(string)
		if survivor, ok := body["survivor_id"].(string); ok {
			key = survivor
		}
		mu.Lock()
		got[strings.TrimPrefix(r.URL.Path, "/")] = append(got[strings.TrimPrefix(r.URL.Path, "/")], r.Header.Get("X-Reticora-Event")+" "+key)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	repo := webhook.NewMemoryRepository()
	subscribed := []string{"ci.updated", events.CIMerged, events.RelationshipCreated, events.RelationshipRemoved}
	for _, sub := range []struct{ name, sa string }{{"unbound", ""}, {"client1", f.sa1}, {"client2", sa2}, {"orgwide", f.saAll}} {
		s := webhook.Subscription{OrganizationID: f.OrgA, Name: "e2e-" + sub.name, URL: srv.URL + "/" + sub.name,
			Secret: "s", Events: subscribed, IsActive: true, ServiceAccountID: sub.sa}
		if err := repo.Create(bg, &s); err != nil {
			t.Fatal(err)
		}
	}
	d := webhook.NewDispatcher(repo, srv.Client(), webhook.DispatcherOptions{Access: user.ServiceAccountAccess{Repo: user.NewPGServiceAccounts(f.App)},
		Objects: webhook.NewPGObjectResolver(f.App), PollInterval: -1})

	// CIs of client 1, client 2 and without client.
	cis := ci.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	c1, c1b := f.CI(t, f.OrgA, f.Client1, "e2e-c1"), f.CI(t, f.OrgA, f.Client1, "e2e-c1b")
	c2, c2b := f.CI(t, f.OrgA, f.Client2, "e2e-c2"), f.CI(t, f.OrgA, f.Client2, "e2e-c2b")
	shared := f.CI(t, f.OrgA, "", "e2e-shared")

	// ci.changed (subscribed as ci.updated).
	for _, id := range []string{c1, c2, shared} {
		name := "e2e-renamed-" + id[len(id)-4:]
		if _, err := cis.Update(orgCtx, f.OrgA, id, ci.UpdateRequest{Name: &name}); err != nil {
			t.Fatalf("update CI %s: %v", id, err)
		}
	}
	// ci.merged within client 1, within client 2 and across both clients.
	merge := func(survivor, victim string) {
		t.Helper()
		if _, err := cis.Merge(orgCtx, f.OrgA, survivor, victim, f.ID()); err != nil {
			t.Fatalf("merge %s into %s: %v", victim, survivor, err)
		}
	}
	m1s, m2s, mxs := f.CI(t, f.OrgA, f.Client1, "e2e-m1-s"), f.CI(t, f.OrgA, f.Client2, "e2e-m2-s"), f.CI(t, f.OrgA, f.Client1, "e2e-mx-s")
	merge(m1s, f.CI(t, f.OrgA, f.Client1, "e2e-m1-v"))
	merge(m2s, f.CI(t, f.OrgA, f.Client2, "e2e-m2-v"))
	merge(mxs, f.CI(t, f.OrgA, f.Client2, "e2e-mx-v"))
	// Discovery edges within each client: relationship.created is written
	// to the outbox with the edge, relationship.removed by the cleanup run.
	edge := func(source, target string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source, last_confirmed_at)
			VALUES ($1, $2, $3, 'depends_on', 'discovery', now() - interval '1 day') RETURNING id::text`, f.OrgA, source, target).Scan(&id); err != nil {
			t.Fatalf("insert edge: %v", err)
		}
		scope := database.OrgWideScope(f.OrgA, "")
		if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			_, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: f.OrgA, Type: events.RelationshipCreated, EntityID: id,
				Data: map[string]any{"id": id, "source_ci_id": source, "target_ci_id": target, "rel_type": "depends_on", "source": "discovery"}})
			return err
		}); err != nil {
			t.Fatalf("append relationship.created: %v", err)
		}
		return id
	}
	r1, r2 := edge(c1, c1b), edge(c2, c2b)
	removed, cleanErr := relationship.NewCleanup(f.App).WithClock(func() time.Time { return time.Now().UTC().Add(30 * 24 * time.Hour) }).RunOrg(bg, f.OrgA)
	if cleanErr != nil || removed.Stale != 2 {
		t.Fatalf("cleanup removed %+v, %v; want both discovery edges", removed, cleanErr)
	}

	// Outbox -> NATS and webhooks.
	pub := events.NewPublisher(f.App, js, d)
	if err := pub.Setup(bg); err != nil {
		t.Fatal(err)
	}
	if _, err := pub.PublishPending(bg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	var pending int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND published_at IS NULL`, f.OrgA).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("unpublished outbox rows of organization A: %d, %v", pending, err)
	}
	ctx, cancel := context.WithTimeout(bg, 10*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	ev := func(event, key string) string { return event + " " + key }
	client1 := []string{ev("ci.changed", c1), ev("ci.changed", shared), ev("ci.merged", m1s),
		ev("relationship.created", r1), ev("relationship.removed", r1)}
	client2 := []string{ev("ci.changed", c2), ev("ci.changed", shared), ev("ci.merged", m2s),
		ev("relationship.created", r2), ev("relationship.removed", r2)}
	all := []string{ev("ci.changed", c1), ev("ci.changed", c2), ev("ci.changed", shared),
		ev("ci.merged", m1s), ev("ci.merged", m2s), ev("ci.merged", mxs),
		ev("relationship.created", r1), ev("relationship.created", r2), ev("relationship.removed", r1), ev("relationship.removed", r2)}
	want := map[string][]string{"unbound": all, "orgwide": all, "client1": client1, "client2": client2}

	mu.Lock()
	defer mu.Unlock()
	for name, w := range want {
		g := append([]string(nil), got[name]...)
		sort.Strings(g)
		w = append([]string(nil), w...)
		sort.Strings(w)
		if !reflect.DeepEqual(g, w) {
			t.Errorf("subscriber %s received\n  %v\nwant\n  %v", name, g, w)
		}
	}
}
