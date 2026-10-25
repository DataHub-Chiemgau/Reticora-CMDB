package rls_test

// Phase-1 acceptance audit, group T1-tenancy: REGRESSION test (passes).
//
// WP-208 (TEN-09, TST-02; GATE-03) gap "Location-Kanal unterhalb von Sites
// (Buildings, Rooms, Racks ...)": buildings, rooms and racks are listed,
// read and written per client, site, multi-scope and foreign-organization
// principal; a write outside the scope answers exactly 404 and leaves the
// row unchanged (WP-409; the rack site scope since WP-340, F-002). The
// org-wide channel writes of the wave (webhooks, jobs, review items,
// notifications) are rows of TestChannelMatrix since WP-409.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1LocationChannelBelowSites' ./internal/tenant/rls/

import (
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditT1LocationChannelBelowSites(t *testing.T) {
	w := newT1World(t, "ed")
	f := w.f
	room1, room2 := f.Room(t, f.Client1, "t1-loc-s1"), f.Room(t, f.Client2, "t1-loc-s2")
	bld1 := w.scalar(`SELECT building_id::text FROM room WHERE id = $1`, room1)
	bld2 := w.scalar(`SELECT building_id::text FROM room WHERE id = $1`, room2)
	site1, site2 := w.siteOfRoom(room1), w.siteOfRoom(room2)
	rack1 := w.create(w.orgA, "/api/v1/racks", map[string]any{"name": "t1-loc-rack1", "room_id": room1})
	rack2 := w.create(w.orgA, "/api/v1/racks", map[string]any{"name": "t1-loc-rack2", "room_id": room2})

	type object struct {
		kind, id, client, site string
	}
	objects := []object{
		{"buildings", bld1, f.Client1, site1}, {"buildings", bld2, f.Client2, site2},
		{"rooms", room1, f.Client1, site1}, {"rooms", room2, f.Client2, site2},
		{"racks", rack1, f.Client1, site1}, {"racks", rack2, f.Client2, site2},
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
	principals := []*channelPrincipal{
		w.orgA,
		w.as("client1", only(f.Client1), all()),
		w.as("site2", all(), only(site2)),
		w.as("multi(c1+c2,s1)", only(f.Client1, f.Client2), only(site1)),
		w.orgB,
	}
	for _, p := range principals {
		lists := map[string]string{}
		for _, kind := range []string{"buildings", "rooms", "racks"} {
			code, body := w.do(p, http.MethodGet, "/api/v1/"+kind+"?limit=200", nil)
			if code != http.StatusOK {
				t.Fatalf("GET /%s as %s: %d %s", kind, p.kind, code, trim(body))
			}
			lists[kind] = body
		}
		for _, o := range objects {
			visible := p.org == f.OrgA && inSet(p.scope.Clients, o.client) && inSet(p.scope.Sites, o.site)
			if got := strings.Contains(lists[o.kind], o.id); got != visible {
				t.Errorf("GET /%s as %s lists %s: %v, want %v", o.kind, p.kind, o.id, got, visible)
			}
			want := http.StatusNotFound
			if visible {
				want = http.StatusOK
			}
			if code, body := w.do(p, http.MethodGet, "/api/v1/"+o.kind+"/"+o.id, nil); code != want {
				t.Errorf("GET /%s/%s as %s: %d %s, want %d", o.kind, o.id, p.kind, code, trim(body), want)
			}
			if visible {
				continue
			}
			if code, body := w.do(p, http.MethodPatch, "/api/v1/"+o.kind+"/"+o.id, map[string]any{"name": "t1-loc-hijack"}); code != http.StatusNotFound {
				t.Errorf("PATCH /%s/%s as %s: %d %s, want 404", o.kind, o.id, p.kind, code, trim(body))
			}
			if code, body := w.do(p, http.MethodDelete, "/api/v1/"+o.kind+"/"+o.id, nil); code != http.StatusNotFound {
				t.Errorf("DELETE /%s/%s as %s: %d %s, want 404", o.kind, o.id, p.kind, code, trim(body))
			}
			table := strings.TrimSuffix(o.kind, "s")
			if got := w.scalar(`SELECT count(*)::text FROM `+table+` WHERE id = $1 AND name <> 't1-loc-hijack'`, o.id); got != "1" {
				t.Fatalf("%s %s after the refused writes by %s: %s unchanged rows, want 1", table, o.id, p.kind, got)
			}
		}
	}
	// Positive control: in-scope writes succeed.
	if code, body := w.do(principals[1], http.MethodPatch, "/api/v1/racks/"+rack1, map[string]any{"name": "t1-loc-rack1b"}); code != http.StatusOK {
		t.Errorf("PATCH own rack as client1: %d %s", code, trim(body))
	}
	if code, body := w.do(principals[2], http.MethodPatch, "/api/v1/rooms/"+room2, map[string]any{"name": "t1-loc-room2b"}); code != http.StatusOK {
		t.Errorf("PATCH room in site scope as site2: %d %s", code, trim(body))
	}
}
