//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect test of the list
// contract through the production router and chain (chain helper of
// t2_auth_chain_test.go).
//
//   - WP-164 (API-02), finding P2 "Several [B] list endpoints are bare
//     arrays without pagination (credentials, permissions, role
//     permissions, team members)"; gaps (high) "No runtime check that list
//     handlers return data (array, never null) plus next_cursor and honour
//     limit" (TestAuditT2ListsAreCursorPages) and "The architecture test
//     does not detect bare-array list endpoints"
//     (TestAuditT2ContractHasNoBareArrayLists; TestEveryListIsCursorPaginated
//     only looks at schemas that have a data array). API-02: "Listen {data,
//     next_cursor}; ... limit Default 50, maximal 200". The handlers encode
//     slices directly and ignore limit, and the contract documents them as
//     arrays.
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2ListsAreCursorPages|TestAuditT2ContractHasNoBareArrayLists' ./internal/server/
package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestAuditT2ListsAreCursorPages: with more rows than the requested limit
// (3 credentials, the permission catalogue), every list answers
// {data: [...<= limit], next_cursor: "..."}.
func TestAuditT2ListsAreCursorPages(t *testing.T) {
	s := newT2Stack(t)
	admin, _ := s.login(t, identity.AllPermissions()...)

	for _, name := range []string{"t2-cred-a", "t2-cred-b", "t2-cred-c"} {
		w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/credentials", bearer: admin, contentType: "application/json",
			body: `{"name":"` + name + `","kind":"ssh_password","secret":{"username":"u","password":"p"}}`})
		if w.Code != http.StatusCreated {
			t.Fatalf("create credential %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{
		"/api/v1/credentials?limit=1",
		"/api/v1/permissions?limit=1",
	} {
		w := s.do(t2Request{method: http.MethodGet, path: path, bearer: admin})
		if w.Code != http.StatusOK {
			t.Errorf("GET %s: %d %s", path, w.Code, w.Body.String())
			continue
		}
		var page struct {
			Data       []json.RawMessage `json:"data"`
			NextCursor string            `json:"next_cursor"`
		}
		body := strings.TrimSpace(w.Body.String())
		if err := json.Unmarshal([]byte(body), &page); err != nil || page.Data == nil || len(page.Data) > 1 || page.NextCursor == "" {
			if len(body) > 120 {
				body = body[:120] + "..."
			}
			t.Errorf("GET %s: body %s; want {data: [at most 1 entry], next_cursor: non-empty} (API-02)", path, body)
		}
	}
}

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
