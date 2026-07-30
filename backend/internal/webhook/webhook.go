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
	CreatedAt      string            `json:"created_at"`
	UpdatedAt      string            `json:"updated_at"`
}

// CreateRequest is the payload for creating a webhook subscription.
type CreateRequest struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Secret  string            `json:"secret"`
	Events  []string          `json:"events"`
	Headers map[string]string `json:"headers,omitempty"`
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
}

// Repository defines persistence operations for webhooks.
type Repository interface {
	List(orgID string, page api.PaginationParams) ([]Subscription, int, error)
	GetByID(orgID, id string) (*Subscription, error)
	Create(sub *Subscription) error
	Delete(orgID, id string) error
	ListByEvent(orgID, event string) ([]Subscription, error)
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

func (r *MemoryRepository) List(orgID string, page api.PaginationParams) ([]Subscription, int, error) {
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

func (r *MemoryRepository) GetByID(orgID, id string) (*Subscription, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sub, ok := r.items[id]
	if !ok || sub.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	return sub, nil
}

func (r *MemoryRepository) Create(sub *Subscription) error {
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

func (r *MemoryRepository) Delete(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	sub, ok := r.items[id]
	if !ok || sub.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.items, id)
	return nil
}

func (r *MemoryRepository) ListByEvent(orgID, event string) ([]Subscription, error) {
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

// Handler provides HTTP handlers for webhook endpoints.
type Handler struct {
	repo       Repository
	deliverer  TestDeliverer
	deliveries DeliveryLister
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
	}
	return h
}

// RegisterRoutes registers webhook routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/webhooks", h.List)
	r.Post("/api/v1/webhooks", h.Create)
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
	sub, err := h.repo.GetByID(t.OrganizationID, id)
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
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
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
	if _, err := h.repo.GetByID(t.OrganizationID, id); err != nil {
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
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
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

// List handles GET /api/v1/webhooks
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	subs, total, err := h.repo.List(t.OrganizationID, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Subscription]{
		Data:    subs,
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
	sub, err := h.repo.GetByID(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "webhook not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, sub)
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

	for _, e := range req.Events {
		if !ValidEvents[e] {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", fmt.Sprintf("invalid event: %s", e))
			return
		}
	}

	sub := &Subscription{
		OrganizationID: t.OrganizationID,
		Name:           req.Name,
		URL:            req.URL,
		Secret:         req.Secret,
		Events:         req.Events,
		IsActive:       true,
		Headers:        req.Headers,
	}

	if err := h.repo.Create(sub); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, sub)
}

// Delete handles DELETE /api/v1/webhooks/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "webhook not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
