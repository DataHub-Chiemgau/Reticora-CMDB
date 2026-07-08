package ci

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed CI repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// withTenant executes fn within a transaction that has app.org_id set for RLS.
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

// List returns paginated CIs filtered by the given parameters.
func (r *PGRepository) List(orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
	ctx := context.Background()
	var items []Item
	var total int

	err := r.withTenant(ctx, orgID, func(ctx context.Context) error {
		// TODO: Implement with actual sqlc-generated queries
		// For now, return empty results
		items = []Item{}
		total = 0
		return nil
	})

	return items, total, err
}

// GetByID retrieves a single CI by ID within the tenant scope.
func (r *PGRepository) GetByID(orgID, id string) (*Item, error) {
	ctx := context.Background()
	var item *Item

	err := r.withTenant(ctx, orgID, func(ctx context.Context) error {
		// TODO: Implement with sqlc-generated GetCI query
		return fmt.Errorf("not found")
	})
	if err != nil {
		return nil, err
	}

	return item, nil
}

// Create inserts a new CI.
func (r *PGRepository) Create(item *Item) error {
	ctx := context.Background()
	return r.withTenant(ctx, item.OrganizationID, func(ctx context.Context) error {
		// TODO: Implement with sqlc-generated CreateCI query
		return nil
	})
}

// Update modifies an existing CI.
func (r *PGRepository) Update(orgID, id string, req UpdateRequest) (*Item, error) {
	ctx := context.Background()
	var item *Item

	err := r.withTenant(ctx, orgID, func(ctx context.Context) error {
		// TODO: Implement with sqlc-generated UpdateCI query
		return fmt.Errorf("not found")
	})

	return item, err
}

// Delete soft-deletes a CI.
func (r *PGRepository) Delete(orgID, id string) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context) error {
		// TODO: Implement with sqlc-generated SoftDeleteCI query
		return nil
	})
}
