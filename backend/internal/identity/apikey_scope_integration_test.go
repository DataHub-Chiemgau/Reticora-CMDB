package identity_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// TestAPIKeyStoreManagementScope covers the management paths of the API key
// store (TEN-06, AUT-04 AC4 of WP-012; evidence WP-536): List, Revoke and
// Rotate run in the caller's tenant transaction. A principal of organization
// B naming organization A gets ErrTenantMismatch; naming its own organization
// with a key id of organization A it gets ErrAPIKeyNotFound, its list holds
// no key of A, and the key row of A stays unchanged (no revocation, no
// shortened expiry, no successor). Without a tenant scope every path returns
// ErrNoTenantScope. The organization's own principal lists, rotates and
// revokes its key (control).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAPIKeyStoreManagementScope(t *testing.T) {
	f := scopetest.Seed(t, "1e")
	store := identity.NewPGAPIKeyStore(f.App)
	bg := context.Background()
	ctxA, ctxB := f.OrgCtx(f.OrgA), f.OrgCtx(f.OrgB)

	save := func(ctx context.Context, org, prefix string) string {
		t.Helper()
		owner := f.AppUser(t, org, "owner-"+prefix)
		key := identity.StoredAPIKey{OrganizationID: org, Name: "scopetest-" + prefix, KeyHash: "hash-" + prefix, KeyPrefix: prefix,
			Environment: "live", CreatedBy: owner}
		if err := store.Save(ctx, &key); err != nil {
			t.Fatalf("save key %s: %v", prefix, err)
		}
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM api_key WHERE key_prefix = $1`, prefix).Scan(&id); err != nil {
			t.Fatalf("read key %s: %v", prefix, err)
		}
		return id
	}
	idA := save(ctxA, f.OrgA, "mgmt1ea")
	idB := save(ctxB, f.OrgB, "mgmt1eb")

	// row is the state of a key row a change through the store would
	// touch, with the number of keys rotated from it.
	row := func(id string) string {
		t.Helper()
		var state string
		if err := f.Admin.QueryRow(bg, `SELECT concat_ws('|', name, revoked_at, expires_at, last_used_at,
				(SELECT COUNT(*) FROM api_key s WHERE s.rotated_from = k.id))
			FROM api_key k WHERE id = $1`, id).Scan(&state); err != nil {
			t.Fatalf("read key row: %v", err)
		}
		return state
	}
	before := row(idA)
	unchanged := func(step string) {
		t.Helper()
		if after := row(idA); after != before {
			t.Errorf("%s changed the organization-A key: %q -> %q", step, before, after)
		}
	}
	overlap := time.Now().Add(time.Hour)

	// Scope of organization B naming organization A.
	if _, err := store.List(ctxB, f.OrgA); !errors.Is(err, database.ErrTenantMismatch) {
		t.Errorf("list A with B scope: got %v, want ErrTenantMismatch", err)
	}
	if err := store.Revoke(ctxB, f.OrgA, idA); !errors.Is(err, database.ErrTenantMismatch) {
		t.Errorf("revoke A with B scope: got %v, want ErrTenantMismatch", err)
	}
	unchanged("revoke with B scope naming A")
	if _, err := store.Rotate(ctxB, f.OrgA, idA, overlap); !errors.Is(err, database.ErrTenantMismatch) {
		t.Errorf("rotate A with B scope: got %v, want ErrTenantMismatch", err)
	}
	unchanged("rotate with B scope naming A")

	// Scope and organization B with a key id of organization A.
	keys, err := store.List(ctxB, f.OrgB)
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != idB {
		t.Errorf("list B: %d keys %+v, want only the organization-B key", len(keys), keys)
	}
	if err = store.Revoke(ctxB, f.OrgB, idA); !errors.Is(err, identity.ErrAPIKeyNotFound) {
		t.Errorf("revoke A key as B: got %v, want ErrAPIKeyNotFound", err)
	}
	unchanged("revoke of the A key as B")
	if _, err = store.Rotate(ctxB, f.OrgB, idA, overlap); !errors.Is(err, identity.ErrAPIKeyNotFound) {
		t.Errorf("rotate A key as B: got %v, want ErrAPIKeyNotFound", err)
	}
	unchanged("rotate of the A key as B")

	// Without a tenant scope.
	if _, err = store.List(bg, f.OrgA); !errors.Is(err, database.ErrNoTenantScope) {
		t.Errorf("list without scope: got %v, want ErrNoTenantScope", err)
	}
	if err = store.Revoke(bg, f.OrgA, idA); !errors.Is(err, database.ErrNoTenantScope) {
		t.Errorf("revoke without scope: got %v, want ErrNoTenantScope", err)
	}
	if _, err = store.Rotate(bg, f.OrgA, idA, overlap); !errors.Is(err, database.ErrNoTenantScope) {
		t.Errorf("rotate without scope: got %v, want ErrNoTenantScope", err)
	}
	unchanged("calls without scope")

	// Control: organization A lists, rotates and revokes its own key.
	keys, err = store.List(ctxA, f.OrgA)
	if err != nil || len(keys) != 1 || keys[0].ID != idA {
		t.Fatalf("list A: %v %+v, want only the organization-A key", err, keys)
	}
	rotated, err := store.Rotate(ctxA, f.OrgA, idA, overlap)
	if err != nil {
		t.Fatalf("rotate A: %v", err)
	}
	if err = store.Revoke(ctxA, f.OrgA, rotated.Created.ID); err != nil {
		t.Fatalf("revoke the successor of the A key: %v", err)
	}
	if after := row(idA); after == before {
		t.Error("the rotation by organization A left its key row unchanged")
	}
}
