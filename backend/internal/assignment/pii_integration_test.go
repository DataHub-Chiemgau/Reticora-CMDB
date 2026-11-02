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
// and return signatures live in the PII vault and the assignment table
// keeps only their surrogates; the REST resource returns the names
// unchanged; a transfer keeps the stored signatures; existing plaintext
// signer names are moved by the pii.backfill run; deleting the vault entry
// shreds the name, deleting the assignment deletes its entries.
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
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
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
	plaintext := func(query string, args ...any) bool {
		t.Helper()
		var found bool
		if qerr := f.Admin.QueryRow(bg, `SELECT EXISTS (`+query+`)`, args...).Scan(&found); qerr != nil {
			t.Fatal(qerr)
		}
		return found
	}
	subjects := func() int {
		t.Helper()
		var n int
		if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM pii_subject WHERE organization_id = $1`, f.OrgA).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n
	}

	// Handover and return through the REST resource.
	w := call(http.MethodPost, "/api/v1/assignments", `{"asset_id": "`+asset+`", "assigned_to": "`+holder+`",
		"signature": {"signer_name": "Max Verifier", "method": "typed", "signed_at": "2026-10-28T10:00:00Z"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	created := decode(w)
	if created.CheckoutSignature == nil || created.CheckoutSignature.SignerName != "Max Verifier" {
		t.Fatalf("create response = %s", w.Body.String())
	}
	w = call(http.MethodPost, "/api/v1/assignments/"+created.ID+"/return", `{"return_condition": "good",
		"signature": {"signer_name": "Rita Rueckgabe", "method": "typed", "signed_at": "2026-10-28T11:00:00Z"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("return: %d %s", w.Code, w.Body.String())
	}
	returned := decode(w)
	if returned.CheckoutSignature == nil || returned.CheckoutSignature.SignerName != "Max Verifier" ||
		returned.ReturnSignature == nil || returned.ReturnSignature.SignerName != "Rita Rueckgabe" {
		t.Fatalf("return response = %s", w.Body.String())
	}

	// The table keeps only surrogates; the vault holds the names encrypted.
	checkout, ret := stored(created.ID)
	if strings.Contains(checkout, "Max Verifier") {
		t.Errorf("assignment.checkout_signature holds the signer name in plaintext: %s", checkout)
	}
	if strings.Contains(ret, "Rita Rueckgabe") {
		t.Errorf("assignment.return_signature holds the signer name in plaintext: %s", ret)
	}
	if t.Failed() {
		t.FailNow()
	}
	checkoutSubject, returnSubject := signer("checkout_signature", checkout), signer("return_signature", ret)
	if !plaintext(`SELECT 1 FROM pii_subject WHERE organization_id = $1 AND surrogate_id = $2 AND dek_ref > 0`, f.OrgA, checkoutSubject) ||
		!plaintext(`SELECT 1 FROM pii_subject WHERE organization_id = $1 AND surrogate_id = $2 AND dek_ref > 0`, f.OrgA, returnSubject) {
		t.Fatal("the signer surrogates have no vault entry")
	}
	if plaintext(`SELECT 1 FROM pii_subject WHERE encode(encrypted_payload, 'escape') ~ '(Verifier|Rueckgabe)'`) {
		t.Fatal("the vault holds the signer names in plaintext")
	}
	if !strings.Contains(checkout, `"method": "typed"`) || !strings.Contains(ret, `"signed_at": "2026-10-28T11:00:00Z"`) {
		t.Fatalf("the other signature fields were lost: %s / %s", checkout, ret)
	}

	// Detail and list resolve the names (REST contract unchanged).
	got := decode(call(http.MethodGet, "/api/v1/assignments/"+created.ID, ""))
	if got.CheckoutSignature == nil || got.CheckoutSignature.SignerName != "Max Verifier" || got.CheckoutSignature.Method != "typed" ||
		got.ReturnSignature == nil || got.ReturnSignature.SignerName != "Rita Rueckgabe" {
		t.Fatalf("get = %+v / %+v", got.CheckoutSignature, got.ReturnSignature)
	}
	if list := call(http.MethodGet, "/api/v1/assignments", "").Body.String(); !strings.Contains(list, `"signer_name":"Max Verifier"`) ||
		!strings.Contains(list, `"signer_name":"Rita Rueckgabe"`) {
		t.Fatalf("list does not resolve the signers: %s", list)
	}

	// A transfer carries the read signatures along without a new vault
	// entry and keeps the stored surrogates.
	before := subjects()
	if w = call(http.MethodPost, "/api/v1/assignments/"+created.ID+"/transfer", `{"new_assignee": "`+successor+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("transfer: %d %s", w.Code, w.Body.String())
	}
	if after := subjects(); after != before {
		t.Fatalf("transfer changed the vault entries: %d -> %d", before, after)
	}
	if c2, r2 := stored(created.ID); c2 != checkout || r2 != ret {
		t.Fatalf("transfer rewrote the signatures: %s / %s", c2, r2)
	}

	// An existing assignment with plaintext signer names is moved by the
	// backfill.
	legacy := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO assignment (id, organization_id, asset_id, assigned_to, assigned_by, assignment_type, status,
			checkout_signature, return_signature)
		VALUES ($1, $2, $3, $4, $4, 'assignment', 'returned',
			'{"signer_name": "Lena Legacy", "method": "drawn", "image_base64": "aGVsbG8="}', '{"signer_name": "Otto Old"}')`,
		legacy, f.OrgA, asset, holder); err != nil {
		t.Fatal(err)
	}
	res, err := jobs.NewPIIBackfill(f.App).RunOrg(bg, f.OrgA)
	if err != nil || res.Total() < 1 {
		t.Fatalf("backfill = %+v, %v", res, err)
	}
	if plaintext(`SELECT 1 FROM assignment WHERE organization_id = $1 AND (checkout_signature::text || COALESCE(return_signature::text, ''))
			~ '(Verifier|Rueckgabe|Legacy|Otto)'`, f.OrgA) {
		t.Fatal("plaintext signer names left after the backfill")
	}
	moved := decode(call(http.MethodGet, "/api/v1/assignments/"+legacy, ""))
	if moved.CheckoutSignature == nil || moved.CheckoutSignature.SignerName != "Lena Legacy" || moved.CheckoutSignature.ImageBase64 != "aGVsbG8=" ||
		moved.ReturnSignature == nil || moved.ReturnSignature.SignerName != "Otto Old" {
		t.Fatalf("moved assignment = %+v / %+v", moved.CheckoutSignature, moved.ReturnSignature)
	}
	if again, aerr := jobs.NewPIIBackfill(f.App).RunOrg(bg, f.OrgA); aerr != nil || again.Total() != 0 {
		t.Fatalf("second backfill = %+v, %v", again, aerr)
	}

	// Another organization does not resolve the signer.
	if _, rerr := vault.Resolve(f.OrgCtx(f.OrgB), f.OrgB, checkoutSubject); rerr == nil {
		t.Fatal("signer resolvable in another organization")
	}

	// Deleting the vault entry shreds the name; the signature stays.
	if err = vault.Delete(ctx, f.OrgA, checkoutSubject); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodGet, "/api/v1/assignments/"+created.ID, "")
	shredded := decode(w)
	if w.Code != http.StatusOK || shredded.CheckoutSignature == nil || shredded.CheckoutSignature.SignerName != "" ||
		shredded.CheckoutSignature.Method != "typed" || strings.Contains(w.Body.String(), "Max Verifier") ||
		shredded.ReturnSignature == nil || shredded.ReturnSignature.SignerName != "Rita Rueckgabe" {
		t.Fatalf("after shredding: %d %s", w.Code, w.Body.String())
	}

	// Deleting the assignment deletes the vault entries of its signers.
	_, legacyRet := stored(legacy)
	legacySubject := signer("return_signature", legacyRet)
	if w = call(http.MethodDelete, "/api/v1/assignments/"+legacy, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if plaintext(`SELECT 1 FROM pii_subject WHERE surrogate_id = $1`, legacySubject) {
		t.Fatal("the vault entry of a deleted assignment's signer is left")
	}
}
