package operator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// fakeCreator records the creation it was asked for.
type fakeCreator struct {
	op  Identity
	req *NewOrganization
	err error
}

func (f *fakeCreator) CreateOrganization(_ context.Context, op Identity, req *NewOrganization) (CreatedOrganization, error) {
	f.op, f.req = op, req
	if f.err != nil {
		return CreatedOrganization{}, f.err
	}
	return CreatedOrganization{ID: "c0de66a0-0000-4000-8000-0000000000aa", Name: req.Name, Slug: "acme", Plan: req.Plan}, nil
}

// TestOperatorCreatesOrganizations covers WP-113 (TLC-02) at the HTTP
// boundary: only operators reach POST /admin/orgs; plan, limits and the
// first org_admin are handed to the creation chain (internal/signup, whose
// integration test proves the database side) together with the operator;
// errors answer RFC 7807 with the field.
func TestOperatorCreatesOrganizations(t *testing.T) {
	creator := &fakeCreator{}
	mux := testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, testIssuer(t), nil, nil).WithOrgCreator(creator))
	auth := map[string]string{BreakGlassHeader: testBreakGlass}
	body := `{"name":"Acme","plan":"pro","limits":{"max_cis":5000},"admin_email":"admin@acme.example"}`

	if w := post(mux, "/api/v1/admin/orgs", body, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d, want 401", w.Code)
	}
	if creator.req != nil {
		t.Fatal("anonymous request reached the creation")
	}

	w := post(mux, "/api/v1/admin/orgs", body, auth)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created CreatedOrganization
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Slug != "acme" || created.Plan != "pro" {
		t.Fatalf("created = %+v (%v)", created, err)
	}
	if creator.op.Kind != KindBreakGlass || creator.req.Limits["max_cis"] != 5000 || creator.req.AdminEmail != "admin@acme.example" {
		t.Errorf("creation got operator %+v request %+v", creator.op, creator.req)
	}

	if w := post(mux, "/api/v1/admin/orgs", `{"name":"Acme","unknown":1}`, auth); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d, want 400", w.Code)
	}

	for name, tc := range map[string]struct {
		err    error
		status int
		field  string
	}{
		"invalid plan": {&api.FieldError{Field: "plan", Message: "bad"}, http.StatusBadRequest, "plan"},
		"slug taken":   {ErrSlugTaken, http.StatusConflict, "slug"},
		"no database":  {ErrCreationUnavailable, http.StatusServiceUnavailable, ""},
		"store failed": {errors.New("boom"), http.StatusInternalServerError, ""},
	} {
		creator.err = tc.err
		w := post(mux, "/api/v1/admin/orgs", body, auth)
		var pd struct {
			Status int    `json:"status"`
			Field  string `json:"field"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &pd)
		if w.Code != tc.status || pd.Status != tc.status || pd.Field != tc.field {
			t.Errorf("%s: %d %s, want %d field %q", name, w.Code, w.Body.String(), tc.status, tc.field)
		}
	}

	unconfigured := testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, testIssuer(t), nil, nil))
	if w := post(unconfigured, "/api/v1/admin/orgs", body, auth); w.Code != http.StatusServiceUnavailable {
		t.Errorf("without creator: %d, want 503", w.Code)
	}
}
