package database

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant/rls"
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
