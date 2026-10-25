package rls_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestScopeRefusalsAreNot5xx covers WP-010, WP-208, WP-401 and WP-403
// (TEN-06, TEN-09; F-145, F-161) through the complete PostgreSQL router: a
// relationship whose endpoint lies outside the caller's client scope or
// organization is refused with 422, and the topology of a root CI the
// caller does not see answers 404 "ci not found" like an unknown id, like
// every other out-of-scope access (TEN-06: a wrong scope yields no rows).
// Both answered 500 "the request could not be completed" before, so the
// channel matrix could not tell a refusal from a broken path and monitoring
// counted 5xx. A root that is no UUID is 400.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestScopeRefusalsAreNot5xx(t *testing.T) {
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
		want         int
		// detail is the expected problem detail; hidden must not appear in
		// the answer.
		detail, hidden string
	}{
		{"client 1 links its CI to a client-2 CI", client1, http.MethodPost, "/api/v1/relationships",
			map[string]any{"source_ci_id": ci1, "target_ci_id": ci2, "rel_type": "runs_on"}, http.StatusUnprocessableEntity, "", ""},
		{"client 1 links a client-2 CI to its CI", client1, http.MethodPost, "/api/v1/relationships",
			map[string]any{"source_ci_id": ci2, "target_ci_id": ci1, "rel_type": "runs_on"}, http.StatusUnprocessableEntity, "", ""},
		{"organization B links to a CI of organization A", w.orgB, http.MethodPost, "/api/v1/relationships",
			map[string]any{"source_ci_id": ciB, "target_ci_id": ci1, "rel_type": "runs_on"}, http.StatusUnprocessableEntity, "", ""},
		{"client 1 reads the topology of a client-2 root", client1, http.MethodGet, "/api/v1/topology?root_ci_id=" + ci2, nil,
			http.StatusNotFound, "ci not found", "t1-refusal-c2"},
		{"organization B reads the topology of an organization-A root", w.orgB, http.MethodGet, "/api/v1/topology?root_ci_id=" + ci1, nil,
			http.StatusNotFound, "ci not found", "t1-refusal-c1"},
		{"organization A reads the topology of an unknown root", w.orgA, http.MethodGet,
			"/api/v1/topology?root_ci_id=00000000-0000-4000-8000-0000000000ff", nil, http.StatusNotFound, "ci not found", ""},
		{"organization A reads the topology of a root that is no UUID", w.orgA, http.MethodGet, "/api/v1/topology?root_ci_id=not-a-uuid", nil,
			http.StatusBadRequest, "root_ci_id: must be a UUID", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, body := w.do(c.p, c.method, c.path, c.body)
			if code != c.want {
				t.Errorf("TEN-06: %s %s as %s answered %d %s, want %d", c.method, c.path, c.p.kind, code, trim(body), c.want)
			}
			var p struct {
				Detail string `json:"detail"`
			}
			if c.detail != "" && (json.Unmarshal([]byte(body), &p) != nil || p.Detail != c.detail) {
				t.Errorf("%s %s as %s: detail of %s, want %q", c.method, c.path, c.p.kind, trim(body), c.detail)
			}
			if c.hidden != "" && strings.Contains(body, c.hidden) {
				t.Errorf("%s %s as %s names %s: %s", c.method, c.path, c.p.kind, c.hidden, trim(body))
			}
		})
	}
}
