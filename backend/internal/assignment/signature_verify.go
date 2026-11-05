package assignment

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// ActionSignatureVerified is the audit action of a verification (WP-748):
// resource stock_movement, changes document_id, valid and scan_status.
const ActionSignatureVerified = "stock_movement.signature_verified"

var (
	// ErrMovementNotFound: the movement is unknown or not visible under
	// the caller's scope (404).
	ErrMovementNotFound = errors.New("movement not found")
	// ErrNotSigned: the movement carries no signature (404).
	ErrNotSigned = errors.New("the movement carries no signature")
	// errSignatureQuarantined: version 1 of the signature document is
	// infected (409 document-quarantined).
	errSignatureQuarantined = errors.New("the signature image is quarantined")
	// errSignatureNotScanned: the scan of version 1 failed (409
	// document-pending-scan).
	errSignatureNotScanned = errors.New("the signature image could not be scanned")
)

// SignatureVerification is the answer of GET
// /api/v1/stock-movements/{id}/signature (AST-02, E-51, WP-748): the
// signature reference of the movement, version 1 of its document with the
// scan status and the result. Valid is true only when the stored image
// matches both checksum_sha256 of the version and the signature_hash of
// the movement; nil stands for a legacy signature without a verifiable
// image (Legacy, WP-749: taken over from 000047 by signature.migrate,
// document, signer surrogate and signed_at from
// assignment_signature_legacy, no signature_hash). It never holds the
// image or a name.
type SignatureVerification struct {
	MovementID      string    `json:"movement_id"`
	DocumentID      string    `json:"document_id"`
	Version         int       `json:"version"`
	SignatureHash   string    `json:"signature_hash"`
	SignerUserID    string    `json:"signer_user_id,omitempty"`
	SignerSurrogate string    `json:"signer_surrogate,omitempty"`
	SignedAt        time.Time `json:"signed_at"`
	ScanStatus      string    `json:"scan_status"`
	Valid           *bool     `json:"valid"`
	Legacy          bool      `json:"legacy"`
}

// signedVersion is the signed movement with version 1 of its document as
// the verification reads it.
type signedVersion struct {
	m          movement.Movement
	version    int
	key        string
	checksum   string
	scanStatus string
	// legacy marks a signature taken over by signature.migrate (WP-749);
	// signedAt is then its signed_at (nil: occurred_at of the movement).
	legacy   bool
	signedAt *time.Time
}

// VerifySignature verifies the signature of the movement id (WP-748): it
// reads the movement and version 1 of its signature document under the
// caller's tenant scope (not visible: ErrMovementNotFound; unsigned:
// ErrNotSigned), refuses an infected version (409 document-quarantined)
// and a failed scan (409 document-pending-scan), loads the image through
// blob.Store.Get outside a transaction and compares its SHA-256 with
// checksum_sha256 and the recomputed movement.SignatureHash with
// signature_hash. A version still pending_scan is verified as well. Every
// verification writes stock_movement.signature_verified in a short
// transaction after the check (database.BeforeCommit).
func (s *Service) VerifySignature(ctx context.Context, orgID, id string) (*SignatureVerification, error) {
	if !api.IsUUID(id) {
		return nil, ErrMovementNotFound
	}
	if s.signatures == nil || s.signatures.blobs == nil {
		return nil, ErrSignatureUnavailable
	}
	var sv *signedVersion
	if err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var readErr error
		sv, readErr = readSignedVersion(ctx, tx, orgID, id)
		return readErr
	}); err != nil {
		return nil, err
	}
	switch sv.scanStatus {
	case document.ScanInfected:
		return nil, errSignatureQuarantined
	case document.ScanFailed:
		return nil, errSignatureNotScanned
	}
	out := &SignatureVerification{MovementID: sv.m.ID, DocumentID: sv.m.DocumentID, Version: sv.version,
		SignatureHash: sv.m.SignatureHash, SignerUserID: sv.m.SignerUserID, SignerSurrogate: sv.m.SignerSurrogate,
		SignedAt: sv.m.OccurredAt, ScanStatus: sv.scanStatus, Legacy: sv.legacy}
	if sv.legacy {
		// No hash covers a legacy image: valid stays null.
		if sv.signedAt != nil {
			out.SignedAt = *sv.signedAt
		}
	} else {
		valid, err := s.matches(ctx, orgID, sv)
		if err != nil {
			return nil, err
		}
		out.Valid = &valid
	}
	if auditErr := s.recordVerification(ctx, orgID, out); auditErr != nil {
		return nil, auditErr
	}
	return out, nil
}

// readSignedVersion reads the movement id with the ids its hash covers
// and version 1 of its document; the policies of stock_movement,
// document and document_version filter the caller's scope.
func readSignedVersion(ctx context.Context, tx pgx.Tx, orgID, id string) (*signedVersion, error) {
	sv := &signedVersion{}
	m := &sv.m
	var version *int
	err := tx.QueryRow(ctx, `SELECT m.id::text, m.organization_id::text, COALESCE(m.asset_id::text, ''), m.movement_type,
			COALESCE(m.assignee_user_id::text, ''), COALESCE(m.assignee_team_id::text, ''),
			COALESCE(m.signer_user_id::text, ''), COALESCE(m.signer_surrogate::text, ''), COALESCE(m.actor_id::text, ''),
			m.occurred_at, COALESCE(m.document_id::text, ''), COALESCE(m.signature_hash, ''),
			v.version, COALESCE(v.storage_key, ''), COALESCE(v.checksum_sha256, ''), COALESCE(v.scan_status, '')
		FROM stock_movement m
		LEFT JOIN document d ON d.id = m.document_id AND d.organization_id = m.organization_id
		LEFT JOIN document_version v ON v.document_id = d.id AND v.version = 1
		WHERE m.id = $1 AND m.organization_id = $2`, id, orgID).
		Scan(&m.ID, &m.OrganizationID, &m.AssetID, &m.MovementType, &m.AssigneeUserID, &m.AssigneeTeamID,
			&m.SignerUserID, &m.SignerSurrogate, &m.ActorID, &m.OccurredAt, &m.DocumentID, &m.SignatureHash,
			&version, &sv.key, &sv.checksum, &sv.scanStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMovementNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read signed movement: %w", err)
	}
	m.OccurredAt = movement.SignatureTime(m.OccurredAt)
	if m.SignatureHash == "" && m.DocumentID == "" {
		return readLegacyVersion(ctx, tx, orgID, sv)
	}
	if m.SignatureHash == "" || m.DocumentID == "" || version == nil {
		return nil, ErrNotSigned
	}
	sv.version = *version
	return sv, nil
}

// readLegacyVersion completes sv from the legacy signature of its movement
// taken over by signature.migrate (WP-749): document, signer surrogate and
// signed_at from the image-free payload of assignment_signature_legacy
// (the handover signature before the return signature of the same
// assignment) and version 1 of the document. A movement without one is
// unsigned (ErrNotSigned).
func readLegacyVersion(ctx context.Context, tx pgx.Tx, orgID string, sv *signedVersion) (*signedVersion, error) {
	var version *int
	var signedAt string
	err := tx.QueryRow(ctx, `SELECT l.payload ->> 'document_id', COALESCE(l.payload ->> 'signer_surrogate', ''),
			COALESCE(l.payload ->> 'signed_at', ''), v.version, COALESCE(v.storage_key, ''), COALESCE(v.checksum_sha256, ''),
			COALESCE(v.scan_status, '')
		FROM assignment_signature_legacy l
		JOIN document d ON d.id = (l.payload ->> 'document_id')::uuid AND d.organization_id = l.organization_id
		LEFT JOIN document_version v ON v.document_id = d.id AND v.version = 1
		WHERE l.movement_id = $1 AND l.organization_id = $2 AND l.payload ? 'document_id'
		ORDER BY l.kind = 'checkout' DESC
		LIMIT 1`, sv.m.ID, orgID).
		Scan(&sv.m.DocumentID, &sv.m.SignerSurrogate, &signedAt, &version, &sv.key, &sv.checksum, &sv.scanStatus)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && version == nil) {
		return nil, ErrNotSigned
	}
	if err != nil {
		return nil, fmt.Errorf("read legacy signature: %w", err)
	}
	if at, parseErr := time.Parse(time.RFC3339Nano, signedAt); parseErr == nil {
		at = at.UTC()
		sv.signedAt = &at
	}
	sv.version, sv.legacy = *version, true
	return sv, nil
}

// matches loads the image of sv outside a transaction and reports whether
// it matches checksum_sha256 and signature_hash. A key outside the
// document's prefix or a missing blob is a broken signature (false); any
// other storage error fails the verification.
func (s *Service) matches(ctx context.Context, orgID string, sv *signedVersion) (bool, error) {
	if sv.key == "" || sv.checksum == "" || !document.OwnsStorageKey(orgID, sv.m.DocumentID, sv.key) {
		return false, nil
	}
	rc, err := s.signatures.blobs.Get(ctx, document.Bucket, sv.key)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load signature image: %w", err)
	}
	// One byte above the limit is enough to fail the checksum.
	png, err := io.ReadAll(io.LimitReader(rc, maxSignatureBytes+1))
	_ = rc.Close()
	if err != nil {
		return false, fmt.Errorf("read signature image: %w", err)
	}
	sum := sha256.Sum256(png)
	checksumOK := subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(sv.checksum)) == 1
	hashOK := subtle.ConstantTimeCompare([]byte(movement.SignatureHash(png, &sv.m)), []byte(sv.m.SignatureHash)) == 1
	return checksumOK && hashOK, nil
}

// recordVerification writes stock_movement.signature_verified for out in
// a short transaction of the request's tenant scope, last before its
// commit (WP-334); the entry holds no image.
func (s *Service) recordVerification(ctx context.Context, orgID string, out *SignatureVerification) error {
	entry := audit.Entry{OrganizationID: orgID, ActorType: "system", Action: ActionSignatureVerified,
		ResourceType: "stock_movement", ResourceID: out.MovementID,
		Changes: map[string]any{"document_id": out.DocumentID, "valid": out.Valid, "scan_status": out.ScanStatus}}
	if user := tenant.FromContext(ctx).UserID; user != "" {
		entry.ActorType = "user"
		if audit.IsUUID(user) {
			entry.ActorID = user
		}
	}
	return s.inTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return database.BeforeCommit(ctx, func() error {
			if _, err := audit.NewPGRecorder().Record(ctx, tx, entry); err != nil {
				return fmt.Errorf("audit %s: %w", ActionSignatureVerified, err)
			}
			return nil
		})
	})
}

// VerifySignature handles GET /api/v1/stock-movements/{id}/signature
// (WP-748): 200 with the verification; an invisible or unsigned movement
// 404, an infected image 409 document-quarantined, a failed scan 409
// document-pending-scan; without the service (profile without database)
// 501, without blob storage 503.
func (h *Handler) VerifySignature(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if !h.writable(w, t) {
		return
	}
	out, err := h.service.VerifySignature(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	switch {
	case err == nil:
		api.WriteJSON(w, http.StatusOK, out)
	case errors.Is(err, ErrMovementNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "movement not found")
	case errors.Is(err, ErrNotSigned):
		api.WriteError(w, http.StatusNotFound, "Not Found", "the movement carries no signature")
	case errors.Is(err, errSignatureQuarantined):
		api.WriteProblem(w, http.StatusConflict, document.ProblemQuarantined, "Document Quarantined",
			"the signature image was found infected and is quarantined")
	case errors.Is(err, errSignatureNotScanned):
		api.WriteProblem(w, http.StatusConflict, document.ProblemPendingScan, "Document Pending Scan",
			"the signature image has not been scanned clean")
	case errors.Is(err, ErrSignatureUnavailable):
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", ErrSignatureUnavailable.Error())
	default:
		api.WriteRepoError(w, err)
	}
}
