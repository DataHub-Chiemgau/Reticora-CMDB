package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant/rls"
	"github.com/jackc/pgx/v5"
)

const (
	testOrg     = "aaaa1111-0000-4000-8000-000000000001"
	testClientA = "aaaa1111-0000-4000-8000-0000000000a1"
	testClientB = "AAAA1111-0000-4000-8000-0000000000A2"
)

// TestTenantScopeValidate covers TEN-04 completeness and the fail-closed rule
// of E-08: a forgotten scope dimension is an error, never "all".
func TestTenantScopeValidate(t *testing.T) {
	valid := OrgWideScope(testOrg, "user-1")
	cases := []struct {
		name       string
		scope      TenantScope
		incomplete bool
		invalid    bool
	}{
		{name: "org-wide", scope: valid},
		{name: "system job without user", scope: OrgWideScope(testOrg, "")},
		{name: "restricted", scope: TenantScope{OrgID: testOrg, Clients: ScopeIDs(testClientA, testClientB), Sites: ScopeIDs(), Teams: AllScopes()}},
		{name: "zero value", scope: TenantScope{}, incomplete: true},
		{name: "missing org", scope: OrgWideScope("", "user-1"), incomplete: true},
		{name: "org not a uuid", scope: OrgWideScope("org-1", "user-1"), invalid: true},
		{name: "org in braces", scope: OrgWideScope("{"+testOrg+"}", ""), invalid: true},
		{name: "clients undecided", scope: TenantScope{OrgID: testOrg, Sites: AllScopes(), Teams: AllScopes()}, incomplete: true},
		{name: "sites undecided", scope: TenantScope{OrgID: testOrg, Clients: AllScopes(), Teams: AllScopes()}, incomplete: true},
		{name: "teams undecided", scope: TenantScope{OrgID: testOrg, Clients: AllScopes(), Sites: AllScopes()}, incomplete: true},
		{name: "client id not a uuid", scope: TenantScope{OrgID: testOrg, Clients: ScopeIDs("c1"), Sites: AllScopes(), Teams: AllScopes()}, invalid: true},
		{name: "list injection", scope: TenantScope{OrgID: testOrg, Clients: ScopeIDs(testClientA + "," + testClientA), Sites: AllScopes(), Teams: AllScopes()}, invalid: true},
		{name: "user id with quotes", scope: OrgWideScope(testOrg, `o'brien\'; --`)},
		{name: "user id with NUL byte", scope: OrgWideScope(testOrg, "user\x00-1"), invalid: true},
		{name: "user id not UTF-8", scope: OrgWideScope(testOrg, "user-\xff"), invalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.scope.Validate()
			switch {
			case tc.incomplete:
				if !errors.Is(err, ErrIncompleteScope) {
					t.Fatalf("Validate() = %v, want ErrIncompleteScope", err)
				}
			case tc.invalid:
				if err == nil || errors.Is(err, ErrIncompleteScope) {
					t.Fatalf("Validate() = %v, want an invalid-id error", err)
				}
			default:
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
			}
		})
	}
}

// TestScopeSetGUCValue pins the encoding the policies of migration 000033
// parse: "" = all, a comma list = restricted, the nil UUID = no access.
func TestScopeSetGUCValue(t *testing.T) {
	cases := []struct {
		name string
		set  ScopeSet
		want string
	}{
		{"all", AllScopes(), ""},
		{"empty means no access", ScopeIDs(), noAccessScope},
		{"one", ScopeIDs(testClientA), testClientA},
		{"several, lower-cased", ScopeIDs(testClientA, testClientB), testClientA + ",aaaa1111-0000-4000-8000-0000000000a2"},
	}
	for _, tc := range cases {
		if got := tc.set.gucValue(); got != tc.want {
			t.Errorf("%s: gucValue() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestScopeIDsCopiesInput(t *testing.T) {
	ids := []string{testClientA}
	set := ScopeIDs(ids...)
	ids[0] = testClientB
	if got := set.IDs(); len(got) != 1 || got[0] != testClientA {
		t.Fatalf("IDs() = %v, want the original id", got)
	}
	if AllScopes().IDs() != nil || !AllScopes().IsAll() || ScopeIDs().IsAll() {
		t.Fatal("AllScopes/ScopeIDs markers are inconsistent")
	}
}

func TestTenantScopeContextRoundTrip(t *testing.T) {
	if _, ok := TenantScopeFromContext(context.Background()); ok {
		t.Fatal("empty context must not carry a scope")
	}
	want := OrgWideScope(testOrg, "user-1")
	got, ok := TenantScopeFromContext(ContextWithTenantScope(context.Background(), &want))
	if !ok || got.OrgID != want.OrgID || got.UserID != want.UserID || !got.Clients.IsAll() {
		t.Fatalf("TenantScopeFromContext() = %+v, %v", got, ok)
	}
}

// TestWithTenantRejectsIncompleteScopeBeforeConnecting: the scope is
// validated first, so a nil pool is never touched and fn never runs.
func TestWithTenantRejectsIncompleteScopeBeforeConnecting(t *testing.T) {
	if err := WithTenant(context.Background(), nil, nil, nil); !errors.Is(err, ErrIncompleteScope) {
		t.Fatalf("WithTenant(nil scope) = %v, want ErrIncompleteScope", err)
	}
	err := WithTenant(context.Background(), nil, &TenantScope{OrgID: testOrg}, nil)
	if !errors.Is(err, ErrIncompleteScope) {
		t.Fatalf("WithTenant() = %v, want ErrIncompleteScope", err)
	}
}

// TestGUCNamesMatchCatalog keeps the names in line with the RLS catalog test.
func TestGUCNamesMatchCatalog(t *testing.T) {
	pairs := [][2]string{
		{OrgGUC, rls.OrgGUC},
		{ClientScopeGUC, rls.ClientScopeGUC},
		{SiteScopeGUC, rls.SiteScopeGUC},
		{TeamScopeGUC, rls.TeamScopeGUC},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("GUC %q differs from the catalog test's %q", p[0], p[1])
		}
	}
}

// unquoteLiteral reads the escape string constant at the start of s the way
// PostgreSQL does, and returns its value and the rest of s after the closing
// quote. The only backslash escape it accepts is a doubled backslash: any
// other one, a backslash quote in particular, depends on server settings.
func unquoteLiteral(t *testing.T, s string) (value, rest string) {
	t.Helper()
	if !strings.HasPrefix(s, "E'") {
		t.Fatalf("no escape string constant: %q", s)
	}
	var b strings.Builder
	for i := 2; i < len(s); i++ {
		switch {
		case s[i] == '\\':
			if i+1 == len(s) || s[i+1] != '\\' {
				t.Fatalf("backslash escape other than \\\\ in %q", s)
			}
			i++
			b.WriteByte('\\')
		case s[i] == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
			b.WriteByte('\'')
		case s[i] == '\'':
			return b.String(), s[i+1:]
		default:
			b.WriteByte(s[i])
		}
	}
	t.Fatalf("unterminated literal: %q", s)
	return "", ""
}

// TestBeginTenantSQLQuotesValues covers the one round trip of WithTenant
// (NFR-03): BEGIN and the five GUCs in one statement. A hostile user id stays
// inside its literal, whatever quotes, backslashes or statement separators
// it carries; a backslash quote is never produced (backslash_quote).
func TestBeginTenantSQLQuotesValues(t *testing.T) {
	for _, user := range []string{"", "user-1", `'`, `\`, `;`, `--`, `\'`, "line\nbreak", `x'); SELECT set_config('app.org_id', '', true); --`} {
		scope := OrgWideScope(testOrg, user)
		scope.Clients = ScopeIDs(testClientA, testClientB)
		scope.Sites = ScopeIDs()
		if err := scope.Validate(); err != nil {
			t.Fatalf("%q: Validate() = %v", user, err)
		}
		sql := beginTenantSQL(&scope)
		rest, ok := strings.CutPrefix(sql, "BEGIN; SELECT ")
		if !ok {
			t.Fatalf("%q: %s does not open the transaction first", user, sql)
		}
		got := map[string]string{}
		for _, name := range []string{OrgGUC, UserGUC, ClientScopeGUC, SiteScopeGUC, TeamScopeGUC} {
			rest, ok = strings.CutPrefix(rest, "set_config('"+name+"', ")
			if !ok {
				t.Fatalf("%q: set_config of %s expected at %q", user, name, rest)
			}
			got[name], rest = unquoteLiteral(t, rest)
			if rest, ok = strings.CutPrefix(rest, ", true)"); !ok {
				t.Fatalf("%q: %s is not set transaction-locally: %q", user, name, rest)
			}
			rest = strings.TrimPrefix(rest, ", ")
		}
		if rest != "" {
			t.Errorf("%q: trailing SQL after the GUCs: %q", user, rest)
		}
		want := map[string]string{
			OrgGUC: testOrg, UserGUC: user, ClientScopeGUC: scope.Clients.gucValue(),
			SiteScopeGUC: noAccessScope, TeamScopeGUC: "",
		}
		for name, w := range want {
			if got[name] != w {
				t.Errorf("%q: %s = %q, want %q", user, name, got[name], w)
			}
		}
	}
}

// TestTenantScopeSameAs: a transaction is joined only by an identical RLS
// context.
func TestTenantScopeSameAs(t *testing.T) {
	base := OrgWideScope(testOrg, "user-1")
	restricted := base
	restricted.Clients = ScopeIDs(testClientA)
	upper := OrgWideScope(strings.ToUpper(testOrg), "user-1")
	for _, tc := range []struct {
		name  string
		other TenantScope
		want  bool
	}{
		{"identical", OrgWideScope(testOrg, "user-1"), true},
		{"org id in upper case", upper, true},
		{"other user", OrgWideScope(testOrg, "user-2"), false},
		{"no user", OrgWideScope(testOrg, ""), false},
		{"restricted clients", restricted, false},
		{"other org", OrgWideScope("aaaa1111-0000-4000-8000-000000000002", "user-1"), false},
	} {
		if got := base.sameAs(&tc.other); got != tc.want {
			t.Errorf("%s: sameAs = %v, want %v", tc.name, got, tc.want)
		}
	}
	a, b := restricted, restricted
	b.Clients = ScopeIDs(strings.ToUpper(testClientA))
	if !a.sameAs(&b) {
		t.Error("client ids differing in case only are the same scope")
	}
}

// foreignTx is a transaction WithTenant did not open.
type foreignTx struct{ pgx.Tx }

// TestContextTxFailsClosed: a context transaction is joined only when
// WithTenant opened it, it is still open and the scope is the same; nothing
// touches the (nil) pool on a refusal.
func TestContextTxFailsClosed(t *testing.T) {
	scope := OrgWideScope(testOrg, "user-1")
	ctx := ContextWithTenantScope(context.Background(), &scope)
	refuse := func(context.Context, pgx.Tx) error {
		t.Fatal("fn must not run")
		return nil
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"foreign transaction", ContextWithTx(ctx, foreignTx{}), ErrTxNotJoinable},
		{"typed nil", ContextWithTx(ctx, (*tenantTx)(nil)), ErrTxNotJoinable},
		{"committed", ContextWithTx(ctx, txInState(scope, txCommitted)), ErrTxDone},
		{"rolled back", ContextWithTx(ctx, txInState(scope, txRolledBack)), ErrTxDone},
		{"other scope", ContextWithTx(ctx, txInState(OrgWideScope(testOrg, "user-2"), txOpen)), ErrTxScopeMismatch},
	} {
		err := WithTenant(tc.ctx, nil, &scope, refuse)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: WithTenant() = %v, want %v", tc.name, err, tc.want)
		}
	}

	// ContextWithTx sets the scope of the transaction; a scope narrowed
	// afterwards no longer joins.
	open := txInState(scope, txOpen)
	joined := ContextWithTx(context.Background(), open)
	if got, ok := TenantScopeFromContext(joined); !ok || !got.sameAs(&scope) {
		t.Fatalf("scope of the joined context: %+v, %v", got, ok)
	}
	ran := false
	if err := WithRequestTenant(joined, nil, testOrg, func(_ context.Context, tx pgx.Tx) error {
		ran = tx == pgx.Tx(open)
		return nil
	}); err != nil || !ran {
		t.Fatalf("join: %v, ran in the context transaction %v", err, ran)
	}
	narrowed := scope
	narrowed.Clients = ScopeIDs(testClientA)
	if err := WithRequestTenant(ContextWithTenantScope(joined, &narrowed), nil, testOrg, refuse); !errors.Is(err, ErrTxScopeMismatch) {
		t.Errorf("narrowed scope: %v, want ErrTxScopeMismatch", err)
	}
	if !InTx(joined) || InTx(WithoutTx(joined)) || InTx(ctx) {
		t.Error("InTx does not follow ContextWithTx/WithoutTx")
	}
	if got, ok := TenantScopeFromContext(WithoutTx(joined)); !ok || !got.sameAs(&scope) {
		t.Error("WithoutTx dropped the tenant scope")
	}

	// A context that already carries another scope keeps it: the wider
	// scope of the transaction never replaces it, its joins fail, and after
	// WithoutTx it is still the narrow scope.
	narrowCtx := ContextWithTx(ContextWithTenantScope(context.Background(), &narrowed), open)
	if got, ok := TenantScopeFromContext(narrowCtx); !ok || !got.sameAs(&narrowed) {
		t.Errorf("scope of a narrowed context after ContextWithTx: %+v, want the narrowed scope", got)
	}
	if err := WithRequestTenant(narrowCtx, nil, testOrg, refuse); !errors.Is(err, ErrTxScopeMismatch) {
		t.Errorf("narrowed context joining a wider transaction: %v, want ErrTxScopeMismatch", err)
	}
	if got, ok := TenantScopeFromContext(WithoutTx(narrowCtx)); !ok || !got.sameAs(&narrowed) {
		t.Errorf("scope after WithoutTx: %+v, want the narrowed scope", got)
	}
	sameCtx := ContextWithTx(ContextWithTenantScope(context.Background(), &scope), open)
	if err := WithRequestTenant(sameCtx, nil, testOrg, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Errorf("context with the transaction's scope: %v, want a join", err)
	}
}

func txInState(scope TenantScope, state int32) *tenantTx {
	t := &tenantTx{scope: scope}
	t.state.Store(state)
	return t
}

// TestAfterCommitWithoutTransaction: outside a transaction the callback runs
// at once and a savepoint is just fn.
func TestAfterCommitWithoutTransaction(t *testing.T) {
	ran := false
	AfterCommit(context.Background(), func(context.Context) { ran = true })
	if !ran {
		t.Error("AfterCommit without a transaction did not run at once")
	}
	errFn := errors.New("fn")
	if err := Savepoint(context.Background(), func(context.Context) error { return errFn }); !errors.Is(err, errFn) {
		t.Errorf("Savepoint() = %v", err)
	}
	if TxFailed(context.Background()) {
		t.Error("TxFailed without a transaction")
	}
}

// TestAfterCommitQueuesOnOpenTransaction: callbacks wait for the commit, run
// in order with a detached context, a panic does not stop the others, and a
// committed transaction runs late callbacks at once.
func TestAfterCommitQueuesOnOpenTransaction(t *testing.T) {
	tx := txInState(OrgWideScope(testOrg, ""), txOpen)
	ctx := ContextWithTx(context.Background(), tx)
	var order []string
	AfterCommit(ctx, func(ctx context.Context) {
		if InTx(ctx) {
			t.Error("callback context still carries the transaction")
		}
		order = append(order, "first")
	})
	AfterCommit(ctx, func(context.Context) { panic("broken callback") })
	AfterCommit(ctx, func(context.Context) { order = append(order, "third") })
	if len(order) != 0 {
		t.Fatal("callbacks ran before the commit")
	}
	tx.state.Store(txCommitted)
	tx.runAfterCommit(WithoutTx(ctx))
	if strings.Join(order, ",") != "first,third" {
		t.Fatalf("callbacks after the commit: %v", order)
	}
	AfterCommit(ctx, func(context.Context) { order = append(order, "late") })
	if order[len(order)-1] != "late" {
		t.Error("a callback after the commit did not run at once")
	}

	rolledBack := txInState(OrgWideScope(testOrg, ""), txRolledBack)
	AfterCommit(ContextWithTx(context.Background(), rolledBack), func(context.Context) { t.Error("callback of a rolled back transaction ran") })
}
