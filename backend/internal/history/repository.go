package history

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence for entity change rows.
type Repository interface {
	// Record appends a change row. The trail is append-only.
	Record(ctx context.Context, change *Change) error
	// List returns the changes of one entity, newest first; audit entries
	// are part of a CI's history only with opts.IncludeAudit (CI-06, E-78),
	// opts.ChangeKind keeps the CI changes of that kind (AUD-03).
	List(ctx context.Context, orgID, entityType, entityID string, page api.PaginationParams, opts ListOptions) ([]Change, int, error)
	// TrailUpTo returns the changes of one entity up to a point in time,
	// oldest first, for point-in-time reconstruction.
	TrailUpTo(ctx context.Context, orgID, entityType, entityID string, at time.Time) ([]Change, error)
	// Visible reports whether the entity of a state snapshot exists under
	// the caller's tenant scope (JOB-04).
	Visible(ctx context.Context, orgID, entityType, entityID string) (bool, error)
	// StateAt reconstructs the state of an entity at req.At in one tenant
	// transaction (AUD-08, WP-793): base, trail, edges and the resolution
	// of the personal values. An entity the caller sees neither as row nor,
	// for a CI, as ci_snapshot is api.ErrNotFound; one without a recorded
	// change up to req.At is a *NoRecordedStateError.
	StateAt(ctx context.Context, orgID string, req *StateRequest) (*StateSnapshot, error)
}

// StateRequest asks StateAt for the state of an entity (ci or asset) at a
// point in time. Reveal resolves the vault surrogates of the values (a
// deleted vault entry becomes null, SEC-05); without it the state keeps
// them (the result of the snapshot job). Relationships adds the edges of a
// CI at At, visible under the caller's scope and projected by the
// relationship grant of the principal of the context.
type StateRequest struct {
	EntityType    string
	EntityID      string
	At            time.Time
	Reveal        bool
	Relationships bool
}

// CITimeline reads the timeline of a CI for the GraphQL BFF (ciHistory,
// WP-792): the unified history of List (one entry per ci_change change set,
// entity_change, location_change, the audit entries with
// opts.IncludeAudit) and, with opts.IncludeRelationships, the entries of
// relationship_change of its edges (source SourceRelationshipChange),
// within the time window of opts, newest first by the time of recording.
// The relationship entries carry the fields the principal of ctx may read
// (relationship.ProjectChanges, as GET /api/v1/relationships/{id}/changes).
// A CI the caller cannot see is api.ErrNotFound. List and REST GET
// /api/v1/history/{entityType}/{id} stay as they are.
type CITimeline interface {
	CITimeline(ctx context.Context, orgID, ciID string, page api.PaginationParams, opts TimelineOptions) ([]Change, int, error)
}

// within reports whether at lies in the window of opts.
func (opts *TimelineOptions) within(at time.Time) bool {
	return (opts.From.IsZero() || !at.Before(opts.From)) && (opts.To.IsZero() || !at.After(opts.To))
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu      sync.RWMutex
	changes []Change
	seq     int
}

// NewMemoryRepository creates an empty in-memory history repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{}
}

// Record appends a change row.
func (r *MemoryRepository) Record(_ context.Context, change *Change) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	change.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
	if change.CreatedAt.IsZero() {
		change.CreatedAt = time.Now().UTC()
	}
	r.changes = append(r.changes, *change)
	return nil
}

// List returns the changes of one entity, newest first; the in-memory
// trail holds no audit entries.
func (r *MemoryRepository) List(_ context.Context, orgID, entityType, entityID string, page api.PaginationParams, opts ListOptions) ([]Change, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Change
	for i := range r.changes {
		c := &r.changes[i]
		if c.OrganizationID == orgID && c.EntityType == entityType && c.EntityID == entityID &&
			(opts.ChangeKind == "" || c.ChangeKind == opts.ChangeKind) {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	total := len(out)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return out[start:end], total, nil
}

// CITimeline implements CITimeline: the in-memory trail holds neither
// audit entries nor relationship changes, so it is the changes of the CI
// within the window, newest first.
func (r *MemoryRepository) CITimeline(_ context.Context, orgID, ciID string, page api.PaginationParams, opts TimelineOptions) ([]Change, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Change{}
	for i := range r.changes {
		c := &r.changes[i]
		if c.OrganizationID == orgID && c.EntityType == "ci" && c.EntityID == ciID && opts.within(c.CreatedAt) {
			out = append(out, *c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	total := len(out)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return out[start:end], total, nil
}

// TrailUpTo returns changes oldest-first for replay.
func (r *MemoryRepository) TrailUpTo(_ context.Context, orgID, entityType, entityID string, at time.Time) ([]Change, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Change
	for i := range r.changes {
		c := &r.changes[i]
		if c.OrganizationID == orgID && c.EntityType == entityType && c.EntityID == entityID &&
			!c.CreatedAt.After(at) {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// StateAt replays the in-memory trail of the entity up to req.At: it
// keeps no snapshots, no surrogates and no edges, so a CI has an empty
// edge list under req.Relationships.
func (r *MemoryRepository) StateAt(ctx context.Context, orgID string, req *StateRequest) (*StateSnapshot, error) {
	visible, err := r.Visible(ctx, orgID, req.EntityType, req.EntityID)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, api.ErrNotFound
	}
	trail, err := r.TrailUpTo(ctx, orgID, req.EntityType, req.EntityID, req.At)
	if err != nil {
		return nil, err
	}
	if len(trail) == 0 {
		return nil, &NoRecordedStateError{At: req.At}
	}
	s := Replay(trail)
	if req.Relationships && req.EntityType == "ci" {
		s.Relationships = &[]EdgeState{}
	}
	return &s, nil
}

// Visible reports whether a change of the entity was recorded: the
// in-memory trail knows no entity rows.
func (r *MemoryRepository) Visible(_ context.Context, orgID, entityType, entityID string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.changes {
		if c := &r.changes[i]; c.OrganizationID == orgID && c.EntityType == entityType && c.EntityID == entityID {
			return true, nil
		}
	}
	return false, nil
}
