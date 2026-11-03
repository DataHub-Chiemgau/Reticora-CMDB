package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// profileStack serves the profile routes over one repository for two
// organizations; ctxFor returns the request context of a principal of an
// organization (with the tenant scope the PostgreSQL repository needs).
type profileStack struct {
	mux    chi.Router
	orgA   string
	orgB   string
	ctxFor func(orgID string) context.Context
}

func memoryStack() *profileStack {
	mux := chi.NewRouter()
	NewHandler(NewMemoryRepository()).RegisterRoutes(mux)
	return &profileStack{mux: mux, orgA: "org-a", orgB: "org-b", ctxFor: func(string) context.Context { return context.Background() }}
}

func (s *profileStack) do(t *testing.T, orgID, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	ctx := tenant.WithTenant(s.ctxFor(orgID), tenant.TenantInfo{OrganizationID: orgID, UserID: "00000000-0000-4000-8000-0000000000f1"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	return w
}

// validProfile is a profile body; name and the extra JSON members
// (",\"key\":value") vary per case.
func validProfile(name, extra string) string {
	return `{"name":"` + name + `","entity":"ci","format":"csv","mode":"both",
		"mapping":{"columns":{"Hostname":{"target":"name","transforms":[{"op":"trim"}]},
		"Serial":{"target":"attributes.serial","transforms":[{"op":"trim"},{"op":"enum_map","values":{"n/a":""}}]},
		"Bought":{"target":"attributes.purchase_date","transforms":[{"op":"date_format","layout":"02.01.2006"}]},
		"Tags":{"target":"attributes.tags","transforms":[{"op":"split","separator":";"}]},
		"Source":{"target":"attributes.source","transforms":[{"op":"constant","value":"import"}]}},
		"defaults":{"attributes.environment":"production"}},
		"identity_keys":["name","attributes.serial"]` + extra + `}`
}

func decodeProfile(t *testing.T, w *httptest.ResponseRecorder) Profile {
	t.Helper()
	var p Profile
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode profile: %v: %s", err, w.Body.String())
	}
	return p
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int, what string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("%s: status %d, want %d: %s", what, w.Code, status, w.Body.String())
	}
}

// runProfileCRUD is the CRUD set of WP-774 against one repository.
func runProfileCRUD(t *testing.T, s *profileStack) {
	const base = "/api/v1/import-profiles"
	w := s.do(t, s.orgA, http.MethodPost, base, validProfile("CMDB servers", `,"description":"Server list"`))
	expect(t, w, http.StatusCreated, "create")
	created := decodeProfile(t, w)
	if created.ID == "" || created.OrganizationID != s.orgA || created.Mode != "both" || len(created.Mapping.Columns) != 5 ||
		created.Mapping.Columns["Serial"].Transforms[1].Values["n/a"] != "" || created.Mapping.Defaults["attributes.environment"] != "production" ||
		len(created.IdentityKeys) != 2 || created.Description == nil || *created.Description != "Server list" || created.Sheet != nil {
		t.Fatalf("created profile %+v", created)
	}

	w = s.do(t, s.orgA, http.MethodGet, base+"/"+created.ID, "")
	expect(t, w, http.StatusOK, "get")
	if got := decodeProfile(t, w); got.Name != "CMDB servers" || got.Mapping.Columns["Bought"].Transforms[0].Layout != "02.01.2006" {
		t.Fatalf("get profile %+v", got)
	}

	// A duplicate name in the same organization is 409; the same name in
	// organization B is a new profile.
	expect(t, s.do(t, s.orgA, http.MethodPost, base, validProfile("CMDB servers", "")), http.StatusConflict, "duplicate name")
	w = s.do(t, s.orgB, http.MethodPost, base, validProfile("CMDB servers", ""))
	expect(t, w, http.StatusCreated, "same name in org B")
	other := decodeProfile(t, w)

	// The profile of organization A is not found from organization B.
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		body := ""
		if method == http.MethodPatch {
			body = `{"name":"stolen"}`
		}
		expect(t, s.do(t, s.orgB, method, base+"/"+created.ID, body), http.StatusNotFound, "org B "+method)
	}
	expect(t, s.do(t, s.orgA, http.MethodGet, base+"/"+other.ID, ""), http.StatusNotFound, "org A reads org B")
	expect(t, s.do(t, s.orgA, http.MethodGet, base+"/00000000-0000-4000-8000-00000000ffff", ""), http.StatusNotFound, "unknown id")

	// PATCH: sent fields change, null clears, the merged profile is valid.
	w = s.do(t, s.orgA, http.MethodPatch, base+"/"+created.ID, `{"format":"xlsx","sheet":"Servers","description":null,"mode":"update_existing"}`)
	expect(t, w, http.StatusOK, "patch")
	if got := decodeProfile(t, w); got.Format != "xlsx" || got.Sheet == nil || *got.Sheet != "Servers" || got.Description != nil ||
		got.Mode != "update_existing" || got.Name != "CMDB servers" || len(got.Mapping.Columns) != 5 {
		t.Fatalf("patched profile %+v", got)
	}
	// Leaving xlsx keeps the sheet and is refused; clearing it as well works.
	expectField(t, s.do(t, s.orgA, http.MethodPatch, base+"/"+created.ID, `{"format":"csv"}`), "sheet")
	expect(t, s.do(t, s.orgA, http.MethodPatch, base+"/"+created.ID, `{"format":"csv","sheet":null}`), http.StatusOK, "patch back to csv")
	expectField(t, s.do(t, s.orgA, http.MethodPatch, base+"/"+created.ID, `{"identity_keys":["attributes.unknown"]}`), "identity_keys[0]")
	w = s.do(t, s.orgA, http.MethodPost, base, validProfile("Second", ""))
	expect(t, w, http.StatusCreated, "second profile")
	second := decodeProfile(t, w)
	expect(t, s.do(t, s.orgA, http.MethodPatch, base+"/"+second.ID, `{"name":"CMDB servers"}`), http.StatusConflict, "rename onto a taken name")

	// Validation: 422 with the field.
	for _, tc := range []struct{ body, field string }{
		{`{"entity":"ci","format":"csv","mode":"both","mapping":{"columns":{"A":{"target":"name"}}}}`, "name"},
		{strings.Replace(validProfile("v1", ""), `"entity":"ci"`, `"entity":"vehicle"`, 1), "entity"},
		{strings.Replace(validProfile("v2", ""), `"format":"csv"`, `"format":"pdf"`, 1), "format"},
		{strings.Replace(validProfile("v3", ""), `"mode":"both"`, `"mode":"replace"`, 1), "mode"},
		{strings.Replace(validProfile("v4", ""), `{"op":"split","separator":";"}`, `{"op":"regex"}`, 1), "mapping.columns.Tags.transforms[0].op"},
		{strings.Replace(validProfile("v5", ""), `{"op":"split","separator":";"}`, `{"op":"split"}`, 1), "mapping.columns.Tags.transforms[0].separator"},
		{strings.Replace(validProfile("v6", ""), `{"op":"constant","value":"import"}`, `{"op":"constant"}`, 1), "mapping.columns.Source.transforms[0].value"},
		{strings.Replace(validProfile("v7", ""), `"identity_keys":["name","attributes.serial"]`, `"identity_keys":["attributes.asset_tag"]`, 1), "identity_keys[0]"},
		{strings.Replace(validProfile("v8", ""), `"identity_keys":["name","attributes.serial"]`, `"identity_keys":["name","name"]`, 1), "identity_keys[1]"},
		{validProfile("v9", `,"sheet":"Tabelle1"`), "sheet"},
		{`{"name":"v10","entity":"ci","format":"csv","mode":"both","mapping":{"columns":{}}}`, "mapping.columns"},
		{`{"name":"v11","entity":"ci","format":"csv","mode":"both","mapping":{"columns":{"A":{"target":"bad target"}}}}`, "mapping.columns.A.target"},
	} {
		expectField(t, s.do(t, s.orgA, http.MethodPost, base, tc.body), tc.field)
	}
	expect(t, s.do(t, s.orgA, http.MethodPost, base, `{"name":`), http.StatusBadRequest, "malformed body")
	expect(t, s.do(t, s.orgA, http.MethodPost, base, `{"name":"x","unknown":1}`), http.StatusBadRequest, "unknown member")

	// DELETE answers 204; afterwards the profile is gone.
	expect(t, s.do(t, s.orgA, http.MethodDelete, base+"/"+second.ID, ""), http.StatusNoContent, "delete")
	expect(t, s.do(t, s.orgA, http.MethodGet, base+"/"+second.ID, ""), http.StatusNotFound, "get deleted")
	expect(t, s.do(t, s.orgA, http.MethodDelete, base+"/"+second.ID, ""), http.StatusNotFound, "delete twice")
}

// runProfileList pages through five profiles with limit, cursor and
// next_cursor; a forged cursor is 400, organization B sees only its own.
func runProfileList(t *testing.T, s *profileStack) {
	const base = "/api/v1/import-profiles"
	for i := 5; i >= 1; i-- {
		expect(t, s.do(t, s.orgA, http.MethodPost, base, validProfile(fmt.Sprintf("list-%d", i), "")), http.StatusCreated, "create list profile")
	}
	var names []string
	path := base + "?limit=2"
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("the list does not end")
		}
		w := s.do(t, s.orgA, http.MethodGet, path, "")
		expect(t, w, http.StatusOK, "list")
		var page ProfileListResponse
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Data) > 2 || page.Total != 5 || page.Limit != 2 {
			t.Fatalf("page %s", w.Body.String())
		}
		for i := range page.Data {
			names = append(names, page.Data[i].Name)
		}
		if page.NextCursor == "" {
			if page.HasMore {
				t.Fatalf("has_more without next_cursor: %s", w.Body.String())
			}
			break
		}
		path = base + "?limit=2&cursor=" + page.NextCursor
	}
	if strings.Join(names, ",") != "list-1,list-2,list-3,list-4,list-5" {
		t.Fatalf("listed %v", names)
	}
	expect(t, s.do(t, s.orgA, http.MethodGet, base+"?cursor=forged.AAAA", ""), http.StatusBadRequest, "forged cursor")
	w := s.do(t, s.orgB, http.MethodGet, base, "")
	expect(t, w, http.StatusOK, "list org B")
	var page ProfileListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(page.Data) != 0 {
		t.Fatalf("org B sees %s", w.Body.String())
	}
}

func expectField(t *testing.T, w *httptest.ResponseRecorder, field string) {
	t.Helper()
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("field %s: status %d, want 422: %s", field, w.Code, w.Body.String())
	}
	var problem struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.Field != field {
		t.Fatalf("field %s: problem %s", field, w.Body.String())
	}
}

// TestImportProfileCRUDMemory covers WP-774 (IMP-IO-01, API-06) against
// the in-memory repository of the --no-db profile.
func TestImportProfileCRUDMemory(t *testing.T) {
	runProfileCRUD(t, memoryStack())
	runProfileList(t, memoryStack())
}

// TestImportProfileCRUDPostgres runs the same set against PGRepository
// under RLS (TEST_DATABASE_URL; skipped without it).
func TestImportProfileCRUDPostgres(t *testing.T) {
	f := scopetest.Seed(t, "05")
	stack := func() *profileStack {
		mux := chi.NewRouter()
		NewHandler(NewPGRepository(f.App)).RegisterRoutes(mux)
		return &profileStack{mux: mux, orgA: f.OrgA, orgB: f.OrgB, ctxFor: f.OrgCtx}
	}
	runProfileCRUD(t, stack())
	if _, err := f.Admin.Exec(context.Background(), `DELETE FROM import_profile WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB); err != nil {
		t.Fatal(err)
	}
	runProfileList(t, stack())

	// RLS: a transaction of organization B neither sees nor inserts rows
	// of organization A.
	var visible int
	err := database.WithRequestTenant(f.OrgCtx(f.OrgB), f.App, f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COUNT(*) FROM import_profile WHERE organization_id = $1`, f.OrgA).Scan(&visible)
	})
	if err != nil || visible != 0 {
		t.Fatalf("org B sees %d profiles of org A (%v)", visible, err)
	}
}

// TestImportRoutesRequireTheImportFeature: every operation under
// /api/v1/import-profiles and /api/v1/imports is gated by the feature key
// import (E-44, WP-705).
func TestImportRoutesRequireTheImportFeature(t *testing.T) {
	for _, op := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/import-profiles"},
		{http.MethodPost, "/api/v1/import-profiles"},
		{http.MethodGet, "/api/v1/import-profiles/00000000-0000-4000-8000-000000000001"},
		{http.MethodPatch, "/api/v1/import-profiles/00000000-0000-4000-8000-000000000001"},
		{http.MethodDelete, "/api/v1/import-profiles/00000000-0000-4000-8000-000000000001"},
		{http.MethodPost, "/api/v1/imports"},
		{http.MethodPost, "/api/v1/imports/preview"},
		{http.MethodGet, "/api/v1/imports/00000000-0000-4000-8000-000000000001"},
		{http.MethodGet, "/api/v1/imports/00000000-0000-4000-8000-000000000001/report"},
	} {
		if key, ok := entitlement.RequiredFeature(op.method, op.path); !ok || key != entitlement.FeatureImport {
			t.Errorf("%s %s: feature %q (%v), want import", op.method, op.path, key, ok)
		}
	}
}
