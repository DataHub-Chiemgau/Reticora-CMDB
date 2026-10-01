package location

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// ErrAssetNotFound is returned when the asset of an entry does not exist or is
// not visible under the caller's tenant scope.
var ErrAssetNotFound = errors.New("asset not found")

// Repository stores asset location history.
type Repository interface {
	Record(ctx context.Context, e *Entry) error
	History(ctx context.Context, orgID, assetID string, page api.PaginationParams) ([]Entry, int, error)
}

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu      sync.RWMutex
	entries map[string][]*Entry
	seq     int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{entries: map[string][]*Entry{}}
}

func (r *MemoryRepository) Record(_ context.Context, e *Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e.ID = fmt.Sprintf("loc-%08d", r.seq)
	e.CreatedAt = time.Now().UTC()
	if e.RecordedAt.IsZero() {
		e.RecordedAt = e.CreatedAt
	}
	r.entries[e.AssetID] = append(r.entries[e.AssetID], e)
	return nil
}

func (r *MemoryRepository) History(_ context.Context, orgID, assetID string, page api.PaginationParams) ([]Entry, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Entry
	for _, e := range r.entries[assetID] {
		if e.OrganizationID == orgID {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RecordedAt.After(out[j].RecordedAt) })
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
