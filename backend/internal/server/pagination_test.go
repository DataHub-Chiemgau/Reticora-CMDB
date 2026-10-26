// Architecture and runtime tests of the list contract (API-02): every list
// is a cursor page {data, next_cursor}, never a bare array, and an empty
// list is "data": [] (WP-164, WP-466, F-075, F-214).
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestEveryListIsCursorPaginated|TestAuditT2ListsAreCursorPages|TestEmptyListsAnswerAnEmptyDataArray' ./internal/server/
package server_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"gopkg.in/yaml.v3"
)

// paginationSpecPath is the contract, relative to this package.
const paginationSpecPath = "../../../api/openapi.yaml"

// bareArrayListsUntilWP467 are the lists of the late-phase modules (E-03)
// that still answer a bare JSON array; WP-467 (t2-pagination-modules) turns
// them into cursor pages and removes this list (F-075).
var bareArrayListsUntilWP467 = map[string]string{
	"/api/v1/monitoring/metrics":   "WP-467 (t2-pagination-modules)",
	"/api/v1/monitoring/alerts":    "WP-467 (t2-pagination-modules)",
	"/api/v1/documents/{id}/links": "WP-467 (t2-pagination-modules)",
	"/api/v1/ai/conversations":     "WP-467 (t2-pagination-modules)",
}

// notALists are GET operations whose body has a data array but that are no
// list resources: a file export streams every row in one document.
var notALists = map[string]string{
	"/api/v1/export/cis": "export download (JSON file of the whole selection)",
	"/api/v1/modules":    "the fixed module catalog of the release (WP-214), no tenant data",
}

// TestEveryListIsCursorPaginated is the architecture test of API-02 over
// the contract: no GET answers a bare JSON array (apart from the transition
// list bareArrayListsUntilWP467), every list (GET with a data array)
// documents next_cursor, every list that takes a limit also takes the
// signed cursor, and the shared limit allows at most 200.
func TestEveryListIsCursorPaginated(t *testing.T) {
	raw, err := os.ReadFile(paginationSpecPath)
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
	var properties func(v any) map[string]any
	properties = func(v any) map[string]any {
		s := resolve(v)
		out := map[string]any{}
		if all, ok := s["allOf"].([]any); ok {
			for _, sub := range all {
				for k, p := range properties(sub) {
					out[k] = p
				}
			}
		}
		if props, ok := s["properties"].(map[string]any); ok {
			for k, p := range props {
				out[k] = p
			}
		}
		return out
	}

	limit := resolve(doc["components"].(map[string]any)["parameters"].(map[string]any)["Limit"])
	if maxLimit := limit["schema"].(map[string]any)["maximum"]; maxLimit != api.MaxPageLimit || api.MaxPageLimit != 200 {
		t.Errorf("limit maximum %v (api.MaxPageLimit %d), want 200", maxLimit, api.MaxPageLimit)
	}

	lists := 0
	var bare []string
	for path, item := range doc["paths"].(map[string]any) {
		get, ok := item.(map[string]any)["get"].(map[string]any)
		if !ok || notALists[path] != "" {
			continue
		}
		ok200 := resolve(get["responses"].(map[string]any)["200"])
		content, _ := ok200["content"].(map[string]any)
		media, _ := content["application/json"].(map[string]any)
		if media == nil {
			continue
		}
		isBare := resolve(media["schema"])["type"] == "array"
		_, transitional := bareArrayListsUntilWP467[path]
		switch {
		case isBare && !transitional:
			bare = append(bare, "GET "+path)
		case !isBare && transitional:
			t.Errorf("GET %s is no bare array any more: remove it from bareArrayListsUntilWP467", path)
		}
		props := properties(media["schema"])
		if data := resolve(props["data"]); data == nil || data["type"] != "array" {
			continue
		}
		lists++
		if _, ok := props["next_cursor"]; !ok {
			t.Errorf("GET %s: list without next_cursor", path)
		}
		params := map[string]bool{}
		for _, p := range asSlice(get["parameters"]) {
			if name, ok := resolve(p)["name"].(string); ok {
				params[name] = true
			}
		}
		if params["limit"] && !params["cursor"] {
			t.Errorf("GET %s: limit without cursor", path)
		}
	}
	if lists < 80 {
		t.Errorf("only %d lists found; the spec reader is broken", lists)
	}
	sort.Strings(bare)
	if len(bare) > 0 {
		t.Errorf("%d GET operations answer a bare JSON array instead of {data, next_cursor} (API-02, F-075):\n  %s", len(bare), strings.Join(bare, "\n  "))
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// TestOnlyTheAPIPackageParsesPagination: limit, offset and cursor are read
// by api.ParsePagination only, so every list gets the same default, cap and
// cursor verification (API-02).
func TestOnlyTheAPIPackageParsesPagination(t *testing.T) {
	root := "../"
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			strings.HasPrefix(filepath.ToSlash(path), "../api/") {
			return err
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			lit, isLit := call.Args[0].(*ast.BasicLit)
			if ok && isLit && sel.Sel.Name == "Get" {
				switch lit.Value {
				case `"limit"`, `"offset"`, `"cursor"`:
					if strings.Contains(exprString(sel.X), "Query()") {
						t.Errorf("%s: reads %s itself instead of api.ParsePagination", fset.Position(call.Pos()), lit.Value)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.CallExpr:
		return exprString(v.Fun) + "()"
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.Ident:
		return v.Name
	}
	return ""
}

// TestForgedCursorIsRejectedOnEveryRoute: the validation middleware of
// every route refuses a cursor the server did not sign, so no list can
// silently ignore it.
func TestForgedCursorIsRejectedOnEveryRoute(t *testing.T) {
	reached := false
	h := middleware.OpenAPIValidation(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	for _, cursor := range []string{"eyJpIjoiY2ktOSJ9", "eyJpIjoiY2ktOSJ9.AAAAAAAAAAAAAAAAAAAAAA"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/webhooks?cursor="+cursor, nil))
		if w.Code != http.StatusBadRequest || reached {
			t.Errorf("cursor %q: status %d, handler reached %v", cursor, w.Code, reached)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/webhooks?cursor="+api.EncodeOffsetCursor(50), nil))
	if !reached {
		t.Errorf("a signed cursor was refused: %d %s", w.Code, w.Body.String())
	}
}

// cursorPage is the body of a list: {data, next_cursor} (API-02).
type cursorPage struct {
	Data       []json.RawMessage `json:"data"`
	Total      int               `json:"total"`
	NextCursor string            `json:"next_cursor"`
}

// TestAuditT2ListsAreCursorPages: with more rows than the requested limit
// (3 credentials, the permission catalogue, the grants of a role, the
// members of a team, the role assignments of a user), every list answers
// {data: [...<= limit], next_cursor: "..."}, and following next_cursor
// returns every row once (WP-164, WP-466, F-075).
func TestAuditT2ListsAreCursorPages(t *testing.T) {
	s := newT2Stack(t)
	admin, _ := s.login(t, identity.AllPermissions()...)
	write := func(method, path, body string) map[string]any {
		t.Helper()
		w := s.do(t2Request{method: method, path: path, bearer: admin, contentType: "application/json", body: body})
		if w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	post := func(path, body string) map[string]any { t.Helper(); return write(http.MethodPost, path, body) }

	for _, name := range []string{"t2-cred-a", "t2-cred-b", "t2-cred-c"} {
		post("/api/v1/credentials", `{"name":"`+name+`","kind":"ssh_password","secret":{"username":"u","password":"p"}}`)
	}
	role, _ := post("/api/v1/roles", `{"name":"t2-paging"}`)["id"].(string)
	write(http.MethodPut, "/api/v1/roles/"+role+"/permissions", `{"permission_keys":["ci:read","ticket:read","asset:read"]}`)
	team, _ := post("/api/v1/teams", `{"name":"t2-paging"}`)["id"].(string)
	var users []string
	for _, name := range []string{"pa", "pb", "pc"} {
		id, _ := post("/api/v1/users", `{"email":"`+name+`@example.com","display_name":"`+name+`"}`)["id"].(string)
		users = append(users, id)
		post("/api/v1/teams/"+team+"/members", `{"user_id":"`+id+`"}`)
	}
	for _, r := range []string{"t2-paging-x", "t2-paging-y"} {
		other, _ := post("/api/v1/roles", `{"name":"`+r+`","permissions":["ci:read"]}`)["id"].(string)
		post("/api/v1/role-assignments", `{"user_id":"`+users[0]+`","custom_role_id":"`+other+`"}`)
	}

	for _, path := range []string{
		"/api/v1/credentials",
		"/api/v1/permissions",
		"/api/v1/roles/" + role + "/permissions",
		"/api/v1/teams/" + team + "/members",
		"/api/v1/users/" + users[0] + "/roles",
	} {
		seen := map[string]bool{}
		total := -1
		for next := path + "?limit=1"; next != ""; {
			w := s.do(t2Request{method: http.MethodGet, path: next, bearer: admin})
			if w.Code != http.StatusOK {
				t.Errorf("GET %s: %d %s", next, w.Code, w.Body.String())
				break
			}
			var page cursorPage
			body := strings.TrimSpace(w.Body.String())
			if err := json.Unmarshal([]byte(body), &page); err != nil || page.Data == nil || len(page.Data) > 1 ||
				(total < 0 && page.NextCursor == "") {
				if len(body) > 120 {
					body = body[:120] + "..."
				}
				t.Errorf("GET %s: body %s; want {data: [at most 1 entry], next_cursor: non-empty} (API-02)", next, body)
				break
			}
			total = page.Total
			for _, row := range page.Data {
				if seen[string(row)] {
					t.Errorf("GET %s: %s returned twice", next, row)
				}
				seen[string(row)] = true
			}
			next = ""
			if page.NextCursor != "" {
				next = path + "?limit=1&cursor=" + page.NextCursor
			}
		}
		if total < 2 || len(seen) != total {
			t.Errorf("GET %s: paged through %d of %d rows; want every row of a list with more than one", path, len(seen), total)
		}
	}
	// A forged cursor is 400 on the paged lists.
	w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/credentials?cursor=eyJvIjoxfQ.AAAAAAAAAAAAAAAAAAAAAA", bearer: admin})
	if w.Code != http.StatusBadRequest {
		t.Errorf("GET /credentials with a forged cursor: %d %s, want 400", w.Code, w.Body.String())
	}
}

// TestEmptyListsAnswerAnEmptyDataArray: an empty list of an organization
// without rows is "data": [], never null; api.ListResponse encodes a nil
// slice as [] whatever the repository returned (API-02, F-214 retest).
func TestEmptyListsAnswerAnEmptyDataArray(t *testing.T) {
	s := newT2Stack(t)
	admin, _ := s.login(t, identity.AllPermissions()...)
	for _, path := range []string{
		"/api/v1/ips?limit=1",
		"/api/v1/ip-addresses?limit=1",
		"/api/v1/credentials",
	} {
		w := s.do(t2Request{method: http.MethodGet, path: path, bearer: admin})
		var members map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &members); w.Code != http.StatusOK || err != nil || string(members["data"]) != "[]" {
			t.Errorf("GET %s: %d %s; want 200 with \"data\": []", path, w.Code, w.Body.String())
		}
	}
	raw, err := json.Marshal(api.ListResponse[string]{Total: 0})
	if err != nil || !strings.Contains(string(raw), `"data":[]`) {
		t.Errorf("ListResponse with nil data encodes as %s (%v); want \"data\":[]", raw, err)
	}
}
