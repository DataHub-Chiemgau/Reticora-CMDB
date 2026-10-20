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

// TestAdminPoolCallersAreListed is the architecture rule of NewAdminPool
// (TEN-03, WP-351): the pool without the runtime login check is only used by
// the command-line tools listed in adminPoolCallers. A reference from
// cmd/server or internal/, from an unlisted file, or a listed file without a
// reference fails the test.
func TestAdminPoolCallersAreListed(t *testing.T) {
	refs := scanAdminPoolRefs(t)
	if len(refs) == 0 {
		t.Error("no reference of NewAdminPool found; the scan is broken")
	}
	for _, msg := range adminPoolViolations(refs, adminPoolCallers) {
		t.Error(msg)
	}

	// The rule itself: a server or internal caller, an unlisted tool and a
	// stale entry are reported; the listed tools are not.
	cases := []struct {
		name  string
		refs  []poolCall
		allow map[string]string
		want  []string
	}{
		{"listed tool", []poolCall{{file: "cmd/seed/main.go", pos: "cmd/seed/main.go:1"}},
			map[string]string{"cmd/seed/main.go": "seed"}, nil},
		{"server caller", []poolCall{{file: "cmd/server/main.go", pos: "cmd/server/main.go:1"}},
			map[string]string{"cmd/server/main.go": "server"}, []string{"cmd/server/main.go:1", "adminPoolCallers entry cmd/server/main.go"}},
		{"internal caller", []poolCall{{file: "internal/jobs/x.go", pos: "internal/jobs/x.go:1"}},
			map[string]string{"internal/jobs/x.go": "job"}, []string{"internal/jobs/x.go:1", "adminPoolCallers entry internal/jobs/x.go"}},
		{"database package caller", []poolCall{{file: "internal/database/x.go", pos: "internal/database/x.go:1"}},
			map[string]string{}, []string{"internal/database/x.go:1"}},
		{"unlisted tool", []poolCall{{file: "cmd/other/main.go", pos: "cmd/other/main.go:1"}},
			map[string]string{}, []string{"cmd/other/main.go:1"}},
		{"stale entry", nil, map[string]string{"cmd/seed/main.go": "seed"}, []string{"cmd/seed/main.go (seed) matches no reference"}},
	}
	for _, tc := range cases {
		got := adminPoolViolations(tc.refs, tc.allow)
		if len(got) != len(tc.want) {
			t.Errorf("rule %s: violations = %q, want %d matching %q", tc.name, got, len(tc.want), tc.want)
			continue
		}
		for i, w := range tc.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("rule %s: violation %q lacks %q", tc.name, got[i], w)
			}
		}
	}
}

// adminPoolViolations checks the references of NewAdminPool against allow:
// every reference must be in a listed file, every entry must be a tool
// outside cmd/server and must still match a reference.
func adminPoolViolations(refs []poolCall, allow map[string]string) []string {
	var out []string
	hit := map[string]bool{}
	for _, r := range refs {
		if _, ok := allow[r.file]; ok && adminPoolCallerAllowed(r.file) {
			hit[r.file] = true
			continue
		}
		out = append(out, r.pos+": database.NewAdminPool skips the runtime login check (TEN-03); "+
			"the server and internal packages use database.NewPool, command-line tools are listed in adminPoolCallers")
	}
	files := make([]string, 0, len(allow))
	for file := range allow {
		files = append(files, file)
	}
	sort.Strings(files)
	for _, file := range files {
		switch {
		case !adminPoolCallerAllowed(file):
			out = append(out, "adminPoolCallers entry "+file+" is no command-line tool; only cmd/ outside cmd/server may use NewAdminPool")
		case !hit[file]:
			out = append(out, "adminPoolCallers entry "+file+" ("+allow[file]+") matches no reference of NewAdminPool any more; remove it")
		}
	}
	return out
}

// adminPoolCallerAllowed reports whether file may be listed at all: a
// command under cmd/ other than the server.
func adminPoolCallerAllowed(file string) bool {
	return strings.HasPrefix(file, "cmd/") && !strings.HasPrefix(file, "cmd/server/")
}

// scanAdminPoolRefs returns every reference of NewAdminPool in the Go files
// of the module, test files and this package included; only the declaration
// in pool.go is skipped.
func scanAdminPoolRefs(t *testing.T) []poolCall {
	t.Helper()
	fset := token.NewFileSet()
	var refs []poolCall
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
			if d.Name() == "vendor" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		var decl *ast.Ident
		for _, d := range file.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "NewAdminPool" && file.Name.Name == "database" {
				decl = fn.Name
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "NewAdminPool" && id != decl {
				refs = append(refs, poolCall{file: rel, pos: rel + ":" + strconv.Itoa(fset.Position(id.Pos()).Line), call: id.Name})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan backend: %v", err)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].pos < refs[j].pos })
	return refs
}
