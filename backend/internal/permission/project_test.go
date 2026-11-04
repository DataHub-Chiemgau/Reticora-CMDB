package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Unit tests of the central projection of WP-265 (RBA-07) on the memory
// grants of WP-264: projection per field kind, structural members, the
// unrestricted pass-through, CheckWrite, the DTO path mapping, the list
// filter check, PolicyFrom and BenchmarkProject.

const (
	tAssetType = "00000000-0000-4000-8000-0000000000d9"
	tAsset     = "00000000-0000-4000-8000-0000000000e9"
)

// sampleCI is a CI DTO as ci.Item encodes it.
func sampleCI() map[string]any {
	return map[string]any{
		"id": tObject, "organization_id": tOrg, "ci_type_id": tChildType, "client_id": tClient1,
		"name": "srv-01", "status": "active", "health": "ok", "version": 3, "schema_version": 1,
		"is_manual": true, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
		"tags": []string{}, "hostname": "srv-01.example", "serial_number": "SN-1", "model": "R650",
		"attributes": map[string]any{"cost_center": "4711", "owner": "ops", "secret": "s3cr3t"},
		"asset": map[string]any{"id": tAsset, "name": "Asset 1", "asset_tag": "A-1", "status": "in_use",
			"supplier": "ACME", "invoice_number": "INV-9"},
		"unknown_member": "x",
	}
}

// restrictedPolicy reads CIs without hostname, attributes.cost_center and
// the instance attribute secret; assets without supplier.
func restrictedPolicy(t *testing.T) *FieldPolicy {
	t.Helper()
	repo, _ := fixture(t)
	role := ptr(tRoleA)
	addField(t, repo, FieldGrant{RoleID: role, ObjectKind: KindCI, FieldKey: ptr("core.hostname"), Level: LevelNone})
	addField(t, repo, FieldGrant{RoleID: role, ObjectKind: KindCI, FieldKey: ptr("attributes.cost_center"), Level: LevelNone})
	addField(t, repo, FieldGrant{RoleID: role, ObjectKind: KindCI, FieldKey: ptr("_instance.secret"), Level: LevelNone})
	addField(t, repo, FieldGrant{RoleID: role, ObjectKind: KindCI, FieldKey: ptr("core.serial_number"), Level: LevelRead})
	addField(t, repo, FieldGrant{RoleID: role, ObjectKind: KindAsset, FieldKey: ptr("core.supplier"), Level: LevelNone})
	return policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:read", "ci:write", "asset:read")}, nil)
}

func decode(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// TestProjectFieldKinds covers the projection per field kind: typed
// column, attribute, instance attribute (WithInstanceKeys), asset facts
// (WithAsset); structural members stay, unknown members go.
func TestProjectFieldKinds(t *testing.T) {
	policy := restrictedPolicy(t)
	ref := ObjectRef{ID: tObject, ClientID: tClient1}
	got, err := Project(policy, KindCI, tChildType, ref, sampleCI(), WithInstanceKeys([]string{"secret"}),
		WithAsset(tAssetType, ObjectRef{ID: tAsset, ClientID: tClient1}))
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	for _, member := range []string{"id", "organization_id", "ci_type_id", "client_id", "name", "status", "health", "version",
		"schema_version", "is_manual", "created_at", "updated_at", "tags", "model", "serial_number", "attributes", "asset"} {
		if _, ok := got[member]; !ok {
			t.Errorf("member %s missing", member)
		}
	}
	for _, member := range []string{"hostname", "unknown_member"} {
		if _, ok := got[member]; ok {
			t.Errorf("member %s must be left out", member)
		}
	}
	attrs := decode(t, got["attributes"]).(map[string]any)
	if !reflect.DeepEqual(attrs, map[string]any{"owner": "ops"}) {
		t.Errorf("attributes = %v, want only owner", attrs)
	}
	asset := decode(t, got["asset"]).(map[string]any)
	if _, ok := asset["supplier"]; ok {
		t.Errorf("asset supplier must be left out: %v", asset)
	}
	for _, member := range []string{"id", "name", "asset_tag", "invoice_number"} {
		if _, ok := asset[member]; !ok {
			t.Errorf("asset member %s missing", member)
		}
	}

	// Without WithInstanceKeys secret is attributes.secret, which the
	// policy does not restrict.
	plain, err := Project(policy, KindCI, tChildType, ref, sampleCI())
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if attrs := decode(t, plain["attributes"]).(map[string]any); attrs["secret"] != "s3cr3t" {
		t.Errorf("attributes.secret without instance keys = %v", attrs)
	}
	// Without WithAsset the facts of an unknown asset are left out while
	// the asset policy is restricted.
	if _, ok := plain["asset"]; ok {
		t.Errorf("asset facts without WithAsset must be left out")
	}
}

// TestProjectUnrestrictedPassThrough: without a restriction the value is
// returned as it is, without a JSON round trip.
func TestProjectUnrestrictedPassThrough(t *testing.T) {
	policy := UnrestrictedPolicy(perms("ci:read"))
	in := sampleCI()
	out, err := ProjectValue(policy, KindCI, tChildType, ObjectRef{ID: tObject}, in)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if reflect.ValueOf(out).Pointer() != reflect.ValueOf(in).Pointer() {
		t.Fatalf("unrestricted projection must return the value itself")
	}
	if NeedsProjection(policy, KindCI) {
		t.Fatalf("NeedsProjection of the unrestricted policy")
	}
	// A field grant of another role of the organization does not restrict
	// this principal either.
	repo, _ := fixture(t)
	addField(t, repo, FieldGrant{RoleID: ptr(tRoleB), ObjectKind: KindCI, FieldKey: ptr("core.hostname"), Level: LevelNone})
	other := policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:read")}, nil)
	if NeedsProjection(other, KindCI) {
		t.Fatalf("a grant of another role restricts the principal")
	}
	// A nil policy reads nothing (fail-closed).
	got, err := Project(nil, KindCI, tChildType, ObjectRef{ID: tObject}, sampleCI())
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if _, ok := got["model"]; ok {
		t.Errorf("nil policy must hide model")
	}
	if _, ok := got["name"]; !ok {
		t.Errorf("nil policy must keep structural name")
	}
	if _, err := Project(policy, KindTicket, "", ObjectRef{}, in); !errors.Is(err, ErrUnsupportedKind) {
		t.Errorf("ticket projection: %v, want ErrUnsupportedKind", err)
	}
}

// TestCheckWrite covers the write check: a field that is not readable or
// not editable and changed is refused with its DTO path, structural fields
// pass, the error is *api.FieldForbiddenError.
func TestCheckWrite(t *testing.T) {
	policy := restrictedPolicy(t)
	ref := ObjectRef{ID: tObject, ClientID: tClient1}
	cases := []struct {
		name    string
		changed []string
		want    []string
	}{
		{name: "nothing changed", changed: nil},
		{name: "structural fields", changed: []string{"name", "status", "client_id"}},
		{name: "editable field", changed: []string{"model", "attributes.owner"}},
		{name: "not readable", changed: []string{"hostname"}, want: []string{"hostname"}},
		{name: "readable but not editable", changed: []string{"serial_number", "model"}, want: []string{"serial_number"}},
		{name: "attribute", changed: []string{"attributes.cost_center"}, want: []string{"attributes.cost_center"}},
		{name: "instance attribute", changed: []string{"attributes.secret"}, want: []string{"attributes.secret"}},
		{name: "path without mapping", changed: []string{"attributes."}, want: []string{"attributes."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckWrite(policy, KindCI, tChildType, ref, tc.changed, WithInstanceKeys([]string{"secret"}))
			if tc.want == nil {
				if err != nil {
					t.Fatalf("CheckWrite = %v, want nil", err)
				}
				return
			}
			var forbidden *api.FieldForbiddenError
			if !errors.As(err, &forbidden) {
				t.Fatalf("CheckWrite = %v, want *api.FieldForbiddenError", err)
			}
			if !slices.Equal(forbidden.Fields, tc.want) {
				t.Fatalf("fields = %v, want %v", forbidden.Fields, tc.want)
			}
		})
	}
	// The unrestricted policy refuses nothing, also without ci:write.
	if err := CheckWrite(UnrestrictedPolicy(nil), KindCI, tChildType, ref, []string{"hostname"}); err != nil {
		t.Fatalf("unrestricted CheckWrite = %v", err)
	}
	// An object grant read makes every projected write 403, structural
	// writes stay.
	repo, _ := fixture(t)
	addObject(t, repo, ObjectGrant{GranteeRoleID: ptr(tRoleA), ObjectKind: KindCI, ObjectID: tObject, Level: LevelRead})
	readOnly := policyFor(t, repo, []identity.Grant{roleGrant(tRoleA, "ci:read", "ci:write")}, nil)
	if err := CheckWrite(readOnly, KindCI, tChildType, ObjectRef{ID: tObject}, []string{"model"}); err == nil {
		t.Fatalf("object grant read must refuse a write of model")
	}
	if err := CheckWrite(readOnly, KindCI, tChildType, ObjectRef{ID: tObject}, []string{"status"}); err != nil {
		t.Fatalf("object grant read must keep structural writes: %v", err)
	}
	if !Readable(readOnly, KindCI, tChildType, ObjectRef{ID: tObject}, "model") {
		t.Fatalf("object grant read must keep model readable")
	}
}

// TestDTOPathMapping covers the mapping DTO path <-> field key.
func TestDTOPathMapping(t *testing.T) {
	cases := []struct {
		kind       ObjectKind
		path, key  string
		structural bool
		ok         bool
	}{
		{KindCI, "hostname", "core.hostname", false, true},
		{KindCI, "attributes.cost_center", "attributes.cost_center", false, true},
		{KindCI, "attributes.secret", "_instance.secret", false, true},
		{KindCI, "name", "", true, true},
		{KindCI, "status", "", true, true},
		{KindCI, "lifecycle_state", "", true, true},
		{KindCI, "templates", "", true, true},
		{KindCI, "asset", "", false, false},
		{KindAsset, "supplier", "core.supplier", false, true},
		{KindAsset, "name", "", true, true},
		{KindAsset, "unknown", "", false, false},
	}
	for _, tc := range cases {
		key, structural, ok := FieldKeyOf(tc.kind, tc.path, WithInstanceKeys([]string{"secret"}))
		if key != tc.key || structural != tc.structural || ok != tc.ok {
			t.Errorf("FieldKeyOf(%s, %s) = %q, %v, %v; want %q, %v, %v", tc.kind, tc.path, key, structural, ok, tc.key, tc.structural, tc.ok)
		}
		if tc.key != "" && tc.kind == KindCI {
			if back := DTOPath(tc.key); back != tc.path {
				t.Errorf("DTOPath(%s) = %s, want %s", tc.key, back, tc.path)
			}
		}
	}
	for name, want := range map[string]string{"hostname": "hostname", "name": "name", "cost_center": "attributes.cost_center",
		"client_id": "client_id"} {
		if got := CIFieldPath(name); got != want {
			t.Errorf("CIFieldPath(%s) = %s, want %s", name, got, want)
		}
	}
}

// TestCheckListFilter: a filter on a field the principal cannot read in
// one of its client scopes, for one of the queried types, is refused;
// structural filters pass; an object grant none refuses every projected
// filter.
func TestCheckListFilter(t *testing.T) {
	repo, _ := fixture(t)
	addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, FieldKey: ptr("attributes.cost_center"), Level: LevelNone})
	addField(t, repo, FieldGrant{RoleID: ptr(tRoleA), ObjectKind: KindCI, CITypeID: ptr(tOtherType), FieldKey: ptr("core.model"),
		Level: LevelNone})
	grants := []identity.Grant{
		{RoleID: tRoleA, Clients: []string{tClient1}, Permissions: perms("ci:read")},
		{RoleID: tRoleB, Clients: []string{tClient2}, Permissions: perms("ci:read")},
	}
	policy := policyFor(t, repo, grants, nil)
	// Level with an empty reference is the union of both roles: readable.
	if !policy.Level(KindCI, "", "attributes.cost_center", ObjectRef{}).Readable() {
		t.Fatalf("union level must be readable")
	}
	err := CheckListFilter(policy, KindCI, nil, []string{"attributes.cost_center", "status", "hostname"})
	var forbidden *api.FieldForbiddenError
	if !errors.As(err, &forbidden) || !slices.Equal(forbidden.Fields, []string{"attributes.cost_center"}) {
		t.Fatalf("CheckListFilter = %v, want 403 on attributes.cost_center", err)
	}
	if err := CheckListFilter(policy, KindCI, nil, []string{"status", "name", "tags"}); err != nil {
		t.Fatalf("structural filters: %v", err)
	}
	// model is restricted only in tOtherType: refused without types and
	// for that type, allowed for another.
	if err := CheckListFilter(policy, KindCI, nil, []string{"model"}); err == nil {
		t.Fatalf("model without types must be refused")
	}
	if err := CheckListFilter(policy, KindCI, []string{tOtherType}, []string{"model"}); err == nil {
		t.Fatalf("model of tOtherType must be refused")
	}
	if err := CheckListFilter(policy, KindCI, []string{tParentType}, []string{"model"}); err != nil {
		t.Fatalf("model of tParentType: %v", err)
	}
	// An object grant none of the principal refuses every projected filter.
	addObject(t, repo, ObjectGrant{GranteeRoleID: ptr(tRoleB), ObjectKind: KindCI, ObjectID: tObject, Level: LevelNone})
	closed := policyFor(t, repo, grants, nil)
	if err := CheckListFilter(closed, KindCI, []string{tParentType}, []string{"model"}); err == nil {
		t.Fatalf("object grant none must refuse model")
	}
	if err := CheckListFilter(closed, KindCI, nil, []string{"status"}); err != nil {
		t.Fatalf("object grant none must keep structural filters: %v", err)
	}
	if err := CheckListFilter(UnrestrictedPolicy(nil), KindCI, nil, []string{"hostname"}); err != nil {
		t.Fatalf("unrestricted: %v", err)
	}
}

// countingResolver counts the resolutions.
type countingResolver struct {
	calls  int
	policy *FieldPolicy
	err    error
}

func (c *countingResolver) Resolve(context.Context, identity.Principal) (*FieldPolicy, error) { //nolint:gocritic // interface of PolicyResolver
	c.calls++
	return c.policy, c.err
}

// TestPolicyFrom: once per request, unrestricted without principal, an
// error with principal but without source or on a failed resolution.
func TestPolicyFrom(t *testing.T) {
	principal := identity.Principal{Subject: tUser, OrganizationID: tOrg, Type: identity.PrincipalTypeUser, Permissions: perms("ci:read")}

	p, err := PolicyFrom(context.Background())
	if err != nil || p == nil || p.Restricted(KindCI) {
		t.Fatalf("without principal = %v, %v; want the unrestricted policy", p, err)
	}
	if _, err = PolicyFrom(identity.WithPrincipal(context.Background(), principal)); !errors.Is(err, ErrNoPolicySource) {
		t.Fatalf("principal without source = %v, want ErrNoPolicySource", err)
	}

	resolver := &countingResolver{policy: UnrestrictedPolicy(principal.Permissions)}
	handler := FieldPolicyMiddleware(resolver)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 200 {
			if _, perr := PolicyFrom(r.Context()); perr != nil {
				t.Errorf("PolicyFrom: %v", perr)
			}
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	handler.ServeHTTP(httptest.NewRecorder(), req.WithContext(identity.WithPrincipal(req.Context(), principal)))
	if resolver.calls != 1 {
		t.Fatalf("Resolve calls = %d, want 1", resolver.calls)
	}
	// A request that needs no policy does not resolve it.
	lazy := &countingResolver{policy: UnrestrictedPolicy(nil)}
	FieldPolicyMiddleware(lazy)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(httptest.NewRecorder(), req.WithContext(identity.WithPrincipal(req.Context(), principal)))
	if lazy.calls != 0 {
		t.Fatalf("Resolve calls without need = %d, want 0", lazy.calls)
	}

	failing := &countingResolver{err: errors.New("database down")}
	FieldPolicyMiddleware(failing)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, perr := PolicyFrom(r.Context()); perr == nil {
			t.Errorf("a failed resolution must be an error")
		}
	})).ServeHTTP(httptest.NewRecorder(), req.WithContext(identity.WithPrincipal(req.Context(), principal)))

	fixed := UnrestrictedPolicy(nil)
	if got, err := PolicyFrom(WithPolicy(identity.WithPrincipal(context.Background(), principal), fixed)); err != nil || got != fixed {
		t.Fatalf("WithPolicy = %v, %v", got, err)
	}
}

// BenchmarkProject projects 200 CI DTOs with a restricted policy.
func BenchmarkProject(b *testing.B) {
	t := &testing.T{}
	policy := restrictedPolicy(t)
	items := make([]map[string]any, 200)
	for i := range items {
		item := sampleCI()
		item["id"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		items[i] = item
	}
	ref := ObjectRef{ClientID: tClient1}
	opts := []ProjectOption{WithInstanceKeys([]string{"secret"}), WithAsset(tAssetType, ObjectRef{ID: tAsset})}
	b.ResetTimer()
	for range b.N {
		for i := range items {
			if _, err := Project(policy, KindCI, tChildType, ref, items[i], opts...); err != nil {
				b.Fatal(err)
			}
		}
	}
}
