package webhook

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UpdateRequest captures mutable webhook subscription fields for future sqlc-backed updates.
type UpdateRequest struct {
	Name     *string  `json:"name,omitempty"`
	URL      *string  `json:"url,omitempty"`
	Events   []string `json:"events,omitempty"`
	IsActive *bool    `json:"is_active,omitempty"`
}

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed webhook repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}

	if err := fn(ctx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// List returns paginated subscriptions.
func (r *PGRepository) List(orgID string, page api.PaginationParams) ([]Subscription, int, error) {
	ctx := context.Background()
	var subs []Subscription
	var total int

	err := r.withTenant(ctx, orgID, func(ctx context.Context) error {
		// TODO: Implement with sqlc-generated ListWebhookSubscriptions query
		subs = []Subscription{}
		total = 0
		return nil
	})

	return subs, total, err
}

func (r *PGRepository) GetByID(orgID, id string) (*Subscription, error) {
	ctx := context.Background()
	var sub *Subscription

	err := r.withTenant(ctx, orgID, func(ctx context.Context) error {
		// TODO: Implement with sqlc-generated GetWebhookSubscription query
		return fmt.Errorf("not found")
	})
	if err != nil {
		return nil, err
	}

	return sub, nil
}

func (r *PGRepository) Create(sub *Subscription) error {
	ctx := context.Background()
	return r.withTenant(ctx, sub.OrganizationID, func(ctx context.Context) error {
		// TODO: Implement with sqlc-generated CreateWebhookSubscription query
		return nil
	})
}

// Update modifies an existing subscription.
func (r *PGRepository) Update(orgID, id string, req UpdateRequest) (*Subscription, error) {
	ctx := context.Background()
	var sub *Subscription

	err := r.withTenant(ctx, orgID, func(ctx context.Context) error {
		// TODO: Implement with sqlc-generated UpdateWebhookSubscription query
		return fmt.Errorf("not found")
	})

	return sub, err
}

func (r *PGRepository) Delete(orgID, id string) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context) error {
		// TODO: Implement with sqlc-generated DeleteWebhookSubscription query
		return nil
	})
}

// GetActiveForEvent returns active subscriptions that match the event type.
func (r *PGRepository) GetActiveForEvent(orgID, eventType string) ([]Subscription, error) {
	ctx := context.Background()
	var subs []Subscription

	err := r.withTenant(ctx, orgID, func(ctx context.Context) error {
		// TODO: Filter active subscriptions matching event_type
		subs = []Subscription{}
		return nil
	})

	return subs, err
}

// ListByEvent satisfies the current webhook.Repository interface.
func (r *PGRepository) ListByEvent(orgID, event string) ([]Subscription, error) {
	return r.GetActiveForEvent(orgID, event)
}
