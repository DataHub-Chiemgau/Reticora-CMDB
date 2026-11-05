package assignment_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// pngPrefix is the base64 of the PNG signature; no audit entry, event or
// vault subject may contain it.
const pngPrefix = "iVBORw0KGgo"

// pngImage returns size bytes that pass as a PNG by content (signature
// \x89PNG\r\n\x1a\n, http.DetectContentType image/png).
func pngImage(size int) []byte {
	b := make([]byte, size)
	copy(b, "\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	for i := 16; i < size; i++ {
		b[i] = byte(i % 251)
	}
	return b
}

// features is the entitlement port of the tests: the listed features are
// enabled; limit, when set, is the storage limit in bytes.
type features struct {
	enabled map[string]bool
	limit   *int64
}

func (f *features) IsEnabled(_ context.Context, _, key string) bool { return f.enabled[key] }

func (f *features) StorageLimitBytes(context.Context, string) (*int64, error) { return f.limit, nil }

func withSignature() *features {
	return &features{enabled: map[string]bool{entitlement.FeatureSignature: true}}
}

// signFixture runs the assignment and movement routes with signatures over
// a file blob store.
type signFixture struct {
	f     *scopetest.Fixture
	t     *testing.T
	actor string
	dir   string
	blobs *blob.FileStore
	mux   *chi.Mux
}

func newSignFixture(t *testing.T, f *scopetest.Fixture, actor string, feats *features) *signFixture {
	t.Helper()
	s := &signFixture{f: f, t: t, actor: actor, dir: t.TempDir()}
	s.blobs = blob.NewFileStore(s.dir)
	movements := movement.NewService(movement.NewPGRepository(f.App))
	svc := assignment.NewService(f.App, movements, events.NewOutbox()).
		WithSignatures(feats, document.NewPGRepository(f.App), s.blobs, nil)
	s.mux = chi.NewRouter()
	assignment.NewHandler(assignment.NewPGRepository(f.App)).WithService(svc).RegisterRoutes(s.mux)
	movement.NewHandler(movements).RegisterRoutes(s.mux)
	return s
}

func (s *signFixture) org() context.Context {
	return tenant.WithTenant(s.f.OrgCtx(s.f.OrgA), tenant.TenantInfo{OrganizationID: s.f.OrgA, UserID: s.actor})
}

func (s *signFixture) client(id string) context.Context {
	return tenant.WithTenant(s.f.ClientCtx(id), tenant.TenantInfo{OrganizationID: s.f.OrgA, ClientID: id, UserID: s.actor})
}

func (s *signFixture) call(ctx context.Context, path string, body any, header ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		s.t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw))).WithContext(ctx)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	return w
}

// sig is the signature member of a request.
func sig(png []byte, signer map[string]string) map[string]any {
	out := map[string]any{"image_png_base64": base64.StdEncoding.EncodeToString(png)}
	if signer != nil {
		out["signer"] = signer
	}
	return out
}

// blobCount is the number of stored blobs.
func (s *signFixture) blobCount() int {
	s.t.Helper()
	n := 0
	if err := filepath.WalkDir(s.dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	}); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *signFixture) count(query string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.f.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

// signedMovement is a signed movement as stored.
type signedMovement struct {
	m        movement.Movement
	category string
	mime     string
	version  int
	checksum string
	key      string
	links    int
}

// signed reads the movement id, its document, version 1 and links and
// checks that the stored hash equals the recomputation over the stored
// blob (movement.SignatureHash).
func (s *signFixture) signed(id string) signedMovement {
	s.t.Helper()
	bg := context.Background()
	var out signedMovement
	m := &out.m
	if err := s.f.Admin.QueryRow(bg, `SELECT id::text, organization_id::text, asset_id::text, movement_type,
			COALESCE(assignee_user_id::text, ''), COALESCE(assignee_team_id::text, ''),
			COALESCE(signer_user_id::text, ''), COALESCE(signer_surrogate::text, ''), COALESCE(actor_id::text, ''),
			occurred_at, COALESCE(document_id::text, ''), COALESCE(signature_hash, '')
		FROM stock_movement WHERE id = $1`, id).Scan(&m.ID, &m.OrganizationID, &m.AssetID, &m.MovementType,
		&m.AssigneeUserID, &m.AssigneeTeamID, &m.SignerUserID, &m.SignerSurrogate, &m.ActorID,
		&m.OccurredAt, &m.DocumentID, &m.SignatureHash); err != nil {
		s.t.Fatalf("movement %s: %v", id, err)
	}
	if m.DocumentID == "" || m.SignatureHash == "" {
		s.t.Fatalf("movement %s carries no signature", id)
	}
	if err := s.f.Admin.QueryRow(bg, `SELECT d.category, d.mime_type, v.version, COALESCE(v.checksum_sha256, ''), v.storage_key,
			(SELECT count(*) FROM document_link l WHERE l.document_id = d.id AND l.entity_type = 'stock_movement' AND l.entity_id = $2)
		FROM document d JOIN document_version v ON v.document_id = d.id WHERE d.id = $1`, m.DocumentID, id).
		Scan(&out.category, &out.mime, &out.version, &out.checksum, &out.key, &out.links); err != nil {
		s.t.Fatalf("document of %s: %v", id, err)
	}
	rc, err := s.blobs.Get(bg, document.Bucket, out.key)
	if err != nil {
		s.t.Fatalf("blob of %s: %v", id, err)
	}
	png, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		s.t.Fatal(err)
	}
	sum := sha256.Sum256(png)
	if out.checksum != hex.EncodeToString(sum[:]) {
		s.t.Errorf("checksum of %s does not match the blob", id)
	}
	if got := movement.SignatureHash(png, m); got != m.SignatureHash {
		s.t.Errorf("signature_hash of %s = %s, recomputed %s", id, m.SignatureHash, got)
	}
	if out.category != "signature" || out.mime != "image/png" || out.version != 1 || out.links != 1 {
		s.t.Errorf("document of %s: category %s, mime %s, version %d, links %d", id, out.category, out.mime, out.version, out.links)
	}
	return out
}

func decodeMovement(t *testing.T, w *httptest.ResponseRecorder) movement.Movement {
	t.Helper()
	var out movement.Movement
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return out
}

// TestSignatureAssignReturnTransfer covers AST-02 (WP-276) against
// PostgreSQL: assignment, return and transfer with a signature each give a
// document of the category signature (image/png, version 1 with checksum),
// a link stock_movement and a signature_hash equal to the recomputation
// over the stored blob; the transfer binds signature to the assignment and
// return_signature to the return. Without signer the holder signs.
func TestSignatureAssignReturnTransfer(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp276actor")
	s := newSignFixture(t, f, actor, withSignature())
	asset := f.Asset(t, f.OrgA, f.Client1, "wp276-asset")
	alice := f.AppUser(t, f.OrgA, "wp276alice")
	bob := f.AppUser(t, f.OrgA, "wp276bob")
	ctx := s.org()
	path := "/api/v1/assets/" + asset

	w := s.call(ctx, path+"/assign", map[string]any{"assigned_user_id": alice, "signature": sig(pngImage(2048), nil)})
	if w.Code != http.StatusCreated {
		t.Fatalf("signed assign: %d %s", w.Code, w.Body.String())
	}
	a := decodeAssignment(t, w)
	if a.Signature == nil || a.Signature.SignerUserID != alice || a.Signature.SignerSurrogate != "" ||
		!a.Signature.SignedAt.Equal(a.AssignedAt) {
		t.Fatalf("signature of the assignment: %+v", a.Signature)
	}
	got := s.signed(a.ID)
	if got.m.SignerUserID != alice || got.m.DocumentID != a.Signature.DocumentID || got.m.SignatureHash != a.Signature.SignatureHash {
		t.Errorf("stored signature %+v, answered %+v", got.m, a.Signature)
	}

	w = s.call(ctx, path+"/return", map[string]any{"expected_holder": alice, "signature": sig(pngImage(1024), nil)})
	if w.Code != http.StatusOK {
		t.Fatalf("signed return: %d %s", w.Code, w.Body.String())
	}
	ret := decodeMovement(t, w)
	if ret.Signature == nil || ret.Signature.SignerUserID != alice {
		t.Fatalf("signature of the return: %+v", ret.Signature)
	}
	s.signed(ret.ID)

	// An unsigned assignment, then a transfer with both signatures.
	if w = s.call(ctx, path+"/assign", map[string]any{"assigned_user_id": alice}); w.Code != http.StatusCreated {
		t.Fatalf("unsigned assign: %d %s", w.Code, w.Body.String())
	}
	if a = decodeAssignment(t, w); a.Signature != nil {
		t.Errorf("unsigned assignment carries %+v", a.Signature)
	}
	w = s.call(ctx, path+"/transfer", map[string]any{"expected_holder": alice, "assigned_user_id": bob,
		"signature": sig(pngImage(3000), nil), "return_signature": sig(pngImage(4000), nil)})
	if w.Code != http.StatusCreated {
		t.Fatalf("signed transfer: %d %s", w.Code, w.Body.String())
	}
	next := decodeAssignment(t, w)
	assigned := s.signed(next.ID)
	if assigned.m.MovementType != "assignment" || assigned.m.SignerUserID != bob {
		t.Errorf("signature of the transfer: %+v, want the assignment signed by the new holder", assigned.m)
	}
	var backID string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM stock_movement
		WHERE asset_id = $1 AND movement_type = 'return' AND occurred_at = $2`, asset, assigned.m.OccurredAt).Scan(&backID); err != nil {
		t.Fatalf("return of the transfer: %v", err)
	}
	back := s.signed(backID)
	if back.m.SignerUserID != alice || back.m.DocumentID == assigned.m.DocumentID || back.m.SignatureHash == assigned.m.SignatureHash {
		t.Errorf("return signature %+v, assignment signature %+v", back.m, assigned.m)
	}

	// The ledger lists the signatures.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/stock-movements?asset_id="+asset, http.NoBody).WithContext(ctx)
	lw := httptest.NewRecorder()
	s.mux.ServeHTTP(lw, r)
	var list struct {
		Data []movement.Movement `json:"data"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &list); err != nil || lw.Code != http.StatusOK {
		t.Fatalf("list movements: %d %s %v", lw.Code, lw.Body.String(), err)
	}
	signedRows := 0
	for i := range list.Data {
		if list.Data[i].Signature != nil {
			signedRows++
		}
	}
	if signedRows != 4 {
		t.Errorf("listed signed movements = %d, want 4", signedRows)
	}
}

// TestSignatureSigner covers the signer (E-51, convention 16, WP-276): a
// team holder needs a signer (422); name creates a vault subject of the
// kind person and neither stock_movement nor document holds the name; a
// user of another organization is 422; the former fields are 422.
func TestSignatureSigner(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp276actor")
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })

	s := newSignFixture(t, f, actor, withSignature())
	ctx := s.org()
	asset := f.Asset(t, f.OrgA, f.Client1, "wp276-signer")
	foreign := f.AppUser(t, f.OrgB, "wp276foreign")
	crew := f.ID()
	if _, err = f.Admin.Exec(context.Background(), `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'wp276-crew')`, crew, f.OrgA); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/assets/" + asset
	img := pngImage(1500)

	w := s.call(ctx, path+"/assign", map[string]any{"assigned_team_id": crew, "signature": sig(img, nil)})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "signature.signer") {
		t.Fatalf("team holder without signer: %d %s, want 422 on signature.signer", w.Code, w.Body.String())
	}
	w = s.call(ctx, path+"/assign", map[string]any{"assigned_team_id": crew,
		"signature": sig(img, map[string]string{"user_id": foreign})})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("signer of another organization: %d %s, want 422", w.Code, w.Body.String())
	}
	if n := s.count(`SELECT count(*) FROM stock_movement WHERE asset_id = $1`, asset); n != 0 || s.blobCount() != 0 {
		t.Fatalf("refused signatures left %d movements, %d blobs", n, s.blobCount())
	}

	const name = "Erika Mustermann"
	subjects := s.count(`SELECT count(*) FROM pii_subject WHERE organization_id = $1 AND kind = 'person'`, f.OrgA)
	w = s.call(ctx, path+"/assign", map[string]any{"assigned_team_id": crew, "signature": sig(img, map[string]string{"name": name})})
	if w.Code != http.StatusCreated {
		t.Fatalf("signer by name: %d %s", w.Code, w.Body.String())
	}
	a := decodeAssignment(t, w)
	if a.Signature == nil || a.Signature.SignerSurrogate == "" || a.Signature.SignerUserID != "" {
		t.Fatalf("signature of the external signer: %+v", a.Signature)
	}
	got := s.signed(a.ID)
	if got.m.SignerSurrogate != a.Signature.SignerSurrogate {
		t.Errorf("stored surrogate differs from the answer")
	}
	if n := s.count(`SELECT count(*) FROM pii_subject WHERE organization_id = $1 AND kind = 'person' AND surrogate_id = $2`,
		f.OrgA, got.m.SignerSurrogate); n != 1 {
		t.Errorf("vault subject of the signer: %d", n)
	}
	if n := s.count(`SELECT count(*) FROM pii_subject WHERE organization_id = $1 AND kind = 'person'`, f.OrgA); n != subjects+1 {
		t.Errorf("person subjects %d, want %d", n, subjects+1)
	}
	if n := s.count(`SELECT count(*) FROM stock_movement m WHERE m.id = $1 AND row_to_json(m)::text LIKE '%' || $2 || '%'`, a.ID, name); n != 0 {
		t.Error("stock_movement holds the name of the signer")
	}
	if n := s.count(`SELECT count(*) FROM document d WHERE d.id = $1 AND row_to_json(d)::text LIKE '%' || $2 || '%'`, got.m.DocumentID, name); n != 0 {
		t.Error("document holds the name of the signer")
	}
	if strings.Contains(w.Body.String(), name) {
		t.Error("the answer holds the name of the signer")
	}

	// The former fields answer 422 on the field, not 400.
	for _, legacy := range []string{"image_base64", "object_key", "signer_name", "signed_at", "method"} {
		body := map[string]any{"expected_holder": crew, "signature": map[string]any{
			"image_png_base64": base64.StdEncoding.EncodeToString(img), legacy: "x"}}
		w = s.call(ctx, path+"/return", body)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "signature."+legacy) {
			t.Errorf("legacy field %s: %d %s, want 422 on the field", legacy, w.Code, w.Body.String())
		}
	}
}

// TestSignatureLimits covers the image limits (WP-276): 512 KiB with an
// Idempotency-Key are taken, 600 KiB in a body below 1 MiB answer 413 (not
// 400), a body above 1 MiB 413, a PDF 422 mime-mismatch.
func TestSignatureLimits(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp276actor")
	s := newSignFixture(t, f, actor, withSignature())
	ctx := s.org()
	alice := f.AppUser(t, f.OrgA, "wp276lalice")
	asset := f.Asset(t, f.OrgA, f.Client1, "wp276-limits")
	path := "/api/v1/assets/" + asset

	w := s.call(ctx, path+"/assign", map[string]any{"assigned_user_id": alice, "signature": sig(pngImage(600<<10), nil)})
	if w.Code != http.StatusRequestEntityTooLarge || problemType(t, w) != "https://reticora.io/problems/413" {
		t.Fatalf("600 KiB: %d %s, want 413", w.Code, w.Body.String())
	}
	w = s.call(ctx, path+"/assign", map[string]any{"assigned_user_id": alice, "notes": strings.Repeat("n", 1<<20)})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body above 1 MiB: %d, want 413", w.Code)
	}
	pdf := append([]byte("%PDF-1.4\n"), make([]byte, 600)...)
	w = s.call(ctx, path+"/assign", map[string]any{"assigned_user_id": alice, "signature": sig(pdf, nil)})
	if w.Code != http.StatusUnprocessableEntity || problemType(t, w) != document.ProblemMimeMismatch {
		t.Fatalf("PDF: %d %s, want 422 mime-mismatch", w.Code, w.Body.String())
	}
	if s.blobCount() != 0 || s.count(`SELECT count(*) FROM stock_movement WHERE asset_id = $1`, asset) != 0 {
		t.Fatal("a refused image left a blob or a movement")
	}
	w = s.call(ctx, path+"/assign", map[string]any{"assigned_user_id": alice, "signature": sig(pngImage(512<<10), nil)},
		"Idempotency-Key", "wp276-limits-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("512 KiB: %d %s", w.Code, w.Body.String())
	}
	s.signed(decodeAssignment(t, w).ID)
}

// TestSignatureEntitlementAndRollback covers the gate and the rollback
// (E-44, K20, WP-276): without the feature signature 403
// feature-not-entitled leaves no movement, document or blob; an exceeded
// max_storage_mb answers 403 entitlement-limit without blob; a holder
// conflict after the blob put (409) deletes the blob and leaves no
// document; without signature assign stays possible.
func TestSignatureEntitlementAndRollback(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp276actor")
	alice := f.AppUser(t, f.OrgA, "wp276ealice")
	asset := f.Asset(t, f.OrgA, f.Client1, "wp276-gate")
	path := "/api/v1/assets/" + asset
	img := pngImage(4096)
	documents := func(s *signFixture) int {
		return s.count(`SELECT count(*) FROM document WHERE organization_id = $1 AND category = 'signature'`, f.OrgA)
	}

	off := newSignFixture(t, f, actor, &features{enabled: map[string]bool{}})
	before := documents(off)
	w := off.call(off.org(), path+"/assign", map[string]any{"assigned_user_id": alice, "signature": sig(img, nil)})
	if w.Code != http.StatusForbidden || problemType(t, w) != "https://reticora.io/problems/feature-not-entitled" {
		t.Fatalf("without feature: %d %s", w.Code, w.Body.String())
	}
	if off.count(`SELECT count(*) FROM stock_movement WHERE asset_id = $1`, asset) != 0 || documents(off) != before || off.blobCount() != 0 {
		t.Fatal("the refused signature left a movement, document or blob")
	}

	limit := int64(1024)
	small := newSignFixture(t, f, actor, &features{enabled: map[string]bool{entitlement.FeatureSignature: true}, limit: &limit})
	w = small.call(small.org(), path+"/assign", map[string]any{"assigned_user_id": alice, "signature": sig(img, nil)})
	if w.Code != http.StatusForbidden || problemType(t, w) != "https://reticora.io/problems/entitlement-limit" {
		t.Fatalf("storage limit: %d %s", w.Code, w.Body.String())
	}
	if small.blobCount() != 0 || documents(small) != before {
		t.Fatal("the refused signature left a blob or a document")
	}

	// Unsigned assign without the feature, then a signed assign of the
	// held asset: 409 after the blob put.
	if w = off.call(off.org(), path+"/assign", map[string]any{"assigned_user_id": alice}); w.Code != http.StatusCreated {
		t.Fatalf("unsigned assign: %d %s", w.Code, w.Body.String())
	}
	on := newSignFixture(t, f, actor, withSignature())
	w = on.call(on.org(), path+"/assign", map[string]any{"assigned_user_id": alice, "signature": sig(img, nil)})
	if w.Code != http.StatusConflict {
		t.Fatalf("held asset: %d %s, want 409", w.Code, w.Body.String())
	}
	if on.blobCount() != 0 || documents(on) != before {
		t.Fatal("the rolled back signature left a blob or a document")
	}
}

// TestSignatureScopeAndPII covers scope and PII (TEN-06, SEC-05, WP-276): a
// principal restricted to client 1 signs an assignment, the link passes
// WITH CHECK and takes client 1; a principal of client 2 sees neither link
// nor document; no audit entry, event or vault subject holds image data.
func TestSignatureScopeAndPII(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp276actor")
	s := newSignFixture(t, f, actor, withSignature())
	alice := f.AppUser(t, f.OrgA, "wp276calice")
	asset := f.Asset(t, f.OrgA, f.Client1, "wp276-scope")
	img := pngImage(2048)

	w := s.call(s.client(f.Client1), "/api/v1/assets/"+asset+"/assign", map[string]any{"assigned_user_id": alice, "signature": sig(img, nil)})
	if w.Code != http.StatusCreated {
		t.Fatalf("client-1 signed assign: %d %s", w.Code, w.Body.String())
	}
	a := decodeAssignment(t, w)
	got := s.signed(a.ID)
	if n := s.count(`SELECT count(*) FROM document_link WHERE document_id = $1 AND client_id = $2`, got.m.DocumentID, f.Client1); n != 1 {
		t.Errorf("link with client 1: %d", n)
	}

	other := s.client(f.Client2)
	if _, err := document.NewPGRepository(f.App).GetByID(other, f.OrgA, got.m.DocumentID); err == nil {
		t.Error("client 2 reads the signature document")
	}
	if links, err := document.NewPGRepository(f.App).GetLinks(other, f.OrgA, got.m.DocumentID); err == nil && len(links) > 0 {
		t.Errorf("client 2 sees the links %+v", links)
	}
	var docs, links int
	if err := database.WithRequestTenant(other, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM document WHERE id = $1`, got.m.DocumentID).Scan(&docs); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM document_link WHERE document_id = $1`, got.m.DocumentID).Scan(&links)
	}); err != nil {
		t.Fatal(err)
	}
	if docs != 0 || links != 0 {
		t.Errorf("client 2 selects %d documents, %d links", docs, links)
	}

	for _, table := range []string{"audit_log", "event_outbox", "pii_subject"} {
		if n := s.count(`SELECT count(*) FROM ` + table + ` t WHERE row_to_json(t)::text LIKE '%` + pngPrefix + `%'`); n != 0 {
			t.Errorf("%s holds image data in %d rows", table, n)
		}
	}
}

// signatureVector is the signature_hash of the fixed vector of
// TestSignatureHashVector, computed independently of the Go code.
const signatureVector = "6efb969f1aaad208fc14dc6cfa6abb9b9988054bbd31245048bf784eaf8e1e83"

// TestSignatureHashVector pins movement.SignatureHash (WP-276) without a
// database: a fixed vector, the fixed key order and the truncation of
// occurred_at to microseconds.
func TestSignatureHashVector(t *testing.T) {
	m := &movement.Movement{ID: "11111111-1111-4111-8111-111111111111", OrganizationID: "22222222-2222-4222-8222-222222222222",
		AssetID: "33333333-3333-4333-8333-333333333333", MovementType: "assignment",
		AssigneeUserID: "44444444-4444-4444-8444-444444444444", SignerUserID: "44444444-4444-4444-8444-444444444444",
		ActorID: "55555555-5555-4555-8555-555555555555", DocumentID: "66666666-6666-4666-8666-666666666666",
		OccurredAt: time.Date(2026, 11, 5, 10, 30, 0, 123456789, time.FixedZone("CET", 3600))}
	png := []byte("\x89PNG\r\n\x1a\nsig")
	canonical := `{"id":"11111111-1111-4111-8111-111111111111","organization_id":"22222222-2222-4222-8222-222222222222",` +
		`"asset_id":"33333333-3333-4333-8333-333333333333","movement_type":"assignment",` +
		`"assignee_user_id":"44444444-4444-4444-8444-444444444444","assignee_team_id":"",` +
		`"signer_user_id":"44444444-4444-4444-8444-444444444444","signer_surrogate":"",` +
		`"actor_id":"55555555-5555-4555-8555-555555555555","occurred_at":"2026-11-05T09:30:00.123456Z",` +
		`"document_id":"66666666-6666-4666-8666-666666666666"}`
	sum := sha256.Sum256(append(append(append([]byte{}, png...), 0), canonical...))
	want := hex.EncodeToString(sum[:])
	if got := movement.SignatureHash(png, m); got != want || got != signatureVector {
		t.Fatalf("SignatureHash = %s, want %s (vector %s)", got, want, signatureVector)
	}
	// Neither the time zone nor the nanoseconds below a microsecond count.
	m2 := *m
	m2.OccurredAt = time.Date(2026, 11, 5, 9, 30, 0, 123456000, time.UTC)
	if movement.SignatureHash(png, &m2) != want {
		t.Error("the hash depends on the time zone or on nanoseconds")
	}
	m2.SignerUserID, m2.SignerSurrogate = "", m.SignerUserID
	if movement.SignatureHash(png, &m2) == want {
		t.Error("the hash ignores which signer column holds the signer")
	}
}
