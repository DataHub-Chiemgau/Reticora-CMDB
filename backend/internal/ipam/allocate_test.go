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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAllocationOnMemoryRepository covers the allocation routes of IPM-03
// (WP-904 AK5) without PostgreSQL: the memory repository allocates under
// its mutex with the same rules.
func TestAllocationOnMemoryRepository(t *testing.T) {
	mux := chi.NewRouter()
	ipam.NewHandler(ipam.NewMemoryRepository()).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	create := func(body string) ipam.Subnet {
		t.Helper()
		w := call(http.MethodPost, "/api/v1/subnets", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /subnets: %d %s", w.Code, w.Body.String())
		}
		var s ipam.Subnet
		_ = json.Unmarshal(w.Body.Bytes(), &s)
		return s
	}
	small := create(`{"cidr":"10.9.0.0/30","gateway":"10.9.0.1"}`)
	w := call(http.MethodPost, "/api/v1/subnets/"+small.ID+"/allocate", `{"type":"reserved"}`)
	var a ipam.IPAddress
	_ = json.Unmarshal(w.Body.Bytes(), &a)
	if w.Code != http.StatusCreated || a.Address != "10.9.0.2" || a.ReservedBy != "user-1" {
		t.Fatalf("allocate = %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/api/v1/subnets/"+small.ID+"/allocate", `{}`); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), ipam.ProblemSubnetExhausted) {
		t.Errorf("allocate in a full subnet = %d %s, want 409 subnet-exhausted", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/api/v1/subnets/"+small.ID+"/allocate", `{"type":"dhcp"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("allocate of type dhcp = %d, want 422", w.Code)
	}
	pool := create(`{"cidr":"10.8.0.0/24","is_pool":true}`)
	create(`{"cidr":"10.8.0.0/25"}`)
	for _, want := range []string{"10.8.0.128/26", "10.8.0.192/26"} {
		w := call(http.MethodPost, "/api/v1/subnets/"+pool.ID+"/allocate-subnet", `{"prefix_length":26}`)
		var s ipam.Subnet
		_ = json.Unmarshal(w.Body.Bytes(), &s)
		if w.Code != http.StatusCreated || s.CIDR != want || s.ParentID != pool.ID {
			t.Errorf("allocate-subnet = %d %s, want %s", w.Code, w.Body.String(), want)
		}
	}
	if w := call(http.MethodPost, "/api/v1/subnets/"+pool.ID+"/allocate-subnet", `{"prefix_length":26}`); w.Code != http.StatusConflict {
		t.Errorf("third allocate-subnet = %d, want 409", w.Code)
	}
	if w := call(http.MethodPost, "/api/v1/subnets/"+pool.ID+"/allocate-subnet", `{"prefix_length":8}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("allocate-subnet of a shorter prefix = %d, want 422", w.Code)
	}
}
