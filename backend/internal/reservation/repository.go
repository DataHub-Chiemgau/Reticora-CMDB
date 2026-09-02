package reservation

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence for reservations.
type Repository interface {
	List(ctx context.Context, orgID string, state string, page api.PaginationParams) ([]Reservation, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Reservation, error)
	// Create inserts a reservation after checking for conflicts. Serialized
	// assets admit exactly one active reservation; quantity items are checked
	// against available stock by the service layer.
	Create(ctx context.Context, res *Reservation) error
	// Transition moves a reservation to a new state (release/expire/consume).
	Transition(ctx context.Context, orgID, id, state string) (*Reservation, error)
	// ActiveForItem returns the active reservations of one item.
	ActiveForItem(ctx context.Context, orgID, itemKind, itemID string) ([]Reservation, error)
	// ExpireDue marks active reservations past their expires_at as expired.
	ExpireDue(ctx context.Context, now time.Time) (int, error)
}

// MovementRecorder records reservation-related movements into the ledger.
type MovementRecorder interface {
	Record(ctx context.Context, orgID, itemKind, itemID, movementType string, quantity *float64, reason string) error
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Reservation
	seq   int
}

// NewMemoryRepository creates an empty in-memory reservation repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: map[string]*Reservation{}}
}

func (r *MemoryRepository) nextID() string {
	r.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
}

func (r *MemoryRepository) List(_ context.Context, orgID string, state string, page api.PaginationParams) ([]Reservation, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Reservation
	for _, res := range r.items {
		if res.OrganizationID != orgID {
			continue
		}
		if state != "" && res.State != state {
			continue
		}
		out = append(out, *res)
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Reservation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res, ok := r.items[id]
	if !ok || res.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	out := *res
	return &out, nil
}

func (r *MemoryRepository) Create(_ context.Context, res *Reservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if res.AssetID != "" {
		for _, existing := range r.items {
			if existing.OrganizationID == res.OrganizationID &&
				existing.AssetID == res.AssetID && existing.State == "active" {
				return fmt.Errorf("asset already has an active reservation")
			}
		}
	}
	res.ID = r.nextID()
	res.State = "active"
	if res.ReservedFrom.IsZero() {
		res.ReservedFrom = time.Now().UTC()
	}
	now := time.Now().UTC()
	res.CreatedAt = now
	res.UpdatedAt = now
	stored := *res
	r.items[res.ID] = &stored
	return nil
}

func (r *MemoryRepository) Transition(_ context.Context, orgID, id, state string) (*Reservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.items[id]
	if !ok || res.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	if res.State != "active" {
		return nil, fmt.Errorf("reservation is not active")
	}
	if !States[state] || state == "active" {
		return nil, fmt.Errorf("invalid target state %q", state)
	}
	res.State = state
	res.UpdatedAt = time.Now().UTC()
	out := *res
	return &out, nil
}

func (r *MemoryRepository) ActiveForItem(_ context.Context, orgID, itemKind, itemID string) ([]Reservation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Reservation
	for _, res := range r.items {
		if res.OrganizationID != orgID || res.State != "active" {
			continue
		}
		if itemKind == "asset" && res.AssetID == itemID {
			out = append(out, *res)
		}
		if itemKind == "quantity_item" && res.QuantityItemID == itemID {
			out = append(out, *res)
		}
	}
	return out, nil
}

func (r *MemoryRepository) ExpireDue(_ context.Context, now time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, res := range r.items {
		if res.State == "active" && res.ExpiresAt != nil && res.ExpiresAt.Before(now) {
			res.State = "expired"
			res.UpdatedAt = now
			count++
		}
	}
	return count, nil
}
