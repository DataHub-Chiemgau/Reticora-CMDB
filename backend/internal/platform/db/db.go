// Package db provides the central database access primitive for Reticora.
// All tenant-scoped database operations MUST go through WithTenant.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool wraps a pgxpool.Pool and provides tenant-scoped transaction helpers.
type Pool struct {
	*pgxpool.Pool
}

// NewPool creates a new Pool from a connection string.
func NewPool(ctx context.Context, connString string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &Pool{Pool: pool}, nil
}

// WithTenant is the ONLY entry point for tenant-scoped database operations.
// It opens a transaction, sets app.org_id (and optionally app.client_scope
// and app.user_id), executes fn, and commits on success.
func WithTenant(ctx context.Context, pool *Pool, orgID, clientScope, userID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Set mandatory org_id for RLS
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set app.org_id: %w", err)
	}

	// Set optional client scope
	if clientScope != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.client_scope', $1, true)", clientScope); err != nil {
			return fmt.Errorf("set app.client_scope: %w", err)
		}
	}

	// Set optional user ID for audit
	if userID != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', $1, true)", userID); err != nil {
			return fmt.Errorf("set app.user_id: %w", err)
		}
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
