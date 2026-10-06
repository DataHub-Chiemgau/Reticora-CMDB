// Package webhook provides webhook subscription management for Reticora CMDB.
package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Subscription represents a webhook subscription.
type Subscription struct {
	ID             string            `json:"id"`
	OrganizationID string            `json:"organization_id"`
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	Secret         string            `json:"-"` // never expose in API responses
	Events         []string          `json:"events"`
	IsActive       bool              `json:"is_active"`
	Headers        map[string]string `json:"headers,omitempty"`
	// ServiceAccountID binds the subscription to a service account: it then
	// receives only events whose object the account may read (RBA-08).
	ServiceAccountID string `json:"service_account_id,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// MaskedHeaderValue replaces every custom header value in API responses.
const MaskedHeaderValue = "***"

// redacted returns the subscription as the API shows it: custom header values
// often carry authentication (Authorization, API keys), so they are
// write-only and masked; only the header names are returned (SEC-01).
func (s *Subscription) redacted() Subscription {
	out := *s
	if len(s.Headers) > 0 {
		out.Headers = make(map[string]string, len(s.Headers))
		for name := range s.Headers {
			out.Headers[name] = MaskedHeaderValue
		}
	}
	return out
}

// CreateRequest is the payload for creating a webhook subscription.
type CreateRequest struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Secret  string            `json:"secret"`
	Events  []string          `json:"events"`
	Headers map[string]string `json:"headers,omitempty"`
	// ServiceAccountID optionally binds the subscription (RBA-08).
	ServiceAccountID string `json:"service_account_id,omitempty"`
}

// TestEvent is the event name used by the webhook test endpoint. It is
// deliberately not part of ValidEvents so it cannot be subscribed to.
const TestEvent = "webhook.test"

// ValidEvents lists allowed webhook events.
var ValidEvents = map[string]bool{
	"ci.created":           true,
	"ci.updated":           true,
	"ci.deleted":           true,
	"ci.status_changed":    true,
	"relationship.created": true,
	"relationship.deleted": true,
	"discovery.completed":  true,
	// Enterprise CMDB + asset/inventory extension (spec §20).
	"ci_type.created":               true,
	"ci_type.updated":               true,
	"ci_type.deactivated":           true,
	"ci_attribute.changed":          true,
	"lifecycle.transitioned":        true,
	"asset.movement":                true,
	"inventory.movement":            true,
	"reservation.created":           true,
	"reservation.released":          true,
	"composition.created":           true,
	"composition.updated":           true,
	"composition.deleted":           true,
	"override.changed":              true,
	"reconciliation.policy_changed": true,
}

// Repository defines persistence operations for webhooks.
type Repository interface {
	List(ctx context.Context, orgID string, page api.PaginationParams) ([]Subscription, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Subscription, error)
	Create(ctx context.Context, sub *Subscription) error
	Delete(ctx context.Context, orgID, id string) error
	ListByEvent(ctx context.Context, orgID, event string) ([]Subscription, error)
}

// MemoryRepository is an in-memory webhook store.
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Subscription
	seq   int
}

// NewMemoryRepository creates a new in-memory webhook repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: make(map[string]*Subscription)}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, page api.PaginationParams) ([]Subscription, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Subscription
	for _, sub := range r.items {
		if sub.OrganizationID != orgID {
			continue
		}
		result = append(result, *sub)
	}

	total := len(result)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return result[start:end], total, nil
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Subscription, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sub, ok := r.items[id]
	if !ok || sub.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	return sub, nil
}

func (r *MemoryRepository) Create(_ context.Context, sub *Subscription) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	sub.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	sub.CreatedAt = now
	sub.UpdatedAt = now
	r.items[sub.ID] = sub
	return nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	sub, ok := r.items[id]
	if !ok || sub.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.items, id)
	return nil
}

func (r *MemoryRepository) ListByEvent(_ context.Context, orgID, event string) ([]Subscription, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Subscription
	for _, sub := range r.items {
		if sub.OrganizationID != orgID || !sub.IsActive {
			continue
		}
		for _, e := range sub.Events {
			if e == event {
				result = append(result, *sub)
				break
			}
		}
	}
	return result, nil
}

// TestDeliverer performs a single synchronous delivery for the test endpoint.
type TestDeliverer interface {
	DeliverOnce(sub Subscription, event string, payload any) (Delivery, error)
}

// DeliveryLister exposes the durable delivery history of a subscription.
type DeliveryLister interface {
	ListDeliveries(ctx context.Context, orgID, subscriptionID string, page api.PaginationParams) ([]DeliveryRecord, int, error)
}

// DeadLetterLister exposes the dead-letter queue of a tenant.
type DeadLetterLister interface {
	ListDeadLetters(ctx context.Context, orgID string, page api.PaginationParams) ([]DeadLetter, int, error)
}

// Handler provides HTTP handlers for webhook endpoints.
type Handler struct {
	repo        Repository
	deliverer   TestDeliverer
	deliveries  DeliveryLister
	deadLetters DeadLetterLister
	// egress validates subscriber URLs when they are stored (SEC-08).
	egress egress.Options
}

// WithEgress sets the destination policy subscriber URLs are validated
// against; the default blocks every internal destination.
func (h *Handler) WithEgress(opts egress.Options) *Handler {
	h.egress = opts
	return h
}

// NewHandler creates a new webhook handler. An optional TestDeliverer enables
// POST /api/v1/webhooks/{id}/test.
func NewHandler(repo Repository, deliverer ...TestDeliverer) *Handler {
	h := &Handler{repo: repo}
	if len(deliverer) > 0 {
		h.deliverer = deliverer[0]
		if lister, ok := deliverer[0].(DeliveryLister); ok {
			h.deliveries = lister
		}
		if lister, ok := deliverer[0].(DeadLetterLister); ok {
			h.deadLetters = lister
		}
	}
	return h
}

// RegisterRoutes registers webhook routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/webhooks", h.List)
	r.Post("/api/v1/webhooks", h.Create)
	r.Get("/api/v1/webhooks/dead-letters", h.ListDeadLetters)
	r.Get("/api/v1/webhooks/{id}", h.Get)
	r.Delete("/api/v1/webhooks/{id}", h.Delete)
	r.Post("/api/v1/webhooks/{id}/test", h.Test)
	r.Get("/api/v1/webhooks/{id}/deliveries", h.ListDeliveries)
}

// Test handles POST /api/v1/webhooks/{id}/test by sending a single signed
// `webhook.test` payload to the subscription URL and returning the attempt.
func (h *Handler) Test(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.deliverer == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "webhook dispatcher is not configured")
		return
	}

	id := chi.URLParam(r, "id")
	sub, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "webhook not found")
		return
	}

	delivery, err := h.deliverer.DeliverOnce(*sub, TestEvent, map[string]any{
		"event":           TestEvent,
		"subscription_id": sub.ID,
		"organization_id": sub.OrganizationID,
		"sent_at":         time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, delivery)
}

// ListDeliveries handles GET /api/v1/webhooks/{id}/deliveries and returns the
// durable delivery history including retry state.
func (h *Handler) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.deliveries == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "webhook delivery store is not configured")
		return
	}

	id := chi.URLParam(r, "id")
	if _, err := h.repo.GetByID(r.Context(), t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "webhook not found")
		return
	}

	page := api.ParsePagination(r)
	records, total, err := h.deliveries.ListDeliveries(r.Context(), t.OrganizationID, id, page)
	if err != nil {
		if errors.Is(err, ErrNoDeliveryStore) {
			api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", err.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[DeliveryRecord]{
		Data:    records,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// ListDeadLetters handles GET /api/v1/webhooks/dead-letters and returns the
// deliveries that exhausted their retry budget, newest first.
func (h *Handler) ListDeadLetters(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.deadLetters == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "webhook delivery store is not configured")
		return
	}

	page := api.ParsePagination(r)
	letters, total, err := h.deadLetters.ListDeadLetters(r.Context(), t.OrganizationID, page)
	if err != nil {
		if errors.Is(err, ErrNoDeliveryStore) {
			api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", err.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[DeadLetter]{
		Data:    letters,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// List handles GET /api/v1/webhooks
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	subs, total, err := h.repo.List(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	shown := make([]Subscription, 0, len(subs))
	for i := range subs {
		shown = append(shown, subs[i].redacted())
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Subscription]{
		Data:    shown,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Get handles GET /api/v1/webhooks/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	sub, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "webhook not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, sub.redacted())
}

// Create handles POST /api/v1/webhooks
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.Name == "" || req.URL == "" || req.Secret == "" || len(req.Events) == 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name, url, secret, and events are required")
		return
	}

	if err := h.egress.ValidateURL(req.URL); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	for _, e := range req.Events {
		if !ValidEvents[e] {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", fmt.Sprintf("invalid event: %s", e))
			return
		}
	}

	sub := &Subscription{
		OrganizationID:   t.OrganizationID,
		Name:             req.Name,
		URL:              req.URL,
		Secret:           req.Secret,
		Events:           req.Events,
		IsActive:         true,
		Headers:          req.Headers,
		ServiceAccountID: req.ServiceAccountID,
	}

	if err := h.repo.Create(r.Context(), sub); err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, sub.redacted())
}

// Delete handles DELETE /api/v1/webhooks/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "webhook not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
