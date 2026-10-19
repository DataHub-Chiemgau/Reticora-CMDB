//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT test (it fails until
// the product is fixed). Found while closing the WP-010 / WP-208 test gaps.
//
// WP-010 (REL-01, TOP-01, TEN-06) gap "Channel matrix accepts 500 as a
// refusal and never requests topology for an invisible root" / WP-208
// (TEN-09, TST-02) P2 "Kanalmatrix ... 500-Antworten (Topologie,
// Fremdkante)": a relationship whose endpoint lies outside the caller's
// client scope or organization, and the topology of an invisible root CI,
// must be refused as a client error (404, or 422 for the relationship) like
// every other out-of-scope access (TEN-06: a wrong scope yields no rows).
// Today POST /api/v1/relationships and GET /api/v1/topology?root_ci_id=
// answer 500 "the request could not be completed", so the matrix cannot
// tell a refusal from a broken path and monitoring counts 5xx.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT1ScopeRefusalsAreNot5xx' ./internal/tenant/rls/

import (
	"net/http"
	"testing"
)

func TestAuditT1ScopeRefusalsAreNot5xx(t *testing.T) {
	w := newT1World(t, "fa")
	f := w.f
	ci1, ci2 := f.CI(t, f.OrgA, f.Client1, "t1-refusal-c1"), f.CI(t, f.OrgA, f.Client2, "t1-refusal-c2")
	ciB := f.CI(t, f.OrgB, "", "t1-refusal-b")
	client1 := w.as("client1", only(f.Client1), all())
	// Positive control: the org-wide principal links and traverses.
	w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": ci1, "target_ci_id": ci2, "rel_type": "depends_on"})
	if code, body := w.do(w.orgA, http.MethodGet, "/api/v1/topology?root_ci_id="+ci2, nil); code != http.StatusOK {
		t.Fatalf("control: org-wide topology of the client-2 CI: %d %s", code, trim(body))
	}
	for _, c := range []struct {
		name         string
		p            *channelPrincipal
		method, path string
		body         any
		want         []int
	}{
		{"client 1 links its CI to a client-2 CI", client1, http.MethodPost, "/api/v1/relationships",
			map[string]any{"source_ci_id": ci1, "target_ci_id": ci2, "rel_type": "runs_on"}, []int{404, 422}},
		{"client 1 links a client-2 CI to its CI", client1, http.MethodPost, "/api/v1/relationships",
			map[string]any{"source_ci_id": ci2, "target_ci_id": ci1, "rel_type": "runs_on"}, []int{404, 422}},
		{"organization B links to a CI of organization A", w.orgB, http.MethodPost, "/api/v1/relationships",
			map[string]any{"source_ci_id": ciB, "target_ci_id": ci1, "rel_type": "runs_on"}, []int{404, 422}},
		{"client 1 reads the topology of a client-2 root", client1, http.MethodGet, "/api/v1/topology?root_ci_id=" + ci2, nil, []int{404}},
		{"organization B reads the topology of an organization-A root", w.orgB, http.MethodGet, "/api/v1/topology?root_ci_id=" + ci1, nil, []int{404}},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, body := w.do(c.p, c.method, c.path, c.body)
			ok := false
			for _, s := range c.want {
				ok = ok || code == s
			}
			if !ok {
				t.Errorf("TEN-06: %s %s as %s answered %d %s, want one of %v", c.method, c.path, c.p.kind, code, trim(body), c.want)
			}
		})
	}
}
