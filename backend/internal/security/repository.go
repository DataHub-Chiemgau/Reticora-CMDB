package security

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository stores security findings.
type Repository interface {
	List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Finding, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Finding, error)
	// Create records a finding through Upsert.
	Create(ctx context.Context, f *Finding) error
	// Upsert records a reported finding by its fingerprint (Fingerprint,
	// unless the caller sets one): a new finding, or the existing one with
	// last_seen_at, severity, title, CVE fields and fixed_version of the
	// report; a resolved finding opens again with status_reason
	// reappeared, an acknowledged or false-positive one keeps its status.
	Upsert(ctx context.Context, f *Finding) (UpsertResult, error)
	// Update changes the status after CheckTransition; resolved_at is set
	// only by resolved and cleared otherwise.
	Update(ctx context.Context, orgID, id string, req UpdateFindingRequest) (*Finding, error)
	// Summary aggregates open findings by severity for the security cockpit.
	Summary(ctx context.Context, orgID string) (map[string]int, error)
}

// MemoryRepository is the in-memory implementation (tests, --no-db). It
// writes no events.
type MemoryRepository struct {
	mu       sync.RWMutex
	findings map[string]*Finding
	seq      int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{findings: map[string]*Finding{}}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Finding, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Finding
	for _, f := range r.findings {
		switch {
		case f.OrganizationID != orgID,
			filter.CIID != "" && f.CIID != filter.CIID,
			filter.Kind != "" && f.Kind != filter.Kind,
			filter.Severity != "" && f.Severity != filter.Severity,
			filter.Status != "" && f.Status != filter.Status,
			filter.Source != "" && f.Source != filter.Source,
			filter.CVE != "" && !slices.Contains(f.CVEIDs, filter.CVE):
			continue
		}
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DetectedAt.After(out[j].DetectedAt) })
	total := len(out)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return out[start:end], total, nil
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Finding, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.findings[id]
	if !ok || f.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *f
	return &cp, nil
}

// Create implements Repository.
func (r *MemoryRepository) Create(ctx context.Context, f *Finding) error {
	_, err := r.Upsert(ctx, f)
	return err
}

// Upsert implements Repository.
func (r *MemoryRepository) Upsert(_ context.Context, f *Finding) (UpsertResult, error) {
	prepare(f)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	for _, cur := range r.findings {
		if cur.OrganizationID != f.OrganizationID || cur.Fingerprint != f.Fingerprint {
			continue
		}
		res := UpsertResult{SeverityChanged: cur.Severity != f.Severity, Reappeared: cur.Status == StatusResolved}
		changed := res.SeverityChanged || res.Reappeared || cur.Title != f.Title || cur.Detail != f.Detail ||
			cur.PackageName != f.PackageName || cur.InstalledVersion != f.InstalledVersion || !slices.Equal(cur.CVEIDs, f.CVEIDs) ||
			!sameScore(cur.CVSSScore, f.CVSSScore) || cur.CVSSVector != f.CVSSVector || cur.KEV != f.KEV ||
			cur.AdvisoryID != f.AdvisoryID || cur.FixedVersion != f.FixedVersion
		cur.LastSeenAt = now
		cur.Severity, cur.Title, cur.CVEIDs = f.Severity, f.Title, slices.Clone(f.CVEIDs)
		cur.Detail, cur.PackageName, cur.InstalledVersion = f.Detail, f.PackageName, f.InstalledVersion
		cur.CVSSScore, cur.CVSSVector, cur.KEV, cur.AdvisoryID, cur.FixedVersion = f.CVSSScore, f.CVSSVector, f.KEV, f.AdvisoryID, f.FixedVersion
		if res.Reappeared {
			cur.Status, cur.StatusReason, cur.ResolvedAt = StatusOpen, ReasonReappeared, nil
		}
		if changed {
			cur.UpdatedAt = now
		}
		*f = *cur
		f.CVEIDs = slices.Clone(cur.CVEIDs)
		return res, nil
	}
	r.seq++
	f.ID = fmt.Sprintf("find-%08d", r.seq)
	f.CreatedAt, f.UpdatedAt, f.LastSeenAt = now, now, now
	if f.DetectedAt.IsZero() {
		f.DetectedAt = now
	}
	cp := *f
	cp.CVEIDs = slices.Clone(f.CVEIDs)
	r.findings[f.ID] = &cp
	return UpsertResult{Created: true}, nil
}

// sameScore compares two optional CVSS scores.
func sameScore(a, b *float64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// Update implements Repository.
func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateFindingRequest) (*Finding, error) {
	if req.Status == nil {
		return nil, errNoStatus
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.findings[id]
	if !ok || f.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if err := CheckTransition(f.Status, *req.Status, req.StatusReason); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	f.Status, f.StatusReason, f.ResolvedAt = *req.Status, "", nil
	if req.StatusReason != nil {
		f.StatusReason = *req.StatusReason
	}
	if f.Status == StatusResolved {
		f.ResolvedAt = &now
	}
	f.UpdatedAt = now
	cp := *f
	return &cp, nil
}

func (r *MemoryRepository) Summary(_ context.Context, orgID string) (map[string]int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]int{}
	for _, f := range r.findings {
		if f.OrganizationID == orgID && f.Status == StatusOpen {
			out[f.Severity]++
		}
	}
	return out, nil
}
