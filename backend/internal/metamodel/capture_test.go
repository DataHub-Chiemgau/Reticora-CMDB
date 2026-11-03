package metamodel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

const (
	captureOrg      = "00000000-0000-4000-8000-0000000000aa"
	captureDraft    = "00000000-0000-4000-8000-0000000000d1"
	finalDraft      = "00000000-0000-4000-8000-0000000000d2"
	captureTargetID = "00000000-0000-4000-8000-0000000000c1"
	captureVersion  = "00000000-0000-4000-8000-0000000000e1"
)

// stubDrafts is the draft repository of the capture tests: AddItem builds
// the item of the capture without base, TemplateVersionID resolves every
// version to captureVersion.
type stubDrafts struct {
	Repository
	added []*capture
}

func (s *stubDrafts) AddItem(_ context.Context, _, id string, c *capture, _ *int64) (*Draft, *Item, error) {
	switch id {
	case captureDraft:
	case finalDraft:
		return nil, nil, ErrDraftImmutable
	default:
		return nil, nil, ErrDraftNotFound
	}
	s.added = append(s.added, c)
	it := &Item{ID: "00000000-0000-4000-8000-00000000f00d", Kind: c.Kind, Op: c.Op, Payload: c.Payload,
		Route: route(c.Spec.Route, "{id}", c.TargetID, "{name}", c.TargetKey)}
	if c.TargetID != "" {
		it.TargetID = &c.TargetID
	}
	if c.TargetKey != "" {
		it.TargetKey = &c.TargetKey
	}
	return &Draft{ID: id, Version: int64(len(s.added) + 1)}, it, nil
}

func (s *stubDrafts) TemplateVersionID(_ context.Context, _, templateID, version string) (string, error) {
	if templateID != captureTargetID || version != "2" {
		return "", ErrTargetNotFound
	}
	return captureVersion, nil
}

// capturePayloads are valid bodies of the capable routes; the others take
// none.
var capturePayloads = map[string]string{
	"POST /api/v1/ci-types":                         `{"key":"probe","name":"Probe"}`,
	"PATCH /api/v1/ci-types/{id}":                   `{"description":"probe"}`,
	"POST /api/v1/ci-types/{id}/clone":              `{"name":"Probe copy"}`,
	"PUT /api/v1/ci-types/{id}/attribute-sets":      `{"attribute_sets":[]}`,
	"PUT /api/v1/ci-types/{id}/fields":              `{"name":"probe_field","data_type":"text"}`,
	"PUT /api/v1/ci-fields/global":                  `{"name":"probe_field","data_type":"text"}`,
	"POST /api/v1/attribute-definitions":            `{"key":"probe","data_type":"text","ci_type_id":"` + captureTargetID + `"}`,
	"PATCH /api/v1/attribute-definitions/{id}":      `{"description":"probe"}`,
	"POST /api/v1/attribute-sets":                   `{"key":"probe"}`,
	"PATCH /api/v1/attribute-sets/{id}":             `{"description":"probe"}`,
	"POST /api/v1/templates":                        `{"key":"probe","ci_type_id":"` + captureTargetID + `"}`,
	"PATCH /api/v1/templates/{id}":                  `{"description":"probe"}`,
	"POST /api/v1/templates/{id}/clone":             `{}`,
	"PUT /api/v1/templates/{id}/versions/{version}": `{"items":[]}`,
	"POST /api/v1/rules": `{"key":"probe_rule","attribute_key":"probe_field","kind":"required",` +
		`"ci_type_id":"` + captureTargetID + `"}`,
	"PATCH /api/v1/rules/{id}": `{"is_active":false}`,
	"POST /api/v1/layouts": `{"key":"probe","ci_type_id":"` + captureTargetID + `",` +
		`"layout":{"sections":[{"key":"main","title":{"de":"Haupt","en":"Main"},"fields":[{"name":"probe_field"}]}]}}`,
	"PATCH /api/v1/layouts/{id}": `{"priority":3}`,
}

// captureCall serves one request on the pattern of a capable route with
// the capture in front of a handler that counts its calls.
func captureCall(t *testing.T, repo Repository, method, pattern, target, body string, perms ...identity.Permission) (*httptest.ResponseRecorder, int) {
	t.Helper()
	calls := 0
	mux := chi.NewRouter()
	mux.With(CaptureDraft(repo, method, pattern)).Method(method, pattern, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTeapot)
	}))
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if perms == nil {
		perms = []identity.Permission{identity.PermCITypeManage, identity.PermCIAttributeManage, identity.PermMetamodelPublish}
	}
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: captureOrg, UserID: "u"})
	ctx = identity.WithPermissions(ctx, perms)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(ctx))
	return w, calls
}

// capturePath fills the path parameters of a pattern.
func capturePath(pattern string) string {
	return strings.NewReplacer("{id}", captureTargetID, "{name}", "probe_field", "{version}", "2").Replace(pattern)
}

func splitRoute(key string) (method, pattern string) {
	method, pattern, _ = strings.Cut(key, " ")
	return method, pattern
}

// TestDraftCapableRoutesAreTheItemKinds: every pair of ItemKinds is one
// capable route and back.
func TestDraftCapableRoutesAreTheItemKinds(t *testing.T) {
	pairs := 0
	for kind, ops := range ItemKinds {
		for op, sp := range ops {
			pairs++
			if got := DraftCapableRoutes[sp.Route]; got.Kind != kind || got.Op != op {
				t.Errorf("%s: captured as %s/%s, want %s/%s", sp.Route, got.Kind, got.Op, kind, op)
			}
		}
	}
	if len(DraftCapableRoutes) != pairs || pairs != 29 {
		t.Errorf("%d capable routes for %d pairs, want 29", len(DraftCapableRoutes), pairs)
	}
	for route := range DraftExemptRoutes {
		if _, capable := DraftCapableRoutes[route]; capable {
			t.Errorf("%s is capable and exempt", route)
		}
	}
}

// TestCaptureMapsEveryRoute: each capable route with ?draft becomes an item
// with kind, op, target_id and target_key from its path parameters; the
// answer is 202 with the item and the ETag of the draft, the resource
// handler does not run. Without ?draft the handler runs.
func TestCaptureMapsEveryRoute(t *testing.T) {
	for key, ri := range DraftCapableRoutes {
		method, pattern := splitRoute(key)
		repo := &stubDrafts{}
		w, calls := captureCall(t, repo, method, pattern, capturePath(pattern)+"?draft="+captureDraft, capturePayloads[key])
		if w.Code != http.StatusAccepted || calls != 0 {
			t.Errorf("%s: status %d, handler calls %d; want 202 without the handler: %s", key, w.Code, calls, w.Body)
			continue
		}
		if w.Header().Get("ETag") != `"2"` {
			t.Errorf("%s: ETag %q, want the version of the draft", key, w.Header().Get("ETag"))
		}
		var it Item
		if err := json.Unmarshal(w.Body.Bytes(), &it); err != nil || it.Kind != ri.Kind || it.Op != ri.Op {
			t.Errorf("%s: answer %s (%v), want an item %s/%s", key, w.Body, err, ri.Kind, ri.Op)
		}
		c := repo.added[0]
		wantID := ""
		switch {
		case strings.Contains(pattern, "{version}"):
			wantID = captureVersion
		case strings.Contains(pattern, "{id}"):
			wantID = captureTargetID
		}
		wantKey := ""
		if ri.Kind == KindCITypeField || ri.Kind == KindGlobalField {
			wantKey = "probe_field"
		}
		if c.Kind != ri.Kind || c.Op != ri.Op || c.TargetID != wantID || c.TargetKey != wantKey {
			t.Errorf("%s: item %s/%s target %q key %q, want %s/%s %q %q", key, c.Kind, c.Op, c.TargetID, c.TargetKey,
				ri.Kind, ri.Op, wantID, wantKey)
		}

		if w, calls = captureCall(t, repo, method, pattern, capturePath(pattern), capturePayloads[key]); w.Code != http.StatusTeapot || calls != 1 {
			t.Errorf("%s without draft: status %d, handler calls %d; want the handler", key, w.Code, calls)
		}
	}
}

// TestCaptureChecksPayloads: the payload is checked like the resource
// route without the stored metamodel, through ?draft and POST …/items.
func TestCaptureChecksPayloads(t *testing.T) {
	cases := []struct{ route, body, kind, op, target string }{
		{"POST /api/v1/ci-types", `{"key":"probe","name":"Probe","display_name":{"fr":"Sonde"}}`, KindCIType, OpCreate, ""},
		{"PATCH /api/v1/ci-types/{id}", `{"display_name":{"de":""}}`, KindCIType, OpUpdate, captureTargetID},
		{"PUT /api/v1/ci-types/{id}/fields", `{"name":"probe_field","data_type":"no_such_type"}`, KindCITypeField, OpUpsert, captureTargetID},
		{"POST /api/v1/attribute-definitions", `{"key":"probe","data_type":"no_such_type","ci_type_id":"` + captureTargetID + `"}`,
			KindAttributeDefinition, OpCreate, ""},
		{"POST /api/v1/ci-types", `{"key":"probe","name":"Probe","unknown":1}`, KindCIType, OpCreate, ""},
		{"POST /api/v1/layouts", `{"key":"probe","ci_type_id":"` + captureTargetID + `","layout":{"sections":[{"key":"_required",` +
			`"title":{"de":"a","en":"b"},"fields":[]}]}}`, KindLayout, OpCreate, ""},
	}
	for _, c := range cases {
		method, pattern := splitRoute(c.route)
		repo := &stubDrafts{}
		w, calls := captureCall(t, repo, method, pattern, capturePath(pattern)+"?draft="+captureDraft, c.body)
		if (w.Code != http.StatusBadRequest && w.Code != http.StatusUnprocessableEntity) || calls != 0 || len(repo.added) != 0 {
			t.Errorf("?draft %s %s: status %d, handler %d, items %d; want 400/422 and nothing captured", c.route, c.body,
				w.Code, calls, len(repo.added))
		}

		body, _ := json.Marshal(map[string]any{"kind": c.kind, "op": c.op, "target_id": c.target, "payload": json.RawMessage(c.body)})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/metamodel/drafts/"+captureDraft+"/items", strings.NewReader(string(body)))
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: captureOrg})
		ctx = identity.WithPermissions(ctx, []identity.Permission{identity.PermCITypeManage, identity.PermCIAttributeManage})
		mux := chi.NewRouter()
		NewHandler(repo).RegisterRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(ctx))
		if (rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity) || len(repo.added) != 0 {
			t.Errorf("POST …/items %s %s: status %d (%s); want 400/422", c.route, c.body, rec.Code, rec.Body)
		}
	}
}

// TestCaptureRefusals: the capture needs citype:manage, a draft id, a known
// draft that is not final and a repository.
func TestCaptureRefusals(t *testing.T) {
	const method, pattern = http.MethodPatch, "/api/v1/ci-types/{id}"
	body := capturePayloads[method+" "+pattern]
	cases := []struct {
		name   string
		repo   Repository
		draft  string
		perms  []identity.Permission
		status int
	}{
		{"without citype:manage", &stubDrafts{}, captureDraft, []identity.Permission{identity.PermCIRead}, http.StatusForbidden},
		{"draft no UUID", &stubDrafts{}, "abc", nil, http.StatusBadRequest},
		{"empty draft", &stubDrafts{}, "", nil, http.StatusBadRequest},
		{"unknown draft", &stubDrafts{}, "00000000-0000-4000-8000-0000000000ff", nil, http.StatusNotFound},
		{"published draft", &stubDrafts{}, finalDraft, nil, http.StatusConflict},
		{"no repository", nil, captureDraft, nil, http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		w, calls := captureCall(t, c.repo, method, pattern, capturePath(pattern)+"?draft="+c.draft, body, c.perms...)
		if w.Code != c.status || calls != 0 {
			t.Errorf("%s: status %d, handler calls %d; want %d without the handler: %s", c.name, w.Code, calls, c.status, w.Body)
		}
	}
	// An unknown template version is 404 like the resource route.
	const versionRoute = "/api/v1/templates/{id}/versions/{version}/publish"
	w, calls := captureCall(t, &stubDrafts{}, http.MethodPost, versionRoute,
		"/api/v1/templates/"+captureTargetID+"/versions/9/publish?draft="+captureDraft, "")
	if w.Code != http.StatusNotFound || calls != 0 {
		t.Errorf("unknown template version: status %d, handler calls %d; want 404", w.Code, calls)
	}
}

// TestCaptureOnlyOnCapableRoutes: a route outside DraftCapableRoutes gets
// no capture (the router refuses ?draft there, server/authz.go).
func TestCaptureOnlyOnCapableRoutes(t *testing.T) {
	w, calls := captureCall(t, &stubDrafts{}, http.MethodPost, "/api/v1/templates/{id}/migrations",
		"/api/v1/templates/"+captureTargetID+"/migrations?draft="+captureDraft, `{}`)
	if w.Code != http.StatusTeapot || calls != 1 {
		t.Errorf("exempt route: status %d, handler calls %d; want the handler", w.Code, calls)
	}
}
