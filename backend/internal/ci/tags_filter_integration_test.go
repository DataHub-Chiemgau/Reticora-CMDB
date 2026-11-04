// filter[tags] of the CI list (CI-08, API-02, E-126; WP-754) against
// PostgreSQL and the memory repository: in finds the CIs with at least one
// of the tags, all those with every one, filter[tags]=k is in with one key;
// total counts every matching CI; an unknown key matches nothing; all on
// another field and like, gte and lte on tags are 400.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestCITagsFilter' ./internal/ci/
package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// checkTagsFilter seeds four CIs through POST /cis and checks the tag
// filters of GET /cis against them.
func checkTagsFilter(t *testing.T, ctx context.Context, repo ci.Repository, typeID, clientID string) {
	t.Helper()
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	names := map[string]string{}
	create := func(name string, tags ...string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"name": name, "ci_type_id": typeID, "client_id": clientID,
			"attributes": map[string]any{"tags": tags}})
		w := call(http.MethodPost, "/api/v1/cis", string(raw))
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
		}
		var item ci.Item
		if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		names[item.ID] = name
	}
	create("tf-a", "Web", "Prod")
	create("tf-b", "web")
	create("tf-c", "prod", "dmz")
	create("tf-d")

	list := func(query string, limit int) (found []string, total int) {
		t.Helper()
		w := call(http.MethodGet, "/api/v1/cis?sort=name&limit="+strconv.Itoa(limit)+"&"+query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
		var page struct {
			Data  []ci.Item `json:"data"`
			Total int       `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Data {
			found = append(found, names[item.ID])
		}
		return found, page.Total
	}
	q := func(key, value string) string { return url.Values{key: {value}}.Encode() }
	for _, c := range []struct {
		query string
		want  []string
	}{
		{q("filter[tags][in]", "web,prod"), []string{"tf-a", "tf-b", "tf-c"}},
		{q("filter[tags][in]", "web,web"), []string{"tf-a", "tf-b"}},
		{q("filter[tags][all]", "web,prod"), []string{"tf-a"}},
		{q("filter[tags][all]", "prod,prod"), []string{"tf-a", "tf-c"}},
		{q("filter[tags]", "dmz"), []string{"tf-c"}},
		{q("filter[tags][exact]", "web"), []string{"tf-a", "tf-b"}},
		{q("filter[tags]", "nope"), nil},
		{q("filter[tags][in]", "dmz,nope"), []string{"tf-c"}},
		{q("filter[tags][all]", "dmz,nope"), nil},
		{q("filter[tags][in]", "web") + "&" + q("filter[tags][all]", "prod"), []string{"tf-a"}},
		{q("filter[tags][in]", "prod") + "&" + q("filter[name][like]", "tf-c"), []string{"tf-c"}},
	} {
		found, total := list(c.query, 9)
		if !slices.Equal(found, c.want) || total != len(c.want) {
			t.Errorf("%s: found %v (total %d), want %v", c.query, found, total, c.want)
		}
	}
	// total counts every matching CI, also beyond the page.
	if found, total := list(q("filter[tags][in]", "web,prod"), 1); !slices.Equal(found, []string{"tf-a"}) || total != 3 {
		t.Errorf("page of one: found %v, total %d, want [tf-a] and 3", found, total)
	}
	for _, query := range []string{
		q("filter[name][all]", "tf-a"),
		q("filter[tags][like]", "web"),
		q("filter[tags][gte]", "web"),
		q("filter[tags][lte]", "web"),
	} {
		if w := call(http.MethodGet, "/api/v1/cis?"+query, ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", query, w.Code, w.Body.String())
		}
	}
}

func TestCITagsFilterAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "f4")
	var typeID string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).
		Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	checkTagsFilter(t, ctx, ci.NewPGRepository(f.App), typeID, f.Client1)
}

func TestCITagsFilterInMemory(t *testing.T) {
	const org = "00000000-0000-0000-0000-0000000000f4"
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org, UserID: "00000000-0000-0000-0000-0000000000f5"})
	checkTagsFilter(t, ctx, ci.NewMemoryRepository(), "00000000-0000-0000-0000-0000000000f6", "")
}
