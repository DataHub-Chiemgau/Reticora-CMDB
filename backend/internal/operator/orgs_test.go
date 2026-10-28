package operator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
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

// TestOperatorOrgListPages covers WP-565 (API-02, F-215): GET /admin/orgs
// pages newest first on (created_at, id). ?limit=1 answers one
// organization and a next_cursor, the next page another one; reading the
// pages of 200 along next_cursor returns every organization once in that
// order. A forged cursor is 400.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOperatorOrgListPages(t *testing.T) {
	f := scopetest.Seed(t, "66")
	mux := testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, testIssuer(t), NewAuditor(f.App), f.App))
	auth := map[string]string{BreakGlassHeader: testBreakGlass}
	type page struct {
		Data []struct {
			ID        string    `json:"id"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"data"`
		Total      int    `json:"total"`
		Limit      int    `json:"limit"`
		NextCursor string `json:"next_cursor"`
	}
	list := func(query string) page {
		t.Helper()
		w := get(mux, "/api/v1/admin/orgs"+query, auth)
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); w.Code != http.StatusOK || err != nil {
			t.Fatalf("GET /admin/orgs%s: %d %.300s", query, w.Code, w.Body.String())
		}
		return p
	}

	first := list("?limit=1")
	if len(first.Data) != 1 || first.Limit != 1 || first.NextCursor == "" || first.Total < 2 {
		t.Fatalf("limit=1: %d entries, limit %d, total %d, next_cursor %q; want one organization and a cursor",
			len(first.Data), first.Limit, first.Total, first.NextCursor)
	}
	second := list("?limit=1&cursor=" + url.QueryEscape(first.NextCursor))
	if len(second.Data) != 1 || second.Data[0].ID == first.Data[0].ID {
		t.Errorf("second page of limit=1: %+v after %+v; want another organization", second.Data, first.Data)
	}

	seen := map[string]bool{}
	var prev *time.Time
	var prevID string
	for query := "?limit=200"; query != ""; {
		p := list(query)
		if len(p.Data) > 200 {
			t.Fatalf("page of %d organizations, want at most 200", len(p.Data))
		}
		for _, o := range p.Data {
			if seen[o.ID] {
				t.Errorf("organization %s returned twice", o.ID)
			}
			seen[o.ID] = true
			if prev != nil && (o.CreatedAt.After(*prev) || (o.CreatedAt.Equal(*prev) && o.ID > prevID)) {
				t.Errorf("organization %s %s does not follow %s %s in the order created_at desc, id desc", o.CreatedAt, o.ID, *prev, prevID)
			}
			at := o.CreatedAt
			prev, prevID = &at, o.ID
		}
		query = ""
		if p.NextCursor != "" {
			query = "?limit=200&cursor=" + url.QueryEscape(p.NextCursor)
		}
	}
	if !seen[f.OrgA] || !seen[f.OrgB] {
		t.Errorf("the pages miss organization A (%v) or B (%v)", seen[f.OrgA], seen[f.OrgB])
	}
	if w := get(mux, "/api/v1/admin/orgs?cursor=eyJpIjoiY2ktOSJ9.AAAAAAAAAAAAAAAAAAAAAA", auth); w.Code != http.StatusBadRequest {
		t.Errorf("forged cursor: %d %.200s, want 400", w.Code, w.Body.String())
	}
}
