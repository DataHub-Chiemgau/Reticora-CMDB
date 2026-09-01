package disposal

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository stores disposal records. There is intentionally no Update or
// Delete: disposal records are revision-safe (append-only).
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Record, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Record, error)
	Create(ctx context.Context, rec *Record) error
}

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu      sync.RWMutex
	records map[string]*Record
	seq     int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{records: map[string]*Record{}}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Record, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Record
	for _, rec := range r.records {
		if rec.OrganizationID != orgID {
			continue
		}
		if filter.Method != "" && rec.Method != filter.Method {
			continue
		}
		if filter.AssetID != "" && rec.AssetID != filter.AssetID {
			continue
		}
		if filter.CIID != "" && rec.CIID != filter.CIID {
			continue
		}
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PerformedAt.After(out[j].PerformedAt) })
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Record, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[id]
	if !ok || rec.OrganizationID != orgID {
		return nil, fmt.Errorf("disposal record not found")
	}
	cp := *rec
	return &cp, nil
}

func (r *MemoryRepository) Create(_ context.Context, rec *Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	rec.ID = fmt.Sprintf("disp-%08d", r.seq)
	rec.CreatedAt = time.Now().UTC()
	r.records[rec.ID] = rec
	return nil
}
