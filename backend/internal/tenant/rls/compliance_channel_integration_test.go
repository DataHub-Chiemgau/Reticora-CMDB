package rls_test

// Compliance evaluation through the complete PostgreSQL router (WP-339,
// F-001, TEN-06, TEN-09). The evaluation replaces the results of the whole
// organization, so only an org-wide principal may run it (E-09); a principal
// restricted to a client or a team is refused before anything is read or
// written (E-08).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -run '^TestComplianceEvaluationStaysInCallerScope$' ./internal/tenant/rls/

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestComplianceEvaluationStaysInCallerScope: a client-1 principal and a
// team-scoped principal trigger POST /api/v1/compliance/evaluations. Both
// get 403, the answer does not name the client-2 CI, and the stored client-2
// result keeps its id and evaluation time. The org-wide evaluation covers
// both clients, and the scoped result list stays correct.
func TestComplianceEvaluationStaysInCallerScope(t *testing.T) {
	w := newT1World(t, "d0")
	f := w.f
	own := f.CI(t, f.OrgA, f.Client1, "t1-comp-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "t1-comp-c2")
	client1 := w.as("client1", only(f.Client1), all())
	team := &channelPrincipal{kind: "team", org: f.OrgA, user: w.userA,
		scope: identity.Scope{Clients: all(), Sites: all(), Teams: only(uuid.NewString())}}

	w.create(w.orgA, "/api/v1/compliance/rules", map[string]any{
		"name": "t1 has name", "severity": "low", "category": "hygiene",
		"expression": map[string]any{"field": "name", "op": "not_empty"},
	})
	// Positive control: the org-wide evaluation, the job
	// compliance.evaluate (WP-1126), covers both clients.
	code, body := w.do(w.orgA, http.MethodPost, "/api/v1/compliance/evaluations", nil)
	if code != http.StatusAccepted {
		t.Fatalf("org-wide evaluation: %d %s, want 202 with the job", code, trim(body))
	}
	deadline := time.Now().Add(15 * time.Second)
	for w.scalar(`SELECT count(*)::text FROM compliance_result WHERE ci_id IN ($1, $2)`, own, foreign) != "2" {
		if time.Now().After(deadline) {
			t.Fatal("the evaluation job did not evaluate both CIs")
		}
		time.Sleep(50 * time.Millisecond)
	}
	before := w.scalar(`SELECT id::text || '|' || evaluated_at::text FROM compliance_result WHERE ci_id = $1`, foreign)

	for _, p := range []*channelPrincipal{client1, team} {
		code, body = w.do(p, http.MethodPost, "/api/v1/compliance/evaluations", nil)
		if code != http.StatusForbidden {
			t.Errorf("POST /compliance/evaluations as %s: %d %s, want 403", p.kind, code, trim(body))
		}
		if strings.Contains(body, foreign) {
			t.Errorf("TEN-06/TEN-09: POST /compliance/evaluations as %s lists the client-2 CI %s", p.kind, foreign)
		}
		if after := w.scalar(`SELECT id::text || '|' || evaluated_at::text FROM compliance_result WHERE ci_id = $1`, foreign); after != before {
			t.Errorf("TEN-06/TEN-09: the client-2 compliance result was rewritten by %s (before %s, after %s)", p.kind, before, after)
		}
	}
	// The scoped result list stays correct.
	if code, body = w.do(client1, http.MethodGet, "/api/v1/compliance/results?limit=100", nil); code != http.StatusOK || strings.Contains(body, foreign) || !strings.Contains(body, own) {
		t.Errorf("GET /compliance/results as client 1: %d, contains own CI %v, client-2 CI %v; want 200 with only the own CI",
			code, strings.Contains(body, own), strings.Contains(body, foreign))
	}
}
