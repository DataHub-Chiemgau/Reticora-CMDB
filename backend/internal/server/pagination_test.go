package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"gopkg.in/yaml.v3"
)

// notALists are GET operations whose body has a data array but that are no
// list resources: a file export streams every row in one document.
var notALists = map[string]string{
	"/api/v1/export/cis": "export download (JSON file of the whole selection)",
	"/api/v1/modules":    "the fixed module catalog of the release (WP-214), no tenant data",
}

// TestEveryListIsCursorPaginated is the architecture test of API-02 over
// the contract: every list (GET with a data array) documents next_cursor,
// every list that takes a limit also takes the signed cursor, and the
// shared limit allows at most 200.
func TestEveryListIsCursorPaginated(t *testing.T) {
	raw, err := os.ReadFile(specPath)
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
