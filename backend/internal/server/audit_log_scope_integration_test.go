package server_test

// Audit list scope of route entries (WP-660, k7n-audit-log-scope, F-389;
// WP-016 AK4, TEN-06, TEN-09, AUD-05): through the production router with
// the PostgreSQL route auditor, the route audit writes the first route
// segment as resource_type ("racks", "relationships", "assets"). A principal
// restricted to client 1, or to client 1 and one of its sites, with
// audit:read lists only the entries about objects it may read; an org-wide
// principal lists all of them.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… \
//	  go test -count=1 -v -run '^TestAuditLogHidesForeignClientRouteEntries$' ./internal/server/

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/jackc/pgx/v5"
)

// TestAuditLogHidesForeignClientRouteEntries is the coding test of F-389:
// on e5c2d86 the client-1 principal lists the racks.create/racks.update,
// relationships.create and assets.create entries of client-2 objects with
// their names and old and new values, which GET of the objects answers 404,
// and likewise the entries of the module routes (tickets, documents,
// maintenance-windows, slas, desks, keys, orders, consumables, inventory,
// forms, stock-movements). Now it sees only the entries of client-1
// objects, the client-1/site-1 principal only those of objects in site 1,
// neither sees an entry of an unknown resource type (fail-closed), on
// GET /api/v1/audit-log and on its alias GET /api/v1/audit, and the
// org-wide principal sees every entry (control).
func TestAuditLogHidesForeignClientRouteEntries(t *testing.T) {
	e := t6NewEnv(t, "6b", nil)
	f := e.F
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "als-admin"), identity.AllPermissions()}
	room1, room1b, room2 := f.Room(t, f.Client1, "als-room-c1"), f.Room(t, f.Client1, "als-room-c1b"), f.Room(t, f.Client2, "als-room-c2")
	var site1 string
	if err := f.Admin.QueryRow(t.Context(), `SELECT site_id::text FROM room WHERE id = $1`, room1).Scan(&site1); err != nil {
		t.Fatal(err)
	}
	created := func(path, body string) string {
		t.Helper()
		r := e.Call(t, admin, http.MethodPost, path, body)
		id := t6ID(r.Body)
		if r.Code != http.StatusCreated || id == "" {
			t.Fatalf("POST %s = %d %s", path, r.Code, t6Short(r.Body))
		}
		return id
	}
	rack1 := created("/api/v1/racks", fmt.Sprintf(`{"name":"als-rack-c1","room_id":%q}`, room1))
	rack1b := created("/api/v1/racks", fmt.Sprintf(`{"name":"als-rack-c1-site-b","room_id":%q}`, room1b))
	rack2 := created("/api/v1/racks", fmt.Sprintf(`{"name":"als-rack-c2","room_id":%q}`, room2))
	if r := e.Call(t, admin, http.MethodPatch, "/api/v1/racks/"+rack2, `{"name":"als-rack-c2-renamed-secret"}`); r.Code != http.StatusOK {
		t.Fatalf("PATCH rack of client 2 = %d %s", r.Code, t6Short(r.Body))
	}
	ciA, ciB := f.CI(t, f.OrgA, f.Client2, "als-ci-c2-a"), f.CI(t, f.OrgA, f.Client2, "als-ci-c2-b")
	rel2 := created("/api/v1/relationships", fmt.Sprintf(`{"source_ci_id":%q,"target_ci_id":%q,"rel_type":"depends_on"}`, ciA, ciB))
	asset1 := created("/api/v1/assets", fmt.Sprintf(`{"asset_tag":"als-asset-c1","name":"als-asset-c1","client_id":%q}`, f.Client1))
	asset2 := created("/api/v1/assets", fmt.Sprintf(`{"asset_tag":"als-asset-c2","name":"als-asset-c2-secret","client_id":%q}`, f.Client2))

	// Objects of the modules (second verifier of F-389): client 2 owns one
	// per table, client 1 an SLA and a key (control); the org-wide admin
	// changes them through the API, so the route audit writes entries of
	// the types tickets, documents, maintenance-windows, slas, desks, keys,
	// orders, consumables, inventory, forms and stock-movements.
	scalar := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(t.Context(), q, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return id
	}
	ticket2 := scalar(`INSERT INTO ticket (organization_id, title, reporter_id, related_ci_id) VALUES ($1, 'als-c2-ticket', $2, $3)
		RETURNING id::text`, f.OrgA, admin.User, ciA)
	document2 := scalar(`INSERT INTO document (organization_id, title, file_name, uploaded_by, storage_key)
		VALUES ($1, 'als-c2-doc', 'als-c2.pdf', $2, '') RETURNING id::text`, f.OrgA, admin.User)
	scalar(`INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3) RETURNING id::text`,
		f.OrgA, document2, ciA)
	window2 := scalar(`INSERT INTO maintenance_window (organization_id, title, starts_at, ends_at)
		VALUES ($1, 'als-c2-mw', now(), now() + interval '1 hour') RETURNING id::text`, f.OrgA)
	scalar(`INSERT INTO maintenance_window_ci (organization_id, maintenance_window_id, ci_id) VALUES ($1, $2, $3) RETURNING maintenance_window_id::text`,
		f.OrgA, window2, ciA)
	sla := func(client, name string) string {
		return scalar(`INSERT INTO sla (organization_id, client_id, name, priority, response_target_minutes, resolution_target_minutes)
			VALUES ($1, $2, $3, 'high', 30, 60) RETURNING id::text`, f.OrgA, client, name)
	}
	sla1, sla2 := sla(f.Client1, "als-c1-sla"), sla(f.Client2, "als-c2-sla")
	desk2 := scalar(`INSERT INTO desk (organization_id, room_id, name) VALUES ($1, $2, 'als-c2-desk') RETURNING id::text`, f.OrgA, room2)
	key := func(client, name string) string {
		return scalar(`INSERT INTO key_item (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`, f.OrgA, client, name)
	}
	key1, key2 := key(f.Client1, "als-c1-key"), key(f.Client2, "als-c2-key")
	order2 := scalar(`INSERT INTO internal_order (organization_id, client_id, order_number, title) VALUES ($1, $2, 'als-c2-ord', 'als-c2-order')
		RETURNING id::text`, f.OrgA, f.Client2)
	form2 := scalar(`INSERT INTO form_def (organization_id, client_id, name, schema) VALUES ($1, $2, 'als-c2-form', '{"type":"object"}')
		RETURNING id::text`, f.OrgA, f.Client2)
	consumable2 := scalar(`INSERT INTO consumable (organization_id, client_id, name) VALUES ($1, $2, 'als-c2-cons') RETURNING id::text`, f.OrgA, f.Client2)
	item2 := scalar(`INSERT INTO quantity_item (organization_id, client_id, name) VALUES ($1, $2, 'als-c2-qty') RETURNING id::text`, f.OrgA, f.Client2)
	modules := map[string]string{ticket2: "ticket of client 2", document2: "document linked to a client-2 CI",
		window2: "maintenance window of a client-2 CI", sla2: "SLA of client 2", desk2: "desk in a room of client 2",
		key2: "key of client 2", order2: "order of client 2", form2: "form of client 2", consumable2: "consumable of client 2",
		item2: "inventory item of client 2"}
	for _, w := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/v1/tickets/" + ticket2, `{"title":"als-c2-ticket-secret"}`},
		{http.MethodPost, "/api/v1/tickets/" + ticket2 + "/comments", `{"content":"als-c2-comment-secret"}`},
		{http.MethodPatch, "/api/v1/documents/" + document2, `{"title":"als-c2-doc-secret"}`},
		{http.MethodPatch, "/api/v1/maintenance-windows/" + window2, `{"title":"als-c2-mw-secret"}`},
		{http.MethodPatch, "/api/v1/slas/" + sla1, `{"name":"als-c1-sla-upd"}`},
		{http.MethodPatch, "/api/v1/slas/" + sla2, `{"name":"als-c2-sla-secret"}`},
		{http.MethodPatch, "/api/v1/desks/" + desk2, `{"name":"als-c2-desk-secret"}`},
		{http.MethodPatch, "/api/v1/keys/" + key1, `{"name":"als-c1-key-upd"}`},
		{http.MethodPatch, "/api/v1/keys/" + key2, `{"name":"als-c2-key-secret"}`},
		{http.MethodPatch, "/api/v1/orders/" + order2, `{"title":"als-c2-order-secret"}`},
		{http.MethodPatch, "/api/v1/forms/" + form2, `{"name":"als-c2-form-secret"}`},
		{http.MethodPatch, "/api/v1/consumables/" + consumable2, `{"name":"als-c2-cons-secret"}`},
		{http.MethodPatch, "/api/v1/inventory/items/" + item2, `{"name":"als-c2-qty-secret"}`},
	} {
		if r := e.Call(t, admin, w.method, w.path, w.body); r.Code != http.StatusOK && r.Code != http.StatusCreated {
			t.Fatalf("%s %s = %d %s", w.method, w.path, r.Code, t6Short(r.Body))
		}
	}
	movement2 := created("/api/v1/stock-movements", fmt.Sprintf(`{"movement_type":"correction","asset_id":%q,"reason":"als-c2-move-secret"}`, asset2))
	modules[movement2] = "stock movement of a client-2 asset"

	// An entry of a resource type no auditor writes yet: hidden from
	// restricted principals (fail-closed).
	unknownID := f.ID()
	scope := database.OrgWideScope(f.OrgA, "")
	if err := database.WithTenant(context.Background(), f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		_, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: f.OrgA, Action: "zz-new-kind.create",
			ResourceType: "zz-new-kind", ResourceID: unknownID, Changes: map[string]any{"name": "als-unknown-secret"}})
		return err
	}); err != nil {
		t.Fatalf("record entry of an unknown type: %v", err)
	}

	all := identity.ScopeSet{All: true}
	type listing struct {
		total   int
		entries []audit.Entry
	}
	readers := 0
	list := func(name string, s identity.Scope, path string) listing {
		t.Helper()
		readers++
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p := identity.Principal{Subject: f.AppUser(t, f.OrgA, fmt.Sprintf("als-%s-%d", name, readers)), OrganizationID: f.OrgA,
			Type: identity.PrincipalTypeUser, Scope: &s, Permissions: []identity.Permission{identity.PermAuditRead}, Credential: "als"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		e.handler.ServeHTTP(w, req.WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s as %s = %d %s", path, name, w.Code, t6Short(w.Body.Bytes()))
		}
		var body struct {
			Data  []audit.Entry `json:"data"`
			Total int           `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Total != len(body.Data) {
			t.Errorf("GET %s as %s: total %d, but %d entries listed", path, name, body.Total, len(body.Data))
		}
		return listing{total: body.Total, entries: body.Data}
	}
	about := func(l listing) map[string][]string {
		out := map[string][]string{}
		for _, en := range l.entries {
			out[en.ResourceID] = append(out[en.ResourceID], en.Action)
		}
		return out
	}
	foreign := alsMerge(modules, map[string]string{rack2: "rack of client 2", rel2: "relationship between client-2 CIs",
		asset2: "asset of client 2", ciA: "CI of client 2", unknownID: "object of an unknown resource type"})

	for _, c := range []struct {
		name   string
		scope  identity.Scope
		see    map[string]string
		notSee map[string]string
	}{
		{"org", identity.Scope{Clients: all, Sites: all, Teams: all},
			alsMerge(modules, map[string]string{rack1: "rack of client 1", rack1b: "rack of client 1 in site b", rack2: "rack of client 2",
				rel2: "relationship of client 2", asset1: "asset of client 1", asset2: "asset of client 2", unknownID: "unknown type"}), nil},
		{"client1", identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: all, Teams: all},
			map[string]string{rack1: "rack of client 1", rack1b: "rack of client 1 in site b", asset1: "asset of client 1",
				sla1: "SLA of client 1", key1: "key of client 1"}, foreign},
		// A site grant names the client of its site (WP-390, F-014).
		{"client1-site1", identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: identity.ScopeSet{IDs: []string{site1}}, Teams: all},
			map[string]string{rack1: "rack of client 1 in site 1"},
			alsMerge(foreign, map[string]string{rack1b: "rack of client 1 in another site"})},
	} {
		// The catalog path and the deprecated alias (API-01) share the
		// handler.
		for _, base := range []string{"/api/v1/audit-log", "/api/v1/audit"} {
			t.Run(c.name+base, func(t *testing.T) {
				l := list(c.name, c.scope, base+"?limit=500")
				got := about(l)
				for id, what := range c.see {
					if len(got[id]) == 0 {
						t.Errorf("%s does not see the entries about the %s", c.name, what)
					}
				}
				for id, what := range c.notSee {
					if actions := got[id]; len(actions) > 0 {
						t.Errorf("%s sees entries about the %s: %v", c.name, what, actions)
					}
				}
				if c.notSee == nil {
					return
				}
				for _, en := range l.entries {
					raw, _ := json.Marshal(en.Changes)
					if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), f.Client2) {
						t.Errorf("%s sees an entry with data outside its scope: %s %s/%s %s", c.name, en.Action, en.ResourceType, en.ResourceID, raw)
					}
				}
				// The resource_type filter narrows the visible entries only.
				racks := about(list(c.name, c.scope, base+"?limit=500&resource_type=racks"))
				for id := range racks {
					if _, hidden := c.notSee[id]; hidden {
						t.Errorf("%s with resource_type=racks sees %s", c.name, c.notSee[id])
					}
				}
				if len(racks[rack1]) == 0 {
					t.Errorf("%s with resource_type=racks does not see the rack of client 1", c.name)
				}
			})
		}
	}

	// Controls: GET of the objects themselves is refused for the client-1
	// principal.
	c1 := identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: all, Teams: all}
	for _, path := range []string{"/api/v1/racks/" + rack2, "/api/v1/assets/" + asset2, "/api/v1/cis/" + ciA} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p := identity.Principal{Subject: admin.User, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser, Scope: &c1,
			Permissions: identity.AllPermissions(), Credential: "als"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		e.handler.ServeHTTP(w, req.WithContext(ctx))
		if w.Code != http.StatusNotFound {
			t.Errorf("control GET %s as client 1 = %d, want 404", path, w.Code)
		}
	}
}

func alsMerge(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
