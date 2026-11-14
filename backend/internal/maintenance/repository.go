package maintenance

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// errWindowNotFound is an unknown or invisible window (404, F-367).
var errWindowNotFound = fmt.Errorf("maintenance window not found: %w", api.ErrNotFound)

// Repository stores maintenance windows and notifications.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Window, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Window, error)
	Create(ctx context.Context, w *Window) error
	Update(ctx context.Context, orgID, id string, req UpdateWindowRequest) (*Window, error)
	Delete(ctx context.Context, orgID, id string) error
	// NotifyClients announces the window to the clients of plan (nil: the
	// clients of the window's own CIs), one notification of the channel
	// notify per client; a client with a pending or sent notification is
	// not announced again, a failed one anew. Returns the notifications of
	// the clients.
	NotifyClients(ctx context.Context, orgID, windowID string, plan *NotifyPlan) ([]Notification, error)
	ListNotifications(ctx context.Context, orgID, windowID string) ([]Notification, error)
	// ActiveWindows returns the windows active at at: starts_at <= at <
	// ends_at and not cancelled (ALR-11, WP-1033), with their CIs.
	ActiveWindows(ctx context.Context, orgID string, at time.Time) ([]Window, error)
	// WindowsBetween returns the windows that are not cancelled and
	// overlap [from, to), with their CIs, ordered by starts_at.
	WindowsBetween(ctx context.Context, orgID string, from, to time.Time) ([]Window, error)
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
		return nil, errWindowNotFound
	}
	cp := *w
	return &cp, nil
}

// ActiveWindows implements Repository.
func (r *MemoryRepository) ActiveWindows(_ context.Context, orgID string, at time.Time) ([]Window, error) {
	return r.between(orgID, at, at.Add(time.Nanosecond)), nil
}

// WindowsBetween implements Repository.
func (r *MemoryRepository) WindowsBetween(_ context.Context, orgID string, from, to time.Time) ([]Window, error) {
	return r.between(orgID, from, to), nil
}

func (r *MemoryRepository) between(orgID string, from, to time.Time) []Window {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Window{}
	for _, w := range r.wins {
		if w.OrganizationID != orgID || w.Status == "cancelled" || !w.StartsAt.Before(to) || !w.EndsAt.After(from) {
			continue
		}
		cp := *w
		cp.CIIDs = slices.Clone(w.CIIDs)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out
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
		return nil, errWindowNotFound
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
		return errWindowNotFound
	}
	delete(r.wins, id)
	delete(r.notif, id)
	return nil
}

func (r *MemoryRepository) NotifyClients(_ context.Context, orgID, windowID string, plan *NotifyPlan) ([]Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.wins[windowID]
	if !ok || w.OrganizationID != orgID {
		return nil, errWindowNotFound
	}
	var clients []string
	if plan != nil {
		for i := range plan.Clients {
			clients = append(clients, plan.Clients[i].ClientID)
		}
	} else {
		for _, ciID := range w.CIIDs {
			if clientID := r.ciClient[ciID]; clientID != "" && !slices.Contains(clients, clientID) {
				clients = append(clients, clientID)
			}
		}
	}
	var out []Notification
	for _, clientID := range clients {
		// Without the notification chain nothing delivers: a stored
		// notification stays pending and is returned (F-235).
		if stored := r.findNotification(windowID, clientID, ChannelNotify); stored != nil {
			if stored.Status == NotificationFailed {
				// A new announcement gets a new id, like PostgreSQL.
				stored.ID, stored.Status, stored.SentAt = r.nextID("mn"), NotificationPending, nil
			}
			out = append(out, *stored)
			continue
		}
		n := &Notification{
			ID:             r.nextID("mn"),
			OrganizationID: orgID,
			WindowID:       windowID,
			ClientID:       clientID,
			Channel:        ChannelNotify,
			Status:         NotificationPending,
			CreatedAt:      time.Now().UTC(),
		}
		r.notif[windowID] = append(r.notif[windowID], n)
		out = append(out, *n)
	}
	return out, nil
}

func (r *MemoryRepository) findNotification(windowID, clientID, channel string) *Notification {
	for _, n := range r.notif[windowID] {
		if n.ClientID == clientID && n.Channel == channel {
			return n
		}
	}
	return nil
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
