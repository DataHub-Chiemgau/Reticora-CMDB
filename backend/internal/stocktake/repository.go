package stocktake

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
)

// Sentinel errors returned by repositories so the handler can select the
// appropriate HTTP status code instead of guessing from infrastructure
// failures.
var (
	// ErrNotFound is returned when the referenced stocktake does not exist.
	ErrNotFound = errors.New("stocktake not found")
	// ErrAlreadyCompleted is returned when a stocktake that is already in a
	// terminal state (completed or cancelled) is completed again.
	ErrAlreadyCompleted = errors.New("stocktake already completed or cancelled")
	// ErrInvalidTransition is returned when the status of the stocktake does
	// not allow the transition or the change (409, WP-848): start needs
	// planned, cancel planned or in_progress, a new area planned.
	ErrInvalidTransition = errors.New("the status of the stocktake does not allow this transition")
	// ErrNoArea is returned by Start for a stocktake without location_id
	// (422 on location_id, WP-848).
	ErrNoArea = errors.New("stocktake has no area")
	// ErrSnapshotUnavailable is returned by the in-memory repository for
	// start and items: it reaches neither the location tree nor the stock
	// of the bins (501, WP-848).
	ErrSnapshotUnavailable = errors.New("the expected stock needs the location tree and the stock of the bins")
	// ErrNoSnapshot is returned by AddScan and Complete for a stocktake
	// without frozen expected stock, a stocktake of an earlier release
	// without area included (409, WP-287): it can only be cancelled.
	ErrNoSnapshot = errors.New("the stocktake has no frozen expected stock")
	// ErrObjectNotFound is returned by AddScan for an asset or consumable
	// that is unknown or outside the scope (404, WP-287).
	ErrObjectNotFound = errors.New("scanned object not found")
	// ErrPlaceFound is returned by AddScan for a place found that is
	// unknown, outside the scope or outside the area, or no bin for a
	// consumable (422 on location_id, WP-287).
	ErrPlaceFound = errors.New("the place found is no location of the area")
)

// Status values of a stocktake (stocktake_status_check, 000012).
const (
	StatusPlanned    = "planned"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusCancelled  = "cancelled"
)

// Repository defines persistence operations for stocktakes.
type Repository interface {
	List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Stocktake, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Stocktake, error)
	Create(ctx context.Context, s *Stocktake) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Stocktake, error)
	Delete(ctx context.Context, orgID, id string) error
	// AddScan records a scan of an in-progress stocktake with snapshot
	// and classifies it (found when the object is in the snapshot, else
	// unexpected). It fails with ErrNotFound for an unknown stocktake,
	// ErrInvalidTransition when it is not in progress, ErrNoSnapshot
	// without snapshot, ErrObjectNotFound for an object outside the scope
	// and ErrPlaceFound for a place found outside the area.
	AddScan(ctx context.Context, scan *StockScan) error
	// ListScans returns a page of the scans, newest first (keyset over
	// scanned_at and id).
	ListScans(ctx context.Context, orgID, stocktakeID string, page api.PaginationParams) ([]StockScan, int, error)
	// Differences returns a page of the difference list, computed from the
	// snapshot and the scans (WP-287), ordered by object.
	Differences(ctx context.Context, orgID, stocktakeID string, page api.PaginationParams) ([]Difference, int, error)
	// Complete finalizes an in-progress stocktake with snapshot: it writes
	// counted_quantity and classification per item, an item per unexpected
	// object and the totals, then the status, in one transaction (WP-287).
	// It books no correction. A completed or cancelled stocktake fails with
	// ErrAlreadyCompleted, a planned one with ErrInvalidTransition, one
	// without snapshot with ErrNoSnapshot.
	Complete(ctx context.Context, orgID, id string) (*Completion, error)
	// Start moves a planned stocktake with an area to in_progress and
	// freezes the expected stock of the area in one transaction (WP-848).
	// It fails with ErrInvalidTransition for another status and with
	// ErrNoArea without location_id.
	Start(ctx context.Context, orgID, id, userID string) (*Stocktake, error)
	// Cancel moves a planned or in-progress stocktake to cancelled;
	// another status fails with ErrInvalidTransition.
	Cancel(ctx context.Context, orgID, id string) (*Stocktake, error)
	// ListItems returns a page of the frozen expected stock, ordered by id.
	ListItems(ctx context.Context, orgID, id string, page api.PaginationParams) ([]Item, int, error)
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu         sync.RWMutex
	stocktakes map[string]*Stocktake
	nextID     int
}

// NewMemoryRepository creates a new in-memory stocktake repository (profile
// --no-db). It freezes no expected stock: start, items and the difference
// list answer 501, scans and the completion 409 (WP-287). The asset lookup
// of the wiring is no longer read.
func NewMemoryRepository(_ asset.Lookup) *MemoryRepository {
	return &MemoryRepository{stocktakes: make(map[string]*Stocktake)}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Stocktake, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Stocktake
	for _, s := range r.stocktakes {
		if s.OrganizationID != orgID {
			continue
		}
		if filter.Status != "" && s.Status != filter.Status {
			continue
		}
		if filter.Scope != "" && s.Scope != filter.Scope {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(s.Title), strings.ToLower(filter.Search)) {
			continue
		}
		result = append(result, *s)
	}

	total := len(result)

	column, direction := NormalizeSort(filter)
	sort.Slice(result, func(i, j int) bool {
		vi, vj := sortValue(&result[i], column), sortValue(&result[j], column)
		if vi == vj {
			if direction == "asc" {
				return result[i].ID < result[j].ID
			}
			return result[i].ID > result[j].ID
		}
		if direction == "asc" {
			return vi < vj
		}
		return vi > vj
	})

	if page.Cursor != nil {
		if !page.Cursor.Matches(column, direction) {
			return nil, 0, fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
		}
		remaining := make([]Stocktake, 0, len(result))
		for i := range result {
			item := &result[i]
			if api.KeysetCompare(sortValue(item, column), item.ID, *page.Cursor, direction) {
				remaining = append(remaining, *item)
			}
		}
		if len(remaining) > page.Limit {
			remaining = remaining[:page.Limit]
		}
		return remaining, total, nil
	}

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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Stocktake, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	return s, nil
}

func (r *MemoryRepository) Create(_ context.Context, s *Stocktake) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	s.ID = fmt.Sprintf("st-%d", r.nextID)
	now := time.Now().UTC()
	s.CreatedAt = now
	s.UpdatedAt = now
	r.stocktakes[s.ID] = s
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateRequest) (*Stocktake, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}

	if req.LocationID != nil {
		if s.Status != StatusPlanned {
			return nil, ErrInvalidTransition
		}
		s.LocationID = *req.LocationID
	}
	if req.Title != nil {
		s.Title = *req.Title
	}
	if req.Description != nil {
		s.Description = *req.Description
	}
	if req.DueDate != nil {
		s.DueDate = *req.DueDate
	}
	s.UpdatedAt = time.Now().UTC()
	return s, nil
}

// Start follows the status and area rules of the PostgreSQL repository and
// then fails with ErrSnapshotUnavailable: the in-memory repository reaches
// neither the location tree nor the stock of the bins.
func (r *MemoryRepository) Start(_ context.Context, orgID, id, _ string) (*Stocktake, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if s.Status != StatusPlanned {
		return nil, ErrInvalidTransition
	}
	if s.LocationID == "" {
		return nil, ErrNoArea
	}
	return nil, ErrSnapshotUnavailable
}

// Cancel moves a planned or in-progress stocktake to cancelled.
func (r *MemoryRepository) Cancel(_ context.Context, orgID, id string) (*Stocktake, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if s.Status != StatusPlanned && s.Status != StatusInProgress {
		return nil, ErrInvalidTransition
	}
	s.Status = StatusCancelled
	s.UpdatedAt = time.Now().UTC()
	out := *s
	return &out, nil
}

// ListItems fails with ErrSnapshotUnavailable for a known stocktake: the
// in-memory repository freezes no expected stock.
func (r *MemoryRepository) ListItems(_ context.Context, orgID, id string, _ api.PaginationParams) ([]Item, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, 0, ErrNotFound
	}
	return nil, 0, ErrSnapshotUnavailable
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.stocktakes, id)
	return nil
}

// AddScan follows the status rule of the PostgreSQL repository: a
// stocktake that is not in progress is ErrInvalidTransition, an
// in-progress one ErrNoSnapshot, since the in-memory repository freezes no
// expected stock.
func (r *MemoryRepository) AddScan(_ context.Context, scan *StockScan) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	st, ok := r.stocktakes[scan.StocktakeID]
	if !ok || st.OrganizationID != scan.OrganizationID {
		return ErrNotFound
	}
	if st.Status != StatusInProgress {
		return ErrInvalidTransition
	}
	return ErrNoSnapshot
}

// Differences fails with ErrSnapshotUnavailable for a known stocktake.
func (r *MemoryRepository) Differences(_ context.Context, orgID, stocktakeID string, _ api.PaginationParams) ([]Difference, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	st, ok := r.stocktakes[stocktakeID]
	if !ok || st.OrganizationID != orgID {
		return nil, 0, ErrNotFound
	}
	return nil, 0, ErrSnapshotUnavailable
}

// Complete follows the status rule of the PostgreSQL repository; an
// in-progress stocktake has no snapshot here (ErrNoSnapshot), so nothing
// changes.
func (r *MemoryRepository) Complete(_ context.Context, orgID, id string) (*Completion, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	return nil, completeRule(s)
}

// completeRule returns the error of the completion of s: ErrAlreadyCompleted
// when it is completed or cancelled, ErrInvalidTransition when it is
// planned, ErrNoSnapshot without frozen expected stock, else nil.
func completeRule(s *Stocktake) error {
	switch {
	case s.Status == StatusCompleted || s.Status == StatusCancelled:
		return ErrAlreadyCompleted
	case s.Status != StatusInProgress:
		return ErrInvalidTransition
	case !s.hasSnapshot():
		return ErrNoSnapshot
	}
	return nil
}

// hasSnapshot reports whether start froze the expected stock of s: only a
// stocktake with area is started (WP-848).
func (s *Stocktake) hasSnapshot() bool {
	return s.LocationID != "" && s.StartedAt != ""
}

// ListScans returns no scans: the in-memory repository records none.
func (r *MemoryRepository) ListScans(_ context.Context, orgID, stocktakeID string, _ api.PaginationParams) ([]StockScan, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	st, ok := r.stocktakes[stocktakeID]
	if !ok || st.OrganizationID != orgID {
		return nil, 0, ErrNotFound
	}
	return []StockScan{}, 0, nil
}
