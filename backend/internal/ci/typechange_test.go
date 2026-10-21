package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestDiscoveryMayReclassify: discovery only raises generic_device to a
// specific type (CI-11).
func TestDiscoveryMayReclassify(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{GenericDeviceType, "switch", true},
		{GenericDeviceType, GenericDeviceType, false},
		{GenericDeviceType, "", false},
		{"switch", GenericDeviceType, false},
		{"switch", "router", false},
	}
	for _, c := range cases {
		if got := DiscoveryMayReclassify(c.from, c.to); got != c.want {
			t.Errorf("%s -> %s: %v", c.from, c.to, got)
		}
	}
}

// TestMissingRequired: nil and empty strings count as missing; findings are
// sorted.
func TestMissingRequired(t *testing.T) {
	got := MissingRequired([]string{"rack_units", "port_count", "vendor", "flag"},
		map[string]any{"vendor": "", "port_count": float64(48), "flag": false, "rack_units": nil})
	if len(got) != 2 || got[0].Field != "rack_units" || got[1].Field != "vendor" {
		t.Errorf("findings %+v", got)
	}
	if got := MissingRequired(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("no required fields: %+v", got)
	}
}

// TestChangeTypeHandler covers POST /cis/{id}/type (CI-11) on the memory
// repository: manual change with findings, unknown type and a type ID that
// is no UUID 422 on ci_type_id (WP-396), discovery refusal of a
// specific-to-specific change.
func TestChangeTypeHandler(t *testing.T) {
	const (
		generic = "c1000000-0000-4000-8000-0000000000a1"
		switchT = "c1000000-0000-4000-8000-0000000000a2"
		router  = "c1000000-0000-4000-8000-0000000000a3"
		unknown = "c1000000-0000-4000-8000-0000000000a4"
	)
	repo := NewMemoryRepository()
	repo.SetType(generic, GenericDeviceType)
	repo.SetType(switchT, "switch", "port_count")
	repo.SetType(router, "router")
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
	item := &Item{OrganizationID: "org-1", Name: "dev-1", CITypeID: generic}
	if err := repo.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	NewHandler(NewService(repo)).RegisterRoutes(mux)
	post := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/cis/"+item.ID+"/type", bytes.NewBufferString(body)).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := post(`{"ci_type_id":"` + switchT + `"}`)
	findings, _ := out["findings"].([]any)
	if code != http.StatusOK || out["changed"] != true || len(findings) != 1 {
		t.Fatalf("manual change: %d %v", code, out)
	}
	for body, detail := range map[string]string{
		`{"ci_type_id":"` + unknown + `"}`: "CI type not found",
		`{"ci_type_id":"server"}`:          "not the ID (UUID) of a CI type",
	} {
		code, out = post(body)
		violations, _ := out["violations"].([]any)
		var v map[string]any
		if len(violations) == 1 {
			v, _ = violations[0].(map[string]any)
		}
		if code != http.StatusUnprocessableEntity || v["field"] != "ci_type_id" || v["detail"] != detail {
			t.Errorf("%s: %d %v, want 422 on ci_type_id %q", body, code, out, detail)
		}
	}
	if got, _ := repo.GetByID(ctx, "org-1", item.ID); got.CITypeID != switchT {
		t.Errorf("type after the refused changes: %s, want %s", got.CITypeID, switchT)
	}
	if code, _ = post(`{}`); code != http.StatusBadRequest {
		t.Errorf("missing ci_type_id: %d", code)
	}
	if _, err := repo.ChangeType(ctx, "org-1", item.ID, router, TypeChangeDiscovery); !errors.Is(err, ErrReclassificationRefused) {
		t.Errorf("discovery switch -> router: %v", err)
	}
	if res, err := repo.ChangeType(ctx, "org-1", item.ID, switchT, TypeChangeManual); err != nil || res.Changed {
		t.Errorf("same type: %+v %v", res, err)
	}
}
