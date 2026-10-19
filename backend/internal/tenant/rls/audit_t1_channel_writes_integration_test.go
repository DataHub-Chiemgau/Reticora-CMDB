package rls_test

// Phase-1 acceptance audit, group T1-tenancy: REGRESSION tests (they pass).
//
// WP-208 (TEN-09, TST-02; GATE-03) gaps:
//   - high "Cross-Scope- und Cross-Org-Schreibzugriffe der org-weiten Kanäle
//     mit Positivkontrolle": TestChannelMatrix reads review items, webhooks,
//     jobs and notifications only and accepts any non-2xx code. Here the
//     foreign writes must answer exactly 404 (or 403) and leave the rows
//     untouched, and the same writes on own objects must succeed, so a 404
//     is not just a broken route. GET /jobs is checked with data.
//   - critical "Location-Kanal unterhalb von Sites (Buildings, Rooms, Racks
//     ...)": buildings and rooms are listed, read and written per client,
//     site, multi-scope and foreign-organization principal. (The rack paths
//     of a site-scoped principal are a confirmed defect, see
//     TestAuditT1RackChannelSiteScope behind the auditdefect tag; only the
//     client and organization dimension of racks is asserted here.)
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1OrgWideChannelWrites|TestAuditT1LocationChannelBelowSites' ./internal/tenant/rls/

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditT1OrgWideChannelWrites(t *testing.T) {
	w := newT1World(t, "ec")
	f := w.f
	otherA := f.AppUser(t, f.OrgA, "t1-cw-other")
	ci2 := f.CI(t, f.OrgA, f.Client2, "t1-cw-ci2")
	client1 := w.as("client1", only(f.Client1), all())
	hook := func(name string) string {
		return w.scalar(`INSERT INTO webhook_subscription (organization_id, name, url, secret, events) VALUES ($1, $2, 'https://a.example/h', 's', '{ci.created}') RETURNING id::text`,
			f.OrgA, name)
	}
	review := func(name string) string {
		return w.scalar(`INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids) VALUES ($1, 'probable_duplicate', $2, ARRAY[$3::uuid]) RETURNING id::text`,
			f.OrgA, fmt.Sprintf(`{"name":%q,"ci_id":%q}`, name, ci2), ci2)
	}
	note := func(user, subject string) string {
		return w.scalar(`INSERT INTO notification_inbox (organization_id, user_id, category, subject, body) VALUES ($1, $2, 'review', $3, 'x') RETURNING id::text`,
			f.OrgA, user, subject)
	}
	hookA, hookOwn := hook("t1-cw-hook"), hook("t1-cw-hook-own")
	jobA := w.scalar(`INSERT INTO job (organization_id, kind, status) VALUES ($1, 't1cw.kind', 'queued') RETURNING id::text`, f.OrgA)
	jobB := w.scalar(`INSERT INTO job (organization_id, kind, status) VALUES ($1, 't1cw.kindb', 'queued') RETURNING id::text`, f.OrgB)
	reviewA, reviewOwn := review("t1-cw-review"), review("t1-cw-review-own")
	noteOther, noteOwn := note(otherA, "t1-cw-note-other"), note(w.userA, "t1-cw-note-own")

	for _, s := range []struct {
		name         string
		p            *channelPrincipal
		method, path string
		body         any
	}{
		{"organization B deletes a webhook of A", w.orgB, http.MethodDelete, "/api/v1/webhooks/" + hookA, nil},
		{"organization B cancels a job of A", w.orgB, http.MethodDelete, "/api/v1/jobs/" + jobA, nil},
		{"organization B reverts a job of A", w.orgB, http.MethodPost, "/api/v1/jobs/" + jobA + "/revert", map[string]any{}},
		{"organization B resolves a review of A", w.orgB, http.MethodPost, "/api/v1/review-items/" + reviewA + "/resolve", map[string]any{"action": "dismiss"}},
		{"client 1 resolves a review about a client-2 CI", client1, http.MethodPost, "/api/v1/review-items/" + reviewA + "/resolve", map[string]any{"action": "dismiss"}},
		{"a user marks another user's notification read", w.orgA, http.MethodPost, "/api/v1/notifications/" + noteOther + "/read", map[string]any{}},
	} {
		t.Run("refused: "+s.name, func(t *testing.T) {
			if code, body := w.do(s.p, s.method, s.path, s.body); !refused(code) {
				t.Errorf("%s %s as %s: %d %s, want 404/403", s.method, s.path, s.p.kind, code, trim(body))
			}
		})
	}
	if got := w.scalar(`SELECT (SELECT count(*) FROM webhook_subscription WHERE id = $1)::text || '|' || (SELECT status FROM review_item WHERE id = $2)
		|| '|' || (SELECT COALESCE(read_at::text, 'unread') FROM notification_inbox WHERE id = $3) || '|' || (SELECT status FROM job WHERE id = $4)`,
		hookA, reviewA, noteOther, jobA); got != "1|open|unread|queued" {
		t.Errorf("state after the refused writes (webhook rows|review|note|job) = %s, want 1|open|unread|queued", got)
	}

	for _, s := range []struct {
		name         string
		method, path string
		body         any
	}{
		{"delete an own webhook", http.MethodDelete, "/api/v1/webhooks/" + hookOwn, nil},
		{"resolve an own review", http.MethodPost, "/api/v1/review-items/" + reviewOwn + "/resolve", map[string]any{"action": "dismiss"}},
		{"mark an own notification read", http.MethodPost, "/api/v1/notifications/" + noteOwn + "/read", map[string]any{}},
	} {
		t.Run("positive control: "+s.name, func(t *testing.T) {
			if code, body := w.do(w.orgA, s.method, s.path, s.body); code < 200 || code >= 300 {
				t.Errorf("%s %s as org: %d %s, want 2xx", s.method, s.path, code, trim(body))
			}
		})
	}
	t.Run("jobs list with data", func(t *testing.T) {
		for _, c := range []struct {
			p        *channelPrincipal
			own, not string
		}{{w.orgA, jobA, jobB}, {w.orgB, jobB, jobA}} {
			code, body := w.do(c.p, http.MethodGet, "/api/v1/jobs?limit=200", nil)
			if code != http.StatusOK || !strings.Contains(body, c.own) || strings.Contains(body, c.not) {
				t.Errorf("GET /jobs as %s: %d, own job %v, foreign job %v; want 200/true/false", c.p.kind, code,
					strings.Contains(body, c.own), strings.Contains(body, c.not))
			}
		}
	})
}

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
	buildings := []object{{"buildings", bld1, f.Client1, site1}, {"buildings", bld2, f.Client2, site2}}
	rooms := []object{{"rooms", room1, f.Client1, site1}, {"rooms", room2, f.Client2, site2}}
	racks := []object{{"racks", rack1, f.Client1, site1}, {"racks", rack2, f.Client2, site2}}
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
		objects := append(append([]object{}, buildings...), rooms...)
		if p.scope.Sites.All {
			// Racks: client and organization dimension only (site scope of
			// racks is the confirmed WP-208 defect).
			objects = append(objects, racks...)
		}
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
			if code, body := w.do(p, http.MethodPatch, "/api/v1/"+o.kind+"/"+o.id, map[string]any{"name": "t1-loc-hijack"}); !refused(code) {
				t.Errorf("PATCH /%s/%s as %s: %d %s, want 404/403", o.kind, o.id, p.kind, code, trim(body))
			}
			if code, body := w.do(p, http.MethodDelete, "/api/v1/"+o.kind+"/"+o.id, nil); !refused(code) {
				t.Errorf("DELETE /%s/%s as %s: %d %s, want 404/403", o.kind, o.id, p.kind, code, trim(body))
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
