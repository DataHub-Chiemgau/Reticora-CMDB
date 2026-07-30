// Package discovery provides collector and discovery job management for Reticora CMDB.
package discovery

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/wire"
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

// IngestItem is a single CI data point from discovery. Newly added identity,
// interface and relationship fields are optional so the JSON contract remains
// backwards compatible with existing collectors.
type IngestItem struct {
	Fingerprint  map[string]any `json:"fingerprint"`
	RawData      map[string]any `json:"raw_data"`
	CITypeName   string         `json:"ci_type_name"`
	Name         string         `json:"name,omitempty"`
	Manufacturer string         `json:"manufacturer,omitempty"`
	Model        string         `json:"model,omitempty"`
	SerialNumber string         `json:"serial_number,omitempty"`
	ManagementIP string         `json:"management_ip,omitempty"`

	// Extended identity fields used for neighbor resolution and topology.
	HardwareUUID string `json:"hardware_uuid,omitempty"`
	PrimaryMAC   string `json:"primary_mac,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	FQDN         string `json:"fqdn,omitempty"`

	// Interfaces discovered on the CI (MACs feed neighbor resolution).
	Interfaces []wire.IngestInterface `json:"interfaces,omitempty"`

	// Relationships discovered from the CI toward neighbors (L2/L3 topology).
	Relationships []wire.IngestRelationship `json:"relationships,omitempty"`
}

// BulkIngestResponse is the response for bulk ingest.
type BulkIngestResponse struct {
	Received      int    `json:"received"`
	Created       int    `json:"created"`
	Updated       int    `json:"updated"`
	Conflicts     int    `json:"conflicts"`
	ReviewItems   int    `json:"review_items"`
	Relationships int    `json:"relationships"`
	JobID         string `json:"job_id,omitempty"`
}

// Repository defines persistence operations for collectors, discovery jobs and
// reconciliation review items.
type Repository interface {
	ListCollectors(orgID string, page api.PaginationParams) ([]Collector, int, error)
	RegisterCollector(c *Collector) error
	Heartbeat(orgID, collectorID string) error

	// Discovery jobs.
	ListJobs(orgID string, filter JobFilter, page api.PaginationParams) ([]Job, int, error)
	CreateJob(j *Job) error
	GetJob(orgID, id string) (*Job, error)

	// Reconciliation review queue.
	ListReviewItems(orgID string, filter ReviewFilter, page api.PaginationParams) ([]ReviewItem, int, error)
	CreateReviewItem(item *ReviewItem) error
	GetReviewItem(orgID, id string) (*ReviewItem, error)
	ResolveReviewItem(orgID, id string, resolution Resolution) (*ReviewItem, error)
}

// MemoryRepository is an in-memory collector store.
type MemoryRepository struct {
	mu          sync.RWMutex
	collectors  map[string]*Collector
	jobs        map[string]*Job
	reviewItems map[string]*ReviewItem
	seq         int
}

// NewMemoryRepository creates a new in-memory discovery repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		collectors:  make(map[string]*Collector),
		jobs:        make(map[string]*Job),
		reviewItems: make(map[string]*ReviewItem),
	}
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
	repo    Repository
	ciRepo  ci.Repository
	relRepo relationship.Repository
}

// NewHandler creates a new discovery handler. ciRepo enables reconciliation and
// review-item resolution; the optional relRepo enables topology relationship
// derivation. Both may be nil (e.g. for --no-db smoke tests), in which case
// those features are skipped gracefully.
func NewHandler(repo Repository, ciRepo ci.Repository, relRepo ...relationship.Repository) *Handler {
	h := &Handler{repo: repo, ciRepo: ciRepo}
	if len(relRepo) > 0 {
		h.relRepo = relRepo[0]
	}
	return h
}

// RegisterRoutes registers discovery routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/collectors", h.ListCollectors)
	r.Post("/api/v1/collectors", h.RegisterCollector)
	r.Post("/api/v1/collectors/{id}/heartbeat", h.Heartbeat)
	r.Post("/api/v1/ingest/bulk", h.BulkIngest)
	// Spec-named alias for the bulk ingest endpoint.
	r.Post("/api/v1/discovery/ingest", h.BulkIngest)

	// Discovery jobs.
	r.Get("/api/v1/discovery/jobs", h.ListJobs)
	r.Post("/api/v1/discovery/jobs", h.CreateJob)
	r.Get("/api/v1/discovery/jobs/{id}", h.GetJob)

	// Reconciliation review queue.
	r.Get("/api/v1/discovery/review-items", h.ListReviewItems)
	r.Post("/api/v1/discovery/review-items/{id}/resolve", h.ResolveReviewItem)
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

	existing, _, err := h.ciRepo.List(r.Context(), t.OrganizationID, ci.FilterParams{}, api.PaginationParams{Limit: 10000, Offset: 0})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	// resolvedCIID[i] holds the CI id that item i resolved to (matched or
	// created). Conflicts leave it empty so no topology is derived for them.
	resolvedCIID := make([]string, len(req.Items))

	for i, item := range req.Items {
		result := Reconcile(existing, item)
		now := time.Now().UTC().Format(time.RFC3339)
		source := ci.SourceSweep
		attributes := map[string]any{
			"fingerprint": item.Fingerprint,
			"raw_data":    item.RawData,
		}

		switch result.Action {
		case ReconcileCreated:
			nowTime := time.Now().UTC()
			newItem := ci.Item{
				OrganizationID:  t.OrganizationID,
				CITypeID:        item.CITypeName,
				Name:            item.Name,
				Status:          "active",
				Manufacturer:    item.Manufacturer,
				Model:           item.Model,
				SerialNumber:    item.SerialNumber,
				HardwareUUID:    item.HardwareUUID,
				ManagementIP:    item.ManagementIP,
				PrimaryMAC:      item.PrimaryMAC,
				Hostname:        item.Hostname,
				FQDN:            item.FQDN,
				Attributes:      attributes,
				DiscoverySource: source,
				FirstSeenAt:     &nowTime,
				LastSeenAt:      &nowTime,
			}
			if err := h.ciRepo.Create(r.Context(), &newItem); err != nil {
				api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
				return
			}
			existing = append(existing, newItem)
			resolvedCIID[i] = newItem.ID
			resp.Created++
		case ReconcileMatched:
			updated, err := h.ciRepo.Update(r.Context(), t.OrganizationID, result.MatchedCIID, ci.UpdateRequest{
				Name:            stringPtr(item.Name),
				Manufacturer:    stringPtr(item.Manufacturer),
				Model:           stringPtr(item.Model),
				SerialNumber:    stringPtr(item.SerialNumber),
				ManagementIP:    stringPtr(item.ManagementIP),
				Attributes:      attributes,
				DiscoverySource: &source,
				LastSeenAt:      &now,
			})
			if err != nil {
				api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
				return
			}
			for j := range existing {
				if existing[j].ID == updated.ID {
					existing[j] = *updated
					break
				}
			}
			resolvedCIID[i] = updated.ID
			resp.Updated++
		case ReconcileConflict:
			resp.Conflicts++
			if reviewItem := reviewItemFromConflict(t.OrganizationID, item, result); reviewItem != nil {
				if err := h.repo.CreateReviewItem(reviewItem); err != nil {
					api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
					return
				}
				resp.ReviewItems++
			}
		}
	}

	resp.Relationships = h.deriveTopology(t.OrganizationID, req.Items, resolvedCIID, existing)

	api.WriteJSON(w, http.StatusAccepted, resp)
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
