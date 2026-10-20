//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect test of the AI
// opt-in contract.
//
//   - WP-076 (AI-02), finding P2 "The organization AI opt-in cannot be set
//     through any API, UI or operator path": no write operation of the
//     contract carries ai_opt_in (TestAuditT2AIOptInSettableThroughAPI).
//     (The opt-in on the Ask path is a regression test since WP-360: see
//     t2_ai_optin_test.go.)
//
// Retest (needs no database):
//
//	cd backend && go test -tags auditdefect -count=1 -v -run '^TestAuditT2AIOptInSettableThroughAPI$' ./internal/server/
package server_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAuditT2AIOptInSettableThroughAPI: AI-02 "Opt-in pro Org" is a decision
// of the organization (or the operator); the contract must offer a write
// operation (POST, PUT or PATCH) whose request body carries ai_opt_in.
func TestAuditT2AIOptInSettableThroughAPI(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	resolve := func(v any) map[string]any {
		m, _ := v.(map[string]any)
		for m != nil {
			ref, ok := m["$ref"].(string)
			if !ok {
				break
			}
			var cur any = doc
			for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				cur = cur.(map[string]any)[part]
			}
			m, _ = cur.(map[string]any)
		}
		return m
	}
	var hasOptIn func(schema map[string]any, depth int) bool
	hasOptIn = func(schema map[string]any, depth int) bool {
		if schema == nil || depth > 8 {
			return false
		}
		if props, ok := schema["properties"].(map[string]any); ok {
			if _, ok := props["ai_opt_in"]; ok {
				return true
			}
		}
		for _, key := range []string{"allOf", "oneOf", "anyOf"} {
			subs, _ := schema[key].([]any)
			for _, sub := range subs {
				if hasOptIn(resolve(sub), depth+1) {
					return true
				}
			}
		}
		return false
	}
	var writers []string
	bodies := 0
	for path, item := range doc["paths"].(map[string]any) {
		for _, method := range []string{"post", "put", "patch"} {
			op, ok := item.(map[string]any)[method].(map[string]any)
			if !ok {
				continue
			}
			body := resolve(op["requestBody"])
			content, _ := body["content"].(map[string]any)
			media, _ := content["application/json"].(map[string]any)
			if media != nil {
				bodies++
			}
			if hasOptIn(resolve(media["schema"]), 0) {
				writers = append(writers, strings.ToUpper(method)+" "+path)
			}
		}
	}
	if bodies < 50 {
		t.Fatalf("only %d JSON request bodies found; the spec reader is broken", bodies)
	}
	if len(writers) == 0 {
		t.Errorf("no POST/PUT/PATCH operation of api/openapi.yaml accepts ai_opt_in; the organization AI opt-in of AI-02 can only be set by SQL")
	}
}
