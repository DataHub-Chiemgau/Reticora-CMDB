package database

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

// backendRoot is the module root seen from this package.
const backendRoot = "../.."

// fixtureDir holds the fixture package of TestTenantGuardFixtures.
const fixtureDir = "testdata/tenantguard"

// databasePkg is the import path of this package.
var databasePkg = reflect.TypeOf(TenantScope{}).PkgPath()

// connTypes are the types whose methods run SQL or hand out a connection,
// by package path and type name (F-190, WP-561). The guard reports every
// method of them, called, taken as a method value or named in a method
// expression, also when it is promoted from an embedded field; only the
// methods listed here touch no table and are not reported: closing,
// pinging and the statistics of a pool, a connection or a handle.
var connTypes = map[string]map[string]bool{
	"github.com/jackc/pgx/v5/pgxpool.Pool": {"Close": true, "Ping": true, "Stat": true},
	"github.com/jackc/pgx/v5/pgxpool.Conn": {"Release": true},
	"github.com/jackc/pgx/v5.Conn":         {"Close": true, "IsClosed": true, "Ping": true},
	"database/sql.DB":                      {"Close": true, "Ping": true, "PingContext": true, "Stats": true},
}

// connOpeners are the functions that open a pool, a connection or a
// database/sql handle of their own (pgx.Connect, pgxpool.New, sql.Open and
// their variants), by package path and name. A call or a function value is
// reported; the types of these packages (sql.NullTime, pgx.Tx) are not.
var connOpeners = map[string]*regexp.Regexp{
	"github.com/jackc/pgx/v5":         regexp.MustCompile(`^Connect`),
	"github.com/jackc/pgx/v5/pgconn":  regexp.MustCompile(`^Connect`),
	"github.com/jackc/pgx/v5/pgxpool": regexp.MustCompile(`^New`),
	"github.com/jackc/pgx/v5/stdlib":  regexp.MustCompile(`^(Open|GetConnector)`),
	"database/sql":                    regexp.MustCompile(`^Open`),
}

// poolType is the type the naming convention applies to.
const poolType = "*github.com/jackc/pgx/v5/pgxpool.Pool"

// poolName is the naming convention for *pgxpool.Pool values. The guard
// finds pool access by type; the convention stays as an additional check
// so a reader sees a pool at its name.
var poolName = regexp.MustCompile(`(?i)pool$`)

// TestNoDatabaseAccessOutsideTenantContext is the architecture test of
// TEN-06 (WP-041, WP-561): outside this package every database access runs
// through WithTenant (or WithRequestTenant) or WithSystem. The guard
// type-checks the module and reports every method use on a pool, a pooled
// connection, a pgx connection or a database/sql handle (connTypes) and
// every function that opens one (connOpeners), whatever the variable is
// called; only the documented system paths of tenantGuardAllowlist may
// do so. TestTenantGuardFixtures proves what the guard sees.
func TestNoDatabaseAccessOutsideTenantContext(t *testing.T) {
	calls, misnamed := scanAccess(loadBackend(t), absDir(t, backendRoot), outsideDatabase)
	if len(calls) == 0 {
		t.Error("no direct database access found, not even in the allowlisted system paths; the scan is broken")
	}
	for _, v := range directAccessViolations(calls, misnamed, tenantGuardAllowlist) {
		t.Error(v.msg)
	}
}

// TestTenantGuardFixtures is the self-test of the guard (F-190): every
// access marked "want:" in testdata/tenantguard/bypass.go (local alias,
// method value, method expression, direct call, misnamed field, embedded
// pool, pooled connection, pgx.Connect, sql.Open, pgxpool.New) is
// reported, nothing else; the tenant transaction, the system read, the
// methods without SQL and database/sql types in allowed.go are not, and
// its direct call (marked "listed:") only while the file is allowlisted.
// The allowlist rule reports stale entries and empty reasons.
func TestTenantGuardFixtures(t *testing.T) {
	pkgs, err := loadPackages(fixtureDir, false, ".")
	if err != nil {
		t.Fatalf("type-check the fixture: %v", err)
	}
	root := absDir(t, fixtureDir)
	calls, misnamed := scanAccess(pkgs, root, func(string) bool { return true })
	want, listed := fixtureMarks(t, pkgs, root)
	if len(want) < 15 || len(listed) != 1 {
		t.Fatalf("fixture marks: %d want, %d listed; the fixture is broken", len(want), len(listed))
	}

	allow := map[string]string{"allowed.go": "fixture of a documented system path"}
	compareFindings(t, "allowed.go listed", directAccessViolations(calls, misnamed, allow), want)
	compareFindings(t, "nothing listed", directAccessViolations(calls, misnamed, nil), merge(want, listed))

	for _, tc := range []struct {
		name  string
		allow map[string]string
		want  string
	}{
		{"stale entry", map[string]string{"allowed.go": "system path", "gone.go": "removed path"},
			"tenantGuardAllowlist entry gone.go (removed path) matches no direct database access any more"},
		{"empty reason", map[string]string{"allowed.go": " "}, "tenantGuardAllowlist entry allowed.go has no reason"},
	} {
		var got []string
		for _, v := range directAccessViolations(calls, misnamed, tc.allow) {
			if v.pos == "" {
				got = append(got, v.msg)
			}
		}
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("rule %s: allowlist violations = %q, want one containing %q", tc.name, got, tc.want)
		}
	}
}

// TestWithSystemCallersAreListed is the release list of E-08 (F-191,
// WP-561): every file outside this package that references WithSystem,
// the read-only transaction with the system flag that reads across
// organizations, is listed in withSystemCallers with its reason. An
// unlisted file, an entry without a reference and an empty reason fail.
// Test files may call it.
func TestWithSystemCallersAreListed(t *testing.T) {
	var refs []poolCall
	for _, r := range funcRefs(loadBackend(t), absDir(t, backendRoot), "WithSystem") {
		if !strings.HasSuffix(r.file, "_test.go") && !strings.HasPrefix(r.file, "internal/database/") {
			refs = append(refs, r)
		}
	}
	if len(refs) == 0 {
		t.Error("no reference of WithSystem found; the scan is broken")
	}
	for _, msg := range callerListViolations("withSystemCallers", "WithSystem", refs, withSystemCallers) {
		t.Error(msg)
	}

	// The rule itself: a listed caller passes; an unlisted file, a stale
	// entry and an empty reason are reported.
	ref := []poolCall{{file: "internal/jobs/x.go", pos: "internal/jobs/x.go:1"}}
	cases := []struct {
		name  string
		refs  []poolCall
		allow map[string]string
		want  []string
	}{
		{"listed caller", ref, map[string]string{"internal/jobs/x.go": "organizations of the job"}, nil},
		{"unlisted file", ref, map[string]string{}, []string{"internal/jobs/x.go:1: database.WithSystem"}},
		{"stale entry", nil, map[string]string{"internal/jobs/x.go": "organizations of the job"},
			[]string{"withSystemCallers entry internal/jobs/x.go (organizations of the job) matches no reference"}},
		{"empty reason", ref, map[string]string{"internal/jobs/x.go": "\t"},
			[]string{"withSystemCallers entry internal/jobs/x.go has no reason"}},
	}
	for _, tc := range cases {
		checkRule(t, tc.name, callerListViolations("withSystemCallers", "WithSystem", tc.refs, tc.allow), tc.want)
	}
}

// TestAdminPoolCallersAreListed is the architecture rule of NewAdminPool
// (TEN-03, WP-351): the pool without the runtime login check is only used by
// the command-line tools listed in adminPoolCallers. A reference from
// cmd/server or internal/, from an unlisted file, or a listed file without a
// reference fails the test. The references are found by type, test files
// included (WP-561).
func TestAdminPoolCallersAreListed(t *testing.T) {
	refs := funcRefs(loadBackend(t), absDir(t, backendRoot), "NewAdminPool")
	if len(refs) == 0 {
		t.Error("no reference of NewAdminPool found; the scan is broken")
	}
	for _, msg := range adminPoolViolations(refs, adminPoolCallers) {
		t.Error(msg)
	}

	// The rule itself: a server or internal caller, an unlisted tool, a
	// stale entry and an empty reason are reported; the listed tools are
	// not.
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
		{"empty reason", []poolCall{{file: "cmd/seed/main.go", pos: "cmd/seed/main.go:1"}},
			map[string]string{"cmd/seed/main.go": ""}, []string{"adminPoolCallers entry cmd/seed/main.go has no reason"}},
	}
	for _, tc := range cases {
		checkRule(t, tc.name, adminPoolViolations(tc.refs, tc.allow), tc.want)
	}
}

// TestSystemWriteCallersAreListed keeps the read-write system transaction
// (WithSystemWrite, WP-459) to the files of systemWriteCallers: a reference
// from any other file than a test, an entry without a reference and an
// empty reason fail.
func TestSystemWriteCallersAreListed(t *testing.T) {
	var refs []poolCall
	for _, r := range funcRefs(loadBackend(t), absDir(t, backendRoot), "WithSystemWrite") {
		if !strings.HasSuffix(r.file, "_test.go") {
			refs = append(refs, r)
		}
	}
	for _, msg := range callerListViolations("systemWriteCallers", "WithSystemWrite", refs, systemWriteCallers) {
		t.Error(msg)
	}
}

// checkRule compares the violations of a rule case with the fragments
// they must contain, in order.
func checkRule(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("rule %s: violations = %q, want %d matching %q", name, got, len(want), want)
		return
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("rule %s: violation %q lacks %q", name, got[i], w)
		}
	}
}

// adminPoolViolations checks the references of NewAdminPool against allow:
// every reference must be in a listed file, every entry must be a tool
// outside cmd/server, give a reason and still match a reference.
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
	for _, file := range sortedKeys(allow) {
		switch {
		case !adminPoolCallerAllowed(file):
			out = append(out, "adminPoolCallers entry "+file+" is no command-line tool; only cmd/ outside cmd/server may use NewAdminPool")
		case strings.TrimSpace(allow[file]) == "":
			out = append(out, "adminPoolCallers entry "+file+" has no reason; say why the tool needs the admin login")
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

// callerListViolations checks the references of the function fn of this
// package against the release list named list: every reference must be in
// a listed file, every entry must give a reason and still match a
// reference.
func callerListViolations(list, fn string, refs []poolCall, allow map[string]string) []string {
	var out []string
	hit := map[string]bool{}
	for _, r := range refs {
		if _, ok := allow[r.file]; ok {
			hit[r.file] = true
			continue
		}
		out = append(out, r.pos+": database."+fn+" reads across organizations with the system flag (E-08); "+
			"list the file with its reason in "+list+" (tenantguard_allowlist.go)")
	}
	for _, file := range sortedKeys(allow) {
		switch {
		case strings.TrimSpace(allow[file]) == "":
			out = append(out, list+" entry "+file+" has no reason; say what the file reads across organizations and why")
		case !hit[file]:
			out = append(out, list+" entry "+file+" ("+allow[file]+") matches no reference of "+fn+" any more; remove it")
		}
	}
	return out
}

// violation is one finding of the tenant guard: pos (file:line) and kind
// ("call" for a database access, "name" for a misnamed pool) for a source
// line, an empty pos for a bad allowlist entry.
type violation struct {
	pos  string
	kind string
	msg  string
}

// directAccessViolations applies the allowlist to the findings of
// scanAccess: a direct access is allowed in a listed file; a misnamed pool
// never; an entry must give a reason and still match an access.
func directAccessViolations(calls, misnamed []poolCall, allow map[string]string) []violation {
	var out []violation
	for _, m := range misnamed {
		out = append(out, violation{pos: m.pos, kind: "name",
			msg: m.pos + ": *pgxpool.Pool value " + m.call + " must be named ...pool so a reader sees the pool"})
	}
	hit := map[string]bool{}
	for _, c := range calls {
		if _, ok := allow[c.file]; ok {
			hit[c.file] = true
			continue
		}
		out = append(out, violation{pos: c.pos, kind: "call",
			msg: c.pos + ": direct database access " + c.call + " outside database.WithTenant/WithSystem (TEN-06); " +
				"use the tenant transaction or document a system path in tenantguard_allowlist.go"})
	}
	for _, file := range sortedKeys(allow) {
		switch {
		case strings.TrimSpace(allow[file]) == "":
			out = append(out, violation{msg: "tenantGuardAllowlist entry " + file + " has no reason; document the system path"})
		case !hit[file]:
			out = append(out, violation{msg: "tenantGuardAllowlist entry " + file + " (" + allow[file] +
				") matches no direct database access any more; remove it"})
		}
	}
	return out
}

type poolCall struct {
	file string
	pos  string
	call string
}

// outsideDatabase reports whether the package is checked by the tenant
// guard: every package of the module except this one, its external tests
// and its subpackages.
func outsideDatabase(pkgPath string) bool {
	return pkgPath != databasePkg && pkgPath != databasePkg+"_test" && !strings.HasPrefix(pkgPath, databasePkg+"/")
}

// scanAccess walks the non-test files under root of the packages check
// accepts and returns the direct database accesses (connTypes,
// connOpeners) and the *pgxpool.Pool variables, fields and parameters that
// break the naming convention, by type information.
func scanAccess(pkgs []*packages.Package, root string, check func(pkgPath string) bool) (calls, misnamed []poolCall) {
	seen := map[string]bool{}
	add := func(list *[]poolCall, p poolCall) {
		if key := p.pos + " " + p.call; !seen[key] {
			seen[key] = true
			*list = append(*list, p)
		}
	}
	for _, pkg := range pkgs {
		if !check(pkg.PkgPath) {
			continue
		}
		info := pkg.TypesInfo
		for _, file := range pkg.Syntax {
			rel, ok := relFile(root, pkg.Fset, file)
			if !ok || strings.HasSuffix(rel, "_test.go") {
				continue
			}
			at := func(pos token.Pos) string { return rel + ":" + strconv.Itoa(pkg.Fset.Position(pos).Line) }
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.SelectorExpr:
					if sel, ok := info.Selections[node]; ok && sel.Kind() != types.FieldVal {
						if name, ok := connMethod(sel.Obj()); ok {
							add(&calls, poolCall{file: rel, pos: at(node.Sel.Pos()), call: name})
						}
					}
				case *ast.Ident:
					if fn, ok := info.Uses[node].(*types.Func); ok && isConnOpener(fn) {
						add(&calls, poolCall{file: rel, pos: at(node.Pos()), call: fn.Pkg().Name() + "." + fn.Name()})
					}
					if v, ok := info.Defs[node].(*types.Var); ok && v.Name() != "_" &&
						types.TypeString(v.Type(), nil) == poolType && !poolName.MatchString(v.Name()) {
						add(&misnamed, poolCall{file: rel, pos: at(node.Pos()), call: v.Name()})
					}
				}
				return true
			})
		}
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].pos < calls[j].pos })
	sort.Slice(misnamed, func(i, j int) bool { return misnamed[i].pos < misnamed[j].pos })
	return calls, misnamed
}

// connMethod reports whether obj is a method of a connTypes type that may
// run SQL, and names it pgxpool.Pool.Exec.
func connMethod(obj types.Object) (string, bool) {
	fn, ok := obj.(*types.Func)
	if !ok {
		return "", false
	}
	recv := fn.Type().(*types.Signature).Recv()
	if recv == nil {
		return "", false
	}
	t := recv.Type()
	if p, isPtr := t.(*types.Pointer); isPtr {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", false
	}
	harmless, ok := connTypes[named.Obj().Pkg().Path()+"."+named.Obj().Name()]
	if !ok || harmless[fn.Name()] {
		return "", false
	}
	return named.Obj().Pkg().Name() + "." + named.Obj().Name() + "." + fn.Name(), true
}

// isConnOpener reports whether fn is a package function of connOpeners.
func isConnOpener(fn *types.Func) bool {
	if fn.Pkg() == nil || fn.Type().(*types.Signature).Recv() != nil {
		return false
	}
	re, ok := connOpeners[fn.Pkg().Path()]
	return ok && re.MatchString(fn.Name())
}

// funcRefs returns every reference (call or function value) of the
// package-level function name of this package in the files under root,
// test files and this package included, found by type information; the
// declaration is no reference.
func funcRefs(pkgs []*packages.Package, root, name string) []poolCall {
	seen := map[string]bool{}
	var refs []poolCall
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			rel, ok := relFile(root, pkg.Fset, file)
			if !ok {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				fn, ok := pkg.TypesInfo.Uses[id].(*types.Func)
				if !ok || fn.Pkg() == nil || fn.Pkg().Path() != databasePkg || fn.Name() != name ||
					fn.Type().(*types.Signature).Recv() != nil {
					return true
				}
				pos := rel + ":" + strconv.Itoa(pkg.Fset.Position(id.Pos()).Line)
				if !seen[pos] {
					seen[pos] = true
					refs = append(refs, poolCall{file: rel, pos: pos, call: name})
				}
				return true
			})
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].pos < refs[j].pos })
	return refs
}

// relFile returns the slash path of file relative to root; files outside
// root (the generated main of a test binary) report false.
func relFile(root string, fset *token.FileSet, file *ast.File) (string, bool) {
	rel, err := filepath.Rel(root, fset.Position(file.Pos()).Filename)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

var (
	backendOnce sync.Once
	backendPkgs []*packages.Package
	backendErr  error
)

// loadBackend type-checks every package of the module once per test
// binary, with its tests and the auditdefect files.
func loadBackend(t *testing.T) []*packages.Package {
	t.Helper()
	backendOnce.Do(func() { backendPkgs, backendErr = loadPackages(backendRoot, true, "./...") })
	if backendErr != nil {
		t.Fatalf("type-check the backend module: %v", backendErr)
	}
	return backendPkgs
}

// loadPackages loads the packages of patterns in dir with syntax and type
// information, type-checking the dependencies from source; no compiled
// export data is needed, so the cost does not depend on the build cache.
// The files outside the module only provide types: their function bodies
// are dropped while parsing, so the type errors this causes there (unused
// imports) are ignored, as in the generated main of a test binary. A
// package of the patterns that does not type-check
// is an error, so the guard never passes on a partial view.
func loadPackages(dir string, tests bool, patterns ...string) ([]*packages.Package, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	module, err := filepath.Abs(backendRoot)
	if err != nil {
		return nil, err
	}
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir:        abs,
		Tests:      tests,
		BuildFlags: []string{"-tags=auditdefect"},
		ParseFile: func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
			if rel, relErr := filepath.Rel(module, filename); relErr == nil && !strings.HasPrefix(rel, "..") {
				return parser.ParseFile(fset, filename, src, parser.AllErrors|parser.ParseComments|parser.SkipObjectResolution)
			}
			file, parseErr := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
			if file != nil {
				for _, decl := range file.Decls {
					if fn, ok := decl.(*ast.FuncDecl); ok {
						fn.Body = nil
					}
				}
			}
			return file, parseErr
		},
	}, patterns...)
	if err != nil {
		return nil, err
	}
	var errs []string
	for _, pkg := range pkgs {
		if strings.HasSuffix(pkg.ID, ".test") {
			continue // the generated main of a test binary, outside the module
		}
		for _, e := range pkg.Errors {
			errs = append(errs, pkg.ID+": "+e.Error())
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%d package errors: %s", len(errs), strings.Join(errs, "; "))
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages in %s", dir)
	}
	return pkgs, nil
}

// absDir returns dir as an absolute path.
func absDir(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs %s: %v", dir, err)
	}
	return abs
}

// fixtureMarks reads the "want:" and "listed:" comments of the fixture as
// "file:line kind" keys; the value names the enclosing declaration.
func fixtureMarks(t *testing.T, pkgs []*packages.Package, root string) (want, listed map[string]string) {
	t.Helper()
	want, listed = map[string]string{}, map[string]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			rel, ok := relFile(root, pkg.Fset, file)
			if !ok {
				continue
			}
			for _, group := range file.Comments {
				for _, c := range group.List {
					text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
					var into map[string]string
					switch {
					case strings.HasPrefix(text, "want:"):
						into = want
					case strings.HasPrefix(text, "listed:"):
						into = listed
					default:
						continue
					}
					line := rel + ":" + strconv.Itoa(pkg.Fset.Position(c.Pos()).Line)
					for _, kind := range strings.Fields(text[strings.Index(text, ":")+1:]) {
						into[line+" "+kind] = enclosing(file, c.Pos())
					}
				}
			}
		}
	}
	return want, listed
}

// enclosing names the declaration around pos for the failure messages.
func enclosing(file *ast.File, pos token.Pos) string {
	for _, d := range file.Decls {
		if d.Pos() > pos || pos > d.End() {
			continue
		}
		switch decl := d.(type) {
		case *ast.FuncDecl:
			return decl.Name.Name
		case *ast.GenDecl:
			if len(decl.Specs) > 0 {
				if ts, ok := decl.Specs[0].(*ast.TypeSpec); ok {
					return ts.Name.Name
				}
			}
		}
	}
	return "?"
}

// compareFindings checks that the source violations are exactly want.
func compareFindings(t *testing.T, name string, got []violation, want map[string]string) {
	t.Helper()
	have := map[string]bool{}
	for _, v := range got {
		if v.pos == "" {
			t.Errorf("%s: unexpected allowlist violation: %s", name, v.msg)
			continue
		}
		key := v.pos + " " + v.kind
		have[key] = true
		if _, ok := want[key]; !ok {
			t.Errorf("%s: reported but not marked: %s", name, v.msg)
		}
	}
	for _, key := range sortedKeys(want) {
		if !have[key] {
			t.Errorf("%s: not reported: %s in %s", name, key, want[key])
		}
	}
}

// merge returns the union of the mark sets.
func merge(sets ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, s := range sets {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// sortedKeys returns the keys of m in order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
