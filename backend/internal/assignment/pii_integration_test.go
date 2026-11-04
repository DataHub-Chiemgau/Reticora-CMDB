package assignment_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
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
// them, the REST resource (GET /api/v1/assignments) returns the names
// unchanged, another organization does not resolve them and deleting the
// vault entry shreds the name.
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
	mux := chi.NewRouter()
	assignment.NewHandler(assignment.NewPGRepository(f.App)).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: holder})
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
		return w
	}
	decode := func(w *httptest.ResponseRecorder) assignment.Assignment {
		t.Helper()
		var a assignment.Assignment
		if derr := json.Unmarshal(w.Body.Bytes(), &a); derr != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), derr)
		}
		return a
	}
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

	// Detail and list resolve the names (REST contract unchanged).
	got := decode(get("/api/v1/assignments/" + handover))
	gotReturned := decode(get("/api/v1/assignments/" + returned))
	if got.CheckoutSignature == nil || got.CheckoutSignature.SignerName != "Max Verifier" || got.CheckoutSignature.Method != "typed" ||
		gotReturned.ReturnSignature == nil || gotReturned.ReturnSignature.SignerName != "Rita Rueckgabe" ||
		gotReturned.CheckoutSignature == nil || gotReturned.CheckoutSignature.SignerName != "Lena Legacy" ||
		gotReturned.CheckoutSignature.ImageBase64 != "aGVsbG8=" {
		t.Fatalf("get = %+v / %+v / %+v", got.CheckoutSignature, gotReturned.CheckoutSignature, gotReturned.ReturnSignature)
	}
	if list := get("/api/v1/assignments").Body.String(); !strings.Contains(list, `"signer_name":"Max Verifier"`) ||
		!strings.Contains(list, `"signer_name":"Rita Rueckgabe"`) {
		t.Fatalf("list does not resolve the signers: %s", list)
	}

	// Another organization does not resolve the signer.
	if _, rerr := vault.Resolve(f.OrgCtx(f.OrgB), f.OrgB, checkoutSubject); rerr == nil {
		t.Fatal("signer resolvable in another organization")
	}

	// Deleting the vault entry shreds the name; the signature stays.
	if err = vault.Delete(ctx, f.OrgA, checkoutSubject); err != nil {
		t.Fatal(err)
	}
	w := get("/api/v1/assignments/" + handover)
	shredded := decode(w)
	if w.Code != http.StatusOK || shredded.CheckoutSignature == nil || shredded.CheckoutSignature.SignerName != "" ||
		shredded.CheckoutSignature.Method != "typed" || strings.Contains(w.Body.String(), "Max Verifier") {
		t.Fatalf("after shredding: %d %s", w.Code, w.Body.String())
	}
	if kept := decode(get("/api/v1/assignments/" + returned)); kept.ReturnSignature == nil ||
		kept.ReturnSignature.SignerName != "Rita Rueckgabe" {
		t.Fatalf("the return signature after shredding the handover signer = %+v", kept.ReturnSignature)
	}
}
