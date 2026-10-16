//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT tests (they fail until
// the product is fixed).
//
// WP-025 (TEN-02, TEN-05, TEN-06, TEN-09) P1 "Review items without
// candidate CIs leak across clients through GET /api/v1/review-items":
// review_item has no client_id/site_id and reviews/pg_repository.go treats an
// empty candidate list as visible, so a client-1 principal lists, reads and
// resolves the unclassified_device / scope_suggested items that discovery
// opened for devices and subnets of client 2.
//
// WP-208 (TEN-09, TST-02) P1 "Site scope bypass in the rack channel":
// rack/pg_repository.go rackVisible LEFT JOINs building; building RLS hides
// the building of a foreign site, building.site_id becomes NULL and the
// NULL branch admits the rack. A site-restricted principal lists, reads
// (REST, layout, mounts, GraphQL rackLayout) and deletes racks of other
// sites; the delete leaves an orphaned location row. Found while writing
// this test: GET /api/v1/racks/{id}/mounts of a rack outside the client
// scope answers 200 (empty list) instead of 404, because
// rack.PGRepository.ListMounts checks only that the rack exists in the
// organization (existence oracle; the mount rows themselves are filtered).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v \
//     -run 'TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope|TestAuditT1RackChannelSiteScope' ./internal/tenant/rls/

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope opens review
// items without candidate CIs through the product's own write paths for a
// device and a subnet of client 2 and checks that a client-1 principal can
// neither list, read nor resolve them, while the org-wide principal sees
// them.
func TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope(t *testing.T) {
	w := newT1World(t, "d3")
	f := w.f
	bg := context.Background()
	client1 := w.as("client1", only(f.Client1), all())
	collector2 := w.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 't1rev-collector-c2', 'active') RETURNING id::text`,
		f.OrgA, f.Client2)

	// unclassified_device: a record of the client-2 discovery scope whose CI
	// type the server does not know (discovery.go openFrame).
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "t1rev-unknown-type", Name: "t1rev-c2-firewall", IP: "10.77.2.1", Serial: "T1REV-C2-SN"},
		time.Now().Add(-time.Minute))
	rec.ClientID = f.Client2
	if code, body := w.do(w.orgA, http.MethodPost, "/api/v1/ingest/bulk", plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}}); code != http.StatusAccepted {
		t.Fatalf("ingest the client-2 record: %d %s", code, trim(body))
	}
	// scope_suggested: the client-2 collector reports an uncovered subnet
	// (scope_handler.go SuggestScopes, the NATS subnets report path).
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	if n, err := discovery.NewHandler(repos.Discovery, repos.CI).SuggestScopes(f.OrgCtx(f.OrgA), f.OrgA, collector2, []string{"10.77.9.0/24"}); err != nil || n != 1 {
		t.Fatalf("suggest the client-2 subnet: n=%d err=%v", n, err)
	}

	items := map[string]string{}
	rows, err := f.Admin.Query(bg, `SELECT kind, id::text FROM review_item WHERE organization_id = $1 AND cardinality(candidate_ci_ids) = 0
		AND kind IN ('unclassified_device', 'scope_suggested')`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			t.Fatal(err)
		}
		items[kind] = id
	}
	rows.Close()
	if items["unclassified_device"] == "" || items["scope_suggested"] == "" {
		t.Fatalf("setup: review items without candidates %v, want unclassified_device and scope_suggested", items)
	}

	// Positive control: the org-wide principal sees both.
	_, orgBody := w.do(w.orgA, http.MethodGet, "/api/v1/review-items?limit=200", nil)
	for kind, id := range items {
		if !strings.Contains(orgBody, id) {
			t.Fatalf("org-wide principal does not see the %s item %s", kind, id)
		}
	}

	code, body := w.do(client1, http.MethodGet, "/api/v1/review-items?limit=200", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /review-items as client 1: %d %s", code, trim(body))
	}
	for _, marker := range []string{"t1rev-c2-firewall", "T1REV-C2-SN", "10.77.9.0/24", "t1rev-collector-c2", f.Client2} {
		if strings.Contains(body, marker) {
			t.Errorf("TEN-06/TEN-09: GET /review-items as client 1 discloses %q of client 2", marker)
		}
	}
	for kind, id := range items {
		if code, _ := w.do(client1, http.MethodGet, "/api/v1/review-items/"+id, nil); code != http.StatusNotFound {
			t.Errorf("GET /review-items/{%s of client 2} as client 1: %d, want 404", kind, code)
		}
		if code, _ := w.do(client1, http.MethodPost, "/api/v1/review-items/"+id+"/resolve", map[string]any{"action": "dismiss"}); code != http.StatusNotFound {
			t.Errorf("POST /review-items/{%s of client 2}/resolve as client 1: %d, want 404", kind, code)
		}
		if st := w.scalar(`SELECT status FROM review_item WHERE id = $1`, id); st != "open" {
			t.Errorf("%s item of client 2 after the client-1 resolve: status %q, want open", kind, st)
		}
	}
	// GraphQL reviewInbox is the same channel.
	if _, gql := w.do(client1, http.MethodPost, "/bff/graphql", map[string]any{"query": "{ reviewInbox(first: 100) { id } }"}); strings.Contains(gql, items["unclassified_device"]) ||
		strings.Contains(gql, items["scope_suggested"]) {
		t.Errorf("TEN-09: GraphQL reviewInbox as client 1 lists review items of client 2")
	}
}

// TestAuditT1RackChannelSiteScope runs the rack channel for every scope
// kind: a rack is visible and writable exactly when its client and site are
// in the principal's scope (TEN-05, CH25), on every path.
func TestAuditT1RackChannelSiteScope(t *testing.T) {
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
	// delete removes the location row as well.
	if code, body := w.do(principals[1], http.MethodPatch, "/api/v1/racks/"+racks[0].id, map[string]any{"name": "t1-rack-one-b"}); code != http.StatusOK {
		t.Errorf("PATCH own rack as client1: %d %s, want 200", code, trim(body))
	}
	if code, body := w.do(w.orgA, http.MethodDelete, "/api/v1/racks/"+racks[1].id, nil); code != http.StatusNoContent {
		t.Errorf("DELETE rack two as org: %d %s, want 204", code, trim(body))
	}
	if got := w.scalar(`SELECT count(*)::text FROM location WHERE id = $1`, racks[1].id); got != "0" {
		t.Errorf("location rows after the permitted delete: %s, want 0", got)
	}
}
