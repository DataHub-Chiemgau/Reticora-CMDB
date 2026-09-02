// Package relationship provides the CI relationship domain for Reticora CMDB.
package relationship

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
	// Provenance and verification metadata (spec §12).
	Confidence        *float64 `json:"confidence,omitempty"`
	FirstSeenAt       string   `json:"first_seen_at,omitempty"`
	LastSeenAt        string   `json:"last_seen_at,omitempty"`
	VerificationState string   `json:"verification_state,omitempty"`
	SourceSystem      string   `json:"source_system,omitempty"`
	Notes             string   `json:"notes,omitempty"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

// CreateRequest is the payload for creating a relationship.
type CreateRequest struct {
	SourceCIID        string         `json:"source_ci_id"`
	TargetCIID        string         `json:"target_ci_id"`
	RelType           string         `json:"rel_type"`
	Attributes        map[string]any `json:"attributes,omitempty"`
	Source            string         `json:"source,omitempty"`
	Confidence        *float64       `json:"confidence,omitempty"`
	VerificationState string         `json:"verification_state,omitempty"`
	SourceSystem      string         `json:"source_system,omitempty"`
	Notes             string         `json:"notes,omitempty"`
}

// UpdateRequest is the payload for editing a relationship's provenance and
// verification metadata (spec §13: manual relationships can be created,
// edited, verified and removed). The endpoints of the edge and its type are
// immutable — rewiring is expressed by deleting and recreating the edge so the
// graph history stays truthful.
type UpdateRequest struct {
	Attributes        map[string]any `json:"attributes,omitempty"`
	Confidence        *float64       `json:"confidence,omitempty"`
	VerificationState *string        `json:"verification_state,omitempty"`
	SourceSystem      *string        `json:"source_system,omitempty"`
	Notes             *string        `json:"notes,omitempty"`
}

// ValidVerificationStates lists the verification vocabulary for relationships.
var ValidVerificationStates = map[string]bool{
	"unverified": true, "verified": true, "disputed": true, "stale": true,
}

// ValidRelTypes lists the built-in relationship types. The relationship_type
// metadata table (migration 000055) is the authoritative catalogue; this map
// remains as the fallback for repositories without metadata access and keeps
// every type that was ever valid (additive).
var ValidRelTypes = map[string]bool{
	"connected_to":      true,
	"hosted_on":         true,
	"depends_on":        true,
	"member_of":         true,
	"member_of_cluster": true,
	"powers":            true,
	"powered_by":        true,
	"runs_on":           true,
	"mounted_in":        true,
	"uplink_to":         true,
	"stores":            true,
	"monitors":          true,
	"backs_up":          true,
	// Extended standard catalogue (spec §11).
	"backed_up_by": true,
	"managed_by":   true,
	"manages":      true,
	"assigned_to":  true,
	"contains":     true,
	"contained_by": true,
	"parent_of":    true,
	"child_of":     true,
	"located_in":   true,
	"uses":         true,
	"used_by":      true,
}

// ValidSources lists the origin vocabulary for relationships (spec §12).
var ValidSources = map[string]bool{
	"manual": true, "discovery": true, "agent": true, "import": true,
	"api": true, "integration": true, "workflow": true, "rule": true,
}

// Repository defines persistence operations for relationships.
type Repository interface {
	List(ctx context.Context, orgID string, ciID string, page api.PaginationParams) ([]Relationship, int, error)
	Create(ctx context.Context, rel *Relationship) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Relationship, error)
	Delete(ctx context.Context, orgID, id string) error
}

// Traverser walks the configuration graph from a root CI up to maxDepth hops
// away, following relationships in both directions. maxDepth is clamped to the
// given bounds and maxNodes caps the number of distinct CIs returned.
type Traverser interface {
	TraverseFrom(ctx context.Context, orgID, rootCIID string, maxDepth, maxNodes int) ([]Relationship, error)
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

func (r *MemoryRepository) List(_ context.Context, orgID string, ciID string, page api.PaginationParams) ([]Relationship, int, error) {
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

func (r *MemoryRepository) Create(_ context.Context, rel *Relationship) error {
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

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateRequest) (*Relationship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rel, ok := r.items[id]
	if !ok || rel.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if req.Attributes != nil {
		if rel.Attributes == nil {
			rel.Attributes = make(map[string]any)
		}
		for k, v := range req.Attributes {
			rel.Attributes[k] = v
		}
	}
	if req.Confidence != nil {
		rel.Confidence = req.Confidence
	}
	if req.VerificationState != nil {
		rel.VerificationState = *req.VerificationState
	}
	if req.SourceSystem != nil {
		rel.SourceSystem = *req.SourceSystem
	}
	if req.Notes != nil {
		rel.Notes = *req.Notes
	}
	rel.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	out := *rel
	return &out, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rel, ok := r.items[id]
	if !ok || rel.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.items, id)
	return nil
}

// TraverseFrom walks the in-memory graph breadth-first from rootCIID. It
// mirrors the recursive-CTE traversal of the PostgreSQL repository for tests
// and the no-db development mode.
func (r *MemoryRepository) TraverseFrom(_ context.Context, orgID, rootCIID string, maxDepth, maxNodes int) ([]Relationship, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if maxDepth < 1 || maxNodes < 1 {
		return nil, nil
	}

	visited := map[string]bool{rootCIID: true}
	frontier := []string{rootCIID}
	result := make([]Relationship, 0)
	reported := map[string]bool{}

	for depth := 0; depth < maxDepth && len(frontier) > 0 && len(visited) <= maxNodes; depth++ {
		var next []string
		for _, id := range frontier {
			for _, rel := range r.items {
				if rel.OrganizationID != orgID {
					continue
				}
				if rel.SourceCIID != id && rel.TargetCIID != id {
					continue
				}
				neighborID := rel.TargetCIID
				if neighborID == id {
					neighborID = rel.SourceCIID
				}
				// Mirror the recursive CTE: an edge is reported when it is
				// first discovered; expansion continues only to endpoints
				// that are not yet on the visited set. Self-loops are only
				// reported when attached to the root (the CTE anchor).
				if !reported[rel.ID] && (neighborID != id || id == rootCIID) {
					reported[rel.ID] = true
					result = append(result, *rel)
				}
				if neighborID == "" || neighborID == id || visited[neighborID] {
					continue
				}
				visited[neighborID] = true
				next = append(next, neighborID)
			}
		}
		frontier = next
	}
	return result, nil
}

// TypeChecker validates a rel_type against the relationship_type metadata
// catalogue when available. Implementations return (true, nil) for known keys
// and (false, nil) for unknown ones.
type TypeChecker interface {
	Exists(ctx context.Context, orgID, key string) (bool, error)
}

// Handler provides HTTP handlers for relationship endpoints.
type Handler struct {
	repo  Repository
	types TypeChecker
}

// NewHandler creates a new relationship handler. An optional TypeChecker
// enables metadata-driven rel_type validation; without one the built-in
// ValidRelTypes catalogue is enforced.
func NewHandler(repo Repository, checkers ...TypeChecker) *Handler {
	h := &Handler{repo: repo}
	if len(checkers) > 0 {
		h.types = checkers[0]
	}
	return h
}

// RegisterRoutes registers relationship routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/relationships", h.List)
	r.Get("/api/v1/cis/{id}/relationships", h.List)
	r.Post("/api/v1/relationships", h.Create)
	r.Patch("/api/v1/relationships/{id}", h.Update)
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

	rels, total, err := h.repo.List(r.Context(), t.OrganizationID, ciID, page)
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
	if !h.validRelType(r, t.OrganizationID, req.RelType) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid rel_type")
		return
	}
	if req.Source != "" && !ValidSources[req.Source] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid source")
		return
	}
	if req.Confidence != nil && (*req.Confidence < 0 || *req.Confidence > 1) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "confidence must be between 0 and 1")
		return
	}

	rel := &Relationship{
		OrganizationID: t.OrganizationID,
		SourceCIID:     req.SourceCIID,
		TargetCIID:     req.TargetCIID,
		RelType:        req.RelType,
		Attributes:     req.Attributes,
		Source:         req.Source,
		Confidence:     req.Confidence,
		SourceSystem:   req.SourceSystem,
		Notes:          req.Notes,
	}
	if rel.Attributes == nil {
		rel.Attributes = make(map[string]any)
	}
	if rel.Source == "" {
		rel.Source = "manual"
	}
	if rel.Source == "manual" {
		// Manual relationships are born verified; they must never be removed
		// silently because discovery does not detect them (spec §12).
		rel.VerificationState = "verified"
	} else {
		rel.VerificationState = req.VerificationState
	}
	if rel.VerificationState == "" {
		// Mirror the column default so the response reports the state that is
		// actually persisted rather than an empty string.
		rel.VerificationState = "unverified"
	}
	if !ValidVerificationStates[rel.VerificationState] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid verification_state")
		return
	}

	if err := h.repo.Create(r.Context(), rel); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, rel)
}

// validRelType resolves the rel_type against the metadata catalogue when
// available, falling back to the built-in list.
func (h *Handler) validRelType(r *http.Request, orgID, relType string) bool {
	if h.types != nil {
		ok, err := h.types.Exists(r.Context(), orgID, relType)
		if err == nil {
			return ok
		}
	}
	return ValidRelTypes[relType]
}

// Update handles PATCH /api/v1/relationships/{id}. It lets an authorized user
// edit and verify an existing relationship (spec §13) without recreating it.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	if req.Confidence != nil && (*req.Confidence < 0 || *req.Confidence > 1) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "confidence must be between 0 and 1")
		return
	}
	if req.VerificationState != nil && !ValidVerificationStates[*req.VerificationState] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid verification_state")
		return
	}

	rel, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			api.WriteError(w, http.StatusNotFound, "Not Found", "relationship not found")
			return
		}
		if api.WriteDBError(w, err) {
			return
		}
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "could not update relationship")
		return
	}

	api.WriteJSON(w, http.StatusOK, rel)
}

// Delete handles DELETE /api/v1/relationships/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "relationship not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
