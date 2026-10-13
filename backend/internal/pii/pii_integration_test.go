package pii_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// TestVault covers WP-240 (SEC-05, CH16) against PostgreSQL: a subject is
// stored encrypted under the organization's data key (no plaintext in
// pii_subject, dek_ref names the key version) and resolves singly and in
// bulk; another organization cannot resolve it, a payload copied to
// another subject does not resolve; a DEK rotation re-encrypts the vault;
// a deleted subject cannot be resolved anymore.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestVault(t *testing.T) {
	f := scopetest.Seed(t, "a0")
	bg := context.Background()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	vault := pii.NewVault(f.App, enc)
	ctxA, ctxB := f.OrgCtx(f.OrgA), f.OrgCtx(f.OrgB)

	erika := pii.Data{"name": "Erika Mustermann", "email": "erika@example.org", "phone": "+49 861 1234"}
	id, err := vault.Create(ctxA, f.OrgA, pii.KindContact, erika)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	maxID, err := vault.Create(ctxA, f.OrgA, pii.KindUser, pii.Data{"name": "Max Muster"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := vault.Resolve(ctxA, f.OrgA, id)
	if err != nil || got.Kind != pii.KindContact || got.Data["name"] != erika["name"] || got.Data["phone"] != erika["phone"] {
		t.Fatalf("resolve = %+v, %v", got, err)
	}
	many, err := vault.ResolveMany(ctxA, f.OrgA, []string{id, maxID, "00000000-0000-4000-8000-000000000000", "not-a-uuid"})
	if err != nil || len(many) != 2 || many[maxID].Data["name"] != "Max Muster" {
		t.Fatalf("resolve many = %v, %v", many, err)
	}

	// Stored encrypted, with the key version.
	var payload []byte
	var dekRef int
	if err = f.Admin.QueryRow(bg, `SELECT encrypted_payload, dek_ref FROM pii_subject WHERE surrogate_id = $1`, id).
		Scan(&payload, &dekRef); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("Erika")) || bytes.Contains(payload, []byte("example.org")) || dekRef != 1 {
		t.Fatalf("stored payload in plaintext or dek_ref %d", dekRef)
	}

	// Organization isolation: B neither resolves nor sees A's subjects.
	if _, err = vault.Resolve(ctxB, f.OrgB, id); !errors.Is(err, pii.ErrNotFound) {
		t.Fatalf("resolve in another organization: %v, want not found", err)
	}
	if _, err = vault.Resolve(ctxB, f.OrgA, id); err == nil {
		t.Fatal("resolve with the scope of another organization succeeded")
	}
	if err = vault.Update(ctxB, f.OrgB, id, pii.Data{"name": "x"}); !errors.Is(err, pii.ErrNotFound) {
		t.Fatalf("update from another organization: %v, want not found", err)
	}

	// A payload copied to another subject does not resolve.
	if _, err = f.Admin.Exec(bg, `UPDATE pii_subject SET encrypted_payload = $1 WHERE surrogate_id = $2`, payload, maxID); err != nil {
		t.Fatal(err)
	}
	if _, err = vault.Resolve(ctxA, f.OrgA, maxID); err == nil {
		t.Fatal("a copied payload resolved")
	}
	if err = vault.Update(ctxA, f.OrgA, maxID, pii.Data{"name": "Max Muster"}); err != nil {
		t.Fatal(err)
	}

	// Update.
	erika["email"] = "e.mustermann@example.org"
	if err = vault.Update(ctxA, f.OrgA, id, erika); err != nil {
		t.Fatal(err)
	}
	if got, err = vault.Resolve(ctxA, f.OrgA, id); err != nil || got.Data["email"] != "e.mustermann@example.org" {
		t.Fatalf("after update = %+v, %v", got, err)
	}

	// A DEK rotation re-encrypts the vault (SEC-06).
	rotated, err := credential.NewPGRepository(f.App).RotateOrgDEK(bg, f.OrgA, enc)
	if err != nil || rotated.Subjects != 2 || rotated.KeyVersion != 2 {
		t.Fatalf("rotate = %+v, %v", rotated, err)
	}
	if got, err = vault.Resolve(ctxA, f.OrgA, id); err != nil || got.Data["name"] != erika["name"] {
		t.Fatalf("after rotation = %+v, %v", got, err)
	}

	// A deleted subject cannot be resolved anymore.
	if err = vault.Delete(ctxA, f.OrgA, id); err != nil {
		t.Fatal(err)
	}
	if _, err = vault.Resolve(ctxA, f.OrgA, id); !errors.Is(err, pii.ErrNotFound) {
		t.Fatalf("resolve after delete: %v, want not found", err)
	}
	if many, err = vault.ResolveMany(ctxA, f.OrgA, []string{id, maxID}); err != nil || len(many) != 1 || many[maxID] == nil {
		t.Fatalf("resolve many after delete = %v, %v", many, err)
	}
	var left int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM pii_subject WHERE surrogate_id = $1`, id).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d rows left after delete (%v)", left, err)
	}
}
