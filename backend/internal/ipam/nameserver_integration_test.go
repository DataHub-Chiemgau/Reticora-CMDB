package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestNameserverSetsOnPostgreSQL covers IPM-05 (WP-905) against PostgreSQL:
//   - effective_dns_servers of a subnet are its own dns_servers, else the
//     servers of its nameserver set, else none;
//   - a set that a subnet uses cannot be deleted (409), a duplicate name is
//     409, an invalid server list 422 servers;
//   - a client principal reads the sets, its POST is 403, its PATCH and
//     DELETE 404 (org-wide writes as for VRFs, E-09);
//   - a set of another organization on a subnet is 422 nameserver_set_id.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestNameserverSetsOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "d5")
	repo := ipam.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
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
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	orgBCtx := tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	createSet := func(ctx context.Context, body string, want int) ipam.NameserverSet {
		t.Helper()
		w := call(ctx, http.MethodPost, "/api/v1/nameserver-sets", body)
		if w.Code != want {
			t.Fatalf("POST /nameserver-sets %s = %d %s, want %d", body, w.Code, w.Body.String(), want)
		}
		var s ipam.NameserverSet
		if want == http.StatusCreated {
			decode(w, &s)
		}
		return s
	}
	createSubnet := func(body string, want int) ipam.Subnet {
		t.Helper()
		w := call(orgCtx, http.MethodPost, "/api/v1/subnets", body)
		if w.Code != want {
			t.Fatalf("POST /subnets %s = %d %s, want %d", body, w.Code, w.Body.String(), want)
		}
		var s ipam.Subnet
		if want == http.StatusCreated {
			decode(w, &s)
		}
		return s
	}

	set := createSet(orgCtx, `{"name":"d5-dns","servers":["10.215.0.53"," 2001:db8::53 "],"description":"site DNS"}`, http.StatusCreated)
	if !slices.Equal(set.Servers, []string{"10.215.0.53", "2001:db8::53"}) || set.Description != "site DNS" {
		t.Errorf("created set = %+v", set)
	}
	createSet(orgCtx, `{"name":"d5-dns","servers":["10.215.0.54"]}`, http.StatusConflict)
	for _, body := range []string{
		`{"name":"d5-bad","servers":[]}`,
		`{"name":"d5-bad","servers":["no-ip"]}`,
		`{"name":"d5-bad","servers":["fe80::1%eth0"]}`,
		`{"name":"d5-bad","servers":["10.0.0.1","10.0.0.2","10.0.0.3","10.0.0.4","10.0.0.5","10.0.0.6","10.0.0.7","10.0.0.8","10.0.0.9"]}`,
	} {
		w := call(orgCtx, http.MethodPost, "/api/v1/nameserver-sets", body)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"servers"`) {
			t.Errorf("POST %s = %d %s, want 422 servers", body, w.Code, w.Body.String())
		}
	}

	// Effective DNS servers: own servers win, else the set, else none.
	own := createSubnet(`{"cidr":"10.215.1.0/24","dns_servers":["10.215.1.53"],"nameserver_set_id":"`+set.ID+`"}`, http.StatusCreated)
	if own.NameserverSetID != set.ID || !slices.Equal(own.EffectiveDNSServers, []string{"10.215.1.53"}) {
		t.Errorf("subnet with own servers = set %q effective %v, want its own server", own.NameserverSetID, own.EffectiveDNSServers)
	}
	viaSet := createSubnet(`{"cidr":"10.215.2.0/24","nameserver_set_id":"`+set.ID+`"}`, http.StatusCreated)
	if !slices.Equal(viaSet.EffectiveDNSServers, set.Servers) {
		t.Errorf("subnet with set only: effective %v, want %v", viaSet.EffectiveDNSServers, set.Servers)
	}
	none := createSubnet(`{"cidr":"10.215.3.0/24"}`, http.StatusCreated)
	if len(none.EffectiveDNSServers) != 0 || none.NameserverSetID != "" {
		t.Errorf("subnet without servers and set: %+v", none)
	}
	// A changed set shows through at once; clearing the set leaves none.
	if w := call(orgCtx, http.MethodPatch, "/api/v1/nameserver-sets/"+set.ID, `{"servers":["10.215.0.99"]}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH set = %d %s", w.Code, w.Body.String())
	}
	w := call(orgCtx, http.MethodGet, "/api/v1/subnets/"+viaSet.ID, "")
	var got ipam.Subnet
	decode(w, &got)
	if !slices.Equal(got.EffectiveDNSServers, []string{"10.215.0.99"}) {
		t.Errorf("subnet after set change: effective %v", got.EffectiveDNSServers)
	}
	w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+none.ID, `{"nameserver_set_id":"`+set.ID+`"}`)
	decode(w, &got)
	if w.Code != http.StatusOK || !slices.Equal(got.EffectiveDNSServers, []string{"10.215.0.99"}) {
		t.Errorf("PATCH subnet set = %d %s", w.Code, w.Body.String())
	}
	w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+none.ID, `{"nameserver_set_id":""}`)
	got = ipam.Subnet{}
	decode(w, &got)
	if w.Code != http.StatusOK || got.NameserverSetID != "" || len(got.EffectiveDNSServers) != 0 {
		t.Errorf("PATCH subnet clears set = %d %s", w.Code, w.Body.String())
	}

	// A set in use cannot be deleted.
	if w = call(orgCtx, http.MethodDelete, "/api/v1/nameserver-sets/"+set.ID, ""); w.Code != http.StatusConflict {
		t.Errorf("DELETE used set = %d %s, want 409", w.Code, w.Body.String())
	}

	// A set of another organization (or an unknown one) on a subnet: 422.
	setB := createSet(orgBCtx, `{"name":"d5-dns-b","servers":["10.216.0.53"]}`, http.StatusCreated)
	for _, body := range []string{
		`{"cidr":"10.215.4.0/24","nameserver_set_id":"` + setB.ID + `"}`,
		`{"cidr":"10.215.4.0/24","nameserver_set_id":"not-a-uuid"}`,
	} {
		w = call(orgCtx, http.MethodPost, "/api/v1/subnets", body)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "nameserver_set_id") {
			t.Errorf("POST /subnets %s = %d %s, want 422 nameserver_set_id", body, w.Code, w.Body.String())
		}
	}
	w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+none.ID, `{"nameserver_set_id":"`+setB.ID+`"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "nameserver_set_id") {
		t.Errorf("PATCH subnet with a set of org B = %d %s, want 422", w.Code, w.Body.String())
	}
	if w = call(orgCtx, http.MethodGet, "/api/v1/nameserver-sets/"+setB.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("GET set of org B from org A = %d, want 404", w.Code)
	}

	// A client principal reads the sets but writes none.
	w = call(client1Ctx, http.MethodGet, "/api/v1/nameserver-sets", "")
	var list struct {
		Data  []ipam.NameserverSet `json:"data"`
		Total int                  `json:"total"`
	}
	decode(w, &list)
	if w.Code != http.StatusOK || list.Total != 1 || len(list.Data) != 1 || list.Data[0].ID != set.ID {
		t.Errorf("client GET /nameserver-sets = %d %s", w.Code, w.Body.String())
	}
	if w = call(client1Ctx, http.MethodGet, "/api/v1/nameserver-sets/"+set.ID, ""); w.Code != http.StatusOK {
		t.Errorf("client GET set = %d", w.Code)
	}
	createSet(client1Ctx, `{"name":"d5-client","servers":["10.215.9.53"]}`, http.StatusForbidden)
	if w = call(client1Ctx, http.MethodPatch, "/api/v1/nameserver-sets/"+set.ID, `{"name":"d5-renamed"}`); w.Code != http.StatusNotFound {
		t.Errorf("client PATCH set = %d %s, want 404", w.Code, w.Body.String())
	}
	if w = call(client1Ctx, http.MethodDelete, "/api/v1/nameserver-sets/"+set.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("client DELETE set = %d %s, want 404", w.Code, w.Body.String())
	}

	// A duplicate name on rename is 409; an unused set is deleted.
	other := createSet(orgCtx, `{"name":"d5-other","servers":["10.215.0.1"]}`, http.StatusCreated)
	if w = call(orgCtx, http.MethodPatch, "/api/v1/nameserver-sets/"+other.ID, `{"name":"d5-dns"}`); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"name"`) {
		t.Errorf("rename to a taken name = %d %s, want 409 name", w.Code, w.Body.String())
	}
	if w = call(orgCtx, http.MethodDelete, "/api/v1/nameserver-sets/"+other.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("DELETE unused set = %d %s", w.Code, w.Body.String())
	}
}

// TestNameserverSetRoutesWithoutDatabase pins AK5 of WP-905: the memory
// repository keeps no sets, so the routes answer 501 and a subnet with a
// set is 422 nameserver_set_id.
func TestNameserverSetRoutesWithoutDatabase(t *testing.T) {
	mux := chi.NewRouter()
	ipam.NewHandler(ipam.NewMemoryRepository()).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/nameserver-sets", ""},
		{http.MethodPost, "/api/v1/nameserver-sets", `{"name":"x","servers":["10.0.0.1"]}`},
		{http.MethodGet, "/api/v1/nameserver-sets/x", ""},
		{http.MethodPatch, "/api/v1/nameserver-sets/x", `{}`},
		{http.MethodDelete, "/api/v1/nameserver-sets/x", ""},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(rt.method, rt.path, bytes.NewBufferString(rt.body)).WithContext(ctx))
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", rt.method, rt.path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/subnets",
		bytes.NewBufferString(`{"cidr":"10.0.0.0/24","nameserver_set_id":"c0de0000-0000-4000-8000-000000000001"}`)).WithContext(ctx))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "nameserver_set_id") {
		t.Errorf("POST /subnets with a set without database = %d %s, want 422", w.Code, w.Body.String())
	}
}
