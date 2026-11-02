package rls_test

// Phase-1 acceptance audit, group T1-tenancy: REGRESSION test (passes).
//
// WP-010 (REL-01, TOP-01, TEN-06) gap "Channel matrix accepts 500 as a
// refusal and never requests topology for an invisible root" and WP-208
// (TEN-09, TST-02) P2 "Kanalmatrix ... Topologie und Impact mit unsichtbarer
// Wurzel per continue übersprungen; Fremdverweise akzeptieren jeden
// Nicht-2xx-Code": a relationship to a CI outside the scope is refused and
// not stored; neighbours and impact of an invisible root answer 404. (The
// exact refusal status, 422 for POST /relationships and 404 for GET
// /topology?root_ci_id, is checked by TestScopeRefusalsAreNot5xx.)
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1TopologyChannelInvisibleRoots' ./internal/tenant/rls/

import (
	"net/http"
	"testing"
)

func TestAuditT1TopologyChannelInvisibleRoots(t *testing.T) {
	w := newT1World(t, "cf")
	f := w.f
	ci1, ci2 := f.CI(t, f.OrgA, f.Client1, "t1-topo-c1"), f.CI(t, f.OrgA, f.Client2, "t1-topo-c2")
	ciB := f.CI(t, f.OrgB, "", "t1-topo-b")
	client1 := w.as("client1", only(f.Client1), all())
	// Positive control: the org-wide principal links and traverses.
	w.create(w.orgA, "/api/v1/relationships", map[string]any{"source_ci_id": ci1, "target_ci_id": ci2, "rel_type": "depends_on"})
	if code, body := w.do(w.orgA, http.MethodGet, "/api/v1/topology?root_ci_id="+ci2, nil); code != http.StatusOK {
		t.Fatalf("control: org-wide topology of the client-2 CI: %d %s", code, trim(body))
	}

	t.Run("relationships to CIs outside the scope", func(t *testing.T) {
		for _, c := range []struct {
			p              *channelPrincipal
			source, target string
		}{{client1, ci1, ci2}, {client1, ci2, ci1}, {w.orgB, ciB, ci1}} {
			code, body := w.do(c.p, http.MethodPost, "/api/v1/relationships", map[string]any{"source_ci_id": c.source, "target_ci_id": c.target, "rel_type": "runs_on"})
			if code < 400 {
				t.Errorf("POST /relationships %s->%s as %s: %d %s, want refused", c.source, c.target, c.p.kind, code, trim(body))
			}
			if n := w.scalar(`SELECT count(*)::text FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2 AND rel_type = 'runs_on'`, c.source, c.target); n != "0" {
				t.Errorf("refused relationship %s->%s as %s was stored (%s rows)", c.source, c.target, c.p.kind, n)
			}
		}
	})
	t.Run("neighbours and impact of an invisible root", func(t *testing.T) {
		for _, c := range []struct {
			p    *channelPrincipal
			root string
		}{{client1, ci2}, {w.orgB, ci1}} {
			for _, path := range []string{
				"/api/v1/topology/cis/" + c.root + "/neighbors",
				"/api/v1/topology/cis/" + c.root + "/impact",
				"/api/v1/impact/" + c.root,
			} {
				if code, body := w.do(c.p, http.MethodGet, path, nil); code != http.StatusNotFound {
					t.Errorf("GET %s as %s: %d %s, want 404", path, c.p.kind, code, trim(body))
				}
			}
		}
	})
}
