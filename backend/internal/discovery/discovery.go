// Package discovery provides collector and discovery job management for Reticora CMDB.
package discovery

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Collector represents a customer-deployed collector instance.
type Collector struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	ClientID       string         `json:"client_id,omitempty"`
	Name           string         `json:"name"`
	Version        string         `json:"version,omitempty"`
	Status         string         `json:"status"`
	LastHeartbeat  string         `json:"last_heartbeat,omitempty"`
	Config         map[string]any `json:"config"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// BulkIngestRequest is the payload sent by a collector for bulk CI data.
type BulkIngestRequest struct {
	CollectorID string       `json:"collector_id"`
	Items       []IngestItem `json:"items"`
}

// IngestItem is a single CI data point from discovery.
type IngestItem struct {
	Fingerprint  map[string]any `json:"fingerprint"`
	RawData      map[string]any `json:"raw_data"`
	CITypeName   string         `json:"ci_type_name"`
	Name         string         `json:"name,omitempty"`
	Manufacturer string         `json:"manufacturer,omitempty"`
	Model        string         `json:"model,omitempty"`
	SerialNumber string         `json:"serial_number,omitempty"`
	ManagementIP string         `json:"management_ip,omitempty"`
}

// BulkIngestResponse is the response for bulk ingest.
type BulkIngestResponse struct {
	Received  int `json:"received"`
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Conflicts int `json:"conflicts"`
}

// Repository defines persistence operations for collectors.
type Repository interface {
	ListCollectors(orgID string, page api.PaginationParams) ([]Collector, int, error)
	RegisterCollector(c *Collector) error
	Heartbeat(orgID, collectorID string) error
}

// MemoryRepository is an in-memory collector store.
type MemoryRepository struct {
	mu         sync.RWMutex
	collectors map[string]*Collector
	seq        int
}

// NewMemoryRepository creates a new in-memory discovery repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{collectors: make(map[string]*Collector)}
}

func (r *MemoryRepository) ListCollectors(orgID string, page api.PaginationParams) ([]Collector, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Collector
	for _, c := range r.collectors {
		if c.OrganizationID != orgID {
			continue
		}
		result = append(result, *c)
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

func (r *MemoryRepository) RegisterCollector(c *Collector) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	c.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	c.CreatedAt = now
	c.UpdatedAt = now
	c.Status = "online"
	c.LastHeartbeat = now
	r.collectors[c.ID] = c
	return nil
}

func (r *MemoryRepository) Heartbeat(orgID, collectorID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.collectors[collectorID]
	if !ok || c.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	c.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)
	c.Status = "online"
	return nil
}

// Handler provides HTTP handlers for discovery endpoints.
type Handler struct {
	repo   Repository
	ciRepo ci.Repository
}

// NewHandler creates a new discovery handler.
func NewHandler(repo Repository, ciRepo ...ci.Repository) *Handler {
	h := &Handler{repo: repo}
	if len(ciRepo) > 0 {
		h.ciRepo = ciRepo[0]
	}
	return h
}

// RegisterRoutes registers discovery routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/collectors", h.ListCollectors)
	r.Post("/api/v1/collectors", h.RegisterCollector)
	r.Post("/api/v1/collectors/{id}/heartbeat", h.Heartbeat)
	r.Post("/api/v1/ingest/bulk", h.BulkIngest)
}

// ListCollectors handles GET /api/v1/collectors
func (h *Handler) ListCollectors(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	collectors, total, err := h.repo.ListCollectors(t.OrganizationID, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Collector]{
		Data:    collectors,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// RegisterCollector handles POST /api/v1/collectors
func (h *Handler) RegisterCollector(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var c Collector
	if err := api.ReadJSON(r, &c); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if c.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}

	c.OrganizationID = t.OrganizationID
	if c.Config == nil {
		c.Config = make(map[string]any)
	}

	if err := h.repo.RegisterCollector(&c); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, c)
}

// Heartbeat handles POST /api/v1/collectors/{id}/heartbeat
func (h *Handler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Heartbeat(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "collector not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// BulkIngest handles POST /api/v1/ingest/bulk.
func (h *Handler) BulkIngest(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req BulkIngestRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if len(req.Items) == 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "items cannot be empty")
		return
	}

	resp := BulkIngestResponse{Received: len(req.Items)}
	if h.ciRepo == nil {
		api.WriteJSON(w, http.StatusAccepted, resp)
		return
	}

	existing, _, err := h.ciRepo.List(t.OrganizationID, ci.FilterParams{}, api.PaginationParams{Limit: 10000, Offset: 0})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	for _, item := range req.Items {
		result := Reconcile(existing, item)
		now := time.Now().UTC().Format(time.RFC3339)
		source := "discovery"
		attributes := map[string]any{
			"fingerprint": item.Fingerprint,
			"raw_data":    item.RawData,
		}

		switch result.Action {
		case ReconcileCreated:
			newItem := ci.Item{
				OrganizationID: t.OrganizationID,
				CITypeID:       item.CITypeName,
				Name:           item.Name,
				Status:         "active",
				Manufacturer:   item.Manufacturer,
				Model:          item.Model,
				SerialNumber:   item.SerialNumber,
				ManagementIP:   item.ManagementIP,
				Attributes:     attributes,
				Source:         source,
				LastSeen:       now,
			}
			if err := h.ciRepo.Create(&newItem); err != nil {
				api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
				return
			}
			existing = append(existing, newItem)
			resp.Created++
		case ReconcileMatched:
			updated, err := h.ciRepo.Update(t.OrganizationID, result.MatchedCIID, ci.UpdateRequest{
				Name:         stringPtr(item.Name),
				Manufacturer: stringPtr(item.Manufacturer),
				Model:        stringPtr(item.Model),
				SerialNumber: stringPtr(item.SerialNumber),
				ManagementIP: stringPtr(item.ManagementIP),
				Attributes:   attributes,
				Source:       &source,
				LastSeen:     &now,
			})
			if err != nil {
				api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
				return
			}
			for i := range existing {
				if existing[i].ID == updated.ID {
					existing[i] = *updated
					break
				}
			}
			resp.Updated++
		case ReconcileConflict:
			resp.Conflicts++
		}
	}

	api.WriteJSON(w, http.StatusAccepted, resp)
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
