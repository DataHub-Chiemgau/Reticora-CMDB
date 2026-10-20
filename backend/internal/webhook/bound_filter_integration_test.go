package webhook_test

// Webhook filter of subscriptions bound to a service account against
// PostgreSQL (WP-356, RBA-08; finding F-009): a bound subscription receives
// an event only when its account may read every object of the event. A
// payload without client_id/site_id is scoped by the CIs it references
// (ci_id, source_ci_id/target_ci_id, both CIs of a merge), soft-deleted and
// merged CIs included; an object without client is readable like a shared
// row under RLS; an event whose scope cannot be resolved reaches only
// unbound subscriptions and accounts that read org-wide.
//
// The ci.merged payload comes from a real merge through ci.PGRepository with
// the outbox (read back from event_outbox); the relationship.removed payload
// has exactly the fields relationship/cleanup.go emits. Deliveries go through
// Dispatcher.Consume, the production consumer of the outbox publisher, with
// the real user.ServiceAccountAccess and webhook.PGObjectResolver over PG.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run '^TestAuditT2BoundSubscriptionGetsOnlyReadableClientEvents$|^TestBoundSubscriptionScopeResolution$' ./internal/webhook/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// boundFixture is organization A with a role granting ci:read and service
// accounts holding it in client 1, in site 1 and org-wide.
type boundFixture struct {
	*scopetest.Fixture
	sa1, saSite, saAll string
	site1, site2       string
	// account creates a further service account holding the role in
	// clientID ("" org-wide).
	account func(name, clientID string) string
}

func newBoundFixture(t *testing.T, tag string) *boundFixture {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	f := scopetest.Seed(t, tag)
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)

	readRole := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-webhook-reader', '["ci:read"]')`, readRole, f.OrgA); err != nil {
		t.Fatal(err)
	}
	saRepo := user.NewPGServiceAccounts(f.App)
	// Assignments within the rights of an organization-wide administrator.
	admin := identity.Principal{OrganizationID: f.OrgA, Permissions: identity.AllPermissions()}
	newSA := func(name string, roles []user.ServiceAccountRole) string {
		t.Helper()
		sa := &user.ServiceAccount{OrganizationID: f.OrgA, Name: name}
		if err := saRepo.CreateServiceAccount(orgCtx, sa); err != nil {
			t.Fatalf("create service account %s: %v", name, err)
		}
		if _, err := saRepo.SetServiceAccountRoles(orgCtx, f.OrgA, sa.ID, &admin, roles); err != nil {
			t.Fatalf("roles of %s: %v", name, err)
		}
		return sa.ID
	}
	b := &boundFixture{Fixture: f}
	site := func(name string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`,
			f.Room(t, f.Client1, name)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	b.site1, b.site2 = site("t2-webhook-site-1"), site("t2-webhook-site-2")
	client1 := f.Client1
	b.sa1 = newSA("t2-webhook-client1", []user.ServiceAccountRole{{RoleID: readRole, ScopeClientID: &client1}})
	b.saSite = newSA("t2-webhook-site1", []user.ServiceAccountRole{{RoleID: readRole, ScopeSiteID: &b.site1}})
	b.saAll = newSA("t2-webhook-orgwide", []user.ServiceAccountRole{{RoleID: readRole}})
	b.account = func(name, clientID string) string {
		role := user.ServiceAccountRole{RoleID: readRole}
		if clientID != "" {
			role.ScopeClientID = &clientID
		}
		return newSA(name, []user.ServiceAccountRole{role})
	}
	return b
}

// ciIn inserts a CI of clientID ("" none) in siteID ("" none); the site
// is derived from the location (ci_derive_location).
func (b *boundFixture) ciIn(t *testing.T, clientID, siteID, name string) string {
	t.Helper()
	id := b.CI(t, b.OrgA, clientID, name)
	if siteID != "" {
		if _, err := b.Admin.Exec(context.Background(), `UPDATE ci SET location_id = $2 WHERE id = $1`, id, siteID); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// deliver hands one event to Dispatcher.Consume with a subscription of the
// event per subscriber (unbound, client1, site1, orgwide) and returns the
// deliveries per subscriber.
func (b *boundFixture) deliver(t *testing.T, event string, payload any) map[string]int {
	t.Helper()
	bg := context.Background()
	data, ok := payload.([]byte)
	if !ok {
		var err error
		if data, err = json.Marshal(payload); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	got := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[strings.TrimPrefix(r.URL.Path, "/")]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	repo := webhook.NewMemoryRepository()
	for _, sub := range []struct{ name, sa string }{{"unbound", ""}, {"client1", b.sa1}, {"site1", b.saSite}, {"orgwide", b.saAll}} {
		s := webhook.Subscription{OrganizationID: b.OrgA, Name: "t2-" + sub.name, URL: srv.URL + "/" + sub.name,
			Secret: "s", Events: []string{event}, IsActive: true, ServiceAccountID: sub.sa}
		if err := repo.Create(bg, &s); err != nil {
			t.Fatal(err)
		}
	}
	d := webhook.NewDispatcher(repo, srv.Client(), webhook.DispatcherOptions{Access: user.ServiceAccountAccess{Repo: user.NewPGServiceAccounts(b.App)},
		Objects: webhook.NewPGObjectResolver(b.App), PollInterval: -1})
	if err := d.Consume(bg, &events.Envelope{OrgID: b.OrgA, Type: event, Data: data}); err != nil {
		t.Fatalf("consume %s: %v", event, err)
	}
	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	_ = d.Shutdown(ctx)
	mu.Lock()
	defer mu.Unlock()
	return got
}

// mergedPayload merges victim into survivor through the CI repository with
// the outbox and returns the ci.merged payload of the outbox row.
func (b *boundFixture) mergedPayload(t *testing.T, survivor, victim string) []byte {
	t.Helper()
	bg := context.Background()
	if _, err := ci.NewPGRepository(b.App).WithEvents(events.NewOutbox()).Merge(b.OrgCtx(b.OrgA), b.OrgA, survivor, victim, b.ID()); err != nil {
		t.Fatalf("merge CIs: %v", err)
	}
	var merged []byte
	if err := b.Admin.QueryRow(bg, `SELECT data FROM event_outbox WHERE organization_id = $1 AND type = 'ci.merged' AND entity_id = $2 ORDER BY sequence DESC LIMIT 1`,
		b.OrgA, survivor).Scan(&merged); err != nil {
		t.Fatalf("read ci.merged from the outbox: %v", err)
	}
	return merged
}

// removedPayload has the fields relationship/cleanup.go emits.
func (b *boundFixture) removedPayload(source, target string) map[string]any {
	return map[string]any{"id": b.ID(), "source_ci_id": source, "target_ci_id": target,
		"rel_type": "depends_on", "source": "discovery", "reason": "stale",
		"last_confirmed_at": time.Now().UTC().Add(-100 * time.Hour).Format(time.RFC3339), "stale_after": "72h0m0s"}
}

// TestAuditT2BoundSubscriptionGetsOnlyReadableClientEvents: a subscription
// bound to a client-1 service account must not receive ci.merged or
// relationship.removed about client-2 CIs; an org-wide account receives
// them (control), and ci.created of a client-2 CI is already filtered
// (control).
func TestAuditT2BoundSubscriptionGetsOnlyReadableClientEvents(t *testing.T) {
	f := newBoundFixture(t, "ec")

	// Real merge of two client-2 CIs; the outbox row is the webhook input.
	survivor := f.CI(t, f.OrgA, f.Client2, "t2-webhook-c2-survivor")
	victim := f.CI(t, f.OrgA, f.Client2, "t2-webhook-c2-victim")
	merged := f.mergedPayload(t, survivor, victim)
	removed := f.removedPayload(survivor, f.CI(t, f.OrgA, f.Client2, "t2-webhook-c2-peer"))
	created := map[string]any{"id": victim, "client_id": f.Client2, "name": "t2-webhook-c2-victim"}

	for _, tc := range []struct {
		event   string
		payload any
	}{
		{events.CIMerged, merged},
		{events.RelationshipRemoved, removed},
		{events.CICreated, created},
	} {
		got := f.deliver(t, tc.event, tc.payload)
		if n := got["client1"]; n != 0 {
			t.Errorf("%s about client-2 CIs delivered %d time(s) to the subscription of a client-1 service account; want 0 (RBA-08: only objects the account may read)", tc.event, n)
		}
		if n := got["orgwide"]; n != 1 {
			t.Errorf("control: %s delivered %d time(s) to the org-wide service account, want 1", tc.event, n)
		}
	}
}

// TestBoundSubscriptionScopeResolution covers the scope sources of the
// filter: the payload's client_id/site_id, the referenced CIs (several
// objects, CIs without client or site, merged and hard-deleted victims) and
// events whose scope cannot be resolved.
func TestBoundSubscriptionScopeResolution(t *testing.T) {
	f := newBoundFixture(t, "f6")
	bg := context.Background()
	c1 := f.ciIn(t, f.Client1, "", "scope-c1")
	c1b := f.ciIn(t, f.Client1, "", "scope-c1b")
	c2 := f.ciIn(t, f.Client2, "", "scope-c2")
	shared := f.ciIn(t, "", "", "scope-shared")
	inSite1 := f.ciIn(t, f.Client1, f.site1, "scope-site1")
	inSite2 := f.ciIn(t, f.Client1, f.site2, "scope-site2")
	deleted := f.ciIn(t, f.Client2, "", "scope-c2-deleted")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatal(err)
	}

	type want struct{ unbound, client1, site1, orgwide int }
	cases := []struct {
		name    string
		event   string
		payload func() any
		want    want
	}{
		{"payload scope client 1", events.CICreated, func() any { return map[string]any{"id": c2, "client_id": f.Client1} }, want{1, 1, 1, 1}},
		{"payload scope client 2", events.CICreated, func() any { return map[string]any{"id": c1, "client_id": f.Client2} }, want{1, 0, 1, 1}},
		{"payload scope site 2", events.CIChanged, func() any { return map[string]any{"id": inSite1, "client_id": f.Client1, "site_id": f.site2} }, want{1, 1, 0, 1}},
		{"resolved CI of client 1", events.CIChanged, func() any { return map[string]any{"id": c1, "name": "x"} }, want{1, 1, 1, 1}},
		{"resolved CI of client 2", events.CIChanged, func() any { return map[string]any{"id": c2, "name": "x"} }, want{1, 0, 1, 1}},
		{"resolved CI without client", events.CIChanged, func() any { return map[string]any{"id": shared} }, want{1, 1, 1, 1}},
		{"resolved CI in site 1", events.CIChanged, func() any { return map[string]any{"id": inSite1} }, want{1, 1, 1, 1}},
		{"resolved CI in site 2", events.CIChanged, func() any { return map[string]any{"id": inSite2} }, want{1, 1, 0, 1}},
		{"soft-deleted CI of client 2", events.CIDeleted, func() any { return map[string]any{"id": strings.ToUpper(deleted)} }, want{1, 0, 1, 1}},
		{"relationship within client 1", events.RelationshipRemoved, func() any { return f.removedPayload(c1, c1b) }, want{1, 1, 1, 1}},
		{"relationship client 1 to client 2", events.RelationshipRemoved, func() any { return f.removedPayload(c1, c2) }, want{1, 0, 1, 1}},
		{"relationship client 1 to shared CI", events.RelationshipCreated, func() any { return f.removedPayload(c1, shared) }, want{1, 1, 1, 1}},
		{"relationship site 1 to site 2", events.RelationshipCreated, func() any { return f.removedPayload(inSite1, inSite2) }, want{1, 1, 0, 1}},
		{"merge within client 1", events.CIMerged, func() any {
			return f.mergedPayload(t, f.ciIn(t, f.Client1, "", "scope-m1-s"), f.ciIn(t, f.Client1, "", "scope-m1-v"))
		}, want{1, 1, 1, 1}},
		{"merge across clients", events.CIMerged, func() any {
			return f.mergedPayload(t, f.ciIn(t, f.Client1, "", "scope-mx-s"), f.ciIn(t, f.Client2, "", "scope-mx-v"))
		}, want{1, 0, 1, 1}},
		{"merge victim of client 2 hard-deleted (ci_merge snapshot)", events.CIMerged, func() any {
			survivor, victim := f.ciIn(t, f.Client1, "", "scope-mh-s"), f.ciIn(t, f.Client2, "", "scope-mh-v")
			p := f.mergedPayload(t, survivor, victim)
			if _, err := f.Admin.Exec(bg, `DELETE FROM ci WHERE id = $1`, victim); err != nil {
				t.Fatalf("hard-delete the victim: %v", err)
			}
			return p
		}, want{1, 0, 1, 1}},
		{"instance field of a client-2 CI", "ci_attribute.changed", func() any { return map[string]any{"ci_id": c2, "name": "rack"} }, want{1, 0, 1, 1}},
		{"attribute definition (organization)", "ci_attribute.changed", func() any { return map[string]any{"id": f.ID(), "key": "rack"} }, want{1, 1, 1, 1}},
		{"override of a client-2 CI", "override.changed", func() any { return map[string]any{"id": f.ID(), "ci_id": c2, "field_name": "name"} }, want{1, 0, 1, 1}},
		// Not resolvable: unbound and org-wide subscriptions only.
		{"unknown CI", events.CIChanged, func() any { return map[string]any{"id": f.ID()} }, want{1, 0, 0, 1}},
		{"CI id not a UUID", events.CIChanged, func() any { return map[string]any{"id": "not-a-uuid"} }, want{1, 0, 0, 1}},
		{"payload without scope and object", events.CIChanged, func() any { return map[string]any{"name": "x"} }, want{1, 0, 0, 1}},
		{"merge without victim", events.CIMerged, func() any { return map[string]any{"survivor_id": c1} }, want{1, 0, 0, 1}},
		{"relationship with unknown target", events.RelationshipRemoved, func() any { return f.removedPayload(c1, f.ID()) }, want{1, 0, 0, 1}},
		{"type without scope source", "lifecycle.transitioned", func() any { return map[string]any{"ci_id": c1, "client_id": f.Client1} }, want{1, 0, 0, 1}},
		{"not a JSON object", events.CIChanged, func() any { return []byte(`["` + c1 + `"]`) }, want{1, 0, 0, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.deliver(t, tc.event, tc.payload())
			if g := (want{got["unbound"], got["client1"], got["site1"], got["orgwide"]}); g != tc.want {
				t.Errorf("%s deliveries (unbound, client 1, site 1, org-wide) = %+v, want %+v", tc.event, g, tc.want)
			}
		})
	}
}
