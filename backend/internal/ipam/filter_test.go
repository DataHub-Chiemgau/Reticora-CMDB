package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestListFiltersOnMemoryRepository covers the list filters of IPM-20
// (WP-909) without PostgreSQL: contains lists the chain of the default VRF
// narrowest first, q and is_pool filter the subnets, type and q the
// addresses; a malformed contains or type is 422 with its field.
func TestListFiltersOnMemoryRepository(t *testing.T) {
	mux := chi.NewRouter()
	ipam.NewHandler(ipam.NewMemoryRepository()).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	for _, body := range []string{`{"cidr":"10.0.0.0/8"}`, `{"cidr":"10.1.1.0/24","name":"Edge"}`, `{"cidr":"10.1.0.0/16","is_pool":true}`} {
		if w := call(http.MethodPost, "/api/v1/subnets", body); w.Code != http.StatusCreated {
			t.Fatalf("POST /subnets %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := call(http.MethodPost, "/api/v1/ips", `{"address":"192.0.2.7","type":"dhcp","dns_name":"kiosk.example.test"}`); w.Code != http.StatusCreated {
		t.Fatalf("POST /ips: %d %s", w.Code, w.Body.String())
	}
	subnets := func(query string) string {
		t.Helper()
		var page api.ListResponse[ipam.Subnet]
		w := call(http.MethodGet, "/api/v1/subnets?"+query, "")
		_ = json.Unmarshal(w.Body.Bytes(), &page)
		out := []string{}
		for _, s := range page.Data {
			out = append(out, s.CIDR)
		}
		return strings.Join(out, ",")
	}
	for query, want := range map[string]string{
		"contains=10.1.1.5": "10.1.1.0/24,10.1.0.0/16,10.0.0.0/8",
		"q=edge":            "10.1.1.0/24",
		"is_pool=true":      "10.1.0.0/16",
	} {
		if got := subnets(query); got != want {
			t.Errorf("GET /subnets?%s = %s, want %s", query, got, want)
		}
	}
	for path, want := range map[string]int{
		"/api/v1/ips?type=dhcp&q=kiosk": 1,
		"/api/v1/ips?type=static":       0,
		"/api/v1/ips?q=192.0.2":         1,
	} {
		var page api.ListResponse[ipam.IPAddress]
		_ = json.Unmarshal(call(http.MethodGet, path, "").Body.Bytes(), &page)
		if len(page.Data) != want {
			t.Errorf("GET %s = %d addresses, want %d", path, len(page.Data), want)
		}
	}
	for path, field := range map[string]string{"/api/v1/subnets?contains=nope": "contains", "/api/v1/ips?type=nope": "type"} {
		if w := call(http.MethodGet, path, ""); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
			t.Errorf("GET %s = %d %s, want 422 %s", path, w.Code, w.Body.String(), field)
		}
	}
}
