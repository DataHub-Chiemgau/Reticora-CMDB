package server

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Architecture rule of API-03 (F-047, WP-408): a handler never writes the
// text of a repository or driver error into a response. Errors reach the
// client through package sentinels, typed rule errors with fixed texts,
// field errors or api.WriteRepoError; the error text itself only when it
// comes from api.ReadJSON or from a function whose doc comment carries a
// line "validation: <reason>" (its texts are domain validation texts).
//
// The scan covers every non-test file of backend/internal. A response text
// is the title or detail of an RFC 7807 problem (api.ProblemDetail,
// httpx.ProblemDetail) and the message of a field error (api.FieldError,
// api.ConflictError); every function whose string parameter reaches one of
// them (api.WriteError, httpx.ValidationError, a package's own writer) is a
// writer as well. In such a text the rule reports X.Error(), an error X
// formatted with fmt and a field of a *pgconn.PgError, unless X is
//
//   - a sentinel: errors.New of a literal, fmt.Errorf of a literal over
//     sentinels, or an element of a literal list of sentinels;
//   - assigned in an enclosing block from api.ReadJSON or a validation
//     function, and not reassigned in between (in a nested block or a
//     loop) from anything else;
//   - declared with a type whose Error method is a validation function;
//   - a field of a value a validation function returned (page.CursorError
//     of api.ParsePagination).
//
// An alias (e := err), a parameter and anything else is reported: each
// remaining place stands in handlerErrorAllowlist with the reason why its
// text is no repository text, or with the finding that is still open.

// errorEchoAllowance permits the echoes of one error in one function.
type errorEchoAllowance struct {
	file string // relative to backend/internal
	fn   string // Name or Recv.Name
	err  string // the error expression, e.g. err
	n    int    // number of echoes of err in fn
	// finding is the ID of the open finding of a raw error (F-nnn) or empty.
	finding string
	// reason is "validation: <why the text is a domain text>" or empty.
	reason string
}

// handlerErrorAllowlist names every place where a handler writes an error
// text the rule cannot trace to a validation: sentinels and rule errors a
// repository returns unwrapped or wrapped with a rule text. Each reason
// names the texts; a new place, a changed count or a stale entry fails
// TestHandlersDoNotEchoRepositoryErrors.
var handlerErrorAllowlist = []errorEchoAllowance{
	{"agent/handler.go", "Handler.ConfirmSite", "err", 1, "", "validation: ErrSiteNotOfClient, returned unwrapped"},
	{"agent/handler.go", "Handler.Enroll", "err", 1, "", "validation: ErrInvalidEnrollmentToken, returned unwrapped"},
	{"api/api.go", "WriteTypedProblem", "typed", 1, "", "validation: the typed problems are the entitlement errors (limit, feature, license) and reviews.Error with fixed texts"},
	{"asset/handler.go", "Handler.List", "err", 1, "", "validation: api.ErrInvalidCursor \"sort order changed\" of the keyset check"},
	{"assignment/handler.go", "Handler.List", "err", 1, "", "validation: api.ErrInvalidCursor \"sort order changed\" of the keyset check"},
	{"ci/handler.go", "Handler.Create", "err", 1, "", "validation: an entitlement refusal (LimitExceeded) without problem type, fixed text"},
	{"ci/handler.go", "Handler.List", "err", 1, "", "validation: api.ErrInvalidCursor \"sort order changed\" of the keyset check"},
	{"ci/handler.go", "Handler.Merge", "err", 3, "", "validation: ErrMergeConflict, ErrMergeSelf, ErrMergeUnsupported, returned unwrapped"},
	{"ci/handler.go", "Handler.Restore", "err", 1, "", "validation: ErrRetentionExpired, returned unwrapped"},
	{"ci/handler.go", "Handler.Unmerge", "err", 3, "", "validation: ErrSnapshotExpired, ErrMergeConflict, ErrMergeUnsupported, returned unwrapped"},
	{"ci/handler.go", "writeConcurrencyError", "err", 1, "", "validation: ErrPreconditionRequired, ErrPreconditionFailed, returned unwrapped"},
	{"citype/attribute_definition.go", "attributeDBError", "pgErr.Message", 1, "", "validation: only for constraint attribute_definition_composition, the RAISE texts of the composition rules (MET-16, migration 000107)"},
	{"citype/handler.go", "writeAttributeError", "err", 2, "", "validation: ErrAttributeNotFound, ErrAttributeReadOnly, returned unwrapped"},
	{"composition/handler.go", "Handler.Create", "err", 1, "", "validation: the fixed rule texts of the repository and the RAISE text of trg_composition_no_cycle, selected by prefix"},
	{"discovery/devicerecord.go", "readIngestItems", "err", 1, "", "validation: gzip.NewReader of the request body (header), like api.ReadJSON"},
	{"discovery/discovery.go", "Handler.EnrollCollector", "err", 1, "", "validation: ErrInvalidCSR with the parse error of the collector's CSR (ca.go)"},
	{"discovery/discovery.go", "Handler.Heartbeat", "err", 1, "", "validation: ErrCollectorDisabled, returned unwrapped"},
	{"document/handler.go", "Handler.List", "err", 1, "", "validation: api.ErrInvalidCursor \"sort order changed\" of the keyset check"},
	{"identity/apikey_handler.go", "APIKeyHandler.Create", "err", 1, "", "validation: GenerateAPIKey refuses the environment of the request; otherwise a crypto/rand failure"},
	{"identity/handler.go", "Handler.Refresh", "err", 1, "", "validation: ErrRefreshInvalid, ErrRefreshReused, ErrSessionRevoked, returned unwrapped (session.go)"},
	{"identity/handler.go", "writeContextError", "err", 1, "", "validation: context.Canceled, context.DeadlineExceeded"},
	{"identity/handler.go", "writeIdentityError", "err", 4, "", "validation: ErrUserInactive, ErrFirstLoginNotPermitted and the fixed texts of the ID token and configuration checks (oidc.go, jwks.go) selected by their markers; IdP and database errors go to the log"},
	{"ipam/handler.go", "writeRepoError", "err", 3, "", "validation: ErrValidation, ErrConflict, ErrInUse with the rule texts of vlan.go, vrf.go, repository.go"},
	{"locations/handler.go", "writeError", "err", 1, "", "validation: ErrInvalidInput with the fixed texts of model.go and mapError"},
	{"locations/handler.go", "writeFieldProblem", "fe", 1, "", "validation: FieldError of the location rules and the RAISE texts of location_no_cycle, location_parent_matrix, location_root_is_site"},
	{"monitoring/monitoring.go", "Handler.UpdateAlert", "err", 1, "", "validation: time.ParseDuration of the request's duration"},
	{"notify/handler.go", "writeError", "blocked.Err", 1, "", "validation: the egress refusal of the SMTP host or port (CheckEgress)"},
	{"notify/handler.go", "writeError", "err", 3, "", "validation: ErrValidation with the rule texts of channel.go, notifier.go, smtp.go; ErrNotFound, ErrConflict unwrapped"},
	{"operator/orgs.go", "Handler.CreateOrg", "err", 2, "", "validation: ErrCreationUnavailable, ErrSlugTaken, returned unwrapped"},
	{"override/handler.go", "Handler.SetOverride", "err", 1, "", "validation: ErrNotOverridable, returned unwrapped"},
	{"relationship/relationship.go", "Handler.Delete", "err", 1, "", "validation: ErrForceRequired, returned unwrapped"},
	{"relationshiptype/handler.go", "writeError", "err", 1, "", "validation: ErrInvalid with the REL-01 rule text (Type.Validate)"},
	{"reviews/handler.go", "writeError", "err", 2, "", "validation: ErrNotFound, ErrAlreadyResolved, returned unwrapped"},
	{"savedview/view_rules.go", "ValidateView", "err", 2, "", "validation: the JSON round trip of the request's filter_spec, like api.ReadJSON"},
	{"signup/handler.go", "writeError", "err", 3, "", "validation: ErrUnavailable, ErrInvalidToken, ErrExpired, returned unwrapped"},
	{"signup/protect.go", "writeProtectionError", "err", 3, "", "validation: ErrSignupRateLimit, ErrCaptchaInvalid, ErrDomainBlocked, returned unwrapped"},
	{"stocktake/handler.go", "Handler.Complete", "err", 1, "", "validation: ErrAlreadyCompleted, returned unwrapped"},
	{"stocktake/handler.go", "Handler.List", "err", 1, "", "validation: api.ErrInvalidCursor \"sort order changed\" of the keyset check"},
	{"ticket/handler.go", "Handler.List", "err", 1, "", "validation: api.ErrInvalidCursor \"sort order changed\" of the keyset check"},
	{"user/handler.go", "Handler.DeleteUser", "err", 1, "", "validation: ErrUserOwnsOpenObjects with the count, ErrUserReferenced"},
	{"user/handler.go", "writeAssignmentError", "err", 1, "", "validation: errRoleNotFound, errUserNotFound, errTeamNotFound, errAssignmentNotFound, returned unwrapped"},
	{"user/invitation_handler.go", "writeInvitationError", "err", 4, "", "validation: ErrInvitationNotFound, ErrInvitationInvalid, ErrInvitationExpired, ErrInvitationAccepted, ErrInvitationUserExists, returned unwrapped"},
	{"user/service_account_handler.go", "writeServiceAccountError", "err", 2, "", "validation: ErrServiceAccountInUse unwrapped and the two fixed texts compared literally"},
	{"webhook/webhook.go", "Handler.ListDeadLetters", "err", 1, "", "validation: ErrNoDeliveryStore, returned unwrapped"},
	{"webhook/webhook.go", "Handler.ListDeliveries", "err", 1, "", "validation: ErrNoDeliveryStore, returned unwrapped"},
	{"webhook/webhook.go", "Handler.RetryDelivery", "err", 2, "", "validation: ErrNoDeliveryStore, ErrNotRetryable, returned unwrapped"},
}

// TestHandlersDoNotEchoRepositoryErrors is the architecture test of API-03
// (F-047): every error text that reaches a response in backend/internal is
// a validation text or stands in handlerErrorAllowlist.
func TestHandlersDoNotEchoRepositoryErrors(t *testing.T) {
	s := newEchoScanner("..")
	if err := s.loadTree(); err != nil {
		t.Fatal(err)
	}
	echoes := s.scan()
	// More than 250 error texts reach a response, most of them ReadJSON
	// errors; far fewer means the scan lost its writers.
	if s.examined < 250 {
		t.Fatalf("only %d error texts in responses examined; the scan is broken", s.examined)
	}
	t.Logf("%d packages, %d error texts in responses, %d not traced to a validation", len(s.order), s.examined, len(echoes))
	for _, msg := range echoViolations(echoes, handlerErrorAllowlist) {
		t.Error(msg)
	}
}

// TestHandlerErrorRuleFixtures is the self-test of the rule over
// testdata/handler_errors: each function of flagged.go writes one
// repository error text and is reported once, allowed.go is not reported;
// the allowlist fails on a new place, a stale entry and an entry without
// finding or reason.
func TestHandlerErrorRuleFixtures(t *testing.T) {
	s := newEchoScanner("..")
	p, err := s.load("fixture/handler_errors", "server/testdata/handler_errors")
	if err != nil || p == nil {
		t.Fatalf("load fixtures: %v", err)
	}
	s.order = append(s.order, p)
	got := map[string]int{}
	for _, e := range s.scan() {
		got[filepath.Base(e.file)+" "+e.fn]++
		t.Log(e)
	}
	want := map[string]int{}
	for _, fn := range []string{
		"handler.repositoryError", "handler.alias", "handler.formatted", "handler.stringVariable",
		"handler.fieldMessage", "handler.fieldMessageUnkeyed", "handler.driverField", "writeAny", "handler.localWriter",
		"handler.reassigned", "handler.reassignedInLoop",
	} {
		want["flagged.go "+fn] = 1
	}
	for key, n := range got {
		if want[key] != n {
			t.Errorf("%s: reported %d times, want %d", key, n, want[key])
		}
	}
	for key, n := range want {
		if got[key] == 0 {
			t.Errorf("%s: not reported, want %d", key, n)
		}
	}

	echo := errorEcho{file: "x/handler.go", fn: "Handler.Create", expr: "err", line: 7, why: "the result of h.repo.Create"}
	listed := errorEchoAllowance{"x/handler.go", "Handler.Create", "err", 1, "", "validation: fixed"}
	cases := []struct {
		name   string
		echoes []errorEcho
		allow  []errorEchoAllowance
		want   []string
	}{
		{"listed", []errorEcho{echo}, []errorEchoAllowance{listed}, nil},
		{"listed with a finding", []errorEcho{echo}, []errorEchoAllowance{{"x/handler.go", "Handler.Create", "err", 1, "F-355", ""}}, nil},
		{"new place", []errorEcho{echo}, nil, []string{"x/handler.go:7 Handler.Create"}},
		{"second place in a listed function", []errorEcho{echo, echo}, []errorEchoAllowance{listed},
			[]string{"Handler.Create err: 2 echoes, the allowlist permits 1"}},
		{"stale entry", nil, []errorEchoAllowance{listed}, []string{"matches no echo"}},
		{"neither finding nor reason", []errorEcho{echo}, []errorEchoAllowance{{"x/handler.go", "Handler.Create", "err", 1, "", ""}},
			[]string{"needs a finding (F-nnn) or a reason"}},
		{"reason without validation", []errorEcho{echo}, []errorEchoAllowance{{"x/handler.go", "Handler.Create", "err", 1, "", "fixed text"}},
			[]string{"needs a finding (F-nnn) or a reason"}},
		{"malformed finding", []errorEcho{echo}, []errorEchoAllowance{{"x/handler.go", "Handler.Create", "err", 1, "F-47a", ""}},
			[]string{"needs a finding (F-nnn) or a reason"}},
		{"finding and reason", []errorEcho{echo}, []errorEchoAllowance{{"x/handler.go", "Handler.Create", "err", 1, "F-355", "validation: fixed"}},
			[]string{"needs a finding (F-nnn) or a reason"}},
		{"no function", []errorEcho{echo}, []errorEchoAllowance{{"x/handler.go", "", "err", 1, "", "validation: fixed"}},
			[]string{"names no file or function", "x/handler.go:7 Handler.Create"}},
	}
	for _, tc := range cases {
		got := echoViolations(tc.echoes, tc.allow)
		if len(got) != len(tc.want) {
			t.Errorf("%s: %q, want %d messages %q", tc.name, got, len(tc.want), tc.want)
			continue
		}
		for i, w := range tc.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%s: message %q does not contain %q", tc.name, got[i], w)
			}
		}
	}
}

// allowanceReason is the form of a reason: validation and why.
var allowanceReason = regexp.MustCompile(`^validation: \S`)

// allowanceFinding is the form of a finding ID.
var allowanceFinding = regexp.MustCompile(`^F-\d{3,}$`)

// echoViolations compares the echoes with the allowlist: an echo of an
// unlisted error, a count other than the listed one, a stale entry and an
// entry without finding or reason (or with both) are violations.
func echoViolations(echoes []errorEcho, allow []errorEchoAllowance) []string {
	type key struct{ file, fn, err string }
	byKey := map[key][]errorEcho{}
	for _, e := range echoes {
		k := key{e.file, e.fn, e.expr}
		byKey[k] = append(byKey[k], e)
	}
	var out []string
	listed := map[key]bool{}
	for _, a := range allow {
		if a.file == "" || a.fn == "" {
			out = append(out, fmt.Sprintf("allowlist entry %q %q %q names no file or function", a.file, a.fn, a.err))
			continue
		}
		if (a.finding == "") == (a.reason == "") ||
			a.finding != "" && !allowanceFinding.MatchString(a.finding) ||
			a.reason != "" && !allowanceReason.MatchString(a.reason) {
			out = append(out, fmt.Sprintf("allowlist entry %s %s %s needs a finding (F-nnn) or a reason \"validation: ...\", not both",
				a.file, a.fn, a.err))
		}
		k := key{a.file, a.fn, a.err}
		listed[k] = true
		switch n := len(byKey[k]); {
		case n == 0:
			out = append(out, fmt.Sprintf("allowlist entry %s %s %s matches no echo any more; remove it", a.file, a.fn, a.err))
		case n != a.n:
			out = append(out, fmt.Sprintf("%s %s %s: %d echoes, the allowlist permits %d (%s)", a.file, a.fn, a.err, n, a.n, byKey[k][0]))
		}
	}
	for _, e := range echoes {
		if !listed[key{e.file, e.fn, e.expr}] {
			out = append(out, e.String()+"; answer with a sentinel, a fixed text or api.WriteRepoError (API-03, F-047), "+
				"mark a validation function \"// validation: <reason>\" or list the place in handlerErrorAllowlist")
		}
	}
	return out
}

// modulePath is the import path of the backend module.
const modulePath = "github.com/DataHub-Chiemgau/Reticora-CMDB/backend"

// apiPath is the package of the shared response writers and ReadJSON.
const apiPath = modulePath + "/internal/api"

// errorEcho is one place where the text of an error reaches a response.
type errorEcho struct {
	file string // relative to backend/internal, slash-separated
	fn   string // enclosing function: Name or Recv.Name
	expr string // the error whose text is written, e.g. err
	line int
	why  string // where the error comes from
}

func (e errorEcho) String() string {
	return fmt.Sprintf("%s:%d %s: the text of %s reaches the response (%s)", e.file, e.line, e.fn, e.expr, e.why)
}

// echoScanner finds error texts that reach a response. It parses and
// type-checks each package on its own: imports resolve to empty packages,
// so only the package's own declarations carry types. That is enough to
// tell variables apart (scopes, shadowing) and to resolve the functions,
// methods and sentinels of the backend packages by their declarations.
type echoScanner struct {
	fset     *token.FileSet
	internal string              // backend/internal on disk
	pkgs     map[string]*echoPkg // by import path
	order    []*echoPkg          // the packages to scan
	// sinks are the parameters of each function that reach a response.
	sinks map[*ast.FuncDecl]map[int]bool
	// examined counts the error texts found in responses.
	examined int
}

type echoPkg struct {
	path  string
	files map[*ast.File]string // file -> path relative to internal
	info  *types.Info
	tpkg  *types.Package
	funcs map[types.Object]*ast.FuncDecl
	named map[string]*ast.FuncDecl  // function or Recv.Method by name
	vals  map[types.Object]ast.Expr // value of package-level variables
	// declared maps the position of a declared name (parameter, field,
	// variable) onto its type expression.
	declared map[token.Pos]ast.Expr
}

func newEchoScanner(internal string) *echoScanner {
	return &echoScanner{
		fset:     token.NewFileSet(),
		internal: internal,
		pkgs:     map[string]*echoPkg{},
		sinks:    map[*ast.FuncDecl]map[int]bool{},
	}
}

// loadTree loads every package of backend/internal outside testdata.
func (s *echoScanner) loadTree() error {
	return filepath.WalkDir(s.internal, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(s.internal, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		importPath := modulePath + "/internal"
		if rel != "." {
			importPath += "/" + rel
		}
		p, err := s.load(importPath, rel)
		if p != nil {
			s.order = append(s.order, p)
		}
		return err
	})
}

// load parses and checks the non-test files of one directory (relative to
// internal) under the given import path; nil when it holds none.
func (s *echoScanner) load(importPath, rel string) (*echoPkg, error) {
	if p, ok := s.pkgs[importPath]; ok {
		return p, nil
	}
	s.pkgs[importPath] = nil
	dir := filepath.Join(s.internal, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	p := &echoPkg{
		path:     importPath,
		files:    map[*ast.File]string{},
		funcs:    map[types.Object]*ast.FuncDecl{},
		named:    map[string]*ast.FuncDecl{},
		vals:     map[types.Object]ast.Expr{},
		declared: map[token.Pos]ast.Expr{},
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(s.fset, filepath.Join(dir, name), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
		p.files[f] = strings.TrimPrefix(rel+"/"+name, "./")
	}
	if len(files) == 0 {
		return nil, nil
	}
	p.info = &types.Info{
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	// Errors are expected: every imported name is unknown.
	conf := types.Config{Importer: emptyImporter{}, Error: func(error) {}}
	p.tpkg, _ = conf.Check(importPath, s.fset, files, p.info)
	s.pkgs[importPath] = p
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Field:
				for _, name := range x.Names {
					p.declared[name.Pos()] = x.Type
				}
			case *ast.ValueSpec:
				for _, name := range x.Names {
					if x.Type != nil {
						p.declared[name.Pos()] = x.Type
					}
				}
			}
			return true
		})
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if obj := p.info.Defs[d.Name]; obj != nil {
					p.funcs[obj] = d
				}
				if r := recvName(d); r != "" {
					p.named[r+"."+d.Name.Name] = d
				} else if d.Recv == nil {
					p.named[d.Name.Name] = d
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Values) != len(vs.Names) {
						continue
					}
					for i, name := range vs.Names {
						if obj := p.info.Defs[name]; obj != nil {
							p.vals[obj] = vs.Values[i]
						}
					}
				}
			}
		}
	}
	return p, nil
}

// pkgOf returns the backend package an import path names, loading it on
// first use; nil outside backend/internal.
func (s *echoScanner) pkgOf(importPath string) *echoPkg {
	if p, ok := s.pkgs[importPath]; ok {
		return p
	}
	rel, ok := strings.CutPrefix(importPath, modulePath+"/internal/")
	if !ok {
		return nil
	}
	p, err := s.load(importPath, rel)
	if err != nil {
		return nil
	}
	return p
}

// emptyImporter resolves every import to an empty package of the name the
// path suggests: the checker then records which package a qualified name
// refers to without type-checking the dependency.
type emptyImporter struct{}

func (emptyImporter) Import(path string) (*types.Package, error) {
	name := path[strings.LastIndex(path, "/")+1:]
	if len(name) > 1 && name[0] == 'v' && strings.Trim(name[1:], "0123456789") == "" {
		trimmed := strings.TrimSuffix(path, "/"+name)
		name = trimmed[strings.LastIndex(trimmed, "/")+1:]
	}
	if i := strings.IndexAny(name, ".-"); i > 0 {
		name = name[:i]
	}
	p := types.NewPackage(path, name)
	p.MarkComplete()
	return p, nil
}

func recvName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch x := t.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return types.ExprString(x.X)
	case *ast.IndexListExpr:
		return types.ExprString(x.X)
	}
	return ""
}

func funcName(fd *ast.FuncDecl) string {
	if r := recvName(fd); r != "" {
		return r + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// validationDoc is the mark of a validation function: a doc comment line
// "validation: <reason>".
var validationDoc = regexp.MustCompile(`(?m)^validation: \S`)

func isValidation(fd *ast.FuncDecl) bool {
	return fd != nil && fd.Doc != nil && validationDoc.MatchString(fd.Doc.Text())
}

// importedPath returns the import path when e names an imported package.
func importedPath(p *echoPkg, e ast.Expr) string {
	id, ok := e.(*ast.Ident)
	if !ok {
		return ""
	}
	if pn, ok := p.info.Uses[id].(*types.PkgName); ok {
		return pn.Imported().Path()
	}
	return ""
}

// ---- writers ----

// responseFields are the struct fields whose value is written into a
// response: title and detail of a problem, the message of a field error.
var responseFields = map[string]map[string]bool{
	"ProblemDetail": {"Title": true, "Detail": true},
	"FieldError":    {"Message": true},
	"ConflictError": {"Message": true},
}

// responseFieldPkgs are the packages whose types of responseFields are
// written.
var responseFieldPkgs = map[string]bool{
	apiPath:                                 true,
	modulePath + "/internal/platform/httpx": true,
}

func (s *echoScanner) scan() []errorEcho {
	s.findWriters()
	var out []errorEcho
	for _, p := range s.order {
		for f, rel := range p.files {
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fl := newFlow(p, fd)
				s.eachResponseText(p, fd, func(e ast.Expr) {
					for _, src := range s.errorTexts(fl, e) {
						out = append(out, errorEcho{file: rel, fn: funcName(fd), expr: src.expr, why: src.why,
							line: s.fset.Position(src.pos).Line})
					}
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].line < out[j].line
	})
	return out
}

// findWriters marks the string parameters of every function that reach a
// response (api.WriteError, httpx.ValidationError, a package's own writer)
// until no function changes. It covers every package loaded so far,
// including the ones loaded on the way (the api package of the fixtures).
func (s *echoScanner) findWriters() {
	for changed := true; changed; {
		loaded := len(s.pkgs)
		changed = false
		for _, p := range s.loaded() {
			for f := range p.files {
				for _, decl := range f.Decls {
					fd, ok := decl.(*ast.FuncDecl)
					if !ok || fd.Body == nil {
						continue
					}
					params := stringParams(p, fd)
					if len(params) == 0 {
						continue
					}
					fl := newFlow(p, fd)
					s.eachResponseText(p, fd, func(e ast.Expr) {
						for _, obj := range fl.stringParamsIn(e) {
							if i, ok := params[obj]; ok && !s.sinks[fd][i] {
								if s.sinks[fd] == nil {
									s.sinks[fd] = map[int]bool{}
								}
								s.sinks[fd][i] = true
								changed = true
							}
						}
					})
				}
			}
		}
		changed = changed || len(s.pkgs) != loaded
	}
}

// loaded returns the packages loaded so far, sorted by import path.
func (s *echoScanner) loaded() []*echoPkg {
	var out []*echoPkg
	for _, p := range s.pkgs {
		if p != nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// stringParams maps the string parameters of fd onto their index.
func stringParams(p *echoPkg, fd *ast.FuncDecl) map[types.Object]int {
	out := map[types.Object]int{}
	i := 0
	for _, field := range fd.Type.Params.List {
		if len(field.Names) == 0 {
			i++
			continue
		}
		for _, name := range field.Names {
			if obj := p.info.Defs[name]; obj != nil {
				if b, ok := obj.Type().(*types.Basic); ok && b.Kind() == types.String {
					out[obj] = i
				}
			}
			i++
		}
	}
	return out
}

// eachResponseText calls fn with every expression in fd whose value is
// written into a response.
func (s *echoScanner) eachResponseText(p *echoPkg, fd *ast.FuncDecl, fn func(ast.Expr)) {
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CompositeLit:
			fields := s.responseLiteral(p, x.Type)
			for _, elt := range x.Elts {
				kv, keyed := elt.(*ast.KeyValueExpr)
				switch {
				case fields == nil:
				case !keyed:
					// Without keys every element counts.
					fn(elt)
				default:
					if key, ok := kv.Key.(*ast.Ident); ok && fields[key.Name] {
						fn(kv.Value)
					}
				}
			}
		case *ast.CallExpr:
			for i := range s.sinks[s.callee(p, x)] {
				if i < len(x.Args) {
					fn(x.Args[i])
				}
			}
		}
		return true
	})
}

// responseLiteral returns the response fields of a composite literal type.
func (s *echoScanner) responseLiteral(p *echoPkg, t ast.Expr) map[string]bool {
	switch x := t.(type) {
	case *ast.Ident:
		if responseFieldPkgs[p.path] {
			return responseFields[x.Name]
		}
	case *ast.SelectorExpr:
		if responseFieldPkgs[importedPath(p, x.X)] {
			return responseFields[x.Sel.Name]
		}
	}
	return nil
}

// callee resolves the function a call invokes to its declaration: a
// function of the same or another backend package, or a method of a type
// a backend package declares.
func (s *echoScanner) callee(p *echoPkg, call *ast.CallExpr) *ast.FuncDecl {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return p.funcs[p.info.Uses[fn]]
	case *ast.SelectorExpr:
		if path := importedPath(p, fn.X); path != "" {
			if q := s.pkgOf(path); q != nil {
				return q.named[fn.Sel.Name]
			}
			return nil
		}
		if sel := p.info.Selections[fn]; sel != nil {
			if fd := p.funcs[sel.Obj()]; fd != nil {
				return fd
			}
		}
		// A method of a type the checker could not see (declared in
		// another package): resolved through the declared type of the
		// receiver, h.egress of type egress.Options.
		if q, typeName := s.declaredType(p, fn.X); q != nil {
			return q.named[typeName+"."+fn.Sel.Name]
		}
	}
	return nil
}

// declaredType returns the package and the name of the declared type of a
// variable or field.
func (s *echoScanner) declaredType(p *echoPkg, e ast.Expr) (*echoPkg, string) {
	var obj types.Object
	switch x := e.(type) {
	case *ast.Ident:
		obj = p.info.Uses[x]
	case *ast.SelectorExpr:
		if sel := p.info.Selections[x]; sel != nil {
			obj = sel.Obj()
		}
	}
	if obj == nil {
		return nil, ""
	}
	return s.namedType(p, p.declared[obj.Pos()])
}

// namedType resolves a type expression (T, *T, pkg.T) to its package and
// name.
func (s *echoScanner) namedType(p *echoPkg, t ast.Expr) (*echoPkg, string) {
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch x := t.(type) {
	case *ast.Ident:
		return p, x.Name
	case *ast.SelectorExpr:
		if q := s.pkgOf(importedPath(p, x.X)); q != nil {
			return q, x.Sel.Name
		}
	}
	return nil, ""
}

// ---- assignments inside one function ----

// flow answers which assignments a use of a local variable may see.
type flow struct {
	p     *echoPkg
	defs  map[types.Object][]def
	loops []ast.Node
}

type def struct {
	node      ast.Node // the assignment; the range expression of a range variable
	container ast.Node // the block or statement whose scope holds it
	value     ast.Expr // the assigned expression; nil for var x T
	typ       ast.Expr // the declared type of var x T
	rangeVar  bool
}

func newFlow(p *echoPkg, fd *ast.FuncDecl) *flow {
	f := &flow{p: p, defs: map[types.Object][]def{}}
	object := func(id *ast.Ident) types.Object {
		if obj := p.info.Defs[id]; obj != nil {
			return obj
		}
		return p.info.Uses[id]
	}
	var stack []ast.Node
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		var parent ast.Node
		for i := len(stack) - 1; i >= 0; i-- {
			if _, labeled := stack[i].(*ast.LabeledStmt); !labeled {
				parent = stack[i]
				break
			}
		}
		stack = append(stack, n)
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" || object(id) == nil {
					continue
				}
				d := def{node: x, container: parent, value: x.Rhs[0]}
				if len(x.Rhs) == len(x.Lhs) {
					d.value = x.Rhs[i]
				}
				f.defs[object(id)] = append(f.defs[object(id)], d)
			}
		case *ast.DeclStmt:
			gd, ok := x.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					obj := p.info.Defs[id]
					if obj == nil {
						continue
					}
					d := def{node: x, container: parent, typ: vs.Type}
					switch {
					case len(vs.Values) == len(vs.Names):
						d.value = vs.Values[i]
					case len(vs.Values) == 1:
						d.value = vs.Values[0]
					}
					f.defs[obj] = append(f.defs[obj], d)
				}
			}
		case *ast.RangeStmt:
			f.loops = append(f.loops, x)
			for _, e := range []ast.Expr{x.Key, x.Value} {
				if id, ok := e.(*ast.Ident); ok && object(id) != nil {
					f.defs[object(id)] = append(f.defs[object(id)], def{node: x.X, container: x, value: x.X, rangeVar: true})
				}
			}
		case *ast.ForStmt:
			f.loops = append(f.loops, x)
		}
		return true
	})
	return f
}

// reaching returns the assignments of obj a use at pos may see. The first
// is the last assignment before pos in a block that encloses pos (nil when
// there is none); the others are the assignments after it and before pos
// in nested blocks, and the assignments after pos in a loop that encloses
// pos.
func (f *flow) reaching(obj types.Object, pos token.Pos) (first *def, others []def) {
	defs := f.defs[obj]
	for i := range defs {
		d := &defs[i]
		if d.node.End() > pos || pos < d.container.Pos() || pos >= d.container.End() {
			continue
		}
		if first == nil || d.node.Pos() > first.node.Pos() {
			first = d
		}
	}
	for i := range defs {
		d := &defs[i]
		if d == first {
			continue
		}
		reaches := d.node.End() <= pos && (first == nil || d.node.Pos() > first.node.Pos())
		for _, l := range f.loops {
			if l.Pos() <= pos && pos < l.End() && d.node.Pos() > pos && d.node.End() <= l.End() {
				reaches = true
			}
		}
		if reaches {
			others = append(others, *d)
		}
	}
	return first, others
}

// all returns first and others as one list.
func all(first *def, others []def) []def {
	if first == nil {
		return others
	}
	return append([]def{*first}, others...)
}

// stringParamsIn returns the variables without assignment in the function
// (its parameters) that a string expression is built from.
func (f *flow) stringParamsIn(e ast.Expr) []types.Object {
	var out []types.Object
	seen := map[types.Object]bool{}
	var visit func(ast.Expr)
	visit = func(e ast.Expr) {
		ast.Inspect(e, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.Ident:
				obj, ok := f.p.info.Uses[x].(*types.Var)
				if !ok || seen[obj] {
					return true
				}
				seen[obj] = true
				first, others := f.reaching(obj, x.Pos())
				if first == nil {
					out = append(out, obj)
				}
				for _, d := range all(first, others) {
					if d.value != nil && !d.rangeVar {
						visit(d.value)
					}
				}
			}
			return true
		})
	}
	visit(e)
	return out
}

// ---- error texts ----

type errorText struct {
	expr string
	why  string
	pos  token.Pos
}

// errorTexts returns the errors whose text a response expression carries
// and that are no sentinel or validation text. Local string variables are
// followed to their assignments.
func (s *echoScanner) errorTexts(fl *flow, e ast.Expr) []errorText {
	var out []errorText
	report := func(x ast.Expr, pos token.Pos) {
		s.examined++
		if why, bad := s.errorOrigin(fl, x, pos, 0); bad {
			out = append(out, errorText{expr: types.ExprString(x), why: why, pos: pos})
		}
	}
	seen := map[types.Object]bool{}
	var visit func(ast.Expr, int)
	visit = func(e ast.Expr, depth int) {
		ast.Inspect(e, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "Error" && len(x.Args) == 0 {
					report(sel.X, x.Pos())
					return false
				}
				if ok && importedPath(fl.p, sel.X) == "fmt" && len(x.Args) > 1 {
					for _, arg := range x.Args[1:] {
						if looksLikeError(fl.p, arg) {
							report(arg, x.Pos())
						}
					}
				}
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && fl.isPgError(id) {
					s.examined++
					out = append(out, errorText{expr: types.ExprString(x), why: "a field of a *pgconn.PgError", pos: x.Pos()})
					return false
				}
			case *ast.Ident:
				obj, ok := fl.p.info.Uses[x].(*types.Var)
				if !ok || seen[obj] || depth > 4 || !isStringOrUnknown(obj) {
					return true
				}
				seen[obj] = true
				first, others := fl.reaching(obj, x.Pos())
				for _, d := range all(first, others) {
					if d.value != nil && !d.rangeVar {
						visit(d.value, depth+1)
					}
				}
			}
			return true
		})
	}
	visit(e, 0)
	return out
}

func isStringOrUnknown(obj types.Object) bool {
	if obj.Type() == types.Typ[types.Invalid] {
		return true
	}
	b, ok := obj.Type().Underlying().(*types.Basic)
	return ok && b.Info()&types.IsString != 0
}

var errorType = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

// errName is the naming convention of error variables, used where the
// type is unknown (the result of a call into another package).
var errName = regexp.MustCompile(`^(err|e)$|Err$|err$`)

// looksLikeError reports whether a fmt argument is an error value.
func looksLikeError(p *echoPkg, e ast.Expr) bool {
	var id *ast.Ident
	switch x := e.(type) {
	case *ast.Ident:
		id = x
	case *ast.SelectorExpr:
		id = x.Sel
	default:
		return false
	}
	if v, ok := p.info.Uses[id].(*types.Var); ok && v.Type() != types.Typ[types.Invalid] {
		return types.Implements(v.Type(), errorType) || types.Implements(types.NewPointer(v.Type()), errorType)
	}
	return errName.MatchString(id.Name)
}

// isPgError reports whether id is a variable declared as *pgconn.PgError.
func (f *flow) isPgError(id *ast.Ident) bool {
	obj, ok := f.p.info.Uses[id].(*types.Var)
	if !ok {
		return false
	}
	t := f.p.declared[obj.Pos()]
	if star, isPointer := t.(*ast.StarExpr); isPointer {
		t = star.X
	}
	sel, ok := t.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "PgError" && strings.HasSuffix(importedPath(f.p, sel.X), "/pgconn")
}

// errorOrigin classifies the error e used at pos: bad reports that its
// text may carry a repository or driver error, why says where it comes
// from.
func (s *echoScanner) errorOrigin(fl *flow, e ast.Expr, pos token.Pos, depth int) (why string, bad bool) {
	if depth > 4 {
		return "nested too deep", true
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return s.errorOrigin(fl, x.X, pos, depth+1)
	case *ast.StarExpr:
		return s.errorOrigin(fl, x.X, pos, depth+1)
	case *ast.CallExpr:
		return s.callOrigin(fl.p, x)
	case *ast.SelectorExpr:
		if path := importedPath(fl.p, x.X); path != "" {
			return s.sentinelOrigin(s.pkgOf(path), x.Sel.Name, path+"."+x.Sel.Name)
		}
		// A field of a value a validation function returned carries its
		// texts: page.CursorError of page := api.ParsePagination(r).
		if call := fl.valueCall(x.X, pos); call != nil {
			if why, bad := s.callOrigin(fl.p, call); bad {
				return "a field of " + types.ExprString(x.X) + ", " + why, true
			}
			return "", false
		}
		return "a field of " + types.ExprString(x.X), true
	case *ast.Ident:
		v, ok := fl.p.info.Uses[x].(*types.Var)
		if !ok {
			return "not a variable", true
		}
		if v.Parent() == fl.p.tpkg.Scope() {
			return s.sentinelOrigin(fl.p, v.Name(), v.Name())
		}
		first, others := fl.reaching(v, pos)
		if first == nil {
			return "a parameter or a variable without assignment in an enclosing block", true
		}
		for _, d := range all(first, others) {
			if why, bad := s.defOrigin(fl, d, depth); bad {
				return why, true
			}
		}
		return "", false
	}
	return "the expression " + types.ExprString(e), true
}

// defOrigin classifies the error an assignment stores.
func (s *echoScanner) defOrigin(fl *flow, d def, depth int) (string, bool) {
	if d.rangeVar {
		// An element of a literal list of sentinels, given directly or as
		// a package variable.
		list := d.value
		if id, ok := list.(*ast.Ident); ok {
			if obj, ok := fl.p.info.Uses[id].(*types.Var); ok && obj.Parent() == fl.p.tpkg.Scope() {
				list = fl.p.vals[obj]
			}
		}
		if s.fixedList(fl.p, list) {
			return "", false
		}
		return "a range variable", true
	}
	switch v := d.value.(type) {
	case nil:
		if d.typ != nil {
			return s.typedOrigin(fl.p, d.typ)
		}
		return "declared without value", true
	case *ast.CallExpr:
		return s.callOrigin(fl.p, v)
	case *ast.UnaryExpr:
		if lit, ok := v.X.(*ast.CompositeLit); ok && v.Op == token.AND {
			return s.typedOrigin(fl.p, lit.Type)
		}
	case *ast.CompositeLit:
		return s.typedOrigin(fl.p, v.Type)
	case *ast.SelectorExpr:
		if path := importedPath(fl.p, v.X); path != "" {
			return s.sentinelOrigin(s.pkgOf(path), v.Sel.Name, path+"."+v.Sel.Name)
		}
		return s.errorOrigin(fl, v, d.node.Pos(), depth+1)
	case *ast.Ident:
		if obj, ok := fl.p.info.Uses[v].(*types.Var); ok && obj.Parent() == fl.p.tpkg.Scope() {
			return s.sentinelOrigin(fl.p, v.Name, v.Name)
		}
		// An alias hides where the error comes from.
		return "an alias of " + v.Name, true
	}
	return "assigned from " + types.ExprString(d.value), true
}

// valueCall returns the call whose result e holds: the call itself, or the
// only assignment of a variable.
func (f *flow) valueCall(e ast.Expr, pos token.Pos) *ast.CallExpr {
	switch x := e.(type) {
	case *ast.CallExpr:
		return x
	case *ast.Ident:
		v, ok := f.p.info.Uses[x].(*types.Var)
		if !ok {
			return nil
		}
		first, others := f.reaching(v, pos)
		if first == nil || len(others) > 0 || first.rangeVar {
			return nil
		}
		call, _ := first.value.(*ast.CallExpr)
		return call
	}
	return nil
}

// callOrigin classifies an error a call returns: api.ReadJSON, errors.New
// of a literal and validation functions give no repository text.
func (s *echoScanner) callOrigin(p *echoPkg, call *ast.CallExpr) (string, bool) {
	if fn, ok := call.Fun.(*ast.SelectorExpr); ok {
		path := importedPath(p, fn.X)
		if path == apiPath && fn.Sel.Name == "ReadJSON" {
			return "", false
		}
		if path == "errors" && fn.Sel.Name == "New" && len(call.Args) == 1 {
			if _, ok := call.Args[0].(*ast.BasicLit); ok {
				return "", false
			}
		}
	}
	if isValidation(s.callee(p, call)) {
		return "", false
	}
	return "the result of " + types.ExprString(call.Fun), true
}

// sentinelOrigin classifies a package-level error variable.
func (s *echoScanner) sentinelOrigin(p *echoPkg, name, label string) (string, bool) {
	if p != nil && p.tpkg != nil {
		if obj := p.tpkg.Scope().Lookup(name); obj != nil && s.fixedError(p, p.vals[obj], 0) {
			return "", false
		}
	}
	return "the package variable " + label, true
}

// fixedError reports whether e is a sentinel with a fixed text: errors.New
// of a literal, fmt.Errorf of a literal over sentinels, or a package
// variable holding one.
func (s *echoScanner) fixedError(p *echoPkg, e ast.Expr, depth int) bool {
	if depth > 4 || p == nil || p.tpkg == nil {
		return false
	}
	switch x := e.(type) {
	case *ast.Ident:
		if obj := p.tpkg.Scope().Lookup(x.Name); obj != nil {
			return s.fixedError(p, p.vals[obj], depth+1)
		}
		return false
	case *ast.SelectorExpr:
		if q := s.pkgOf(importedPath(p, x.X)); q != nil && q.tpkg != nil {
			if obj := q.tpkg.Scope().Lookup(x.Sel.Name); obj != nil {
				return s.fixedError(q, q.vals[obj], depth+1)
			}
		}
		return false
	}
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	fn, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if _, ok := call.Args[0].(*ast.BasicLit); !ok {
		return false
	}
	switch path := importedPath(p, fn.X); {
	case path == "errors" && fn.Sel.Name == "New":
		return len(call.Args) == 1
	case path == "fmt" && fn.Sel.Name == "Errorf":
		for _, arg := range call.Args[1:] {
			if !s.fixedError(p, arg, depth+1) {
				return false
			}
		}
		return true
	}
	return false
}

// fixedList reports whether e is a literal list of sentinels.
func (s *echoScanner) fixedList(p *echoPkg, e ast.Expr) bool {
	lit, ok := e.(*ast.CompositeLit)
	if !ok || len(lit.Elts) == 0 {
		return false
	}
	for _, elt := range lit.Elts {
		if !s.fixedError(p, elt, 0) {
			return false
		}
	}
	return true
}

// typedOrigin classifies a variable of a concrete error type, such as the
// target of errors.As: its text is the type's Error method.
func (s *echoScanner) typedOrigin(p *echoPkg, t ast.Expr) (string, bool) {
	if q, name := s.namedType(p, t); q != nil && isValidation(q.named[name+".Error"]) {
		return "", false
	}
	return "an error of type " + types.ExprString(t), true
}
