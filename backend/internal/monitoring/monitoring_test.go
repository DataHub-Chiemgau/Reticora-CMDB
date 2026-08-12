package monitoring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func TestMemoryMetricStoreQueryAggregatesByStep(t *testing.T) {
	store := NewMemoryMetricStore()
	base := time.Date(2026, 7, 8, 13, 0, 0, 0, time.UTC)
	if err := store.Ingest(t.Context(), []Metric{
		{OrgID: "org-1", CIID: "ci-1", Name: "cpu_usage", Value: 10, Timestamp: base.Add(10 * time.Second)},
		{OrgID: "org-1", CIID: "ci-1", Name: "cpu_usage", Value: 20, Timestamp: base.Add(20 * time.Second)},
		{OrgID: "org-1", CIID: "ci-1", Name: "cpu_usage", Value: 30, Timestamp: base.Add(70 * time.Second)},
	}); err != nil {
		t.Fatal(err)
	}

	points, err := store.Query(t.Context(), MetricQuery{
		OrgID: "org-1",
		CIID:  "ci-1",
		Name:  "cpu_usage",
		From:  base,
		To:    base.Add(2 * time.Minute),
		Step:  time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("expected 2 aggregated points, got %d", len(points))
	}
	if points[0].Value != 15 {
		t.Fatalf("expected first bucket average 15, got %v", points[0].Value)
	}
	if points[1].Value != 30 {
		t.Fatalf("expected second bucket average 30, got %v", points[1].Value)
	}
}

func TestHandlerMetricAndAlertRoutes(t *testing.T) {
	h := NewHandler(NewMemoryMetricStore())
	r := chi.NewRouter()
	h.RegisterRoutes(r)

	ingestReq := httptest.NewRequest(http.MethodPost, "/api/v1/monitoring/metrics", strings.NewReader(`{"metrics":[{"ci_id":"ci-1","name":"cpu_usage","value":42.5}]}`))
	ingestReq = ingestReq.WithContext(tenant.WithTenant(ingestReq.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, ingestReq)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected ingest status 202, got %d", w.Code)
	}

	queryReq := httptest.NewRequest(http.MethodGet, "/api/v1/monitoring/metrics?name=cpu_usage&ci_id=ci-1", nil)
	queryReq = queryReq.WithContext(tenant.WithTenant(queryReq.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, queryReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected query status 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "42.5") {
		t.Fatalf("expected metric value in response, got %s", w.Body.String())
	}

	alertReq := httptest.NewRequest(http.MethodPost, "/api/v1/monitoring/alerts", strings.NewReader(`{"name":"High CPU","metric_name":"cpu_usage","condition":"gt","threshold":90,"duration":"5m","severity":"critical","enabled":true}`))
	alertReq = alertReq.WithContext(tenant.WithTenant(alertReq.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, alertReq)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected alert status 201, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "High CPU") || !strings.Contains(body, "\"duration\":\"5m0s\"") {
		t.Fatalf("unexpected alert response: %s", body)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/monitoring/alerts", nil)
	listReq = listReq.WithContext(tenant.WithTenant(listReq.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, listReq)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "High CPU") {
		t.Fatalf("unexpected list response: %d %s", w.Code, w.Body.String())
	}

	createdID := extractJSONField(body, "id")
	if createdID == "" {
		t.Fatalf("expected created alert id in %s", body)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/monitoring/alerts/"+createdID, nil)
	deleteReq = deleteReq.WithContext(tenant.WithTenant(deleteReq.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, deleteReq)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected delete status 204, got %d", w.Code)
	}
}

func TestEvaluatorFiresAfterDurationOnce(t *testing.T) {
	store := NewMemoryMetricStore()
	base := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	if err := store.Ingest(t.Context(), []Metric{
		{OrgID: "org-1", CIID: "ci-1", Name: "cpu_usage", Value: 95, Timestamp: base},
	}); err != nil {
		t.Fatal(err)
	}

	alerts := NewAlertManager()
	if _, err := alerts.CreateRule(t.Context(), AlertRule{
		OrgID: "org-1", Name: "High CPU", MetricName: "cpu_usage",
		Condition: "gt", Threshold: 90, Duration: 5 * time.Minute,
		Severity: "critical", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	var fired []AlertEvent
	ev := NewEvaluator(store, alerts, NotifierFunc(func(_ context.Context, e AlertEvent) {
		fired = append(fired, e)
	}))
	ev.Now = func() time.Time { return base.Add(2 * time.Minute) }

	// Condition holds, but the duration has not elapsed yet.
	ev.Evaluate(t.Context())
	if len(fired) != 0 {
		t.Fatalf("expected no alert before duration elapsed, got %d", len(fired))
	}

	// After the duration the alert fires exactly once.
	ev.Now = func() time.Time { return base.Add(6 * time.Minute) }
	ev.Evaluate(t.Context())
	if len(fired) != 1 {
		t.Fatalf("expected exactly one alert after duration, got %d", len(fired))
	}
	if fired[0].Value != 95 || fired[0].Rule.Name != "High CPU" {
		t.Fatalf("unexpected alert event: %+v", fired[0])
	}
	ev.Evaluate(t.Context())
	if len(fired) != 1 {
		t.Fatalf("expected no duplicate notification, got %d", len(fired))
	}

	// Recovery clears the pending state; a later breach starts a new window.
	if err := store.Ingest(t.Context(), []Metric{
		{OrgID: "org-1", CIID: "ci-1", Name: "cpu_usage", Value: 40, Timestamp: base.Add(7 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	ev.Now = func() time.Time { return base.Add(8 * time.Minute) }
	ev.Evaluate(t.Context())
	updated, err := alerts.ListRules(t.Context(), "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].PendingSince != nil {
		t.Fatalf("expected pending state cleared, got %+v", updated)
	}
}

func TestEvaluatorDisabledRulesAreSkipped(t *testing.T) {
	store := NewMemoryMetricStore()
	if err := store.Ingest(t.Context(), []Metric{
		{OrgID: "org-1", Name: "cpu_usage", Value: 99, Timestamp: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	alerts := NewAlertManager()
	if _, err := alerts.CreateRule(t.Context(), AlertRule{
		OrgID: "org-1", Name: "disabled", MetricName: "cpu_usage",
		Condition: "gt", Threshold: 1, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	fired := 0
	ev := NewEvaluator(store, alerts, NotifierFunc(func(context.Context, AlertEvent) { fired++ }))
	ev.Evaluate(t.Context())
	if fired != 0 {
		t.Fatalf("disabled rule must not fire, got %d alerts", fired)
	}
}

func extractJSONField(body, field string) string {
	needle := `"` + field + `":"`
	idx := strings.Index(body, needle)
	if idx == -1 {
		return ""
	}
	start := idx + len(needle)
	end := strings.Index(body[start:], `"`)
	if end == -1 {
		return ""
	}
	return body[start : start+end]
}
