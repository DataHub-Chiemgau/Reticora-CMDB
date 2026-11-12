// Proof of the read-only guarantee, layer (a): the exported API (ADM-02,
// E-224, WP-927).
//
// These tests and readonly_ast_test.go and readonly_mock_test.go are the
// standing proof that POST, PUT, PATCH and DELETE cannot reach ADMP. The
// endpoint work packages (WP-1073 to WP-1076) and every later change may
// only extend them, never loosen them (DoD in
// .github/pull_request_template.md).
package readonly

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// readPrefixes are the only allowed prefixes of an exported method of
// Client.
var readPrefixes = []string{"Get", "List", "Lookup"}

// forbiddenHTTPTypes give direct HTTP access; no exported field, method
// or function of the package exposes one.
var forbiddenHTTPTypes = []reflect.Type{
	reflect.TypeFor[*http.Client](),
	reflect.TypeFor[http.RoundTripper](),
	reflect.TypeFor[*http.Transport](),
	reflect.TypeFor[*http.Request](),
}

// exportedTypes, exportedFuncs and exportedVars are the exported API of
// the package; TestNoExportedHTTPAccess checks against the source that
// they are complete, so a new exported type, function or variable must be
// listed here. Exported constants need no list: a constant cannot hold an
// HTTP client, transport or request.
var (
	exportedTypes = map[string]reflect.Type{
		"Client":        reflect.TypeFor[Client](),
		"Config":        reflect.TypeFor[Config](),
		"Error":         reflect.TypeFor[Error](),
		"SecretSource":  reflect.TypeFor[SecretSource](),
		"Authenticator": reflect.TypeFor[Authenticator](),
		// The decoded answers of the endpoints (WP-1073 ff.).
		"Session":    reflect.TypeFor[Session](),
		"AccessPort": reflect.TypeFor[AccessPort](),
		"Service":    reflect.TypeFor[Service](),
	}
	exportedFuncs = map[string]any{
		"New":             New,
		"ClassOf":         ClassOf,
		"IsCircuitOpen":   IsCircuitOpen,
		"RegisterMetrics": RegisterMetrics,
	}
	exportedVars = map[string]reflect.Type{
		"Classes":                  reflect.TypeOf(&Classes).Elem(),
		"ErrMethodNotAllowed":      reflect.TypeOf(&ErrMethodNotAllowed).Elem(),
		"ErrDestinationNotAllowed": reflect.TypeOf(&ErrDestinationNotAllowed).Elem(),
		"ErrInvalidPath":           reflect.TypeOf(&ErrInvalidPath).Elem(),
		"ErrCircuitOpen":           reflect.TypeOf(&ErrCircuitOpen).Elem(),
		"ErrInvalidConfig":         reflect.TypeOf(&ErrInvalidConfig).Elem(),
		"ErrPathNotAllowed":        reflect.TypeOf(&ErrPathNotAllowed).Elem(),
	}
)

// readMethodViolations returns the exported methods of t whose name does
// not begin with a read prefix.
func readMethodViolations(t reflect.Type) []string {
	var out []string
	for i := range t.NumMethod() {
		name := t.Method(i).Name
		if !slices.ContainsFunc(readPrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			out = append(out, name)
		}
	}
	return out
}

// writingClient stands for a change that adds a writing method: the
// checker must report it.
type writingClient struct{}

func (writingClient) GetX(context.Context) error  { return nil }
func (writingClient) PostX(context.Context) error { return nil }

// TestClientExportsOnlyReadMethods: the method set of *Client holds only
// names with the prefix Get, List or Lookup; the checker reports a test
// type with the method PostX.
func TestClientExportsOnlyReadMethods(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[*Client](), reflect.TypeFor[Client]()} {
		if bad := readMethodViolations(typ); len(bad) != 0 {
			t.Errorf("%v exports methods that are no reads: %v", typ, bad)
		}
	}
	if bad := readMethodViolations(reflect.TypeFor[writingClient]()); !slices.Equal(bad, []string{"PostX"}) {
		t.Fatalf("the checker reports %v for a type with PostX, want [PostX]", bad)
	}
}

// httpViolations returns the exported fields, method results and
// function results of typ that are a forbidden HTTP type.
func httpViolations(name string, typ reflect.Type) []string {
	var out []string
	// A value of a forbidden type or of its element (http.Client for
	// *http.Client) is forbidden too.
	forbidden := func(t reflect.Type) bool {
		return slices.Contains(forbiddenHTTPTypes, t) || slices.Contains(forbiddenHTTPTypes, reflect.PointerTo(t))
	}
	if forbidden(typ) {
		return append(out, name+" is "+typ.String())
	}
	results := func(what string, ft reflect.Type) {
		for i := range ft.NumOut() {
			if forbidden(ft.Out(i)) {
				out = append(out, what+" returns "+ft.Out(i).String())
			}
		}
	}
	if typ.Kind() == reflect.Func {
		results(name, typ)
		return out
	}
	if typ.Kind() == reflect.Struct {
		for i := range typ.NumField() {
			f := typ.Field(i)
			if f.IsExported() && forbidden(f.Type) {
				out = append(out, name+"."+f.Name+" is "+f.Type.String())
			}
		}
	}
	// The method set of the pointer holds the value methods as well.
	methods := reflect.PointerTo(typ)
	if typ.Kind() == reflect.Interface {
		methods = typ
	}
	for i := range methods.NumMethod() {
		m := methods.Method(i)
		results(name+"."+m.Name, m.Type)
	}
	return out
}

// exportedDecls returns the exported type, function and variable names
// declared in the non-test files of the package (methods excluded).
func exportedDecls(t *testing.T) (types, funcs, vars []string) {
	t.Helper()
	fset := token.NewFileSet()
	for _, file := range packageFiles(t, ".") {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ty, fn, va := exportedDeclsOf(f)
		types, funcs, vars = append(types, ty...), append(funcs, fn...), append(vars, va...)
	}
	sort.Strings(types)
	sort.Strings(funcs)
	sort.Strings(vars)
	return types, funcs, vars
}

// exportedDeclsOf returns the exported type, function and variable names
// of one file.
func exportedDeclsOf(f *ast.File) (types, funcs, vars []string) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.IsExported() {
				funcs = append(funcs, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					if sp.Name.IsExported() {
						types = append(types, sp.Name.Name)
					}
				case *ast.ValueSpec:
					if d.Tok != token.VAR {
						continue
					}
					for _, n := range sp.Names {
						if n.IsExported() {
							vars = append(vars, n.Name)
						}
					}
				}
			}
		}
	}
	return types, funcs, vars
}

// packageFiles returns the non-test Go files of dir.
func packageFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			out = append(out, dir+"/"+e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// testExposingType stands for a change that exposes the transport: the
// checker must report it.
type testExposingType struct {
	Transport http.RoundTripper
	inner     *http.Client
}

func (testExposingType) Request() *http.Request { return nil }

// testTransport stands for an exported variable that hands out a
// transport: the checker must report it.
var testTransport http.RoundTripper = http.DefaultTransport

// TestNoExportedHTTPAccess: no exported field of an exported type has
// the type *http.Client, http.RoundTripper, *http.Transport or
// *http.Request, no exported method or function returns one and no
// exported variable is one. The lists of exported types, functions and
// variables are complete.
func TestNoExportedHTTPAccess(t *testing.T) {
	types, funcs, vars := exportedDecls(t)
	listedTypes := make([]string, 0, len(exportedTypes))
	for name := range exportedTypes {
		listedTypes = append(listedTypes, name)
	}
	listedFuncs := make([]string, 0, len(exportedFuncs))
	for name := range exportedFuncs {
		listedFuncs = append(listedFuncs, name)
	}
	listedVars := make([]string, 0, len(exportedVars))
	for name := range exportedVars {
		listedVars = append(listedVars, name)
	}
	sort.Strings(listedTypes)
	sort.Strings(listedFuncs)
	sort.Strings(listedVars)
	if !slices.Equal(vars, listedVars) {
		t.Errorf("exported variables %v, listed %v: list every exported variable in exportedVars", vars, listedVars)
	}
	if !slices.Equal(types, listedTypes) {
		t.Errorf("exported types %v, listed %v: list every exported type in exportedTypes", types, listedTypes)
	}
	if !slices.Equal(funcs, listedFuncs) {
		t.Errorf("exported functions %v, listed %v: list every exported function in exportedFuncs", funcs, listedFuncs)
	}
	for name, typ := range exportedTypes {
		if bad := httpViolations(name, typ); len(bad) != 0 {
			t.Errorf("exported HTTP access: %v", bad)
		}
	}
	for name, fn := range exportedFuncs {
		if bad := httpViolations(name, reflect.TypeOf(fn)); len(bad) != 0 {
			t.Errorf("exported HTTP access: %v", bad)
		}
	}
	for name, typ := range exportedVars {
		if bad := httpViolations(name, typ); len(bad) != 0 {
			t.Errorf("exported HTTP access: %v", bad)
		}
	}
	// An exported transport variable is found in the source and reported.
	fragment := "package readonly\n\nimport \"net/http\"\n\nvar Transport http.RoundTripper = http.DefaultTransport\n\nvar inner = 1\n"
	f, err := parser.ParseFile(token.NewFileSet(), "fragment.go", fragment, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, fragVars := exportedDeclsOf(f); !slices.Equal(fragVars, []string{"Transport"}) {
		t.Errorf("exported variables of the fragment: %v, want [Transport]", fragVars)
	}
	if bad := httpViolations("Transport", reflect.TypeOf(&testTransport).Elem()); !slices.Equal(bad, []string{"Transport is http.RoundTripper"}) {
		t.Errorf("the checker reports %v for an exported RoundTripper variable", bad)
	}
	if bad := httpViolations("Client", reflect.TypeFor[http.Client]()); len(bad) != 1 {
		t.Errorf("the checker reports %v for an http.Client value", bad)
	}
	// The checker reports an exported transport field and a method
	// returning a request; the unexported field is no access.
	bad := httpViolations("testExposingType", reflect.TypeFor[testExposingType]())
	sort.Strings(bad)
	want := []string{"testExposingType.Request returns *http.Request", "testExposingType.Transport is http.RoundTripper"}
	if !slices.Equal(bad, want) {
		t.Fatalf("the checker reports %v, want %v", bad, want)
	}
	_ = testExposingType{}.inner
}
