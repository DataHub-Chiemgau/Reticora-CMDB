package export

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

// JobFilters narrows an export job to a subset of the tenant's CIs (csv,
// json: status, ci_type_id, client_id, or a selection of ids), a selection
// of assets (csv, json with entity asset: ids, WP-782) or assets (datev:
// client_id, lifecycle_state, WP-310).
type JobFilters struct {
	// Entity is what a csv or json export renders: EntityCI (the default,
	// also empty) or EntityAsset, which only POST /api/v1/assets/bulk
	// creates (WP-782); POST /api/v1/exports refuses it.
	Entity         string `json:"entity,omitempty"`
	Status         string `json:"status,omitempty"`
	CITypeID       string `json:"ci_type_id,omitempty"`
	ClientID       string `json:"client_id,omitempty"`
	LifecycleState string `json:"lifecycle_state,omitempty"`
	// IDs selects the CIs of a csv or json export by id (WP-780, at most
	// MaxExportIDs): the visible, not deleted ones among them, sorted by
	// created_at and id. It stands alone, without the other filters.
	IDs []string `json:"ids,omitempty"`
}

// MaxExportIDs bounds filters.ids like a bulk request (API-08).
const MaxExportIDs = 500

// Entities of a csv or json export (filters.entity, WP-782).
const (
	EntityCI    = "ci"
	EntityAsset = "asset"
)

// IsAsset reports whether the filters export assets by id (entity asset).
func (f *JobFilters) IsAsset() bool { return f.Entity == EntityAsset }

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
	// Heartbeat renews the heartbeat (started_at) of a running job to at;
	// a job that no longer runs is left as it is (TEC-14, F-315).
	Heartbeat(ctx context.Context, orgID, id string, at time.Time) error
	// ReclaimStale puts running jobs whose heartbeat (started_at) lies
	// before the given time back to pending: their worker died (TEC-14).
	ReclaimStale(ctx context.Context, startedBefore time.Time) (int, error)
}

// Validate checks a create request and returns a human-readable problem.
// An asset export (entity asset) takes the format csv or json and a
// selection of ids only; POST /api/v1/exports refuses it beforehand.
// validation: fixed texts.
func (r *CreateJobRequest) Validate() error {
	switch r.Format {
	case "csv", "datev", "json":
	default:
		return fmt.Errorf("format must be 'csv', 'datev' or 'json'")
	}
	switch r.Filters.Entity {
	case "", EntityCI:
	case EntityAsset:
		if r.Format == FormatDATEV {
			return fmt.Errorf("filters.entity asset applies to the formats csv and json only")
		}
		if r.Filters.IDs == nil || r.Filters.Status != "" || r.Filters.CITypeID != "" || r.Filters.ClientID != "" ||
			r.Filters.LifecycleState != "" {
			return fmt.Errorf("filters.entity asset takes filters.ids only")
		}
	default:
		return fmt.Errorf("filters.entity must be 'ci' or 'asset'")
	}
	// DATEV exports assets (WP-310): the CI filters do not apply to it,
	// lifecycle_state applies to it only.
	if r.Format == FormatDATEV {
		if r.Filters.Status != "" {
			return fmt.Errorf("filters.status does not apply to the format datev; use filters.lifecycle_state")
		}
		if r.Filters.CITypeID != "" {
			return fmt.Errorf("filters.ci_type_id does not apply to the format datev")
		}
	} else if r.Filters.LifecycleState != "" {
		return fmt.Errorf("filters.lifecycle_state applies to the format datev only")
	}
	return r.Filters.validateIDs(r.Format)
}

// validateIDs checks filters.ids: csv and json only, 1 to MaxExportIDs
// UUIDs, without another filter. It normalizes them to their canonical
// lower-case form, each once.
// validation: fixed texts.
func (f *JobFilters) validateIDs(format string) error {
	if f.IDs == nil {
		return nil
	}
	switch {
	case format == FormatDATEV:
		return fmt.Errorf("filters.ids applies to the formats csv and json only")
	case len(f.IDs) == 0:
		return fmt.Errorf("filters.ids must name at least one id")
	case len(f.IDs) > MaxExportIDs:
		return fmt.Errorf("filters.ids takes at most %d ids", MaxExportIDs)
	case f.Status != "" || f.CITypeID != "" || f.ClientID != "":
		return fmt.Errorf("filters.ids cannot be combined with another filter")
	}
	seen := make(map[string]bool, len(f.IDs))
	ids := make([]string, 0, len(f.IDs))
	for _, id := range f.IDs {
		if !api.IsUUID(id) {
			if f.IsAsset() {
				return fmt.Errorf("filters.ids must hold asset ids (UUIDs)")
			}
			return fmt.Errorf("filters.ids must hold CI ids (UUIDs)")
		}
		id = strings.ToLower(id)
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	f.IDs = ids
	return nil
}
