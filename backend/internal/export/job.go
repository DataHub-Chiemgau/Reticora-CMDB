package export

import (
	"context"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Job status values for an asynchronous export run.
const (
	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusCompleted = "completed"
	JobStatusFailed    = "failed"
	JobStatusExpired   = "expired"
)

// Job is an asynchronous export run whose result is rendered into blob
// storage and downloaded through a signed URL.
type Job struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	InitiatedBy    string     `json:"initiated_by,omitempty"`
	Format         string     `json:"format"`
	Status         string     `json:"status"`
	Filters        JobFilters `json:"filters"`
	ObjectKey      string     `json:"object_key,omitempty"`
	RowCount       *int       `json:"row_count,omitempty"`
	FileSizeBytes  *int64     `json:"file_size_bytes,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	// Scope is the creator's tenant scope at creation; the worker renders
	// the export with exactly this scope. Nil for jobs without a snapshot,
	// which the worker refuses.
	Scope *JobScope `json:"scope,omitempty"`
}

// JobScope snapshots a tenant scope. A nil list grants the whole
// organization in that dimension; a non-nil list restricts it to the listed
// ids (empty: no access, E-08).
type JobScope struct {
	Clients []string `json:"clients"`
	Sites   []string `json:"sites"`
	Teams   []string `json:"teams"`
}

// SnapshotScope records scope for a job.
func SnapshotScope(scope *database.TenantScope) *JobScope {
	set := func(s database.ScopeSet) []string {
		if s.IsAll() {
			return nil
		}
		if ids := s.IDs(); ids != nil {
			return ids
		}
		return []string{}
	}
	return &JobScope{Clients: set(scope.Clients), Sites: set(scope.Sites), Teams: set(scope.Teams)}
}

// TenantScope restores the snapshot as the scope of userID in orgID.
func (s *JobScope) TenantScope(orgID, userID string) database.TenantScope {
	set := func(ids []string) database.ScopeSet {
		if ids == nil {
			return database.AllScopes()
		}
		return database.ScopeIDs(ids...)
	}
	return database.TenantScope{OrgID: orgID, UserID: userID, Clients: set(s.Clients), Sites: set(s.Sites), Teams: set(s.Teams)}
}

// JobFilters narrows an export job to a subset of the tenant's CIs.
type JobFilters struct {
	Status   string `json:"status,omitempty"`
	CITypeID string `json:"ci_type_id,omitempty"`
	ClientID string `json:"client_id,omitempty"`
}

// CreateJobRequest is the payload for POST /api/v1/export/jobs.
type CreateJobRequest struct {
	Format  string     `json:"format"`
	Filters JobFilters `json:"filters,omitempty"`
}

// JobRepository defines persistence for export jobs. All methods are scoped
// to a tenant; GetJob and ListJobs return only jobs created by the principal
// of the request (EXP-01). Claiming pending jobs for the worker is the single
// cross-tenant operation and is never invoked from request paths.
type JobRepository interface {
	CreateJob(ctx context.Context, orgID string, job *Job) error
	GetJob(ctx context.Context, orgID, id string) (*Job, error)
	ListJobs(ctx context.Context, orgID string, limit, offset int) ([]Job, int, error)
	MarkRunning(ctx context.Context, orgID, id string, at time.Time) error
	CompleteJob(ctx context.Context, orgID, id string, objectKey string, rowCount int, fileSize int64, completedAt, expiresAt time.Time) error
	FailJob(ctx context.Context, orgID, id, message string, at time.Time) error
	ClaimPending(ctx context.Context, limit int) ([]Job, error)
}

// Validate checks a create request and returns a human-readable problem.
func (r *CreateJobRequest) Validate() error {
	switch r.Format {
	case "csv", "datev", "json":
	default:
		return fmt.Errorf("format must be 'csv', 'datev' or 'json'")
	}
	return nil
}
