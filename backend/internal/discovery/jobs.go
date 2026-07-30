package discovery

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Discovery job types and statuses (mirrors the discovery_job table CHECKs).
const (
	JobTypeSweep = "sweep"
	JobTypePoll  = "poll"
	JobTypeFull  = "full"

	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusCompleted = "completed"
	JobStatusFailed    = "failed"
)

// Job represents a discovery/ingest job backed by the discovery_job table.
type Job struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	CollectorID    string         `json:"collector_id,omitempty"`
	JobType        string         `json:"job_type"`
	Status         string         `json:"status"`
	Config         map[string]any `json:"config"`
	ResultSummary  map[string]any `json:"result_summary,omitempty"`
	StartedAt      string         `json:"started_at,omitempty"`
	CompletedAt    string         `json:"completed_at,omitempty"`
	CreatedAt      string         `json:"created_at"`
}

// JobFilter narrows a discovery-job listing.
type JobFilter struct {
	Status      string
	CollectorID string
}

// CreateJobRequest is the payload for creating a discovery job.
type CreateJobRequest struct {
	CollectorID string         `json:"collector_id"`
	JobType     string         `json:"job_type"`
	Config      map[string]any `json:"config,omitempty"`
}

var validJobTypes = map[string]bool{
	JobTypeSweep: true,
	JobTypePoll:  true,
	JobTypeFull:  true,
}

// ---- MemoryRepository job methods ----

func (r *MemoryRepository) CreateJob(j *Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	j.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	if j.Status == "" {
		j.Status = JobStatusPending
	}
	if j.Config == nil {
		j.Config = map[string]any{}
	}
	j.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	stored := *j
	r.jobs[j.ID] = &stored
	return nil
}

func (r *MemoryRepository) ListJobs(orgID string, filter JobFilter, page api.PaginationParams) ([]Job, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Job
	for _, j := range r.jobs {
		if j.OrganizationID != orgID {
			continue
		}
		if filter.Status != "" && j.Status != filter.Status {
			continue
		}
		if filter.CollectorID != "" && j.CollectorID != filter.CollectorID {
			continue
		}
		result = append(result, *j)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })

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

func (r *MemoryRepository) GetJob(orgID, id string) (*Job, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	j, ok := r.jobs[id]
	if !ok || j.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	clone := *j
	return &clone, nil
}

// ---- Handlers ----

// ListJobs handles GET /api/v1/discovery/jobs
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	filter := JobFilter{
		Status:      r.URL.Query().Get("status"),
		CollectorID: r.URL.Query().Get("collector_id"),
	}
	jobs, total, err := h.repo.ListJobs(t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Job]{
		Data:    jobs,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// CreateJob handles POST /api/v1/discovery/jobs
func (h *Handler) CreateJob(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	var req CreateJobRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.CollectorID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "collector_id is required")
		return
	}
	if req.JobType == "" {
		req.JobType = JobTypeSweep
	}
	if !validJobTypes[req.JobType] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid job_type")
		return
	}

	job := &Job{
		OrganizationID: t.OrganizationID,
		CollectorID:    req.CollectorID,
		JobType:        req.JobType,
		Status:         JobStatusPending,
		Config:         req.Config,
	}
	if err := h.repo.CreateJob(job); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, job)
}

// GetJob handles GET /api/v1/discovery/jobs/{id}
func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	job, err := h.repo.GetJob(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "discovery job not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, job)
}
