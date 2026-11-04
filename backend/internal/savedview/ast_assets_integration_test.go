package savedview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// srcAllowAll grants every permission key (PermissionChecker) and every
// feature (EntitlementChecker).
type srcAllowAll struct{}

func (srcAllowAll) HasPermission(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (srcAllowAll) IsEnabled(context.Context, string, string) bool { return true }

// srcAccess holds the role assignment of the fixture user for the field
// policy.
type srcAccess struct{ grants []identity.Grant }

func (a srcAccess) AccessGrants(context.Context, string, string) ([]identity.Grant, error) {
	return a.grants, nil
}

func (srcAccess) RoleGrants(context.Context, string, []string) ([]identity.Grant, error) {
	return nil, nil
}

// srcEnv is the fixture of the WP-309 tests: two CIs (big with a linked
// asset, small), two assets, attribute definitions and tags of
// organization A.
type srcEnv struct {
	t                *testing.T
	f                *scopetest.Fixture
	big, small       string
	assetA, assetB   string
	owner            string
	mux              *chi.Mux
	engine           *PGQueryEngine
	ciNames, asNames map[string]string
}

func newSRCEnv(t *testing.T) *srcEnv {
	t.Helper()
	f := scopetest.Seed(t, "bc")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	e := &srcEnv{t: t, f: f, engine: NewPGQueryEngine(f.App)}
	e.big = f.CI(t, f.OrgA, f.Client1, "wp309-big")
	e.small = f.CI(t, f.OrgA, f.Client1, "wp309-small")
	e.assetA = f.Asset(t, f.OrgA, f.Client1, "WP309-A")
	e.assetB = f.Asset(t, f.OrgA, f.Client1, "WP309-B")
	e.ciNames = map[string]string{e.big: "wp309-big", e.small: "wp309-small"}
	e.asNames = map[string]string{e.assetA: "WP309-A", e.assetB: "WP309-B"}
	// Organization-global definitions (MET-13); days_left is computed
	// (WP-258), its value written as the refresh of WP-717 writes it.
	for _, d := range []struct {
		key, dataType string
		searchable    bool
		computed      any
	}{
		{"ram_gb", "integer", true, nil},
		{"wp309_rack", "text", true, nil},
		{"bought", "date", true, nil},
		{"secret_note", "text", false, nil},
		{"room", "text", true, nil},
		{"days_left", "integer", true, `{"op":"days_between","args":[{"op":"today"},{"op":"field","field":"asset.warranty_until"}]}`},
	} {
		exec(`INSERT INTO attribute_definition (organization_id, ci_type_id, scope, key, data_type, searchable, computed)
			VALUES ($1, NULL, 'global', $2, $3, $4, $5::jsonb)`, f.OrgA, d.key, d.dataType, d.searchable, d.computed)
	}
	exec(`UPDATE ci SET attributes = '{"ram_gb": 64, "wp309_rack": "R12", "bought": "2024-03-01", "days_left": 10, "secret_note": "x"}' WHERE id = $1`, e.big)
	exec(`UPDATE ci SET attributes = '{"ram_gb": 8, "wp309_rack": "R7", "bought": "2022-01-01", "days_left": 400}' WHERE id = $1`, e.small)
	exec(`UPDATE asset SET cost_center = 'CC-1', custom_fields = '{"room": "R1"}', warranty_until = '2026-12-31',
		supplier = 'ACME Corp', ci_id = $2 WHERE id = $1`, e.assetA, e.big)
	exec(`UPDATE asset SET cost_center = 'CC-2', custom_fields = '{"room": "R2"}', warranty_until = '2030-01-01',
		supplier = 'Other Ltd' WHERE id = $1`, e.assetB)
	exec(`UPDATE ci SET asset_id = $2 WHERE id = $1`, e.big, e.assetA)
	tags := map[string]string{}
	for _, key := range []string{"prod", "dmz"} {
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO tag (organization_id, key, display_name) VALUES ($1, $2, '{"de": "T"}') RETURNING id::text`,
			f.OrgA, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		tags[key] = id
	}
	for _, link := range []struct{ ci, tag string }{{e.big, "prod"}, {e.big, "dmz"}, {e.small, "prod"}} {
		exec(`INSERT INTO ci_tag (organization_id, ci_id, tag_id) VALUES ($1, $2, $3)`, f.OrgA, link.ci, tags[link.tag])
	}
	for _, link := range []struct{ asset, tag string }{{e.assetA, "prod"}, {e.assetB, "dmz"}} {
		exec(`INSERT INTO asset_tag_link (organization_id, asset_id, tag_id) VALUES ($1, $2, $3)`, f.OrgA, link.asset, tags[link.tag])
	}
	e.owner = f.AppUser(t, f.OrgA, "wp309-owner")
	e.mux = chi.NewRouter()
	NewHandler(NewPGRepository(f.App)).WithQueryEngine(e.engine).WithPermissions(srcAllowAll{}).
		WithEntitlements(srcAllowAll{}).WithInventory(true).RegisterRoutes(e.mux)
	return e
}

// run executes the spec on the engine and returns the sorted names of the
// rows.
func (e *srcEnv) run(spec string) ([]string, error) {
	e.t.Helper()
	var s FilterSpec
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		e.t.Fatal(err)
	}
	rows, _, err := e.engine.Query(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &s, api.PaginationParams{Limit: 100})
	var names []string
	for i := range rows {
		names = append(names, rows[i].Name)
	}
	sort.Strings(names)
	return names, err
}

// call runs a request as the owner with the field policy and returns the
// status and the decoded body.
func (e *srcEnv) call(policy *permission.FieldPolicy, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	org := e.f.OrgA
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org, UserID: e.owner})
	scope := database.OrgWideScope(org, e.owner)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: e.owner, OrganizationID: org, Type: identity.PrincipalTypeUser})
	ctx = permission.WithPolicy(ctx, policy)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// TestStructuredQueryAssetsAgainstPostgres covers SRC-03 (WP-309) against
// PostgreSQL: attributes are resolved against attribute_definition (unknown
// 422, not searchable 400 with the hint, in the AST and the flat
// attributes) and compare by the kind of their definition; between on
// numbers and dates; has_any and has_all on the tags of CIs and assets
// (unknown tag 422); a computed attribute; asset.attributes.<key> and the
// AST-01 column asset.cost_center; saved views with asset.warranty_end and
// asset.supplier like; every preset runs.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStructuredQueryAssetsAgainstPostgres(t *testing.T) {
	e := newSRCEnv(t)
	for _, c := range []struct {
		name, spec string
		want       []string
	}{
		// A number given as a string compares numerically by the definition.
		{"integer as a string", `{"where": {"field": "attributes.ram_gb", "op": "gte", "value": "16"}}`, []string{"wp309-big"}},
		{"flat integer as a string", `{"attributes": {"ram_gb": "8"}}`, []string{"wp309-small"}},
		{"between numbers", `{"where": {"field": "attributes.ram_gb", "op": "between", "value": [8, 16]}}`, []string{"wp309-small"}},
		{"between inclusive", `{"where": {"field": "attributes.ram_gb", "op": "between", "value": [8, 64]}}`, []string{"wp309-big", "wp309-small"}},
		{"between dates", `{"where": {"field": "attributes.bought", "op": "between", "value": ["2023-01-01", "2024-12-31"]}}`, []string{"wp309-big"}},
		{"between on a column", `{"where": {"and": [{"field": "name", "op": "like", "value": "wp309-*"},
			{"field": "created_at", "op": "between", "value": ["2000-01-01", "2999-01-01"]}]}}`, []string{"wp309-big", "wp309-small"}},
		{"text attribute", `{"where": {"field": "attributes.wp309_rack", "op": "eq", "value": "R12"}}`, []string{"wp309-big"}},
		{"has_any", `{"where": {"field": "tags", "op": "has_any", "value": ["dmz"]}}`, []string{"wp309-big"}},
		{"has_any of two", `{"where": {"field": "tags", "op": "has_any", "value": ["dmz", "prod"]}}`, []string{"wp309-big", "wp309-small"}},
		{"has_all", `{"where": {"field": "tags", "op": "has_all", "value": ["prod", "dmz"]}}`, []string{"wp309-big"}},
		{"not has_all", `{"where": {"and": [{"field": "name", "op": "like", "value": "wp309-*"},
			{"not": {"field": "tags", "op": "has_all", "value": ["prod", "dmz"]}}]}}`, []string{"wp309-small"}},
		{"flat tags", `{"tags": ["prod", "dmz"]}`, []string{"wp309-big"}},
		{"computed attribute", `{"where": {"field": "attributes.days_left", "op": "lt", "value": 30}}`, []string{"wp309-big"}},
		{"asset.attributes", `{"where": {"field": "asset.attributes.room", "op": "eq", "value": "R1"}}`, []string{"wp309-big"}},
		{"asset.cost_center", `{"where": {"field": "asset.cost_center", "op": "eq", "value": "CC-1"}}`, []string{"wp309-big"}},
		{"asset tags has_any", `{"entity_kind": "asset", "where": {"field": "tags", "op": "has_any", "value": ["prod", "dmz"]}}`, []string{"WP309-A", "WP309-B"}},
		{"asset tags has_all", `{"entity_kind": "asset", "where": {"field": "tags", "op": "has_all", "value": ["prod"]}}`, []string{"WP309-A"}},
		{"asset flat tags", `{"entity_kind": "asset", "tags": ["dmz"]}`, []string{"WP309-B"}},
		{"asset custom field", `{"entity_kind": "asset", "where": {"field": "attributes.room", "op": "eq", "value": "R2"}}`, []string{"WP309-B"}},
		{"asset cost_center", `{"entity_kind": "asset", "where": {"field": "cost_center", "op": "like", "value": "CC-*"}}`, []string{"WP309-A", "WP309-B"}},
	} {
		got, err := e.run(c.spec)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v (%v), want %v", c.name, got, err, c.want)
		}
	}

	// Refusals of the catalog: 422 for an unknown attribute or tag, 400
	// with the hint for an attribute that is not searchable.
	for _, c := range []struct {
		name, spec, field string
		status            int
		hint              string
	}{
		{"unknown attribute", `{"where": {"and": [{"field": "name", "op": "eq", "value": "x"}, {"field": "attributes.nope", "op": "eq", "value": "x"}]}}`,
			"where.and[1]", http.StatusUnprocessableEntity, `unknown attribute "nope"`},
		{"flat unknown attribute", `{"attributes": {"nope": "x"}}`, "attributes.nope", http.StatusUnprocessableEntity, `unknown attribute "nope"`},
		{"not searchable", `{"where": {"field": "attributes.secret_note", "op": "eq", "value": "x"}}`, "where", http.StatusBadRequest, "mark it searchable"},
		{"flat not searchable", `{"attributes": {"secret_note": "x"}}`, "attributes.secret_note", http.StatusBadRequest, "mark it searchable"},
		{"unknown asset attribute", `{"where": {"field": "asset.attributes.nope", "op": "exists"}}`, "where", http.StatusUnprocessableEntity, "unknown attribute"},
		{"unknown tag", `{"where": {"field": "tags", "op": "has_any", "value": ["prod", "nope"]}}`, "where.value[1]", http.StatusUnprocessableEntity, `unknown tag "nope"`},
		{"flat unknown tag", `{"entity_kind": "asset", "tags": ["nope"]}`, "tags[0]", http.StatusUnprocessableEntity, `unknown tag "nope"`},
		{"text value of an integer", `{"where": {"field": "attributes.ram_gb", "op": "eq", "value": "16GB"}}`, "where.value", http.StatusUnprocessableEntity, "number"},
	} {
		_, err := e.run(c.spec)
		var fe *api.FieldError
		status := 0
		if errors.As(err, &fe) {
			status = fe.Status
			if status == 0 {
				status = http.StatusBadRequest
			}
		}
		if fe == nil || fe.Field != c.field || status != c.status || !strings.Contains(fe.Message, c.hint) {
			t.Errorf("%s: %v, want %d on %s with %q", c.name, err, c.status, c.field, c.hint)
		}
		// The same through the API.
		code, out := e.call(permission.UnrestrictedPolicy(nil), http.MethodPost, "/api/v1/search/query", c.spec)
		if code != c.status || out["field"] != c.field {
			t.Errorf("%s via the API: %d %v, want %d on %s", c.name, code, out, c.status, c.field)
		}
	}

	// Saved views with the AST-01 alias asset.warranty_end and a like on
	// the free text asset.supplier keep their hits; saving checks the
	// catalog like the query.
	for _, c := range []struct{ name, where string }{
		{"warranty_end", `{"field": "asset.warranty_end", "op": "lt", "value": "2027-01-01"}`},
		{"supplier like", `{"field": "asset.supplier", "op": "like", "value": "ACME*"}`},
	} {
		code, out := e.call(permission.UnrestrictedPolicy(nil), http.MethodPost, "/api/v1/saved-views",
			`{"name": "wp309 `+c.name+`", "entity_kind": "ci", "filter_spec": {"where": `+c.where+`}}`)
		id, _ := out["id"].(string)
		if code != http.StatusCreated || id == "" {
			t.Fatalf("save %s: %d %v", c.name, code, out)
		}
		code, out = e.call(permission.UnrestrictedPolicy(nil), http.MethodGet, "/api/v1/saved-views/"+id, "")
		spec, _ := json.Marshal(out["filter_spec"])
		if code != http.StatusOK {
			t.Fatalf("read %s: %d %v", c.name, code, out)
		}
		code, out = e.call(permission.UnrestrictedPolicy(nil), http.MethodPost, "/api/v1/search/query", string(spec))
		data, _ := out["data"].([]any)
		if code != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["name"] != "wp309-big" {
			t.Errorf("run %s: %d %v, want wp309-big", c.name, code, out)
		}
	}
	for _, c := range []struct{ body, field string }{
		{`{"name": "wp309 bad", "entity_kind": "ci", "filter_spec": {"where": {"field": "attributes.nope", "op": "exists"}}}`, "filter_spec.where"},
		{`{"name": "wp309 bad", "entity_kind": "ci", "filter_spec": {"attributes": {"secret_note": "x"}}}`, "filter_spec.attributes.secret_note"},
		{`{"name": "wp309 bad", "entity_kind": "asset", "filter_spec": {"tags": ["nope"]}}`, "filter_spec.tags[0]"},
	} {
		if code, out := e.call(permission.UnrestrictedPolicy(nil), http.MethodPost, "/api/v1/saved-views", c.body); code/100 != 4 || out["field"] != c.field {
			t.Errorf("save %s: %d %v, want a refusal on %s", c.body, code, out, c.field)
		}
	}

	// Every preset runs without an error.
	for _, p := range Presets() {
		raw, err := json.Marshal(p.FilterSpec)
		if err != nil {
			t.Fatal(err)
		}
		var spec FilterSpec
		if err = json.Unmarshal(raw, &spec); err != nil {
			t.Fatal(err)
		}
		spec.EntityKind = p.EntityKind
		if _, _, err = e.engine.Query(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &spec, api.PaginationParams{Limit: 10}); err != nil {
			t.Errorf("preset %q: %v", p.Name, err)
		}
	}
}

// TestStructuredQueryFieldClassesAgainstPostgres covers the field
// permissions of the WP-309 field classes (RBA-07, WP-729): a principal who
// may not read the field gets 403 with the field for asset.attributes.<key>,
// an AST-01 column of the linked asset, a tag comparison, a between
// comparison and a computed attribute; the readable fields pass.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStructuredQueryFieldClassesAgainstPostgres(t *testing.T) {
	e := newSRCEnv(t)
	const role = "00000000-0000-4000-8000-00000000f309"
	repo := permission.NewMemoryFieldGrants()
	bg := context.Background()
	ptr := func(s string) *string { return &s }
	for _, g := range []permission.FieldGrant{
		{ObjectKind: permission.KindAsset, FieldKey: ptr("attributes.room")},
		{ObjectKind: permission.KindAsset, FieldKey: ptr("core.cost_center")},
		{ObjectKind: permission.KindCI, FieldKey: ptr("attributes.tags")},
		{ObjectKind: permission.KindCI, FieldKey: ptr("attributes.ram_gb")},
		{ObjectKind: permission.KindCI, FieldKey: ptr("attributes.days_left")},
	} {
		g.RoleID, g.Level = ptr(role), permission.LevelNone
		if _, err := repo.CreateFieldGrant(bg, e.f.OrgA, &g); err != nil {
			t.Fatal(err)
		}
	}
	perms := []identity.Permission{identity.PermCIRead, identity.PermAssetRead, "search:read"}
	policy, err := permission.NewFieldResolver(srcAccess{grants: []identity.Grant{{RoleID: role, Permissions: perms}}}, nil, repo, nil).
		Resolve(bg, identity.Principal{Subject: e.owner, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser, Permissions: perms})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, body, field string }{
		{"asset.attributes", `{"where": {"field": "asset.attributes.room", "op": "eq", "value": "R1"}}`, "asset.attributes.room"},
		{"asset AST-01 column", `{"where": {"field": "asset.cost_center", "op": "eq", "value": "CC-1"}}`, "asset.cost_center"},
		{"tag comparison", `{"where": {"field": "tags", "op": "has_any", "value": ["prod"]}}`, "tags"},
		{"between", `{"where": {"field": "attributes.ram_gb", "op": "between", "value": [1, 2]}}`, "attributes.ram_gb"},
		{"computed attribute", `{"where": {"field": "attributes.days_left", "op": "lt", "value": 30}}`, "attributes.days_left"},
	} {
		code, out := e.call(policy, http.MethodPost, "/api/v1/search/query", c.body)
		fields, _ := out["fields"].([]any)
		if code != http.StatusForbidden || len(fields) != 1 || fields[0] != c.field || out["total"] != nil {
			t.Errorf("%s: %d %v, want 403 with fields [%s]", c.name, code, out, c.field)
		}
		code, out = e.call(policy, http.MethodPost, "/api/v1/saved-views", `{"name": "wp309 fr", "entity_kind": "ci", "filter_spec": `+c.body+`}`)
		if fields, _ = out["fields"].([]any); code != http.StatusForbidden || len(fields) != 1 || fields[0] != c.field {
			t.Errorf("save %s: %d %v, want 403 with fields [%s]", c.name, code, out, c.field)
		}
	}
	if code, out := e.call(policy, http.MethodPost, "/api/v1/search/query",
		`{"where": {"field": "attributes.bought", "op": "between", "value": ["2023-01-01", "2024-12-31"]}}`); code != http.StatusOK {
		t.Errorf("a readable attribute: %d %v, want 200", code, out)
	}
}

// TestStructuredQueryIndexUseAgainstPostgres runs EXPLAIN on the statement
// of the query engine as the application role under row level security
// (SR-04, WP-836, K22): tag comparisons read the indexes of ci_tag and
// asset_tag_link, asset fields of a CI the primary key of asset or
// idx_ci_org_asset, equality on a searchable attribute the index of its key
// (idx_attr_key_<key>, 000164). The plans of the other comparisons are
// logged (index or scan); their budget at 10,000 objects is WP-327.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStructuredQueryIndexUseAgainstPostgres(t *testing.T) {
	e := newSRCEnv(t)
	bg := context.Background()
	const key = "wp309_rack"
	if _, err := e.f.Admin.Exec(bg, citype.KeyIndexStatement(key)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.f.Admin.Exec(context.Background(), `DROP INDEX IF EXISTS `+citype.KeyIndexName(key))
	})
	explain := func(spec string) string {
		t.Helper()
		var s FilterSpec
		if err := json.Unmarshal([]byte(spec), &s); err != nil {
			t.Fatal(err)
		}
		var plan []string
		err := database.WithRequestTenant(e.f.OrgCtx(e.f.OrgA), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			cat, err := resolveCatalog(ctx, tx, &s)
			if err != nil {
				return err
			}
			table := s.entityKind()
			where, args, err := s.compile(table, cat, []any{e.f.OrgA}, 2)
			if err != nil {
				return err
			}
			base := fmt.Sprintf("%s.organization_id = $1 AND %s", table, where)
			if table == "ci" {
				base = "ci.deleted_at IS NULL AND " + base
			}
			if _, err = tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, fmt.Sprintf("EXPLAIN SELECT %s.id FROM %s WHERE %s", table, table, base), args...)
			if err != nil {
				return err
			}
			plan, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		if err != nil {
			t.Fatalf("explain %s: %v", spec, err)
		}
		return strings.Join(plan, "\n")
	}
	for _, c := range []struct {
		name, spec string
		indexes    []string // one of them
	}{
		{"ci tags has_any", `{"where": {"field": "tags", "op": "has_any", "value": ["prod"]}}`, []string{"ci_tag_ci_id_tag_id_key", "idx_ci_tag_org_tag"}},
		{"ci tags has_all", `{"where": {"field": "tags", "op": "has_all", "value": ["prod", "dmz"]}}`, []string{"ci_tag_ci_id_tag_id_key", "idx_ci_tag_org_tag"}},
		{"asset tags", `{"entity_kind": "asset", "where": {"field": "tags", "op": "has_any", "value": ["prod"]}}`,
			[]string{"asset_tag_link_asset_id_tag_id_key", "idx_asset_tag_link_org_tag"}},
		{"asset field of a CI", `{"where": {"field": "asset.cost_center", "op": "eq", "value": "CC-1"}}`, []string{"asset_pkey", "idx_ci_org_asset"}},
		{"asset attribute of a CI", `{"where": {"field": "asset.attributes.room", "op": "eq", "value": "R1"}}`, []string{"asset_pkey", "idx_ci_org_asset"}},
		{"attribute equality", `{"where": {"field": "attributes.wp309_rack", "op": "eq", "value": "R12"}}`, []string{citype.KeyIndexName(key)}},
		{"flat attribute equality", `{"attributes": {"wp309_rack": "R12"}}`, []string{citype.KeyIndexName(key)}},
	} {
		plan := explain(c.spec)
		if !slices.ContainsFunc(c.indexes, func(index string) bool { return strings.Contains(plan, index) }) {
			t.Errorf("%s does not use %v:\n%s", c.name, c.indexes, plan)
		}
	}
	// The other comparisons are documented, not asserted (SR-04).
	for _, spec := range []string{
		`{"where": {"field": "attributes.wp309_rack", "op": "like", "value": "R1*"}}`,
		`{"where": {"field": "attributes.ram_gb", "op": "between", "value": [8, 16]}}`,
		`{"where": {"field": "attributes.bought", "op": "gte", "value": "2024-01-01"}}`,
	} {
		t.Logf("%s:\n%s", spec, explain(spec))
	}
}
