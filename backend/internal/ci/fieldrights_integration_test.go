package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// Field permissions of the CI REST API (RBA-07, E-43, WP-265) against
// PostgreSQL through the production router (server.NewRouter with the
// field grants of WP-264): projection of every read channel, the write
// check with 403 and fields before 422 and 412, overrides, list filters,
// type change and merge, object grants, ETag independence, bulk status,
// API keys and the byte-identical answer of an organization without
// grants. It runs only against PostgreSQL (TEST_DATABASE_URL).

// phase1Resolver resolves every principal to the policy without field
// permissions (an organization without grants).
type phase1Resolver struct{}

func (phase1Resolver) Resolve(_ context.Context, p identity.Principal) (*permission.FieldPolicy, error) { //nolint:gocritic // interface of permission.PolicyResolver
	return permission.UnrestrictedPolicy(p.Permissions), nil
}

// fieldPolicySource is the policy source server.NewRouter puts behind the
// route permission (permission.FieldPolicyMiddleware): the tests that mount
// the CI handler on their own router need it, since a principal without
// policy source is answered 500 (fail-closed, WP-265).
func fieldPolicySource() func(http.Handler) http.Handler {
	return permission.FieldPolicyMiddleware(phase1Resolver{})
}

// frResponse is the answer of one request.
type frResponse struct {
	code   int
	body   []byte
	header http.Header
}

func (r *frResponse) object(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
	return out
}

// frEnv is the router of the test and its principals.
type frEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	router http.Handler
}

// newFREnv builds the production router; it is rebuilt after a change of
// the grants (the snapshot is cached per resolver).
func newFREnv(t *testing.T, f *scopetest.Fixture) *frEnv {
	t.Helper()
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp-265", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OpenAPIValidation},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &frEnv{t: t, f: f, router: middleware.RequestID(middleware.TenantMiddleware(mux))}
}

// userPrincipal is a session principal of the user with the permissions.
func userPrincipal(org, userID string, perms []identity.Permission) identity.Principal {
	scope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	return identity.Principal{Subject: userID, OrganizationID: org, Type: identity.PrincipalTypeUser, Scope: scope,
		Permissions: perms, Credential: "wp-265"}
}

func (e *frEnv) do(p *identity.Principal, method, path string, body any, headers ...string) *frResponse {
	e.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), *p), p.Permissions)
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req.WithContext(ctx))
	return &frResponse{code: w.Code, body: w.Body.Bytes(), header: w.Header()}
}

// exec runs seed statements through the maintenance pool.
func frExec(t *testing.T, f *scopetest.Fixture, sql string, args ...any) {
	t.Helper()
	if _, err := f.Admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// frRole creates a role of the organization with the permissions.
func frRole(t *testing.T, f *scopetest.Fixture, org, name string, perms ...string) string {
	t.Helper()
	id := f.ID()
	raw, _ := json.Marshal(perms)
	frExec(t, f, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, $3, $4::jsonb)`, id, org, name, string(raw))
	return id
}

func frPerms(keys ...string) []identity.Permission {
	out := make([]identity.Permission, len(keys))
	for i, k := range keys {
		out[i] = identity.Permission(k)
	}
	return out
}

// wantForbidden checks a 403 of the field permissions with the fields.
func wantForbidden(t *testing.T, what string, r *frResponse, fields ...string) {
	t.Helper()
	if r.code != http.StatusForbidden {
		t.Fatalf("%s: %d %s, want 403", what, r.code, r.body)
	}
	body := r.object(t)
	if body["type"] != api.ProblemTypeForbidden {
		t.Fatalf("%s: type %v, want %s", what, body["type"], api.ProblemTypeForbidden)
	}
	var got []string
	for _, v := range body["fields"].([]any) {
		got = append(got, v.(string))
	}
	for _, field := range fields {
		if !slices.Contains(got, field) {
			t.Fatalf("%s: fields %v miss %s", what, got, field)
		}
	}
	if _, ok := body["total"]; ok {
		t.Fatalf("%s: a 403 carries no total", what)
	}
}

// hidden checks that the body does not contain any of the secret values.
func hidden(t *testing.T, what string, body []byte, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if bytes.Contains(body, []byte(s)) {
			t.Fatalf("%s leaks %q: %s", what, s, body)
		}
	}
}

// Values the reader may not see.
const (
	frHostname   = "fr-host-secret.example"
	frCostCenter = "cc-4711-secret"
	frSalary     = "salary-secret"
	frInstance   = "instance-secret"
	frSupplier   = "supplier-secret"
)

func TestFieldRightsRESTAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "65")
	bg := context.Background()
	var serverType, otherType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key <> 'server' AND organization_id IS NULL AND is_active
		ORDER BY key LIMIT 1`).Scan(&otherType); err != nil {
		t.Fatal(err)
	}

	// The reader: CIs without hostname, cost_center and the instance
	// attribute secret_inst, serial_number read-only, assets without
	// supplier. salary is sensitive without any grant.
	readerPerms := []string{"ci:read", "ci:write", "ci:delete", "override:write", "asset:read", "ci_instance_attribute:manage"}
	roleR := frRole(t, f, f.OrgA, "wp265-reader", readerPerms...)
	userR := f.AppUser(t, f.OrgA, "wp265-reader")
	frExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, userR, roleR)
	for _, g := range []struct{ kind, key, level string }{
		{"ci", "core.hostname", "none"}, {"ci", "attributes.cost_center", "none"}, {"ci", "_instance.secret_inst", "none"},
		{"ci", "core.serial_number", "read"}, {"asset", "core.supplier", "none"},
	} {
		frExec(t, f, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, $3, $4, $5)`,
			f.OrgA, roleR, g.kind, g.key, g.level)
	}
	frExec(t, f, `INSERT INTO field_sensitivity (organization_id, object_kind, field_key) VALUES ($1, 'ci', 'attributes.salary')`, f.OrgA)

	userA := f.AppUser(t, f.OrgA, "wp265-admin")
	frExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, userA)

	reader := userPrincipal(f.OrgA, userR, frPerms(readerPerms...))
	admin := userPrincipal(f.OrgA, userA, identity.AllPermissions())

	ci1 := f.CI(t, f.OrgA, f.Client1, "wp265-ci-1")
	frExec(t, f, `UPDATE ci SET hostname = $2, serial_number = 'SN-1', model = 'M1',
		attributes = jsonb_build_object('cost_center', $3::text, 'owner', 'ops', 'salary', $4::text,
			'_instance', jsonb_build_object('secret_inst', $5::text)) WHERE id = $1`,
		ci1, frHostname, frCostCenter, frSalary, frInstance)
	frExec(t, f, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type) VALUES ($1, $2, 'secret_inst', 'string')`,
		f.OrgA, ci1)
	asset1 := f.Asset(t, f.OrgA, f.Client1, "WP265-A1")
	frExec(t, f, `UPDATE asset SET supplier = $2, invoice_number = 'INV-265' WHERE id = $1`, asset1, frSupplier)
	frExec(t, f, `UPDATE ci SET asset_id = $2 WHERE id = $1`, ci1, asset1)
	ci2 := f.CI(t, f.OrgA, f.Client1, "wp265-ci-2")
	ci3 := f.CI(t, f.OrgA, f.Client2, "wp265-ci-3")
	frExec(t, f, `UPDATE ci SET model = 'M3' WHERE id = $1`, ci3)
	ciB := f.CI(t, f.OrgB, "", "wp265-ci-b")

	e := newFREnv(t, f)
	secrets := []string{frHostname, frCostCenter, frSalary, frInstance, frSupplier}

	t.Run("detail, include and ETag", func(t *testing.T) {
		r := e.do(&reader, http.MethodGet, "/api/v1/cis/"+ci1, nil)
		if r.code != http.StatusOK {
			t.Fatalf("get: %d %s", r.code, r.body)
		}
		hidden(t, "GET as reader", r.body, secrets...)
		body := r.object(t)
		if body["serial_number"] != "SN-1" || body["model"] != "M1" || body["name"] != "wp265-ci-1" {
			t.Fatalf("readable fields missing: %v", body)
		}
		attrs := body["attributes"].(map[string]any)
		if attrs["owner"] != "ops" || len(attrs) != 1 {
			t.Fatalf("attributes = %v, want only owner", attrs)
		}
		asset := body["asset"].(map[string]any)
		if asset["invoice_number"] != "INV-265" || asset["supplier"] != nil {
			t.Fatalf("asset = %v", asset)
		}
		a := e.do(&admin, http.MethodGet, "/api/v1/cis/"+ci1, nil)
		if a.code != http.StatusOK {
			t.Fatalf("get as admin: %d %s", a.code, a.body)
		}
		hidden(t, "GET as admin (sensitive)", a.body, frSalary)
		adminBody := a.object(t)
		if adminBody["hostname"] != frHostname || adminBody["attributes"].(map[string]any)["secret_inst"] != frInstance {
			t.Fatalf("admin must read hostname and the instance value: %v", adminBody)
		}
		if r.header.Get("ETag") == "" || r.header.Get("ETag") != a.header.Get("ETag") {
			t.Fatalf("ETag reader %q, admin %q", r.header.Get("ETag"), a.header.Get("ETag"))
		}
		inc := e.do(&reader, http.MethodGet, "/api/v1/cis/"+ci1+"?include=asset,references", nil)
		if inc.code != http.StatusOK {
			t.Fatalf("include: %d %s", inc.code, inc.body)
		}
		hidden(t, "include=asset", inc.body, secrets...)
		if !bytes.Contains(inc.body, []byte("INV-265")) {
			t.Fatalf("include=asset lacks the readable facts: %s", inc.body)
		}
	})

	t.Run("list, fields and filters", func(t *testing.T) {
		r := e.do(&reader, http.MethodGet, "/api/v1/cis?client_id="+f.Client1, nil)
		if r.code != http.StatusOK || !bytes.Contains(r.body, []byte(ci1)) {
			t.Fatalf("list: %d %s", r.code, r.body)
		}
		hidden(t, "list", r.body, secrets...)
		sel := e.do(&reader, http.MethodGet, "/api/v1/cis?client_id="+f.Client1+"&fields=hostname,model,attributes", nil)
		if sel.code != http.StatusOK || !bytes.Contains(sel.body, []byte(`"model":"M1"`)) {
			t.Fatalf("fields=: %d %s", sel.code, sel.body)
		}
		hidden(t, "fields=", sel.body, secrets...)
		wantForbidden(t, "filter on a hidden attribute",
			e.do(&reader, http.MethodGet, "/api/v1/cis?"+url.Values{"filter[attributes.cost_center][exact]": {frCostCenter}}.Encode(), nil),
			"attributes.cost_center")
		wantForbidden(t, "filter on hostname",
			e.do(&reader, http.MethodGet, "/api/v1/cis?"+url.Values{"filter[hostname][exact]": {frHostname}}.Encode(), nil), "hostname")
		if ok := e.do(&reader, http.MethodGet, "/api/v1/cis?"+url.Values{"filter[model][exact]": {"M1"}}.Encode(), nil); ok.code != http.StatusOK {
			t.Fatalf("filter on model: %d %s", ok.code, ok.body)
		}
		if ok := e.do(&admin, http.MethodGet, "/api/v1/cis?"+url.Values{"filter[hostname][exact]": {frHostname}}.Encode(), nil); ok.code != http.StatusOK {
			t.Fatalf("admin filter on hostname: %d %s", ok.code, ok.body)
		}
	})

	t.Run("provenance, fields, instance attributes", func(t *testing.T) {
		for _, path := range []string{"/provenance", "/fields", "/instance-attributes"} {
			r := e.do(&reader, http.MethodGet, "/api/v1/cis/"+ci1+path, nil)
			if r.code != http.StatusOK {
				t.Fatalf("%s: %d %s", path, r.code, r.body)
			}
			hidden(t, path, r.body, secrets...)
		}
		prov := e.do(&reader, http.MethodGet, "/api/v1/cis/"+ci1+"/provenance", nil)
		if !bytes.Contains(prov.body, []byte(`"field":"model"`)) || bytes.Contains(prov.body, []byte(`"field":"hostname"`)) {
			t.Fatalf("provenance: %s", prov.body)
		}
		inst := e.do(&admin, http.MethodGet, "/api/v1/cis/"+ci1+"/instance-attributes", nil)
		if !bytes.Contains(inst.body, []byte(frInstance)) {
			t.Fatalf("admin instance attributes: %s", inst.body)
		}
	})

	t.Run("write check", func(t *testing.T) {
		patch := func(body map[string]any, headers ...string) *frResponse {
			return e.do(&reader, http.MethodPatch, "/api/v1/cis/"+ci1, body, headers...)
		}
		wantForbidden(t, "PATCH hidden field with its value", patch(map[string]any{"hostname": frHostname}), "hostname")
		wantForbidden(t, "PATCH hidden field", patch(map[string]any{"hostname": "other"}), "hostname")
		wantForbidden(t, "PATCH read-only field", patch(map[string]any{"serial_number": "SN-2"}), "serial_number")
		wantForbidden(t, "PATCH hidden attribute", patch(map[string]any{"attributes": map[string]any{"cost_center": "x"}}),
			"attributes.cost_center")
		wantForbidden(t, "PATCH hidden instance attribute", patch(map[string]any{"attributes": map[string]any{"secret_inst": nil}}),
			"attributes.secret_inst")
		wantForbidden(t, "403 before 412", patch(map[string]any{"hostname": "x"}, "If-Match", `"999"`), "hostname")
		wantForbidden(t, "403 before 422", patch(map[string]any{"hostname": "x", "management_ip": "not-an-ip"}), "hostname")
		if r := patch(map[string]any{"serial_number": "SN-1", "model": "M1b"}); r.code != http.StatusOK {
			t.Fatalf("form PATCH with the unchanged read-only serial_number: %d %s", r.code, r.body)
		} else {
			hidden(t, "PATCH answer", r.body, secrets...)
		}
		wantForbidden(t, "PUT hidden instance attribute",
			e.do(&reader, http.MethodPut, "/api/v1/cis/"+ci1+"/instance-attributes/secret_inst", map[string]any{"value": "y"}),
			"attributes.secret_inst")
		wantForbidden(t, "POST with a hidden field", e.do(&reader, http.MethodPost, "/api/v1/cis", map[string]any{
			"ci_type_id": serverType, "client_id": f.Client1, "name": "wp265-new", "hostname": "h"}), "hostname")
		created := e.do(&reader, http.MethodPost, "/api/v1/cis", map[string]any{
			"ci_type_id": serverType, "client_id": f.Client1, "name": "wp265-new", "model": "MX"})
		if created.code != http.StatusCreated {
			t.Fatalf("POST: %d %s", created.code, created.body)
		}
		wantForbidden(t, "override PUT", e.do(&reader, http.MethodPut, "/api/v1/cis/"+ci1+"/fields/hostname/override",
			map[string]any{"value": "x", "reason": "test"}), "hostname")
		wantForbidden(t, "override DELETE", e.do(&reader, http.MethodDelete, "/api/v1/cis/"+ci1+"/fields/hostname/override", nil), "hostname")
		if r := e.do(&reader, http.MethodPut, "/api/v1/cis/"+ci1+"/fields/model/override",
			map[string]any{"value": "M-ovr", "reason": "test"}); r.code != http.StatusOK {
			t.Fatalf("override of model: %d %s", r.code, r.body)
		}
		if r := e.do(&admin, http.MethodPut, "/api/v1/cis/"+ci1+"/fields/hostname/override",
			map[string]any{"value": frHostname, "reason": "test"}); r.code != http.StatusOK {
			t.Fatalf("admin override of hostname: %d %s", r.code, r.body)
		}
		fields := e.do(&reader, http.MethodGet, "/api/v1/cis/"+ci1+"/fields", nil)
		hidden(t, "fields after the override", fields.body, frHostname)
		if !bytes.Contains(e.do(&admin, http.MethodGet, "/api/v1/cis/"+ci1+"/fields", nil).body, []byte(frHostname)) {
			t.Fatalf("admin must see the hostname override")
		}
	})

	t.Run("changes and ETag of hidden changes", func(t *testing.T) {
		before := e.do(&reader, http.MethodGet, "/api/v1/cis/"+ci1, nil).header.Get("ETag")
		if r := e.do(&admin, http.MethodPatch, "/api/v1/cis/"+ci1, map[string]any{"hostname": frHostname + ".2",
			"attributes": map[string]any{"cost_center": frCostCenter + "-2", "owner": "ops2"}}); r.code != http.StatusOK {
			t.Fatalf("admin PATCH: %d %s", r.code, r.body)
		}
		after := e.do(&reader, http.MethodGet, "/api/v1/cis/"+ci1, nil).header.Get("ETag")
		adminTag := e.do(&admin, http.MethodGet, "/api/v1/cis/"+ci1, nil).header.Get("ETag")
		if after == before || after != adminTag {
			t.Fatalf("ETag before %s, after %s, admin %s", before, after, adminTag)
		}
		changes := e.do(&reader, http.MethodGet, "/api/v1/cis/"+ci1+"/changes?limit=100", nil)
		if changes.code != http.StatusOK {
			t.Fatalf("changes: %d %s", changes.code, changes.body)
		}
		hidden(t, "changes", changes.body, secrets...)
		if !bytes.Contains(changes.body, []byte("ops2")) {
			t.Fatalf("changes lack the readable attribute: %s", changes.body)
		}
		if !bytes.Contains(e.do(&admin, http.MethodGet, "/api/v1/cis/"+ci1+"/changes?limit=100", nil).body, []byte(frHostname+".2")) {
			t.Fatalf("admin changes lack the hostname")
		}
		created := e.do(&admin, http.MethodPost, "/api/v1/cis", map[string]any{
			"ci_type_id": serverType, "client_id": f.Client1, "name": "wp265-created", "hostname": frHostname})
		if created.code != http.StatusCreated {
			t.Fatalf("admin POST: %d %s", created.code, created.body)
		}
		// A sensitive field without grant is not writable, not even by the
		// administrator.
		wantForbidden(t, "admin POST of a sensitive field", e.do(&admin, http.MethodPost, "/api/v1/cis", map[string]any{
			"ci_type_id": serverType, "client_id": f.Client1, "name": "wp265-sensitive", "attributes": map[string]any{"salary": frSalary}}),
			"attributes.salary")
		id := created.object(t)["id"].(string)
		frExec(t, f, `UPDATE ci SET attributes = attributes || jsonb_build_object('salary', $2::text) WHERE id = $1`, id, frSalary)
		history := e.do(&reader, http.MethodGet, "/api/v1/cis/"+id+"/changes", nil)
		if history.code != http.StatusOK || !bytes.Contains(history.body, []byte(`"change_type":"create"`)) {
			t.Fatalf("history of the created CI: %d %s", history.code, history.body)
		}
		hidden(t, "create entry", history.body, frHostname, frSalary)
		if r := e.do(&admin, http.MethodDelete, "/api/v1/cis/"+id, nil); r.code != http.StatusNoContent {
			t.Fatalf("delete: %d", r.code)
		}
		deleted := e.do(&reader, http.MethodGet, "/api/v1/cis/"+id+"/changes", nil)
		hidden(t, "history of a deleted CI", deleted.body, frHostname, frSalary)
		restored := e.do(&admin, http.MethodPost, "/api/v1/cis/"+id+"/restore", nil)
		if restored.code != http.StatusOK {
			t.Fatalf("restore: %d %s", restored.code, restored.body)
		}
		hidden(t, "restore answer (sensitive)", restored.body, frSalary)
	})

	t.Run("type change and merge", func(t *testing.T) {
		wantForbidden(t, "type change over hidden values",
			e.do(&reader, http.MethodPost, "/api/v1/cis/"+ci1+"/type", map[string]any{"ci_type_id": otherType}), "hostname")
		wantForbidden(t, "merge of a victim with hidden values",
			e.do(&reader, http.MethodPost, "/api/v1/cis/"+ci2+"/merge", map[string]any{"victim_id": ci1}), "hostname")
		r := e.do(&reader, http.MethodPost, "/api/v1/cis/"+ci2+"/type", map[string]any{"ci_type_id": otherType})
		if r.code != http.StatusOK {
			t.Fatalf("type change of a CI without hidden values: %d %s", r.code, r.body)
		}
	})

	t.Run("bulk set_status", func(t *testing.T) {
		r := e.do(&reader, http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": "set_status", "ids": []string{ci1, ci3},
			"status": "maintenance"})
		if r.code != http.StatusOK {
			t.Fatalf("bulk: %d %s", r.code, r.body)
		}
		var report ci.BulkResult
		if err := json.Unmarshal(r.body, &report); err != nil {
			t.Fatal(err)
		}
		for _, item := range report.Items {
			if item.Code != http.StatusOK {
				t.Fatalf("bulk item %s: %d %s", item.ID, item.Code, item.Error)
			}
		}
	})

	t.Run("filter of a role restricted in one client scope", func(t *testing.T) {
		roleC1 := frRole(t, f, f.OrgA, "wp265-client1", "ci:read")
		roleC2 := frRole(t, f, f.OrgA, "wp265-client2", "ci:read")
		userS := f.AppUser(t, f.OrgA, "wp265-scoped")
		frExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4), ($1, $2, $5, $6)`,
			f.OrgA, userS, roleC1, f.Client1, roleC2, f.Client2)
		frExec(t, f, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ci', 'attributes.owner', 'none')`,
			f.OrgA, roleC1)
		scoped := newFREnv(t, f)
		s := userPrincipal(f.OrgA, userS, frPerms("ci:read"))
		wantForbidden(t, "filter on owner", scoped.do(&s, http.MethodGet, "/api/v1/cis?"+url.Values{"filter[attributes.owner][exact]": {"ops2"}}.Encode(), nil),
			"attributes.owner")
		r := scoped.do(&s, http.MethodGet, "/api/v1/cis/"+ci1, nil)
		if r.code != http.StatusOK || bytes.Contains(r.body, []byte("ops2")) {
			t.Fatalf("owner in client 1 must be hidden: %d %s", r.code, r.body)
		}
	})

	t.Run("API keys", func(t *testing.T) {
		key := identity.Principal{Subject: userR, OrganizationID: f.OrgA, Type: identity.PrincipalTypeAPIKey,
			Permissions: frPerms(readerPerms...), Credential: "wp-265-key"}
		r := e.do(&key, http.MethodGet, "/api/v1/cis/"+ci1, nil)
		if r.code != http.StatusOK {
			t.Fatalf("API key of the reader: %d %s", r.code, r.body)
		}
		hidden(t, "API key of the reader", r.body, secrets...)
		account := f.ID()
		frExec(t, f, `INSERT INTO service_account (id, organization_id, name, created_by) VALUES ($1, $2, 'wp265-account', $3)`, account, f.OrgA, userA)
		frExec(t, f, `INSERT INTO service_account_role (organization_id, service_account_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, account, roleR)
		sa := identity.Principal{Subject: f.ID(), OrganizationID: f.OrgA, Type: identity.PrincipalTypeAPIKey, ServiceAccountID: account,
			Permissions: identity.AllPermissions(), Credential: "wp-265-sa"}
		r = newFREnv(t, f).do(&sa, http.MethodGet, "/api/v1/cis/"+ci1, nil)
		if r.code != http.StatusOK {
			t.Fatalf("API key of the service account: %d %s", r.code, r.body)
		}
		hidden(t, "API key of the service account", r.body, secrets...)
		if !bytes.Contains(r.body, []byte(`"model"`)) {
			t.Fatalf("the service account reads model: %s", r.body)
		}
	})

	t.Run("object grants", func(t *testing.T) {
		frExec(t, f, `INSERT INTO object_grant (organization_id, grantee_role_id, object_kind, object_id, level) VALUES
			($1, $2, 'ci', $3, 'none'), ($1, $2, 'ci', $4, 'read')`, f.OrgA, roleR, ci3, ci2)
		og := newFREnv(t, f)
		r := og.do(&reader, http.MethodGet, "/api/v1/cis/"+ci3, nil)
		if r.code != http.StatusOK {
			t.Fatalf("object grant none hides no object: %d %s", r.code, r.body)
		}
		body := r.object(t)
		if body["model"] != nil || body["name"] != "wp265-ci-3" {
			t.Fatalf("object grant none: %v", body)
		}
		wantForbidden(t, "write under object grant read", og.do(&reader, http.MethodPatch, "/api/v1/cis/"+ci2,
			map[string]any{"model": "x"}), "model")
		if r := og.do(&reader, http.MethodPatch, "/api/v1/cis/"+ci2, map[string]any{"status": "maintenance"}); r.code != http.StatusOK {
			t.Fatalf("structural write under object grant read: %d %s", r.code, r.body)
		}
		wantForbidden(t, "filter with an object grant none",
			og.do(&reader, http.MethodGet, "/api/v1/cis?"+url.Values{"filter[model][exact]": {"M3"}}.Encode(), nil), "model")
		list := og.do(&reader, http.MethodGet, "/api/v1/cis?client_id="+f.Client2, nil)
		if list.code != http.StatusOK || !bytes.Contains(list.body, []byte(ci3)) || bytes.Contains(list.body, []byte(`"M3"`)) {
			t.Fatalf("list under object grant none: %d %s", list.code, list.body)
		}
	})

	t.Run("other organization", func(t *testing.T) {
		if r := e.do(&reader, http.MethodGet, "/api/v1/cis/"+ciB, nil); r.code != http.StatusNotFound {
			t.Fatalf("CI of another organization: %d %s", r.code, r.body)
		}
		for _, field := range []string{"model", "hostname"} {
			if r := e.do(&reader, http.MethodPatch, "/api/v1/cis/"+ciB, map[string]any{field: "x"}); r.code != http.StatusNotFound {
				t.Fatalf("PATCH %s of a foreign CI: %d %s, want 404", field, r.code, r.body)
			}
		}
	})
}

// TestFieldRightsUnrestrictedIsByteIdentical: an organization without any
// field permission answers exactly as phase 1 (WP-265).
func TestFieldRightsUnrestrictedIsByteIdentical(t *testing.T) {
	f := scopetest.Seed(t, "65")
	id := f.CI(t, f.OrgB, "", "wp265-plain")
	frExec(t, f, `UPDATE ci SET hostname = 'plain.example', attributes = '{"owner": "ops"}' WHERE id = $1`, id)
	userB := f.AppUser(t, f.OrgB, "wp265-plain")
	frExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgB, userB)
	e := newFREnv(t, f)
	p := userPrincipal(f.OrgB, userB, identity.AllPermissions())
	got := e.do(&p, http.MethodGet, "/api/v1/cis/"+id, nil)
	if got.code != http.StatusOK {
		t.Fatalf("get: %d %s", got.code, got.body)
	}
	item, err := ci.NewPGRepository(f.App).GetByID(f.OrgCtx(f.OrgB), f.OrgB, id)
	if err != nil {
		t.Fatal(err)
	}
	want := httptest.NewRecorder()
	api.WriteJSON(want, http.StatusOK, item)
	if !bytes.Equal(got.body, want.Body.Bytes()) {
		t.Fatalf("answer without grants differs:\n got %s\nwant %s", got.body, want.Body.Bytes())
	}
	if !strings.Contains(string(got.body), "plain.example") {
		t.Fatalf("hostname missing: %s", got.body)
	}
}
