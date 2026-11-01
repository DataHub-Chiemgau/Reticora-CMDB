package server_test

// WP-672 (API-02), finding F-401: an empty list answers "data": [], never
// null, over the PostgreSQL repositories as cmd/server wires them; the
// search without a hit (SearchResponse) was the remaining list of F-214's
// class, the history of an entity without changes, the availability
// projection and the results of the compliance score had the same nil
// slice.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run '^TestEmptyResponsesAnswerArraysOverPostgres$' ./internal/server/

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"gopkg.in/yaml.v3"
)

// TestEmptyResponsesAnswerArraysOverPostgres sends every GET of the
// contract as an org-wide caller with every permission, path parameters
// replaced by an unknown UUID, and requires each 200 with a data member to
// carry an array; the compliance score carries its results and by_ci_type
// as arrays.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEmptyResponsesAnswerArraysOverPostgres(t *testing.T) {
	raw, err := os.ReadFile(paginationSpecPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	r := newAuditT3Router(t, "f6")
	param := regexp.MustCompile(`\{[^}]+\}`)
	const unknown = "00000000-0000-4000-8000-0000000000aa"
	var paths []string
	for p, ops := range doc.Paths {
		if _, ok := ops["get"]; ok {
			paths = append(paths, param.ReplaceAllString(p, unknown))
		}
	}
	sort.Strings(paths)
	// The search with a text and a type, and the history of each entity
	// type ({entityType} above is no type).
	paths = append(paths, "/api/v1/search?q=no-such-text", "/api/v1/search?q=no-such-text&type=ci")
	for _, typ := range []string{"ci", "relationship", "location", "asset", "reservation", "composition"} {
		paths = append(paths, "/api/v1/history/"+typ+"/"+unknown)
	}
	answered := 0
	for _, path := range paths {
		w := r.call(identity.AllPermissions(), http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			continue
		}
		var members map[string]json.RawMessage
		if json.Unmarshal(w.Body.Bytes(), &members) != nil {
			continue
		}
		answered++
		want := []string{"data"}
		if path == "/api/v1/compliance/score" {
			want = []string{"results", "by_ci_type"}
		}
		for _, name := range want {
			if v, ok := members[name]; (ok || name != "data") && (len(v) == 0 || v[0] != '[') {
				t.Errorf("GET %s: %q is %s, want an array; body %.200s", path, name, v, w.Body.String())
			}
		}
	}
	if answered < 100 {
		t.Fatalf("only %d of %d GETs answered 200 with a JSON object; the sweep covers too little", answered, len(paths))
	}
}
