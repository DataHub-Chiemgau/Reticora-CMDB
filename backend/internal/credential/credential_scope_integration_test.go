package credential_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestCredentialRepositoryClientScope runs the credential repository with the
// principal's tenant scope (TEN-06, WP-011): a principal restricted to client
// 1 neither lists, reads, updates nor deletes a credential of client 2.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCredentialRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "15")
	repo := credential.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)

	stored := func(clientID, name string) *credential.StoredCredential {
		return &credential.StoredCredential{
			Credential: credential.Credential{OrganizationID: f.OrgA, ClientID: clientID, Name: name, Kind: credential.KindSSHPassword, KeyVersion: 1},
			Ciphertext: []byte("ciphertext"),
		}
	}
	own := stored(f.Client1, "own")
	foreign := stored(f.Client2, "foreign")
	for _, c := range []*credential.StoredCredential{own, foreign} {
		if err := repo.Create(orgCtx, c); err != nil {
			t.Fatalf("org-wide create %s: %v", c.Name, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	list, err := repo.List(ctx, f.OrgA)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != own.ID {
		t.Fatalf("client-1 list: got %+v, want only the client-1 credential", list)
	}
	if _, err = repo.Get(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal read a credential of client 2")
	}
	foreign.Name = "renamed"
	if err = repo.Update(ctx, foreign); err == nil {
		t.Fatal("client-1 principal updated a credential of client 2")
	}
	if err = repo.Delete(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal deleted a credential of client 2")
	}
	if err = repo.Create(ctx, stored(f.Client2, "intruder")); err == nil {
		t.Fatal("client-1 principal created a credential for client 2")
	}

	var name string
	if err = f.Admin.QueryRow(context.Background(), `SELECT name FROM credential WHERE id = $1`, foreign.ID).Scan(&name); err != nil {
		t.Fatalf("read foreign credential: %v", err)
	}
	if name != "foreign" {
		t.Fatalf("client-2 credential changed: name=%q", name)
	}

	if _, err = repo.List(context.Background(), f.OrgA); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
