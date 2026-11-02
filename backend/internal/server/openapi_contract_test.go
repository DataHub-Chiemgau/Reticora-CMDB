package server

import (
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// contractResponse is an OpenAPI response or a reference to one.
type contractResponse struct {
	Ref     string               `yaml:"$ref"`
	Content map[string]yaml.Node `yaml:"content"`
}

// problemContractExceptions are the error responses that are no RFC 7807
// problem: the readiness probe answers 503 with its check list (OPS-01).
var problemContractExceptions = []string{"GET /readyz 503"}

// TestOpenAPIErrorResponsesAreProblemJSON covers WP-568 (API-03, F-213):
// every 4xx and 5xx response of the API contract that declares a body
// declares application/problem+json and nothing else, as the server
// answers every error (api.WriteJSON, api.WriteProblemBody); the only
// exception is /readyz. A response named by $ref is checked through its
// component.
func TestOpenAPIErrorResponsesAreProblemJSON(t *testing.T) {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Responses map[string]contractResponse `yaml:"responses"`
		} `yaml:"components"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", specPath, err)
	}
	resolve := func(r contractResponse) (contractResponse, bool) {
		for seen := 0; r.Ref != "" && seen < 8; seen++ {
			next, ok := doc.Components.Responses[strings.TrimPrefix(r.Ref, "#/components/responses/")]
			if !ok {
				return r, false
			}
			r = next
		}
		return r, r.Ref == ""
	}
	methods := []string{"get", "put", "post", "delete", "patch", "head", "options"}
	checked := 0
	var violations []string
	for path, item := range doc.Paths {
		for method, node := range item {
			if !slices.Contains(methods, method) {
				continue
			}
			var op struct {
				Responses map[string]contractResponse `yaml:"responses"`
			}
			if err := node.Decode(&op); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			for code, response := range op.Responses {
				if code == "" || (code[0] != '4' && code[0] != '5') {
					continue
				}
				name := strings.ToUpper(method) + " " + path + " " + code
				resolved, ok := resolve(response)
				if !ok {
					violations = append(violations, name+": unresolved "+response.Ref)
					continue
				}
				if len(resolved.Content) == 0 || slices.Contains(problemContractExceptions, name) {
					continue
				}
				checked++
				mediaTypes := make([]string, 0, len(resolved.Content))
				for mediaType := range resolved.Content {
					mediaTypes = append(mediaTypes, mediaType)
				}
				sort.Strings(mediaTypes)
				if !slices.Equal(mediaTypes, []string{"application/problem+json"}) {
					violations = append(violations, name+": "+strings.Join(mediaTypes, ", "))
				}
			}
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Errorf("error responses not declared as application/problem+json (API-03):\n  %s", strings.Join(violations, "\n  "))
	}
	// The scan must have seen the contract: well over a thousand error
	// responses declare a body.
	if checked < 1000 {
		t.Errorf("checked %d error responses with a body; the scan missed the contract", checked)
	}
}
