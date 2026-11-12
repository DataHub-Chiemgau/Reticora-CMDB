package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestVLANDomainsOnPostgreSQL covers IPM-06 (WP-906, E-212) against
// PostgreSQL:
//   - the same vlan_id in two domains is allowed, twice in one domain 409;
//   - VLAN 100 at site A without domain and VLAN 100 in a domain with site
//     A coexist; without domain E-24 holds unchanged (twice VLAN 100 at the
//     same site 409);
//   - a VLAN of a domain at a site outside the domain is 422 site_id, an
//     unknown domain 422 domain_id;
//   - a site whose VLANs of the domain remain cannot leave it (409); a site
//     with a domain assignment is not deleted (409 with the kind
//     vlan_domain_site); a domain with VLANs is not deleted (409);
//   - a client principal reads the domains and sees only the assignments
//     of its sites, but writes no domain (POST 403, PATCH/DELETE 404).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestVLANDomainsOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "d6")
	mux := chi.NewRouter()
	ipam.NewHandler(ipam.NewPGRepository(f.App)).RegisterRoutes(mux)
	locations.NewHandler(locations.NewPGRepository(f.App)).RegisterRoutes(mux)
	user := f.AppUser(t, f.OrgA, "d6-admin")
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, v any) {
		t.Helper()
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	expect := func(w *httptest.ResponseRecorder, want int, field, what string) {
		t.Helper()
		if w.Code != want || (field != "" && !strings.Contains(w.Body.String(), `"`+field+`"`)) {
			t.Errorf("%s = %d %s, want %d %s", what, w.Code, w.Body.String(), want, field)
		}
	}
	site := func(clientID, name string) string {
		t.Helper()
		w := call(orgCtx, http.MethodPost, "/api/v1/locations", `{"kind":"site","client_id":"`+clientID+`","name":"`+name+`"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /locations = %d %s", w.Code, w.Body.String())
		}
		var s struct {
			ID string `json:"id"`
		}
		decode(w, &s)
		return s.ID
	}
	siteA, siteB, siteC := site(f.Client1, "d6-a"), site(f.Client2, "d6-b"), site(f.Client1, "d6-c")
	domain := func(body string) ipam.VLANDomain {
		t.Helper()
		w := call(orgCtx, http.MethodPost, "/api/v1/vlan-domains", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /vlan-domains %s = %d %s", body, w.Code, w.Body.String())
		}
		var d ipam.VLANDomain
		decode(w, &d)
		return d
	}
	vlan := func(body string, want int, field string) ipam.VLAN {
		t.Helper()
		w := call(orgCtx, http.MethodPost, "/api/v1/vlans", body)
		expect(w, want, field, "POST /vlans "+body)
		var v ipam.VLAN
		if w.Code == http.StatusCreated {
			decode(w, &v)
		}
		return v
	}
	sorted := func(ids ...string) []string {
		sort.Strings(ids)
		return ids
	}

	d1 := domain(`{"name":"d6-campus","description":"L2 campus","site_ids":["` + siteA + `","` + siteB + `","` + siteA + `"]}`)
	if !slices.Equal(d1.SiteIDs, sorted(siteA, siteB)) || d1.Description != "L2 campus" {
		t.Errorf("created domain = %+v, want sites A and B", d1)
	}
	d2 := domain(`{"name":"d6-dc","site_ids":["` + siteA + `"]}`)
	expect(call(orgCtx, http.MethodPost, "/api/v1/vlan-domains", `{"name":"d6-dc"}`), http.StatusConflict, "name", "duplicate domain name")
	expect(call(orgCtx, http.MethodPost, "/api/v1/vlan-domains", `{"name":"d6-x","site_ids":["`+f.ID()+`"]}`),
		http.StatusUnprocessableEntity, "site_ids", "domain with an unknown site")

	// Uniqueness per domain; E-24 for VLANs without domain.
	inD1 := vlan(`{"vlan_id":100,"name":"d6-100","site_id":"`+siteA+`","domain_id":"`+d1.ID+`"}`, http.StatusCreated, "")
	if inD1.DomainID != d1.ID || inD1.ClientID != f.Client1 {
		t.Errorf("VLAN of domain 1 = %+v", inD1)
	}
	vlan(`{"vlan_id":100,"name":"d6-100-dc","domain_id":"`+d2.ID+`"}`, http.StatusCreated, "")
	vlan(`{"vlan_id":100,"name":"d6-100-b","site_id":"`+siteB+`","domain_id":"`+d1.ID+`"}`, http.StatusConflict, "")
	vlan(`{"vlan_id":100,"name":"d6-100-none","domain_id":"`+d1.ID+`"}`, http.StatusConflict, "")
	plain := vlan(`{"vlan_id":100,"name":"d6-100-a","site_id":"`+siteA+`"}`, http.StatusCreated, "")
	vlan(`{"vlan_id":100,"name":"d6-100-a2","site_id":"`+siteA+`"}`, http.StatusConflict, "")

	// The site of a VLAN of a domain belongs to the domain; the domain is
	// known.
	vlan(`{"vlan_id":200,"name":"d6-200","site_id":"`+siteB+`","domain_id":"`+d2.ID+`"}`, http.StatusUnprocessableEntity, "site_id")
	vlan(`{"vlan_id":200,"name":"d6-200","domain_id":"`+f.ID()+`"}`, http.StatusUnprocessableEntity, "domain_id")
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vlans/"+plain.ID, `{"domain_id":"`+d2.ID+`","vlan_id":300}`),
		http.StatusOK, "", "PATCH a VLAN at site A into domain 2")
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vlans/"+plain.ID, `{"domain_id":""}`), http.StatusOK, "", "PATCH clears the domain")
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vlans/"+plain.ID, `{"domain_id":"`+d2.ID+`"}`), http.StatusOK, "", "PATCH back into domain 2")
	if w := call(orgCtx, http.MethodPatch, "/api/v1/vlans/"+plain.ID, `{"domain_id":" "}`); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"domain_id"`) {
		t.Errorf("PATCH with a blank domain_id = %d %s, want 200 without domain", w.Code, w.Body.String())
	}

	// A site with VLANs of the domain cannot leave it; others can.
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vlan-domains/"+d1.ID, `{"site_ids":["`+siteB+`"]}`),
		http.StatusConflict, "site_ids", "remove site A with a VLAN from domain 1")
	w := call(orgCtx, http.MethodPatch, "/api/v1/vlan-domains/"+d1.ID, `{"site_ids":["`+siteA+`","`+siteC+`"],"name":"d6-campus2"}`)
	var got ipam.VLANDomain
	decode(w, &got)
	if w.Code != http.StatusOK || !slices.Equal(got.SiteIDs, sorted(siteA, siteC)) || got.Name != "d6-campus2" {
		t.Errorf("PATCH domain 1 sites A, C = %d %s", w.Code, w.Body.String())
	}

	// A site with a domain assignment is not deleted.
	w = call(orgCtx, http.MethodDelete, "/api/v1/locations/"+siteC, "")
	var problem struct {
		Dependencies []string `json:"dependencies"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &problem)
	if w.Code != http.StatusConflict || !slices.Equal(problem.Dependencies, []string{locations.DependencyVLANDomain}) {
		t.Errorf("DELETE site with a domain assignment = %d %s, want 409 [vlan_domain_site]", w.Code, w.Body.String())
	}

	// The VLANs of a domain; a domain with VLANs is not deleted.
	w = call(orgCtx, http.MethodGet, "/api/v1/vlan-domains/"+d1.ID+"/vlans", "")
	var vlans struct {
		Data  []ipam.VLAN `json:"data"`
		Total int         `json:"total"`
	}
	decode(w, &vlans)
	if w.Code != http.StatusOK || vlans.Total != 1 || len(vlans.Data) != 1 || vlans.Data[0].ID != inD1.ID {
		t.Errorf("GET /vlan-domains/{id}/vlans = %d %s", w.Code, w.Body.String())
	}
	expect(call(orgCtx, http.MethodGet, "/api/v1/vlan-domains/"+f.ID()+"/vlans", ""), http.StatusNotFound, "", "VLANs of an unknown domain")
	expect(call(orgCtx, http.MethodDelete, "/api/v1/vlan-domains/"+d1.ID, ""), http.StatusConflict, "", "DELETE domain with VLANs")

	// A client principal reads the domains with the sites of its client
	// only and writes none.
	w = call(client1Ctx, http.MethodGet, "/api/v1/vlan-domains", "")
	var list struct {
		Data []ipam.VLANDomain `json:"data"`
	}
	decode(w, &list)
	if w.Code != http.StatusOK || len(list.Data) != 2 {
		t.Fatalf("client GET /vlan-domains = %d %s", w.Code, w.Body.String())
	}
	d3 := domain(`{"name":"d6-wan","site_ids":["` + siteB + `"]}`)
	w = call(client1Ctx, http.MethodGet, "/api/v1/vlan-domains/"+d3.ID, "")
	got = ipam.VLANDomain{}
	decode(w, &got)
	if w.Code != http.StatusOK || len(got.SiteIDs) != 0 {
		t.Errorf("client GET domain of client-2 sites = %d %s, want no sites", w.Code, w.Body.String())
	}
	expect(call(client1Ctx, http.MethodPost, "/api/v1/vlan-domains", `{"name":"d6-client"}`), http.StatusForbidden, "", "client POST domain")
	expect(call(client1Ctx, http.MethodPatch, "/api/v1/vlan-domains/"+d3.ID, `{"site_ids":["`+siteA+`"]}`),
		http.StatusNotFound, "", "client PATCH domain")
	expect(call(client1Ctx, http.MethodDelete, "/api/v1/vlan-domains/"+d3.ID, ""), http.StatusNotFound, "", "client DELETE domain")

	// A domain without VLANs goes with its assignments; the site is then
	// deletable.
	expect(call(orgCtx, http.MethodDelete, "/api/v1/vlan-domains/"+d3.ID, ""), http.StatusNoContent, "", "DELETE unused domain")
	expect(call(orgCtx, http.MethodPatch, "/api/v1/vlan-domains/"+d1.ID, `{"site_ids":["`+siteA+`"]}`), http.StatusOK, "", "remove site C")
	expect(call(orgCtx, http.MethodDelete, "/api/v1/locations/"+siteC, ""), http.StatusNoContent, "", "DELETE site without assignment")
}

// TestVLANDomainRoutesWithoutDatabase pins AK5 of WP-906: without
// PostgreSQL the domain routes answer 501, and the memory repository keeps
// the domain of a VLAN and its uniqueness per domain.
func TestVLANDomainRoutesWithoutDatabase(t *testing.T) {
	repo := ipam.NewMemoryRepository()
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		return w
	}
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/vlan-domains", ""},
		{http.MethodPost, "/api/v1/vlan-domains", `{"name":"x"}`},
		{http.MethodGet, "/api/v1/vlan-domains/x", ""},
		{http.MethodPatch, "/api/v1/vlan-domains/x", `{}`},
		{http.MethodDelete, "/api/v1/vlan-domains/x", ""},
		{http.MethodGet, "/api/v1/vlan-domains/x/vlans", ""},
	} {
		if w := serve(rt.method, rt.path, rt.body); w.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", rt.method, rt.path, w.Code)
		}
	}
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"vlan_id":10,"name":"a","site_id":"s1","domain_id":"d1"}`, http.StatusCreated},
		{`{"vlan_id":10,"name":"b","site_id":"s2","domain_id":"d1"}`, http.StatusConflict},
		{`{"vlan_id":10,"name":"c","domain_id":"d2"}`, http.StatusCreated},
		{`{"vlan_id":10,"name":"d","site_id":"s1"}`, http.StatusCreated},
		{`{"vlan_id":10,"name":"e","site_id":"s1"}`, http.StatusConflict},
	} {
		w := serve(http.MethodPost, "/api/v1/vlans", c.body)
		if w.Code != c.want {
			t.Errorf("POST /vlans %s = %d %s, want %d", c.body, w.Code, w.Body.String(), c.want)
		}
		if c.want == http.StatusCreated && !strings.Contains(c.body, `"domain_id"`) == strings.Contains(w.Body.String(), `"domain_id"`) {
			t.Errorf("POST /vlans %s: domain_id not passed through: %s", c.body, w.Body.String())
		}
	}
}
