//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect test of the webhook
// filter of subscriptions bound to a service account, against PostgreSQL.
//
//   - WP-069 (RBA-08, RBA-07; tenant/client isolation), finding P2 "Webhook
//     filter delivers merge and relationship events of other clients to
//     client-scoped service accounts"; gap (critical) "End-to-end event
//     filtering with real payloads and real SA grants". eventObjectScope
//     (dispatcher.go) reads only top-level client_id/site_id; ci.merged
//     (ci/merge.go) and relationship.removed (relationship/cleanup.go) carry
//     neither, and CanReadEvent treats "" as an org-wide object, so a
//     client-1 account receives events about client-2 CIs.
//
// The ci.merged payload comes from a real merge of two client-2 CIs through
// ci.PGRepository with the outbox (read back from event_outbox); the
// relationship.removed payload has exactly the fields relationship/cleanup.go
// emits. Deliveries go through Dispatcher.Consume, the production consumer of
// the outbox publisher, with the real user.ServiceAccountAccess over PG.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_02?sslmode=disable go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./internal/webhook/
package webhook_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// TestAuditT2BoundSubscriptionGetsOnlyReadableClientEvents: a subscription
// bound to a client-1 service account must not receive ci.merged or
// relationship.removed about client-2 CIs; an org-wide account receives
// them (control), and ci.created of a client-2 CI is already filtered
// (control).
func TestAuditT2BoundSubscriptionGetsOnlyReadableClientEvents(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("TEST_DATABASE_URL is required for this audit test")
	}
	f := scopetest.Seed(t, "ec")
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)

	readRole := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-webhook-reader', '["ci:read"]')`, readRole, f.OrgA); err != nil {
		t.Fatal(err)
	}
	saRepo := user.NewPGServiceAccounts(f.App)
	newSA := func(name string, roles []user.ServiceAccountRole) string {
		t.Helper()
		sa := &user.ServiceAccount{OrganizationID: f.OrgA, Name: name}
		if err := saRepo.CreateServiceAccount(orgCtx, sa); err != nil {
			t.Fatalf("create service account %s: %v", name, err)
		}
		if _, err := saRepo.SetServiceAccountRoles(orgCtx, f.OrgA, sa.ID, roles); err != nil {
			t.Fatalf("roles of %s: %v", name, err)
		}
		return sa.ID
	}
	client1 := f.Client1
	sa1 := newSA("t2-webhook-client1", []user.ServiceAccountRole{{RoleID: readRole, ScopeClientID: &client1}})
	saAll := newSA("t2-webhook-orgwide", []user.ServiceAccountRole{{RoleID: readRole}})

	// Real merge of two client-2 CIs; the outbox row is the webhook input.
	survivor := f.CI(t, f.OrgA, f.Client2, "t2-webhook-c2-survivor")
	victim := f.CI(t, f.OrgA, f.Client2, "t2-webhook-c2-victim")
	if _, err := ci.NewPGRepository(f.App).WithEvents(events.NewOutbox()).Merge(orgCtx, f.OrgA, survivor, victim, f.ID()); err != nil {
		t.Fatalf("merge client-2 CIs: %v", err)
	}
	var merged []byte
	if err := f.Admin.QueryRow(bg, `SELECT data FROM event_outbox WHERE organization_id = $1 AND type = 'ci.merged' AND entity_id = $2 ORDER BY sequence DESC LIMIT 1`,
		f.OrgA, survivor).Scan(&merged); err != nil {
		t.Fatalf("read ci.merged from the outbox: %v", err)
	}
	removed, _ := json.Marshal(map[string]any{"id": f.ID(), "source_ci_id": survivor, "target_ci_id": f.CI(t, f.OrgA, f.Client2, "t2-webhook-c2-peer"),
		"rel_type": "depends_on", "source": "discovery", "reason": "stale",
		"last_confirmed_at": time.Now().UTC().Add(-100 * time.Hour).Format(time.RFC3339), "stale_after": "72h0m0s"})
	created, _ := json.Marshal(map[string]any{"id": victim, "client_id": f.Client2, "name": "t2-webhook-c2-victim"})

	var mu sync.Mutex
	got := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	repo := webhook.NewMemoryRepository()
	types := []string{events.CIMerged, events.RelationshipRemoved, events.CICreated}
	for _, sub := range []struct{ name, sa string }{{"client1", sa1}, {"orgwide", saAll}} {
		for _, ev := range types {
			s := webhook.Subscription{OrganizationID: f.OrgA, Name: "t2-" + sub.name + "-" + ev, URL: srv.URL + "/" + sub.name + "/" + ev,
				Secret: "s", Events: []string{ev}, IsActive: true, ServiceAccountID: sub.sa}
			if err := repo.Create(bg, &s); err != nil {
				t.Fatal(err)
			}
		}
	}
	d := webhook.NewDispatcher(repo, srv.Client(), webhook.DispatcherOptions{Access: user.ServiceAccountAccess{Repo: saRepo}, PollInterval: -1})
	for _, env := range []*events.Envelope{
		{OrgID: f.OrgA, Type: events.CIMerged, Data: merged},
		{OrgID: f.OrgA, Type: events.RelationshipRemoved, Data: removed},
		{OrgID: f.OrgA, Type: events.CICreated, Data: created},
	} {
		if err := d.Consume(bg, env); err != nil {
			t.Fatalf("consume %s: %v", env.Type, err)
		}
	}
	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	_ = d.Shutdown(ctx)

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range types {
		if n := got["/client1/"+ev]; n != 0 {
			t.Errorf("%s about client-2 CIs delivered %d time(s) to the subscription of a client-1 service account; want 0 (RBA-08: only objects the account may read)", ev, n)
		}
		if n := got["/orgwide/"+ev]; n != 1 {
			t.Errorf("control: %s delivered %d time(s) to the org-wide service account, want 1", ev, n)
		}
	}
}
