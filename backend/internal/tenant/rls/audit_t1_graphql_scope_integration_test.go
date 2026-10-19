package rls_test

// Phase-1 acceptance audit, group T1-tenancy: REGRESSION test (passes).
//
// WP-208 (TEN-09, TST-02; GQL-04) gap "Alle GraphQL-Query-Felder ×
// Scope-Principals": TestChannelMatrix queries only the 'cis' field. Here
// every query field that returns tenant data (ci, relationships, topology,
// searchEverything, reviewInbox, jobs) is asked by an org-wide, a client-1
// and a foreign-organization principal through POST /bff/graphql of the
// complete PostgreSQL router. Ids and names of objects outside the scope
// never appear; ci(id) outside the scope is null; own objects appear.
// (rackLayout is covered by TestAuditT1RackChannelSiteScope.)
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1GraphQLFieldsHonourScope' ./internal/tenant/rls/

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAuditT1GraphQLFieldsHonourScope(t *testing.T) {
	w := newT1World(t, "fb")
	f := w.f
	// CIs are created through the API so the search index is written as in
	// production (ci.NewIndexingRepository).
	serverType := w.scalar(`SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`)
	newCI := func(p *channelPrincipal, name, client string) string {
		body := map[string]any{"ci_type_id": serverType, "name": name}
		if client != "" {
			body["client_id"] = client
		}
		return w.create(p, "/api/v1/cis", body)
	}
	ci1, ci2 := newCI(w.orgA, "t1gql-one", f.Client1), newCI(w.orgA, "t1gql-two", f.Client2)
	ciB := newCI(w.orgB, "t1gql-borg", "")
	client1 := w.as("client1", only(f.Client1), all())
	w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": ci1, "target_ci_id": ci2, "rel_type": "depends_on"})
	review2 := w.scalar(`INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids) VALUES ($1, 'probable_duplicate', $2, ARRAY[$3::uuid]) RETURNING id::text`,
		f.OrgA, fmt.Sprintf(`{"name":"t1gql-review","ci_id":%q}`, ci2), ci2)
	jobA := w.scalar(`INSERT INTO job (organization_id, kind, status) VALUES ($1, 't1gql.kind', 'queued') RETURNING id::text`, f.OrgA)
	jobB := w.scalar(`INSERT INTO job (organization_id, kind, status) VALUES ($1, 't1gql.kindb', 'queued') RETURNING id::text`, f.OrgB)

	queries := map[string]string{
		"ci(one)":          fmt.Sprintf(`{ ci(id: %q) { id name } }`, ci1),
		"ci(two)":          fmt.Sprintf(`{ ci(id: %q) { id name } }`, ci2),
		"relationships":    fmt.Sprintf(`{ relationships(ciId: %q) { id sourceCiId targetCiId sourceCi { id name } targetCi { id name } } }`, ci1),
		"topology":         fmt.Sprintf(`{ topology(rootCiId: %q, depth: 3) { nodes { id name } edges { sourceCiId targetCiId } } }`, ci1),
		"searchEverything": `{ searchEverything(q: "t1gql") { total hits { entityId title } } }`,
		"reviewInbox":      `{ reviewInbox(first: 100) { id payload candidates { id name } } }`,
		"jobs":             `{ jobs(first: 100) { id kind } }`,
	}
	// markers of the objects of client 2 and organization B; own[...] lists
	// what each principal must see in which query (positive controls).
	foreign := map[string][]string{
		"org":       {ciB, "t1gql-borg", jobB},
		"client1":   {ci2, "t1gql-two", review2, ciB, "t1gql-borg", jobB},
		"other-org": {ci1, ci2, "t1gql-one", "t1gql-two", review2, jobA},
	}
	own := map[string]map[string]string{
		"org":       {"ci(two)": "t1gql-two", "relationships": ci2, "topology": ci2, "searchEverything": "t1gql-two", "reviewInbox": review2, "jobs": jobA},
		"client1":   {"ci(one)": "t1gql-one", "searchEverything": "t1gql-one", "topology": ci1, "jobs": jobA},
		"other-org": {"jobs": jobB},
	}
	for _, p := range []*channelPrincipal{w.orgA, client1, w.orgB} {
		for name, q := range queries {
			code, body := w.do(p, http.MethodPost, "/bff/graphql", map[string]any{"query": q})
			if code != http.StatusOK {
				t.Errorf("%s as %s: %d %s", name, p.kind, code, trim(body))
				continue
			}
			for _, m := range foreign[p.kind] {
				if strings.Contains(body, m) {
					t.Errorf("TEN-09/GQL-04: GraphQL %s as %s discloses %q: %s", name, p.kind, m, trim(body))
				}
			}
			if want, ok := own[p.kind][name]; ok && !strings.Contains(body, want) {
				t.Errorf("GraphQL %s as %s does not show the own object %q: %s", name, p.kind, want, trim(body))
			}
			// ci(id) of an object outside the scope is null.
			if _, mine := own[p.kind][name]; strings.HasPrefix(name, "ci(") && !mine && p.kind != "org" && !strings.Contains(body, `"ci":null`) {
				t.Errorf("GraphQL %s as %s: %s, want ci null", name, p.kind, trim(body))
			}
		}
	}
}
