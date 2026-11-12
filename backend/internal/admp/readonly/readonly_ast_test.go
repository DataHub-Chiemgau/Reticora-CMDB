// Proof of the read-only guarantee, layer (c): the syntax of the package
// and of its parent package admp (ADM-02, E-224, WP-927).
//
// The endpoint work packages (WP-1073 to WP-1076) and every later change
// may only extend these rules, never loosen them (DoD in
// .github/pull_request_template.md).
package readonly

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The files with exceptions: the login may name another method (auth.go);
// requests are built and sent only in client.go (GET) and auth.go (the
// login); the guarding transports pass requests on (transport.go).
const (
	authFile      = "auth.go"
	clientFile    = "client.go"
	transportFile = "transport.go"
)

const egressPath = "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"

// writeMethods are the method constants and literals that name a write.
var writeMethods = map[string]bool{"MethodPost": true, "MethodPut": true, "MethodPatch": true, "MethodDelete": true}

// writeLiterals are compared without case.
var writeLiterals = []string{"POST", "PUT", "PATCH", "DELETE"}

// forbiddenSelectors are selectors x.Name that write or bypass the
// transport, on any receiver, called or taken as a value.
var forbiddenSelectors = map[string]bool{"Post": true, "PostForm": true, "Put": true, "Patch": true, "Delete": true, "Head": true}

// forbiddenHTTPFuncs are the package-level shortcuts of net/http that
// bypass the guarding transports (called or taken as a value).
var forbiddenHTTPFuncs = map[string]bool{"Get": true, "Head": true, "Post": true, "PostForm": true}

// httpRequestBuilders build a request of any method.
var httpRequestBuilders = map[string]bool{"NewRequest": true, "NewRequestWithContext": true}

// clientBuilders are the types and functions that make an HTTP client or
// transport.
var (
	httpClientTypes = map[string]bool{"Client": true, "Transport": true}
	egressBuilders  = map[string]bool{"NewClient": true, "NewDialer": true}
	// netDialers reach the network around the HTTP transports.
	netDialers = map[string]bool{"Dial": true, "DialTimeout": true, "DialTCP": true, "DialUDP": true, "DialIP": true,
		"DialUnix": true, "Dialer": true}
)

// readonlyImports are the only packages the non-test files of readonly
// may import; another one (net/http/httputil, os/exec, …) is a way around
// the guarding transports.
var readonlyImports = map[string]bool{
	"bytes": true, "context": true, "encoding/json": true, "errors": true, "fmt": true, "io": true,
	"math/rand/v2": true, "net": true, "net/http": true, "net/url": true, "path": true, "strconv": true,
	"strings": true, "sync": true, "time": true,
	"github.com/prometheus/client_golang/prometheus": true,
	egressPath: true,
}

// importName returns the local name of the import path in f (empty when
// not imported).
func importName(f *ast.File, path, def string) string {
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return def
	}
	return ""
}

// isPkgSel reports whether e is pkg.name.
func isPkgSel(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || pkg == "" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && sel.Sel.Name == name
}

// pkgSelName returns name when e is pkg.name, else "".
func pkgSelName(e ast.Expr, pkg string) string {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || pkg == "" {
		return ""
	}
	if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
		return sel.Sel.Name
	}
	return ""
}

// typeName strips a pointer or a parenthesis from a type expression.
func typeName(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return e
		}
	}
}

// calledSelectors returns the selectors that are the function of a call.
func calledSelectors(f *ast.File) map[*ast.SelectorExpr]bool {
	out := map[*ast.SelectorExpr]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
				out[sel] = true
			}
		}
		return true
	})
	return out
}

// writeValue reports whether e names a write method: a write constant of
// net/http, a write literal or a name in names.
func writeValue(e ast.Expr, httpPkg string, names map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			found = found || writeMethods[pkgSelName(x, httpPkg)]
		case *ast.Ident:
			found = found || names[x.Name]
		case *ast.BasicLit:
			if v, err := strconv.Unquote(x.Value); x.Kind == token.STRING && err == nil {
				for _, w := range writeLiterals {
					found = found || strings.EqualFold(strings.TrimSpace(v), w)
				}
			}
		}
		return !found
	})
	return found
}

// writeNames returns the package-level constants and variables of the
// package sources (file name → source) whose value names a write method, also through
// another such name (defaultAuthMethod = http.MethodPost). The checker
// allows their use only in auth.go.
func writeNames(srcs map[string][]byte) map[string]bool {
	type spec struct {
		names   []*ast.Ident
		values  []ast.Expr
		httpPkg string
	}
	var specs []spec
	fset := token.NewFileSet()
	for file, src := range srcs {
		f, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			continue
		}
		httpPkg := importName(f, "net/http", "http")
		// Package-level names only: a local name derived from one is
		// reported where it uses the package-level name.
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok {
				for _, sp := range gd.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok {
						specs = append(specs, spec{names: vs.Names, values: vs.Values, httpPkg: httpPkg})
					}
				}
			}
		}
	}
	names := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, sp := range specs {
			for i, id := range sp.names {
				if id == nil || id.Name == "_" || names[id.Name] {
					continue
				}
				v := ast.Expr(nil)
				switch {
				case len(sp.values) == len(sp.names):
					v = sp.values[i]
				case len(sp.values) == 1:
					v = sp.values[0]
				}
				if v != nil && writeValue(v, sp.httpPkg, names) {
					names[id.Name] = true
					changed = true
				}
			}
		}
	}
	return names
}

// checkReadonlySource returns the violations of the read-only rules in the
// source of file (base name decides the exceptions); names are the write
// names of the package (writeNames).
func checkReadonlySource(file string, src []byte, names map[string]bool) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse: %v", file, err)}
	}
	base := filepath.Base(file)
	httpPkg := importName(f, "net/http", "http")
	egressPkg := importName(f, egressPath, "egress")
	netPkg := importName(f, "net", "net")
	called := calledSelectors(f)
	builds := base == clientFile || base == authFile
	var out []string
	report := func(n ast.Node, format string, args ...any) {
		out = append(out, fmt.Sprintf("%s: %s", fset.Position(n.Pos()), fmt.Sprintf(format, args...)))
	}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		switch {
		case !readonlyImports[p]:
			report(imp, "import of %s", p)
		case imp.Name != nil && (imp.Name.Name == "." || imp.Name.Name == "_"):
			report(imp, "import %s of %s", imp.Name.Name, p)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if names[x.Name] && base != authFile {
				report(x, "write method %s outside %s", x.Name, authFile)
			}
		case *ast.SelectorExpr:
			name := x.Sel.Name
			httpName := pkgSelName(x, httpPkg)
			switch {
			case writeMethods[httpName] && base != authFile:
				report(x, "http.%s outside %s", name, authFile)
			case httpName == "DefaultClient" || httpName == "DefaultTransport":
				report(x, "http.%s", name)
			case forbiddenHTTPFuncs[httpName] && !called[x]:
				report(x, "http.%s as a value", name)
			case httpRequestBuilders[httpName] && !called[x]:
				report(x, "http.%s as a value", name)
			case egressBuilders[pkgSelName(x, egressPkg)] && !builds:
				report(x, "egress.%s outside %s and %s", name, clientFile, authFile)
			case netDialers[pkgSelName(x, netPkg)]:
				report(x, "net.%s", name)
			case httpName != "":
			case forbiddenSelectors[name]:
				report(x, "use of .%s", name)
			case name == "Do" && !builds:
				report(x, ".Do outside %s and %s", clientFile, authFile)
			case name == "RoundTrip" && base != transportFile:
				report(x, ".RoundTrip outside %s", transportFile)
			case name == "method" && base != authFile && base != transportFile:
				report(x, ".method outside %s and %s", authFile, transportFile)
			case name == "AuthMethod" && base != authFile:
				report(x, ".AuthMethod outside %s", authFile)
			}
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "Method" {
					report(x, "assignment to .Method")
				}
			}
		case *ast.CompositeLit:
			if t := pkgSelName(typeName(x.Type), httpPkg); t == "Request" {
				report(x, "http.Request literal")
			} else if httpClientTypes[t] && !builds {
				report(x, "http.%s literal outside %s and %s", t, clientFile, authFile)
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING && base != authFile {
				if v, err := strconv.Unquote(x.Value); err == nil {
					for _, w := range writeLiterals {
						if strings.EqualFold(strings.TrimSpace(v), w) {
							report(x, "method literal %q outside %s", v, authFile)
						}
					}
				}
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
				if t := pkgSelName(x.Args[0], httpPkg); t == "Request" || httpClientTypes[t] && !builds {
					report(x, "new(http.%s)", t)
				}
			}
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			name := sel.Sel.Name
			isHTTP := isPkgSel(sel, httpPkg, name)
			switch {
			case isHTTP && forbiddenHTTPFuncs[name]:
				report(x, "call of http.%s", name)
			case isHTTP && httpRequestBuilders[name]:
				switch base {
				case authFile:
				case clientFile:
					idx := 0
					if name == "NewRequestWithContext" {
						idx = 1
					}
					if len(x.Args) <= idx || !isPkgSel(x.Args[idx], httpPkg, "MethodGet") {
						report(x, "http.%s in %s without exactly http.MethodGet", name, clientFile)
					}
				default:
					report(x, "http.%s outside %s and %s", name, clientFile, authFile)
				}
			}
		}
		return true
	})
	return out
}

// checkOutboundSource returns the outgoing HTTP building blocks in a file
// of the parent package admp outside readonly, called or taken as a value.
func checkOutboundSource(file string, src []byte) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse: %v", file, err)}
	}
	httpPkg := importName(f, "net/http", "http")
	egressPkg := importName(f, egressPath, "egress")
	netPkg := importName(f, "net", "net")
	var out []string
	report := func(n ast.Node, format string, args ...any) {
		out = append(out, fmt.Sprintf("%s: %s", fset.Position(n.Pos()), fmt.Sprintf(format, args...)))
	}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		switch {
		case p == "net/http/httputil" || strings.HasPrefix(p, "net/rpc"):
			report(imp, "import of %s", p)
		case imp.Name != nil && imp.Name.Name == "." && (p == "net/http" || p == "net" || p == egressPath):
			report(imp, "dot import of %s", p)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CompositeLit:
			if t := pkgSelName(typeName(x.Type), httpPkg); httpClientTypes[t] || t == "Request" {
				report(x, "http.%s literal", t)
			}
		case *ast.SelectorExpr:
			httpName := pkgSelName(x, httpPkg)
			switch {
			case httpName == "DefaultClient" || httpName == "DefaultTransport":
				report(x, "http.%s", httpName)
			case forbiddenHTTPFuncs[httpName] || httpRequestBuilders[httpName]:
				report(x, "use of http.%s", httpName)
			case egressBuilders[pkgSelName(x, egressPkg)]:
				report(x, "use of egress.%s", x.Sel.Name)
			case netDialers[pkgSelName(x, netPkg)]:
				report(x, "use of net.%s", x.Sel.Name)
			case httpName == "" && (x.Sel.Name == "RoundTrip" || x.Sel.Name == "Do") && isHTTPSender(x):
				report(x, "use of .%s", x.Sel.Name)
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
				if t := pkgSelName(x.Args[0], httpPkg); httpClientTypes[t] || t == "Request" {
					report(x, "new(http.%s)", t)
				}
			}
		}
		return true
	})
	return out
}

// isHTTPSender reports whether a selector .Do or .RoundTrip may send a
// request: every one except the methods of a sync.Once-like receiver
// named once (x.once.Do).
func isHTTPSender(x *ast.SelectorExpr) bool {
	if x.Sel.Name != "Do" {
		return true
	}
	switch r := x.X.(type) {
	case *ast.Ident:
		return !strings.Contains(strings.ToLower(r.Name), "once")
	case *ast.SelectorExpr:
		return !strings.Contains(strings.ToLower(r.Sel.Name), "once")
	}
	return true
}

func readSource(t *testing.T, file string) []byte {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// TestReadonlyPackageHasNoWriteCalls: in the non-test files of readonly,
// write methods, their literals and every constant or variable holding
// one (defaultAuthMethod) stand only in auth.go, the method field of the
// login only in auth.go and transport.go; no http.Get/Head/Post/PostForm
// (called or as a value), no http.DefaultClient; no .Post, .PostForm,
// .Put, .Patch, .Delete or .Head; no http.Request literal and no
// assignment to .Method; requests are built only in client.go (with
// exactly http.MethodGet) and auth.go, sent (.Do) only there and passed on
// (.RoundTrip) only in transport.go; HTTP clients and transports
// (egress.NewClient, http.Client{}) are made only in client.go and
// auth.go; no net.Dial; only the listed imports.
func TestReadonlyPackageHasNoWriteCalls(t *testing.T) {
	files := packageFiles(t, ".")
	srcs := map[string][]byte{}
	for _, file := range files {
		srcs[file] = readSource(t, file)
	}
	write := writeNames(srcs)
	if !write["defaultAuthMethod"] {
		t.Errorf("write names %v miss defaultAuthMethod (auth.go)", write)
	}
	var names []string
	for _, file := range files {
		names = append(names, filepath.Base(file))
		for _, v := range checkReadonlySource(file, srcs[file], write) {
			t.Error(v)
		}
	}
	for _, need := range []string{authFile, clientFile, "transport.go"} {
		found := false
		for _, n := range names {
			found = found || n == need
		}
		if !found {
			t.Errorf("%s is missing: the rules name it", need)
		}
	}
}

// TestAdmpPackageHasNoOutboundHTTPOutsideReadonly: the non-test files
// under backend/internal/admp outside readonly (also future ones and
// subpackages) build no outgoing HTTP: no http.NewRequest*, no
// http.Client literal, no http.DefaultClient, no http.Get/Post/PostForm/
// Head and no egress.NewClient.
func TestAdmpPackageHasNoOutboundHTTPOutsideReadonly(t *testing.T) {
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	err = filepath.WalkDir("..", func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			abs, aerr := filepath.Abs(path)
			if aerr != nil {
				return aerr
			}
			if abs == self || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		checked++
		for _, v := range checkOutboundSource(path, readSource(t, path)) {
			t.Error(v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no file of the package admp was checked")
	}
}

// TestReadonlyASTCheckerDetectsViolations: the checkers report embedded
// fragments that break the rules (negative cases).
func TestReadonlyASTCheckerDetectsViolations(t *testing.T) {
	const header = "package readonly\n\nimport (\n\t\"context\"\n\t\"net/http\"\n)\n\nvar _ = context.Background\n\n"
	readonlyCases := []struct {
		name, file, body string
		want             string
	}{
		{"MethodPost in client.go", "client.go", `func f(ctx context.Context) { _, _ = http.NewRequestWithContext(ctx, http.MethodPost, "u", nil) }`, "http.MethodPost outside auth.go"},
		{"MethodDelete constant", "transport.go", `var m = http.MethodDelete`, "http.MethodDelete outside auth.go"},
		{"literal delete", "client.go", `var m = "delete"`, `method literal "delete"`},
		{"literal PATCH", "ratelimit.go", "var m = `PATCH`", `method literal "PATCH"`},
		{"http.Post", "client.go", `func f() { _, _ = http.Post("u", "", nil) }`, "call of http.Post"},
		{"http.Get", "transport.go", `func f() { _, _ = http.Get("u") }`, "call of http.Get"},
		{"http.Post as a value", "ratelimit.go", `var p = http.Post`, "http.Post as a value"},
		{"http.NewRequest as a value", "client.go", `var nr = http.NewRequest`, "http.NewRequest as a value"},
		{"DefaultClient", "client.go", `var c = http.DefaultClient`, "http.DefaultClient"},
		{"selector Patch", "client.go", `type x struct{}; func (x) Patch() {}; func f() { x{}.Patch() }`, "use of .Patch"},
		{"selector Head", "auth.go", `func f(c *http.Client) { _, _ = c.Head("u") }`, "use of .Head"},
		{"selector Post as a value", "client.go", `func f(c *http.Client) { p := c.Post; _ = p }`, "use of .Post"},
		{"NewRequest outside", "transport.go", `func f(ctx context.Context) { _, _ = http.NewRequestWithContext(ctx, http.MethodGet, "u", nil) }`, "outside client.go and auth.go"},
		{"NewRequest with a variable method", "client.go", `func f(ctx context.Context, m string) { _, _ = http.NewRequestWithContext(ctx, m, "u", nil) }`, "without exactly http.MethodGet"},
		{"NewRequest with a literal GET", "client.go", `func f() { _, _ = http.NewRequest("GET", "u", nil) }`, "without exactly http.MethodGet"},
		{"Do outside", "ratelimit.go", `func f(c *http.Client, r *http.Request) { _, _ = c.Do(r) }`, ".Do outside"},
		{"Do as a value outside", "transport.go", `func f(c *http.Client) { do := c.Do; _ = do }`, ".Do outside"},
		{"defaultAuthMethod outside auth.go", "client.go", `func f() string { return defaultAuthMethod }`, "write method defaultAuthMethod outside auth.go"},
		{"login method outside auth.go", "client.go", `func f(l *login) string { return l.method }`, ".method outside auth.go and transport.go"},
		{"AuthMethod outside auth.go", "client.go", `func f(c Config) string { return c.AuthMethod }`, ".AuthMethod outside auth.go"},
		{"request literal", "client.go", `var r = &http.Request{Method: http.MethodGet}`, "http.Request literal"},
		{"new request", "client.go", `var r = new(http.Request)`, "new(http.Request)"},
		{"assignment to Method", "client.go", `func f(r *http.Request) { r.Method = http.MethodGet }`, "assignment to .Method"},
		{"RoundTrip outside transport.go", "client.go", `func f(rt http.RoundTripper, r *http.Request) { _, _ = rt.RoundTrip(r) }`, ".RoundTrip outside transport.go"},
		{"client literal outside", "ratelimit.go", `var c = &http.Client{}`, "http.Client literal outside client.go and auth.go"},
		{"transport literal outside", "transport.go", `var tr = &http.Transport{}`, "http.Transport literal outside"},
		{"new client outside", "ratelimit.go", `var c = new(http.Client)`, "new(http.Client)"},
		{"egress.NewClient outside", "ratelimit.go", "", "egress.NewClient outside client.go and auth.go"},
		{"egress bypass of the finding", "ratelimit.go", "", "http.Request literal"},
		{"egress bypass reaches RoundTrip", "ratelimit.go", "", ".RoundTrip outside transport.go"},
		{"egress bypass names defaultAuthMethod", "ratelimit.go", "", "write method defaultAuthMethod outside auth.go"},
		{"net.Dial", "client.go", `import "net"; var d = net.Dial`, "net.Dial"},
		{"foreign import", "client.go", `import "net/http/httputil"; var _ = httputil.NewSingleHostReverseProxy`, "import of net/http/httputil"},
		{"dot import", "client.go", "", "import . of net/http"},
		{"aliased net/http", "client.go", "", "http.MethodPut outside auth.go"},
	}
	const egressHeader = "package readonly\n\nimport (\n\t\"net/http\"\n\t\"net/url\"\n\n\t\"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress\"\n)\n\n"
	const bypass = `func bypass(u *url.URL) { req := &http.Request{Method: defaultAuthMethod, URL: u}; _, _ = egress.NewClient(egress.Options{}).Transport.RoundTrip(req) }`
	special := map[string]string{
		"aliased net/http":                      "package readonly\n\nimport web \"net/http\"\n\nvar m = web.MethodPut\n",
		"dot import":                            "package readonly\n\nimport . \"net/http\"\n\nvar m = MethodGet\n",
		"egress.NewClient outside":              egressHeader + `var c = egress.NewClient(egress.Options{}); var _ = http.NoBody; var _ url.URL`,
		"egress bypass of the finding":          egressHeader + bypass,
		"egress bypass reaches RoundTrip":       egressHeader + bypass,
		"egress bypass names defaultAuthMethod": egressHeader + bypass,
	}
	authSrc := map[string][]byte{"auth.go": []byte("package readonly\n\nimport \"net/http\"\n\nconst defaultAuthMethod = http.MethodPost\n")}
	write := writeNames(authSrc)
	for _, tc := range readonlyCases {
		src := header + tc.body
		if strings.HasPrefix(tc.body, "import ") {
			src = "package readonly\n\n" + tc.body
		}
		if sp, ok := special[tc.name]; ok {
			src = sp
		}
		got := checkReadonlySource(tc.file, []byte(src), write)
		if !containsViolation(got, tc.want) {
			t.Errorf("%s: violations %v, want one with %q", tc.name, got, tc.want)
		}
	}
	// A constant derived from a write name is a write name too.
	authSrc["auth.go"] = append(authSrc["auth.go"], []byte("const loginMethod = defaultAuthMethod\n")...)
	if derived := writeNames(authSrc); !derived["loginMethod"] {
		t.Errorf("derived write name missed: %v", derived)
	}
	// The allowed forms stay quiet.
	allowed := map[string]string{
		"auth.go":      header + `func f(ctx context.Context, c *http.Client, l *login) { r, _ := http.NewRequestWithContext(ctx, l.method, "u", nil); _, _ = c.Do(r); _ = "POST"; _ = defaultAuthMethod }`,
		"transport.go": header + `func f(t *authTransport, rt http.RoundTripper, r *http.Request) { _ = r.Method != t.method; _, _ = rt.RoundTrip(r) }`,
		"client.go":    header + `func f(ctx context.Context, c *http.Client) { r, _ := http.NewRequestWithContext(ctx, http.MethodGet, "u", http.NoBody); _, _ = c.Do(r) }`,
	}
	for file, src := range allowed {
		if got := checkReadonlySource(file, []byte(src), write); len(got) != 0 {
			t.Errorf("allowed form in %s reported: %v", file, got)
		}
	}

	const parentHeader = "package admp\n\nimport (\n\t\"context\"\n\t\"net/http\"\n\n\t\"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress\"\n)\n\nvar _ = context.Background\nvar _ egress.Options\n\n"
	parentCases := []struct{ name, body, want string }{
		{"http.Post", `func f() { _, _ = http.Post("u", "", nil) }`, "use of http.Post"},
		{"http.Post as a value", `var post = http.Post; func bypassParent() { _, _ = post("http://x", "", nil) }`, "use of http.Post"},
		{"http.NewRequest", `func f(ctx context.Context) { _, _ = http.NewRequestWithContext(ctx, http.MethodGet, "u", nil) }`, "use of http.NewRequestWithContext"},
		{"http.NewRequest as a value", `var nr = http.NewRequest`, "use of http.NewRequest"},
		{"client literal", `var c = &http.Client{}`, "http.Client literal"},
		{"transport literal", `var c = http.Transport{}`, "http.Transport literal"},
		{"request literal", `var r = &http.Request{}`, "http.Request literal"},
		{"new client", `var c = new(http.Client)`, "new(http.Client)"},
		{"DefaultClient", `var c = http.DefaultClient`, "http.DefaultClient"},
		{"egress.NewClient", `var c = egress.NewClient(egress.Options{})`, "use of egress.NewClient"},
		{"egress.NewClient as a value", `var nc = egress.NewClient`, "use of egress.NewClient"},
		{"Do as a value", `func f(c *http.Client) { do := c.Do; _ = do }`, "use of .Do"},
		{"RoundTrip", `func f(rt http.RoundTripper, r *http.Request) { _, _ = rt.RoundTrip(r) }`, "use of .RoundTrip"},
	}
	for _, tc := range parentCases {
		got := checkOutboundSource("account.go", []byte(parentHeader+tc.body))
		if !containsViolation(got, tc.want) {
			t.Errorf("parent %s: violations %v, want one with %q", tc.name, got, tc.want)
		}
	}
	// A handler of the parent package (routes, status codes) is no
	// outgoing HTTP.
	handler := parentHeader + `func h(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent); _ = r.Method == http.MethodPost }`
	if got := checkOutboundSource("account.go", []byte(handler)); len(got) != 0 {
		t.Errorf("a handler was reported: %v", got)
	}
}

func containsViolation(got []string, want string) bool {
	sort.Strings(got)
	for _, g := range got {
		if strings.Contains(g, want) {
			return true
		}
	}
	return false
}
