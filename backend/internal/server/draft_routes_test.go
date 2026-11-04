package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/metamodel"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
)

// draftPrefixes are the prefixes of the metamodel resource routes (WP-720,
// MET-53): every write route below them is captured into a draft with
// ?draft or exempt with a reason.
var draftPrefixes = []string{
	"/api/v1/ci-types", "/api/v1/ci-fields", "/api/v1/attribute-definitions", "/api/v1/attribute-sets",
	"/api/v1/templates", "/api/v1/rules", "/api/v1/layouts",
}

func underDraftPrefix(path string) bool {
	for _, prefix := range draftPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// TestDraftRoutesCoverTheMetamodelWrites is the architecture rule of
// WP-720: the write routes below the metamodel prefixes and
// DraftCapableRoutes ∪ DraftExemptRoutes are the same set (a new write
// route needs an entry), each entry is registered, and a capable route
// needs the permission of its item.
func TestDraftRoutesCoverTheMetamodelWrites(t *testing.T) {
	registered := map[string]bool{}
	for _, op := range routeTable(t, testRouter(t)) {
		key := op.Method + " " + op.Path
		registered[key] = true
		if !isWriteMethod(op.Method) || !underDraftPrefix(op.Path) {
			continue
		}
		_, capable := metamodel.DraftCapableRoutes[key]
		_, exempt := metamodel.DraftExemptRoutes[key]
		if capable == exempt {
			t.Errorf("%s: capable %v, exempt %v; a metamodel write route is in exactly one of DraftCapableRoutes and DraftExemptRoutes",
				key, capable, exempt)
		}
	}
	for key, ri := range metamodel.DraftCapableRoutes {
		if !registered[key] {
			t.Errorf("DraftCapableRoutes: %s is not registered", key)
		}
		method, path, _ := strings.Cut(key, " ")
		if !underDraftPrefix(path) {
			t.Errorf("DraftCapableRoutes: %s lies outside the metamodel prefixes", key)
		}
		if got, _ := PermissionForRoute(method, path); got != metamodel.ItemPermission(ri.Kind, ri.Op) {
			t.Errorf("%s: route permission %q, item permission %q", key, got, metamodel.ItemPermission(ri.Kind, ri.Op))
		}
	}
	for key, reason := range metamodel.DraftExemptRoutes {
		if !registered[key] {
			t.Errorf("DraftExemptRoutes: %s is not registered", key)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("DraftExemptRoutes: %s has no reason", key)
		}
	}
}

// chainDrafts is the draft repository of the chain test: the capture of a
// template version route resolves the version first, which records the
// link and whether the audit middleware ran before it.
type chainDrafts struct {
	metamodel.Repository
	trace *[]string
}

func (c chainDrafts) TemplateVersionID(ctx context.Context, _, _, _ string) (string, error) {
	if _, audited := audit.RequestInfoFrom(ctx); audited {
		*c.trace = append(*c.trace, "audit")
	}
	*c.trace = append(*c.trace, "capture")
	return "", metamodel.ErrTargetNotFound
}

// TestDraftCaptureRunsInnermost: the capture runs behind authorization,
// validation, idempotency and the audit middleware (WP-720, pattern of
// TestRouteMiddlewareRunsAfterAuthorization); the resource handler does
// not run, and a forbidden request reaches neither.
func TestDraftCaptureRunsInnermost(t *testing.T) {
	var trace []string
	step := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				trace = append(trace, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	mux := chi.NewRouter()
	router := &authorizingRouter{Router: mux, after: []func(http.Handler) http.Handler{step("validation"), step("idempotency")},
		drafts: chainDrafts{trace: &trace}}
	router.Post("/api/v1/templates/{id}/versions/{version}/publish", func(w http.ResponseWriter, _ *http.Request) {
		trace = append(trace, "handler")
		w.WriteHeader(http.StatusOK)
	})
	call := func(perms ...identity.Permission) int {
		req := withPrincipal(httptest.NewRequest(http.MethodPost,
			"/api/v1/templates/00000000-0000-4000-8000-000000000001/versions/2/publish?draft=00000000-0000-4000-8000-000000000002", nil),
			identity.Principal{Subject: "u", OrganizationID: "org", Permissions: perms, Type: identity.PrincipalTypeUser})
		req = req.WithContext(identity.WithPermissions(req.Context(), perms))
		w := httptest.NewRecorder()
		middleware.TenantMiddleware(mux).ServeHTTP(w, req)
		return w.Code
	}
	if code := call(identity.PermCITypeManage); code != http.StatusForbidden || len(trace) != 0 {
		t.Fatalf("forbidden request: status %d, trace %v; want 403 before every link", code, trace)
	}
	if code := call(identity.PermMetamodelPublish, identity.PermCITypeManage); code != http.StatusNotFound ||
		strings.Join(trace, ",") != "validation,idempotency,audit,capture" {
		t.Errorf("captured request: status %d, trace %v; want validation, idempotency, audit, capture and no handler", code, trace)
	}
}

// TestDraftParameterFailsClosed: ?draft at a write route outside
// DraftCapableRoutes is 400 draft-unsupported; in the G1 release (module
// metamodel off) the capable G1 routes and the form preview answer ?draft
// with 404 and write nothing. Without ?draft the routes run unchanged.
func TestDraftParameterFailsClosed(t *testing.T) {
	const draft = "?draft=00000000-0000-4000-8000-000000000002"
	call := func(h http.Handler, method, target, body string) (int, map[string]any) {
		t.Helper()
		p := identity.Principal{Subject: "user-1", OrganizationID: "org-1", Permissions: identity.AllPermissions(),
			Type: identity.PrincipalTypeUser}
		req := withPrincipal(httptest.NewRequest(method, target, strings.NewReader(body)), p)
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(identity.WithPermissions(req.Context(), p.Permissions))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	all := middleware.TenantMiddleware(testRouter(t))
	if code, body := call(all, http.MethodPost, "/api/v1/cis"+draft, `{}`); code != http.StatusBadRequest ||
		body["type"] != "urn:reticora:problem:draft-unsupported" {
		t.Errorf("POST /api/v1/cis?draft: %d %v; want 400 draft-unsupported", code, body)
	}
	for _, exempt := range []string{"/api/v1/templates/00000000-0000-4000-8000-000000000001/migrations",
		"/api/v1/attribute-definitions/00000000-0000-4000-8000-000000000001/reindex"} {
		if code, body := call(all, http.MethodPost, exempt+draft, `{}`); code != http.StatusBadRequest ||
			body["type"] != "urn:reticora:problem:draft-unsupported" {
			t.Errorf("POST %s?draft: %d %v; want 400 draft-unsupported", exempt, code, body)
		}
	}

	g1, err := ParseModules("")
	if err != nil {
		t.Fatal(err)
	}
	h := middleware.TenantMiddleware(testRouterWithModules(t, &audit.Handler{}, g1))
	code, created := call(h, http.MethodPost, "/api/v1/ci-types", `{"key":"draft_probe","name":"Draft probe","description":"before"}`)
	if code != http.StatusCreated {
		t.Fatalf("create type: %d %v", code, created)
	}
	id, _ := created["id"].(string)
	code, body := call(h, http.MethodPatch, "/api/v1/ci-types/"+id+draft, `{"description":"after"}`)
	if code != http.StatusNotFound || body["type"] != "urn:reticora:problem:not-found" || body["detail"] != "Modul metamodel abgeschaltet" {
		t.Errorf("G1 PATCH ?draft: %d %v; want 404 module off", code, body)
	}
	if status, got := call(h, http.MethodGet, "/api/v1/ci-types/"+id, ""); status != http.StatusOK || got["description"] != "before" {
		t.Errorf("G1 PATCH ?draft ran the handler: %d %v", status, got)
	}
	if code, body = call(h, http.MethodGet, "/api/v1/ci-types/"+id+"/form"+draft, ""); code != http.StatusNotFound ||
		body["detail"] != "Modul metamodel abgeschaltet" {
		t.Errorf("G1 GET form ?draft: %d %v; want 404 module off", code, body)
	}
	if code, body = call(h, http.MethodPost, "/api/v1/cis"+draft, `{}`); code != http.StatusBadRequest ||
		body["type"] != "urn:reticora:problem:draft-unsupported" {
		t.Errorf("G1 POST /api/v1/cis?draft: %d %v; want 400 draft-unsupported", code, body)
	}
	if code, body = call(h, http.MethodPatch, "/api/v1/ci-types/"+id, `{"description":"after"}`); code != http.StatusOK ||
		body["description"] != "after" {
		t.Errorf("G1 PATCH without draft: %d %v; want the change", code, body)
	}
}
