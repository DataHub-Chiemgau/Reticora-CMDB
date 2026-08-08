package rls

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// newStubDB returns a *sql.DB backed by the pgx stdlib driver with no live
// connection. It is used only to exercise argument validation and error
// paths that occur before any query is executed.
func newStubDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://invalid.invalid:1/none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestWithTenantRequiresOrgID(t *testing.T) {
	db := newStubDB(t)
	err := WithTenant(context.Background(), db, "", "", "", func(ctx context.Context, tx *sql.Tx) error {
		t.Fatal("fn must not run without org id")
		return nil
	})
	if err == nil {
		t.Fatal("expected error for empty org id")
	}
}

func TestSetTenantContextRequiresOrgID(t *testing.T) {
	db := newStubDB(t)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{})
	if err := SetTenantContext(ctx, db); err == nil {
		t.Fatal("expected error for missing organization in context")
	}
}
