package savedview_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Field permissions in the structured query (RBA-07, WP-729): POST
// /api/v1/search/query refuses every referenced field the caller may not
// read for one of the queried types with 403 and the fields, before any
// SQL runs and without a total; structural fields pass; the rows are
// projected per type and scope; without field permissions the answer is
// unchanged.

const frRoleID = "00000000-0000-4000-8000-00000000f7a1"

// frAccess holds the role assignments of the fixture user.
type frAccess struct{ grants []identity.Grant }

func (a frAccess) AccessGrants(context.Context, string, string) ([]identity.Grant, error) {
	return a.grants, nil
}

func (a frAccess) RoleGrants(context.Context, string, []string) ([]identity.Grant, error) {
	return nil, nil
}

func frPtr(s string) *string { return &s }

// frEnv is the query endpoint of a fixture with a server CI, a switch CI
// and an asset.
type frEnv struct {
	t        *testing.T
	f        *scopetest.Fixture
	mux      *chi.Mux
	serverID string
	switchID string
	server   string // CI type ids
	sw       string
	assetID  string
}

func newFREnv(t *testing.T) *frEnv {
	t.Helper()
	f := scopetest.Seed(t, "bc")
	ctx := context.Background()
	e := &frEnv{t: t, f: f}
	for key, dst := range map[string]*string{"server": &e.server, "switch": &e.sw} {
		if err := f.Admin.QueryRow(ctx, `SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL LIMIT 1`, key).Scan(dst); err != nil {
			t.Fatalf("ci_type %s: %v", key, err)
		}
	}
	e.serverID = f.CI(t, f.OrgA, f.Client1, "fr-server")
	e.switchID = f.CI(t, f.OrgA, f.Client1, "fr-switch")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`UPDATE ci SET hostname = 'fr-host', manufacturer = 'Dell', model = 'R650', serial_number = 'SN-SRV-729',
		attributes = '{"cost_center": "CC-4711", "location": "Berlin", "_instance": {"secret": "inst-secret", "rack": "R1"},
		"_observed": {"serial_number": {"value": "SN-SRV-729"}, "hostname": {"value": "fr-host"}}}' WHERE id = $1`, e.serverID)
	exec(`UPDATE ci SET ci_type_id = $2, hostname = 'fr-sw', manufacturer = 'Cisco', serial_number = 'SN-SW-729',
		attributes = '{"location": "Munich"}' WHERE id = $1`, e.switchID, e.sw)
	e.assetID = f.Asset(t, f.OrgA, f.Client1, "FR-A-729")
	exec(`UPDATE asset SET category = 'accessory', supplier = 'ACME-729', status = 'in_stock', warranty_end = CURRENT_DATE + 30,
		purchase_cost = 999, custom_fields = '{"secret": "asset-secret", "color": "blue", "tags": ["fr"]}' WHERE id = $1`, e.assetID)
	exec(`UPDATE ci SET asset_id = $2 WHERE id = $1`, e.serverID, e.assetID)

	e.mux = chi.NewMux()
	savedview.NewHandler(savedview.NewMemoryRepository()).WithQueryEngine(savedview.NewPGQueryEngine(f.App)).
		WithPermissions(grants{"ci:read": true, "asset:read": true}).WithEntitlements(features{"assets_basic": true}).WithInventory(true).RegisterRoutes(e.mux)
	return e
}

// restricted is the policy of the role: serial_number not readable for
// servers, attributes.cost_center sensitive, the instance attribute secret
// closed; for assets category, warranty_end, supplier, purchase_cost and
// the custom fields secret and tags closed.
func (e *frEnv) restricted() *permission.FieldPolicy {
	e.t.Helper()
	repo := permission.NewMemoryFieldGrants()
	bg := context.Background()
	for _, g := range []permission.FieldGrant{
		{ObjectKind: permission.KindCI, CITypeID: frPtr(e.server), FieldKey: frPtr("core.serial_number")},
		{ObjectKind: permission.KindCI, FieldKey: frPtr("_instance.secret")},
		{ObjectKind: permission.KindAsset, FieldKey: frPtr("core.category")},
		{ObjectKind: permission.KindAsset, FieldKey: frPtr("core.warranty_end")},
		{ObjectKind: permission.KindAsset, FieldKey: frPtr("core.supplier")},
		{ObjectKind: permission.KindAsset, FieldKey: frPtr("core.purchase_cost")},
		{ObjectKind: permission.KindAsset, FieldKey: frPtr("attributes.secret")},
		{ObjectKind: permission.KindAsset, FieldKey: frPtr("attributes.tags")},
	} {
		g.RoleID, g.Level = frPtr(frRoleID), permission.LevelNone
		if _, err := repo.CreateFieldGrant(bg, e.f.OrgA, &g); err != nil {
			e.t.Fatal(err)
		}
	}
	if _, err := repo.CreateSensitiveField(bg, e.f.OrgA, &permission.SensitiveField{ObjectKind: permission.KindCI,
		FieldKey: "attributes.cost_center"}); err != nil {
		e.t.Fatal(err)
	}
	return e.resolve(repo)
}

func (e *frEnv) resolve(repo permission.FieldGrantRepository) *permission.FieldPolicy {
	e.t.Helper()
	perms := []identity.Permission{identity.PermCIRead, identity.PermAssetRead, "search:read"}
	access := frAccess{grants: []identity.Grant{{RoleID: frRoleID, Permissions: perms}}}
	policy, err := permission.NewFieldResolver(access, nil, repo, nil).Resolve(context.Background(),
		identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser, Permissions: perms})
	if err != nil {
		e.t.Fatal(err)
	}
	return policy
}

// query runs a structured query with the policy and returns status,
// decoded body and raw body.
func (e *frEnv) query(policy *permission.FieldPolicy, body string) (int, map[string]any, string) {
	e.t.Helper()
	ctx := tenant.WithTenant(e.f.OrgCtx(e.f.OrgA), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser})
	ctx = permission.WithPolicy(ctx, policy)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/search/query", strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func fieldsOf(out map[string]any) []string {
	raw, _ := out["fields"].([]any)
	fields := make([]string, 0, len(raw))
	for _, f := range raw {
		s, _ := f.(string)
		fields = append(fields, s)
	}
	return fields
}

func TestStructuredQueryFieldRightsAgainstPostgres(t *testing.T) {
	e := newFREnv(t)
	policy := e.restricted()

	for _, c := range []struct{ name, body, field string }{
		{"typed AST field", `{"where": {"field": "serial_number", "op": "eq", "value": "SN-SRV-729"}}`, "serial_number"},
		{"flat asset_category", `{"entity_kind": "asset", "asset_category": "accessory"}`, "asset_category"},
		{"flat warranty_within_days", `{"entity_kind": "asset", "warranty_within_days": 90}`, "warranty_within_days"},
		{"AST attribute", `{"where": {"field": "attributes.cost_center", "op": "exists"}}`, "attributes.cost_center"},
		{"FilterSpec.Attributes", `{"attributes": {"cost_center": "CC-4711"}}`, "attributes.cost_center"},
		{"tags as attributes.tags", `{"entity_kind": "asset", "tags": ["fr"]}`, "tags"},
		{"sort key", `{"sort": [{"field": "serial_number", "dir": "asc"}]}`, "serial_number"},
		{"column", `{"columns": ["name", "serial_number"]}`, "serial_number"},
		{"asset field of a CI", `{"where": {"field": "asset.purchase_cost", "op": "gt", "value": 100}}`, "asset.purchase_cost"},
		{"asset custom field", `{"entity_kind": "asset", "where": {"field": "attributes.secret", "op": "exists"}}`, "attributes.secret"},
		{"asset sort on a column", `{"entity_kind": "asset", "sort": [{"field": "supplier"}]}`, "supplier"},
	} {
		code, out, raw := e.query(policy, c.body)
		if code != http.StatusForbidden || out["type"] != "urn:reticora:problem:forbidden" || !slices.Equal(fieldsOf(out), []string{c.field}) {
			t.Errorf("%s: %d %s, want 403 with fields [%s]", c.name, code, raw, c.field)
		}
		if _, has := out["total"]; has || strings.Contains(raw, "fr-server") {
			t.Errorf("%s: the refusal carries a result: %s", c.name, raw)
		}
	}
	// Several refused fields are named together, sorted.
	if code, out, raw := e.query(policy, `{"attributes": {"cost_center": "x"}, "columns": ["serial_number"]}`); code != http.StatusForbidden ||
		!slices.Equal(fieldsOf(out), []string{"attributes.cost_center", "serial_number"}) {
		t.Errorf("two refused fields: %d %s", code, raw)
	}

	// Without the read permission of the entity kind the 403 has no fields.
	noRead := chi.NewMux()
	savedview.NewHandler(savedview.NewMemoryRepository()).WithQueryEngine(savedview.NewPGQueryEngine(e.f.App)).
		WithPermissions(grants{"asset:read": true}).WithEntitlements(features{"assets_basic": true}).WithInventory(true).RegisterRoutes(noRead)
	saved := e.mux
	e.mux = noRead
	if code, out, raw := e.query(policy, `{"where": {"field": "serial_number", "op": "eq", "value": "x"}}`); code != http.StatusForbidden ||
		out["fields"] != nil {
		t.Errorf("without ci:read: %d %s, want 403 without fields", code, raw)
	}
	e.mux = saved

	// Structural fields are not checked; ci_type narrows the queried types,
	// for which serial_number is readable.
	for _, body := range []string{
		`{"where": {"and": [{"field": "name", "op": "like", "value": "fr-"}, {"field": "status", "op": "eq", "value": "active"},
			{"field": "client_id", "op": "exists"}, {"field": "lifecycle_state", "op": "exists", "value": false}]},
			"sort": [{"field": "name"}], "columns": ["id", "status", "ci_type", "location_id"]}`,
		`{"ci_type": "switch", "where": {"field": "serial_number", "op": "eq", "value": "SN-SW-729"}, "columns": ["serial_number"]}`,
		`{"entity_kind": "asset", "where": {"field": "name", "op": "eq", "value": "FR-A-729"}, "columns": ["asset_tag", "status"]}`,
	} {
		if code, _, raw := e.query(policy, body); code != http.StatusOK {
			t.Errorf("%s: %d %s, want 200", body, code, raw)
		}
	}
	if code, out, raw := e.query(policy, `{"ci_type": "switch", "columns": ["serial_number"]}`); code != http.StatusOK ||
		!strings.Contains(raw, "SN-SW-729") || out["total"] != float64(1) {
		t.Errorf("switch column: %d %s", code, raw)
	}

	// Projection: summary and attributes per type.
	code, out, raw := e.query(policy, `{"search": "fr-"}`)
	if code != http.StatusOK {
		t.Fatalf("ci query: %d %s", code, raw)
	}
	rows := map[string]map[string]any{}
	for _, row := range out["data"].([]any) {
		r := row.(map[string]any)
		rows[r["id"].(string)] = r
	}
	srv, sw := rows[e.serverID], rows[e.switchID]
	if srv == nil || sw == nil {
		t.Fatalf("rows %s", raw)
	}
	if srv["summary"] != "fr-host Dell R650" || sw["summary"] != "fr-sw Cisco SN-SW-729" {
		t.Errorf("summaries %q and %q, want the server's without its serial number", srv["summary"], sw["summary"])
	}
	attrs := srv["attributes"].(map[string]any)
	instance, _ := attrs["_instance"].(map[string]any)
	observed, _ := attrs["_observed"].(map[string]any)
	if attrs["location"] != "Berlin" || attrs["cost_center"] != nil || instance["rack"] != "R1" || instance["secret"] != nil ||
		observed["hostname"] == nil || observed["serial_number"] != nil {
		t.Errorf("server attributes %v", attrs)
	}
	for _, leak := range []string{"SN-SRV-729", "CC-4711", "inst-secret"} {
		if strings.Contains(raw, leak) {
			t.Errorf("ci query leaks %q: %s", leak, raw)
		}
	}
	code, out, raw = e.query(policy, `{"entity_kind": "asset", "search": "FR-A-729"}`)
	if code != http.StatusOK || out["total"] != float64(1) {
		t.Fatalf("asset query: %d %s", code, raw)
	}
	asset := out["data"].([]any)[0].(map[string]any)
	if asset["summary"] != "FR-A-729 in_stock" || asset["attributes"].(map[string]any)["color"] != "blue" {
		t.Errorf("asset row %v", asset)
	}
	for _, leak := range []string{"asset-secret", "ACME-729", "accessory", `"tags"`} {
		if strings.Contains(raw, leak) {
			t.Errorf("asset query leaks %q: %s", leak, raw)
		}
	}

	// A principal without a resolvable policy gets no result (fail-closed).
	if code, _, raw := e.query(nil, `{"search": "fr-"}`); code != http.StatusInternalServerError || strings.Contains(raw, "fr-server") {
		t.Errorf("without policy: %d %s, want 500 without rows", code, raw)
	}
	// A nil policy projects every field with field permissions away.
	rows2 := []savedview.QueryResult{{ID: e.serverID, EntityKind: "ci", Name: "fr-server", TypeID: e.server,
		SummaryParts: []savedview.SummaryPart{{Field: "hostname", Value: "fr-host"}}, Attributes: map[string]any{"location": "Berlin"},
		Columns: map[string]any{"name": "fr-server", "hostname": "fr-host"}}}
	savedview.ProjectResults(nil, "ci", rows2)
	if r := rows2[0]; r.Summary != "" || len(r.Attributes) != 0 || len(r.Columns) != 1 || r.Columns["name"] != "fr-server" {
		t.Errorf("nil policy projection %+v", r)
	}
}

// TestStructuredQueryWithoutFieldRightsUnchanged: without field permissions
// the answer is the one of phase 1: the summary is concat_ws of its
// columns, the attributes are stored ones, and an unrestricted policy and
// a policy resolved for an organization without grants answer the same
// bytes.
func TestStructuredQueryWithoutFieldRightsUnchanged(t *testing.T) {
	e := newFREnv(t)
	resolved := e.resolve(permission.NewMemoryFieldGrants())
	for _, body := range []string{
		`{"search": "fr-", "columns": ["serial_number", "attributes.cost_center"], "sort": [{"field": "serial_number"}]}`,
		`{"entity_kind": "asset", "search": "FR-A-729", "columns": ["supplier"]}`,
	} {
		code, out, raw := e.query(resolved, body)
		codeU, _, rawU := e.query(permission.UnrestrictedPolicy(nil), body)
		if code != http.StatusOK || codeU != http.StatusOK || raw != rawU {
			t.Fatalf("%s: %d %s\nunrestricted: %d %s", body, code, raw, codeU, rawU)
		}
		for _, row := range out["data"].([]any) {
			r := row.(map[string]any)
			table, summary := "ci", "concat_ws(' ', hostname, manufacturer, model, serial_number, os_name)"
			if r["entity_kind"] == "asset" {
				table, summary = "asset", "concat_ws(' ', asset_tag, category, status, supplier, serial_number, location)"
			}
			var want string
			if err := e.f.Admin.QueryRow(context.Background(), "SELECT COALESCE("+summary+", '') FROM "+table+" WHERE id = $1",
				r["id"]).Scan(&want); err != nil {
				t.Fatal(err)
			}
			if got, _ := r["summary"].(string); got != want {
				t.Errorf("summary %q, want concat_ws %q", got, want)
			}
		}
	}
	if _, _, raw := e.query(resolved, `{"search": "fr-server"}`); !strings.Contains(raw, "SN-SRV-729") || !strings.Contains(raw, "inst-secret") ||
		!strings.Contains(raw, "CC-4711") {
		t.Errorf("without field permissions the stored attributes are answered: %s", raw)
	}
}
