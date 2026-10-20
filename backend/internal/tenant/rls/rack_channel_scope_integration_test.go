package rls_test

// Rack channel through the complete PostgreSQL router (WP-340, F-002, F-180,
// TEN-06, TEN-09). A rack is visible and writable exactly when its client and
// site are in the principal's scope (TEN-05, CH25), on every path: list,
// detail, layout, mounts, GraphQL rackLayout, PATCH and DELETE. A refused
// DELETE keeps the rack and its location row; the mounts of an invisible rack
// answer 404 like its detail (no existence oracle).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -run '^TestRackChannelSiteScope$' ./internal/tenant/rls/

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestRackChannelSiteScope runs the rack channel for every scope kind: org,
// client, site, client+site (several clients, one site), the client- and
// site-scoped principal of the probe (clients=[c1], sites=[s1]) and a
// principal of another organization.
func TestRackChannelSiteScope(t *testing.T) {
	w := newT1World(t, "d4")
	f := w.f
	room1, room2 := f.Room(t, f.Client1, "t1-rack-s1"), f.Room(t, f.Client2, "t1-rack-s2")
	site1, site2 := w.siteOfRoom(room1), w.siteOfRoom(room2)
	type rackInfo struct{ id, name, client, site string }
	racks := []rackInfo{
		{w.create(w.orgA, "/api/v1/racks", map[string]any{"name": "t1-rack-one", "room_id": room1}), "t1-rack-one", f.Client1, site1},
		{w.create(w.orgA, "/api/v1/racks", map[string]any{"name": "t1-rack-two", "room_id": room2}), "t1-rack-two", f.Client2, site2},
	}
	principals := []*channelPrincipal{
		w.orgA,
		w.as("client1", only(f.Client1), all()),
		w.as("site2", all(), only(site2)),
		w.as("multi(c1+c2,s1)", only(f.Client1, f.Client2), only(site1)),
		// probe_rack_cross_client.sql (F-002): the LEFT JOIN admitted the
		// client-2/site-2 rack for this principal.
		w.as("c1+s1", only(f.Client1), only(site1)),
		w.orgB,
	}
	inSet := func(s identity.ScopeSet, v string) bool {
		if s.All {
			return true
		}
		for _, id := range s.IDs {
			if id == v {
				return true
			}
		}
		return false
	}
	visible := func(p *channelPrincipal, r rackInfo) bool {
		return p.org == f.OrgA && inSet(p.scope.Clients, r.client) && inSet(p.scope.Sites, r.site)
	}
	wantCode := func(v bool) int {
		if v {
			return http.StatusOK
		}
		return http.StatusNotFound
	}

	// Reads.
	for _, p := range principals {
		_, list := w.do(p, http.MethodGet, "/api/v1/racks?limit=200", nil)
		for _, r := range racks {
			v := visible(p, r)
			if got := strings.Contains(list, r.id); got != v {
				t.Errorf("GET /racks as %s: rack %s listed %v, want %v", p.kind, r.name, got, v)
			}
			for _, path := range []string{"/api/v1/racks/" + r.id, "/api/v1/racks/" + r.id + "/layout", "/api/v1/racks/" + r.id + "/mounts"} {
				if code, _ := w.do(p, http.MethodGet, path, nil); code != wantCode(v) {
					t.Errorf("GET %s (%s) as %s: %d, want %d", path, r.name, p.kind, code, wantCode(v))
				}
			}
			_, gql := w.do(p, http.MethodPost, "/bff/graphql", map[string]any{"query": fmt.Sprintf(`{ rackLayout(rackId: %q) { rack { id name } } }`, r.id)})
			if got := strings.Contains(gql, r.name); got != v {
				t.Errorf("GraphQL rackLayout(%s) as %s: rack disclosed %v, want %v", r.name, p.kind, got, v)
			}
		}
	}
	// Writes outside the scope are refused and leave rack and location rows.
	for _, p := range principals {
		for _, r := range racks {
			if visible(p, r) {
				continue
			}
			if code, body := w.do(p, http.MethodPatch, "/api/v1/racks/"+r.id, map[string]any{"name": "t1-renamed-by-" + p.kind}); !refused(code) {
				t.Errorf("PATCH rack %s as %s: %d %s, want 404/403", r.name, p.kind, code, trim(body))
			}
			if code, body := w.do(p, http.MethodDelete, "/api/v1/racks/"+r.id, nil); code != http.StatusNotFound {
				t.Errorf("DELETE rack %s as %s: %d %s, want 404", r.name, p.kind, code, trim(body))
			}
			if got := w.scalar(`SELECT (SELECT count(*) FROM rack WHERE id = $1)::text || '/' || (SELECT count(*) FROM location WHERE id = $1)::text`, r.id); got != "1/1" {
				t.Errorf("rack %s after DELETE by %s: rack/location rows %s, want 1/1", r.name, p.kind, got)
				return // the rack is gone; later principals cannot be checked against it
			}
		}
	}
	// Positive control: in scope the same writes succeed, and a permitted
	// delete removes the rack and its location row.
	if code, body := w.do(principals[1], http.MethodPatch, "/api/v1/racks/"+racks[0].id, map[string]any{"name": "t1-rack-one-b"}); code != http.StatusOK {
		t.Errorf("PATCH own rack as client1: %d %s, want 200", code, trim(body))
	}
	if code, body := w.do(w.orgA, http.MethodDelete, "/api/v1/racks/"+racks[1].id, nil); code != http.StatusNoContent {
		t.Errorf("DELETE rack two as org: %d %s, want 204", code, trim(body))
	}
	if got := w.scalar(`SELECT (SELECT count(*) FROM rack WHERE id = $1)::text || '/' || (SELECT count(*) FROM location WHERE id = $1)::text`, racks[1].id); got != "0/0" {
		t.Errorf("rack two after the permitted delete: rack/location rows %s, want 0/0", got)
	}
}
