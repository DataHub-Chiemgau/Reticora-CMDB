// Package relationship provides the CI relationship domain for Reticora CMDB.
package relationship

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Relationship represents a directed edge between two CIs.
type Relationship struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	SourceCIID     string         `json:"source_ci_id"`
	TargetCIID     string         `json:"target_ci_id"`
	RelType        string         `json:"rel_type"`
	Attributes     map[string]any `json:"attributes"`
	Source         string         `json:"source"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// CreateRequest is the payload for creating a relationship.
type CreateRequest struct {
	SourceCIID string         `json:"source_ci_id"`
	TargetCIID string         `json:"target_ci_id"`
	RelType    string         `json:"rel_type"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Source     string         `json:"source,omitempty"`
}

// ValidRelTypes lists allowed relationship types.
var ValidRelTypes = map[string]bool{
	"connected_to": true,
	"hosted_on":    true,
	"depends_on":   true,
	"member_of":    true,
	"powers":       true,
	"stores":       true,
	"monitors":     true,
	"backs_up":     true,
}

// Repository defines persistence operations for relationships.
type Repository interface {
	List(orgID string, ciID string, page api.PaginationParams) ([]Relationship, int, error)
	Create(rel *Relationship) error
	Delete(orgID, id string) error
}

// MemoryRepository is an in-memory relationship store.
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Relationship
	seq   int
}

// NewMemoryRepository creates a new in-memory relationship repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: make(map[string]*Relationship)}
}

func (r *MemoryRepository) List(orgID string, ciID string, page api.PaginationParams) ([]Relationship, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Relationship
	for _, rel := range r.items {
		if rel.OrganizationID != orgID {
			continue
		}
		if ciID != "" && rel.SourceCIID != ciID && rel.TargetCIID != ciID {
			continue
		}
		result = append(result, *rel)
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

func (r *MemoryRepository) Create(rel *Relationship) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	rel.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	rel.CreatedAt = now
	rel.UpdatedAt = now
	r.items[rel.ID] = rel
	return nil
}

func (r *MemoryRepository) Delete(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rel, ok := r.items[id]
	if !ok || rel.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.items, id)
	return nil
}

// Handler provides HTTP handlers for relationship endpoints.
type Handler struct {
	repo Repository
}

// NewHandler creates a new relationship handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers relationship routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/cis/{id}/relationships", h.List)
	r.Post("/api/v1/relationships", h.Create)
	r.Delete("/api/v1/relationships/{id}", h.Delete)
}

// List handles GET /api/v1/cis/{id}/relationships
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	ciID := chi.URLParam(r, "id")
	page := api.ParsePagination(r)

	rels, total, err := h.repo.List(t.OrganizationID, ciID, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Relationship]{
		Data:    rels,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Create handles POST /api/v1/relationships
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

	if req.SourceCIID == "" || req.TargetCIID == "" || req.RelType == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "source_ci_id, target_ci_id, and rel_type are required")
		return
	}
	if !ValidRelTypes[req.RelType] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid rel_type")
		return
	}

	rel := &Relationship{
		OrganizationID: t.OrganizationID,
		SourceCIID:     req.SourceCIID,
		TargetCIID:     req.TargetCIID,
		RelType:        req.RelType,
		Attributes:     req.Attributes,
		Source:         req.Source,
	}
	if rel.Attributes == nil {
		rel.Attributes = make(map[string]any)
	}
	if rel.Source == "" {
		rel.Source = "manual"
	}

	if err := h.repo.Create(rel); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, rel)
}

// Delete handles DELETE /api/v1/relationships/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "relationship not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
