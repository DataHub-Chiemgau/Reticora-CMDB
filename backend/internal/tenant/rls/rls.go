// Package rls provides helpers for setting PostgreSQL Row-Level Security
// session variables on database connections.
//
// Per the spec, every tenant-scoped database operation MUST go through
// WithTenant which sets app.org_id, optionally app.client_scope and app.user_id
// as transaction-local GUC variables for RLS policies.
package rls

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// SetTenantContext sets the RLS session variables on a database connection
// within an active transaction. The variables are transaction-local (true flag).
func SetTenantContext(ctx context.Context, db *sql.DB) error {
	t := tenant.FromContext(ctx)
	if t.OrganizationID == "" {
		return fmt.Errorf("rls: organization_id is required")
	}

	_, err := db.ExecContext(ctx,
		"SELECT set_config('app.org_id', $1, true)",
		t.OrganizationID,
	)
	if err != nil {
		return fmt.Errorf("rls: failed to set app.org_id: %w", err)
	}

	if t.ClientID != "" {
		_, err = db.ExecContext(ctx,
			"SELECT set_config('app.client_scope', $1, true)",
			t.ClientID,
		)
		if err != nil {
			return fmt.Errorf("rls: failed to set app.client_scope: %w", err)
		}
	}

	if t.UserID != "" {
		_, err = db.ExecContext(ctx,
			"SELECT set_config('app.user_id', $1, true)",
			t.UserID,
		)
		if err != nil {
			return fmt.Errorf("rls: failed to set app.user_id: %w", err)
		}
	}

	return nil
}

// WithTenant is the sole database entry point for tenant-scoped operations.
// It begins a transaction, sets the RLS context variables, executes fn, and
// commits on success. All repositories MUST use this; no path without
// tenant context is allowed.
func WithTenant(ctx context.Context, db *sql.DB, orgID, clientScope, userID string, fn func(ctx context.Context, tx *sql.Tx) error) error {
	if orgID == "" {
		return fmt.Errorf("rls: organization_id is required")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rls: begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("rls: set app.org_id: %w", err)
	}

	if clientScope != "" {
		if _, err := tx.ExecContext(ctx, "SELECT set_config('app.client_scope', $1, true)", clientScope); err != nil {
			return fmt.Errorf("rls: set app.client_scope: %w", err)
		}
	}

	if userID != "" {
		if _, err := tx.ExecContext(ctx, "SELECT set_config('app.user_id', $1, true)", userID); err != nil {
			return fmt.Errorf("rls: set app.user_id: %w", err)
		}
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit()
}
