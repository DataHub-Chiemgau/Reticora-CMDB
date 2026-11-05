package assignment_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// cancelOnPut stores the blob and then cancels the run: the transaction
// after the blob put fails.
type cancelOnPut struct {
	blob.Store
	cancel context.CancelFunc
}

func (c *cancelOnPut) Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error {
	err := c.Store.Put(ctx, bucket, key, r, size, contentType)
	c.cancel()
	return err
}

// legacyFixture holds the legacy signatures of a test.
type legacyFixture struct {
	*signFixture
	asset string
}

// movement inserts an assignment movement of the asset booked by the actor.
func (l *legacyFixture) movement() string {
	l.t.Helper()
	id := l.f.ID()
	if _, err := l.f.Admin.Exec(context.Background(), `INSERT INTO stock_movement (id, organization_id, item_kind, asset_id,
			movement_type, assignee_user_id, actor_id)
		VALUES ($1, $2, 'asset', $3, 'assignment', $4, $4)`, id, l.f.OrgA, l.asset, l.actor); err != nil {
		l.t.Fatal(err)
	}
	return id
}

// legacy inserts a legacy signature of the movement as migration 000266
// keeps it (payload_sha256 over payload::text) and returns its id.
func (l *legacyFixture) legacy(movementID, kind string, payload map[string]any) string {
	l.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		l.t.Fatal(err)
	}
	var id string
	if err = l.f.Admin.QueryRow(context.Background(), `INSERT INTO assignment_signature_legacy
			(organization_id, movement_id, kind, payload, payload_sha256)
		VALUES ($1, $2, $3, $4::jsonb, encode(sha256(convert_to($4::jsonb::text, 'UTF8')), 'hex')) RETURNING id::text`,
		l.f.OrgA, movementID, kind, string(raw)).Scan(&id); err != nil {
		l.t.Fatal(err)
	}
	return id
}

// state is the payload and payload_sha256 of every legacy signature of
// organization A, by id.
func (l *legacyFixture) state() map[string][2]string {
	l.t.Helper()
	rows, err := l.f.Admin.Query(context.Background(), `SELECT id::text, COALESCE(payload::text, ''), payload_sha256
		FROM assignment_signature_legacy WHERE organization_id = $1`, l.f.OrgA)
	if err != nil {
		l.t.Fatal(err)
	}
	out := map[string][2]string{}
	for rows.Next() {
		var id, payload, sum string
		if err = rows.Scan(&id, &payload, &sum); err != nil {
			l.t.Fatal(err)
		}
		out[id] = [2]string{payload, sum}
	}
	if err = rows.Err(); err != nil {
		l.t.Fatal(err)
	}
	return out
}

// migrated reads the image-free payload of the legacy signature id.
func (l *legacyFixture) migrated(id string) map[string]any {
	l.t.Helper()
	var raw []byte
	if err := l.f.Admin.QueryRow(context.Background(), `SELECT payload FROM assignment_signature_legacy WHERE id = $1`, id).
		Scan(&raw); err != nil {
		l.t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		l.t.Fatal(err)
	}
	return out
}

// removeLegacy deletes the legacy signatures of the fixture organizations
// with the guards lifted, before the fixture removes the organizations.
func removeLegacy(t *testing.T, f *scopetest.Fixture) {
	t.Helper()
	ctx := context.Background()
	conn, err := f.Admin.Acquire(ctx)
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		t.Error(err)
		return
	}
	if _, err = conn.Exec(ctx, `DELETE FROM assignment_signature_legacy WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB); err != nil {
		t.Error(err)
	}
	if _, err = conn.Exec(ctx, `SET session_replication_role = origin`); err != nil {
		t.Error(err)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestLegacySignatureMigration covers AST-02 (E-51, WP-749) against
// PostgreSQL, a file blob store and the vault: the run signature.migrate
// takes a legacy signature with image_base64 and one with an object key
// below org/<org>/ over as documents of the category signature with
// version 1 and a link to the movement; signer_surrogate is the surrogate
// of signer_name without a new subject, a plaintext name becomes one new
// vault subject; the payload is image-free, payload_sha256 stays and
// stock_movement.signature_migrated is written. A foreign or missing
// object key and a non-image stay unchanged and are counted, as does a
// row over the storage limit; a second run changes nothing; a failure
// after the blob put deletes the blob. The guard refuses DELETE, a
// changed payload_sha256, an image in payload and a second replacement.
// The verification answers legacy with valid null; document, audit_log
// and the legacy table hold no plaintext name; client scope 2 sees
// neither the link nor the movement.
func TestLegacySignatureMigration(t *testing.T) {
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
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })
	actor := f.AppUser(t, f.OrgA, "wp749actor")
	l := &legacyFixture{signFixture: newSignFixture(t, f, actor, withSignature()), asset: f.Asset(t, f.OrgA, f.Client1, "wp749-legacy")}
	t.Cleanup(func() { removeLegacy(t, f) })

	// The surrogate of a signer sealed before the drop (WP-656).
	var ref map[string]any
	scope := database.OrgWideScope(f.OrgA, "")
	if err = database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var serr error
		ref, serr = pii.SealValueTx(ctx, tx, f.OrgA, "Lena Legacy")
		return serr
	}); err != nil {
		t.Fatal(err)
	}
	subject, ok := pii.RefID(ref)
	if !ok || !uuidPattern.MatchString(subject) {
		t.Fatalf("surrogate %v", ref)
	}

	checkoutPNG, returnPNG := pngImage(1500), pngImage(900)
	objectKey := blob.OrgKey(f.OrgA, "signatures", "wp749-return.png")
	if err = l.blobs.Put(bg, document.Bucket, objectKey, bytes.NewReader(returnPNG), int64(len(returnPNG)), "image/png"); err != nil {
		t.Fatal(err)
	}
	signed := l.movement()
	checkout := l.legacy(signed, "checkout", map[string]any{"image_base64": base64.StdEncoding.EncodeToString(checkoutPNG),
		"signer_name": ref, "signed_at": "2026-01-02T03:04:05Z", "method": "touchscreen"})
	ret := l.legacy(signed, "return", map[string]any{"object_key": objectKey, "signer_name": "Klara Klartext",
		"signed_at": "2026-01-03T04:05:06Z", "method": "drawn"})
	foreign := l.legacy(l.movement(), "checkout", map[string]any{"object_key": blob.OrgKey(f.OrgB, "x.png"), "signer_name": ref})
	missing := l.legacy(l.movement(), "checkout", map[string]any{"signer_name": ref, "method": "typed"})
	notImage := l.legacy(l.movement(), "checkout", map[string]any{"image_base64": base64.StdEncoding.EncodeToString([]byte("hello, world")),
		"signer_name": ref})
	before := l.state()
	subjects := func() int {
		return l.count(`SELECT count(*) FROM pii_subject WHERE organization_id = $1`, f.OrgA)
	}
	subjectsBefore, blobsBefore := subjects(), l.blobCount()
	documents := func() int {
		return l.count(`SELECT count(*) FROM document WHERE organization_id = $1`, f.OrgA)
	}

	// A failure after the blob put (the run is cancelled once the image is
	// stored) rolls the batch back and deletes the blob.
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	if _, err = assignment.NewLegacySignatures(f.App, &cancelOnPut{Store: l.blobs, cancel: cancel}).RunOrg(ctx, f.OrgA); err == nil {
		t.Fatal("the cancelled run succeeded")
	}
	if n := l.blobCount(); n != blobsBefore {
		t.Fatalf("blobs after the failed run: %d, want %d", n, blobsBefore)
	}
	if got := l.state(); !reflect.DeepEqual(got, before) || documents() != 0 || subjects() != subjectsBefore {
		t.Fatalf("the failed run changed rows: %v, documents %d", got, documents())
	}

	run := assignment.NewLegacySignatures(f.App, l.blobs)
	res, err := run.RunOrg(bg, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	wantSkipped := map[string]int{assignment.LegacySkipForeignKey: 1, assignment.LegacySkipMissingImage: 1, assignment.LegacySkipNoImage: 1}
	if res.Migrated != 2 || !reflect.DeepEqual(res.Reasons, wantSkipped) {
		t.Fatalf("run: migrated %d, skipped %v, want 2 and %v", res.Migrated, res.Reasons, wantSkipped)
	}
	after := l.state()
	for _, id := range []string{foreign, missing, notImage} {
		if after[id] != before[id] {
			t.Errorf("skipped row %s changed: %v", id, after[id])
		}
	}

	// Both taken over: image-free payload, payload_sha256 kept, document,
	// version 1 with the image, a link to the movement, the audit entry.
	if n := subjects(); n != subjectsBefore+1 {
		t.Errorf("vault subjects: %d, want %d (one for the plaintext name)", n, subjectsBefore+1)
	}
	for _, c := range []struct {
		id, signedAt, method string
		image                []byte
	}{
		{checkout, "2026-01-02T03:04:05Z", "touchscreen", checkoutPNG},
		{ret, "2026-01-03T04:05:06Z", "drawn", returnPNG},
	} {
		if after[c.id][1] != before[c.id][1] {
			t.Errorf("payload_sha256 of %s changed", c.id)
		}
		p := l.migrated(c.id)
		if keys := sortedKeys(p); !reflect.DeepEqual(keys, []string{"document_id", "method", "signed_at", "signer_surrogate"}) {
			t.Fatalf("payload of %s has keys %v", c.id, keys)
		}
		if p["signed_at"] != c.signedAt || p["method"] != c.method {
			t.Errorf("payload of %s = %v", c.id, p)
		}
		docID, _ := p["document_id"].(string)
		var category, mime, storageKey, checksum, uploadedBy string
		var version, links int
		if err = f.Admin.QueryRow(bg, `SELECT d.category, d.mime_type, d.uploaded_by::text, v.version, v.storage_key,
				COALESCE(v.checksum_sha256, ''),
				(SELECT count(*) FROM document_link k WHERE k.document_id = d.id AND k.entity_type = 'stock_movement' AND k.entity_id = $2)
			FROM document d JOIN document_version v ON v.document_id = d.id WHERE d.id = $1`, docID, signed).
			Scan(&category, &mime, &uploadedBy, &version, &storageKey, &checksum, &links); err != nil {
			t.Fatalf("document of %s: %v", c.id, err)
		}
		if category != document.CategorySignature || mime != "image/png" || version != 1 || links != 1 || uploadedBy != actor ||
			!document.OwnsStorageKey(f.OrgA, docID, storageKey) {
			t.Errorf("document of %s: %s %s v%d links %d by %s key %s", c.id, category, mime, version, links, uploadedBy, storageKey)
		}
		rc, gerr := l.blobs.Get(bg, document.Bucket, storageKey)
		if gerr != nil {
			t.Fatalf("image of %s: %v", c.id, gerr)
		}
		stored, _ := io.ReadAll(rc)
		_ = rc.Close()
		if !bytes.Equal(stored, c.image) {
			t.Errorf("image of %s differs", c.id)
		}
		var changes map[string]any
		var raw []byte
		if err = f.Admin.QueryRow(bg, `SELECT changes FROM audit_log WHERE organization_id = $1 AND action = $2
				AND resource_type = 'stock_movement' AND resource_id = $3 AND changes->>'document_id' = $4`,
			f.OrgA, assignment.ActionSignatureMigrated, signed, docID).Scan(&raw); err != nil {
			t.Fatalf("audit of %s: %v", c.id, err)
		}
		if err = json.Unmarshal(raw, &changes); err != nil {
			t.Fatal(err)
		}
		if changes["movement_id"] != signed || changes["payload_sha256"] != before[c.id][1] || len(changes) != 3 {
			t.Errorf("audit changes of %s: %v", c.id, changes)
		}
	}
	if p := l.migrated(checkout); p["signer_surrogate"] != subject {
		t.Errorf("signer of the checkout = %v, want the surrogate %s", p["signer_surrogate"], subject)
	}
	newSubject, _ := l.migrated(ret)["signer_surrogate"].(string)
	if !uuidPattern.MatchString(newSubject) || newSubject == subject ||
		l.count(`SELECT count(*) FROM pii_subject WHERE organization_id = $1 AND surrogate_id = $2 AND kind = 'value'`, f.OrgA, newSubject) != 1 {
		t.Errorf("signer of the return = %q, want a new subject of the kind value", newSubject)
	}
	if n := l.count(`SELECT count(*) FROM document d WHERE d.organization_id = $1 AND row_to_json(d)::text ~ 'Klara|Lena'`, f.OrgA) +
		l.count(`SELECT count(*) FROM audit_log a WHERE a.organization_id = $1 AND row_to_json(a)::text ~ 'Klara|Lena|`+pngPrefix+`'`, f.OrgA) +
		l.count(`SELECT count(*) FROM assignment_signature_legacy s WHERE s.organization_id = $1 AND s.payload::text ~ 'Klara'`, f.OrgA) +
		l.count(`SELECT count(*) FROM pii_subject WHERE encode(encrypted_payload, 'escape') ~ 'Klara'`); n != 0 {
		t.Errorf("plaintext names or images in %d rows", n)
	}

	// A second run changes nothing; the skipped rows are counted again.
	docs, audits, blobs := documents(), l.count(`SELECT count(*) FROM audit_log WHERE action = $1`, assignment.ActionSignatureMigrated), l.blobCount()
	res, err = run.RunOrg(bg, f.OrgA)
	if err != nil || res.Migrated != 0 || !reflect.DeepEqual(res.Reasons, wantSkipped) {
		t.Fatalf("second run: %+v, %v", res, err)
	}
	if !reflect.DeepEqual(l.state(), after) || documents() != docs || l.blobCount() != blobs ||
		l.count(`SELECT count(*) FROM audit_log WHERE action = $1`, assignment.ActionSignatureMigrated) != audits {
		t.Error("the second run changed something")
	}

	// Over the storage limit the row stays and no blob is kept.
	limit := int64(0)
	overLimit := l.legacy(l.movement(), "checkout", map[string]any{"image_base64": base64.StdEncoding.EncodeToString(checkoutPNG)})
	res, err = assignment.NewLegacySignatures(f.App, l.blobs).WithLimits(&features{limit: &limit}).RunOrg(bg, f.OrgA)
	if err != nil || res.Migrated != 0 || res.Reasons[assignment.LegacySkipStorageLimit] != 1 {
		t.Fatalf("run over the limit: %+v, %v", res, err)
	}
	if l.count(`SELECT count(*) FROM assignment_signature_legacy WHERE id = $1 AND payload ? 'document_id'`, overLimit) != 0 ||
		l.blobCount() != blobs {
		t.Error("the row over the limit changed or left a blob")
	}

	// The guard: no DELETE, no changed payload_sha256, no image in payload,
	// no second replacement.
	for name, stmt := range map[string]string{
		"DELETE":         `DELETE FROM assignment_signature_legacy WHERE id = $1`,
		"UPDATE hash":    `UPDATE assignment_signature_legacy SET payload_sha256 = repeat('0', 64) WHERE id = $1`,
		"UPDATE image":   `UPDATE assignment_signature_legacy SET payload = '{"image_base64": "aGVsbG8="}' WHERE id = $1`,
		"UPDATE smuggle": `UPDATE assignment_signature_legacy SET payload = jsonb_build_object('document_id', gen_random_uuid()::text, 'signer_surrogate', NULL, 'signed_at', NULL, 'method', repeat('A', 100)) WHERE id = $1`,
	} {
		if _, err = f.Admin.Exec(bg, stmt, missing); err == nil {
			t.Errorf("%s on assignment_signature_legacy succeeded", name)
		}
	}
	if _, err = f.Admin.Exec(bg, `UPDATE assignment_signature_legacy SET payload = jsonb_build_object('document_id', gen_random_uuid()::text,
			'signer_surrogate', NULL, 'signed_at', NULL, 'method', NULL) WHERE id = $1`, checkout); err == nil {
		t.Error("a taken over payload was replaced again")
	}

	// The verification answers legacy with valid null, for the handover
	// signature; client 2 sees neither the movement nor the link.
	w, v := l.verify(l.org(), signed)
	checkoutDoc := l.migrated(checkout)["document_id"]
	if w.Code != http.StatusOK || !v.Legacy || v.Valid != nil || v.DocumentID != checkoutDoc || v.SignerSurrogate != subject ||
		v.SignatureHash != "" || v.Version != 1 || v.ScanStatus != document.ScanPending ||
		!v.SignedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("legacy verification: %d %s", w.Code, w.Body.String())
	}
	if n := l.count(`SELECT count(*) FROM audit_log WHERE action = $1 AND resource_id = $2 AND changes->'valid' = 'null'::jsonb`,
		assignment.ActionSignatureVerified, signed); n != 1 {
		t.Errorf("verification audits with valid null: %d", n)
	}
	if w, _ = l.verify(l.client(f.Client2), signed); w.Code != http.StatusNotFound {
		t.Errorf("client 2: %d %s, want 404", w.Code, w.Body.String())
	}
	if w, _ = l.verify(l.client(f.Client1), signed); w.Code != http.StatusOK {
		t.Errorf("client 1: %d %s, want 200", w.Code, w.Body.String())
	}
	if w, _ = l.verify(l.org(), l.movement()); w.Code != http.StatusNotFound {
		t.Errorf("movement without signature: %d, want 404", w.Code)
	}
	visibleLinks := func(ctx context.Context) int {
		var n int
		if lerr := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM document_link WHERE entity_type = 'stock_movement' AND entity_id = $1`, signed).Scan(&n)
		}); lerr != nil {
			t.Fatal(lerr)
		}
		return n
	}
	if a, b := visibleLinks(f.ClientCtx(f.Client1)), visibleLinks(f.ClientCtx(f.Client2)); a != 2 || b != 0 {
		t.Errorf("links visible to client 1: %d (want 2), client 2: %d (want 0)", a, b)
	}
}
