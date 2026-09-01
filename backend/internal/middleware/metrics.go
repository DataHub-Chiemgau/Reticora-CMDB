package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// routedPath returns the matched chi route pattern (e.g. /api/v1/cis/{id})
// instead of the raw request path. Using the route pattern bounds label
// cardinality: concrete entity IDs never become label values. Falls back to
// the cleaned request path when no route matched (404s, operational
// endpoints).
func routedPath(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return cleanRoutePath(r.URL.Path)
}

// httpMetrics holds the tenant-aware HTTP request metrics. They are
// registered on the dedicated registry that serves /metrics so the endpoint
// stays stable.
type httpMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// tenantLabel mirrors the guard in NewHTTPMetrics: when false the
// organization_id label is left empty to bound series cardinality.
func newHTTPMetrics() *httpMetrics {
	labelNames := []string{"method", "path", "status", "organization_id"}
	return &httpMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "reticora",
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests by method, routed path, status and tenant.",
		}, labelNames),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "reticora",
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request duration in seconds by method, routed path, status and tenant.",
			Buckets:   prometheus.DefBuckets,
		}, labelNames),
	}
}

func (m *httpMetrics) observe(r *http.Request, status int, d time.Duration, includeTenant bool) {
	orgID := ""
	if includeTenant {
		orgID = tenant.FromContext(r.Context()).OrganizationID
	}
	path := routedPath(r)
	code := strconv.Itoa(status)
	m.requests.WithLabelValues(r.Method, path, code, orgID).Inc()
	m.duration.WithLabelValues(r.Method, path, code, orgID).Observe(d.Seconds())
}

// RegisterHTTPMetrics builds the HTTP metric collectors and registers them on
// the given registry. It returns the middleware that records them.
//
// includeTenant controls whether the organization_id label is populated. The
// label multiplies the number of series by the number of tenants, so it is a
// deliberate opt-in for deployments with a bounded tenant count; when false
// the label is left empty and all tenants aggregate into one series per
// method/path/status.
func RegisterHTTPMetrics(registry *prometheus.Registry, includeTenant bool) func(http.Handler) http.Handler {
	m := newHTTPMetrics()
	registry.MustRegister(m.requests, m.duration)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			m.observe(r, rw.status, time.Since(start), includeTenant)
		})
	}
}
