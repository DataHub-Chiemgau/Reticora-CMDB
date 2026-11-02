package server_test

// WP-687 (CI-12, JOB-03, F-416): the unmerge and the revert of the merge job
// through the real router (server.NewRouter with the OpenAPI validation,
// newAuditT3Router). After the merges a->b and b->c the survivor b of the
// first merge is the victim of an open merge: POST /cis/{a}/unmerge (also
// with force=true) and POST /jobs/{job of a->b}/revert answer 409
// problem+json naming the merge b->c and change nothing. Once the merge
// b->c is reverted, the revert of a->b is refused only for the later change
// of b (E-80, unmerge without force).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -run 'TestMergeJobRevertRefusesWhileSurvivorIsMergedVictim' ./internal/server/

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestMergeJobRevertRefusesWhileSurvivorIsMergedVictim(t *testing.T) {
	r := newAuditT3Router(t, "d8")
	f := r.f
	perms := identity.AllPermissions()
	a, b, c := f.CI(t, f.OrgA, f.Client1, "wp687-a"), f.CI(t, f.OrgA, f.Client1, "wp687-b"), f.CI(t, f.OrgA, f.Client1, "wp687-c")
	merge := func(survivor, victim string) (mergeID, jobID string) {
		t.Helper()
		w := r.call(perms, http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`)
		var out struct {
			MergeID string `json:"merge_id"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("merge %s into %s: %d %s", victim, survivor, w.Code, w.Body.String())
		}
		return out.MergeID, r.str(`SELECT id::text FROM job WHERE organization_id = $1 AND kind = 'ci.merge' AND ref_id = $2`, f.OrgA, out.MergeID)
	}
	state := func() string {
		return r.str(`SELECT string_agg(format('%s %s %s %s', c.id, c.deleted_at IS NOT NULL, COALESCE(c.merged_into_id::text, '-'), c.version), '; ' ORDER BY c.id)
			|| ' open=' || (SELECT count(*) FROM ci_merge WHERE victim_id = ANY($1::uuid[]) AND unmerged_at IS NULL)
			|| ' reverted=' || (SELECT count(*) FROM job WHERE organization_id = $2 AND kind = 'ci.merge' AND reverted_at IS NOT NULL)
			FROM ci c WHERE c.id = ANY($1::uuid[])`, []string{a, b, c}, f.OrgA)
	}
	_, jobAB := merge(b, a)
	mergeBC, jobBC := merge(c, b)
	before := state()

	for _, path := range []string{"/api/v1/cis/" + a + "/unmerge", "/api/v1/cis/" + a + "/unmerge?force=true", "/api/v1/jobs/" + jobAB + "/revert"} {
		w := r.call(perms, http.MethodPost, path, "")
		var p struct {
			Status int    `json:"status"`
			Detail string `json:"detail"`
		}
		if w.Code != http.StatusConflict || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Status != http.StatusConflict ||
			!strings.Contains(p.Detail, "unmerge that merge first: merge "+mergeBC) ||
			!strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("POST %s while b is merged into c: %d %s, want 409 problem+json naming the merge %s", path, w.Code, w.Body.String(), mergeBC)
		}
		assertNoLeak(t, "refusal of "+path, w.Body.String())
		if after := state(); after != before {
			t.Errorf("POST %s changed\n%s\nbefore\n%s", path, after, before)
		}
	}

	// The later merge first: its revert passes, then a->b is refused only
	// for the later change of b (the revert never forces, E-80).
	if w := r.call(perms, http.MethodPost, "/api/v1/jobs/"+jobBC+"/revert", ""); w.Code != http.StatusOK {
		t.Fatalf("revert of the merge b->c: %d %s", w.Code, w.Body.String())
	}
	if w := r.call(perms, http.MethodPost, "/api/v1/jobs/"+jobAB+"/revert", ""); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "the survivor changed after the merge") {
		t.Errorf("revert of the merge a->b after b->c was reverted: %d %s, want 409 for the later change of b", w.Code, w.Body.String())
	}
	if w := r.call(perms, http.MethodPost, "/api/v1/cis/"+a+"/unmerge?force=true", ""); w.Code != http.StatusOK {
		t.Errorf("forced unmerge of a after b->c was reverted: %d %s", w.Code, w.Body.String())
	}
	if got := r.str(`SELECT count(*)::text FROM ci WHERE id = ANY($1::uuid[]) AND (deleted_at IS NOT NULL OR merged_into_id IS NOT NULL)`,
		[]string{a, b, c}); got != "0" {
		t.Errorf("after both unmerges %s CIs still merged: %s", got, state())
	}
}
