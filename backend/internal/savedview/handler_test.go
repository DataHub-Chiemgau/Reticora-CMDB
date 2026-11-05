package savedview_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// fakeEngine is a query engine without a database: it answers every query
// with one row and refuses a spec with err (also when checking a spec).
// It counts the queries and checks it served.
type fakeEngine struct {
	err             error
	queries, checks int
}

func (e *fakeEngine) Query(context.Context, string, *savedview.FilterSpec, api.PaginationParams) ([]savedview.QueryResult, int, error) {
	e.queries++
	if e.err != nil {
		return nil, 0, e.err
	}
	return []savedview.QueryResult{{ID: "row", EntityKind: "ci", Name: "row"}}, 1, nil
}

func (e *fakeEngine) CheckSpec(context.Context, string, *savedview.FilterSpec) error {
	e.checks++
	return e.err
}

// features is an EntitlementChecker enabling exactly the listed features.
type features map[string]bool

func (f features) IsEnabled(_ context.Context, _, key string) bool { return f[key] }

// handlerCase is the configuration of one handler of the WP-309 tests.
type handlerCase struct {
	perms        grants
	entitlements savedview.EntitlementChecker
	inventory    bool
	engine       savedview.QueryEngine
}

// serve runs one request as the org-wide user "user" of organization
// "org" without field permissions and returns status and problem.
func (c handlerCase) serve(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	h := savedview.NewHandler(savedview.NewMemoryRepository()).WithPermissions(c.perms).WithInventory(c.inventory)
	if c.entitlements != nil {
		h.WithEntitlements(c.entitlements)
	}
	if c.engine != nil {
		h.WithQueryEngine(c.engine)
	}
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org", UserID: "user"})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user", OrganizationID: "org", Type: identity.PrincipalTypeUser})
	ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(nil))
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// TestStructuredQueryAssetAccess covers WP-309 without a database: asset
// references (entity_kind asset, asset.<field>, asset.attributes.<key>)
// need asset:read (403 missing permission asset:read, also for a CI query),
// the module inventory and the entitlement assets_basic (403
// feature-not-entitled; without an entitlement checker refused), on POST
// /api/v1/search/query and when saving a view. A CI query without asset
// fields needs none of them.
func TestStructuredQueryAssetAccess(t *testing.T) {
	assetsBasic := features{"assets_basic": true}
	all := grants{"ci:read": true, "asset:read": true}
	const (
		ciAssetField = `{"where": {"field": "asset.cost_center", "op": "eq", "value": "CC"}}`
		ciAssetAttr  = `{"where": {"or": [{"field": "name", "op": "eq", "value": "x"}, {"field": "asset.attributes.room", "op": "eq", "value": "R1"}]}}`
		assetQuery   = `{"entity_kind": "asset"}`
		ciOnly       = `{"where": {"field": "name", "op": "eq", "value": "x"}}`
	)
	for _, c := range []struct {
		name   string
		cfg    handlerCase
		body   string
		status int
		detail string // problem detail or type
	}{
		{"asset field with ci:read only", handlerCase{grants{"ci:read": true}, assetsBasic, true, nil}, ciAssetField, http.StatusForbidden, "missing permission asset:read"},
		{"asset attribute with ci:read only", handlerCase{grants{"ci:read": true}, assetsBasic, true, nil}, ciAssetAttr, http.StatusForbidden, "missing permission asset:read"},
		{"asset query without assets_basic", handlerCase{all, features{}, true, nil}, assetQuery, http.StatusForbidden, api.ProblemFeatureNotEntitled},
		{"asset field without assets_basic", handlerCase{all, features{}, true, nil}, ciAssetField, http.StatusForbidden, api.ProblemFeatureNotEntitled},
		{"asset query, inventory off", handlerCase{all, assetsBasic, false, nil}, assetQuery, http.StatusForbidden, api.ProblemFeatureNotEntitled},
		{"asset attribute, inventory off", handlerCase{all, assetsBasic, false, nil}, ciAssetAttr, http.StatusForbidden, api.ProblemFeatureNotEntitled},
		{"asset query without checker", handlerCase{all, nil, true, nil}, assetQuery, http.StatusForbidden, api.ProblemFeatureNotEntitled},
		{"asset field without checker", handlerCase{all, nil, true, nil}, ciAssetField, http.StatusForbidden, api.ProblemFeatureNotEntitled},
		{"asset query entitled", handlerCase{all, assetsBasic, true, nil}, assetQuery, http.StatusOK, ""},
		{"asset field entitled", handlerCase{all, assetsBasic, true, nil}, ciAssetField, http.StatusOK, ""},
		{"CI query without any of them", handlerCase{grants{"ci:read": true}, nil, false, nil}, ciOnly, http.StatusOK, ""},
	} {
		engine := &fakeEngine{}
		c.cfg.engine = engine
		code, out := c.cfg.serve(t, http.MethodPost, "/api/v1/search/query", c.body)
		if code != c.status || (c.detail != "" && out["detail"] != c.detail && out["type"] != c.detail) {
			t.Errorf("query, %s: %d %v, want %d %s", c.name, code, out, c.status, c.detail)
		}
		if refused := c.status != http.StatusOK; refused && engine.queries != 0 {
			t.Errorf("query, %s: the engine ran although the request was refused", c.name)
		}

		// Saving a view checks the same (POST and PATCH).
		engine = &fakeEngine{}
		c.cfg.engine = engine
		var spec map[string]any
		if err := json.Unmarshal([]byte(c.body), &spec); err != nil {
			t.Fatal(err)
		}
		kind, _ := spec["entity_kind"].(string)
		delete(spec, "entity_kind")
		filter, _ := json.Marshal(spec)
		view := `{"name": "v", "entity_kind": "` + kind + `", "filter_spec": ` + string(filter) + `}`
		want := c.status
		if want == http.StatusOK {
			want = http.StatusCreated
		}
		code, out = c.cfg.serve(t, http.MethodPost, "/api/v1/saved-views", view)
		if code != want || (c.detail != "" && out["detail"] != c.detail && out["type"] != c.detail) {
			t.Errorf("save, %s: %d %v, want %d %s", c.name, code, out, want, c.detail)
		}
		if refused := c.status != http.StatusOK; !refused && engine.checks != 1 {
			t.Errorf("save, %s: %d engine checks, want 1", c.name, engine.checks)
		}
	}
}

// TestSavedViewPatchChecksTheFilter: PATCH checks the filter like POST
// (WP-309); the view stays as it was when the check refuses.
func TestSavedViewPatchChecksTheFilter(t *testing.T) {
	repo := savedview.NewMemoryRepository()
	engine := &fakeEngine{}
	mux := chi.NewRouter()
	savedview.NewHandler(repo).WithQueryEngine(engine).WithPermissions(grants{"ci:read": true}).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org", UserID: "user"})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user", OrganizationID: "org", Type: identity.PrincipalTypeUser})
	ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(nil))
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := call(http.MethodPost, "/api/v1/saved-views", `{"name": "v", "entity_kind": "ci", "filter_spec": {"status": "active"}}`)
	id, _ := out["id"].(string)
	if code != http.StatusCreated || id == "" {
		t.Fatalf("create: %d %v", code, out)
	}
	code, out = call(http.MethodPatch, "/api/v1/saved-views/"+id, `{"name": "w", "filter_spec": {"where": {"field": "asset.model", "op": "eq", "value": "x"}}}`)
	if code != http.StatusForbidden || out["detail"] != "missing permission asset:read" {
		t.Errorf("patch with an asset field: %d %v, want 403 missing permission asset:read", code, out)
	}
	engine.err = &api.FieldError{Field: "where", Message: "unknown attribute \"nope\"", Status: http.StatusUnprocessableEntity}
	code, out = call(http.MethodPatch, "/api/v1/saved-views/"+id, `{"name": "w", "filter_spec": {"where": {"field": "attributes.nope", "op": "eq", "value": "x"}}}`)
	if code != http.StatusUnprocessableEntity || out["field"] != "filter_spec.where" {
		t.Errorf("patch with an unknown attribute: %d %v, want 422 on filter_spec.where", code, out)
	}
	if item, err := repo.GetByID(context.Background(), "org", id, "user"); err != nil || item.Name != "v" {
		t.Errorf("the refused PATCH changed the view: %+v %v", item, err)
	}
}

// TestStructuredQueryEngineFieldErrors: a field error of the engine (the
// catalog: unknown attribute or tag 422, not searchable 400) reaches the
// client with its status and field, for the query and, as
// filter_spec.<field>, when saving a view. Without an engine a view is
// checked statically only.
func TestStructuredQueryEngineFieldErrors(t *testing.T) {
	for _, fe := range []*api.FieldError{
		{Field: "where.and[1]", Message: `unknown attribute "nope"`, Status: http.StatusUnprocessableEntity},
		{Field: "attributes.secret", Message: `attribute "secret" is not searchable; mark it searchable in its attribute definition to filter on it`,
			Status: http.StatusBadRequest},
		{Field: "tags[0]", Message: `unknown tag "nope"`, Status: http.StatusUnprocessableEntity},
	} {
		cfg := handlerCase{perms: grants{"ci:read": true}, engine: &fakeEngine{err: fe}}
		code, out := cfg.serve(t, http.MethodPost, "/api/v1/search/query", `{"where": {"field": "name", "op": "eq", "value": "x"}}`)
		if code != fe.Status || out["field"] != fe.Field || out["detail"] != fe.Error() {
			t.Errorf("query, %s: %d %v, want %d", fe.Field, code, out, fe.Status)
		}
		code, out = cfg.serve(t, http.MethodPost, "/api/v1/saved-views", `{"name": "v", "entity_kind": "ci", "filter_spec": {"status": "active"}}`)
		if code != fe.Status || out["field"] != "filter_spec."+fe.Field {
			t.Errorf("save, %s: %d %v, want %d on filter_spec.%s", fe.Field, code, out, fe.Status, fe.Field)
		}
	}
	// The memory profile has no engine: only the static check applies, also
	// to asset fields without asset:read.
	cfg := handlerCase{perms: grants{}}
	code, out := cfg.serve(t, http.MethodPost, "/api/v1/saved-views",
		`{"name": "v", "entity_kind": "ci", "filter_spec": {"where": {"field": "asset.attributes.room", "op": "eq", "value": "R1"}}}`)
	if code != http.StatusCreated {
		t.Errorf("save without an engine: %d %v, want 201", code, out)
	}
	code, out = cfg.serve(t, http.MethodPost, "/api/v1/saved-views",
		`{"name": "w", "entity_kind": "ci", "filter_spec": {"where": {"field": "tags", "op": "eq", "value": "x"}}}`)
	if code != http.StatusUnprocessableEntity || out["field"] != "filter_spec.where" {
		t.Errorf("static check without an engine: %d %v, want 422 on filter_spec.where", code, out)
	}
}
