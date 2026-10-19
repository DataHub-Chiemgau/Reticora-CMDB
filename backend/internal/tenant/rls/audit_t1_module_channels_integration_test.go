package rls_test

// Phase-1 acceptance audit, group T1-tenancy: REGRESSION test (passes).
//
// WP-017 (AST-01, AST-04, TEN-06) gap "No router-level isolation test (auth
// middleware -> authz -> handler -> RLS) for /assets, /assignments,
// /reservations, /compositions", WP-018 (AST-02, AST-05) gap "No
// router-level client-scope API test for stock endpoints" and WP-020
// (TKT-01, TEN-06) gap "No router-level client-scope API test for
// /tickets": every object of client 2 is missing from the client-1 lists,
// its detail answers 404, writes answer 404 and leave the row unchanged,
// the own objects stay readable, and organization B sees nothing of A.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1ModuleChannelsClientScope' ./internal/tenant/rls/

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuditT1ModuleChannelsClientScope(t *testing.T) {
	w := newT1World(t, "ce")
	f := w.f
	client1 := w.as("client1", only(f.Client1), all())
	ci1, ci2 := f.CI(t, f.OrgA, f.Client1, "t1-mod-ci1"), f.CI(t, f.OrgA, f.Client2, "t1-mod-ci2")
	a1, a2 := f.Asset(t, f.OrgA, f.Client1, "t1-mod-a1"), f.Asset(t, f.OrgA, f.Client2, "t1-mod-a2")
	ticket := func(ci, title string) string {
		return w.scalar(`INSERT INTO ticket (organization_id, title, reporter_id, related_ci_id) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			f.OrgA, title, w.userA, ci)
	}
	consumable := func(client, name string) string {
		return w.scalar(`INSERT INTO consumable (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`, f.OrgA, client, name)
	}
	item := func(client, name string) string {
		return w.scalar(`INSERT INTO quantity_item (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`, f.OrgA, client, name)
	}
	movement := func(asset string) string {
		return w.create(w.orgA, "/api/v1/stock-movements", map[string]any{"movement_type": "correction", "asset_id": asset, "reason": "t1"})
	}
	t1, t2 := ticket(ci1, "t1-mod-ticket1"), ticket(ci2, "t1-mod-ticket2")
	if got := w.scalar(`SELECT COALESCE(client_id::text, '') FROM ticket WHERE id = $1`, t2); got != f.Client2 {
		t.Fatalf("setup: ticket of the client-2 CI has client %q, want client 2", got)
	}

	kinds := []struct {
		name       string
		list       string // list path
		detail     string // detail path prefix ("" = no detail route)
		own, other string
		patch      map[string]any
		del        bool
		state      string // SQL that must not change through the refused writes
	}{
		{"assets", "/api/v1/assets", "/api/v1/assets/", a1, a2, map[string]any{"name": "t1-mod-hijack"}, true,
			`SELECT name FROM asset WHERE id = $1`},
		{"reservations", "/api/v1/reservations", "/api/v1/reservations/",
			w.create(w.orgA, "/api/v1/reservations", map[string]any{"asset_id": a1}),
			w.create(w.orgA, "/api/v1/reservations", map[string]any{"asset_id": a2}), nil, false,
			`SELECT state FROM reservation WHERE id = $1`},
		{"tickets", "/api/v1/tickets", "/api/v1/tickets/", t1, t2, map[string]any{"title": "t1-mod-hijack"}, true,
			`SELECT title || '|' || status FROM ticket WHERE id = $1`},
		{"consumables", "/api/v1/consumables", "/api/v1/consumables/", consumable(f.Client1, "t1-mod-cons1"), consumable(f.Client2, "t1-mod-cons2"),
			map[string]any{"name": "t1-mod-hijack"}, true, `SELECT name FROM consumable WHERE id = $1`},
		{"inventory items", "/api/v1/inventory/items", "/api/v1/inventory/items/", item(f.Client1, "t1-mod-item1"), item(f.Client2, "t1-mod-item2"),
			map[string]any{"name": "t1-mod-hijack"}, true, `SELECT name FROM quantity_item WHERE id = $1`},
		{"stock movements", "/api/v1/stock-movements", "", movement(a1), movement(a2), nil, false,
			`SELECT movement_type FROM asset_movement WHERE id = $1`},
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			before := w.scalar(k.state, k.other)
			code, list := w.do(client1, http.MethodGet, k.list+"?limit=200", nil)
			if code != http.StatusOK || !strings.Contains(list, k.own) {
				t.Fatalf("GET %s as client 1: %d, own object listed %v; want 200 with it", k.list, code, strings.Contains(list, k.own))
			}
			if strings.Contains(list, k.other) {
				t.Errorf("TEN-06: GET %s as client 1 lists the client-2 object %s", k.list, k.other)
			}
			if _, orgList := w.do(w.orgA, http.MethodGet, k.list+"?limit=200", nil); !strings.Contains(orgList, k.other) {
				t.Errorf("control: the org-wide principal does not list the client-2 object in %s", k.list)
			}
			if code, body := w.do(w.orgB, http.MethodGet, k.list+"?limit=200", nil); code != http.StatusOK || strings.Contains(body, k.own) || strings.Contains(body, k.other) {
				t.Errorf("GET %s as organization B: %d, lists objects of organization A %v", k.list, code, strings.Contains(body, k.own) || strings.Contains(body, k.other))
			}
			if k.detail != "" {
				for _, c := range []struct {
					p    *channelPrincipal
					id   string
					want int
				}{{client1, k.own, http.StatusOK}, {client1, k.other, http.StatusNotFound}, {w.orgB, k.own, http.StatusNotFound}} {
					if code, body := w.do(c.p, http.MethodGet, k.detail+c.id, nil); code != c.want {
						t.Errorf("GET %s%s as %s: %d %s, want %d", k.detail, c.id, c.p.kind, code, trim(body), c.want)
					}
				}
			}
			for _, p := range []*channelPrincipal{client1, w.orgB} {
				if k.patch != nil {
					if code, body := w.do(p, http.MethodPatch, k.detail+k.other, k.patch); code != http.StatusNotFound {
						t.Errorf("PATCH %s%s as %s: %d %s, want 404", k.detail, k.other, p.kind, code, trim(body))
					}
				}
				if k.del {
					if code, body := w.do(p, http.MethodDelete, k.detail+k.other, nil); code != http.StatusNotFound {
						t.Errorf("DELETE %s%s as %s: %d %s, want 404", k.detail, k.other, p.kind, code, trim(body))
					}
				}
				if k.name == "reservations" {
					// The exact status (404) is part of the WP-017 error
					// contract defect, see TestAuditT1RepositoryErrorsAreSanitized.
					if code, body := w.do(p, http.MethodPost, k.detail+k.other+"/release", nil); code < 400 {
						t.Errorf("POST %s%s/release as %s: %d %s, want refused", k.detail, k.other, p.kind, code, trim(body))
					}
				}
			}
			if after := w.scalar(k.state, k.other); after != before {
				t.Errorf("the client-2 object changed through refused writes: %q -> %q", before, after)
			}
		})
	}
}
