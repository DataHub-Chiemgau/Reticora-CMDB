package maintenance

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository stores maintenance windows and notifications.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Window, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Window, error)
	Create(ctx context.Context, w *Window) error
	Update(ctx context.Context, orgID, id string, req UpdateWindowRequest) (*Window, error)
	Delete(ctx context.Context, orgID, id string) error
	// NotifyClients derives the affected clients from the window's CIs and
	// records one notification per client. Returns the notifications.
	NotifyClients(ctx context.Context, orgID, windowID string) ([]Notification, error)
	ListNotifications(ctx context.Context, orgID, windowID string) ([]Notification, error)
}

// MemoryRepository is the in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu    sync.RWMutex
	wins  map[string]*Window
	notif map[string][]*Notification
	seq   int
	// ciClient maps CI id → client id so NotifyClients can derive clients
	// without a CI repository dependency in memory mode.
	ciClient map[string]string
}

// NewMemoryRepository creates an empty in-memory store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{wins: map[string]*Window{}, notif: map[string][]*Notification{}, ciClient: map[string]string{}}
}

// SetCIClient registers a CI→client mapping for notification derivation (tests).
func (r *MemoryRepository) SetCIClient(ciID, clientID string) { r.ciClient[ciID] = clientID }

func (r *MemoryRepository) nextID(prefix string) string {
	r.seq++
	return fmt.Sprintf("%s-%08d", prefix, r.seq)
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Window, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Window
	for _, w := range r.wins {
		if w.OrganizationID != orgID {
			continue
		}
		if filter.Status != "" && w.Status != filter.Status {
			continue
		}
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
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

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Window, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.wins[id]
	if !ok || w.OrganizationID != orgID {
		return nil, fmt.Errorf("maintenance window not found")
	}
	cp := *w
	return &cp, nil
}

func (r *MemoryRepository) Create(_ context.Context, w *Window) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w.ID = r.nextID("mw")
	if w.Status == "" {
		w.Status = "scheduled"
	}
	now := time.Now().UTC()
	w.CreatedAt = now
	w.UpdatedAt = now
	r.wins[w.ID] = w
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateWindowRequest) (*Window, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.wins[id]
	if !ok || w.OrganizationID != orgID {
		return nil, fmt.Errorf("maintenance window not found")
	}
	if req.Title != nil {
		w.Title = *req.Title
	}
	if req.Description != nil {
		w.Description = *req.Description
	}
	if req.Status != nil {
		w.Status = *req.Status
	}
	w.UpdatedAt = time.Now().UTC()
	cp := *w
	return &cp, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.wins[id]
	if !ok || w.OrganizationID != orgID {
		return fmt.Errorf("maintenance window not found")
	}
	delete(r.wins, id)
	delete(r.notif, id)
	return nil
}

func (r *MemoryRepository) NotifyClients(_ context.Context, orgID, windowID string) ([]Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.wins[windowID]
	if !ok || w.OrganizationID != orgID {
		return nil, fmt.Errorf("maintenance window not found")
	}
	seen := map[string]bool{}
	var out []Notification
	for _, ciID := range w.CIIDs {
		clientID := r.ciClient[ciID]
		if clientID == "" || seen[clientID] {
			continue
		}
		seen[clientID] = true
		now := time.Now().UTC()
		n := &Notification{
			ID:             r.nextID("mn"),
			OrganizationID: orgID,
			WindowID:       windowID,
			ClientID:       clientID,
			Channel:        "webhook",
			Status:         "sent",
			SentAt:         &now,
			CreatedAt:      now,
		}
		r.notif[windowID] = append(r.notif[windowID], n)
		out = append(out, *n)
	}
	return out, nil
}

func (r *MemoryRepository) ListNotifications(_ context.Context, orgID, windowID string) ([]Notification, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Notification
	for _, n := range r.notif[windowID] {
		if n.OrganizationID == orgID {
			out = append(out, *n)
		}
	}
	return out, nil
}
