package assignment_test

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestAssignmentSignatureKeepsSurrogate covers F-385 (WP-656, SEC-05,
// AUD-04, WP-241 AK1) against PostgreSQL: the signer names of the handover
// and return signatures live in the PII vault and the rows of assignment
// keep only their surrogates. Since WP-746 no path below
// /api/v1/assignments writes, so the assignments with signatures are
// inserted per SQL with plaintext signer names; the pii.backfill run seals
// them. Since WP-747 GET /api/v1/assignments reads the ledger and returns
// no signatures, so the test checks the table and the vault directly: the
// vault resolves the surrogates to the names, another organization does
// not resolve them and deleting the vault entry shreds the name while the
// signature stays (until WP-843).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssignmentSignatureKeepsSurrogate(t *testing.T) {
	f := scopetest.Seed(t, "a6")
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
	pii.SetDefault(vault)
	t.Cleanup(func() { pii.SetDefault(nil) })

	asset := f.Asset(t, f.OrgA, f.Client1, "wp656-asset")
	holder := f.AppUser(t, f.OrgA, "wp656holder")
	successor := f.AppUser(t, f.OrgA, "wp656next")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: holder})
	stored := func(id string) (checkout, ret string) {
		t.Helper()
		if qerr := f.Admin.QueryRow(bg, `SELECT COALESCE(checkout_signature::text, ''), COALESCE(return_signature::text, '')
			FROM assignment WHERE id = $1`, id).Scan(&checkout, &ret); qerr != nil {
			t.Fatal(qerr)
		}
		return checkout, ret
	}
	signer := func(column, sig string) string {
		t.Helper()
		var parsed map[string]any
		if uerr := json.Unmarshal([]byte(sig), &parsed); uerr != nil {
			t.Fatalf("%s = %s: %v", column, sig, uerr)
		}
		id, ok := pii.RefID(parsed["signer_name"])
		if !ok || !uuidPattern.MatchString(id) {
			t.Fatalf("assignment.%s holds no surrogate for the signer: %s", column, sig)
		}
		return id
	}
	exists := func(query string, args ...any) bool {
		t.Helper()
		var found bool
		if qerr := f.Admin.QueryRow(bg, `SELECT EXISTS (`+query+`)`, args...).Scan(&found); qerr != nil {
			t.Fatal(qerr)
		}
		return found
	}
	insert := func(assignee, status, checkout, ret string) string {
		t.Helper()
		id := f.ID()
		if _, ierr := f.Admin.Exec(bg, `INSERT INTO assignment (id, organization_id, asset_id, assigned_to, assigned_by, assignment_type, status,
				checkout_signature, return_signature)
			VALUES ($1, $2, $3, $4, $4, 'assignment', $5, $6::jsonb, NULLIF($7, '')::jsonb)`,
			id, f.OrgA, asset, assignee, status, checkout, ret); ierr != nil {
			t.Fatal(ierr)
		}
		return id
	}

	// Assignments with plaintext signer names, as the paths before WP-656
	// wrote them.
	handover := insert(holder, "returned", `{"signer_name": "Max Verifier", "method": "typed", "signed_at": "2026-10-28T10:00:00Z"}`, "")
	returned := insert(successor, "returned", `{"signer_name": "Lena Legacy", "method": "drawn", "image_base64": "aGVsbG8="}`,
		`{"signer_name": "Rita Rueckgabe", "method": "typed", "signed_at": "2026-10-28T11:00:00Z"}`)

	// The backfill seals the names: the table keeps only surrogates, the
	// vault holds the names encrypted.
	res, err := jobs.NewPIIBackfill(f.App).RunOrg(bg, f.OrgA)
	if err != nil || res.Total() < 1 {
		t.Fatalf("backfill = %+v, %v", res, err)
	}
	if exists(`SELECT 1 FROM assignment WHERE organization_id = $1 AND (checkout_signature::text || COALESCE(return_signature::text, ''))
			~ '(Verifier|Rueckgabe|Legacy)'`, f.OrgA) {
		t.Fatal("plaintext signer names left after the backfill")
	}
	checkout, _ := stored(handover)
	_, ret := stored(returned)
	checkoutSubject, returnSubject := signer("checkout_signature", checkout), signer("return_signature", ret)
	if !exists(`SELECT 1 FROM pii_subject WHERE organization_id = $1 AND surrogate_id = $2 AND dek_ref > 0`, f.OrgA, checkoutSubject) ||
		!exists(`SELECT 1 FROM pii_subject WHERE organization_id = $1 AND surrogate_id = $2 AND dek_ref > 0`, f.OrgA, returnSubject) {
		t.Fatal("the signer surrogates have no vault entry")
	}
	if exists(`SELECT 1 FROM pii_subject WHERE encode(encrypted_payload, 'escape') ~ '(Verifier|Rueckgabe|Legacy)'`) {
		t.Fatal("the vault holds the signer names in plaintext")
	}
	if !strings.Contains(checkout, `"method": "typed"`) || !strings.Contains(ret, `"signed_at": "2026-10-28T11:00:00Z"`) {
		t.Fatalf("the other signature fields were lost: %s / %s", checkout, ret)
	}
	if again, aerr := jobs.NewPIIBackfill(f.App).RunOrg(bg, f.OrgA); aerr != nil || again.Total() != 0 {
		t.Fatalf("second backfill = %+v, %v", again, aerr)
	}

	// The vault resolves the surrogates to the names in organization A.
	// GET /api/v1/assignments reads the ledger since WP-747 and returns no
	// signatures; the table keeps them until WP-843.
	name := func(orgID, id string) (string, error) {
		subject, rerr := vault.Resolve(f.OrgCtx(orgID), orgID, id)
		if rerr != nil {
			return "", rerr
		}
		return subject.Data["v"], nil
	}
	legacyCheckout, _ := stored(returned)
	legacySubject := signer("checkout_signature", legacyCheckout)
	for subject, want := range map[string]string{checkoutSubject: "Max Verifier", returnSubject: "Rita Rueckgabe", legacySubject: "Lena Legacy"} {
		if got, rerr := name(f.OrgA, subject); rerr != nil || got != want {
			t.Fatalf("signer %s = %q, %v, want %q", subject, got, rerr, want)
		}
	}
	if !strings.Contains(legacyCheckout, `"image_base64": "aGVsbG8="`) {
		t.Fatalf("the image of the signature was lost: %s", legacyCheckout)
	}

	// Another organization does not resolve the signer.
	if got, rerr := name(f.OrgB, checkoutSubject); rerr == nil {
		t.Fatalf("signer resolvable in another organization: %q", got)
	}

	// Deleting the vault entry shreds the name; the signature with its
	// surrogate stays in the table, the other signers stay resolvable.
	if err = vault.Delete(ctx, f.OrgA, checkoutSubject); err != nil {
		t.Fatal(err)
	}
	if got, rerr := name(f.OrgA, checkoutSubject); !errors.Is(rerr, pii.ErrNotFound) {
		t.Fatalf("shredded signer = %q, %v, want pii.ErrNotFound", got, rerr)
	}
	if exists(`SELECT 1 FROM pii_subject WHERE organization_id = $1 AND surrogate_id = $2`, f.OrgA, checkoutSubject) {
		t.Fatal("the vault entry of the shredded signer is left")
	}
	if after, _ := stored(handover); after != checkout {
		t.Fatalf("the signature changed by shredding: %s, want %s", after, checkout)
	}
	if got, rerr := name(f.OrgA, returnSubject); rerr != nil || got != "Rita Rueckgabe" {
		t.Fatalf("the return signer after shredding the handover signer = %q, %v", got, rerr)
	}
}
