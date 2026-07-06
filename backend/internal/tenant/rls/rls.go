// Package rls provides helpers for setting PostgreSQL Row-Level Security
// session variables on database connections.
package rls

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// SetTenantContext sets the RLS session variables on a database connection.
// This must be called at the beginning of each request's transaction.
func SetTenantContext(ctx context.Context, db *sql.DB) error {
	t := tenant.FromContext(ctx)
	if t.OrganizationID == "" {
		return fmt.Errorf("rls: organization_id is required")
	}

	_, err := db.ExecContext(ctx,
		"SELECT set_config('app.organization_id', $1, true)",
		t.OrganizationID,
	)
	if err != nil {
		return fmt.Errorf("rls: failed to set organization_id: %w", err)
	}

	if t.ClientID != "" {
		_, err = db.ExecContext(ctx,
			"SELECT set_config('app.client_id', $1, true)",
			t.ClientID,
		)
		if err != nil {
			return fmt.Errorf("rls: failed to set client_id: %w", err)
		}
	}

	return nil
}
