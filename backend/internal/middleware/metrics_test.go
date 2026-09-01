package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

func buildMetricsMux(t *testing.T, includeTenant bool) (chi.Router, *prometheus.Registry) {
	t.Helper()
	registry := prometheus.NewRegistry()
	mw := RegisterHTTPMetrics(registry, includeTenant)
	mux := chi.NewRouter()
	mux.Use(mw)
	mux.With().Get("/api/v1/cis/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Get("/api/v1/fail", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	return mux, registry
}

func TestHTTPMetricsIncrement(t *testing.T) {
	mux, registry := buildMetricsMux(t, false)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/cis/abc-123", nil)
	mux.ServeHTTP(httptest.NewRecorder(), r)

	// The path label must be the route pattern, not the concrete ID, to bound
	// cardinality.
	fams, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range fams {
		if f.GetName() != "reticora_http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["path"] == "/api/v1/cis/{id}" && labels["method"] == "GET" && labels["status"] == "200" {
				found = true
				if m.GetCounter().GetValue() != 1 {
					t.Fatalf("expected counter 1, got %v", m.GetCounter().GetValue())
				}
			}
		}
	}
	if !found {
		t.Fatal("expected a metric for the routed path /api/v1/cis/{id}")
	}
}

func TestHTTPMetricsStatusAndDurationRecorded(t *testing.T) {
	mux, registry := buildMetricsMux(t, false)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/fail", nil)
	mux.ServeHTTP(httptest.NewRecorder(), r)

	fams, _ := registry.Gather()
	var sawCount, sawHist bool
	for _, f := range fams {
		switch f.GetName() {
		case "reticora_http_requests_total":
			for _, m := range f.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "status" && l.GetValue() == "500" {
						sawCount = true
					}
				}
			}
		case "reticora_http_request_duration_seconds":
			if len(f.GetMetric()) > 0 {
				sawHist = true
			}
		}
	}
	if !sawCount {
		t.Fatal("expected a 500 counter")
	}
	if !sawHist {
		t.Fatal("expected a duration histogram")
	}
}

func TestHTTPMetricsTenantLabel(t *testing.T) {
	mux, registry := buildMetricsMux(t, true)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-42"})
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/cis/x", nil)
	handler.ServeHTTP(httptest.NewRecorder(), r)

	fams, _ := registry.Gather()
	var orgLabel string
	for _, f := range fams {
		if f.GetName() != "reticora_http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "organization_id" {
					orgLabel = l.GetValue()
				}
			}
		}
	}
	if orgLabel != "org-42" {
		t.Fatalf("expected organization_id label org-42, got %q", orgLabel)
	}
}

func TestHTTPMetricsTenantLabelDisabled(t *testing.T) {
	mux, registry := buildMetricsMux(t, false)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-42"})
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/cis/x", nil)
	handler.ServeHTTP(httptest.NewRecorder(), r)

	fams, _ := registry.Gather()
	for _, f := range fams {
		if f.GetName() != "reticora_http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "organization_id" && l.GetValue() != "" {
					t.Fatalf("expected empty organization_id when disabled, got %q", l.GetValue())
				}
			}
		}
	}
}

func TestHTTPMetricsNoTenantNoPanic(t *testing.T) {
	mux, _ := buildMetricsMux(t, true)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/cis/x", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}
