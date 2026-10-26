//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect test of the list
// contract.
//
//   - WP-164 (API-02), finding F-075 "Several [B] list endpoints are bare
//     arrays without pagination"; gap (high) "The architecture test does
//     not detect bare-array list endpoints"
//     (TestAuditT2ContractHasNoBareArrayLists). API-02: "Listen {data,
//     next_cursor}; ... limit Default 50, maximal 200". WP-466 paged the
//     credentials, the permission catalogue and the role and team
//     sub-lists (their runtime test TestAuditT2ListsAreCursorPages moved to
//     pagination_test.go); the module lists of WP-467 still answer bare
//     arrays.
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2ContractHasNoBareArrayLists' ./internal/server/
package server_test

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAuditT2ContractHasNoBareArrayLists: no GET operation of the contract
// answers 200 with a bare JSON array; every list is {data, next_cursor}.
func TestAuditT2ContractHasNoBareArrayLists(t *testing.T) {
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
	var bare []string
	gets := 0
	for path, item := range doc["paths"].(map[string]any) {
		get, ok := item.(map[string]any)["get"].(map[string]any)
		if !ok {
			continue
		}
		gets++
		responses, _ := get["responses"].(map[string]any)
		ok200 := resolve(responses["200"])
		content, _ := ok200["content"].(map[string]any)
		media, _ := content["application/json"].(map[string]any)
		if schema := resolve(media["schema"]); schema != nil && schema["type"] == "array" {
			bare = append(bare, "GET "+path)
		}
	}
	if gets < 100 {
		t.Fatalf("only %d GET operations found; the spec reader is broken", gets)
	}
	sort.Strings(bare)
	if len(bare) > 0 {
		t.Errorf("%d GET operations answer a bare JSON array instead of {data, next_cursor} (API-02):\n  %s", len(bare), strings.Join(bare, "\n  "))
	}
}
