package webhook

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Delivery statuses persisted in webhook_delivery.status.
const (
	StatusPending  = "pending"
	StatusRetrying = "retrying"
	StatusSuccess  = "success"
	StatusFailed   = "failed"
	// StatusDead marks a delivery whose retry budget is exhausted and whose
	// payload was moved to the dead-letter queue.
	StatusDead = "dead"
)

// ErrNoDeliveryStore is returned when a durable operation is requested but no
// delivery store is configured.
var ErrNoDeliveryStore = errors.New("webhook: no delivery store configured")

// DeliveryRecord is the durable representation of a webhook delivery. Every
// queued event is persisted before the first HTTP attempt, so deliveries
// survive a restart and can be retried with exponential backoff.
type DeliveryRecord struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	SubscriptionID string     `json:"subscription_id"`
	Event          string     `json:"event"`
	Payload        []byte     `json:"-"`
	Status         string     `json:"status"`
	Attempt        int        `json:"attempt"`
	MaxAttempts    int        `json:"max_attempts"`
	ResponseStatus int        `json:"response_status,omitempty"`
	DurationMS     int        `json:"duration_ms,omitempty"`
	Error          string     `json:"error,omitempty"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// DeadLetter is a delivery that exhausted its retry budget. The dispatcher
// moves the payload here so operators can inspect and replay it.
type DeadLetter struct {
	ID             string    `json:"id"`
	DeliveryID     string    `json:"delivery_id"`
	OrganizationID string    `json:"organization_id"`
	SubscriptionID string    `json:"subscription_id"`
	Event          string    `json:"event"`
	Attempts       int       `json:"attempts"`
	LastStatusCode int       `json:"last_status_code,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	FirstAttemptAt time.Time `json:"first_attempt_at"`
	DeadAt         time.Time `json:"dead_at"`
}

// DeliveryStore persists webhook deliveries and their retry schedule.
type DeliveryStore interface {
	// Enqueue stores a new delivery in the pending state.
	Enqueue(ctx context.Context, rec DeliveryRecord) (DeliveryRecord, error)
	// Update persists the outcome of one delivery attempt.
	Update(ctx context.Context, rec DeliveryRecord) error
	// ClaimDue atomically leases deliveries whose retry time has come. The
	// implementation must be safe to run from multiple server replicas.
	ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]DeliveryRecord, error)
	// ListBySubscription returns the delivery history of one subscription.
	ListBySubscription(ctx context.Context, orgID, subscriptionID string, page api.PaginationParams) ([]DeliveryRecord, int, error)
	// MoveToDeadLetter moves a failed delivery to the dead-letter queue and
	// marks the delivery record as dead. It is idempotent per delivery.
	MoveToDeadLetter(ctx context.Context, rec DeliveryRecord) error
	// ListDeadLetters returns the dead-letter queue of one tenant.
	ListDeadLetters(ctx context.Context, orgID string, page api.PaginationParams) ([]DeadLetter, int, error)
}

// MemoryDeliveryStore is an in-memory DeliveryStore for tests and the explicit
// --no-db development mode. It is not durable across restarts.
type MemoryDeliveryStore struct {
	mu          sync.RWMutex
	items       map[string]DeliveryRecord
	deadLetters map[string]DeadLetter
	seq         int
}

// NewMemoryDeliveryStore creates an empty in-memory delivery store.
func NewMemoryDeliveryStore() *MemoryDeliveryStore {
	return &MemoryDeliveryStore{
		items:       make(map[string]DeliveryRecord),
		deadLetters: make(map[string]DeadLetter),
	}
}

// Enqueue stores a new pending delivery.
func (s *MemoryDeliveryStore) Enqueue(_ context.Context, rec DeliveryRecord) (DeliveryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	rec.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", s.seq, s.seq)
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	if rec.Status == "" {
		rec.Status = StatusPending
	}
	s.items[rec.ID] = rec
	return rec, nil
}

// Update persists the outcome of one attempt.
func (s *MemoryDeliveryStore) Update(_ context.Context, rec DeliveryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.items[rec.ID]
	if !ok || existing.OrganizationID != rec.OrganizationID {
		return fmt.Errorf("delivery not found")
	}
	rec.Payload = existing.Payload
	rec.CreatedAt = existing.CreatedAt
	s.items[rec.ID] = rec
	return nil
}

// ClaimDue leases deliveries that are due for another attempt.
func (s *MemoryDeliveryStore) ClaimDue(_ context.Context, now time.Time, limit int, lease time.Duration) ([]DeliveryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	due := make([]DeliveryRecord, 0, limit)
	for id, rec := range s.items {
		if rec.Status != StatusPending && rec.Status != StatusRetrying {
			continue
		}
		if rec.NextRetryAt == nil || rec.NextRetryAt.After(now) {
			continue
		}
		leased := now.Add(lease)
		rec.Status = StatusRetrying
		rec.NextRetryAt = &leased
		s.items[id] = rec
		due = append(due, rec)
		if len(due) >= limit {
			break
		}
	}

	sort.Slice(due, func(i, j int) bool { return due[i].CreatedAt.Before(due[j].CreatedAt) })
	return due, nil
}

// ListBySubscription returns the stored deliveries of one subscription, newest first.
func (s *MemoryDeliveryStore) ListBySubscription(_ context.Context, orgID, subscriptionID string, page api.PaginationParams) ([]DeliveryRecord, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var all []DeliveryRecord
	for _, rec := range s.items {
		if rec.OrganizationID != orgID || rec.SubscriptionID != subscriptionID {
			continue
		}
		all = append(all, rec)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })

	total := len(all)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total || page.Limit <= 0 {
		end = total
	}
	return all[start:end], total, nil
}

// MoveToDeadLetter records the delivery in the dead-letter queue and marks it
// dead. Calling it twice for the same delivery is a no-op.
func (s *MemoryDeliveryStore) MoveToDeadLetter(_ context.Context, rec DeliveryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.items[rec.ID]
	if !ok || existing.OrganizationID != rec.OrganizationID {
		return fmt.Errorf("delivery not found")
	}
	if _, ok := s.deadLetters[rec.ID]; ok {
		return nil
	}

	s.deadLetters[rec.ID] = DeadLetter{
		ID:             fmt.Sprintf("dlq-%s", rec.ID),
		DeliveryID:     rec.ID,
		OrganizationID: rec.OrganizationID,
		SubscriptionID: rec.SubscriptionID,
		Event:          rec.Event,
		Attempts:       rec.Attempt,
		LastStatusCode: rec.ResponseStatus,
		LastError:      rec.Error,
		FirstAttemptAt: existing.CreatedAt,
		DeadAt:         time.Now().UTC(),
	}

	existing.Status = StatusDead
	existing.Attempt = rec.Attempt
	existing.ResponseStatus = rec.ResponseStatus
	existing.DurationMS = rec.DurationMS
	existing.Error = rec.Error
	existing.NextRetryAt = nil
	s.items[rec.ID] = existing
	return nil
}

// ListDeadLetters returns the dead-letter queue of one tenant, newest first.
func (s *MemoryDeliveryStore) ListDeadLetters(_ context.Context, orgID string, page api.PaginationParams) ([]DeadLetter, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var all []DeadLetter
	for _, dl := range s.deadLetters {
		if dl.OrganizationID != orgID {
			continue
		}
		all = append(all, dl)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].DeadAt.After(all[j].DeadAt) })

	total := len(all)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total || page.Limit <= 0 {
		end = total
	}
	return all[start:end], total, nil
}
