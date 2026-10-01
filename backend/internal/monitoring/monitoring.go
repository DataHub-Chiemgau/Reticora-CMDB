// Package monitoring provides metric collection and alerting capabilities.
package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// MetricStore defines metric ingestion and query operations.
type MetricStore interface {
	Ingest(ctx context.Context, metrics []Metric) error
	Query(ctx context.Context, q MetricQuery) ([]MetricPoint, error)
}

// Metric is a single telemetry sample.
type Metric struct {
	OrgID     string            `json:"org_id"`
	CIID      string            `json:"ci_id,omitempty"`
	Name      string            `json:"name"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// MetricQuery describes a metric query request.
type MetricQuery struct {
	OrgID string        `json:"org_id"`
	CIID  string        `json:"ci_id,omitempty"`
	Name  string        `json:"name,omitempty"`
	From  time.Time     `json:"from,omitempty"`
	To    time.Time     `json:"to,omitempty"`
	Step  time.Duration `json:"step,omitempty"`
}

// MetricPoint is a sampled or aggregated metric value.
type MetricPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// AlertRule describes a threshold-based alert.
type AlertRule struct {
	ID         string        `json:"id"`
	OrgID      string        `json:"org_id"`
	Name       string        `json:"name"`
	MetricName string        `json:"metric_name"`
	Condition  string        `json:"condition"`
	Threshold  float64       `json:"threshold"`
	Duration   time.Duration `json:"duration"`
	Severity   string        `json:"severity"`
	Enabled    bool          `json:"enabled"`
	// PendingSince and LastFiredAt are evaluation bookkeeping: the rule
	// fires only when the condition held continuously for Duration, and it
	// notifies once per firing instead of on every evaluation tick.
	PendingSince *time.Time `json:"pending_since,omitempty"`
	LastFiredAt  *time.Time `json:"last_fired_at,omitempty"`
}

// AlertEvent describes a fired alert.
type AlertEvent struct {
	Rule    AlertRule `json:"rule"`
	Value   float64   `json:"value"`
	Sample  Metric    `json:"sample"`
	FiredAt time.Time `json:"fired_at"`
}

// Notifier receives fired alerts (webhooks, tickets, log lines, ...).
type Notifier interface {
	NotifyAlert(ctx context.Context, event AlertEvent)
}

// NotifierFunc adapts a plain function to the Notifier interface.
type NotifierFunc func(ctx context.Context, event AlertEvent)

// NotifyAlert implements Notifier.
func (f NotifierFunc) NotifyAlert(ctx context.Context, event AlertEvent) { f(ctx, event) }

// AlertStore is the persistence port for alert rules and their evaluation
// state. AlertManager implements it in memory; PGAlertStore persists in
// PostgreSQL.
type AlertStore interface {
	ListRules(ctx context.Context, orgID string) ([]AlertRule, error)
	CreateRule(ctx context.Context, rule AlertRule) (AlertRule, error)
	UpdateRule(ctx context.Context, orgID, id string, req UpdateAlertRuleRequest) (AlertRule, error)
	DeleteRule(ctx context.Context, orgID, id string) (bool, error)
	ListEnabled(ctx context.Context) ([]AlertRule, error)
	MarkPending(ctx context.Context, rule AlertRule, since time.Time) error
	MarkFired(ctx context.Context, rule AlertRule, at time.Time) error
	ClearPending(ctx context.Context, rule AlertRule) error
}

// UpdateAlertRuleRequest carries the mutable fields of an alert rule. Nil
// pointers leave the stored value untouched, so a PATCH can toggle a single
// field (typically enabled) without resending the whole rule.
type UpdateAlertRuleRequest struct {
	Name       *string  `json:"name"`
	Condition  *string  `json:"condition"`
	Threshold  *float64 `json:"threshold"`
	Duration   *string  `json:"duration"`
	Severity   *string  `json:"severity"`
	Enabled    *bool    `json:"enabled"`
}

// MarshalJSON encodes duration values as Go duration strings.
func (r AlertRule) MarshalJSON() ([]byte, error) {
	type alias AlertRule
	return json.Marshal(struct {
		alias
		Duration string `json:"duration"`
	}{
		alias:    alias(r),
		Duration: r.Duration.String(),
	})
}

// UnmarshalJSON accepts duration values as a string or nanoseconds.
func (r *AlertRule) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID           string      `json:"id"`
		OrgID        string      `json:"org_id"`
		Name         string      `json:"name"`
		MetricName   string      `json:"metric_name"`
		Condition    string      `json:"condition"`
		Threshold    float64     `json:"threshold"`
		Duration     interface{} `json:"duration"`
		Severity     string      `json:"severity"`
		Enabled      bool        `json:"enabled"`
		PendingSince *time.Time  `json:"pending_since"`
		LastFiredAt  *time.Time  `json:"last_fired_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var duration time.Duration
	switch value := raw.Duration.(type) {
	case nil:
		duration = 0
	case string:
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}
		duration = parsed
	case float64:
		duration = time.Duration(value)
	default:
		return errors.New("duration must be a string or number")
	}

	*r = AlertRule{
		ID:           raw.ID,
		OrgID:        raw.OrgID,
		Name:         raw.Name,
		MetricName:   raw.MetricName,
		Condition:    raw.Condition,
		Threshold:    raw.Threshold,
		Duration:     duration,
		Severity:     raw.Severity,
		Enabled:      raw.Enabled,
		PendingSince: raw.PendingSince,
		LastFiredAt:  raw.LastFiredAt,
	}
	return nil
}

// AlertManager manages in-memory alert rules. It implements AlertStore and
// is used for the --no-db development mode and tests; production uses
// PGAlertStore.
type AlertManager struct {
	rules []AlertRule
	mu    sync.RWMutex
}

// NewAlertManager creates an empty alert manager.
func NewAlertManager() *AlertManager {
	return &AlertManager{rules: make([]AlertRule, 0)}
}

// ListRules returns alert rules visible to the given org.
func (m *AlertManager) ListRules(_ context.Context, orgID string) ([]AlertRule, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]AlertRule, 0, len(m.rules))
	for _, rule := range m.rules {
		if orgID != "" && rule.OrgID != orgID {
			continue
		}
		result = append(result, rule)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// CreateRule stores a new alert rule.
func (m *AlertManager) CreateRule(_ context.Context, rule AlertRule) (AlertRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if rule.ID == "" {
		rule.ID = fmt.Sprintf("alert-%d", time.Now().UTC().UnixNano())
	}
	for _, existing := range m.rules {
		if existing.ID == rule.ID && existing.OrgID == rule.OrgID {
			return AlertRule{}, fmt.Errorf("alert rule %q already exists", rule.ID)
		}
	}
	m.rules = append(m.rules, rule)
	return rule, nil
}

// UpdateRule applies a partial update to an in-memory rule.
func (m *AlertManager) UpdateRule(_ context.Context, orgID, id string, req UpdateAlertRuleRequest) (AlertRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, rule := range m.rules {
		if rule.ID != id || rule.OrgID != orgID {
			continue
		}
		if req.Name != nil {
			rule.Name = *req.Name
		}
		if req.Condition != nil {
			rule.Condition = *req.Condition
		}
		if req.Threshold != nil {
			rule.Threshold = *req.Threshold
		}
		if req.Duration != nil {
			d, err := time.ParseDuration(*req.Duration)
			if err != nil {
				return AlertRule{}, fmt.Errorf("invalid duration %q: %w", *req.Duration, err)
			}
			rule.Duration = d
		}
		if req.Severity != nil {
			rule.Severity = *req.Severity
		}
		if req.Enabled != nil {
			rule.Enabled = *req.Enabled
		}
		m.rules[i] = rule
		return rule, nil
	}
	return AlertRule{}, fmt.Errorf("alert rule not found")
}

// DeleteRule removes an alert rule.
func (m *AlertManager) DeleteRule(_ context.Context, orgID, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, rule := range m.rules {
		if rule.ID != id || rule.OrgID != orgID {
			continue
		}
		m.rules = append(m.rules[:i], m.rules[i+1:]...)
		return true, nil
	}
	return false, nil
}

// ListEnabled returns every enabled rule regardless of tenant.
func (m *AlertManager) ListEnabled(_ context.Context) ([]AlertRule, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]AlertRule, 0, len(m.rules))
	for _, rule := range m.rules {
		if rule.Enabled {
			result = append(result, rule)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OrgID != result[j].OrgID {
			return result[i].OrgID < result[j].OrgID
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// MarkPending records when the condition started being true.
func (m *AlertManager) MarkPending(_ context.Context, rule AlertRule, since time.Time) error {
	return m.updateRuleState(rule, func(r *AlertRule) { r.PendingSince = &since })
}

// MarkFired records the firing and clears the pending marker.
func (m *AlertManager) MarkFired(_ context.Context, rule AlertRule, at time.Time) error {
	return m.updateRuleState(rule, func(r *AlertRule) {
		r.PendingSince = nil
		r.LastFiredAt = &at
	})
}

// ClearPending resets the duration tracking once the condition no longer
// holds.
func (m *AlertManager) ClearPending(_ context.Context, rule AlertRule) error {
	return m.updateRuleState(rule, func(r *AlertRule) { r.PendingSince = nil })
}

func (m *AlertManager) updateRuleState(rule AlertRule, update func(*AlertRule)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rules {
		if m.rules[i].ID == rule.ID && m.rules[i].OrgID == rule.OrgID {
			update(&m.rules[i])
			return nil
		}
	}
	return fmt.Errorf("alert rule %s not found", rule.ID)
}

// MemoryMetricStore stores metrics in memory for development and tests.
type MemoryMetricStore struct {
	mu      sync.RWMutex
	metrics []Metric
}

// NewMemoryMetricStore creates an empty in-memory metric store.
func NewMemoryMetricStore() *MemoryMetricStore {
	return &MemoryMetricStore{metrics: make([]Metric, 0)}
}

// Ingest appends metrics to the in-memory store.
func (s *MemoryMetricStore) Ingest(ctx context.Context, metrics []Metric) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, metric := range metrics {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if metric.Timestamp.IsZero() {
			metric.Timestamp = time.Now().UTC()
		}
		metric.Timestamp = metric.Timestamp.UTC()
		metric.Labels = cloneLabels(metric.Labels)
		s.metrics = append(s.metrics, metric)
	}
	return nil
}

// Query filters and optionally aggregates metrics.
func (s *MemoryMetricStore) Query(ctx context.Context, q MetricQuery) ([]MetricPoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	points := make([]MetricPoint, 0)
	for _, metric := range s.metrics {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if q.OrgID != "" && metric.OrgID != q.OrgID {
			continue
		}
		if q.CIID != "" && metric.CIID != q.CIID {
			continue
		}
		if q.Name != "" && metric.Name != q.Name {
			continue
		}
		if !q.From.IsZero() && metric.Timestamp.Before(q.From) {
			continue
		}
		if !q.To.IsZero() && metric.Timestamp.After(q.To) {
			continue
		}
		points = append(points, MetricPoint{Timestamp: metric.Timestamp.UTC(), Value: metric.Value})
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].Timestamp.Before(points[j].Timestamp)
	})
	if q.Step <= 0 {
		return points, nil
	}

	type bucket struct {
		sum   float64
		count int
	}
	buckets := make(map[time.Time]bucket)
	ordered := make([]time.Time, 0)
	seen := make(map[time.Time]struct{})
	for _, point := range points {
		bucketTime := point.Timestamp.UTC().Truncate(q.Step)
		current := buckets[bucketTime]
		current.sum += point.Value
		current.count++
		buckets[bucketTime] = current
		if _, ok := seen[bucketTime]; !ok {
			seen[bucketTime] = struct{}{}
			ordered = append(ordered, bucketTime)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Before(ordered[j]) })

	aggregated := make([]MetricPoint, 0, len(ordered))
	for _, bucketTime := range ordered {
		current := buckets[bucketTime]
		aggregated = append(aggregated, MetricPoint{
			Timestamp: bucketTime,
			Value:     current.sum / float64(current.count),
		})
	}
	return aggregated, nil
}

// EvaluatingStore is implemented by stores that can evaluate rules on their
// own; it is used by the router to attach the evaluator without changing the
// MetricStore contract.
type EvaluatingStore interface {
	AlertStore() AlertStore
}

// Handler provides HTTP handlers for monitoring endpoints.
type Handler struct {
	store  MetricStore
	alerts AlertStore
}

// NewHandler creates a new monitoring handler backed by in-memory stores
// when no implementations are given.
func NewHandler(store MetricStore, alerts ...AlertStore) *Handler {
	if store == nil {
		store = NewMemoryMetricStore()
	}
	alertStore := AlertStore(NewAlertManager())
	if len(alerts) > 0 && alerts[0] != nil {
		alertStore = alerts[0]
	}
	return &Handler{store: store, alerts: alertStore}
}

// RegisterRoutes registers monitoring routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/monitoring/metrics", h.QueryMetrics)
	r.Post("/api/v1/monitoring/metrics", h.IngestMetrics)
	r.Get("/api/v1/monitoring/alerts", h.ListAlerts)
	r.Post("/api/v1/monitoring/alerts", h.CreateAlert)
	r.Patch("/api/v1/monitoring/alerts/{id}", h.UpdateAlert)
	r.Delete("/api/v1/monitoring/alerts/{id}", h.DeleteAlert)
}

// QueryMetrics handles GET /api/v1/monitoring/metrics.
func (h *Handler) QueryMetrics(w http.ResponseWriter, r *http.Request) {
	orgID, ok := requireOrgID(w, r)
	if !ok {
		return
	}

	query, err := metricQueryFromRequest(r, orgID)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	points, err := h.store.Query(r.Context(), query)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, points)
}

// IngestMetrics handles POST /api/v1/monitoring/metrics.
func (h *Handler) IngestMetrics(w http.ResponseWriter, r *http.Request) {
	orgID, ok := requireOrgID(w, r)
	if !ok {
		return
	}

	metrics, err := decodeMetrics(r.Body)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if len(metrics) == 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "at least one metric is required")
		return
	}

	now := time.Now().UTC()
	for i := range metrics {
		metrics[i].OrgID = orgID
		if metrics[i].Timestamp.IsZero() {
			metrics[i].Timestamp = now
		}
		metrics[i].Timestamp = metrics[i].Timestamp.UTC()
		if metrics[i].Labels == nil {
			metrics[i].Labels = make(map[string]string)
		}
		if strings.TrimSpace(metrics[i].Name) == "" {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "metric name is required")
			return
		}
	}

	if err := h.store.Ingest(r.Context(), metrics); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"ingested": len(metrics)})
}

// ListAlerts handles GET /api/v1/monitoring/alerts.
func (h *Handler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	orgID, ok := requireOrgID(w, r)
	if !ok {
		return
	}
	rules, err := h.alerts.ListRules(r.Context(), orgID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, rules)
}

// CreateAlert handles POST /api/v1/monitoring/alerts.
func (h *Handler) CreateAlert(w http.ResponseWriter, r *http.Request) {
	orgID, ok := requireOrgID(w, r)
	if !ok {
		return
	}

	var rule AlertRule
	if err := decodeJSON(r.Body, &rule); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if err := validateAlertRule(&rule); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	rule.OrgID = orgID

	created, err := h.alerts.CreateRule(r.Context(), rule)
	if err != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, created)
}

// UpdateAlert handles PATCH /api/v1/monitoring/alerts/{id}. It exists so a
// rule can be toggled or tuned after creation without delete/recreate.
func (h *Handler) UpdateAlert(w http.ResponseWriter, r *http.Request) {
	orgID, ok := requireOrgID(w, r)
	if !ok {
		return
	}

	var req UpdateAlertRuleRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Condition != nil {
		switch *req.Condition {
		case "gt", "lt", "eq":
		default:
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "condition must be one of gt, lt or eq")
			return
		}
	}
	if req.Severity != nil {
		switch *req.Severity {
		case "critical", "warning", "info":
		default:
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "severity must be one of critical, warning or info")
			return
		}
	}
	if req.Duration != nil {
		if _, err := time.ParseDuration(*req.Duration); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid duration: "+err.Error())
			return
		}
	}

	rule, err := h.alerts.UpdateRule(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			api.WriteError(w, http.StatusNotFound, "Not Found", "alert rule not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, rule)
}

// DeleteAlert handles DELETE /api/v1/monitoring/alerts/{id}.
func (h *Handler) DeleteAlert(w http.ResponseWriter, r *http.Request) {
	orgID, ok := requireOrgID(w, r)
	if !ok {
		return
	}
	deleted, err := h.alerts.DeleteRule(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if !deleted {
		api.WriteError(w, http.StatusNotFound, "Not Found", "alert rule not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func requireOrgID(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := tenant.FromContext(r.Context()).OrganizationID
	if orgID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return orgID, true
}

func metricQueryFromRequest(r *http.Request, orgID string) (MetricQuery, error) {
	query := MetricQuery{
		OrgID: orgID,
		CIID:  firstNonEmpty(r.URL.Query().Get("ci_id"), r.URL.Query().Get("ciid")),
		Name:  strings.TrimSpace(r.URL.Query().Get("name")),
	}
	var err error
	if value := r.URL.Query().Get("from"); value != "" {
		query.From, err = parseRequestTime(value)
		if err != nil {
			return MetricQuery{}, fmt.Errorf("invalid from timestamp")
		}
	}
	if value := r.URL.Query().Get("to"); value != "" {
		query.To, err = parseRequestTime(value)
		if err != nil {
			return MetricQuery{}, fmt.Errorf("invalid to timestamp")
		}
	}
	if value := r.URL.Query().Get("step"); value != "" {
		query.Step, err = time.ParseDuration(value)
		if err != nil {
			return MetricQuery{}, fmt.Errorf("invalid step duration")
		}
	}
	if !query.From.IsZero() && !query.To.IsZero() && query.To.Before(query.From) {
		return MetricQuery{}, errors.New("to must be greater than or equal to from")
	}
	return query, nil
}

func parseRequestTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
}

func decodeMetrics(body io.ReadCloser) ([]Metric, error) {
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("request body is required")
	}
	if !json.Valid(data) {
		return nil, errors.New("request body must be valid JSON")
	}

	var metrics []Metric
	if err := json.Unmarshal(data, &metrics); err == nil && metrics != nil {
		return metrics, nil
	}

	var wrapper struct {
		Metrics []Metric `json:"metrics"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil && wrapper.Metrics != nil {
		return wrapper.Metrics, nil
	}

	var metric Metric
	if err := json.Unmarshal(data, &metric); err == nil {
		return []Metric{metric}, nil
	}

	return nil, errors.New("unable to decode metrics payload")
}

func decodeJSON(body io.ReadCloser, dest any) error {
	defer body.Close()
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func validateAlertRule(rule *AlertRule) error {
	if strings.TrimSpace(rule.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(rule.MetricName) == "" {
		return errors.New("metric_name is required")
	}
	switch rule.Condition {
	case "gt", "lt", "eq":
	default:
		return errors.New("condition must be one of gt, lt or eq")
	}
	if rule.Severity == "" {
		rule.Severity = "warning"
	}
	switch rule.Severity {
	case "critical", "warning", "info":
	default:
		return errors.New("severity must be one of critical, warning or info")
	}
	return nil
}

func cloneLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
