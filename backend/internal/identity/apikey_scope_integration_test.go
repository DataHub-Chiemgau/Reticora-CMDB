package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestAPIKeyStoreScope runs the writes of the API key store in a tenant
// transaction (TEN-06, AUT-04 part of WP-012): a key is saved with the
// creating principal's scope and only for its organization; marking a key as
// used acts for the key's organization and does not touch other keys.
// Identifying a key by prefix without tenant context is part of WP-067.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAPIKeyStoreScope(t *testing.T) {
	f := scopetest.Seed(t, "1e")
	store := identity.NewPGAPIKeyStore(f.App)
	ctx := f.ClientCtx(f.Client1)

	key := identity.StoredAPIKey{OrganizationID: f.OrgA, Name: "scopetest", KeyHash: "hash-a", KeyPrefix: "prefix1e", CreatedBy: f.User}
	if err := store.Save(ctx, &key); err != nil {
		t.Fatalf("save org A key: %v", err)
	}
	other := identity.StoredAPIKey{OrganizationID: f.OrgB, Name: "scopetest", KeyHash: "hash-b", KeyPrefix: "prefix1e", CreatedBy: f.User}
	if err := store.Save(ctx, &other); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("save organization B key: got %v, want ErrTenantMismatch", err)
	}
	if err := store.Save(context.Background(), &key); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("save without scope: got %v, want ErrNoTenantScope", err)
	}

	var idA string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM api_key WHERE organization_id = $1`, f.OrgA).Scan(&idA); err != nil {
		t.Fatalf("read org A key: %v", err)
	}
	// Marking a key of organization A as used on behalf of organization B
	// reaches no row.
	if err := store.MarkUsed(context.Background(), f.OrgB, idA); err != nil {
		t.Fatalf("mark used for organization B: %v", err)
	}
	var used bool
	if err := f.Admin.QueryRow(context.Background(), `SELECT last_used_at IS NOT NULL FROM api_key WHERE id = $1`, idA).Scan(&used); err != nil {
		t.Fatalf("read last_used_at: %v", err)
	}
	if used {
		t.Fatal("organization B context marked a key of organization A")
	}
	if err := store.MarkUsed(context.Background(), f.OrgA, idA); err != nil {
		t.Fatalf("mark used: %v", err)
	}
	if err := f.Admin.QueryRow(context.Background(), `SELECT last_used_at IS NOT NULL FROM api_key WHERE id = $1`, idA).Scan(&used); err != nil {
		t.Fatalf("read last_used_at: %v", err)
	}
	if !used {
		t.Fatal("last_used_at was not set")
	}
}
