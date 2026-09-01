package desk

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository stores desks and their bookings.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Desk, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Desk, error)
	Create(ctx context.Context, d *Desk) error
	Update(ctx context.Context, orgID, id string, req UpdateDeskRequest) (*Desk, error)
	Delete(ctx context.Context, orgID, id string) error
	// Book reserves a desk for a time window; it fails on overlap with an
	// existing active booking of the same desk.
	Book(ctx context.Context, b *Booking) (*Booking, error)
	CancelBooking(ctx context.Context, orgID, bookingID string) (*Booking, error)
	ListBookings(ctx context.Context, orgID, deskID string) ([]Booking, error)
}

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu       sync.RWMutex
	desks    map[string]*Desk
	bookings map[string][]*Booking
	seq      int
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{desks: map[string]*Desk{}, bookings: map[string][]*Booking{}}
}

func (r *MemoryRepository) nextID(prefix string) string {
	r.seq++
	return fmt.Sprintf("%s-%08d", prefix, r.seq)
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Desk, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Desk
	for _, d := range r.desks {
		if d.OrganizationID != orgID {
			continue
		}
		if filter.RoomID != "" && d.RoomID != filter.RoomID {
			continue
		}
		if filter.Status != "" && d.Status != filter.Status {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(d.Name), strings.ToLower(filter.Search)) {
			continue
		}
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Desk, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.desks[id]
	if !ok || d.OrganizationID != orgID {
		return nil, fmt.Errorf("desk not found")
	}
	cp := *d
	return &cp, nil
}

func (r *MemoryRepository) Create(_ context.Context, d *Desk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d.ID = r.nextID("desk")
	if d.Status == "" {
		d.Status = "available"
	}
	if d.Attributes == nil {
		d.Attributes = map[string]any{}
	}
	now := time.Now().UTC()
	d.CreatedAt = now
	d.UpdatedAt = now
	r.desks[d.ID] = d
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateDeskRequest) (*Desk, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.desks[id]
	if !ok || d.OrganizationID != orgID {
		return nil, fmt.Errorf("desk not found")
	}
	if req.RoomID != nil {
		d.RoomID = *req.RoomID
	}
	if req.Name != nil {
		d.Name = *req.Name
	}
	if req.Status != nil {
		d.Status = *req.Status
	}
	if req.Attributes != nil {
		d.Attributes = req.Attributes
	}
	d.UpdatedAt = time.Now().UTC()
	cp := *d
	return &cp, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.desks[id]
	if !ok || d.OrganizationID != orgID {
		return fmt.Errorf("desk not found")
	}
	delete(r.desks, id)
	delete(r.bookings, id)
	return nil
}

func (r *MemoryRepository) Book(_ context.Context, b *Booking) (*Booking, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.desks[b.DeskID]
	if !ok || d.OrganizationID != b.OrganizationID {
		return nil, fmt.Errorf("desk not found")
	}
	if !b.EndsAt.After(b.StartsAt) {
		return nil, fmt.Errorf("ends_at must be after starts_at")
	}
	for _, existing := range r.bookings[b.DeskID] {
		if existing.Status != "active" {
			continue
		}
		if b.StartsAt.Before(existing.EndsAt) && b.EndsAt.After(existing.StartsAt) {
			return nil, fmt.Errorf("desk already booked in this time window")
		}
	}
	b.ID = r.nextID("bk")
	b.Status = "active"
	b.CreatedAt = time.Now().UTC()
	r.bookings[b.DeskID] = append(r.bookings[b.DeskID], b)
	cp := *b
	return &cp, nil
}

func (r *MemoryRepository) CancelBooking(_ context.Context, orgID, bookingID string) (*Booking, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, list := range r.bookings {
		for _, b := range list {
			if b.ID == bookingID && b.OrganizationID == orgID {
				b.Status = "cancelled"
				cp := *b
				return &cp, nil
			}
		}
	}
	return nil, fmt.Errorf("booking not found")
}

func (r *MemoryRepository) ListBookings(_ context.Context, orgID, deskID string) ([]Booking, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Booking
	for _, b := range r.bookings[deskID] {
		if b.OrganizationID == orgID {
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out, nil
}
