package security

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository stores security findings.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Finding, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Finding, error)
	Create(ctx context.Context, f *Finding) error
	Update(ctx context.Context, orgID, id string, req UpdateFindingRequest) (*Finding, error)
	// Summary aggregates open findings by severity for the security cockpit.
	Summary(ctx context.Context, orgID string) (map[string]int, error)
}

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu       sync.RWMutex
	findings map[string]*Finding
	seq      int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{findings: map[string]*Finding{}}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Finding, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Finding
	for _, f := range r.findings {
		if f.OrganizationID != orgID {
			continue
		}
		if filter.CIID != "" && f.CIID != filter.CIID {
			continue
		}
		if filter.Kind != "" && f.Kind != filter.Kind {
			continue
		}
		if filter.Severity != "" && f.Severity != filter.Severity {
			continue
		}
		if filter.Status != "" && f.Status != filter.Status {
			continue
		}
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DetectedAt.After(out[j].DetectedAt) })
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Finding, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.findings[id]
	if !ok || f.OrganizationID != orgID {
		return nil, fmt.Errorf("finding not found")
	}
	cp := *f
	return &cp, nil
}

func (r *MemoryRepository) Create(_ context.Context, f *Finding) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	f.ID = fmt.Sprintf("find-%08d", r.seq)
	if f.Status == "" {
		f.Status = "open"
	}
	if f.Severity == "" {
		f.Severity = "medium"
	}
	now := time.Now().UTC()
	f.CreatedAt = now
	f.UpdatedAt = now
	if f.DetectedAt.IsZero() {
		f.DetectedAt = now
	}
	r.findings[f.ID] = f
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateFindingRequest) (*Finding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.findings[id]
	if !ok || f.OrganizationID != orgID {
		return nil, fmt.Errorf("finding not found")
	}
	if req.Status != nil {
		f.Status = *req.Status
		if *req.Status == "resolved" || *req.Status == "false_positive" {
			now := time.Now().UTC()
			f.ResolvedAt = &now
		}
	}
	f.UpdatedAt = time.Now().UTC()
	cp := *f
	return &cp, nil
}

func (r *MemoryRepository) Summary(_ context.Context, orgID string) (map[string]int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]int{}
	for _, f := range r.findings {
		if f.OrganizationID == orgID && f.Status == "open" {
			out[f.Severity]++
		}
	}
	return out, nil
}
