package locations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestHandlerTreeAndFieldProblems covers WP-053 (LOC-10, LOC-07): the API
// writes the canonical tree, the tree endpoint returns every node, and
// violations of the parent matrix are RFC 7807 problems naming the field.
func TestHandlerTreeAndFieldProblems(t *testing.T) {
	mux := chi.NewRouter()
	NewHandler(NewMemoryRepository()).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	create := func(body string) Location {
		t.Helper()
		w := call(http.MethodPost, "/api/v1/locations", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: status %d %s", body, w.Code, w.Body.String())
		}
		var loc Location
		if err := json.Unmarshal(w.Body.Bytes(), &loc); err != nil {
			t.Fatal(err)
		}
		return loc
	}
	problemField := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		if w.Code != http.StatusUnprocessableEntity || w.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("status %d type %q body %s, want a 422 problem", w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
		var p struct {
			Type       string `json:"type"`
			Violations []struct {
				Field  string `json:"field"`
				Detail string `json:"detail"`
			} `json:"violations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || len(p.Violations) != 1 || p.Violations[0].Detail == "" || p.Type == "" {
			t.Fatalf("problem %s: %v", w.Body.String(), err)
		}
		return p.Violations[0].Field
	}

	site := create(`{"kind":"site","client_id":"client-1","name":"HQ"}`)
	building := create(`{"kind":"building","parent_id":"` + site.ID + `","name":"B1"}`)
	room := create(`{"kind":"room","parent_id":"` + building.ID + `","name":"R1"}`)
	create(`{"kind":"rack","parent_id":"` + room.ID + `","name":"K1"}`)
	wh := create(`{"kind":"warehouse","parent_id":"` + site.ID + `","name":"W1"}`)
	if room.SiteID != site.ID || room.ClientID != "client-1" || room.Path != site.Path+"."+label(building.ID)+"."+label(room.ID) {
		t.Errorf("room derivation: %+v", room)
	}

	// Field problems for the parent matrix and the request fields.
	for body, field := range map[string]string{
		`{"kind":"site","name":"no client"}`:                                    "client_id",
		`{"kind":"floor","parent_id":"` + site.ID + `","name":"F"}`:             "kind",
		`{"kind":"building","name":"orphan"}`:                                   "parent_id",
		`{"kind":"building","parent_id":"` + wh.ID + `","name":"in warehouse"}`: "parent_id",
		`{"kind":"zone","parent_id":"` + wh.ID + `","name":""}`:                 "name",
	} {
		if got := problemField(call(http.MethodPost, "/api/v1/locations", body)); got != field {
			t.Errorf("create %s: field %q, want %q", body, got, field)
		}
	}
	if got := problemField(call(http.MethodPatch, "/api/v1/locations/"+room.ID, `{"parent_id":"`+wh.ID+`"}`)); got != "parent_id" {
		t.Errorf("move room below warehouse: field %q", got)
	}
	if got := problemField(call(http.MethodGet, "/api/v1/locations?kind=desk", "")); got != "kind" {
		t.Errorf("list unknown kind: field %q", got)
	}

	// Rename and move.
	b2 := create(`{"kind":"building","parent_id":"` + site.ID + `","name":"B2"}`)
	if w := call(http.MethodPatch, "/api/v1/locations/"+room.ID, `{"name":"R1-new","parent_id":"`+b2.ID+`"}`); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"R1-new"`) || !strings.Contains(w.Body.String(), label(b2.ID)) {
		t.Errorf("rename and move: %d %s", w.Code, w.Body.String())
	}

	// The tree holds every node with its children.
	w := call(http.MethodGet, "/api/v1/locations/tree", "")
	var tree struct {
		Data []*TreeNode `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tree); err != nil || w.Code != http.StatusOK {
		t.Fatalf("tree: %d %v", w.Code, err)
	}
	if len(tree.Data) != 1 || count(tree.Data) != 6 {
		t.Fatalf("tree: %d roots %d nodes, want 1 and 6: %s", len(tree.Data), count(tree.Data), w.Body.String())
	}
	if chain(tree.Data[0].Children[1]) != b2.ID+"/"+room.ID+"/"+tree.Data[0].Children[1].Children[0].Children[0].ID {
		t.Errorf("moved room or its rack missing below B2: %s", w.Body.String())
	}

	// Listing filters.
	var list struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(call(http.MethodGet, "/api/v1/locations?parent_id="+site.ID, "").Body.Bytes(), &list)
	if list.Total != 3 {
		t.Errorf("children of the site: %d, want 3", list.Total)
	}

	// A node with children is not deleted; a leaf is; an unknown one is 404.
	if w = call(http.MethodDelete, "/api/v1/locations/"+site.ID, ""); w.Code != http.StatusConflict {
		t.Errorf("delete site with children: %d", w.Code)
	}
	if w = call(http.MethodDelete, "/api/v1/locations/"+building.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("delete empty building: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodGet, "/api/v1/locations/"+building.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("get deleted building: %d", w.Code)
	}
}

// TestHandlerBarcodeConflictNamesField covers WP-289 (INV-03) in the memory
// profile: PATCH /api/v1/locations/{id} assigns and removes the barcode, and
// a code another location of the organization holds is a 409 problem with
// field barcode that does not name that location; another organization may
// use the same code.
func TestHandlerBarcodeConflictNamesField(t *testing.T) {
	mux := chi.NewRouter()
	NewHandler(NewMemoryRepository()).RegisterRoutes(mux)
	call := func(org, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: org}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	create := func(org, body string) Location {
		t.Helper()
		w := call(org, http.MethodPost, "/api/v1/locations", body)
		var loc Location
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &loc) != nil {
			t.Fatalf("create %s: status %d %s", body, w.Code, w.Body.String())
		}
		return loc
	}
	barcodeOf := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		var loc Location
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &loc) != nil {
			t.Fatalf("status %d %s, want 200", w.Code, w.Body.String())
		}
		return loc.Barcode
	}
	site := create("org-1", `{"kind":"site","client_id":"client-1","name":"HQ"}`)
	wh := create("org-1", `{"kind":"warehouse","parent_id":"`+site.ID+`","name":"W1"}`)
	other := create("org-2", `{"kind":"site","client_id":"client-2","name":"HQ"}`)

	if got := barcodeOf(call("org-1", http.MethodPatch, "/api/v1/locations/"+site.ID, `{"barcode":"LOC-1"}`)); got != "LOC-1" {
		t.Errorf("assign: barcode %q, want LOC-1", got)
	}
	if got := barcodeOf(call("org-1", http.MethodPatch, "/api/v1/locations/"+site.ID, `{"barcode":"LOC-1"}`)); got != "LOC-1" {
		t.Errorf("assign the own code again: barcode %q, want LOC-1", got)
	}
	w := call("org-1", http.MethodPatch, "/api/v1/locations/"+wh.ID, `{"barcode":"LOC-1"}`)
	var p struct {
		Status int    `json:"status"`
		Field  string `json:"field"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != http.StatusConflict ||
		w.Header().Get("Content-Type") != "application/problem+json" || p.Status != http.StatusConflict || p.Field != "barcode" {
		t.Fatalf("duplicate: %d %q %s (%v), want a 409 problem with field barcode", w.Code, w.Header().Get("Content-Type"), w.Body.String(), err)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for key, value := range body {
		if value == site.ID || value == site.Name {
			t.Errorf("duplicate names the location holding the code in %q: %s", key, w.Body.String())
		}
	}
	if got := barcodeOf(call("org-2", http.MethodPatch, "/api/v1/locations/"+other.ID, `{"barcode":"LOC-1"}`)); got != "LOC-1" {
		t.Errorf("other organization: barcode %q, want LOC-1", got)
	}
	if got := barcodeOf(call("org-1", http.MethodPatch, "/api/v1/locations/"+site.ID, `{"barcode":""}`)); got != "" {
		t.Errorf("remove: barcode %q, want none", got)
	}
	if got := barcodeOf(call("org-1", http.MethodPatch, "/api/v1/locations/"+wh.ID, `{"barcode":"LOC-1"}`)); got != "LOC-1" {
		t.Errorf("assign the released code: barcode %q, want LOC-1", got)
	}
}
