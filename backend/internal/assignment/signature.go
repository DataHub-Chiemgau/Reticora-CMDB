package assignment

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// Limits of a signature (AST-02, WP-276): the decoded PNG holds at most
// 512 KiB, its base64 at most 700 KiB (maxLength of image_png_base64) and
// the whole request body less than 1 MiB.
const (
	maxSignatureBytes  = 512 << 10
	maxSignatureBase64 = 716800
	// MaxSignedBody bounds the body of assign, return and transfer
	// (http.MaxBytesReader before the JSON is read).
	MaxSignedBody = 1 << 20
)

// signatureMime is the only type of a signature image (E-51: the PNG of the
// canvas).
const signatureMime = "image/png"

var (
	// ErrSignatureTooLarge: the signature image exceeds 512 KiB (413).
	ErrSignatureTooLarge = errors.New("the signature image exceeds 512 KiB")
	// ErrSignatureUnavailable: no blob storage for signature images (503).
	ErrSignatureUnavailable = errors.New("signatures need the document storage")
	// ErrContactNotFound: the contact of signer.contact_id is not visible
	// (404).
	ErrContactNotFound = errors.New("contact not found")
)

// legacySignatureFields are the fields of the former signature schema
// (migration 000047), refused with 422 on the field.
var legacySignatureFields = []string{"image_base64", "object_key", "signer_name", "signed_at", "method"}

// signatureFields are the body fields holding a SignatureInput.
var signatureFields = []string{"signature", "return_signature"}

// SignatureStorage is the document side of the signatures: the storage in
// use of the organization (document.Repository.StorageUsage), checked
// before the blob is stored.
type SignatureStorage interface {
	StorageUsage(ctx context.Context, orgID string) (int64, error)
}

// signatures holds the ports of the signature path (WithSignatures).
type signatures struct {
	features entitlement.Checker
	limits   document.StorageLimiter
	usage    SignatureStorage
	blobs    blob.Store
	scans    document.ScanSubmitter
}

// WithSignatures enables signatures on assign, return and transfer
// (WP-276): features decides the feature signature (fail-closed: without
// it every signature is refused with 403 feature-not-entitled), and when it
// also resolves the storage limit (*entitlement.Service) the image counts
// against max_storage_mb; usage reads the storage in use, blobs stores the
// image and scans submits its malware scan after the commit (nil: the
// version stays pending_scan for the sweep).
func (s *Service) WithSignatures(features entitlement.Checker, usage SignatureStorage, blobs blob.Store, scans document.ScanSubmitter) *Service {
	sig := &signatures{usage: usage, blobs: blobs, scans: scans}
	if svc, ok := features.(*entitlement.Service); !ok || svc != nil {
		sig.features = features
		if limits, isLimiter := features.(document.StorageLimiter); isLimiter {
			sig.limits = limits
		}
	}
	if store, ok := blobs.(*blob.FileStore); ok && store == nil {
		sig.blobs = nil
	}
	s.signatures = sig
	return s
}

// pendingSignature is a checked signature image on its way into a movement:
// the ids chosen in the application (K20), the PNG and its checksum, the
// storage limit and the signer of the request.
type pendingSignature struct {
	field                             string
	signer                            *SignerInput
	png                               []byte
	checksum                          string
	movementID, documentID, versionID string
	key                               string
	limit                             *int64
	stored                            bool
}

// newID returns a random UUID (v4) like newMergeID (ci/merge.go).
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// fieldError is a 422 field error.
func fieldError(field, message string) error {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// prepareSignature checks the signature in of field before any side effect:
// the feature signature (403 feature-not-entitled, fail-closed without
// checker), the blob storage, the image (base64, at most 512 KiB, else
// ErrSignatureTooLarge; a PNG by its content signature, else
// document.ErrMimeMismatch), the form of the signer and, without binding,
// the storage limit (403 entitlement-limit). Nil in is no signature.
func (s *Service) prepareSignature(ctx context.Context, orgID, field string, in *SignatureInput) (*pendingSignature, error) {
	if in == nil {
		return nil, nil
	}
	sig := s.signatures
	if sig == nil || sig.features == nil || !sig.features.IsEnabled(ctx, orgID, entitlement.FeatureSignature) {
		return nil, &entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureSignature}
	}
	if sig.blobs == nil {
		return nil, ErrSignatureUnavailable
	}
	png, err := decodeImage(field, in.ImagePNGBase64)
	if err != nil {
		return nil, err
	}
	if signerErr := checkSigner(field, in.Signer); signerErr != nil {
		return nil, signerErr
	}
	p := &pendingSignature{field: field, signer: in.Signer, png: png,
		movementID: newID(), documentID: newID(), versionID: newID()}
	sum := sha256.Sum256(png)
	p.checksum = hex.EncodeToString(sum[:])
	p.key = blob.OrgKey(orgID, "documents", p.documentID, p.versionID)
	if sig.limits != nil {
		if p.limit, err = sig.limits.StorageLimitBytes(ctx, orgID); err != nil {
			return nil, err
		}
	}
	if p.limit != nil && sig.usage != nil {
		used, usageErr := sig.usage.StorageUsage(ctx, orgID)
		if usageErr != nil {
			return nil, usageErr
		}
		if used+int64(len(png)) > *p.limit {
			return nil, &entitlement.LimitExceededError{FeatureKey: entitlement.LimitMaxStorageMB, Limit: *p.limit, Current: used}
		}
	}
	return p, nil
}

// decodeImage decodes the base64 PNG of field: at most 700 KiB of base64
// and 512 KiB of image (ErrSignatureTooLarge), valid base64 (422) and a PNG
// by content (document.VerifyContent, document.ErrMimeMismatch).
func decodeImage(field, encoded string) ([]byte, error) {
	name := field + ".image_png_base64"
	if encoded == "" {
		return nil, fieldError(name, "image_png_base64 is required")
	}
	if len(encoded) > maxSignatureBase64 {
		return nil, ErrSignatureTooLarge
	}
	png, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fieldError(name, "must be base64")
	}
	if len(png) > maxSignatureBytes {
		return nil, ErrSignatureTooLarge
	}
	if mimeErr := document.VerifyContent(signatureMime, png); mimeErr != nil {
		return nil, mimeErr
	}
	return png, nil
}

// checkSigner checks the form of the signer: exactly one of user_id,
// contact_id and name, the ids as UUIDs.
func checkSigner(field string, s *SignerInput) error {
	if s == nil {
		return nil
	}
	name := field + ".signer"
	n := 0
	for _, v := range []string{s.UserID, s.ContactID, strings.TrimSpace(s.Name)} {
		if v != "" {
			n++
		}
	}
	switch {
	case n != 1:
		return fieldError(name, "exactly one of user_id, contact_id and name is required")
	case s.UserID != "" && !api.IsUUID(s.UserID):
		return fieldError(name+".user_id", "must be a UUID")
	case s.ContactID != "" && !api.IsUUID(s.ContactID):
		return fieldError(name+".contact_id", "must be a UUID")
	}
	return nil
}

// store puts the images of pending before the transaction (K20), under a
// key of their own per version.
func (s *Service) store(ctx context.Context, pending []*pendingSignature) error {
	for _, p := range pending {
		if p == nil {
			continue
		}
		if err := s.signatures.blobs.Put(ctx, document.Bucket, p.key, bytes.NewReader(p.png), int64(len(p.png)), signatureMime); err != nil {
			return fmt.Errorf("store signature image: %w", err)
		}
		p.stored = true
	}
	return nil
}

// discard deletes the stored images of pending again: the transaction
// failed or was rolled back.
func (s *Service) discard(ctx context.Context, pending []*pendingSignature) {
	for _, p := range pending {
		if p != nil && p.stored {
			_ = s.signatures.blobs.Delete(context.WithoutCancel(ctx), document.Bucket, p.key)
		}
	}
}

// sign binds p to the movement m in tx before it is booked (WP-276): it
// resolves the signer (without one the holder; a team holder needs one; a
// user of the organization; the surrogate of a visible contact; a new
// vault subject of the kind person for a name), inserts the document of the
// category signature with version 1 (usage against max_storage_mb), sets
// id, occurred_at, document_id and the signer of m and computes the
// signature_hash. holder is the holder of the movement. Nil p signs
// nothing.
func (s *Service) sign(ctx context.Context, tx pgx.Tx, p *pendingSignature, m *movement.Movement, holder Holder, at time.Time) (*document.Version, error) {
	if p == nil {
		return nil, nil
	}
	if err := resolveSigner(ctx, tx, p, m, holder); err != nil {
		return nil, err
	}
	d := &document.Document{ID: p.documentID, OrganizationID: m.OrganizationID,
		Title: "Signature " + m.MovementType + " " + p.movementID, FileName: "signature-" + p.movementID + ".png",
		Category: document.CategorySignature, UploadedBy: m.ActorID}
	checksum := p.checksum
	v := &document.Version{ID: p.versionID, StorageKey: p.key, Size: int64(len(p.png)), MimeType: signatureMime,
		ChecksumSHA256: &checksum, UploadedBy: m.ActorID}
	if err := document.CreateWithFirstVersionTx(ctx, tx, d, v, p.limit); err != nil {
		return nil, err
	}
	m.ID, m.OccurredAt, m.DocumentID = p.movementID, movement.SignatureTime(at), p.documentID
	m.SignatureHash = movement.SignatureHash(p.png, m)
	return v, nil
}

// resolveSigner sets the signer of m from p.signer or the holder.
func resolveSigner(ctx context.Context, tx pgx.Tx, p *pendingSignature, m *movement.Movement, holder Holder) error {
	name := p.field + ".signer"
	s := p.signer
	switch {
	case s == nil && holder.UserID != "":
		m.SignerUserID = holder.UserID
	case s == nil:
		return fieldError(name, "signer is required when a team holds the asset")
	case s.UserID != "":
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1 AND organization_id = $2)`,
			s.UserID, m.OrganizationID).Scan(&ok); err != nil {
			return fmt.Errorf("check signer: %w", err)
		}
		if !ok {
			return fieldError(name+".user_id", "user not found")
		}
		m.SignerUserID = s.UserID
	case s.ContactID != "":
		var subject string
		err := tx.QueryRow(ctx, `SELECT COALESCE(pii_subject_id::text, '') FROM contact WHERE id = $1 AND organization_id = $2`,
			s.ContactID, m.OrganizationID).Scan(&subject)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrContactNotFound
		}
		if err != nil {
			return fmt.Errorf("check signer contact: %w", err)
		}
		if subject == "" {
			return fieldError(name+".contact_id", "the contact has no pseudonymized personal data")
		}
		m.SignerSurrogate = subject
	default:
		vault := pii.Default()
		if vault == nil {
			return fieldError(name+".name", "external signers need the personal data vault")
		}
		subject, err := vault.CreateTx(ctx, tx, m.OrganizationID, pii.KindPerson, pii.Data{"name": strings.TrimSpace(s.Name)})
		if err != nil {
			return fmt.Errorf("store signer: %w", err)
		}
		m.SignerSurrogate = subject
	}
	return nil
}

// bind links the document of the booked, signed movement m to it (entity
// type stock_movement, so derive_document_link_scope takes client and site
// of the movement) and submits the scan of version 1 after the commit.
func (s *Service) bind(ctx context.Context, tx pgx.Tx, m *movement.Movement, v *document.Version) error {
	if v == nil {
		return nil
	}
	if err := document.LinkTx(ctx, tx, &document.DocumentLink{OrganizationID: m.OrganizationID, DocumentID: m.DocumentID,
		EntityType: "stock_movement", EntityID: m.ID}); err != nil {
		return fmt.Errorf("link signature: %w", err)
	}
	document.EnqueueScan(ctx, s.signatures.scans, v)
	return nil
}

// transactionTime is now() of tx: occurred_at of every movement of the
// transaction.
func transactionTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("transaction time: %w", err)
	}
	return at.UTC(), nil
}

// readSignedBody reads the body of assign, return or transfer into v: at
// most MaxSignedBody bytes (*http.MaxBytesError), the legacy signature
// fields refused with a 422 field error before the strict decoding (which
// would answer 400 for them), then api.ReadJSON's rules (unknown fields
// refused).
// validation: the decoding errors of the request body (like api.ReadJSON)
// and fixed field errors.
func readSignedBody(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxSignedBody))
	_ = r.Body.Close()
	if err != nil {
		return err
	}
	if legacyErr := refuseLegacySignature(body); legacyErr != nil {
		return legacyErr
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// refuseLegacySignature returns a 422 field error for a field of the former
// signature schema in signature or return_signature; a body that is no
// JSON object is left to the strict decoding.
func refuseLegacySignature(body []byte) error {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return nil
	}
	for _, field := range signatureFields {
		raw, ok := top[field]
		if !ok {
			continue
		}
		var sig map[string]json.RawMessage
		if json.Unmarshal(raw, &sig) != nil {
			continue
		}
		for _, legacy := range legacySignatureFields {
			if _, found := sig[legacy]; found {
				return fieldError(field+"."+legacy, legacy+" is no longer accepted; send image_png_base64 and signer")
			}
		}
	}
	return nil
}
