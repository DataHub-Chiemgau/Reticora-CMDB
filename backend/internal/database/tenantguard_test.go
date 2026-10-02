package database

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// backendRoot is the module root seen from this package.
const backendRoot = "../.."

// poolMethods are the pgxpool.Pool methods that run SQL or hand out a
// connection. Ping, Stat and Close are not listed: they touch no table.
var poolMethods = map[string]bool{
	"Query": true, "QueryRow": true, "Exec": true, "Begin": true, "BeginTx": true,
	"SendBatch": true, "CopyFrom": true, "Acquire": true, "AcquireFunc": true,
}

// poolName is the naming convention for *pgxpool.Pool values; the guard
// recognises pool receivers by name, so the convention is enforced as well.
var poolName = regexp.MustCompile(`(?i)pool$`)

// TestNoDatabaseAccessOutsideTenantContext is the architecture test of
// TEN-06 (WP-041): outside this package every database access runs through
// WithTenant (or WithRequestTenant) or WithSystem. A direct call of a pool
// method is only allowed in the documented system paths of
// tenantGuardAllowlist.
func TestNoDatabaseAccessOutsideTenantContext(t *testing.T) {
	calls, misnamed := scanBackend(t)

	for _, name := range misnamed {
		t.Errorf("%s: *pgxpool.Pool value must be named ...pool so the tenant guard sees its calls", name)
	}

	hit := map[string]bool{}
	for _, c := range calls {
		if _, ok := tenantGuardAllowlist[c.file]; ok {
			hit[c.file] = true
			continue
		}
		t.Errorf("%s: direct database access %s outside database.WithTenant/WithSystem (TEN-06); "+
			"use the tenant transaction or document a system path in tenantguard_allowlist.go", c.pos, c.call)
	}
	for file, reason := range tenantGuardAllowlist {
		if !hit[file] {
			t.Errorf("tenantGuardAllowlist entry %s (%s) matches no direct pool call any more; remove it", file, reason)
		}
	}
}

type poolCall struct {
	file string
	pos  string
	call string
}

// scanBackend parses every non-test Go file of the module outside this
// package and returns the direct pool method calls and the *pgxpool.Pool
// values that do not follow the naming convention.
func scanBackend(t *testing.T) ([]poolCall, []string) {
	t.Helper()
	fset := token.NewFileSet()
	var calls []poolCall
	var misnamed []string
	err := filepath.WalkDir(backendRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(backendRoot, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "internal/database" || strings.HasPrefix(rel, "internal/database/") ||
				d.Name() == "vendor" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok || !poolMethods[sel.Sel.Name] {
					return true
				}
				if name := lastName(sel.X); name != "" && poolName.MatchString(name) {
					calls = append(calls, poolCall{
						file: rel,
						pos:  rel + ":" + strconv.Itoa(fset.Position(node.Pos()).Line),
						call: name + "." + sel.Sel.Name,
					})
				}
			case *ast.Field:
				if isPoolType(node.Type) {
					for _, id := range node.Names {
						if !poolName.MatchString(id.Name) {
							misnamed = append(misnamed, rel+":"+strconv.Itoa(fset.Position(id.Pos()).Line)+" "+id.Name)
						}
					}
				}
			case *ast.ValueSpec:
				if node.Type != nil && isPoolType(node.Type) {
					for _, id := range node.Names {
						if !poolName.MatchString(id.Name) {
							misnamed = append(misnamed, rel+":"+strconv.Itoa(fset.Position(id.Pos()).Line)+" "+id.Name)
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan backend: %v", err)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].pos < calls[j].pos })
	return calls, misnamed
}

// lastName returns the identifier a call receiver ends with: pool for
// pool.Exec, pool for r.pool.Exec, AuditPool for opts.AuditPool.Exec.
func lastName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.ParenExpr:
		return lastName(e.X)
	}
	return ""
}

// isPoolType reports whether expr is *pgxpool.Pool.
func isPoolType(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "pgxpool" && sel.Sel.Name == "Pool"
}
