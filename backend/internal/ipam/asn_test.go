package ipam

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// AS number notation and validation (IPM-16, WP-1066): asplain, asdot
// (RFC 5396) and the prefix AS; 0, 23456, 65535, 4294967295 and values
// beyond 32 bits are refused.

func TestParseASNAsplainAsdot(t *testing.T) {
	for in, want := range map[string]int64{
		"65001":        65001,
		"4200000000":   4200000000,
		"1.10":         65546,
		"AS4200000000": 4200000000,
		"as65001":      65001,
		"AS 1.10":      65546,
		" 64512 ":      64512,
		"0.1":          1,
		"65535.65534":  4294967294,
		"1":            1,
	} {
		got, err := ParseASN(in)
		if err != nil || got != want {
			t.Errorf("ParseASN(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"65535.65535", "4294967296", "1.65536", "65536.0", "AS", "", "-1", "+5", "1.+2", "1..2",
		"AS-1", "abc", "1.2.3", "0x10", "65001.5e3"} {
		if got, err := ParseASN(in); !errors.Is(err, ErrInvalidASN) {
			t.Errorf("ParseASN(%q) = %d, %v; want ErrInvalidASN", in, got, err)
		}
	}
}

func TestParseASNReserved(t *testing.T) {
	for _, in := range []string{"0", "23456", "AS23456", "65535", "0.65535", "4294967295", "65535.65535", "0.0"} {
		if got, err := ParseASN(in); !errors.Is(err, ErrInvalidASN) {
			t.Errorf("ParseASN(%q) = %d, %v; want ErrInvalidASN", in, got, err)
		}
	}
	for _, in := range []string{"23455", "23457", "65534", "65536", "4294967294"} {
		if _, err := ParseASN(in); err != nil {
			t.Errorf("ParseASN(%q): %v", in, err)
		}
	}
}

func TestASDotOutput(t *testing.T) {
	for n, want := range map[int64]string{
		1: "1", 65001: "65001", 65534: "65534", 65536: "1.0", 65546: "1.10", 4200000000: "64086.59904", 4294967294: "65535.65534",
	} {
		if got := ASDot(n); got != want {
			t.Errorf("ASDot(%d) = %q, want %q", n, got, want)
		}
		if back, err := ParseASN(ASDot(n)); err != nil || back != n {
			t.Errorf("ParseASN(ASDot(%d)) = %d, %v", n, back, err)
		}
	}
	var a struct {
		Number ASNumber `json:"number"`
	}
	for body, want := range map[string]string{`{"number":65001}`: "65001", `{"number":"1.10"}`: "1.10", `{"number":1.5}`: "invalid"} {
		if err := json.Unmarshal([]byte(body), &a); err != nil || string(a.Number) != want {
			t.Errorf("decode %s = %q, %v; want %q", body, a.Number, err, want)
		}
	}
}

// TestASNRoutesWithoutDatabase covers AK7 and the field errors of AK4:
// without PostgreSQL the routes answer 501; the input checks come first.
func TestASNRoutesWithoutDatabase(t *testing.T) {
	mux := chi.NewRouter()
	NewHandler(NewMemoryRepository()).RegisterRoutes(mux)
	ctx := tenant.WithTenant(t.Context(), tenant.TenantInfo{OrganizationID: "org-1"})
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/asns", ""},
		{http.MethodPost, "/api/v1/asns", `{"number":65001,"name":"x","kind":"own"}`},
		{http.MethodGet, "/api/v1/asn-ranges", ""},
		{http.MethodDelete, "/api/v1/asn-ranges/x", ""},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(c.method, c.path, strings.NewReader(c.body)).WithContext(ctx))
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", c.method, c.path, w.Code)
		}
	}
}
