package citype

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestUpsertFieldRefusesSystemTypes covers the legacy field upsert PUT
// /api/v1/ci-types/{id}/fields ("Feld hinzufügen" of the CI type admin) on
// the in-memory repository, which mirrors PGRepository.UpsertField: a
// system type (no organization) is read-only for tenants, so the answer is
// 422 on ci_type_id with the message of POST /attribute-definitions, not a
// permission error; an unknown type and a type of another organization are
// the documented 404; a type of the organization takes the field. The
// PostgreSQL side is TestAttributeDefinitionsAgainstPostgres.
func TestUpsertFieldRefusesSystemTypes(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SeedType(Type{ID: "sys-switch", Key: "switch", Name: "Switch", IsSystem: true, IsActive: true})
	repo.SeedType(Type{ID: "org-a-type", OrganizationID: "org-a", Key: "router_copy", Name: "Router copy", IsActive: true})
	repo.SeedType(Type{ID: "org-b-type", OrganizationID: "org-b", Key: "pdu_copy", Name: "PDU copy", IsActive: true})
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	put := func(typeID, body string) *httptest.ResponseRecorder {
		ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-a", UserID: "user-a"})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/ci-types/"+typeID+"/fields", strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	w := put("sys-switch", `{"name":"inventory_no","data_type":"text"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("system type: %d %s, want 422", w.Code, w.Body.String())
	}
	var problem api.FieldProblem
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Field != "ci_type_id" || problem.Detail != "ci_type_id: system ci types cannot get organization attributes" {
		t.Errorf("system type problem = %+v", problem)
	}
	if fields, _ := repo.ListFields(context.Background(), "org-a", "sys-switch"); len(fields) != 0 {
		t.Errorf("system type took fields: %+v", fields)
	}

	for name, typeID := range map[string]string{"unknown type": "no-such-type", "type of another organization": "org-b-type"} {
		if w := put(typeID, `{"name":"inventory_no","data_type":"text"}`); w.Code != http.StatusNotFound ||
			!strings.Contains(w.Body.String(), "ci type or ci not found") {
			t.Errorf("%s: %d %s, want 404", name, w.Code, w.Body.String())
		}
	}
	if fields, _ := repo.ListFields(context.Background(), "org-b", "org-b-type"); len(fields) != 0 {
		t.Errorf("type of organization B took a field of A: %+v", fields)
	}

	if w := put("org-a-type", `{"name":"inventory_no","data_type":"text"}`); w.Code != http.StatusOK {
		t.Fatalf("type of the organization: %d %s, want 200", w.Code, w.Body.String())
	}
	if fields, _ := repo.ListFields(context.Background(), "org-a", "org-a-type"); len(fields) != 1 || fields[0].Name != "inventory_no" {
		t.Errorf("fields of the organization's type = %+v", fields)
	}
}

// TestCheckAttributeTypeErrors pins the refusals the PostgreSQL checks
// build: the system-type refusal is one 422 on ci_type_id for
// /attribute-definitions and the legacy field upsert, and an unknown type
// of the legacy path is "not found", which writeFieldDefinitionError turns
// into its documented 404.
func TestCheckAttributeTypeErrors(t *testing.T) {
	var fe *api.FieldError
	if err := errSystemTypeAttribute(); !errors.As(err, &fe) || fe.Field != "ci_type_id" ||
		fe.Status != http.StatusUnprocessableEntity || fe.Message != "system ci types cannot get organization attributes" {
		t.Errorf("errSystemTypeAttribute() = %#v", err)
	}
	for name, c := range map[string]struct {
		err    error
		status int
		body   string
	}{
		"system type":  {errSystemTypeAttribute(), http.StatusUnprocessableEntity, `"field":"ci_type_id"`},
		"unknown type": {errors.New("not found"), http.StatusNotFound, "ci type or ci not found"},
	} {
		w := httptest.NewRecorder()
		writeFieldDefinitionError(w, c.err)
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.body) {
			t.Errorf("%s: %d %s, want %d with %s", name, w.Code, w.Body.String(), c.status, c.body)
		}
	}
}
