package rls_test

// Evidence of the CMDB core channels (WP-536, t1-ev-core-isolation): open
// evidence of the audit WPs WP-011 (history and credentials of client 2),
// WP-013 (client-scoped collector management, sanitized discovery and IPAM
// errors of the auditor probes) and WP-015 (execution of a shared saved
// view). Every request runs through the complete PostgreSQL router
// (newT1World): router, authorization, handler, repository and RLS. A
// principal restricted to client 1 neither reads nor changes an object of
// client 2; such an object answers 404 and its rows stay unchanged.
//
// Requirements: TEN-06 (TEN-04, API-03).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run '^TestCoreChannels' ./internal/tenant/rls/

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// coreIDs returns the ids of the data array of a list response.
func coreIDs(t *testing.T, body string) map[string]bool {
	t.Helper()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode list: %v: %s", err, trim(body))
	}
	ids := map[string]bool{}
	for _, d := range out.Data {
		ids[d.ID] = true
	}
	return ids
}

// TestCoreChannelsHistoryAndCredentials covers WP-011: client 1 reads
// neither the seeded history of a client-2 CI nor a client-2 credential, and
// cannot delete that credential; the history and credential rows stay
// unchanged. The own objects of client 1 are readable (control).
func TestCoreChannelsHistoryAndCredentials(t *testing.T) {
	w := newT1World(t, "47")
	f := w.f
	client1 := w.as("client1", only(f.Client1), all())
	ci1 := f.CI(t, f.OrgA, f.Client1, "core-hist-c1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "core-hist-c2")

	// History: an org-wide change of each CI writes ci_change and audit
	// rows (seeded through the API like every change).
	for id, name := range map[string]string{ci1: "core-hist-c1-renamed", ci2: "core-hist-c2-renamed"} {
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/cis/"+id, map[string]any{"name": name}); code != http.StatusOK {
			t.Fatalf("seed history of %s: %d %s", id, code, trim(body))
		}
	}
	historyRows := func(id string) string {
		return w.scalar(`SELECT (SELECT COUNT(*) FROM ci_change WHERE ci_id = $1)::text || '/' ||
			(SELECT COUNT(*) FROM audit_log WHERE resource_id = $1)::text`, id)
	}
	seeded := historyRows(ci2)
	if strings.HasPrefix(seeded, "0/") {
		t.Fatalf("no ci_change rows seeded for the client-2 CI: %s", seeded)
	}

	t.Run("history of a client-2 CI", func(t *testing.T) {
		for _, path := range []string{"/api/v1/history/ci/" + ci2, "/api/v1/history/ci/" + ci2 + "?change_kind=updated", "/api/v1/cis/" + ci2 + "/state"} {
			code, body := w.do(client1, http.MethodGet, path, nil)
			if code != http.StatusNotFound {
				t.Errorf("GET %s as client 1: %d %s, want 404", path, code, trim(body))
			}
			if strings.Contains(body, "core-hist-c2") {
				t.Errorf("GET %s as client 1 discloses the client-2 CI: %s", path, trim(body))
			}
		}
		if after := historyRows(ci2); after != seeded {
			t.Errorf("history rows of the client-2 CI changed: %s -> %s", seeded, after)
		}
		code, body := w.do(client1, http.MethodGet, "/api/v1/history/ci/"+ci1, nil)
		if code != http.StatusOK || !strings.Contains(body, "core-hist-c1-renamed") {
			t.Errorf("GET own history as client 1: %d %s, want 200 with the rename", code, trim(body))
		}
	})

	t.Run("credentials of client 2", func(t *testing.T) {
		cred := func(client, name string) string {
			return w.create(w.orgA, "/api/v1/credentials", map[string]any{"client_id": client, "name": name, "kind": "api_token",
				"secret": map[string]any{"token": "core-secret-" + name}})
		}
		own, foreign := cred(f.Client1, "core-cred-c1"), cred(f.Client2, "core-cred-c2")
		state := func(id string) string {
			return w.scalar(`SELECT concat_ws('|', name, client_id, updated_at, key_version, md5(ciphertext::text)) FROM credential WHERE id = $1`, id)
		}
		before := state(foreign)

		code, body := w.do(client1, http.MethodGet, "/api/v1/credentials?limit=200", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /credentials as client 1: %d %s", code, trim(body))
		}
		if ids := coreIDs(t, body); !ids[own] || ids[foreign] {
			t.Errorf("GET /credentials as client 1 lists own %v, client 2 %v; want true/false", ids[own], ids[foreign])
		}
		for _, m := range []string{http.MethodGet, http.MethodDelete} {
			if code, body = w.do(client1, m, "/api/v1/credentials/"+foreign, nil); code != http.StatusNotFound {
				t.Errorf("%s client-2 credential as client 1: %d %s, want 404", m, code, trim(body))
			}
			if strings.Contains(body, "core-cred-c2") || strings.Contains(body, "core-secret") {
				t.Errorf("%s client-2 credential as client 1 discloses it: %s", m, trim(body))
			}
		}
		if after := state(foreign); after != before {
			t.Errorf("client-2 credential row changed: %q -> %q", before, after)
		}
		if code, body = w.do(client1, http.MethodGet, "/api/v1/credentials/"+own, nil); code != http.StatusOK {
			t.Errorf("GET own credential as client 1: %d %s, want 200", code, trim(body))
		}
	})
}

// TestCoreChannelsCollectorManagement covers the hardening of WP-013:
// collector management through the router is client-scoped. Client 1 lists
// only its own collector; changing the client-2 collector answers 404 like
// an unknown id, detail and DELETE (no such routes) refuse it exactly like
// the own collector, and its row stays unchanged. The org-wide principal
// changes the client-2 collector (control).
func TestCoreChannelsCollectorManagement(t *testing.T) {
	w := newT1World(t, "48")
	f := w.f
	client1 := w.as("client1", only(f.Client1), all())
	const unknown = "00000000-0000-4000-8000-0000000000fe"
	collector := func(client, name string) string {
		return w.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, $3, 'active') RETURNING id::text`,
			f.OrgA, client, name)
	}
	own, foreign := collector(f.Client1, "core-col-c1"), collector(f.Client2, "core-col-c2")
	state := func(id string) string {
		return w.scalar(`SELECT concat_ws('|', name, client_id, site_id, status, updated_at, md5(config::text)) FROM collector WHERE id = $1`, id)
	}
	before := state(foreign)

	code, body := w.do(client1, http.MethodGet, "/api/v1/collectors?limit=200", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /collectors as client 1: %d %s", code, trim(body))
	}
	if ids := coreIDs(t, body); !ids[own] || ids[foreign] {
		t.Errorf("GET /collectors as client 1 lists own %v, client 2 %v; want true/false", ids[own], ids[foreign])
	}
	if strings.Contains(body, "core-col-c2") {
		t.Errorf("GET /collectors as client 1 discloses the client-2 collector: %s", trim(body))
	}

	// PATCH is the collector management route (api/openapi.yaml): a
	// client-2 collector answers 404 like an unknown id.
	for _, patch := range []map[string]any{{"name": "core-col-hijack"}, {"status": "disabled"}} {
		code, body = w.do(client1, http.MethodPatch, "/api/v1/collectors/"+foreign, patch)
		ucode, ubody := w.do(client1, http.MethodPatch, "/api/v1/collectors/"+unknown, patch)
		if code != http.StatusNotFound || ucode != code || !sameProblem(body, ubody) {
			t.Errorf("PATCH %v of the client-2 collector as client 1: %d %s, unknown id %d %s; want 404 alike",
				patch, code, trim(body), ucode, trim(ubody))
		}
		if strings.Contains(body, "core-col-c2") {
			t.Errorf("PATCH of the client-2 collector as client 1 discloses it: %s", trim(body))
		}
	}
	// The router offers no detail and no DELETE route for collectors
	// (api/openapi.yaml lists PATCH only). Both answer the client-2
	// collector exactly like the own collector and an unknown id, never
	// 2xx: no read, no deletion, no existence oracle.
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		code, body = w.do(client1, method, "/api/v1/collectors/"+foreign, nil)
		ocode, obody := w.do(client1, method, "/api/v1/collectors/"+own, nil)
		ucode, ubody := w.do(client1, method, "/api/v1/collectors/"+unknown, nil)
		if code < 400 || code != ocode || code != ucode || !sameProblem(body, obody) || !sameProblem(body, ubody) {
			t.Errorf("%s client-2 collector as client 1: %d %s; own %d %s; unknown %d %s; want one refusal for all",
				method, code, trim(body), ocode, trim(obody), ucode, trim(ubody))
		}
		if strings.Contains(body, "core-col-c2") {
			t.Errorf("%s client-2 collector as client 1 discloses it: %s", method, trim(body))
		}
	}
	if after := state(foreign); after != before {
		t.Errorf("client-2 collector row changed: %q -> %q", before, after)
	}

	// Control: the org-wide principal renames the client-2 collector.
	if code, body = w.do(w.orgA, http.MethodPatch, "/api/v1/collectors/"+foreign, map[string]any{"name": "core-col-c2-org"}); code != http.StatusOK {
		t.Errorf("PATCH client-2 collector as org: %d %s, want 200", code, trim(body))
	}
}

// sameProblem reports whether two problem responses agree in everything but
// their trace and instance fields.
func sameProblem(a, b string) bool {
	strip := func(s string) map[string]any {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) != nil {
			return nil
		}
		delete(m, "trace_id")
		delete(m, "instance")
		delete(m, "request_id")
		return m
	}
	ma, mb := strip(a), strip(b)
	if ma == nil || mb == nil {
		return a == b
	}
	ja, _ := json.Marshal(ma)
	jb, _ := json.Marshal(mb)
	return string(ja) == string(jb)
}

// TestCoreChannelsDiscoveryIPAMErrorsAreSanitized turns the auditor probes
// of WP-013 (ipam_probe_test.go, discovery_probe_test.go of the audit, not in
// the repository) into a router regression test: invalid ids, unknown and
// foreign references and NUL bytes in the discovery and IPAM channels answer
// a client error with a fixed text, never a SQLSTATE, constraint, policy or
// driver text, and never 2xx.
func TestCoreChannelsDiscoveryIPAMErrorsAreSanitized(t *testing.T) {
	w := newT1World(t, "4a")
	f := w.f
	client1 := w.as("client1", only(f.Client1), all())
	const nul = "x\x00y"
	const unknown = "00000000-0000-4000-8000-0000000000fd"
	collector1 := w.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'core-err-c1', 'active') RETURNING id::text`,
		f.OrgA, f.Client1)
	collector2 := w.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'core-err-c2', 'active') RETURNING id::text`,
		f.OrgA, f.Client2)
	collectorB := w.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'core-err-b', 'active') RETURNING id::text`, f.OrgB)
	subnet2 := w.create(w.orgA, "/api/v1/subnets", map[string]any{"cidr": "10.247.2.0/24", "client_id": f.Client2, "name": "core-err-s2"})
	vrfB := w.create(w.orgB, "/api/v1/vrfs", map[string]any{"name": "core-err-vrf-b"})
	scope := func(collectorID, name string) map[string]any {
		return map[string]any{"collector_id": collectorID, "kind": "cidr", "target": "10.247.9.0/24", "name": name, "schedule": "24h"}
	}

	for _, c := range []struct {
		name         string
		p            *channelPrincipal
		method, path string
		body         any
		want         []int
	}{
		// IPAM: subnets and VRFs.
		{"subnet with a NUL name", w.orgA, http.MethodPost, "/api/v1/subnets", map[string]any{"cidr": "10.247.3.0/24", "name": nul}, []int{400, 422}},
		{"subnet with an invalid cidr", w.orgA, http.MethodPost, "/api/v1/subnets", map[string]any{"cidr": "10.247.300.0/24"}, []int{400, 422}},
		{"subnet of an invalid id", w.orgA, http.MethodGet, "/api/v1/subnets/not-a-uuid", nil, []int{404}},
		{"PATCH subnet of an invalid id", w.orgA, http.MethodPatch, "/api/v1/subnets/not-a-uuid", map[string]any{"name": "x"}, []int{404}},
		{"DELETE subnet of an invalid id", w.orgA, http.MethodDelete, "/api/v1/subnets/not-a-uuid", nil, []int{404}},
		{"addresses of an invalid subnet id", w.orgA, http.MethodGet, "/api/v1/subnets/not-a-uuid/addresses", nil, []int{404}},
		{"subnet with an invalid vrf_id", w.orgA, http.MethodPost, "/api/v1/subnets", map[string]any{"cidr": "10.247.4.0/24", "vrf_id": "not-a-uuid"},
			[]int{400, 422}},
		{"subnet with an unknown vrf_id", w.orgA, http.MethodPost, "/api/v1/subnets", map[string]any{"cidr": "10.247.5.0/24", "vrf_id": unknown},
			[]int{400, 422}},
		{"subnet with a VRF of organization B", w.orgA, http.MethodPost, "/api/v1/subnets", map[string]any{"cidr": "10.247.6.0/24", "vrf_id": vrfB},
			[]int{400, 422}},
		{"subnet with an unknown client", w.orgA, http.MethodPost, "/api/v1/subnets", map[string]any{"cidr": "10.247.7.0/24", "client_id": unknown},
			[]int{400, 403, 422}},
		{"subnet for client 2 by client 1", client1, http.MethodPost, "/api/v1/subnets", map[string]any{"cidr": "10.247.8.0/24", "client_id": f.Client2},
			[]int{403, 404, 422}},
		{"client-2 subnet read by client 1", client1, http.MethodGet, "/api/v1/subnets/" + subnet2, nil, []int{404}},
		{"client-2 subnet changed by client 1", client1, http.MethodPatch, "/api/v1/subnets/" + subnet2, map[string]any{"name": "core-err-hijack"}, []int{404}},
		{"VRF with a NUL name", w.orgA, http.MethodPost, "/api/v1/vrfs", map[string]any{"name": nul}, []int{400, 422}},
		{"VRF of an invalid id", w.orgA, http.MethodGet, "/api/v1/vrfs/not-a-uuid", nil, []int{400, 404}},
		{"VRF of organization B", w.orgA, http.MethodGet, "/api/v1/vrfs/" + vrfB, nil, []int{404}},
		{"PATCH VRF of organization B", w.orgA, http.MethodPatch, "/api/v1/vrfs/" + vrfB, map[string]any{"name": "core-err-hijack"}, []int{404}},

		// Discovery: collectors, scopes, jobs.
		{"collector with a NUL name", w.orgA, http.MethodPost, "/api/v1/collectors", map[string]any{"name": nul}, []int{400, 422}},
		{"PATCH collector of an invalid id", w.orgA, http.MethodPatch, "/api/v1/collectors/not-a-uuid", map[string]any{"name": "x"}, []int{404}},
		{"PATCH collector with an invalid site_id", w.orgA, http.MethodPatch, "/api/v1/collectors/" + collector1, map[string]any{"site_id": "not-a-uuid"},
			[]int{400, 422}},
		{"PATCH collector with a NUL name", w.orgA, http.MethodPatch, "/api/v1/collectors/" + collector1, map[string]any{"name": nul}, []int{400, 422}},
		{"collector of organization B changed", w.orgA, http.MethodPatch, "/api/v1/collectors/" + collectorB, map[string]any{"name": "core-err-hijack"},
			[]int{404}},
		{"discovery scope with an invalid collector_id", w.orgA, http.MethodPost, "/api/v1/discovery-scopes", scope("not-a-uuid", "core-err-1"),
			[]int{400, 404, 422}},
		{"discovery scope with an unknown collector", w.orgA, http.MethodPost, "/api/v1/discovery-scopes", scope(unknown, "core-err-2"), []int{404, 422}},
		{"discovery scope with a collector of organization B", w.orgA, http.MethodPost, "/api/v1/discovery-scopes", scope(collectorB, "core-err-3"),
			[]int{404, 422}},
		{"discovery scope with a client-2 collector by client 1", client1, http.MethodPost, "/api/v1/discovery-scopes", scope(collector2, "core-err-4"),
			[]int{403, 404, 422}},
		{"discovery scope with an invalid target", w.orgA, http.MethodPost, "/api/v1/discovery-scopes", map[string]any{"collector_id": collector1,
			"kind": "cidr", "target": "10.247.300.0/24", "schedule": "24h"}, []int{400, 422}},
		{"discovery scope with a NUL target", w.orgA, http.MethodPost, "/api/v1/discovery-scopes", map[string]any{"collector_id": collector1,
			"kind": "cidr", "target": nul, "schedule": "24h"}, []int{400, 422}},
		{"discovery scope of an invalid id", w.orgA, http.MethodGet, "/api/v1/discovery-scopes/not-a-uuid", nil, []int{404}},
		{"DELETE discovery scope of an invalid id", w.orgA, http.MethodDelete, "/api/v1/discovery-scopes/not-a-uuid", nil, []int{404}},
		{"discovery scopes of an invalid collector filter", w.orgA, http.MethodGet, "/api/v1/discovery-scopes?collector_id=not-a-uuid", nil,
			[]int{200, 400, 422}},
		{"discovery job of an invalid id", w.orgA, http.MethodGet, "/api/v1/discovery/jobs/not-a-uuid", nil, []int{404}},
		{"discovery job with an invalid collector_id", w.orgA, http.MethodPost, "/api/v1/discovery/jobs", map[string]any{"collector_id": "not-a-uuid",
			"type": "network_scan"}, []int{400, 404, 422}},
		{"discovery job with a client-2 collector by client 1", client1, http.MethodPost, "/api/v1/discovery/jobs", map[string]any{"collector_id": collector2,
			"type": "network_scan"}, []int{400, 403, 404, 422}},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, body := w.do(c.p, c.method, c.path, c.body)
			for _, m := range append([]string{"idx_", "_key\"", "_check", "_fkey", "pgx", "policy for table", "relation \"", "discovery_scope",
				"discovery_job", "ip_address"}, t1LeakMarkers...) {
				if strings.Contains(body, m) {
					t.Errorf("%s %s answered %d and discloses database text %q: %s", c.method, c.path, code, m, trim(body))
					break
				}
			}
			ok := false
			for _, s := range c.want {
				ok = ok || code == s
			}
			if !ok {
				t.Errorf("%s %s answered %d, want one of %v: %s", c.method, c.path, code, c.want, trim(body))
			}
		})
	}
	// None of the refused writes left a row.
	if n := w.scalar(`SELECT COUNT(*)::text FROM discovery_scope WHERE name LIKE 'core-err-%'`); n != "0" {
		t.Errorf("%s discovery scopes stored by refused requests, want 0", n)
	}
	if n := w.scalar(`SELECT COUNT(*)::text FROM subnet WHERE name = 'core-err-hijack' OR cidr::text LIKE '10.247.8.%'`); n != "0" {
		t.Errorf("%s subnets changed or stored by refused requests, want 0", n)
	}
	if n := w.scalar(`SELECT COUNT(*)::text FROM vrf WHERE name = 'core-err-hijack'`); n != "0" {
		t.Errorf("organization-B VRF renamed through organization A")
	}
	if n := w.scalar(`SELECT COUNT(*)::text FROM collector WHERE name = 'core-err-hijack'`); n != "0" {
		t.Errorf("organization-B collector renamed through organization A")
	}
}

// TestCoreChannelsSharedSavedView covers WP-015: a saved view an org-wide
// user shares with the organization runs for a principal of client 1 in that
// principal's scope. Executing its filter (POST /search/query) returns only
// client-1 rows, also when the view names client 2 in its filter; the owner
// sees the rows of both clients (control).
func TestCoreChannelsSharedSavedView(t *testing.T) {
	w := newT1World(t, "41")
	f := w.f
	ci1 := f.CI(t, f.OrgA, f.Client1, "core-view-c1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "core-view-c2")
	// The client-1 user holds the engineer role in client 1 only; the views
	// belong to the org-wide user of the world.
	member := f.AppUser(t, f.OrgA, "core-view-member")
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id)
		SELECT $1, $2, id, $3 FROM role WHERE organization_id = $1 AND name = 'engineer'`, f.OrgA, member, f.Client1); err != nil {
		t.Fatalf("assign engineer in client 1: %v", err)
	}
	client1 := &channelPrincipal{kind: "client1", org: f.OrgA, user: member, scope: w.as("", only(f.Client1), all()).scope}

	for _, v := range []struct {
		name   string
		filter map[string]any
		owner  map[string]bool // CIs the owner's execution returns
	}{
		{"core-view-all", map[string]any{"entity_kind": "ci", "search": "core-view-"}, map[string]bool{ci1: true, ci2: true}},
		{"core-view-client2", map[string]any{"entity_kind": "ci", "search": "core-view-", "client_id": f.Client2}, map[string]bool{ci1: false, ci2: true}},
	} {
		t.Run(v.name, func(t *testing.T) {
			id := w.create(w.orgA, "/api/v1/saved-views", map[string]any{"name": v.name, "entity_kind": "ci", "filter_spec": v.filter, "shared": true})
			code, body := w.do(client1, http.MethodGet, "/api/v1/saved-views/"+id, nil)
			if code != http.StatusOK {
				t.Fatalf("GET shared view as client 1: %d %s, want 200", code, trim(body))
			}
			var view struct {
				FilterSpec map[string]any `json:"filter_spec"`
			}
			if err := json.Unmarshal([]byte(body), &view); err != nil {
				t.Fatalf("decode view: %v", err)
			}
			run := func(p *channelPrincipal) map[string]bool {
				t.Helper()
				code, body := w.do(p, http.MethodPost, "/api/v1/search/query?limit=200", view.FilterSpec)
				if code != http.StatusOK {
					t.Fatalf("POST /search/query as %s: %d %s", p.kind, code, trim(body))
				}
				return coreIDs(t, body)
			}
			got := run(client1)
			if got[ci2] {
				t.Errorf("the shared view returns the client-2 CI to client 1")
			}
			if want := v.owner[ci1]; got[ci1] != want {
				t.Errorf("the shared view returns the client-1 CI to client 1: %v, want %v", got[ci1], want)
			}
			owner := run(w.orgA)
			for ci, want := range v.owner {
				if owner[ci] != want {
					t.Errorf("the view returns CI %s to its org-wide owner: %v, want %v", ci, owner[ci], want)
				}
			}
		})
	}
}
